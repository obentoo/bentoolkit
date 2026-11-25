package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/obentoo/bentoolkit/internal/autoupdate"
	"github.com/obentoo/bentoolkit/internal/autoupdate/fixer"
	"github.com/obentoo/bentoolkit/internal/autoupdate/llm"
	"github.com/obentoo/bentoolkit/internal/autoupdate/repair"
	"github.com/obentoo/bentoolkit/internal/autoupdate/validate"
	"github.com/obentoo/bentoolkit/internal/common/config"
	"github.com/obentoo/bentoolkit/internal/common/i18n"
	"github.com/obentoo/bentoolkit/internal/common/output"
	"github.com/obentoo/bentoolkit/internal/common/report"
	"github.com/obentoo/bentoolkit/internal/common/secrets"
)

// repairTarget is what --repair works on once every refusal has passed: the
// bundle a failed apply left and the path it was read from.
type repairTarget struct {
	bundle     repair.Bundle
	bundlePath string
}

// refuseRepairMisuse returns the exit-1 refusal of a --repair run that cannot
// start a session: another mode beside it, stdin or stdout not a terminal, or
// no claude executable on PATH. Without --repair it returns nil.
func (ar *autoupdateRun) refuseRepairMisuse() error {
	if ar.opts.repair == "" {
		return nil
	}
	for _, mode := range []struct {
		name string
		set  bool
	}{
		{"apply", ar.opts.apply != ""},
		{"check", ar.opts.check},
		{"revive", ar.opts.revive != ""},
		{"clean", ar.opts.clean},
		{"list", ar.opts.list},
		{"revive-list", ar.opts.reviveList},
		{"lint", ar.opts.lint},
		{"mark-auto-disabled", ar.opts.markAutoDisabled},
	} {
		if mode.set {
			return failWith(1, fmt.Errorf("--repair cannot be combined with --%s", mode.name))
		}
	}
	if !ar.deps.checkInteractive() || !ar.deps.uiIsTerminal() {
		return failWith(1, errors.New("--repair needs an interactive terminal on both stdin and stdout: the session asks the operator before every action"))
	}
	if _, err := ar.deps.lookPath("claude"); err != nil {
		return failWith(1, fmt.Errorf("--repair needs the claude executable on PATH: %w", err))
	}
	return nil
}

// runRepair handles --repair under the overlay lock runAutoupdate holds; a
// comma-separated list goes to one session for all of them (runBatchRepair). It
// refuses a bundle it cannot trust or that no longer matches the pending
// update before anything is shown or asked, starts the session only on the
// operator's yes, and then re-validates the staged tree whatever the session's
// exit status.
func (ar *autoupdateRun) runRepair(ctx context.Context, overlayPath, configDir string, auCfg config.AutoupdateConfig) error {
	pkgs := repairPackages(ar.opts.repair)
	if len(pkgs) <= 1 {
		return ar.runRepairFor(ctx, strings.TrimSpace(ar.opts.repair), overlayPath, configDir, auCfg)
	}
	if !ar.runBatchRepair(ctx, pkgs, overlayPath, configDir, auCfg) {
		return failWith(1, fmt.Errorf("--repair %s: not every bump was proven; the summary above names each one", strings.Join(pkgs, ",")))
	}
	return nil
}

// runRepairFor is runRepair for pkg: --repair's own package, or one the check
// offers at its end.
func (ar *autoupdateRun) runRepairFor(ctx context.Context, pkg, overlayPath, configDir string, auCfg config.AutoupdateConfig) error {
	target, err := ar.loadRepairTarget(configDir, pkg)
	if err != nil {
		return err
	}
	b := target.bundle
	category, name, _ := strings.Cut(pkg, "/")
	pkgDir := filepath.Join(b.StagedRoot, category, name)
	for _, dir := range []string{overlayPath, b.StagedRoot, pkgDir} {
		if err := llm.CheckAgentDir(dir); err != nil {
			return failWith(1, fmt.Errorf("--repair %s: the session's permission rules cannot carry the path %q: %w", pkg, dir, err))
		}
	}
	claudePath, err := ar.deps.lookPath("claude")
	if err != nil {
		return failWith(1, fmt.Errorf("--repair needs the claude executable on PATH: %w", err))
	}

	if err := ctx.Err(); err != nil {
		return repairInterrupted(pkg, "before the session was offered", err)
	}
	printRepairBundle(target)
	if !ar.confirmRepairSession(ctx, pkgDir) {
		output.Info.Println(i18n.T("No session started; the bundle and the staged tree are unchanged."))
		return nil
	}

	environ := os.Environ()
	err = ar.runRepairSession(ctx, claudePath, repair.SessionEnv(environ, repairSecretEnvNames(environ, auCfg)), repair.SessionSpec{
		Bundle:      b,
		PkgDir:      pkgDir,
		OverlayPath: overlayPath,
		Secrets:     secrets.Paths(),
		Guidance:    fixer.EbuildGuidance,
	})
	if err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		ar.markRepaired(b)
		return failWith(1, fmt.Errorf("--repair %s: interrupted before the re-validation; the staged tree is kept as the session left it: %w", pkg, err))
	}
	return ar.revalidateRepair(ctx, overlayPath, configDir, auCfg.LLM, b)
}

