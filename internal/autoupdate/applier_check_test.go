package autoupdate

// Authored for story 040, sub-task 1.1 — R1.1, R1.2, R1.3, R1.4.
//
// # What these pin, and why here rather than in validate
//
// validate.PromotionDecision refuses a gate list whose deciding gates all
// declined OVER THE CANDIDATE, and it reads that cause from GateResult.Declined
// rather than from prose. The rule was shipped by story 039 with three
// producers taught to name their cause, all of them inside validate. The
// applier's own two candidate-shaped faults — a staged tree that could not be
// prepared, a manifest step that failed — went through validate.SkippedGates,
// which is hardcoded to DeclineUnrecorded, so they named nothing.
//
// The assertion has to be made HERE because the producer is here: a test in
// validate can only prove that the rule reads a cause it was handed, never that
// the applier hands it one.
//
// # These faults publish nothing today, and the stamp is still required
//
// Measured against 1e864a0 before authoring: Applier.Validate is reached only
// from `overlay autoupdate --check`, which promotes nothing; the apply path's
// two equivalents (applier.go, prepErr and manifestErr) return through
// failApply well before PromotionDecision is consulted; and the gates that
// survive into a StageRecord lose Declined to `json:"-"`, where StageRecord.Proves
// refuses an all-SKIPPED record on the OUTCOME instead. So no promotion changes
// its answer today.
//
// What changes is that the applier stops being the one caller whose vacuities
// are indistinguishable from a host that merely cannot build — which is the
// exact argument hostDeclinedGates already makes for the other half of this
// pair, and the reason its own comment gives for stamping a field nothing reads
// yet.

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/obentoo/bentoolkit/internal/autoupdate/validate"
)

// checkFixture lays out one pending bump and an applier that validates it at
// compile depth, so every build gate the depth owes an outcome for appears in
// the list a fault produces. stagingRoot and execCommand are the two knobs the
// two faults need.
func checkFixture(t *testing.T, stagingRoot string, failManifest bool) (*Applier, string) {
	t.Helper()

	tmp := t.TempDir()
	overlayDir := filepath.Join(tmp, "overlay")
	configDir := filepath.Join(tmp, "config")
	const pkg = "app-misc/checkfault"

	createTestEbuildFile(t, overlayDir, pkg, "1.0.0")

	pending, err := NewPendingList(configDir)
	if err != nil {
		t.Fatalf("creating pending list: %v", err)
	}
	pending.Add(PendingUpdate{
		Package:        pkg,
		CurrentVersion: "1.0.0",
		NewVersion:     "2.0.0",
		Status:         StatusPending,
	})

	execCommand := mockExecCommandSuccess
	if failManifest {
		execCommand = mockExecCommandFailure
	}

	applier, err := NewApplier(overlayDir, configDir,
		WithApplierPendingList(pending),
		WithExecCommand(execCommand),
		WithConfirmFunc(func(string) bool { return true }),
		WithApplierStagingRoot(stagingRoot),
		WithApplierDepth(validate.DepthCompile),
		WithApplierIsolationProbe(func() (bool, string) { return true, "" }),
	)
	if err != nil {
		t.Fatalf("creating applier: %v", err)
	}
	return applier, pkg
}

// assertCandidateDeclined is the whole of R1.1 and R1.2 in one place: every
// SKIPPED gate this fault produced says the CANDIDATE is what stopped it, and
// the rule that reads that field refuses the list.
func assertCandidateDeclined(t *testing.T, gates []validate.GateResult, what string) {
	t.Helper()

	if len(gates) == 0 {
		t.Fatalf("%s produced no gate at all; a fault that reports nothing is indistinguishable from a run that had nothing to report", what)
	}

	skipped := 0
	for _, gate := range gates {
		if gate.Outcome != validate.OutcomeSkipped {
			continue
		}
		skipped++
		if gate.Declined != validate.DeclineCandidate {
			t.Errorf("%s: the %s gate reported SKIPPED with Declined=%q, want %q\nreason: %q\n"+
				"nothing read this ebuild, so a list of these skips must not read as a host that merely could not build",
				what, gate.Gate, gate.Declined, validate.DeclineCandidate, gate.Reason)
		}
	}
	if skipped == 0 {
		t.Fatalf("%s produced no SKIPPED gate, so this test asserted nothing: %+v", what, gates)
	}

	if promoted, reason := validate.PromotionDecision(gates, nil); promoted {
		t.Errorf("%s: PromotionDecision says %q\nwant a refusal — every gate that could have decided declined over the candidate, so nothing was measured",
			what, reason)
	}
}

