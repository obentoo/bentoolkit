package overlay

// Authored for story 046, sub-task 5.1 — R1.5, R5.1, R5.2.
//
// Written from the contract: R1.5 — "WHEN a manifest run finishes, THE SYSTEM
// SHALL state how many targets succeeded and how many failed, AS VALUES every
// renderer and the export read from the report" — and design.md D5, which names
// the defect: "a producer today ends its run by handing the Reporter a
// formatted sentence — BatchDone("%d ok, %d failed") — which is a report
// squeezed through a progress channel, where nothing can count it, export it or
// re-render it."
//
// # The contract this file assumes
//
// Three names, each the smallest thing that makes the requirement observable:
//
//	func (ManifestResult) Ok() int      — how many targets succeeded
//	func (ManifestResult) Failed() int  — how many did not
//	ManifestUpdate.Err error            — why a target failed, wrapped
//	ManifestUpdate.Output string        — what the failing command printed
//
// The counts are asked of ManifestResult, which already exists and already
// holds Updates, so the numbers cannot disagree with the rows they summarize.
// RegenerateManifests keeps the signature it has: the run's outcome becomes
// reachable, which is what R1.5 asks, without a signature change this story has
// no other reason to make. If the implementer returns a *ManifestResult
// directly instead, the wrapping below is one line to delete.
//
// Err and Output are separate from the existing Error string on purpose. A
// string cannot be unwrapped, cannot be matched with errors.Is, and cannot
// carry the pkgdev output the operator needs — and D5's whole point is that the
// facts stop being flattened into text on the way out.
//
// Red on arrival: ManifestResult has no Ok/Failed, ManifestUpdate has no
// Err/Output.
//
// It is authored beside manifest_test.go, not over it: that file holds the
// scope, target-resolution and rollback tests this story does not touch.
//
// lookPath, execCommand and recManifestReporter are the package's own seams and
// its recording reporter (manifest.go, manifest_reporter_test.go).

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// manifestTargets writes package directories under a temp overlay and returns
// the targets naming them.
func manifestTargets(t *testing.T, atoms ...string) (overlay string, targets []ManifestUpdate) {
	t.Helper()

	overlay = t.TempDir()
	for _, atom := range atoms {
		category, pkg, ok := strings.Cut(atom, "/")
		if !ok {
			t.Fatalf("fixture atom %q is not category/package", atom)
		}
		if err := os.MkdirAll(filepath.Join(overlay, category, pkg), 0o755); err != nil {
			t.Fatalf("creating %s: %v", atom, err)
		}
		targets = append(targets, ManifestUpdate{Category: category, Package: pkg})
	}
	return overlay, targets
}

// stubPkgdev makes pkgdev discoverable and replaces each invocation with a
// shell command whose success is decided by outcomes, in target order.
//
// The failing command writes to stderr and exits non-zero, which is what a real
// pkgdev failure looks like: the output is the diagnostic the operator needs,
// and losing it is the second half of the defect this sub-task removes.
func stubPkgdev(t *testing.T, outcomes ...bool) {
	t.Helper()

	oldLook, oldExec := lookPath, execCommand
	t.Cleanup(func() { lookPath, execCommand = oldLook, oldExec })

	lookPath = func(string) (string, error) { return "/usr/bin/pkgdev", nil }

	call := 0
	execCommand = func(ctx context.Context, _ string, _ ...string) *exec.Cmd {
		ok := true
		if call < len(outcomes) {
			ok = outcomes[call]
		}
		call++
		if ok {
			return exec.CommandContext(ctx, "sh", "-c", "printf 'manifest written\\n'")
		}
		return exec.CommandContext(ctx, "sh", "-c", "printf 'FAIL-OUT: could not fetch distfile\\n' 1>&2; exit 1")
	}
}

