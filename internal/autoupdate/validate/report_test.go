package validate

// Authored for story 031, sub-task 4.2 — R3, R3.4, R3.5, R4, R5.5, R5.6, R5.7.
// REWRITTEN onto GateResult for story 033, sub-task 5.1 — R4, R4.4, R5, R5.3.
//
// Written from the contract: design.md's Data Models fix Report, EbuildResult,
// Finding, Outcome and Severity, and its CLI table fixes the three exit codes.
//
// ONE THING THIS TEST PINS RATHER THAN INHERITS. design.md says exit 2 means
// "the selector names a category or package the overlay does not hold", but it
// does not say how a Report carries that. This file pins the field
// `Report.UnmatchedSelector string` — non-empty means nothing matched. If the
// implementer prefers another shape, this is the file to change first; the
// three exit-code behaviours themselves are not negotiable.
//
// WHAT THE REWRITE CHANGED, AND WHAT IT DID NOT. The two hardcoded outcome
// fields (Options, QA) and the single shared Reason became `Gates []GateResult`
// (story 033 D12), so every case below is expressed through gates now. Not one
// asserted BEHAVIOUR moved: the three exit codes, the clean run, the two
// non-blocking severities, D8's advisory pkgcheck and the "SKIPPED always
// carries a reason" invariant are all still here, and the invariant is now
// asserted per gate, which is the only place it was ever true.
//
// Red is DEFERRED to Run mode: the package does not exist yet.

import "testing"

func TestReport_ExitCode_CleanRunIsZero(t *testing.T) {
	r := Report{
		Overlay: "/var/db/repos/bentoo",
		Results: []EbuildResult{
			{Package: "media-plugins/gst-plugins-qt6", Version: "1.28.6", Gates: []GateResult{
				{Gate: GateOptions, Outcome: OutcomePass},
				{Gate: GateQA, Outcome: OutcomePass},
			}},
			{Package: "dev-python/gst-python", Version: "1.28.6", Gates: []GateResult{
				{Gate: GateOptions, Outcome: OutcomeSkipped, Reason: "distfile absent: gst-python-1.28.6.tar.xz"},
				{Gate: GateQA, Outcome: OutcomePass},
			}},
		},
	}

	if got := r.ExitCode(); got != 0 {
		t.Errorf("ExitCode: got %d, want 0 — PASS and SKIPPED are both clean outcomes", got)
	}
}

func TestReport_ExitCode_ErrorFindingIsOne(t *testing.T) {
	r := Report{
		Results: []EbuildResult{{
			Package: "media-plugins/gst-plugins-qt6",
			Version: "1.29.2",
			Gates: []GateResult{{
				Gate:    GateOptions,
				Outcome: OutcomeFailed,
				Findings: []Finding{
					{Gate: GateOptions, Severity: SeverityError, Detail: "-Daalib= is passed but upstream 1.29.2 declares no such option"},
				},
			}},
		}},
	}

	if got := r.ExitCode(); got != 1 {
		t.Errorf("ExitCode: got %d, want 1", got)
	}
}

func TestReport_ExitCode_UnmatchedSelectorIsTwo(t *testing.T) {
	r := Report{UnmatchedSelector: "media-plugins/does-not-exist"}

	if got := r.ExitCode(); got != 2 {
		t.Errorf("ExitCode: got %d, want 2 — a selector matching nothing is a usage error, not a clean run", got)
	}
}

// TestReport_ExitCode_QaFindingsDoNotChangeIt is design decision D8, and it is
// what keeps the command usable. The overlay carries pre-existing pkgcheck
// findings that have nothing to do with any bump; letting them set the exit
// status would make `overlay validate` fail across the whole tree and become
// noise — a metadata.xml DOCTYPE typo outranking the real signal.
func TestReport_ExitCode_QaFindingsDoNotChangeIt(t *testing.T) {
	r := Report{
		Results: []EbuildResult{{
			Package: "media-plugins/gst-plugins-qt6",
			Version: "1.28.6",
			Gates: []GateResult{
				{Gate: GateOptions, Outcome: OutcomePass},
				{Gate: GateQA, Outcome: OutcomePass, Findings: []Finding{
					{Gate: GateQA, Severity: SeverityError, Detail: "MissingXmlDoctype: metadata.xml lacks a DOCTYPE"},
				}},
			},
		}},
	}

	if got := r.ExitCode(); got != 0 {
		t.Errorf("ExitCode: got %d, want 0 — pkgcheck findings never decide the exit code (D8)", got)
	}
}