// markRepaired sets the repair mark on b's bundle once a session has run and
// its tree was not promoted, so a later --apply or --check keeps the tree. A
// mark that cannot be written is a WARN: the run's outcome is already decided.
func (ar *autoupdateRun) markRepaired(b repair.Bundle) {
	if err := repair.MarkRepaired(b.StagedRoot, time.Now()); err != nil {
		ar.log().Warn("the repair bundle could not be marked as repaired; a later --apply may restage over the session's work",
			"package", b.Package, "staged_root", b.StagedRoot, "err", err)
	}
}

// repairInterrupted is the exit-1 outcome of a cancelled run that stopped
// before it changed anything.
func repairInterrupted(pkg, when string, cause error) error {
	return failWith(1, fmt.Errorf("--repair %s: interrupted %s; the bundle and the staged tree are unchanged: %w", pkg, when, cause))
}

// repairSecretEnvNames lists the variables in environ the session must not
// inherit because bentoo reads a secret from them: the GitHub and GitLab
// tokens, the LLM and search API key variables the config names, and every
// BENTOO_* variable. That namespace holds the authenticated-fetch serials and
// form values, the repository tokens and the notification credentials, and a
// package record may expand any name in it into a request header.
func repairSecretEnvNames(environ []string, auCfg config.AutoupdateConfig) []string {
	names := []string{"GITHUB_TOKEN", "GH_TOKEN", "GITLAB_TOKEN", auCfg.LLM.APIKeyEnv, auCfg.Search.APIKeyEnv}
	for _, kv := range environ {
		if name, _, _ := strings.Cut(kv, "="); strings.HasPrefix(name, "BENTOO_") {
			names = append(names, name)
		}
	}
	return names
}

// printRepairBundle shows what the session is about to work on.
func printRepairBundle(target repairTarget) {
	b := target.bundle
	refused := i18n.T("none")
	if len(b.RefusedTools) > 0 {
		refused = strings.Join(b.RefusedTools, ", ")
	}
	fmt.Println()
	output.Header.Println(i18n.T("Repair"))
	output.Package.Printf("  %s\n", b.Package)
	fmt.Printf("    "+i18n.T("Version:       %s → %s")+"\n", b.CurrentVersion, b.Candidate)
	fmt.Printf("    "+i18n.T("Failed stage:  %s")+"\n", b.Stage)
	fmt.Printf("    "+i18n.T("Refused tools: %s")+"\n", refused)
	fmt.Printf("    "+i18n.T("Staged tree:   %s")+"\n", b.StagedRoot)
	fmt.Printf("    "+i18n.T("Bundle:        %s")+"\n", target.bundlePath)
	fmt.Println()
}

// confirmRepairSession asks before the session starts. --yes does not answer
// it, and the next Ctrl+C at the prompt ends the command: the stdin read does
// not watch the run's context.
func (ar *autoupdateRun) confirmRepairSession(ctx context.Context, pkgDir string) bool {
	restore := yieldContextSignalsToPrompt(ctx)
	defer restore()
	return ar.deps.confirmRepair(fmt.Sprintf(i18n.T("Start an interactive claude session in %s?"), pkgDir))
}

// runRepairSession runs claude in the foreground on the process's own
// terminal, in the staged package directory, with env as its environment, and
// logs how it ended. The child gets no context and no process group of its
// own: it stays in the terminal's foreground group, so the operator's Ctrl+C
// and SIGQUIT reach it while bentoo drops them. A SIGTERM or SIGHUP bentoo
// receives meanwhile does not stop the session, but it does stop the run once
// the session has ended: the returned error skips the re-validation.
func (ar *autoupdateRun) runRepairSession(ctx context.Context, claudePath string, env []string, spec repair.SessionSpec) error {
	pkg := spec.Bundle.Package
	received, ok := ar.runForegroundSession(ctx, claudePath, repair.SessionArgs(spec), spec.PkgDir, env, pkg)
	if !ok {
		return repairInterrupted(pkg, "before the session started", context.Canceled)
	}
	if received != nil {
		ar.markRepaired(spec.Bundle)
		return ar.repairStoppedBy(pkg, received)
	}
	return nil
}