// TestManifestRunCountsOkAndFailed pins R1.5. The counts are values the caller
// holds, so the same numbers can go to a renderer, to the export and to the
// summary line — instead of existing only inside a sentence handed to a
// progress channel.
//
// Every case also RECONCILES: ok plus failed is the number of targets. A target
// counted in neither column, or in both, is the one failure a pair of
// independent counters has, and it is invisible to a test that checks only that
// the numbers are plausible.
func TestManifestRunCountsOkAndFailed(t *testing.T) {
	cases := map[string]struct {
		atoms      []string
		outcomes   []bool
		wantOk     int
		wantFailed int
	}{
		"all succeed": {[]string{"c/a", "c/b"}, []bool{true, true}, 2, 0},
		"all fail":    {[]string{"c/a", "c/b"}, []bool{false, false}, 0, 2},
		"one of each": {[]string{"c/a", "c/b"}, []bool{true, false}, 1, 1},
		"one target":  {[]string{"c/a"}, []bool{true}, 1, 0},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			overlay, targets := manifestTargets(t, tc.atoms...)
			stubPkgdev(t, tc.outcomes...)

			result := RegenerateManifests(t.Context(), overlay, targets, &ManifestOptions{Jobs: 1, Keep: true})

			if got := result.Ok(); got != tc.wantOk {
				t.Errorf("Ok() = %d, want %d", got, tc.wantOk)
			}
			if got := result.Failed(); got != tc.wantFailed {
				t.Errorf("Failed() = %d, want %d", got, tc.wantFailed)
			}
			if sum := result.Ok() + result.Failed(); sum != len(result.Updates) {
				t.Errorf("Ok()+Failed() = %d over %d targets — a target counted twice, or in no column at all, makes both numbers unusable to the report that sums them (R1.5)",
					sum, len(result.Updates))
			}
		})
	}
}

// TestFailedTargetCarriesItsWrappedErrorAndOutput pins R5.1 for the failing
// half: what the library learned about a failure reaches the caller as an
// error it can unwrap and as the output it can show, not as a sentence.
//
// The identifiers are asserted because an error without the atom that produced
// it is unactionable — the repository's Go convention says so, and a run with
// ten targets is exactly where it stops being obvious which one failed.
func TestFailedTargetCarriesItsWrappedErrorAndOutput(t *testing.T) {
	overlay, targets := manifestTargets(t, "c/a", "c/b")
	stubPkgdev(t, true, false)

	updates := RegenerateManifests(t.Context(), overlay, targets, &ManifestOptions{Jobs: 1, Keep: true}).Updates
	if len(updates) != 2 {
		t.Fatalf("got %d updates, want 2", len(updates))
	}

	failed := updates[1]
	if failed.Success {
		t.Fatalf("the second target reports success although its command exited non-zero: %+v", failed)
	}

	if failed.Err == nil {
		t.Fatal("a failed target carries no error value — the caller receives a string it cannot unwrap, match or wrap further (R5.1)")
	}
	if errors.Unwrap(failed.Err) == nil {
		t.Errorf("the failure error wraps nothing: %v — %%w is what keeps the cause reachable", failed.Err)
	}
	if !strings.Contains(failed.Err.Error(), "c/b") {
		t.Errorf("the failure error does not name the target it belongs to: %v — an error without its identifier cannot be reproduced", failed.Err)
	}
	if !strings.Contains(failed.Output, "FAIL-OUT") {
		t.Errorf("the failing command's output did not reach the caller: %q — it is the diagnostic the operator acts on, and the terminal that showed it is gone (R5.2)", failed.Output)
	}

	// The succeeding target must carry neither, or "did it fail?" becomes a
	// question about whether a field happens to be populated.
	if updates[0].Err != nil {
		t.Errorf("a succeeding target carries an error: %v", updates[0].Err)
	}
}

// TestManifestRunStillReportsToTheReporter pins what must NOT change. D5 is
// explicit: tui.Reporter keeps its interface and its consumers, the live region
// during a run is still driven by it, and the report is a separate artifact.
// Returning the counts must not cost the operator the progress they see while
// the run is happening.
func TestManifestRunStillReportsToTheReporter(t *testing.T) {
	overlay, targets := manifestTargets(t, "c/a", "c/b")
	stubPkgdev(t, true, false)

	rec := &recManifestReporter{}
	RegenerateManifests(t.Context(), overlay, targets, &ManifestOptions{Jobs: 1, Keep: true, Reporter: rec})

	events := rec.snap()
	for _, want := range []string{"batchstart:2", "start:c/a", "done:c/a:true", "start:c/b", "done:c/b:false", "batchdone"} {
		if !mfHas(events, want) {
			t.Errorf("the reporter never saw %q — the live region lost an event the report was supposed to leave alone (D5)\nevents: %v", want, events)
		}
	}
}
