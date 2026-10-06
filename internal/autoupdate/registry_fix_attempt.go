package autoupdate

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"github.com/obentoo/bentoolkit/internal/autoupdate/fixer"
	"github.com/obentoo/bentoolkit/internal/autoupdate/registry"
	"github.com/obentoo/bentoolkit/internal/common/fileutil"
)

// The registry-fix transaction of story 060: it edits the registry through a
// fixer.RegistryFixer, re-checks the package with a fresh Checker and reverts
// the edit unless the re-check passes. It stays in the core because it drives
// the Checker (story 061).

// RegistryFixStatus is the outcome of one AttemptRegistryFix.
type RegistryFixStatus int

const (
	// RegistryFixSkipped means no edit was made: the snapshot or the load of
	// packages.toml failed, so the fixer never ran and nothing was written.
	RegistryFixSkipped RegistryFixStatus = iota
	// RegistryFixReverted means the fixer or the re-check setup failed and the
	// snapshot has already been restored (or RestoreErr says why it could not be).
	RegistryFixReverted
	// RegistryFixPassed means the fresh re-check extracted a version; the edit
	// is kept.
	RegistryFixPassed
	// RegistryFixStillFailing means the re-check still fails; the edit is on
	// disk and the caller settles it by keeping it or calling Revert.
	RegistryFixStillFailing
)

// RegistryFixStage names the step a Skipped or Reverted attempt stopped at, so
// the caller prints the same line it prints today for each of the four. It is
// zero for Passed and StillFailing.
type RegistryFixStage int

const (
	// RegistryFixStageSnapshot: the read or stat of packages.toml failed → Skipped.
	RegistryFixStageSnapshot RegistryFixStage = iota + 1
	// RegistryFixStageLoad: LoadPackagesConfig failed → Skipped.
	RegistryFixStageLoad
	// RegistryFixStageFixer: FixRegistry returned an error → Reverted.
	RegistryFixStageFixer
	// RegistryFixStageChecker: the re-check checker could not be built → Reverted.
	RegistryFixStageChecker
)

// RegistryFixAttempt is the data record of one snapshot-guarded registry fix.
// It carries no terminal output: the caller decides what to print from Status,
// Stage and the errors.
type RegistryFixAttempt struct {
	// Package is the package the attempt was for.
	Package string
	// Status is the attempt's outcome.
	Status RegistryFixStatus
	// Stage is the step a Skipped or Reverted attempt stopped at; zero for
	// Passed and StillFailing.
	Stage RegistryFixStage
	// Result is the agent's result (Model, Summary, DeniedTools); nil when the
	// fixer never ran.
	Result *fixer.RegistryFixResult
	// Recheck is the fresh re-check's result; nil unless a re-check ran.
	Recheck *CheckResult
	// RecheckErr is the still-failing error shown to the operator: the
	// re-check's own Error when it is set, the CheckPackage error otherwise.
	RecheckErr error
	// Err is the RAW cause of a Skipped or Reverted attempt, with no extra
	// wrapping, so the caller's printed %v text is unchanged; nil otherwise.
	Err error
	// RestoreErr is set when the automatic restore of a Reverted attempt failed.
	RestoreErr error

	// configPath, snapshot and mode are the pre-attempt state Revert restores.
	configPath string
	snapshot   []byte
	mode       os.FileMode
}

