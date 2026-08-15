package overlay

import (
	"strconv"
	"strings"
	"testing"
)

// This file exists because a MUTATION SURVIVED sub-task 8.1's frozen fragment.
//
// Collapsing baselineReachProse's four arms into two — reading Classified.Reduced
// alone and never consulting Span — left every test in the package green. The
// gap is in the coverage, not in the implementation: the only two Classified
// values any test in this package builds by hand both carry Span 1, so of the
// four states only two are ever constructed, and the two that are NOT are the
// two production actually prints.
//
//   - Reduced=true, Span=0 is the SAME-VERSION case: nodejs's 492 differences,
//     all ours, with no version move for anything to be attributed to. Reported
//     without a third point because none was needed.
//   - Reduced=false, Span=0 is what AnnotateBaseline renders for every
//     different-version package today, because ReduceDiff is called with a nil
//     move: no third point was offered, so nothing was attributed.
//
// Reduced read alone turns the first into "a third point was accepted" and makes
// the second indistinguishable from "a third point was offered and REFUSED for
// being too wide". Those are three different facts, and R2.5 is precisely the
// requirement that the reach of a classification be legible: a classification
// whose reach is invisible is indistinguishable from a guess.
//
// The assertions below are deliberately about the ARITHMETIC of the reach rather
// than about its wording, so a rewrite of the sentences does not fail them and a
// collapse of the states cannot pass them.

// classificationReachState is one of the four (Reduced, Span) states, with the
// question its sentence has to answer differently from the other three.
type classificationReachState struct {
	name string
	// classified is the state, and nothing else about it is varied: the three
	// counts are held constant so that any difference between two sentences is a
	// difference of reach and not of arithmetic.
	classified Classified
	// thirdPoint is whether a third point existed at all. When one did, its width
	// is the only thing that makes the refusal legible, so the sentence must
	// state it; when none did, the sentence must invent no width, because there
	// is nothing there to have one.
	thirdPoint bool
}

// classificationReachStates are the four states, with the two Span values chosen
// far apart and far from the counts: 2 is a third point narrow enough to accept,
// 19 is the nodejs-scale span the bound refuses.
func classificationReachStates() []classificationReachState {
	return []classificationReachState{
		{
			name:       "the baseline is our own version, so no third point was needed",
			classified: Classified{VersionMove: 0, Ours: 492, Unclassified: 0, Reduced: true, Span: 0},
			thirdPoint: false,
		},
		{
			name:       "a third point was offered and accepted",
			classified: Classified{VersionMove: 0, Ours: 492, Unclassified: 0, Reduced: true, Span: 2},
			thirdPoint: true,
		},
		{
			name:       "a third point was offered and refused for being too wide",
			classified: Classified{VersionMove: 0, Ours: 492, Unclassified: 0, Reduced: false, Span: 19},
			thirdPoint: true,
		},
		{
			name:       "no third point was offered at all",
			classified: Classified{VersionMove: 0, Ours: 492, Unclassified: 0, Reduced: false, Span: 0},
			thirdPoint: false,
		},
	}
}

// TestClassificationReachReadsReducedAndSpanTogether pins the property the
// surviving mutation broke: the four (Reduced, Span) states must reach the
// operator as four different sentences, and the width of a third point must be
// stated when one existed and invented when one did not.
//
// It asserts the SPAN NUMBER rather than any wording. A sentence that states no
// number for a third point two release steps wide has stopped reporting the
// reach; a sentence that states a number where no third point existed has made
// one up. Both are failures the wording could otherwise hide.
//
// _Requirements: R2, R2.5_
func TestClassificationReachReadsReducedAndSpanTogether(t *testing.T) {
	seen := make(map[string]string, 4)

	for _, state := range classificationReachStates() {
		t.Run(state.name, func(t *testing.T) {
			prose := baselineReachProse(state.classified)
			if prose == "" {
				t.Fatal("the reach of the classification is not reported at all; R2.5 is the requirement that it be stated beside the counts")
			}

			span := strconv.Itoa(state.classified.Span)
			switch {
			case state.thirdPoint && !strings.Contains(prose, span):
				t.Errorf("the reach reads %q and never states the third point's width (%s release steps); the refusal and the acceptance are only legible from the width that caused them", prose, span)
			case !state.thirdPoint && strings.ContainsAny(prose, "0123456789"):
				t.Errorf("the reach reads %q and states a width although no third point was offered; a span nobody measured must not be printed as one that was", prose)
			}
		})
	}

	// Pairwise distinctness, asserted across the four rather than inside any one
	// of them: the failure mode is a COLLAPSE, and a collapse is invisible from
	// one state at a time.
	for _, state := range classificationReachStates() {
		prose := baselineReachProse(state.classified)
		if other, clash := seen[prose]; clash {
			t.Errorf("%q and %q report the same reach, %q — Reduced and Span are being read separately, and three different facts about the third point now read alike (R2.5)",
				state.name, other, prose)
		}
		seen[prose] = state.name
	}
}

// TestClassificationReachReachesTheRenderedLines is the other half: the reach is
// established correctly above and must arrive on the line the operator reads.
// The counts and the reach are one sentence on purpose — a total with no reach
// beside it is a number whose worth cannot be judged.
//
// _Requirements: R2, R2.4, R2.5_
func TestClassificationReachReachesTheRenderedLines(t *testing.T) {
	for _, state := range classificationReachStates() {
		t.Run(state.name, func(t *testing.T) {
			lines := classificationLines(CompareResult{
				Category: "net-libs", Package: "nodejs",
				Classified: state.classified,
			})
			if len(lines) == 0 {
				t.Fatal("a classified package rendered no lines; the reduction reached no report (R2.4)")
			}
			if !strings.Contains(lines[0], baselineReachProse(state.classified)) {
				t.Errorf("the lead line is %q and does not carry the reach %q; the total is printed without saying what it is worth (R2.5)",
					lines[0], baselineReachProse(state.classified))
			}
		})
	}
}
