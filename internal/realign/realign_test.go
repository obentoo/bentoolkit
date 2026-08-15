// MERGE FRAGMENT — story 034, sub-task 7.1 (stage and gate a realignment).
//
// Target file: internal/realign/realign_test.go  (NEW file, package realign)
// Sub-task 7.2 appends to this same file and reuses `realignTreeHash`,
// `realignStageFixture` and the seam swap helper below; none of them is
// re-declared there.
//
// Pinned contract:
//
//	type Proposal struct {
//	    Category, Package, Version string
//	    Ebuild                     []byte // the realigned bytes, proved as-is
//	}
//	type Proof struct {
//	    StagedRoot string
//	    Gates      []validate.GateResult
//	    Passed     bool
//	}
//	type Options struct {
//	    Overlay, StagingRoot string
//	    Depth                validate.Depth
//	    Deps                 validate.BuildDeps
//	}
//	func Prove(ctx context.Context, p Proposal, opts Options) (Proof, error)
//
//	// package-level seams over story 033, swapped by tests and restored with
//	// t.Cleanup — the discipline validate/archive.go:21-24 already states for
//	// exec.CommandContext, applied one level up.
//	var stageRealignment = validate.Stage
//	var runRealignGates  = validate.RunBuildGates
//
// The seams are typed as the 033 functions themselves, which is how "consumed
// unchanged" is held mechanically: if either signature moves, the var assignment
// stops compiling. The explicit compile-time assertions in
// TestRealignConsumesStory033Unchanged say the same thing where a reader will
// see it. Nothing here wraps or re-declares those functions.
//
// Two notes on where this lives and what it drives:
//
//  1. This package exists because of an import edge. internal/overlay imports
//     NOTHING from internal/autoupdate today, and prune.go's RegistryKeys
//     comment states the reason outright: the keys are supplied by the caller
//     and never looked up there, "which is what keeps this package free of an
//     import edge to internal/autoupdate". Putting realign.go in
//     internal/overlay would have overturned that boundary silently. So Stage 2
//     lives in internal/realign, which imports internal/overlay for the proposal
//     and internal/autoupdate/validate for the proof. overlay stays clean,
//     cmd/bentoo stays thin, and the orchestration stays in an ordinary,
//     testable package.
//
//     One consequence for these tests: they can no longer borrow
//     internal/overlay's unexported test helpers. `writeVerifyEbuild` is
//     replaced by the local `realignWriteEbuild` below. Nothing else here
//     reached into that package's unexported state, so no assertion was lost.
//
//  2. RunBuildGates' seam is swapped rather than driven through
//     validate.BuildDeps. Driving the real gate runner would mean a real
//     `ebuild … clean compile`: a network fetch, a writable DISTDIR and portage
//     on the host, all three of which this story's constraints forbid a test to
//     need. The real ladder is exercised by story 033's own tests and by task
//     9.1's live-tagged rung; what group 7 owes is that it CALLS the ladder,
//     with the depth it was asked for, and adds nothing to it.
package realign

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/obentoo/bentoolkit/internal/autoupdate/validate"
)

const (
	realignPublishedEbuild = "EAPI=8\ninherit meson python-any-r1\nIUSE=\"qml wayland\"\n"
	realignProposedEbuild  = "EAPI=8\ninherit gstreamer-meson\nIUSE=\"qml\"\n"
)

// realignTreeHash fingerprints a whole directory tree: every relative path in
// sorted order, each followed by the bytes of the file at it.
//
// It is the assertion R5.4 actually rests on. "The overlay was not modified" is
// a claim about the whole tree — a promotion that wrote the right ebuild and
// also dropped a stray Manifest, or removed a sibling version, satisfies any
// per-file check and fails this one. Sorted paths keep it deterministic;
// directory entries are included so an added empty directory still shows.
func realignTreeHash(t *testing.T, root string) string {
	t.Helper()
	var paths []string
	err := filepath.Walk(root, func(path string, info os.FileInfo, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		rel, relErr := filepath.Rel(root, path)
		if relErr != nil {
			return relErr
		}
		paths = append(paths, rel)
		return nil
	})
	if err != nil {
		t.Fatalf("walking %s: %v", root, err)
	}
	sort.Strings(paths)

	sum := sha256.New()
	for _, rel := range paths {
		sum.Write([]byte(rel))
		sum.Write([]byte{0})
		info, statErr := os.Stat(filepath.Join(root, rel))
		if statErr != nil {
			t.Fatalf("stat %s: %v", rel, statErr)
		}
		if info.IsDir() {
			continue
		}
		data, readErr := os.ReadFile(filepath.Join(root, rel))
		if readErr != nil {
			t.Fatalf("read %s: %v", rel, readErr)
		}
		sum.Write(data)
		sum.Write([]byte{0})
	}
	return hex.EncodeToString(sum.Sum(nil))
}

