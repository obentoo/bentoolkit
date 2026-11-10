package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/obentoo/bentoolkit/internal/autoupdate"
	"github.com/obentoo/bentoolkit/internal/autoupdate/cooldown"
	"github.com/obentoo/bentoolkit/internal/autoupdate/ebuilds"
	"github.com/obentoo/bentoolkit/internal/autoupdate/registry"
	"github.com/obentoo/bentoolkit/internal/common/ebuild"
	"github.com/obentoo/bentoolkit/internal/common/filelock"
	"github.com/obentoo/bentoolkit/internal/common/logging"
	"github.com/spf13/cobra"
)

// errCooldownDisabled answers a writing subcommand on an overlay without a
// cooldown block: there is nothing to confirm, add to or sweep.
var errCooldownDisabled = errors.New("cooldown disabled for this overlay: profiles/package.mask has no `# BEGIN bentoo-cooldown` block")

// newCooldownCmd builds `overlay cooldown` and its four subcommands.
func newCooldownCmd(d *deps) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "cooldown",
		Short: "Inspect and drive the autoupdate cooldown",
		Long: `The autoupdate cooldown keeps every version the autoupdate promotes masked in
profiles/package.mask (the managed bentoo-cooldown block) until it is released:
by time (days= on the BEGIN line), by installation from ::bentoo, or by hand.
.autoupdate/cooldown.unmask lists the same atoms for a host that wants them now.`,
	}
	cmd.AddCommand(newCooldownStatusCmd(d), newCooldownConfirmCmd(), newCooldownAddCmd(), newCooldownSweepCmd(d))
	return cmd
}

// cooldownStatusRow is one object of `status --json`: the tray's contract.
// Exactly these seven keys.
type cooldownStatusRow struct {
	Package   string   `json:"package"`
	Version   string   `json:"version"`
	Since     string   `json:"since"`
	Eligible  string   `json:"eligible"`
	Group     []string `json:"group"`
	Origin    string   `json:"origin"`
	Installed *bool    `json:"installed"`
}

func newCooldownStatusCmd(d *deps) *cobra.Command {
	var asJSON bool
	pkgdbRoot := d.cooldownPkgdbRoot
	cmd := &cobra.Command{
		Use:   "status",
		Short: "List the versions under cooldown (read-only, takes no lock)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx := commandContext(cmd)
			log := logging.FromContext(ctx)
			overlayPath, err := cooldownOverlay(cmd)
			if err != nil {
				return err
			}
			block, err := cooldown.Load(overlayPath)
			if err != nil {
				log.Error("cooldown status: the block cannot be read", "err", err)
				return failWith(1, err)
			}
			if block == nil {
				fmt.Println("cooldown disabled for this overlay")
				return nil
			}
			rows := cooldownStatusRows(ctx, log, block, pkgdbRoot)
			if asJSON {
				return printCooldownJSON(rows)
			}
			if err := printCooldownTable(rows); err != nil {
				return failWith(1, fmt.Errorf("printing the cooldown status: %w", err))
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "print a JSON array of {package, version, since, eligible, group, origin, installed}")
	cmd.Flags().StringVar(&pkgdbRoot, "pkgdb-root", pkgdbRoot, "system root whose var/db/pkg answers `installed`")
	return cmd
}

// cooldownStatusRows builds one row per entry. installed is nil for every row
// when the installed-package database cannot be read.
func cooldownStatusRows(ctx context.Context, log *slog.Logger, block *cooldown.Block, pkgdbRoot string) []cooldownStatusRow {
	inst, err := cooldown.NewPkgdbInstalledContext(ctx, pkgdbRoot)
	if err != nil {
		log.Warn("cooldown status: installed state unknown", "root", pkgdbRoot, "err", err)
		inst = nil
	}
	rows := make([]cooldownStatusRow, 0, len(block.Entries))
	for _, e := range block.Entries {
		row := cooldownStatusRow{
			Package:  e.Package,
			Version:  e.Version,
			Since:    e.Since.UTC().Format(time.RFC3339),
			Eligible: block.EntryEligible(e).UTC().Format(time.RFC3339),
			Group:    []string{},
			Origin:   e.Origin,
		}
		if e.Group != "" {
			row.Group = strings.Split(e.Group, ",")
		}
		if inst != nil {
			v, ok := inst.HighestFromRepo(e.Package, cooldown.Repo)
			installed := ok && ebuild.CompareVersions(v, e.Version) >= 0
			row.Installed = &installed
		}
		rows = append(rows, row)
	}
	return rows
}

func printCooldownJSON(rows []cooldownStatusRow) error {
	out, err := json.MarshalIndent(rows, "", "  ")
	if err != nil {
		return failWith(1, fmt.Errorf("encoding the cooldown status: %w", err))
	}
	fmt.Println(string(out))
	return nil
}

func printCooldownTable(rows []cooldownStatusRow) error {
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	if _, err := fmt.Fprintln(w, "PACKAGE\tVERSION\tSINCE\tELIGIBLE\tGROUP\tORIGIN\tINSTALLED"); err != nil {
		return err
	}
	for _, r := range rows {
		installed := "?"
		if r.Installed != nil {
			installed = fmt.Sprint(*r.Installed)
		}
		group := strings.Join(r.Group, ",")
		if group == "" {
			group = "-"
		}
		if _, err := fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\t%s\n", r.Package, r.Version, r.Since, r.Eligible, group, r.Origin, installed); err != nil {
			return err
		}
	}
	return w.Flush()
}

func newCooldownConfirmCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "confirm =cat/pkg-N...",
		Short: "Release a version and every older one of the package (a whole group for a grouped package)",
		Args:  cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			confirms := make([]cooldown.Confirm, 0, len(args))
			for _, a := range args {
				c, err := cooldown.ParseConfirm(a)
				if err != nil {
					return failWith(1, fmt.Errorf("cooldown confirm: %w", err))
				}
				confirms = append(confirms, c)
			}
			return withCooldownBlock(cmd, func(log *slog.Logger, overlayPath string, block *cooldown.Block) error {
				lifts, err := cooldown.ConfirmLift(block, confirms)
				if err != nil {
					return failWith(1, fmt.Errorf("cooldown confirm: %w", err))
				}
				if err := block.Save(overlayPath); err != nil {
					return failWith(1, fmt.Errorf("cooldown confirm: %w", err))
				}
				logCooldownLifts(log, lifts)
				printCooldownLifts(lifts)
				return nil
			})
		},
	}
}

func newCooldownAddCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "add =cat/pkg-PVR",
		Short: "Mask a version present in the overlay under the cooldown, by hand",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			pkg, version, err := cooldown.ParseAtom(args[0])
			if err != nil {
				return failWith(1, fmt.Errorf("cooldown add: %w", err))
			}
			return withCooldownBlock(cmd, func(log *slog.Logger, overlayPath string, block *cooldown.Block) error {
				if !ebuildExistsFn(overlayPath)(pkg, version) {
					return failWith(1, fmt.Errorf("cooldown add: %s has no ebuild in %s", args[0], overlayPath))
				}
				e := cooldown.Entry{
					Package: pkg, Version: version, Origin: cooldown.OriginManual,
					Since: time.Now().UTC().Truncate(time.Second),
					Group: recordCooldownGroup(log, overlayPath, pkg),
				}
				if !block.Add(e) {
					fmt.Printf("%s is already under cooldown\n", e.Atom())
					return nil
				}
				if err := block.Save(overlayPath); err != nil {
					return failWith(1, fmt.Errorf("cooldown add: %w", err))
				}
				eligible := block.Eligible(pkg).UTC().Format(time.RFC3339)
				log.Info("cooldown: entry added", "package", pkg, "version", version, "origin", e.Origin, "eligible", eligible)
				fmt.Printf("masked  %s (eligible %s)\n", e.Atom(), eligible)
				return nil
			})
		},
	}
}

