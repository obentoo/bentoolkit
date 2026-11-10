package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	"github.com/obentoo/bentoolkit/internal/autoupdate"
	"github.com/obentoo/bentoolkit/internal/autoupdate/cooldown"
	"github.com/obentoo/bentoolkit/internal/autoupdate/ebuilds"
	"github.com/obentoo/bentoolkit/internal/common/output"
)

// cooldownRun is what the apply-start sweep leaves for the rest of the run.
type cooldownRun struct {
	enabled bool
	lifts   []cooldown.Lift
	option  autoupdate.ApplierOption
}

// ebuildExistsFn reports whether overlayPath holds pkg's ebuild of version.
func ebuildExistsFn(overlayPath string) func(pkg, ver string) bool {
	return func(pkg, ver string) bool {
		category, name, ok := ebuilds.SplitPkgAtom(pkg)
		if !ok {
			return false
		}
		_, err := os.Stat(filepath.Join(overlayPath, category, name, name+"-"+ver+".ebuild"))
		return err == nil
	}
}

// sweepCooldown loads the block of overlayPath and applies every release rule:
// elapsed, installed under pkgdbRoot, stale. It logs one Warn per
// entry dated in the future and per stale lift, one Info per lift, and one
// Warn when the installed-package database cannot be read. It does not
// save. A nil block with a nil error is cooldown disabled.
func sweepCooldown(ctx context.Context, log *slog.Logger, overlayPath, pkgdbRoot string, confirms []cooldown.Confirm) (*cooldown.Block, []cooldown.Lift, error) {
	block, err := cooldown.Load(overlayPath)
	if err != nil || block == nil {
		return nil, nil, err
	}
	inst, err := cooldown.NewPkgdbInstalledContext(ctx, pkgdbRoot)
	if err != nil {
		log.Warn("cooldown: release by installation skipped this run", "root", pkgdbRoot, "err", err)
		inst = nil
	}
	now := time.Now().UTC()
	for _, e := range block.Future(now) {
		log.Warn("cooldown: entry dated in the future, read as now", "package", e.Package, "version", e.Version,
			"since", e.Since.Format(time.RFC3339))
	}
	lifts, rest := cooldown.Sweep(block, now, inst, confirms, ebuildExistsFn(overlayPath))
	logCooldownLifts(log, lifts)
	return rest, lifts, nil
}

// logCooldownLifts logs one Info per lift, and a Warn for a stale one.
func logCooldownLifts(log *slog.Logger, lifts []cooldown.Lift) {
	for _, l := range lifts {
		if l.Reason == cooldown.ReasonStale {
			log.Warn("cooldown: lifted an entry whose ebuild is gone", "package", l.Package, "version", l.Version, "reason", l.Reason)
			continue
		}
		log.Info("cooldown: lifted", "package", l.Package, "version", l.Version, "reason", l.Reason)
	}
}

// cooldownApplyStart runs under the apply lock, before any candidate is
// checked: it releases what is due, saves, and hands the swept block to the
// Applier. A block that cannot be read is reported and the run continues
// with every promotion that needs an entry refused; a failed save
// stops the run before anything is staged.
func (ar *autoupdateRun) cooldownApplyStart(ctx context.Context, overlayPath string) (cooldownRun, error) {
	block, lifts, err := sweepCooldown(ctx, ar.log(), overlayPath, ar.deps.cooldownPkgdbRoot, nil)
	if err != nil {
		ar.log().Error("cooldown: the block cannot be read; every promotion that needs an entry is refused this run", "err", err)
		output.Error.Printf("cooldown: %v\n", err)
		return cooldownRun{enabled: true, option: autoupdate.WithApplierCooldown(nil, err)}, nil
	}
	if block == nil {
		return cooldownRun{option: autoupdate.WithApplierCooldown(nil, nil)}, nil
	}
	if err := block.Save(overlayPath); err != nil {
		return cooldownRun{}, failWith(1, fmt.Errorf("cooldown: saving the swept block before the apply: %w", err))
	}
	return cooldownRun{enabled: true, lifts: lifts, option: autoupdate.WithApplierCooldown(block, nil)}, nil
}

// displayCooldownSummary prints the run's Cooldown section: the
// versions this run masked and the ones it released, each by atom.
func displayCooldownSummary(run cooldownRun, results []*autoupdate.ApplyResult) {
	if !run.enabled {
		return
	}
	var added []cooldown.Entry
	var retired, skipped, graduated []string
	for _, r := range results {
		if r == nil {
			continue
		}
		added = append(added, r.CooldownAdded...)
		for _, v := range r.CooldownRetired {
			retired = append(retired, "="+cooldownAtomOf(r.Package)+"-"+v)
		}
		if g := r.Graduated; g != nil && g.Success {
			graduated = append(graduated, fmt.Sprintf("=%s-%s (from %s)", cooldownAtomOf(g.Package), g.NewVersion, g.OldVersion))
		}
		if r.CooldownSkipped != "" && r.Success {
			skipped = append(skipped, fmt.Sprintf("=%s-%s (%s)", cooldownAtomOf(r.Package), r.NewVersion, r.CooldownSkipped))
		}
	}
	fmt.Println()
	output.Header.Printf("Cooldown: %d added, %d replaced, %d graduated, %d lifted, %d unmasked\n",
		len(added), len(retired), len(graduated), len(run.lifts), len(skipped))
	for _, e := range added {
		fmt.Printf("  masked    %s (since %s)\n", e.Atom(), e.Since.UTC().Format(time.RFC3339))
	}
	for _, a := range retired {
		fmt.Printf("  replaced  %s\n", a)
	}
	for _, g := range graduated {
		fmt.Printf("  graduated %s\n", g)
	}
	for _, s := range skipped {
		fmt.Printf("  unmasked  %s\n", s)
	}
	for _, l := range run.lifts {
		fmt.Printf("  lifted    %s (%s)\n", l.Atom(), l.Reason)
	}
}

// cooldownAtomOf strips a record key's ":slot" or "@label" to the bare
// cat/pkg an atom is built from.
func cooldownAtomOf(key string) string {
	if c, n, ok := ebuilds.SplitPkgAtom(key); ok {
		return c + "/" + n
	}
	return key
}
