package main

import (
	"context"
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	"github.com/obentoo/bentoolkit/internal/autoupdate"
	"github.com/obentoo/bentoolkit/internal/common/ebuild"
	"github.com/obentoo/bentoolkit/internal/common/logger"
	"github.com/obentoo/bentoolkit/internal/common/output"
)

// sweepPlannerFn and sweepExecutorFn are the seams the CLI tests drive. Both
// default to the real implementations, so a caller that supplies neither gets
// production behaviour.
var (
	sweepPlannerFn  = autoupdate.PlanOverlaySweep
	sweepExecutorFn = autoupdate.ExecuteOverlaySweep
	confirmSweepFn  = confirmAction
)

// runSweep is `bentoo overlay autoupdate --clean` with no `--apply`: it sweeps
// the package directories holding an ebuild no registry entry claims.
//
// # Why this exists as its own mode
//
// `--clean` was a modifier on an apply, and the sweep ran inside the apply's
// success path — which Apply reaches only for a package with a pending update.
// A package already at its upstream version could therefore never be swept: no
// check produces a pending entry for it, so no apply happens, so no sweep
// happens. The residue of every bump run without `--clean` accumulated in one
// direction, removable only by a future bump of the same package.
//
// # The order of operations is the safety property
//
// Plan, print, confirm, execute — and the executor is not entered at all when
// the confirmation is declined, so a declined sweep leaves the overlay
// byte-identical by construction rather than by care. The overlay auto-commits
// and pushes, so a wrong removal is a published removal within minutes; that is
// why the non-interactive path refuses instead of assuming consent.
func runSweep(ctx context.Context, overlayPath string, args []string) {
	// A sweep with no registry has no claims, and "no entry claims this" is the
	// state in which planSweep refuses to remove anything. Failing loudly here
	// is the honest outcome: the alternative is an empty batch that reads as
	// "nothing to clean" when the truth is "nothing could be read".
	cfg, err := autoupdate.LoadPackagesConfig(overlayPath)
	if err != nil {
		logger.Error("cannot sweep: %v", err)
		osExit(1)
		return
	}

	var target string
	if len(args) > 0 {
		target = args[0]
	}

	batch, err := sweepPlannerFn(overlayPath, cfg.Packages, target)
	if err != nil {
		logger.Error("%v", err)
		osExit(1)
		return
	}

	displaySweepPlan(batch)

	if batch.TotalRemove == 0 {
		// R3.5: there is nothing to confirm. Asking "remove 0 files?" would
		// train the operator to say yes.
		output.Info.Println("  Nothing to sweep: every ebuild is claimed by an entry.")
		return
	}

	if !confirmSweep(batch) {
		return
	}

	report := sweepExecutorFn(ctx, overlayPath, batch,
		autoupdate.WithSweepConcurrency(autoupdateConcurrency),
		autoupdate.WithSweepPackagesConfig(cfg.Packages),
	)
	displaySweepReport(report)
}

// displaySweepPlan prints the whole batch before anything is removed.
//
// The full list, never a sample: the operator approves one batch, and a
// truncated list hides exactly the line that is wrong — the same reasoning the
// registry reconciliation's report already states.
func displaySweepPlan(batch autoupdate.SweepBatch) {
	fmt.Println()
	output.Header.Println("Overlay Sweep")
	fmt.Println()

	if len(batch.Dirs) == 0 && len(batch.PlanErrors) == 0 {
		output.Info.Println("  No package directory holds an ebuild that no entry claims.")
		return
	}

	var removable, blocked []autoupdate.SweepDirPlan
	for _, d := range batch.Dirs {
		if d.IsBlocked() {
			blocked = append(blocked, d)
			continue
		}
		if len(d.Remove) > 0 {
			removable = append(removable, d)
		}
	}

	if len(removable) > 0 {
		fmt.Printf("  To remove (%d ebuild(s) in %d director(ies)):\n", batch.TotalRemove, len(removable))
		for _, d := range removable {
			pkgName := filepath.Base(d.Atom)
			for _, v := range d.Remove {
				fmt.Printf("    %-45s %s-%s.ebuild\n", d.Atom, pkgName, v)
			}
			for _, line := range keptLines(d.Keep) {
				output.Info.Printf("      keeps %s\n", line)
			}
		}
		fmt.Println()
	}

	if len(blocked) > 0 {
		fmt.Printf("  Blocked — nothing removed (%d):\n", len(blocked))
		for _, d := range blocked {
			if d.Blocked != "" {
				fmt.Printf("    %-45s entry %q has no version pin\n", d.Atom, d.Blocked)
			} else {
				fmt.Printf("    %-45s no registry entry claims this directory\n", d.Atom)
			}
			if len(d.WouldRemove) > 0 {
				output.Info.Printf("      would have removed: %s\n", strings.Join(d.WouldRemove, ", "))
			}
		}
		fmt.Println()
	}

	if len(batch.PlanErrors) > 0 {
		fmt.Printf("  Could not be planned (%d):\n", len(batch.PlanErrors))
		for _, e := range batch.PlanErrors {
			output.Error.Printf("    %-45s %v\n", e.Atom, e.Err)
		}
		fmt.Println()
	}
}