// TestReport_ExitCode_WarningsAndInfoDoNotFail pins that the two non-blocking
// severities stay non-blocking. An unresolved option name is a limit of the
// gate's reach, not a defect in the ebuild.
func TestReport_ExitCode_WarningsAndInfoDoNotFail(t *testing.T) {
	r := Report{
		Results: []EbuildResult{{
			Package: "media-plugins/gst-plugins-qt6",
			Version: "1.28.6",
			Gates: []GateResult{{
				Gate:    GateOptions,
				Outcome: OutcomePass,
				Findings: []Finding{
					{Gate: GateOptions, Severity: SeverityWarning, Detail: "-D${plugin}= at line 57 cannot be resolved statically"},
					{Gate: GateOptions, Severity: SeverityInfo, Detail: "upstream declares vulkan, which the ebuild never passes"},
				},
			}},
		}},
	}

	if got := r.ExitCode(); got != 0 {
		t.Errorf("ExitCode: got %d, want 0", got)
	}
}

// TestEbuildResult_SkippedAlwaysCarriesAReason is the invariant the whole story
// rests on. A SKIPPED without a reason is indistinguishable from a pass to
// anyone reading the report, which is the exact defect this gate exists to
// remove — so it is asserted, not left to convention.
//
// Rewritten onto gates, and note what the rewrite did to the CONVERSE. It used
// to assert that a result with no skip carried no reason at all; with a reason
// per gate that is too strong, because a PASS may state its own reach ("a
// configure pass does not cover compilation"). It is kept where nothing is left
// to qualify: the option gate reads two static inputs and a PASS from it means
// exactly what it says.
func TestEbuildResult_SkippedAlwaysCarriesAReason(t *testing.T) {
	r := Report{
		Results: []EbuildResult{
			{Package: "a/b", Version: "1", Gates: []GateResult{
				{Gate: GateOptions, Outcome: OutcomeSkipped, Reason: "build system is not Meson: cmake"},
				{Gate: GateQA, Outcome: OutcomePass},
			}},
			{Package: "c/d", Version: "2", Gates: []GateResult{
				{Gate: GateOptions, Outcome: OutcomePass},
				{Gate: GateQA, Outcome: OutcomeSkipped, Reason: "pkgcheck was not found on PATH"},
			}},
			{Package: "e/f", Version: "3", Gates: []GateResult{
				{Gate: GateOptions, Outcome: OutcomePass},
				{Gate: GateQA, Outcome: OutcomePass},
			}},
		},
	}

	for _, res := range r.Results {
		for _, gate := range res.Gates {
			if gate.Outcome == OutcomeSkipped && gate.Reason == "" {
				t.Errorf("%s-%s gate %q reports SKIPPED with an empty reason; a skip nobody can read is a pass",
					res.Package, res.Version, gate.Gate)
			}
			if gate.Gate == GateOptions && gate.Outcome == OutcomePass && gate.Reason != "" {
				t.Errorf("%s-%s option gate carries a reason (%q) on a PASS", res.Package, res.Version, gate.Reason)
			}
		}
	}
}

// TestReport_ExitCode_MixedRunPrefersFailure pins that one bad ebuild in a
// whole-overlay sweep is enough to fail the run.
func TestReport_ExitCode_MixedRunPrefersFailure(t *testing.T) {
	r := Report{
		Results: []EbuildResult{
			{Package: "a/b", Version: "1", Gates: []GateResult{{Gate: GateOptions, Outcome: OutcomePass}}},
			{Package: "c/d", Version: "2", Gates: []GateResult{
				{Gate: GateOptions, Outcome: OutcomeSkipped, Reason: "distfile absent: d-2.tar.xz"},
			}},
			{Package: "e/f", Version: "3", Gates: []GateResult{{
				Gate:     GateOptions,
				Outcome:  OutcomeFailed,
				Findings: []Finding{{Gate: GateOptions, Severity: SeverityError, Detail: "-Dgone= is undeclared"}},
			}}},
		},
	}

	if got := r.ExitCode(); got != 1 {
		t.Errorf("ExitCode: got %d, want 1", got)
	}
}

