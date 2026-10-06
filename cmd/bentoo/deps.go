package main

import (
	"context"
	"log/slog"
	"time"

	"github.com/obentoo/bentoolkit/internal/autoupdate"
	"github.com/obentoo/bentoolkit/internal/autoupdate/fixer"
	"github.com/obentoo/bentoolkit/internal/autoupdate/registry"
	"github.com/obentoo/bentoolkit/internal/autoupdate/validate"
	"github.com/obentoo/bentoolkit/internal/common/config"
	"github.com/obentoo/bentoolkit/internal/common/output"
	"github.com/obentoo/bentoolkit/internal/common/provider"
	"github.com/obentoo/bentoolkit/internal/overlay"
	"github.com/obentoo/bentoolkit/internal/realign"
	"github.com/obentoo/bentoolkit/internal/snapshot"
)

// deps holds the test seams of cmd/bentoo (story 060, design C6). defaultDeps
// builds the production wiring once per tree; newRootCmdWith hands it to every
// constructor that reaches a seam, and the run functions read it through their
// closures. A test substitutes a field on its own deps value, so the
// substitution reaches only the tree built from it and never the defaults
// another tree starts from (R6.1, R6.5).
//
// It holds functions (and the snapshot.Runner interface) only, never a
// context.Context (containedctx): a context is per-invocation and travels as a
// parameter.
type deps struct {
	// registryWriter is how the post-check reconciliation writes
	// packages.toml. The test that must prove NOTHING was written keeps the
	// real writer wired and compares the file's bytes before and after, instead
	// of asserting against a mock that would pass even without the guard.
	registryWriter func(overlayPath string, pins map[string]string) error
	// confirmRegistryWrite is the y/N question before a registry write. The
	// default, confirmAction, reads os.Stdin and answers "no" on any read
	// error, so an EOF is a decline rather than an accident.
	confirmRegistryWrite func(prompt string) bool
	// registryPromptIsInteractive reports whether this process may ASK before
	// writing. It requires BOTH stdin and stdout to be terminals: stdout so the
	// operator sees what they consent to (R3.4), stdin so a piped `yes` or a CI
	// heredoc cannot answer for a human. Staged, compare, lint --fix,
	// --mark-auto-disabled, the check's validation prompt and the sweep share it.
	registryPromptIsInteractive func() bool
	// checkRegistryFixer builds the LLM registry fixer --check offers, and
	// checkInteractive asks whether stdin is interactive. Without them the
	// registry-fix loop, and the overlay lock it must run under (S056-R4.6),
	// could only be reached from a terminal with a configured claude CLI.
	checkRegistryFixer func(log *slog.Logger, llmCfg config.LLMConfig) (fixer.RegistryFixer, error)
	checkInteractive   func() bool
	// resolveGentooProvider obtains the ::gentoo provider for the revive flows
	// and for prune, so they can be driven with an on-disk fake.
	resolveGentooProvider func(ctx context.Context, log *slog.Logger, cfg *config.Config) (provider.Provider, error)
	// setVersionsForCheck is the ONE way the check could publish. It is
	// deliberately never called: a test keeps it wired, runs every path and
	// reads the seam afterwards, so R9.2 is proved rather than asserted.
	setVersionsForCheck func(overlayPath string, pins map[string]string) error
	// uiIsTerminal is the ONE point where this package asks "is stdout a
	// terminal?". A `go test` binary writes to a pipe, so without it the
	// on-a-terminal half of R3.7 could not be checked at all.
	uiIsTerminal func() bool
	// sweepPlanner, sweepExecutor and confirmSweep drive `--clean`. The
	// check's validation prompt asks through confirmSweep too.
	sweepPlanner  func(log *slog.Logger, overlayPath string, cfgs map[string]registry.PackageConfig, target string) (autoupdate.SweepBatch, error)
	sweepExecutor func(ctx context.Context, overlayPath string, batch autoupdate.SweepBatch, opts ...autoupdate.SweepOption) autoupdate.SweepReport
	confirmSweep  func(prompt string) bool

	// validateRunner is the `overlay validate` runner. A test has to be able
	// to prove the runner was NOT REACHED, and that is only observable if
	// reaching it goes through a replaceable name.
	validateRunner func(ctx context.Context, opts validate.Options) (validate.Report, error)

	// confirmStagedClean is the y/N question of `overlay staged clean`. It is
	// not confirmSweep: that seam belongs to the overlay sweep, whose prompt
	// covers a published deletion, and one seam shared by two commands would
	// let a test pinning either one answer for the other. The staged planner
	// and executor deliberately get NO seam (see overlay_staged.go).
	confirmStagedClean func(prompt string) bool

	// realignProve is the `compare --realign` prover, so no build ever runs in
	// a test. Its type is realign.Prove's signature, so a change to the
	// contract stops defaultDeps compiling instead of quietly changing what a
	// realignment is proved by. confirmRealignPlan is the y/N question before
	// the ladder runs.
	realignProve       func(ctx context.Context, p realign.Proposal, opts realign.Options) (realign.Proof, error)
	confirmRealignPlan func(prompt string) bool

	// realignPromote is the publisher, typed on realign.Promote's signature on
	// the same discipline as realignProve. Tests that assert how an outcome is
	// REPORTED substitute it; tests that assert what a publication WRITES drive
	// the real one against a fixture overlay. confirmRealignPublish is the
	// per-package y/N question, and realignPublishIsInteractive gates it on
	// BOTH stdin and stdout being terminals, the same probe
	// registryPromptIsInteractive uses, so a piped `yes` cannot answer for a
	// human.
	realignPromote              func(p realign.Proposal, proof realign.Proof, approved bool, overlayRoot string) error
	confirmRealignPublish       func(prompt string) bool
	realignPublishIsInteractive func() bool

	// newClaudeAsker builds the `claude` client both compare reviews ask
	// through, so tests can script the CLI without one being installed and
	// prove `--no-review` reaches it ZERO times (R5.6 of story 025). The
	// budget enters here and nowhere else (see newClaudeCodeAsker).
	newClaudeAsker func(log *slog.Logger, budget time.Duration) (claudeAsker, error)

	// prunePlanner, pruneExecutor and confirmPrune drive `overlay prune`, on
	// the sweep's shape: a test has to be able to prove the executor was NOT
	// REACHED. pruneInteractive reports whether stdin is a terminal, which
	// under `go test` is always no — without it R6.1's confirmation and R4.4's
	// refusal to accept one from a script would be untestable.
	prunePlanner     func(results []overlay.CompareResult, prov provider.Provider, opts overlay.PruneOptions) overlay.PruneBatch
	pruneExecutor    func(batch overlay.PruneBatch, opts overlay.PruneOptions) []overlay.PruneResult
	confirmPrune     func(prompt string) bool
	pruneInteractive func() bool

	// snapshotRunner is the subprocess seam every snapshot verb threads into
	// the snapshot package; tests substitute a MockRunner. Its default is nil,
	// which the snapshot package resolves to its production execRunner: the
	// same resolution the package variable it replaced had (R6.2). It is the
	// one field that is an interface rather than a function.
	snapshotRunner snapshot.Runner
	// snapshotRollbackConfirm is the y/N question of `snapshot rollback`. Its
	// default is nil, which makes snapshot.Rollback ask through its own stdin
	// prompt; tests substitute a yes/no decision without terminal I/O. A plain
	// func(string) bool is assignable to RollbackOptions.Confirm (the
	// unexported confirmFunc type) from package main.
	snapshotRollbackConfirm func(string) bool
	// snapshotRestoreConfirm is the y/N question of `snapshot restore`. Its
	// default is nil, which makes snapshot.Restore fall back to its own stdin
	// prompt (defaultConfirmFunc); tests substitute a yes/no decision without
	// terminal I/O. A plain func(string) bool is assignable to
	// RestoreOptions.Confirm (the unexported confirmFunc type) from package main.
	snapshotRestoreConfirm func(string) bool
}

