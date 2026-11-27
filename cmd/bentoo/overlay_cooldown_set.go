package main

import (
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"strings"

	"github.com/obentoo/bentoolkit/internal/autoupdate/cooldown"
	"github.com/obentoo/bentoolkit/internal/autoupdate/registry"
	"github.com/obentoo/bentoolkit/internal/common/i18n"
	"github.com/obentoo/bentoolkit/internal/common/logging"
	"github.com/obentoo/bentoolkit/internal/common/output"
	"github.com/spf13/cobra"
)

// `cooldown set` and `cooldown unset` write a record's cooldown override in
// packages.toml. registry.PlanCooldownEdit computes and verifies the rewrite;
// this file takes the overlay lock, shows what changes and writes it. Like
// every registry writer it only edits the file: committing and publishing stay
// with the overlay's own workflow.

// cooldownEditOpts are the flags the two subcommands share.
type cooldownEditOpts struct {
	allLines  bool
	dryRun    bool
	sweep     bool
	pkgdbRoot string
}

func (o *cooldownEditOpts) register(cmd *cobra.Command) {
	cmd.Flags().BoolVar(&o.allLines, "all-lines", false, i18n.N("let a bare cat/pkg stand for every record of the package (each @series and :slot)"))
	cmd.Flags().BoolVar(&o.dryRun, "dry-run", false, i18n.N("print the diff and write nothing"))
	cmd.Flags().BoolVar(&o.sweep, "sweep", false, i18n.N("run the cooldown sweep right after the write"))
	cmd.Flags().StringVar(&o.pkgdbRoot, "pkgdb-root", o.pkgdbRoot, i18n.N("system root whose var/db/pkg releases entries by installation (with --sweep)"))
}

func newCooldownSetCmd(d *deps) *cobra.Command {
	opts := cooldownEditOpts{pkgdbRoot: d.cooldownPkgdbRoot}
	var days int
	var reason string
	cmd := &cobra.Command{
		Use:   "set <record-key>... --days N --reason TEXT",
		Short: i18n.N("Set a registry record's cooldown_days and cooldown_reason"),
		Long: i18n.N(`Write cooldown_days and cooldown_reason on the packages.toml records named
(cat/pkg, cat/pkg@series or cat/pkg:slot), in place and keeping the file's
layout. --days 0 takes the package out of the cooldown; 1 to 3650 sizes its
wait. A record in a cooldown_with group brings every member of the group: a
group is released whole, so its members share one wait.

The change acts on the versions already masked at the next --apply or
'bentoo overlay cooldown sweep'.`),
		Args: cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if !cmd.Flags().Changed("days") {
				return failWith(1, errors.New("cooldown set: --days is required"))
			}
			if !cmd.Flags().Changed("reason") || strings.TrimSpace(reason) == "" {
				return failWith(1, errors.New("cooldown set: --reason is required: a record that does not wait the block's days says why"))
			}
			req := registry.CooldownRequest{Keys: args, AllLines: opts.allLines, Days: &days, Reason: reason}
			return runCooldownEdit(cmd, "set", opts, req)
		},
	}
	cmd.Flags().IntVar(&days, "days", 0, i18n.N("the record's wait in days: 0 (out of the cooldown) to 3650"))
	cmd.Flags().StringVar(&reason, "reason", "", i18n.N("why the package does not wait the block's days (required)"))
	opts.register(cmd)
	return cmd
}

func newCooldownUnsetCmd(d *deps) *cobra.Command {
	opts := cooldownEditOpts{pkgdbRoot: d.cooldownPkgdbRoot}
	cmd := &cobra.Command{
		Use:   "unset <record-key>...",
		Short: i18n.N("Drop a registry record's cooldown override: it waits the block's days again"),
		Long: i18n.N(`Remove cooldown_days and cooldown_reason from the packages.toml records named
(cat/pkg, cat/pkg@series or cat/pkg:slot), so they wait the days= of the
cooldown block again. A record in a cooldown_with group brings every member of
the group.

The change acts on the versions already masked at the next --apply or
'bentoo overlay cooldown sweep'.`),
		Args: cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runCooldownEdit(cmd, "unset", opts, registry.CooldownRequest{Keys: args, AllLines: opts.allLines})
		},
	}
	opts.register(cmd)
	return cmd
}