// recordCooldownGroup is the group the registry record of pkg declares
// (cooldown_with), or empty when there is no record or no registry.
func recordCooldownGroup(log *slog.Logger, overlayPath, pkg string) string {
	cfg, err := registry.LoadPackagesConfig(overlayPath)
	if err != nil {
		log.Debug("cooldown add: no usable packages.toml; the entry gets no group", "err", err)
		return ""
	}
	for key, rec := range cfg.Packages {
		category, name, ok := ebuilds.SplitPkgAtom(key)
		if ok && category+"/"+name == pkg && len(rec.CooldownWith) > 0 {
			return autoupdate.CooldownGroup(pkg, rec)
		}
	}
	return ""
}

func newCooldownSweepCmd(d *deps) *cobra.Command {
	var dryRun bool
	pkgdbRoot := d.cooldownPkgdbRoot
	cmd := &cobra.Command{
		Use:   "sweep",
		Short: "Release every entry that is due (elapsed, installed, stale), outside an apply",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx := commandContext(cmd)
			log := logging.FromContext(ctx)
			overlayPath, err := cooldownOverlay(cmd)
			if err != nil {
				return err
			}
			if !dryRun {
				release, err := takeCooldownLock(overlayPath)
				if err != nil {
					return err
				}
				defer release()
			}
			block, lifts, err := sweepCooldown(ctx, log, overlayPath, pkgdbRoot, nil)
			if err != nil {
				return failWith(1, fmt.Errorf("cooldown sweep: %w", err))
			}
			if block == nil {
				return failWith(1, errCooldownDisabled)
			}
			printCooldownLifts(lifts)
			if dryRun {
				return nil
			}
			if err := block.Save(overlayPath); err != nil {
				return failWith(1, fmt.Errorf("cooldown sweep: %w", err))
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "print what would be lifted and write nothing")
	cmd.Flags().StringVar(&pkgdbRoot, "pkgdb-root", pkgdbRoot, "system root whose var/db/pkg releases entries by installation")
	return cmd
}

func printCooldownLifts(lifts []cooldown.Lift) {
	if len(lifts) == 0 {
		fmt.Println("nothing to lift")
		return
	}
	for _, l := range lifts {
		fmt.Printf("lifted  %s (%s)\n", l.Atom(), l.Reason)
	}
}

// cooldownOverlay resolves the overlay the command works on.
func cooldownOverlay(cmd *cobra.Command) (string, error) {
	appCtx, err := loadAppContextNoValidation(cmd)
	if err != nil {
		return "", failWith(1, fmt.Errorf("loading the config: %w", err))
	}
	return appCtx.OverlayPath, nil
}

// takeCooldownLock takes the apply's overlay lock, waiting up to the
// lock's own timeout; a busy lock is an exit-1 error naming the lock and its
// holder.
func takeCooldownLock(overlayPath string) (release func(), err error) {
	lock, err := acquireOverlayLock(overlayPath)
	if err != nil {
		if errors.Is(err, filelock.ErrLocked) {
			return nil, failWith(1, fmt.Errorf("an apply holds the overlay: %w", err))
		}
		return nil, failWith(1, fmt.Errorf("cannot take the overlay lock: %w", err))
	}
	unregister := registerExitCleanup(lock.Release)
	return func() { unregister(); lock.Release() }, nil
}

// withCooldownBlock runs fn under the overlay lock with the overlay's block;
// an unreadable block or an overlay without one is an exit-1 error and fn is
// never called.
func withCooldownBlock(cmd *cobra.Command, fn func(log *slog.Logger, overlayPath string, block *cooldown.Block) error) error {
	log := logging.FromContext(commandContext(cmd))
	overlayPath, err := cooldownOverlay(cmd)
	if err != nil {
		return err
	}
	release, err := takeCooldownLock(overlayPath)
	if err != nil {
		return err
	}
	defer release()
	block, err := cooldown.Load(overlayPath)
	if err != nil {
		return failWith(1, err)
	}
	if block == nil {
		return failWith(1, errCooldownDisabled)
	}
	return fn(log, overlayPath, block)
}
