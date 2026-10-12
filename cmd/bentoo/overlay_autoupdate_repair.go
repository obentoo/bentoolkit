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
	"github.com/obentoo/bentoolkit/internal/common/output"
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

// runRepair handles --repair under the overlay lock runAutoupdate holds. It
// refuses a bundle it cannot trust or that no longer matches the pending
// update before anything is shown or asked, starts the session only on the
// operator's yes, and then re-validates the staged tree whatever the session's
// exit status.
func (ar *autoupdateRun) runRepair(ctx context.Context, overlayPath, configDir string, auCfg config.AutoupdateConfig) error {
	target, err := ar.loadRepairTarget(configDir)
	if err != nil {
		return err
	}
	pkg, b := ar.opts.repair, target.bundle
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
		output.Info.Println("No session started; the bundle and the staged tree are unchanged.")
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
	refused := "none"
	if len(b.RefusedTools) > 0 {
		refused = strings.Join(b.RefusedTools, ", ")
	}
	fmt.Println()
	output.Header.Println("Repair")
	output.Package.Printf("  %s\n", b.Package)
	fmt.Printf("    Version:       %s → %s\n", b.CurrentVersion, b.Candidate)
	fmt.Printf("    Failed stage:  %s\n", b.Stage)
	fmt.Printf("    Refused tools: %s\n", refused)
	fmt.Printf("    Staged tree:   %s\n", b.StagedRoot)
	fmt.Printf("    Bundle:        %s\n", target.bundlePath)
	fmt.Println()
}

// confirmRepairSession asks before the session starts. --yes does not answer
// it, and the next Ctrl+C at the prompt ends the command: the stdin read does
// not watch the run's context.
func (ar *autoupdateRun) confirmRepairSession(ctx context.Context, pkgDir string) bool {
	restore := yieldContextSignalsToPrompt(ctx)
	defer restore()
	return ar.deps.confirmRepair(fmt.Sprintf("Start an interactive claude session in %s?", pkgDir))
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
	//nolint:gosec,noctx // a caught signal must not kill the operator's session; the argv is built by repair.SessionArgs
	session := exec.Command(claudePath, repair.SessionArgs(spec)...)
	session.Stdin, session.Stdout, session.Stderr = os.Stdin, os.Stdout, os.Stderr
	session.Dir = spec.PkgDir
	session.Env = env

	resume, ok := policyFrom(ctx).suspendSignals()
	if !ok {
		return repairInterrupted(pkg, "before the session started", context.Canceled)
	}
	if err := session.Start(); err != nil {
		if sig := resume(); sig != nil {
			return ar.repairStoppedBy(pkg, sig)
		}
		ar.log().Warn("the repair session did not start; re-validating the staged tree as it is",
			"package", pkg, "dir", spec.PkgDir, "err", err)
		return nil
	}
	err := session.Wait()
	received := resume()
	ar.log().Info("the repair session ended",
		"package", pkg, "exit_status", session.ProcessState.ExitCode(), "err", err)
	if received != nil {
		ar.markRepaired(spec.Bundle)
		return ar.repairStoppedBy(pkg, received)
	}
	return nil
}

// repairStoppedBy is the exit-1 outcome of a run that received SIGTERM or
// SIGHUP: nothing is re-validated or promoted, and the staged tree is left as
// the session left it.
func (ar *autoupdateRun) repairStoppedBy(pkg string, sig os.Signal) error {
	ar.log().Warn("re-validation skipped: a signal arrived during the repair session", "package", pkg, "signal", sig.String())
	return failWith(1, fmt.Errorf("--repair %s: the re-validation was skipped because bentoo received %s during the session; run it again with --repair %s",
		pkg, sig, pkg))
}

// revalidateRepair puts the adopted staged tree through the apply path once,
// under the lock the run already holds; adopting the tree disables the LLM
// agents for that run. Only a promoted
// tree removes the bundle; a failed one has had it rewritten by the applier,
// and the bundle carries the repair mark on every way out that did not promote.
func (ar *autoupdateRun) revalidateRepair(ctx context.Context, overlayPath, configDir string, llmCfg config.LLMConfig, b repair.Bundle) error {
	applyCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	extra, finish := ar.buildApplyReporter(applyCtx, cancel, 1)
	defer finish()

	applier, err := ar.newSingleApplier(overlayPath, configDir, llmCfg,
		append(extra, autoupdate.WithAdoptedStagedTree(b.StagedRoot))...)
	if err != nil {
		return err
	}
	result, err := applier.Apply(applyCtx, b.Package, ar.opts.compile || repairNeedsCompile(b))
	finish()
	if err != nil || result == nil || !result.Success {
		// The applier marks the bundle it rewrites; an exit before it reached
		// the tree rewrote nothing.
		ar.markRepaired(b)
	}

	switch {
	case err != nil:
		ar.displayApplyResult(result)
		return failWith(1, fmt.Errorf("--repair %s: the re-validation failed: %w", b.Package, err))
	case result == nil || !result.Success:
		ar.displayApplyResult(result)
		return failWith(1, fmt.Errorf("--repair %s: the staged tree was not promoted: the package is %s", b.Package, repairOutcome(result)))
	}
	if err := repair.RemoveBundle(b.StagedRoot); err != nil {
		ar.log().Warn("the staged tree was promoted but its repair bundle could not be removed",
			"package", b.Package, "staged_root", b.StagedRoot, "err", err)
	}
	ar.displayApplyResult(result)
	return nil
}

// repairNeedsCompile reports whether the re-validation must run the compile
// gate: the adopted tree's depth floor does not reach it, so a bundle that
// failed there asks for it explicitly. An explicit --compile asks for it too.
func repairNeedsCompile(b repair.Bundle) bool {
	return b.Stage == "gate:"+validate.GateCompile
}

// repairOutcome names why an apply that returned no error did not promote.
func repairOutcome(result *autoupdate.ApplyResult) string {
	switch {
	case result == nil:
		return "in an unknown state"
	case result.Obsolete:
		return "obsolete and was pruned from pending"
	case len(result.Waiting) > 0:
		return "waiting: " + strings.Join(result.Waiting, "; ")
	case result.Held:
		return "held (" + result.HoldReason + ")"
	default:
		return "not applied"
	}
}

// loadRepairTarget finds the package's newest bundle and checks it against
// its own location and the pending list. Every refusal is exit 1 and names
// what disagrees.
func (ar *autoupdateRun) loadRepairTarget(configDir string) (repairTarget, error) {
	pkg := ar.opts.repair
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