// realignWriteEbuild writes <root>/<category>/<pkg>/<pkg>-<version>.ebuild.
//
// internal/overlay's writeVerifyEbuild does the same thing, and this is
// deliberately not an import of it: a test helper is unexported and lives with
// its package's tests, so a second package gets its own four lines rather than
// an export that exists only for tests.
func realignWriteEbuild(t *testing.T, root, category, pkg, version, body string) {
	t.Helper()
	dir := filepath.Join(root, category, pkg)
	if err := os.MkdirAll(dir, 0o750); err != nil {
		t.Fatalf("mkdir %s: %v", dir, err)
	}
	if err := os.WriteFile(filepath.Join(dir, pkg+"-"+version+".ebuild"), []byte(body), 0o600); err != nil {
		t.Fatalf("write %s: %v", dir, err)
	}
}

// realignStageFixture publishes one ebuild in an overlay and returns the overlay
// root, a staging root OUTSIDE it, and the proposal that would realign it.
//
// The staging root is a sibling of the overlay, never a child: R5.1 says the
// staged tree lives outside the published overlay, and a fixture that put it
// inside would make TestProveStagesOutsideTheOverlay unfalsifiable.
func realignStageFixture(t *testing.T) (overlayRoot, stagingRoot string, proposal Proposal) {
	t.Helper()
	base := t.TempDir()
	overlayRoot = filepath.Join(base, "overlay")
	stagingRoot = filepath.Join(base, "staging")
	if err := os.MkdirAll(stagingRoot, 0o750); err != nil {
		t.Fatalf("mkdir staging: %v", err)
	}
	realignWriteEbuild(t, overlayRoot, "media-libs", "gst-plugins-qt6", "1.29.2", realignPublishedEbuild)

	return overlayRoot, stagingRoot, Proposal{
		Category: "media-libs",
		Package:  "gst-plugins-qt6",
		Version:  "1.29.2",
		Ebuild:   []byte(realignProposedEbuild),
	}
}

// realignSeamRecorder captures what the realignment path asked story 033 to do
// and answers with a canned result, so no build ever runs.
type realignSeamRecorder struct {
	stageCalls []validate.StageRequest
	buildCalls []validate.BuildRequest
	stagedRoot string
	gates      []validate.GateResult
	stageErr   error
	gatesErr   error
}

// realignSwapSeams installs the recorder and restores the real functions with
// t.Cleanup, the convention this repository uses everywhere a package-level seam
// stands in for a subprocess.
func realignSwapSeams(t *testing.T, rec *realignSeamRecorder) {
	t.Helper()
	origStage, origGates := stageRealignment, runRealignGates
	t.Cleanup(func() { stageRealignment, runRealignGates = origStage, origGates })

	stageRealignment = func(req validate.StageRequest) (string, error) {
		rec.stageCalls = append(rec.stageCalls, req)
		if rec.stageErr != nil {
			return "", rec.stageErr
		}
		// A real staged tree, so "outside the overlay" is a claim about a path
		// that exists rather than about a string.
		if err := os.MkdirAll(rec.stagedRoot, 0o750); err != nil {
			t.Fatalf("creating the staged root: %v", err)
		}
		return rec.stagedRoot, nil
	}
	runRealignGates = func(_ context.Context, req validate.BuildRequest, _ validate.BuildDeps) ([]validate.GateResult, error) {
		rec.buildCalls = append(rec.buildCalls, req)
		return rec.gates, rec.gatesErr
	}
}

func realignPassingGates() []validate.GateResult {
	return []validate.GateResult{
		{Gate: "options", Outcome: validate.OutcomePass},
		{Gate: "configure", Outcome: validate.OutcomeSkipped, Reason: "portage is not available on this host"},
	}
}

