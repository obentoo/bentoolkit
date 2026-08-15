package autoupdate

// Authored for story 035, sub-task 3.1 — R3.1, and R2.1/R2.2 on this path.
//
// A CORRECTION TO THE PLAN'S TARGET FILE, on the same grounds
// applier_golden_test.go already records for story 033. tasks.md names
// `cmd/bentoo/overlay_autoupdate_check_test.go`, and this assertion cannot live
// there: the cases in that file drive the cobra command against a stubbed
// runner, so they never construct an Applier and never reach the manifest step
// whose distdir is the whole subject here. `(*Applier).Validate` — the `--check`
// runner — lives in package autoupdate, so the test does too.
//
// # Why --check needs the same rule and not a lighter one
//
// `--check` publishes nothing, which makes its failure mode quieter rather than
// smaller. An apply that reads an empty distdir publishes an unread bump and the
// damage is visible in the overlay; a check that reads an empty distdir prints
// "proved" for a bump nothing read, the operator believes the sweep, and the
// apply that follows is authorised by a report that measured nothing.
//
// Both callers run the same runStaticGates against the same staged tree. One
// rule, both callers.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/obentoo/bentoolkit/internal/autoupdate/validate"
)

// TestCheckPath_ReadsWhatTheRunFetched is the golden pair again, through
// Validate instead of Apply, on a host that has never fetched the release.
func TestCheckPath_ReadsWhatTheRunFetched(t *testing.T) {
	sandbox := t.TempDir()
	prev := fixSandboxRoot
	fixSandboxRoot = func() string { return sandbox }
	t.Cleanup(func() { fixSandboxRoot = prev })

	applier, _, overlayDir, _ := goldenApplyFixtureWith(t, goldenFixtureOpts{
		omitCandidateArchive: true,
		fetchingPkgdev:       true,
	})
	const pkg = "media-plugins/gst-plugins-qt6"

	before := hashOverlayTree(t, overlayDir)
	result := applier.Validate(pkg, validate.DepthOptions)

	// The option gate must have an OPINION. Before this story it reported
	// SKIPPED — "the only distfile present does not belong to version 1.29.2" —
	// which on the apply path promotes and here reads as "nothing was wrong".
	var options *validate.GateResult
	for i := range result.Gates {
		if result.Gates[i].Gate == validate.GateOptions {
			options = &result.Gates[i]
			break
		}
	}
	if options == nil {
		t.Fatalf("the check produced no option gate at all: %+v", result.Gates)
	}
	if options.Outcome != validate.OutcomeFailed {
		t.Errorf("the option gate reported %s, want FAILED\nreason: %q\n"+
			"upstream 1.29.2 declares neither aalib nor libcaca and the ebuild passes both; a gate that could not "+
			"read the archive this very run fetched is answering about nothing",
			options.Outcome, options.Reason)
	}

	detail := options.Reason
	for _, f := range options.Findings {
		detail += " " + f.Detail
	}
	for _, want := range []string{"aalib", "libcaca"} {
		if !strings.Contains(detail, want) {
			t.Errorf("the option gate does not name %q: %q", want, detail)
		}
	}

	// R2.1/R2.2 on this path: the fetched distfiles do not outlive the run.
	entries, err := os.ReadDir(sandbox)
	if err != nil {
		t.Fatalf("reading the sandbox root: %v", err)
	}
	var leaked []string
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), "bentoo-staged-distfiles-") {
			leaked = append(leaked, e.Name())
		}
	}
	if len(leaked) != 0 {
		t.Errorf("the check left %v under the sandbox root; a sweep over the whole registry would keep one "+
			"archive per package it never asked to keep", leaked)
	}

	// And the promise --check exists for: nothing is published, whatever the
	// gates said.
	if after := hashOverlayTree(t, overlayDir); after != before {
		t.Errorf("--check changed the published overlay: %s -> %s", before, after)
	}
	candidate := filepath.Join(overlayDir, "media-plugins", "gst-plugins-qt6", "gst-plugins-qt6-1.29.2.ebuild")
	if _, err := os.Stat(candidate); err == nil {
		t.Errorf("--check wrote the candidate into the published overlay at %q", candidate)
	}
}