// runCooldownEdit is the shared body of set and unset: lock, plan, report,
// write, and the optional sweep.
func runCooldownEdit(cmd *cobra.Command, verb string, opts cooldownEditOpts, req registry.CooldownRequest) error {
	ctx := commandContext(cmd)
	log := logging.FromContext(ctx)
	if opts.dryRun && opts.sweep {
		return failWith(1, fmt.Errorf("cooldown %s: --sweep writes the cooldown block and cannot be combined with --dry-run", verb))
	}
	overlayPath, err := cooldownOverlay(cmd)
	if err != nil {
		return err
	}
	if !opts.dryRun {
		release, err := takeCooldownLock(overlayPath)
		if err != nil {
			return err
		}
		defer release()
	}

	edit, err := registry.PlanCooldownEdit(overlayPath, req)
	if err != nil {
		var amb *registry.AmbiguousKeyError
		if errors.As(err, &amb) {
			err = fmt.Errorf("%w; or pass --all-lines for all of them", err)
		}
		return failWith(1, fmt.Errorf("cooldown %s: %w", verb, err))
	}

	printCooldownEditNotes(edit)
	if !edit.Changed() {
		fmt.Println(i18n.T("packages.toml already says that: nothing to write"))
		return nil
	}
	printCooldownChanges(edit)

	if opts.dryRun {
		diff, err := registryDiff(edit.Path, edit.Original, edit.Updated)
		if err != nil {
			return failWith(1, fmt.Errorf("cooldown %s: %w", verb, err))
		}
		fmt.Println()
		printRepairDiff(diff)
		fmt.Println()
		output.Warning.Println(i18n.T("dry run: packages.toml is unchanged"))
		return nil
	}

	if err := edit.Write(); err != nil {
		return failWith(1, fmt.Errorf("cooldown %s: %w", verb, err))
	}
	for _, c := range edit.Changes {
		log.Info("cooldown: registry override changed", "verb", verb, "package", c.Key,
			"old_days", cooldownDaysLog(c.OldDays), "new_days", cooldownDaysLog(c.NewDays))
	}
	output.Success.Printf(i18n.TCount("packages.toml: %d record(s) changed", len(edit.Changes))+"\n", len(edit.Changes))
	output.Info.Println("  " + i18n.T("Review it before it is published: 'bentoo overlay diff'"))
	warnIfCooldownDisabled(log, overlayPath)

	if !opts.sweep {
		fmt.Println("  " + i18n.T("It acts on the versions already masked at the next --apply or 'bentoo overlay cooldown sweep'."))
		return nil
	}
	block, lifts, err := sweepCooldown(ctx, log, overlayPath, opts.pkgdbRoot, nil)
	if err != nil {
		return failWith(1, fmt.Errorf("cooldown %s: the registry was written but the sweep failed: %w", verb, err))
	}
	if block == nil {
		return nil
	}
	printCooldownLifts(lifts)
	if err := block.Save(overlayPath); err != nil {
		return failWith(1, fmt.Errorf("cooldown %s: the registry was written but the sweep failed: %w", verb, err))
	}
	return nil
}

// printCooldownEditNotes says which records the edit covers beyond the ones
// named, and which it leaves alone.
func printCooldownEditNotes(edit *registry.CooldownEdit) {
	if len(edit.GroupAdded) > 0 {
		fmt.Printf(i18n.T("cooldown_with group: also editing %s (a group is released whole, so its members share one wait)\n"),
			strings.Join(edit.GroupAdded, ", "))
	}
	for _, key := range edit.Targets {
		if others := edit.Siblings[key]; len(others) > 0 {
			fmt.Printf(i18n.T("note: %s leaves %s as they are (name them too, or pass --all-lines)\n"), key, strings.Join(others, ", "))
		}
	}
}

// printCooldownChanges prints each changed record as old -> new.
func printCooldownChanges(edit *registry.CooldownEdit) {
	for _, c := range edit.Changes {
		fmt.Printf("  %s: cooldown_days %s -> %s\n", c.Key, cooldownDaysText(c.OldDays), cooldownDaysText(c.NewDays))
		if old, cur := cooldownReasonText(c.OldReason), cooldownReasonText(c.NewReason); old != cur {
			fmt.Printf("  %s  cooldown_reason %s -> %s\n", strings.Repeat(" ", len(c.Key)), old, cur)
		}
	}
}

func cooldownDaysText(d *int) string {
	if d == nil {
		return i18n.T("(block's days)")
	}
	return strconv.Itoa(*d)
}

func cooldownReasonText(r *string) string {
	if r == nil {
		return i18n.T("(none)")
	}
	return strconv.Quote(*r)
}

// cooldownDaysLog is the log value of a wait: the number, or "block".
func cooldownDaysLog(d *int) string {
	if d == nil {
		return "block"
	}
	return strconv.Itoa(*d)
}

// warnIfCooldownDisabled tells the operator the value is recorded but has
// nothing to act on yet: the overlay carries no cooldown block.
func warnIfCooldownDisabled(log *slog.Logger, overlayPath string) {
	block, err := cooldown.Load(overlayPath)
	if err != nil {
		log.Debug("cooldown: block unreadable after the registry edit", "err", err)
		return
	}
	if block == nil {
		output.Warning.Println("  " + i18n.T("This overlay has no cooldown block yet: the value applies once one exists."))
	}
}