// TestRealignConsumesStory033Unchanged is the "add no gate, invent no wrapper"
// assertion, held at compile time.
//
// # Why every declaration below is //nolint:staticcheck
//
// staticcheck's QF1011 reads `var _ T = expr` as a redundant annotation and
// offers to drop the T, because the right-hand side already has that type. Here
// the T IS the assertion and the right-hand side is the thing under test: drop
// it and `var _ = validate.Stage` states nothing at all, so the suggested
// simplification would delete the only check in this function while leaving it
// compiling and green. The signature drift these four lines exist to catch — a
// parameter added to Stage, a return value moved on RunBuildGates — would then
// reach a reader as nothing.
//
// _Requirements: R5, R5.1, R5.2_
func TestRealignConsumesStory033Unchanged(t *testing.T) {
	var _ func(validate.StageRequest) (string, error) = validate.Stage                                                             //nolint:staticcheck // QF1011: the explicit type is the assertion
	var _ func(context.Context, validate.BuildRequest, validate.BuildDeps) ([]validate.GateResult, error) = validate.RunBuildGates //nolint:staticcheck // QF1011: the explicit type is the assertion

	// The seams are the SAME functions, not adapters around them. An adapter is
	// where a second copy of the ladder starts.
	var _ func(validate.StageRequest) (string, error) = stageRealignment                                                    //nolint:staticcheck // QF1011: the explicit type is the assertion
	var _ func(context.Context, validate.BuildRequest, validate.BuildDeps) ([]validate.GateResult, error) = runRealignGates //nolint:staticcheck // QF1011: the explicit type is the assertion
}

// TestProveStagesAndGatesAtTheRequestedDepth is R5.1 and R5.2: the
// realigned ebuild is materialised in a staged tree and the ladder runs to the
// depth the operator asked for — not to a depth this package chose.
//
// _Requirements: R5, R5.1, R5.2_
func TestProveStagesAndGatesAtTheRequestedDepth(t *testing.T) {
	overlayRoot, stagingRoot, proposal := realignStageFixture(t)
	rec := &realignSeamRecorder{
		stagedRoot: filepath.Join(stagingRoot, "media-libs", "gst-plugins-qt6", "1.29.2"),
		gates:      realignPassingGates(),
	}
	realignSwapSeams(t, rec)

	proof, err := Prove(context.Background(), proposal, Options{
		Overlay:     overlayRoot,
		StagingRoot: stagingRoot,
		Depth:       validate.DepthConfigure,
	})
	if err != nil {
		t.Fatalf("Prove returned %v, want nil", err)
	}

	if len(rec.stageCalls) != 1 {
		t.Fatalf("validate.Stage was called %d times, want 1", len(rec.stageCalls))
	}
	staged := rec.stageCalls[0]
	if string(staged.EbuildBytes) != realignProposedEbuild {
		t.Errorf("Stage received %q, want the proposed bytes verbatim — what is proved must be what was proposed (R5.5 begins here)", staged.EbuildBytes)
	}
	if staged.Atom != "media-libs/gst-plugins-qt6" || staged.Version != "1.29.2" {
		t.Errorf("Stage received atom %q version %q, want media-libs/gst-plugins-qt6 at 1.29.2", staged.Atom, staged.Version)
	}

	if len(rec.buildCalls) != 1 {
		t.Fatalf("validate.RunBuildGates was called %d times, want 1", len(rec.buildCalls))
	}
	build := rec.buildCalls[0]
	if build.Depth != validate.DepthConfigure {
		t.Errorf("the gates ran at depth %v, want %v — a realignment proved at a shallower depth than requested is a proof of something the operator did not ask about (R5.2)", build.Depth, validate.DepthConfigure)
	}
	if build.StagedRoot != rec.stagedRoot {
		t.Errorf("the gates ran against %q, want the staged root %q; gating anything else proves the wrong bytes", build.StagedRoot, rec.stagedRoot)
	}
	if !proof.Passed {
		t.Error("Passed is false although every gate reported PASS or SKIPPED; R5.3 accepts both")
	}
}

// TestProveAddsNoGate is an explicit Out of Scope item: "Stage 2 runs
// story 033's ladder and adds nothing to it."
//
// The temptation is real and specific — a "matches ::gentoo now" check would be
// cheap to append here and would look like a gate, which is exactly how the
// realignment path would acquire an authority the design gave to three separate
// parties.
//
// _Requirements: R5, R5.2_
func TestProveAddsNoGate(t *testing.T) {
	overlayRoot, stagingRoot, proposal := realignStageFixture(t)
	want := realignPassingGates()
	rec := &realignSeamRecorder{stagedRoot: filepath.Join(stagingRoot, "staged"), gates: want}
	realignSwapSeams(t, rec)

	proof, err := Prove(context.Background(), proposal, Options{
		Overlay: overlayRoot, StagingRoot: stagingRoot, Depth: validate.DepthOptions,
	})
	if err != nil {
		t.Fatalf("Prove returned %v, want nil", err)
	}

	if len(proof.Gates) != len(want) {
		t.Fatalf("the proof carries %d gates and the ladder returned %d; this story adds no gate: %+v", len(proof.Gates), len(want), proof.Gates)
	}
	for i := range want {
		if proof.Gates[i].Gate != want[i].Gate || proof.Gates[i].Outcome != want[i].Outcome {
			t.Errorf("gate %d is %+v, want %+v — the ladder's results are reported as they came back, in order", i, proof.Gates[i], want[i])
		}
	}
}