// keptLines renders a directory's surviving versions with the entry claiming
// each, in Gentoo order so it reads alongside the removal list. Naming the
// claiming entry is the point: "keeps 1.28.5" invites the question the report
// exists to answer — kept by whom, and may I delete it?
func keptLines(keep map[string]string) []string {
	versions := make([]string, 0, len(keep))
	for v := range keep {
		versions = append(versions, v)
	}
	sort.SliceStable(versions, func(i, j int) bool {
		if c := ebuild.CompareVersions(versions[i], versions[j]); c != 0 {
			return c < 0
		}
		return versions[i] < versions[j]
	})

	lines := make([]string, 0, len(versions))
	for _, v := range versions {
		if key := keep[v]; key != "" {
			lines = append(lines, fmt.Sprintf("%s — claimed by %s", v, key))
			continue
		}
		lines = append(lines, fmt.Sprintf("%s — kept by rule (live ebuild, or the last release)", v))
	}
	return lines
}

// confirmSweep takes ONE confirmation covering the whole batch.
//
// The three gates, in order of how much they trust the caller: --yes deletes
// unattended because the operator asked for that in so many words; an
// interactive terminal is asked; anything else prints the plan and removes
// nothing. registryPromptIsInteractive requires BOTH a stdin and a stdout TTY,
// so `yes | bentoo overlay autoupdate --clean` cannot answer for a human.
func confirmSweep(batch autoupdate.SweepBatch) bool {
	dirs := 0
	for _, d := range batch.Dirs {
		if len(d.Remove) > 0 {
			dirs++
		}
	}

	if autoupdateYes {
		output.Warning.Printf("  --yes given: removing %d ebuild(s) without a prompt.\n", batch.TotalRemove)
		return true
	}
	if !registryPromptIsInteractive() {
		output.Warning.Println("  Not an interactive terminal and --yes was not given: nothing removed.")
		output.Info.Printf("  Re-run with --yes to remove these %d ebuild(s) unattended.\n", batch.TotalRemove)
		return false
	}

	fmt.Println()
	output.Warning.Println("  These ebuilds are deleted from the overlay, which auto-commits and publishes.")
	return confirmSweepFn(fmt.Sprintf(
		"Remove %d ebuild(s) from %d director(ies)?", batch.TotalRemove, dirs))
}

// displaySweepReport prints what actually happened, per directory and in total.
//
// It reuses the apply-time sweep's vocabulary ("Removed:", "Kept: … claimed
// by") so the two sweeps read alike, and reports only files that really went
// away — under-reporting a deletion is survivable, claiming one that did not
// happen is not.
func displaySweepReport(report autoupdate.SweepReport) {
	fmt.Println()
	output.Header.Println("Sweep Result")
	fmt.Println()

	for _, r := range report.Results {
		if len(r.Removed) == 0 && r.Err == nil {
			continue
		}
		fmt.Printf("  %s\n", r.Atom)
		if len(r.Removed) > 0 {
			pkgName := filepath.Base(r.Atom)
			names := make([]string, 0, len(r.Removed))
			for _, v := range r.Removed {
				names = append(names, fmt.Sprintf("%s-%s.ebuild", pkgName, v))
			}
			fmt.Printf("    Removed: %s\n", strings.Join(names, ", "))
		}
		if lines := keptLines(r.Kept); len(lines) > 0 {
			fmt.Println("    Kept:")
			for _, l := range lines {
				fmt.Printf("      %s\n", l)
			}
		}
		if r.Err != nil {
			output.Error.Printf("    Error:   %v\n", r.Err)
		}
	}

	fmt.Println()
	output.Success.Printf("  Swept %d director(ies), removed %d ebuild(s).\n", report.Swept, report.Removed)
	if report.BlockedN > 0 {
		output.Warning.Printf("  %d director(ies) blocked — see the plan above for the entry to fix.\n", report.BlockedN)
	}
	if report.Failed > 0 {
		// R6.3: reported, but the exit code still says the command ran. The
		// removals that succeeded really happened, and a non-zero exit would
		// claim otherwise.
		output.Warning.Printf("  %d director(ies) failed — nothing else was affected.\n", report.Failed)
	}
	if report.Removed > 0 {
		output.Info.Println("  Review the diff before it is published: 'bentoo overlay diff'")
	}
}