// TestValidate_StagingFault_DeclinesOverTheCandidate — R1.1.
//
// The staging root is a regular FILE, so validate.Stage cannot create the tree
// under it and prepareInStagingTree returns the ErrStageUnpreparable sentinel.
// That is the fault applier_check.go answers with one SKIPPED option gate plus
// one SKIPPED build gate per gate the depth covers.
func TestValidate_StagingFault_DeclinesOverTheCandidate(t *testing.T) {
	blocked := filepath.Join(t.TempDir(), "staging-is-a-file")
	if err := os.WriteFile(blocked, []byte("not a directory"), 0o644); err != nil {
		t.Fatalf("writing the blocked staging root: %v", err)
	}

	applier, pkg := checkFixture(t, blocked, false)
	result := applier.Validate(pkg, validate.DepthNone)

	assertCandidateDeclined(t, result.Gates, "a staged tree that could not be prepared")
}

// TestValidate_ManifestFault_DeclinesOverTheCandidate — R1.2.
//
// The exec seam fails every child, so the manifest step fails and the candidate
// has no digested distfile for any gate to read.
func TestValidate_ManifestFault_DeclinesOverTheCandidate(t *testing.T) {
	applier, pkg := checkFixture(t, filepath.Join(t.TempDir(), "staging"), true)
	result := applier.Validate(pkg, validate.DepthNone)

	assertCandidateDeclined(t, result.Gates, "a manifest step that failed")
}

// TestHostDeclinedGates_StillPromote — R1.3, and story 033's R3.12 held.
//
// A regression pin that is expected to arrive green: it exists so that the
// mirror added for the candidate cause cannot be made to serve both, which
// would take `overlay autoupdate` down on every workstation that simply lacks a
// bump's build dependencies.
func TestHostDeclinedGates_StillPromote(t *testing.T) {
	gates := hostDeclinedGates(validate.DepthCompile, "this host has no ebuild on PATH")
	if len(gates) == 0 {
		t.Fatalf("hostDeclinedGates produced no gate at compile depth")
	}

	for _, gate := range gates {
		if gate.Declined != validate.DeclineHost {
			t.Errorf("the %s gate carries Declined=%q, want %q — the machine said nothing about the bump",
				gate.Gate, gate.Declined, validate.DeclineHost)
		}
	}

	if promoted, reason := validate.PromotionDecision(gates, nil); !promoted {
		t.Errorf("a host that cannot build refused the bump: %q\nstory 033's R3.12 requires it to publish with the depth it did not reach named", reason)
	}
}

// TestQAGateNeverDecides_OnTheAppliersOwnFaults — the D8 exclusion, pinned from
// this side.
//
// A QA finding is about the overlay, not about the bump, so stamping the
// applier's faults must not give the QA gate a vote it never had: a list whose
// only candidate-declined skip is QA still promotes.
func TestQAGateNeverDecides_OnTheAppliersOwnFaults(t *testing.T) {
	gates := []validate.GateResult{
		{Gate: validate.GateOptions, Outcome: validate.OutcomePass, Reason: "the ebuild's flags match upstream"},
		{Gate: validate.GateQA, Outcome: validate.OutcomeSkipped, Reason: "pkgcheck is not installed", Declined: validate.DeclineCandidate},
	}

	if promoted, reason := validate.PromotionDecision(gates, nil); !promoted {
		t.Errorf("the QA gate decided the bump: %q\nit is excluded in both directions, so a metadata.xml verdict cannot stand in for a build nobody ran", reason)
	}
}

// =============================================================================
// Story 040, sub-task 1.2 — the same two faults measured at the APPLY, R1, R1.1,
// R1.2.
//
// # Why these live beside the check-path tests
//
// Because they are the other half of ONE measurement. Sub-task 1.1 asserts what
// the applier now RECORDS about its two candidate-shaped faults; these assert
// what the command an operator actually runs DOES about them. Splitting them
// across files would leave each half readable and the pair invisible.
//
// # What this measurement found, stated here because it contradicts the plan
//
// tasks.md 1.2 expects to show a behaviour CHANGE: "an apply whose staging
// failed and an apply whose manifest step failed NOW fail rather than succeed".
// They already did, and not by the mechanism story 040 adds. The apply path
// (applier.go) answers both faults through failApply, which returns before
// validate.PromotionDecision is ever consulted — so the DeclineCandidate stamp
// sub-task 1.1 added is not on this path at all, and could not be: the stamp is
// reached only through Applier.Validate, i.e. `overlay autoupdate --check`,
// which publishes nothing.
//
// These tests are therefore a REGRESSION PIN that arrives green, which tasks.md
// anticipates in its own words ("Expect regression pins that arrive green").
// They pin the refusal against the two ways it could be lost — a future edit
// that lets either fault fall through to the gate list, where a list of skips
// carrying no cause promotes — and the mutation table of task 4.1 is what makes
// them evidence rather than decoration.
//
// The operator warning tasks.md carries ("this story changes overlay
// autoupdate's behaviour ... applies that today promote will start refusing")
// is NOT borne out by the code. Nothing an operator runs changes its answer.