// TestProveStagesOutsideTheOverlay is R5.1, and it is a security
// property rather than a tidiness one: the overlay auto-commits, so anything
// materialised inside it is published within minutes of being written, before
// any gate has said whether it builds.
//
// _Requirements: R5, R5.1, R5.4_
func TestProveStagesOutsideTheOverlay(t *testing.T) {
	overlayRoot, stagingRoot, proposal := realignStageFixture(t)
	rec := &realignSeamRecorder{stagedRoot: filepath.Join(stagingRoot, "staged"), gates: realignPassingGates()}
	realignSwapSeams(t, rec)

	before := realignTreeHash(t, overlayRoot)

	proof, err := Prove(context.Background(), proposal, Options{
		Overlay: overlayRoot, StagingRoot: stagingRoot, Depth: validate.DepthCompile,
	})
	if err != nil {
		t.Fatalf("Prove returned %v, want nil", err)
	}

	rel, relErr := filepath.Rel(overlayRoot, proof.StagedRoot)
	if relErr == nil && !strings.HasPrefix(rel, "..") {
		t.Errorf("the staged tree %q sits inside the published overlay %q; the overlay auto-commits, so staging inside it publishes an unproved ebuild (R5.1)", proof.StagedRoot, overlayRoot)
	}
	if got := realignTreeHash(t, overlayRoot); got != before {
		t.Errorf("proving a realignment changed the published overlay (hash %s -> %s); nothing is published before approval (R5.4)", before, got)
	}
}

// TestProveReportsAFailingGate: a gate that fails is an outcome the
// report carries, not an error that aborts the run, and it is emphatically not a
// promotion. Task 7.2 owns the refusal to publish; what 7.1 owes is that the
// failure arrives intact and the overlay is untouched on the way.
//
// _Requirements: R5, R5.2, R5.4_
func TestProveReportsAFailingGate(t *testing.T) {
	overlayRoot, stagingRoot, proposal := realignStageFixture(t)
	rec := &realignSeamRecorder{
		stagedRoot: filepath.Join(stagingRoot, "staged"),
		gates: []validate.GateResult{
			{Gate: "options", Outcome: validate.OutcomePass},
			{Gate: "configure", Outcome: validate.OutcomeFailed, Reason: "meson: unknown option -Dqml"},
		},
	}
	realignSwapSeams(t, rec)

	before := realignTreeHash(t, overlayRoot)

	proof, err := Prove(context.Background(), proposal, Options{
		Overlay: overlayRoot, StagingRoot: stagingRoot, Depth: validate.DepthConfigure,
	})
	if err != nil {
		t.Fatalf("Prove returned %v, want nil — a FAILED gate is a result, not a broken run", err)
	}
	if proof.Passed {
		t.Error("Passed is true with a FAILED gate; R5.3 promotes only when every gate reported PASS or SKIPPED")
	}
	if len(proof.Gates) != 2 || proof.Gates[1].Reason == "" {
		t.Errorf("the failing gate reached the proof without its reason: %+v — 'it failed' that cannot say why is unactionable", proof.Gates)
	}
	if got := realignTreeHash(t, overlayRoot); got != before {
		t.Errorf("a failed proof changed the published overlay (hash %s -> %s) (R5.4)", before, got)
	}
}