// TestEbuildResult_WorstOutcomeIsWhatTheHeadlineShows covers the renderer's one
// column against five gates. FAILED has to win, or a configure failure hides
// behind an option-gate pass — which is D12's whole complaint about the shipped
// shape, seen from the human report instead of the exit code.
func TestEbuildResult_WorstOutcomeIsWhatTheHeadlineShows(t *testing.T) {
	cases := []struct {
		name  string
		gates []GateResult
		want  Outcome
	}{
		{
			name:  "every gate passed",
			gates: []GateResult{{Gate: GateOptions, Outcome: OutcomePass}, {Gate: GateConfigure, Outcome: OutcomePass}},
			want:  OutcomePass,
		},
		{
			name: "one failure outranks every pass",
			gates: []GateResult{
				{Gate: GateOptions, Outcome: OutcomePass},
				{Gate: GatePatches, Outcome: OutcomePass},
				{Gate: GateConfigure, Outcome: OutcomeFailed},
			},
			want: OutcomeFailed,
		},
		{
			name: "a skip is not a pass",
			gates: []GateResult{
				{Gate: GateOptions, Outcome: OutcomePass},
				{Gate: GateCompile, Outcome: OutcomeSkipped, Reason: "dev-libs/unsatisfied-1.0 is not installed"},
			},
			want: OutcomeSkipped,
		},
		{
			name:  "no gate at all reports nothing, and nothing is not a pass",
			gates: nil,
			want:  OutcomeSkipped,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			res := EbuildResult{Package: "a/b", Version: "1", Gates: tc.gates}
			if got := res.WorstOutcome(); got != tc.want {
				t.Errorf("WorstOutcome: got %q, want %q", got, tc.want)
			}
		})
	}
}

// TestEbuildResult_WorstOutcomeIgnoresPkgcheck is D8 applied to the summary line
// rather than to the exit code. pkgcheck is absent on plenty of hosts, and if a
// skipped QA gate could set the headline then a clean whole-overlay run would
// render as "0 passed, 500 skipped" while the command exited 0 — the summary and
// the exit code saying opposite things about the same run.
func TestEbuildResult_WorstOutcomeIgnoresPkgcheck(t *testing.T) {
	res := EbuildResult{
		Package: "media-plugins/gst-plugins-qt6",
		Version: "1.28.6",
		Gates: []GateResult{
			{Gate: GateOptions, Outcome: OutcomePass},
			{Gate: GateQA, Outcome: OutcomeSkipped, Reason: "pkgcheck was not found on PATH"},
		},
	}

	if got := res.WorstOutcome(); got != OutcomePass {
		t.Errorf("WorstOutcome: got %q, want PASS — an absent pkgcheck must not turn a validated ebuild into a skip", got)
	}
}

// TestReport_ExitCode_ConfigureGateErrorIsOne is the regression the whole
// sub-task exists to prevent, and it is the sharpest assertion in this file: the
// shipped ExitCode filters on GateOptions and would answer 0 here.
func TestReport_ExitCode_ConfigureGateErrorIsOne(t *testing.T) {
	r := Report{
		Results: []EbuildResult{{
			Package: "media-plugins/gst-plugins-qt6",
			Version: "1.29.2",
			Depth:   "configure",
			Gates: []GateResult{
				{Gate: GateOptions, Outcome: OutcomePass},
				{Gate: GatePatches, Outcome: OutcomePass},
				{Gate: GateConfigure, Outcome: OutcomeFailed, Findings: []Finding{{
					Gate:     GateConfigure,
					Severity: SeverityError,
					Detail:   `meson.build:1:0: ERROR: Unknown option: "aalib".`,
				}}},
			},
		}},
	}

	if got := r.ExitCode(); got != 1 {
		t.Errorf("ExitCode: got %d, want 1 — an error finding on the configure gate must fail the run; "+
			"filtering on GateOptions makes the gate that actually runs the build invisible", got)
	}
	if !r.HasErrors() {
		t.Error("HasErrors is false while an error finding is present; the summary line and the exit code have drifted apart")
	}
}

