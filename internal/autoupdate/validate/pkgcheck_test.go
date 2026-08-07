package validate

// Authored for story 031, sub-task 5.2 — R6, R6.1, R6.2, R6.4.
//
// Written from the contract: design.md fixes
// `PkgcheckFindings(ctx, pkgDir, atom) ([]Finding, Outcome, string)` — findings,
// an outcome, and the reason that outcome carries.
//
// This file pins two package-level seams design.md implies but does not name:
// `execCommand` (the shape applier.go and the CLI commands already use) and
// `lookPath`, so "pkgcheck is absent" is reachable on a host that has it.
//
// Red is DEFERRED to Run mode: the package does not exist yet.

import (
	"context"
	"errors"
	"os/exec"
	"strings"
	"testing"
)

// stubValidateExec points the package exec seam at `printf`, so the stubbed
// command emits stdout exactly and exits 0.
func stubValidateExec(t *testing.T, stdout string) {
	t.Helper()
	orig := execCommand
	execCommand = func(ctx context.Context, _ string, _ ...string) *exec.Cmd {
		return exec.CommandContext(ctx, "printf", "%s", stdout)
	}
	t.Cleanup(func() { execCommand = orig })
}

// stubValidateExecFailing points the seam at a command that exits non-zero.
func stubValidateExecFailing(t *testing.T) {
	t.Helper()
	orig := execCommand
	execCommand = func(ctx context.Context, _ string, _ ...string) *exec.Cmd {
		return exec.CommandContext(ctx, "false")
	}
	t.Cleanup(func() { execCommand = orig })
}

// stubPkgcheckMissing makes the binary lookup fail for the rest of the test.
func stubPkgcheckMissing(t *testing.T) {
	t.Helper()
	orig := lookPath
	lookPath = func(string) (string, error) { return "", errors.New("executable file not found in $PATH") }
	t.Cleanup(func() { lookPath = orig })
}

// TestPkgcheckFindings_AbsentBinaryIsSkippedNotClean is R6.2. A host without
// pkgcheck must not produce a QA section that reads as "nothing wrong".
func TestPkgcheckFindings_AbsentBinaryIsSkippedNotClean(t *testing.T) {
	stubPkgcheckMissing(t)

	findings, outcome, reason := PkgcheckFindings(context.Background(), t.TempDir(), "media-plugins/gst-plugins-qt6")

	if outcome != "SKIPPED" {
		t.Errorf("outcome: got %q, want SKIPPED", outcome)
	}
	if !strings.Contains(reason, "pkgcheck") {
		t.Errorf("reason %q does not name pkgcheck; a skip nobody can read is a pass", reason)
	}
	if len(findings) != 0 {
		t.Errorf("got %d findings from a run that never happened", len(findings))
	}
}

// TestPkgcheckFindings_FailedInvocationIsSkipped pins the same rule for a
// pkgcheck that ran and failed: no findings is not the same as no problems.
func TestPkgcheckFindings_FailedInvocationIsSkipped(t *testing.T) {
	stubValidateExecFailing(t)

	_, outcome, reason := PkgcheckFindings(context.Background(), t.TempDir(), "media-plugins/gst-plugins-qt6")

	if outcome != "SKIPPED" {
		t.Errorf("outcome: got %q, want SKIPPED when the invocation fails", outcome)
	}
	if reason == "" {
		t.Error("a SKIPPED outcome carries an empty reason")
	}
}

// TestPkgcheckFindings_CarriesGateQa pins the field Report.ExitCode selects on.
// Get this wrong and pre-existing QA noise starts failing the whole overlay,
// which is design decision D8's failure mode.
func TestPkgcheckFindings_CarriesGateQa(t *testing.T) {
	stubValidateExec(t, firstFixtureLine(t)+"\n")

	findings, outcome, _ := PkgcheckFindings(context.Background(), t.TempDir(), "media-plugins/gst-plugins-qt6")

	if outcome == "SKIPPED" {
		t.Fatal("a successful invocation reported SKIPPED")
	}
	if len(findings) == 0 {
		t.Fatal("a stream with one record produced no findings")
	}
	for _, f := range findings {
		if f.Gate != "qa" {
			t.Errorf("finding %q carries gate %q, want \"qa\"", f.Detail, f.Gate)
		}
	}
}

// TestPkgcheckFindings_EmptyStreamIsAPass pins that a package pkgcheck had
// nothing to say about is clean rather than skipped — the distinction the whole
// story turns on, applied here too.
func TestPkgcheckFindings_EmptyStreamIsAPass(t *testing.T) {
	stubValidateExec(t, "")

	findings, outcome, reason := PkgcheckFindings(context.Background(), t.TempDir(), "media-plugins/gst-plugins-qt6")

	if outcome != "PASS" {
		t.Errorf("outcome: got %q, want PASS — pkgcheck ran and found nothing", outcome)
	}
	if len(findings) != 0 {
		t.Errorf("got %d findings from an empty stream", len(findings))
	}
	if reason != "" {
		t.Errorf("a PASS carries a reason (%q); reasons belong to skips", reason)
	}
}