// TestProveStagingFailureIsAnError separates the two ways a proof can
// end without a promotion. A gate that says no is an ANSWER; a staging step that
// could not run has produced none, and reporting it as `Passed=false` would let
// "we could not look" read as "it does not build".
//
// _Requirements: R5, R5.1_
func TestProveStagingFailureIsAnError(t *testing.T) {
	overlayRoot, stagingRoot, proposal := realignStageFixture(t)
	rec := &realignSeamRecorder{stageErr: errors.New("staging root is not writable")}
	realignSwapSeams(t, rec)

	before := realignTreeHash(t, overlayRoot)

	proof, err := Prove(context.Background(), proposal, Options{
		Overlay: overlayRoot, StagingRoot: stagingRoot, Depth: validate.DepthOptions,
	})
	if err == nil {
		t.Fatal("Prove returned nil error although staging failed; an unstaged realignment was never examined and must not read as an examined one")
	}
	if proof.Passed {
		t.Error("Passed is true after a staging failure")
	}
	if len(rec.buildCalls) != 0 {
		t.Errorf("the gates ran %d time(s) after staging failed; there is nothing to gate", len(rec.buildCalls))
	}
	if got := realignTreeHash(t, overlayRoot); got != before {
		t.Errorf("a failed staging attempt changed the published overlay (hash %s -> %s)", before, got)
	}
}

// Pinned contract:
//
//	func Promote(p Proposal, proof Proof, approved bool, overlayRoot string) error
//
// `approved` is a bare bool parameter rather than a prompt this function runs,
// and that is the design decision D8 asks for held at the signature: the model
// proposes, the gates prove, the maintainer approves, and no two of those are
// collapsed. The prompt itself is a TTY concern and lives in task 7.3, where
// cobra and the terminal are. A function that asked the question itself could
// not be tested for refusing.
//
// Every case below hashes the WHOLE published overlay before and after. R5.4
// says the published overlay is left byte-identical, which is a claim about the
// tree: a refusal that wrote the right nothing and also dropped a Manifest, or
// removed a sibling version, passes any per-file check and fails this one.

// realignPublishedPath is where the fixture's ebuild lives in the overlay.
func realignPublishedPath(overlayRoot string) string {
	return filepath.Join(overlayRoot, "media-libs", "gst-plugins-qt6", "gst-plugins-qt6-1.29.2.ebuild")
}

// realignProvenProof is what Prove returns for a realignment that
// cleared the ladder: PASS and SKIPPED only, which R5.3 accepts as passing.
func realignProvenProof(t *testing.T, stagingRoot string) Proof {
	t.Helper()
	staged := filepath.Join(stagingRoot, "staged")
	if err := os.MkdirAll(staged, 0o750); err != nil {
		t.Fatalf("creating the staged root: %v", err)
	}
	return Proof{StagedRoot: staged, Gates: realignPassingGates(), Passed: true}
}

// TestPromotePublishesExactlyWhatWasProved is R5.3 and R5.5. The
// bytes that reach the overlay are the bytes the gates ran against — not a
// re-render, not a re-application of the diff, not the proposal regenerated from
// the baseline. Anything regenerated at promotion time is unproved by
// construction, however similar it looks.
//
// _Requirements: R5, R5.3, R5.5_
func TestPromotePublishesExactlyWhatWasProved(t *testing.T) {
	overlayRoot, stagingRoot, proposal := realignStageFixture(t)
	proof := realignProvenProof(t, stagingRoot)

	if err := Promote(proposal, proof, true, overlayRoot); err != nil {
		t.Fatalf("Promote returned %v, want nil for an approved and passing realignment", err)
	}

	got, err := os.ReadFile(realignPublishedPath(overlayRoot))
	if err != nil {
		t.Fatalf("reading the published ebuild: %v", err)
	}
	if string(got) != realignProposedEbuild {
		t.Errorf("the published ebuild is:\n%s\nwant the exact bytes that were proved:\n%s\nanything regenerated at promotion time is unproved however similar it looks (R5.5)", got, realignProposedEbuild)
	}
}

// TestPromoteRefusesAFailingGate is R5.3's first half. The maintainer
// said yes; the gates said no. Approval is permission to publish something that
// works, never an override of the finding that it does not.
//
// _Requirements: R5, R5.3, R5.4_
func TestPromoteRefusesAFailingGate(t *testing.T) {
	overlayRoot, stagingRoot, proposal := realignStageFixture(t)
	proof := realignProvenProof(t, stagingRoot)
	proof.Gates = append(proof.Gates, validate.GateResult{
		Gate: "compile", Outcome: validate.OutcomeFailed, Reason: "meson: unknown option -Dqml",
	})
	proof.Passed = false

	before := realignTreeHash(t, overlayRoot)

	err := Promote(proposal, proof, true, overlayRoot)

	if err == nil {
		t.Error("Promote returned nil for an approved but FAILING realignment; approval is permission to publish something that works, not an override of the gate that says it does not (R5.3)")
	}
	if got := realignTreeHash(t, overlayRoot); got != before {
		t.Errorf("the published overlay changed (hash %s -> %s); R5.4 leaves it byte-identical when a realignment does not pass", before, got)
	}
	if data, readErr := os.ReadFile(realignPublishedPath(overlayRoot)); readErr != nil || string(data) != realignPublishedEbuild {
		t.Errorf("the published ebuild is now %q (err %v), want the original bytes untouched", data, readErr)
	}
}