// AttemptRegistryFix runs one snapshot-guarded fix for pkg: it captures the raw
// packages.toml bytes and mode, hands the package's current entry to registryFixer, and
// re-checks the package through a FRESH Checker from newChecker so the edited
// config is reloaded from disk. Success is decided by that re-check, never by
// the agent's summary.
//
// It never returns with packages.toml in a half-edited state except when Status
// is RegistryFixStillFailing, which the caller settles with Revert or by keeping
// the edit. A fixer or checker-construction error restores the snapshot before
// returning; a failure of that restore is reported in RestoreErr, not returned
// early. Nothing here prints.
func AttemptRegistryFix(ctx context.Context, overlayPath, pkg string, fetchErr error,
	registryFixer fixer.RegistryFixer, newChecker func() (*Checker, error)) *RegistryFixAttempt {
	configDir := filepath.Join(overlayPath, ".autoupdate")
	a := &RegistryFixAttempt{
		Package:    pkg,
		configPath: filepath.Join(configDir, "packages.toml"),
	}

	// Snapshot BEFORE any edit so a failing or erroring fix can be reverted
	// byte-for-byte. Without a snapshot the file must not be edited.
	data, err := os.ReadFile(a.configPath)
	if err != nil {
		return a.skip(RegistryFixStageSnapshot, fmt.Errorf("failed to read packages.toml: %w", err))
	}
	info, err := os.Stat(a.configPath)
	if err != nil {
		return a.skip(RegistryFixStageSnapshot, fmt.Errorf("failed to stat packages.toml: %w", err))
	}
	a.snapshot, a.mode = data, info.Mode()

	// Load the current (broken) config to seed the fix request. No edit has
	// happened yet, so a load failure just skips the package.
	pc, err := registry.LoadPackagesConfig(overlayPath)
	if err != nil {
		return a.skip(RegistryFixStageLoad, err)
	}
	cfg := pc.Packages[pkg]
	var fetchText string
	if fetchErr != nil {
		fetchText = fetchErr.Error()
	}
	req := fixer.RegistryFixRequest{
		Package:    pkg,
		Config:     &cfg,
		FetchError: fetchText,
		ConfigDir:  configDir,
	}

	res, err := registryFixer.FixRegistry(ctx, req)
	a.Result = &res
	if err != nil {
		return a.revertAfter(RegistryFixStageFixer, err)
	}

	c, err := newChecker()
	if err != nil {
		return a.revertAfter(RegistryFixStageChecker, err)
	}
	checkRes, checkErr := c.CheckPackage(ctx, pkg, true)
	a.Recheck = checkRes

	// Pass = no error AND a version was extracted. A benign cache/pending
	// warning leaves checkErr nil with UpstreamVersion set, so the gate is
	// checkErr == nil && version != "", NOT checkRes.Error (which may hold a
	// non-fatal cache warning).
	if checkErr == nil && checkRes != nil && checkRes.UpstreamVersion != "" {
		a.Status = RegistryFixPassed
		return a
	}

	a.Status = RegistryFixStillFailing
	a.RecheckErr = checkErr
	if checkRes != nil && checkRes.Error != nil {
		a.RecheckErr = checkRes.Error
	}
	return a
}

// skip records a Skipped attempt that stopped at stage with the raw cause err.
func (a *RegistryFixAttempt) skip(stage RegistryFixStage, err error) *RegistryFixAttempt {
	a.Status, a.Stage, a.Err = RegistryFixSkipped, stage, err
	return a
}

// revertAfter restores the snapshot after stage failed with the raw cause err
// and records a Reverted attempt. A failed restore lands in RestoreErr so the
// caller can warn and move on to the next package.
func (a *RegistryFixAttempt) revertAfter(stage RegistryFixStage, err error) *RegistryFixAttempt {
	a.Status, a.Stage, a.Err = RegistryFixReverted, stage, err
	a.RestoreErr = a.Revert()
	return a
}

// Revert restores the pre-attempt packages.toml bytes and permission bits
// atomically, through the shared fileutil.WriteFileAtomic — the helper every
// other registry writer uses — so the restored registry carries exactly the
// captured mode whatever the umask and whatever stale temporary file sits beside
// it. A Skipped attempt (the zero Status) never edited the file and may hold no
// snapshot, so reverting it is a no-op rather than a write of empty bytes.
func (a *RegistryFixAttempt) Revert() error {
	if a.Status == RegistryFixSkipped {
		return nil
	}
	if err := fileutil.WriteFileAtomic(a.configPath, a.snapshot, a.mode.Perm()); err != nil {
		return fmt.Errorf("failed to restore %s: %w", a.configPath, err)
	}
	return nil
}

// RepairableFetchFailures returns, in lexical order, the packages whose failure
// wraps ErrFetchFailed — the only class the registry fixer can repair. The match
// is errors.Is, never the error text: a failure that merely says "failed to fetch
// upstream version" is not repairable, and one wrapped twice or joined with
// another error still is.
//
// A failure that also wraps ErrUpstreamUnreachable is excluded: a timeout or a
// TLS EOF is a fetch failure the record did not cause, so offering to rewrite
// the record would invite a distracted "y" to break an entry that was correct.
func RepairableFetchFailures(failures map[string]error) []string {
	pkgs := make([]string, 0, len(failures))
	for pkg, ferr := range failures {
		if errors.Is(ferr, ErrFetchFailed) && !errors.Is(ferr, ErrUpstreamUnreachable) {
			pkgs = append(pkgs, pkg)
		}
	}
	sort.Strings(pkgs)
	return pkgs
}