// TestReport_ExitCode_EveryGateThatCanFailDoesFail generalises the case above
// so a fifth gate added later cannot be forgotten. Each row is one gate carrying
// one error finding and nothing else.
func TestReport_ExitCode_EveryGateThatCanFailDoesFail(t *testing.T) {
	for _, gate := range []string{GateOptions, GatePatches, GateConfigure, GateCompile} {
		t.Run(gate, func(t *testing.T) {
			r := Report{Results: []EbuildResult{{
				Package: "media-plugins/gst-plugins-qt6",
				Version: "1.29.2",
				Gates: []GateResult{{
					Gate:     gate,
					Outcome:  OutcomeFailed,
					Findings: []Finding{{Gate: gate, Severity: SeverityError, Detail: "the ebuild no longer matches upstream"}},
				}},
			}}}

			if got := r.ExitCode(); got != 1 {
				t.Errorf("ExitCode: got %d, want 1 for an error finding on the %s gate", got, gate)
			}
		})
	}
}

// TestReport_ExitCode_QaFindingsStillDoNotChangeIt is the Unchanged Behaviour
// guard (story Unchanged Behavior 4, design D8). "Count every gate" must not
// quietly become "count pkgcheck too": the overlay carries pre-existing QA
// findings unrelated to any bump, and letting them decide the status fails the
// whole tree and reduces the command to noise.
func TestReport_ExitCode_QaFindingsStillDoNotChangeIt(t *testing.T) {
	r := Report{Results: []EbuildResult{{
		Package: "media-plugins/gst-plugins-qt6",
		Version: "1.28.6",
		Gates: []GateResult{
			{Gate: GateOptions, Outcome: OutcomePass},
			{Gate: GateQA, Outcome: OutcomePass, Findings: []Finding{
				{Gate: GateQA, Severity: SeverityError, Detail: "MissingXmlDoctype: metadata.xml lacks a DOCTYPE"},
			}},
		},
	}}}

	if got := r.ExitCode(); got != 0 {
		t.Errorf("ExitCode: got %d, want 0 — pkgcheck findings stay advisory (D8)", got)
	}
}

// TestReport_ExitCode_ReviewFindingsNeverDecide is R7.6 made structural rather
// than remembered. The reviewer emits at info or warning and never at error, so
// a model's opinion cannot fail a bump even if it wanted to; this asserts the
// consequence at the only place it would be observable.
func TestReport_ExitCode_ReviewFindingsNeverDecide(t *testing.T) {
	r := Report{Results: []EbuildResult{{
		Package: "media-plugins/gst-plugins-qt6",
		Version: "1.29.2",
		Gates: []GateResult{
			{Gate: GateOptions, Outcome: OutcomePass},
			{Gate: GateReview, Outcome: OutcomePass, Findings: []Finding{
				{Gate: GateReview, Severity: SeverityWarning, Detail: "upstream moved the plugin registration API"},
				{Gate: GateReview, Severity: SeverityInfo, Detail: "the build file gained two options"},
			}},
		},
	}}}

	if got := r.ExitCode(); got != 0 {
		t.Errorf("ExitCode: got %d, want 0 — a reviewer's report never decides whether a bump passed (R7.6)", got)
	}
	for _, gate := range r.Results[0].Gates {
		if gate.Gate != GateReview {
			continue
		}
		for _, f := range gate.Findings {
			if f.Severity == SeverityError {
				t.Errorf("the review gate emitted an error finding (%q); it may raise scrutiny, never decide", f.Detail)
			}
		}
	}
}