// TestPromoteRefusesWithoutApproval is R5.3's second half and the
// shortcut this whole sub-task exists to block: treating a passing gate as
// implicit approval.
//
// It is the likely shortcut because it is the reasonable-sounding one — "every
// gate passed, so what is the maintainer adding?" What the maintainer is adding
// is the judgement the gates cannot make: nodejs's 492-line divergence would
// pass every gate in the ladder and reverting it would still be wrong. The gates
// answer "does it build"; only a human answers "should we".
//
// _Requirements: R5, R5.3, R5.4_
func TestPromoteRefusesWithoutApproval(t *testing.T) {
	overlayRoot, stagingRoot, proposal := realignStageFixture(t)
	proof := realignProvenProof(t, stagingRoot)

	if !proof.Passed {
		t.Fatal("the fixture proof does not pass, so this test would refuse for the wrong reason")
	}
	before := realignTreeHash(t, overlayRoot)

	err := Promote(proposal, proof, false, overlayRoot)

	if err == nil {
		t.Error("Promote returned nil for an UNAPPROVED realignment whose gates all passed; a passing gate is not consent, and the overlay auto-commits, so a promotion is a publish (R5.3, R5.4)")
	}
	if got := realignTreeHash(t, overlayRoot); got != before {
		t.Errorf("the published overlay changed (hash %s -> %s) without approval", before, got)
	}
	if data, readErr := os.ReadFile(realignPublishedPath(overlayRoot)); readErr != nil || string(data) != realignPublishedEbuild {
		t.Errorf("the published ebuild is now %q (err %v), want the original bytes untouched", data, readErr)
	}
}

// TestPromoteRefusalsNameWhichAuthoritySaidNo keeps the three
// authorities legible after the fact. "Not promoted" is the same outcome for
// three different reasons, and an operator who cannot tell which one applies
// does not know whether to fix the ebuild, re-run the gates, or say yes.
//
// _Requirements: R5, R5.3_
func TestPromoteRefusalsNameWhichAuthoritySaidNo(t *testing.T) {
	cases := []struct {
		name     string
		approved bool
		passed   bool
		gate     *validate.GateResult
		mustName string
	}{
		{
			name:     "the gates said no",
			approved: true,
			passed:   false,
			gate:     &validate.GateResult{Gate: "compile", Outcome: validate.OutcomeFailed, Reason: "meson: unknown option -Dqml"},
			mustName: "compile",
		},
		{
			name:     "the maintainer said no",
			approved: false,
			passed:   true,
			mustName: "approv",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			overlayRoot, stagingRoot, proposal := realignStageFixture(t)
			proof := realignProvenProof(t, stagingRoot)
			proof.Passed = tc.passed
			if tc.gate != nil {
				proof.Gates = append(proof.Gates, *tc.gate)
			}

			err := Promote(proposal, proof, tc.approved, overlayRoot)
			if err == nil {
				t.Fatalf("Promote returned nil, want a refusal")
			}
			if !strings.Contains(strings.ToLower(err.Error()), tc.mustName) {
				t.Errorf("the refusal is %q, want it to name %q — three authorities can refuse and the operator must know which one did", err.Error(), tc.mustName)
			}
		})
	}
}

// TestPromoteAcceptsSkippedGates: R5.3 says PASS **or** SKIPPED. A
// SKIPPED gate is a gate that could not run — no portage on the host, no
// distfile — and story 031's rule applies unchanged: absence of evidence is not
// evidence of failure. Treating SKIPPED as failure would make the whole ladder
// unusable on any machine without a full build environment, which is most of
// them.
//
// _Requirements: R5, R5.3, R5.5_
func TestPromoteAcceptsSkippedGates(t *testing.T) {
	overlayRoot, stagingRoot, proposal := realignStageFixture(t)
	proof := realignProvenProof(t, stagingRoot)
	proof.Gates = []validate.GateResult{
		{Gate: "options", Outcome: validate.OutcomePass},
		{Gate: "compile", Outcome: validate.OutcomeSkipped, Reason: "portage is not available on this host"},
	}

	if err := Promote(proposal, proof, true, overlayRoot); err != nil {
		t.Fatalf("Promote returned %v; R5.3 accepts PASS or SKIPPED, and a SKIPPED gate is one that could not run rather than one that failed", err)
	}
	got, err := os.ReadFile(realignPublishedPath(overlayRoot))
	if err != nil || string(got) != realignProposedEbuild {
		t.Errorf("the published ebuild is %q (err %v), want the proved bytes", got, err)
	}
}