// runForegroundSession runs claude with argv in dir as runRepairSession
// describes, for --repair's one bump and for the batch session alike; label
// names the bump(s) in the log. It returns the SIGTERM or SIGHUP bentoo
// received meanwhile, if any, and false when the signals could not be
// suspended because the run is already stopping. A session that does not
// start is a WARN and counts as ended: the trees are re-validated as they are.
func (ar *autoupdateRun) runForegroundSession(ctx context.Context, claudePath string, argv []string, dir string, env []string, label string) (os.Signal, bool) {
	//nolint:gosec,noctx // a caught signal must not kill the operator's session; the argv is built by package repair
	session := exec.Command(claudePath, argv...)
	session.Stdin, session.Stdout, session.Stderr = os.Stdin, os.Stdout, os.Stderr
	session.Dir = dir
	session.Env = env

	resume, ok := policyFrom(ctx).suspendSignals()
	if !ok {
		return nil, false
	}
	if err := session.Start(); err != nil {
		if sig := resume(); sig != nil {
			return sig, true
		}
		ar.log().Warn("the repair session did not start; re-validating the staged tree as it is",
			"package", label, "dir", dir, "err", err)
		return nil, true
	}
	err := session.Wait()
	received := resume()
	ar.log().Info("the repair session ended",
		"package", label, "exit_status", session.ProcessState.ExitCode(), "err", err)
	return received, true
}

// repairStoppedBy is the exit-1 outcome of a run that received SIGTERM or
// SIGHUP: nothing is re-validated or promoted, and the staged tree is left as
// the session left it.
func (ar *autoupdateRun) repairStoppedBy(pkg string, sig os.Signal) error {
	ar.log().Warn("re-validation skipped: a signal arrived during the repair session", "package", pkg, "signal", sig.String())
	return failWith(1, fmt.Errorf("--repair %s: the re-validation was skipped because bentoo received %s during the session; run it again with --repair %s",
		pkg, sig, pkg))
}

// revalidateRepair proves the adopted staged tree the way --check does, under
// the lock the run already holds, and publishes nothing: a READY verdict is a
// proof `--apply` publishes, as for any checked bump. Adopting the tree
// disables the LLM agents for the run. A READY tree has its bundle removed by
// the check; any other verdict keeps it, marked, and is exit 1.
func (ar *autoupdateRun) revalidateRepair(ctx context.Context, overlayPath, configDir string, llmCfg config.LLMConfig, b repair.Bundle) error {
	proved := false
	defer func() {
		if !proved {
			ar.markRepaired(b)
		}
	}()

	extra := []autoupdate.ApplierOption{autoupdate.WithAdoptedStagedTree(b.StagedRoot)}
	if depth, raise := repairDepth(b, ar.validate.Depth); raise {
		extra = append(extra, autoupdate.WithApplierDepth(depth))
	}
	// The live region the re-validation's builds run under.
	checkCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	live, release := ar.buildLiveReporter(checkCtx, cancel, 1, "Re-validating the repair")
	defer release()
	extra = append(extra, live...)
	applier, err := ar.newSingleApplier(overlayPath, configDir, llmCfg, extra...)
	if err != nil {
		return err
	}
	result := applier.Validate(checkCtx, b.Package, validate.DepthNone)
	release()
	if err := checkCtx.Err(); err != nil {
		return failWith(1, fmt.Errorf("--repair %s: interrupted during the re-validation; the staged tree is kept as the session left it: %w", b.Package, err))
	}

	state := repairState(result)
	printRepairVerdict(b.Package, state, result)
	if state == report.Wait {
		// Another repair cannot change the host: the liborcus (slot conflict)
		// and amd-gaia (no python3_15 dependency) repairs of 2026-11-23 both
		// ended WAIT and were told to repair again.
		return failWith(1, fmt.Errorf("--repair %s: the repaired tree is %s: only this host stood in the way (the gates above name what it lacks); "+
			"the bundle is kept: install what is missing, or prove it where it is installed (a lab VM), then run --repair %s again",
			b.Package, state, b.Package))
	}
	if state != report.Ready {
		return failWith(1, fmt.Errorf("--repair %s: the repaired tree is %s, not proven; the bundle is kept: run --repair %s again", b.Package, state, b.Package))
	}
	proved = true
	return nil
}

// repairState is the check's verdict on a re-validated tree.
func repairState(result validate.EbuildResult) report.State {
	facts := validate.ReportGateFacts(result.Gates, result.DepthRequested)
	return report.StateOf(report.Classify(false, facts), facts)
}

