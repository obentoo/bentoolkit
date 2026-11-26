package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/obentoo/bentoolkit/internal/autoupdate/fixer"
	"github.com/obentoo/bentoolkit/internal/autoupdate/llm"
	"github.com/obentoo/bentoolkit/internal/autoupdate/repair"
	"github.com/obentoo/bentoolkit/internal/common/config"
	"github.com/obentoo/bentoolkit/internal/common/i18n"
	"github.com/obentoo/bentoolkit/internal/common/output"
	"github.com/obentoo/bentoolkit/internal/common/report"
	"github.com/obentoo/bentoolkit/internal/common/secrets"
)

// offerRepairs ends an interactive `--check --llm`: the bumps the check left
// to the maintainer (HUMAN) that have a repair bundle are offered a repair.
// One such bump goes through the --repair flow itself, which resumes the
// build fixer's session. Several go through ONE claude session for all of
// them, asked once, and are then re-validated one after the other with
// nothing more to answer: one session per bump made the operator wait out
// each session and its re-validation before the next bump was shown
// (2026-11-25). A repair's outcome is printed and never moves the check's exit
// code. Nothing is offered under --yes, to a run without a terminal on both
// stdin and stdout, or without claude on PATH: an unattended run never stops
// to ask.
func (ar *autoupdateRun) offerRepairs(ctx context.Context, overlayPath, configDir string, auCfg config.AutoupdateConfig, validated report.Run) {
	if ar.opts.yes || !ar.deps.checkInteractive() || !ar.deps.uiIsTerminal() {
		return
	}
	if _, err := ar.deps.lookPath("claude"); err != nil {
		return
	}
	payload, ok := validated.Payload.(report.AutoupdateCheck)
	if !ok {
		return
	}
	offered := humanBumpsWithBundle(filepath.Join(configDir, stagingDirName), payload)
	if len(offered) == 0 {
		return
	}

	fmt.Println()
	output.Header.Println(i18n.T("Repair sessions"))
	if len(offered) == 1 {
		if err := ar.runRepairFor(ctx, offered[0], overlayPath, configDir, auCfg); err != nil {
			output.Warning.Printf("  %s: %s\n", offered[0], failureDiagnostic(err))
		}
		return
	}
	ar.runBatchRepair(ctx, offered, overlayPath, configDir, auCfg)
}

// repairPackages splits --repair's value into its packages: one, or a
// comma-separated list repaired in one session. Blank items and repeats are
// dropped, keeping the first order.
func repairPackages(value string) []string {
	var pkgs []string
	seen := map[string]bool{}
	for _, p := range strings.Split(value, ",") {
		p = strings.TrimSpace(p)
		if p == "" || seen[p] {
			continue
		}
		seen[p] = true
		pkgs = append(pkgs, p)
	}
	return pkgs
}

// runBatchRepair is offerRepairs for several bumps, and --repair given a
// comma-separated list. A bump whose bundle is refused, or whose paths the
// session's rules cannot carry, is reported and left out; the rest share the
// session. It reports whether every bump in pkgs ended proven.
func (ar *autoupdateRun) runBatchRepair(ctx context.Context, pkgs []string, overlayPath, configDir string, auCfg config.AutoupdateConfig) bool {
	stagingRoot := filepath.Join(configDir, stagingDirName)
	for _, dir := range []string{overlayPath, stagingRoot} {
		if err := llm.CheckAgentDir(dir); err != nil {
			output.Warning.Printf("  "+i18n.T("No repair session: its permission rules cannot carry the path %q: %v")+"\n", dir, err)
			return false
		}
	}
	targets := ar.loadBatchTargets(pkgs, configDir)
	allLoaded := len(targets) == len(pkgs)
	if len(targets) == 0 {
		return false
	}
	if len(targets) == 1 {
		pkg := targets[0].bundle.Package
		if err := ar.runRepairFor(ctx, pkg, overlayPath, configDir, auCfg); err != nil {
			output.Warning.Printf("  %s: %s\n", pkg, failureDiagnostic(err))
			return false
		}
		return allLoaded
	}
	claudePath, err := ar.deps.lookPath("claude")
	if err != nil || ctx.Err() != nil {
		return false
	}

	bundles := make([]repair.Bundle, 0, len(targets))
	names := make([]string, 0, len(targets))
	for _, t := range targets {
		printRepairBundle(t)
		bundles = append(bundles, t.bundle)
		names = append(names, t.bundle.Package)
	}
	if !ar.confirmBatchSession(ctx, len(targets), stagingRoot) {
		output.Info.Println(i18n.T("No session started; the bundles and the staged trees are unchanged."))
		return allLoaded
	}

	environ := os.Environ()
	argv := repair.BatchSessionArgs(repair.BatchSessionSpec{
		Bundles:     bundles,
		OverlayPath: overlayPath,
		Secrets:     secrets.Paths(),
		Guidance:    fixer.EbuildGuidance,
	})
	env := repair.SessionEnv(environ, repairSecretEnvNames(environ, auCfg))
	received, ok := ar.runForegroundSession(ctx, claudePath, argv, stagingRoot, env, strings.Join(names, ","))
	if !ok {
		output.Warning.Println("  " + i18n.T("Interrupted before the session started; the bundles and the staged trees are unchanged."))
		return false
	}
	if received != nil || ctx.Err() != nil {
		for _, b := range bundles {
			ar.markRepaired(b)
		}
		output.Warning.Println("  " + i18n.T("The re-validation was skipped: the run is stopping. Each staged tree is kept as the session left it; run --repair with them again."))
		return false
	}
	return ar.revalidateBatch(ctx, bundles, overlayPath, configDir, auCfg) && allLoaded
}