// The tests below are not part of story 034's authored fragment. They cover the
// refusals sub-task 7.2's implementation added beyond the two the fragment
// names, so that no guard standing between a proposal and a tree that commits
// and pushes itself ships without an executed proof that it holds.
//
// Every one of them re-asserts the whole-tree hash for the same reason the
// fragment does: R5.4 is a claim about the tree, not about one file.

// TestPromoteRefusesAProofOfNothing is the vacuity R5.3 would otherwise be
// satisfied by. "Every gate reported PASS or SKIPPED" is trivially true of an
// empty list, and validate.RunBuildGates returns exactly that for every depth
// below DepthPatches — so a realignment proved at DepthNone or DepthOptions was
// read by nothing, and publishing it would leave approval as the only authority
// that ever spoke.
//
// _Requirements: R5, R5.3, R5.4_
func TestPromoteRefusesAProofOfNothing(t *testing.T) {
	overlayRoot, stagingRoot, proposal := realignStageFixture(t)
	proof := realignProvenProof(t, stagingRoot)
	proof.Gates = nil

	before := realignTreeHash(t, overlayRoot)

	err := Promote(proposal, proof, true, overlayRoot)
	if err == nil {
		t.Fatal("Promote returned nil for a proof carrying no gate at all; an empty gate list satisfies R5.3 only vacuously")
	}
	if !errors.Is(err, ErrNotPromoted) {
		t.Errorf("the refusal %v is not an ErrNotPromoted; a caller has to be able to tell a decision from a broken write", err)
	}
	if got := realignTreeHash(t, overlayRoot); got != before {
		t.Errorf("the published overlay changed (hash %s -> %s) (R5.4)", before, got)
	}
}

// TestPromoteRefusesAProofThatContradictsItsOwnGates: Proof carries the verdict
// twice — as the Passed bit and as the gate list it was computed from. When the
// two disagree, nothing here can tell which one is stale, and in a tree that
// publishes itself only the refusal is safe.
//
// _Requirements: R5, R5.3, R5.4_
func TestPromoteRefusesAProofThatContradictsItsOwnGates(t *testing.T) {
	overlayRoot, stagingRoot, proposal := realignStageFixture(t)
	proof := realignProvenProof(t, stagingRoot)
	proof.Passed = false // every gate in it still reads PASS or SKIPPED

	before := realignTreeHash(t, overlayRoot)

	err := Promote(proposal, proof, true, overlayRoot)
	if err == nil {
		t.Fatal("Promote returned nil for a proof whose recorded verdict contradicts its own gates")
	}
	if !errors.Is(err, ErrNotPromoted) {
		t.Errorf("the refusal %v is not an ErrNotPromoted", err)
	}
	if got := realignTreeHash(t, overlayRoot); got != before {
		t.Errorf("the published overlay changed (hash %s -> %s) (R5.4)", before, got)
	}
}

// TestPromoteRefusesToPublishAnEbuildTheOverlayNeverHad: a realignment rewrites
// how an ALREADY PUBLISHED version is built. If that version is not there, the
// overlay moved on since the comparison — dropped, or revised to a -r1 this
// proposal does not name — and writing anyway would ADD an ebuild nobody
// approved, unclaimed by any registry pin, which is what `--clean` deletes.
//
// _Requirements: R5, R5.4, R5.5_
func TestPromoteRefusesToPublishAnEbuildTheOverlayNeverHad(t *testing.T) {
	overlayRoot, stagingRoot, proposal := realignStageFixture(t)
	proof := realignProvenProof(t, stagingRoot)
	proposal.Version = "1.30.0" // never published here

	before := realignTreeHash(t, overlayRoot)

	err := Promote(proposal, proof, true, overlayRoot)
	if err == nil {
		t.Fatal("Promote returned nil for a version the overlay does not carry; that is a bump wearing a realignment's clothes")
	}
	if !errors.Is(err, ErrNotPromoted) {
		t.Errorf("the refusal %v is not an ErrNotPromoted", err)
	}
	if got := realignTreeHash(t, overlayRoot); got != before {
		t.Errorf("the published overlay changed (hash %s -> %s) (R5.4)", before, got)
	}
}

