package main

// Authored for story 031, sub-task 6.1 — R5, R5.1, R5.2, R5.3, R5.4, R5.7.
//
// Written from the contract: design.md's CLI table fixes the three selector
// forms and the three exit codes.
//
// This file pins the seam `validateRunnerFn`, in the shape overlay_prune.go
// already uses — and for the same reason that file states: a test has to be
// able to prove the runner was NOT REACHED, which is only observable if
// reaching it goes through a replaceable name.
//
// captureExit comes from snapshot_test.go in this package.
//
// Red is DEFERRED to Run mode: the command does not exist yet.

import (
	"context"
	"testing"

	"github.com/obentoo/bentoolkit/internal/autoupdate/validate"
)

// There is deliberately NO overlay fixture in this file. The runner is stubbed
// in every test below, so nothing ever scans a tree — what is under test here
// is SELECTION: which options the command hands the runner, and whether it
// reaches the runner at all. The runner's own behaviour over a real tree is
// run_test.go's and golden_test.go's job, in the validate package.

// stubValidateRunner captures the options the command hands the runner and
// answers with a clean report, so the assertions are about SELECTION only.
func stubValidateRunner(t *testing.T, report validate.Report) *validate.Options {
	t.Helper()
	seen := &validate.Options{}
	orig := validateRunnerFn
	validateRunnerFn = func(_ context.Context, opts validate.Options) (validate.Report, error) {
		*seen = opts
		return report, nil
	}
	t.Cleanup(func() { validateRunnerFn = orig })
	return seen
}

func TestOverlayValidate_NoSelectorTakesTheWholeOverlay(t *testing.T) {
	seen := stubValidateRunner(t, validate.Report{})

	code, exited := captureExit(t, func() {
		runValidate(newValidateCmd(), []string{})
	})

	if exited && code != 0 {
		t.Errorf("exit code: got %d, want 0", code)
	}
	if seen.Selector != "" {
		t.Errorf("Selector: got %q, want empty for a whole-overlay run", seen.Selector)
	}
}

func TestOverlayValidate_CategorySelectorNarrows(t *testing.T) {
	seen := stubValidateRunner(t, validate.Report{})

	captureExit(t, func() {
		runValidate(newValidateCmd(), []string{"media-plugins"})
	})

	if seen.Selector != "media-plugins" {
		t.Errorf("Selector: got %q, want %q", seen.Selector, "media-plugins")
	}
}

func TestOverlayValidate_PackageSelectorNarrows(t *testing.T) {
	seen := stubValidateRunner(t, validate.Report{})

	captureExit(t, func() {
		runValidate(newValidateCmd(), []string{"media-plugins/gst-plugins-qt6"})
	})

	if seen.Selector != "media-plugins/gst-plugins-qt6" {
		t.Errorf("Selector: got %q, want %q", seen.Selector, "media-plugins/gst-plugins-qt6")
	}
}

// TestOverlayValidate_UnknownSelectorExitsTwo is R5.7. Exit 2 is the usage
// code: the operator asked about something that is not there, which is neither
// a clean run nor a finding.
func TestOverlayValidate_UnknownSelectorExitsTwo(t *testing.T) {
	stubValidateRunner(t, validate.Report{UnmatchedSelector: "media-plugins/does-not-exist"})

	code, exited := captureExit(t, func() {
		runValidate(newValidateCmd(), []string{"media-plugins/does-not-exist"})
	})

	if !exited {
		t.Fatal("the command did not exit")
	}
	if code != 2 {
		t.Errorf("exit code: got %d, want 2", code)
	}
}

// TestOverlayValidate_ErrorFindingExitsOne pins the difference between a gate
// that found something and a command that was used wrongly.
func TestOverlayValidate_ErrorFindingExitsOne(t *testing.T) {
	stubValidateRunner(t, validate.Report{
		Results: []validate.EbuildResult{{
			Package: "media-plugins/gst-plugins-qt6",
			Version: "1.29.2",
			Gates: []validate.GateResult{{
				Gate:    validate.GateOptions,
				Outcome: validate.OutcomeFailed,
				Findings: []validate.Finding{
					{Gate: validate.GateOptions, Severity: validate.SeverityError, Detail: "-Daalib= is undeclared upstream"},
				},
			}},
		}},
	})

	code, exited := captureExit(t, func() {
		runValidate(newValidateCmd(), []string{"media-plugins/gst-plugins-qt6"})
	})

	if !exited || code != 1 {
		t.Errorf("exit code: got %d (exited=%v), want 1", code, exited)
	}
}

// TestOverlayValidate_UnknownSelectorNeverReachesTheRunner is the assertion the
// seam exists for: a usage error must be answered without doing any work.
func TestOverlayValidate_UnknownSelectorNeverReachesTheRunner(t *testing.T) {
	var reached bool
	orig := validateRunnerFn
	validateRunnerFn = func(context.Context, validate.Options) (validate.Report, error) {
		reached = true
		return validate.Report{}, nil
	}
	t.Cleanup(func() { validateRunnerFn = orig })

	captureExit(t, func() {
		runValidate(newValidateCmd(), []string{"not-a-category/nor-a-package/too-many-parts"})
	})

	if reached {
		t.Error("a malformed selector reached the runner; it should be rejected before any work")
	}
}