// loadBatchTargets loads each package's bundle as --repair would, reporting
// and dropping the ones it refuses.
func (ar *autoupdateRun) loadBatchTargets(pkgs []string, configDir string) []repairTarget {
	targets := make([]repairTarget, 0, len(pkgs))
	for _, pkg := range pkgs {
		target, err := ar.loadRepairTarget(configDir, pkg)
		if err == nil {
			err = llm.CheckAgentDir(target.bundle.StagedRoot)
		}
		if err != nil {
			output.Warning.Printf("  %s: %s\n", pkg, failureDiagnostic(err))
			continue
		}
		targets = append(targets, target)
	}
	return targets
}

// revalidateBatch proves every tree the session worked on, one after the
// other, exactly as --repair proves its one tree, and ends with a summary.
// A run stopped half-way marks the trees it did not reach. It reports whether
// every tree ended proven.
func (ar *autoupdateRun) revalidateBatch(ctx context.Context, bundles []repair.Bundle, overlayPath, configDir string, auCfg config.AutoupdateConfig) bool {
	var proven, notProven []string
	for i, b := range bundles {
		if ctx.Err() != nil {
			for _, rest := range bundles[i:] {
				ar.markRepaired(rest)
				notProven = append(notProven, rest.Package)
			}
			break
		}
		fmt.Printf("\n[%d/%d] %s\n", i+1, len(bundles), b.Package)
		if err := ar.revalidateRepair(ctx, overlayPath, configDir, auCfg.LLM, b); err != nil {
			output.Warning.Printf("  %s: %s\n", b.Package, failureDiagnostic(err))
			notProven = append(notProven, b.Package)
			continue
		}
		proven = append(proven, b.Package)
	}

	fmt.Println()
	output.Header.Println(i18n.T("Repair summary"))
	for _, pkg := range proven {
		output.Success.Printf("  ✓ %s\n", pkg)
	}
	for _, pkg := range notProven {
		output.Warning.Printf("  ✗ %s\n", pkg)
	}
	if len(proven) > 0 {
		output.Info.Println(i18n.T("Publish the proven ones with 'bentoo overlay autoupdate --apply <cat/pkg>'"))
	}
	if len(notProven) > 0 {
		output.Info.Printf(i18n.T("The others keep their bundle: run --repair %s to repair them again in one session.")+"\n", strings.Join(notProven, ","))
	}
	return len(notProven) == 0
}

// confirmBatchSession asks once before the shared session starts, as
// confirmRepairSession does for one bump: --yes does not answer it.
func (ar *autoupdateRun) confirmBatchSession(ctx context.Context, n int, dir string) bool {
	restore := yieldContextSignalsToPrompt(ctx)
	defer restore()
	return ar.deps.confirmRepair(fmt.Sprintf(i18n.TCount("Start ONE interactive claude session for these %d bump(s) in %s?", n), n, dir))
}