// printRepairVerdict shows each gate's outcome and what to do next.
func printRepairVerdict(pkg string, state report.State, result validate.EbuildResult) {
	fmt.Println()
	output.Header.Println(i18n.T("Re-validation"))
	for _, g := range result.Gates {
		fmt.Printf("    %-12s %s", g.Gate, g.Outcome)
		if g.Reason != "" && g.Outcome != validate.OutcomePass {
			fmt.Printf(": %s", g.Reason)
		}
		fmt.Println()
	}
	if state == report.Ready {
		output.Success.Printf("\n"+i18n.T("✓ %s is READY: the repaired tree is proven.")+"\n", pkg)
		output.Info.Printf(i18n.T("Publish it with 'bentoo overlay autoupdate --apply %s'")+"\n", pkg)
		return
	}
	output.Warning.Printf("\n"+i18n.T("%s is %s.")+"\n", pkg, state)
}

// repairNeedsCompile reports whether the re-validation must reach the compile
// gate: the adopted tree's depth floor does not reach it, so a bundle that
// failed there asks for --depth=compile explicitly, on the unprivileged ladder.
func repairNeedsCompile(b repair.Bundle) bool {
	return b.Stage == "gate:"+validate.GateCompile
}

// repairDepth is the depth the re-validation is raised to, and whether to
// raise it: compile for a bundle that failed there, unless --depth already
// asks for as much or more.
func repairDepth(b repair.Bundle, asked *validate.Depth) (validate.Depth, bool) {
	if !repairNeedsCompile(b) || (asked != nil && *asked >= validate.DepthCompile) {
		return 0, false
	}
	return validate.DepthCompile, true
}

// loadRepairTarget finds the package's newest bundle and checks it against
// its own location and the pending list. Every refusal is exit 1 and names
// what disagrees.
func (ar *autoupdateRun) loadRepairTarget(configDir, pkg string) (repairTarget, error) {
	stagingRoot := filepath.Join(configDir, stagingDirName)
	b, bundlePath, err := repair.FindBundle(stagingRoot, pkg)
	if err != nil {
		return repairTarget{}, failWith(1, fmt.Errorf("--repair %s: %w", pkg, err))
	}

	// The bundle is a plain JSON file: a staged root it names elsewhere would
	// widen the session's working tree beyond the one the failed run kept.
	dir := filepath.Dir(bundlePath)
	if filepath.Clean(b.StagedRoot) != dir {
		return repairTarget{}, failWith(1, fmt.Errorf("--repair %s: the bundle %s names the staged tree %s, not the directory it was found in (%s)",
			pkg, bundlePath, b.StagedRoot, dir))
	}
	want, err := validate.StagedTreePath(stagingRoot, pkg, b.Candidate)
	if err != nil {
		return repairTarget{}, failWith(1, fmt.Errorf("--repair %s: the bundle %s: %w", pkg, bundlePath, err))
	}
	if dir != want {
		return repairTarget{}, failWith(1, fmt.Errorf("--repair %s: the bundle %s is for candidate %s, whose staged tree is %s, not %s",
			pkg, bundlePath, b.Candidate, want, dir))
	}

	pending, err := autoupdate.NewPendingList(configDir)
	if err != nil {
		return repairTarget{}, failWith(1, fmt.Errorf("--repair %s: failed to load pending list: %w", pkg, err))
	}
	entry, found := pending.Get(pkg)
	switch {
	case !found:
		return repairTarget{}, failWith(1, fmt.Errorf("--repair %s: no pending update, but the bundle is for candidate %s (current %s); run --check to find the update again",
			pkg, b.Candidate, b.CurrentVersion))
	case entry.NewVersion != b.Candidate:
		return repairTarget{}, failWith(1, fmt.Errorf("--repair %s: the pending candidate is %s, but the bundle is for %s; run --apply %s to stage the pending one",
			pkg, entry.NewVersion, b.Candidate, pkg))
	}
	return repairTarget{bundle: b, bundlePath: bundlePath}, nil
}

// humanBumpsWithBundle lists, in the run's order, the bumps the check left to
// the maintainer (HUMAN) that have a repair bundle: the ones offerRepairs
// offers. It lives here because this file is the only one allowed to read
// bundles: a guard test fails any other reader.
func humanBumpsWithBundle(stagingRoot string, payload report.AutoupdateCheck) []string {
	var offered []string
	for _, row := range payload.Results {
		if row.State != report.Human {
			continue
		}
		if _, _, err := repair.FindBundle(stagingRoot, row.Package); err == nil {
			offered = append(offered, row.Package)
		}
	}
	return offered
}