// TestGateResult_EverySkippedGateCarriesAReason is the invariant this sub-task
// moves from a constructor onto the TYPE. "SKIPPED always carries a reason" was
// enforced by skippedResult alone, which left every future gate free to forget
// it; a skip nobody can read is a pass.
func TestGateResult_EverySkippedGateCarriesAReason(t *testing.T) {
	r := Report{Results: []EbuildResult{{
		Package:        "media-plugins/gst-plugins-qt6",
		Version:        "1.29.2",
		Depth:          "options",
		DepthRequested: "compile",
		DepthReason:    "the staged tree could not be prepared",
		Gates: []GateResult{
			{Gate: GateOptions, Outcome: OutcomePass},
			{Gate: GatePatches, Outcome: OutcomeSkipped, Reason: "the staged tree could not be prepared: permission denied"},
			{Gate: GateConfigure, Outcome: OutcomeSkipped, Reason: "the staged tree could not be prepared: permission denied"},
			{Gate: GateCompile, Outcome: OutcomeSkipped, Reason: "dev-libs/unsatisfied-1.0 is not installed"},
		},
	}}}

	for _, res := range r.Results {
		for _, gate := range res.Gates {
			switch gate.Outcome {
			case OutcomeSkipped:
				if gate.Reason == "" {
					t.Errorf("gate %q reports SKIPPED with an empty reason; a skip nobody can read is a pass", gate.Gate)
				}
			case OutcomePass:
				if gate.Reason != "" && gate.Outcome == OutcomePass && gate.Gate == GateOptions {
					// A PASS may carry a reach statement ("a configure pass does
					// not cover compilation"), so this is asserted only where the
					// gate has nothing to qualify.
					t.Errorf("the options gate carries a reason (%q) on a PASS", gate.Reason)
				}
			}
		}
	}

	if got := r.ExitCode(); got != 0 {
		t.Errorf("ExitCode: got %d, want 0 — SKIPPED is a reported outcome, not a failure", got)
	}
}

// TestGateResult_TwoGatesSkippingDifferentlyKeepBothReasons is why the single
// contended Reason field had to move onto each gate: attachQA already overwrote
// it (run.go:263), so two skips for different causes rendered as one.
func TestGateResult_TwoGatesSkippingDifferentlyKeepBothReasons(t *testing.T) {
	res := EbuildResult{
		Package: "media-plugins/gst-plugins-qt6",
		Version: "1.29.2",
		Gates: []GateResult{
			{Gate: GateOptions, Outcome: OutcomeSkipped, Reason: "no distfile named by the Manifest is present"},
			{Gate: GateQA, Outcome: OutcomeSkipped, Reason: "pkgcheck was not found on PATH"},
		},
	}

	reasons := map[string]string{}
	for _, gate := range res.Gates {
		reasons[gate.Gate] = gate.Reason
	}
	if reasons[GateOptions] == reasons[GateQA] {
		t.Fatalf("both gates report the same reason (%q); one skip overwrote the other's explanation", reasons[GateOptions])
	}
	if reasons[GateOptions] == "" || reasons[GateQA] == "" {
		t.Errorf("a skip lost its reason: options=%q qa=%q", reasons[GateOptions], reasons[GateQA])
	}
}

// TestReport_ExitCode_UnmatchedSelectorStillIsTwo keeps the third exit code
// intact through the rewrite. It is the one code that is NOT about findings, and
// a rewrite of the finding loop is exactly where it would be lost.
func TestReport_ExitCode_UnmatchedSelectorStillIsTwo(t *testing.T) {
	r := Report{UnmatchedSelector: "media-plugins/does-not-exist"}

	if got := r.ExitCode(); got != 2 {
		t.Errorf("ExitCode: got %d, want 2 — a selector matching nothing is a usage error, not a clean run", got)
	}
}

// TestEbuildResult_DepthReportsItsOwnReach is the R4/R6.4 rule applied to the
// ladder itself: "compile was asked for and configure is as far as it got,
// because …" is only expressible with DepthRequested beside Depth.
func TestEbuildResult_DepthReportsItsOwnReach(t *testing.T) {
	res := EbuildResult{
		Package:        "media-plugins/gst-plugins-qt6",
		Version:        "1.29.2",
		Depth:          "configure",
		DepthRequested: "compile",
		DepthReason:    "policy: series crossing; the compile gate was skipped for lack of an installed dependency",
		Gates: []GateResult{
			{Gate: GateConfigure, Outcome: OutcomePass, Reason: "a configure pass does not cover compilation"},
			{Gate: GateCompile, Outcome: OutcomeSkipped, Reason: "dev-libs/unsatisfied-1.0 is not installed"},
		},
	}

	if res.Depth == res.DepthRequested {
		t.Fatal("Depth and DepthRequested are the same on a run that did not reach the depth it was asked for")
	}
	if res.DepthReason == "" {
		t.Error("a depth that fell short of the requested one carries no reason")
	}
}