// TestPromoteRefusesBeforeTouchingTheOverlay covers the refusals that are about
// the proposal rather than about an authority: a name that cannot address one
// file inside the overlay, and a body that would empty the ebuild it claims to
// realign.
//
// The traversal cases are the reason this guard exists here at all. Stage's own
// splitStagedAtom protects the STAGED tree and nothing in validate ever joins a
// path inside the published overlay, so this is not a second copy of that rule
// — it is the only guard on the path that leads into a git repository which
// commits and pushes on a timer.
//
// _Requirements: R5, R5.4, R5.5_
func TestPromoteRefusesBeforeTouchingTheOverlay(t *testing.T) {
	cases := []struct {
		name      string
		mutate    func(*Proposal)
		noOverlay bool
	}{
		{name: "the category climbs out of the overlay", mutate: func(p *Proposal) { p.Category = ".." }},
		{name: "the category carries a separator", mutate: func(p *Proposal) { p.Category = "../../etc" }},
		{name: "the package is empty", mutate: func(p *Proposal) { p.Package = "" }},
		{name: "the version carries a separator", mutate: func(p *Proposal) { p.Version = "1.29.2/../.." }},
		{name: "the body is empty", mutate: func(p *Proposal) { p.Ebuild = nil }},
		{name: "no overlay was named", mutate: func(*Proposal) {}, noOverlay: true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			overlayRoot, stagingRoot, proposal := realignStageFixture(t)
			proof := realignProvenProof(t, stagingRoot)
			tc.mutate(&proposal)

			target := overlayRoot
			if tc.noOverlay {
				target = ""
			}

			before := realignTreeHash(t, overlayRoot)

			err := Promote(proposal, proof, true, target)
			if err == nil {
				t.Fatal("Promote returned nil, want a refusal taken before anything was written")
			}
			if !errors.Is(err, ErrNotPromoted) {
				t.Errorf("the refusal %v is not an ErrNotPromoted", err)
			}
			if got := realignTreeHash(t, overlayRoot); got != before {
				t.Errorf("the published overlay changed (hash %s -> %s) (R5.4)", before, got)
			}
		})
	}
}

// TestPromoteChangesTheBytesAndNothingElse holds the two side effects a
// promotion must not have.
//
// The mode is the one the overlay already carried: a realignment changes how a
// version is built, and changing who may READ the file as a side effect of that
// would be a second change nobody approved — on a tree Portage reads as an
// unprivileged user.
//
// The package directory holding exactly one entry afterwards is the temporary
// file's receipt. Publishing goes through a temp-and-rename so that no reader
// ever sees a half-written ebuild under the published name, and the price is a
// file the overlay never had existing inside it for the width of one write. This
// overlay auto-commits, so a temporary left behind is a temporary published.
//
// _Requirements: R5, R5.4, R5.5_
func TestPromoteChangesTheBytesAndNothingElse(t *testing.T) {
	overlayRoot, stagingRoot, proposal := realignStageFixture(t)
	proof := realignProvenProof(t, stagingRoot)

	published := realignPublishedPath(overlayRoot)
	if err := os.Chmod(published, 0o640); err != nil {
		t.Fatalf("chmod %s: %v", published, err)
	}

	if err := Promote(proposal, proof, true, overlayRoot); err != nil {
		t.Fatalf("Promote returned %v, want nil", err)
	}

	info, err := os.Stat(published)
	if err != nil {
		t.Fatalf("stat %s: %v", published, err)
	}
	if info.Mode().Perm() != 0o640 {
		t.Errorf("the published ebuild is now mode %04o, want the 0640 the overlay already carried; a promotion changes bytes, not who may read them", info.Mode().Perm())
	}

	entries, err := os.ReadDir(filepath.Dir(published))
	if err != nil {
		t.Fatalf("reading the package directory: %v", err)
	}
	if len(entries) != 1 || entries[0].Name() != filepath.Base(published) {
		names := make([]string, 0, len(entries))
		for _, e := range entries {
			names = append(names, e.Name())
		}
		t.Errorf("the package directory holds %v, want only the published ebuild; the overlay auto-commits, so a temporary file left inside it is a temporary file published", names)
	}
}
