package main

import (
	"errors"
	"regexp"
	"strings"
	"testing"

	"github.com/obentoo/bentoolkit/internal/autoupdate"
)

// Story 079, sub-task 6.2: a waiting bump is rendered and counted apart from
// applied, held, obsolete and failed ones.

// rwaitSplitSummary splits the `--apply all` output into the per-package
// section and the "Apply All Summary" block.
func rwaitSplitSummary(t *testing.T, out string) (perPackage, summary string) {
	t.Helper()
	i := strings.Index(out, "Apply All Summary")
	if i < 0 {
		t.Fatalf("output has no \"Apply All Summary\" header:\n%s", out)
	}
	return out[:i], out[i:]
}

var rwaitWaitingLine = regexp.MustCompile(`(?m)^\s*Waiting:\s+(\d+)\b`)

func TestRequiresWaitingCountedInSummary(t *testing.T) {
	results := []*autoupdate.ApplyResult{
		{Package: "dev-lang/flutter", OldVersion: "3.47.6", NewVersion: "3.48.0", Waiting: []string{"~dev-lang/dart-3.14.0"}},
		{Package: "app-misc/applied", OldVersion: "1.0", NewVersion: "1.1", Success: true},
		{Package: "app-misc/held", OldVersion: "1.0", NewVersion: "1.1", Held: true, HoldReason: "hold = true"},
		{Package: "app-misc/tool", OldVersion: "2.0", NewVersion: "2.1", Waiting: []string{"=dev-libs/libfoo-4.2"}},
		{Package: "app-misc/broken", OldVersion: "1.0", NewVersion: "1.1", Error: errors.New("manifest failed")},
	}

	out := captureStdout(t, func() { displayApplyAllResults(results, 1) })
	perPackage, summary := rwaitSplitSummary(t, out)

	// Rendered: each waiting bump names the atom it waits for, so the two
	// waiting entries stay distinguishable in the per-package section.
	for _, atom := range []string{"~dev-lang/dart-3.14.0", "=dev-libs/libfoo-4.2"} {
		if !strings.Contains(perPackage, atom) {
			t.Errorf("per-package output does not name the unmet atom %q:\n%s", atom, perPackage)
		}
	}

	// Counted apart: two waiting, and neither leaks into Applied or Failed.
	m := rwaitWaitingLine.FindStringSubmatch(summary)
	if m == nil {
		t.Fatalf("summary has no \"Waiting: N\" line:\n%s", summary)
	}
	if m[1] != "2" {
		t.Errorf("summary Waiting count = %s, want 2:\n%s", m[1], summary)
	}
	if !regexp.MustCompile(`(?m)^\s*Applied:\s+1\b`).MatchString(summary) {
		t.Errorf("summary Applied count is not 1 (a waiting bump is not applied):\n%s", summary)
	}
	if !regexp.MustCompile(`(?m)^\s*Held:\s+1\b`).MatchString(summary) {
		t.Errorf("summary Held count is not 1 (a waiting bump is not held):\n%s", summary)
	}
	if !regexp.MustCompile(`(?m)^\s*Failed:\s+1\b`).MatchString(summary) {
		t.Errorf("summary Failed count is not 1 (a waiting bump is not a failure):\n%s", summary)
	}
}

func TestRequiresWaitingNoLineWhenNoneWait(t *testing.T) {
	results := []*autoupdate.ApplyResult{
		{Package: "app-misc/applied", OldVersion: "1.0", NewVersion: "1.1", Success: true},
		{Package: "app-misc/held", OldVersion: "1.0", NewVersion: "1.1", Held: true, HoldReason: "hold = true"},
		nil,
	}

	out := captureStdout(t, func() { displayApplyAllResults(results, 0) })
	_, summary := rwaitSplitSummary(t, out)

	if strings.Contains(out, "Waiting") {
		t.Errorf("output mentions Waiting although no bump waits:\n%s", out)
	}
	if !regexp.MustCompile(`(?m)^\s*Applied:\s+1\b`).MatchString(summary) {
		t.Errorf("summary Applied count is not 1:\n%s", summary)
	}
}