// applyFixture is checkFixture for the apply path, with the registry seam
// captured: pins is appended to for every version this apply tried to record.
func applyFixture(t *testing.T, stagingRoot string, failManifest bool) (*Applier, string, string, *[]string) {
	t.Helper()

	tmp := t.TempDir()
	overlayDir := filepath.Join(tmp, "overlay")
	configDir := filepath.Join(tmp, "config")
	const pkg = "app-misc/applyfault"

	createTestEbuildFile(t, overlayDir, pkg, "1.0.0")

	pending, err := NewPendingList(configDir)
	if err != nil {
		t.Fatalf("creating pending list: %v", err)
	}
	pending.Add(PendingUpdate{
		Package:        pkg,
		CurrentVersion: "1.0.0",
		NewVersion:     "2.0.0",
		Status:         StatusPending,
	})

	execCommand := mockExecCommandSuccess
	if failManifest {
		execCommand = mockExecCommandFailure
	}

	pins := &[]string{}
	applier, err := NewApplier(overlayDir, configDir,
		WithApplierPendingList(pending),
		WithExecCommand(execCommand),
		WithConfirmFunc(func(string) bool { return true }),
		WithApplierStagingRoot(stagingRoot),
		WithApplierDepth(validate.DepthCompile),
		WithApplierIsolationProbe(func() (bool, string) { return true, "" }),
		WithApplierSetVersionsFunc(func(_ string, recorded map[string]string) error {
			for atom, version := range recorded {
				*pins = append(*pins, atom+"="+version)
			}
			return nil
		}),
	)
	if err != nil {
		t.Fatalf("creating applier: %v", err)
	}
	return applier, pkg, overlayDir, pins
}

// assertApplyRefused is R1.1 and R1.2 read where they land: the apply fails, the
// published overlay is byte-identical to what it was, and no version was pinned.
func assertApplyRefused(t *testing.T, applier *Applier, pkg, overlayDir string, pins *[]string, what string) {
	t.Helper()

	before := hashOverlayTree(t, overlayDir)
	result, err := applier.Apply(pkg, false)

	if err == nil {
		t.Errorf("%s: Apply returned no error\nnothing read this candidate, so an apply that succeeds here publishes an unmeasured ebuild into an overlay that auto-commits and pushes", what)
	}
	if result != nil && result.Success {
		t.Errorf("%s: the apply reported Success\nerror on the result: %v", what, result.Error)
	}
	if after := hashOverlayTree(t, overlayDir); after != before {
		t.Errorf("%s: the published overlay changed: %s -> %s", what, before, after)
	}
	if len(*pins) != 0 {
		t.Errorf("%s: the registry recorded %v\na pin written for a bump that was refused aims --clean at the only ebuild present (S021-R2.2)", what, *pins)
	}
	candidate := filepath.Join(overlayDir, "app-misc", "applyfault", "applyfault-2.0.0.ebuild")
	if _, statErr := os.Stat(candidate); statErr == nil {
		t.Errorf("%s: the candidate was written into the published overlay at %q", what, candidate)
	}
}

// TestApply_StagingFault_RefusesAndWritesNothing — R1.1 at the apply.
func TestApply_StagingFault_RefusesAndWritesNothing(t *testing.T) {
	blocked := filepath.Join(t.TempDir(), "staging-is-a-file")
	if err := os.WriteFile(blocked, []byte("not a directory"), 0o644); err != nil {
		t.Fatalf("writing the blocked staging root: %v", err)
	}

	applier, pkg, overlayDir, pins := applyFixture(t, blocked, false)
	assertApplyRefused(t, applier, pkg, overlayDir, pins, "an apply whose staged tree could not be prepared")
}

// TestApply_ManifestFault_RefusesAndWritesNothing — R1.2 at the apply.
func TestApply_ManifestFault_RefusesAndWritesNothing(t *testing.T) {
	applier, pkg, overlayDir, pins := applyFixture(t, filepath.Join(t.TempDir(), "staging"), true)
	assertApplyRefused(t, applier, pkg, overlayDir, pins, "an apply whose manifest step failed")
}