// defaultDeps returns the production wiring: the values the replaced package
// variables defaulted to. Each call returns a fresh value, so substituting a
// field of one never reaches another.
func defaultDeps() *deps {
	return &deps{
		registryWriter:              registry.SetPackageVersions,
		confirmRegistryWrite:        confirmAction,
		registryPromptIsInteractive: stdinAndStdoutAreTerminals,
		checkRegistryFixer:          newConfiguredRegistryFixer,
		checkInteractive:            stdinIsTerminal,
		resolveGentooProvider:       resolveGentooProvider,
		setVersionsForCheck:         registry.SetPackageVersions,
		uiIsTerminal:                output.IsTerminal,
		sweepPlanner:                autoupdate.PlanOverlaySweep,
		sweepExecutor:               autoupdate.ExecuteOverlaySweep,
		confirmSweep:                confirmAction,
		validateRunner:              validate.Run,
		confirmStagedClean:          confirmAction,
		realignProve:                realign.Prove,
		confirmRealignPlan:          confirmAction,
		realignPromote:              realign.Promote,
		confirmRealignPublish:       confirmAction,
		// The SAME function as registryPromptIsInteractive (design C6).
		realignPublishIsInteractive: stdinAndStdoutAreTerminals,
		newClaudeAsker:              newClaudeCodeAsker,
		prunePlanner:                overlay.PlanPrune,
		pruneExecutor:               overlay.ExecutePrune,
		confirmPrune:                confirmAction,
		pruneInteractive:            stdinIsTerminal,
		// nil on purpose: the snapshot package resolves a nil Runner to its
		// production execRunner, and snapshot.Rollback a nil Confirm to its
		// stdin prompt. Naming those here would change what nil selects.
		snapshotRunner:          nil,
		snapshotRollbackConfirm: nil,
		snapshotRestoreConfirm:  nil,
	}
}

// stdinAndStdoutAreTerminals is the production registryPromptIsInteractive:
// someone is there to answer (stdin) and can see what they answer (stdout).
func stdinAndStdoutAreTerminals() bool {
	return stdinIsTerminal() && output.IsTerminal()
}
