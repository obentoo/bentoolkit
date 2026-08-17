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

// MERGE FRAGMENT — story 036, sub-task 3.1 (the fifth reach state).
//
// Target file: internal/overlay/annotate_baseline_reach_test.go  (APPEND, at
// the end of the file — after TestClassificationReachReachesTheRenderedLines).
// Do NOT repeat the `package overlay` clause.
//
// IMPORTS: none added. This fragment uses `strconv`, `strings` and `testing`,
// all three of which the target file already imports.
//
// # Symbols
//
// Added, all prefixed `fifthReach` so nothing can collide with story 034's
// `classificationReachState` / `classificationReachStates` in the same file:
// `fifthReachSubtractionClaim`, `fifthReachShippedState`,
// `fifthReachShippedStates`, `fifthReachAccepted`, `fifthReachExplainedNothing`,
// and the three Test functions below — named `TestClassificationReach…` so the
// sub-task's validation command, `-run 'TestClassificationReach'`, selects them
// together with the two that are already there.
//
// Borrowed, never re-declared: `Classified` (reduce.go), `baselineReachProse`
// and `classificationLines` (annotate_baseline.go), `CompareResult`.
//
// # PINNED CONTRACT (design.md D5)
//
//	func baselineReachProse(classified Classified) string   // SIGNATURE UNCHANGED
//
// design.md says the function "takes the version-move count as well"; it already
// does, inside `Classified`. Changing the signature would break story 034's
// reach test, which calls it with one argument and which this sub-task must keep
// passing — so the count is read off the value, not added as a parameter.
//
//	| Reduced | Span | VersionMove | reach                                       |
//	|---------|------|-------------|---------------------------------------------|
//	| true    | 0    | —           | attributed without a third point            |
//	| true    | >0   | >0          | reduced against a third point spanning N    |
//	| true    | >0   | 0           | a third point spanning N explained nothing  |  <- NEW
//	| false   | >0   | —           | NOT reduced: the third point is too wide    |
//	| false   | 0    | —           | NOT reduced: no third point was available   |
//
// # THE ONE GUARD THAT IS EASY TO GET WRONG
//
// The new arm needs BOTH `Span > 0` and `VersionMove == 0`. A guard written on
// the count alone swallows the first row — `Reduced` with `Span == 0` is the
// SAME-VERSION case, where the baseline is our own ebuild and there was no
// version move for anything to be attributed to. That row has a version-move
// count of zero for a completely different reason, and reporting it as "a third
// point explained nothing" would invent a third point that never existed. The
// first test below pins that row byte for byte, which is what makes the mistake
// fail immediately rather than in the renderer.
//
// # A NOTE ON STORY 034's FIXTURE, which this fragment does not touch
//
// `classificationReachStates()` builds every one of its four states with
// `VersionMove: 0`, including the row it calls "a third point was offered and
// accepted". After this sub-task that row renders the FIFTH sentence rather than
// the second — legitimately, because a third point that attributed nothing is
// what the row describes. Its own assertions (a width is stated, the sentences
// are pairwise distinct) still hold, so the file keeps passing untouched. But
// the row no longer pins the ACCEPTED sentence, and that coverage would be lost
// silently; the table below carries it instead, with `VersionMove` above zero.
// Repairing 034's fixture is optional and is not required by this sub-task.
//
// # WHAT THIS FRAGMENT DELIBERATELY DOES NOT PIN — R2.4
//
// Task 3.1's ToDo asks the sentence to "name which third point was used". There
// is nowhere in the pinned contract to read that from: `Classified` is frozen
// with reduce.go, and `baselineReachProse` takes only a `Classified`. So R2.4's
// carrier is undecided by design.md, and pinning a field name here would be
// inventing one. It is raised in this story's red-evidence instead, and the
// implementer should record whatever carrier they add in the deviation register.

// fifthReachSubtractionClaim is the phrase R2.3 names in as many words: the
// report SHALL NOT read `reduced against a third point` over a run in which
// nothing was subtracted.
//
// It is a constant so a test can name the claim without copying the sentence,
// on the same argument that made `undeclaredDivergenceCaveat` and
// `baselineSkippedLead` constants.
const fifthReachSubtractionClaim = "reduced against a third point"

// fifthReachShippedState is one of story 034's four states with the sentence it
// prints TODAY, verbatim.
type fifthReachShippedState struct {
	name       string
	classified Classified
	want       string
}

// fifthReachShippedStates are the four sentences copied out of
// baselineReachProse as it ships. They are pinned byte for byte because task
// 3.1's instruction is that they "must read exactly as they do today": this
// story changes what is said about ONE state nobody could say anything true
// about, and rewording the other four would be a change to shipped output that
// no requirement asks for.
//
// Each `VersionMove` is set deliberately rather than left at zero — that is the
// field the fifth state turns on, and a table that ignored it could not tell the
// second row from the new one.
func fifthReachShippedStates() []fifthReachShippedState {
	return []fifthReachShippedState{
		{
			name: "the baseline is our own version, so no third point was needed",
			// Span 0 with Reduced true is the SAME-VERSION case: nodejs's 492
			// differences, all ours, with no version move in existence. Its
			// version-move count is zero and it must NOT become the fifth state.
			classified: Classified{VersionMove: 0, Ours: 492, Unclassified: 0, Reduced: true, Span: 0},
			want:       "attributed without a third point: the baseline is our own version",
		},
		{
			name:       "a third point was accepted and explained something",
			classified: Classified{VersionMove: 3, Ours: 9, Unclassified: 0, Reduced: true, Span: 2},
			want:       "reduced against a third point spanning 2 release steps",
		},
		{
			name:       "a third point was offered and refused for being too wide",
			classified: Classified{VersionMove: 0, Ours: 492, Unclassified: 0, Reduced: false, Span: 19},
			want:       "NOT reduced: the third point offered spans 19 release steps, wider than the baseline is from us",
		},
		{
			name:       "no third point was offered at all",
			classified: Classified{VersionMove: 0, Ours: 492, Unclassified: 0, Reduced: false, Span: 0},
			want:       "NOT reduced: no third point was available, so nothing was attributed",
		},
	}
}

// fifthReachAccepted is a third point that was accepted AND attributed
// something: the state whose sentence is allowed to claim a subtraction.
func fifthReachAccepted() Classified {
	return Classified{VersionMove: 4, Ours: 8, Unclassified: 0, Reduced: true, Span: 2}
}

// fifthReachExplainedNothing is D5's new state, and it differs from
// fifthReachAccepted in ONE field: the version-move count.
//
// Everything else is held constant on purpose. Any difference between the two
// sentences is then a difference of what was ATTRIBUTED and not of arithmetic,
// which is the only way the assertion below says what it claims to say.
func fifthReachExplainedNothing() Classified {
	return Classified{VersionMove: 0, Ours: 12, Unclassified: 0, Reduced: true, Span: 2}
}

// TestClassificationReachKeepsStoryO34sFourSentences is the regression half of
// the sub-task: four of the five states are already right and must not move.
//
// _Requirements: R2, R2.2, R2.3_
func TestClassificationReachKeepsStoryO34sFourSentences(t *testing.T) {
	for _, state := range fifthReachShippedStates() {
		t.Run(state.name, func(t *testing.T) {
			if got := baselineReachProse(state.classified); got != state.want {
				t.Errorf("the reach reads\n  %q\nwant\n  %q\nThese four sentences ship today and this story changes only the fifth; a reworded state here is an output change no requirement asked for. If the first row is the one that moved, the new arm is guarding on the version-move count alone: Reduced with Span 0 is the SAME-VERSION case, whose count is zero because no version move ever existed, not because a third point explained nothing.",
					got, state.want)
			}
		})
	}
}

// TestClassificationReachDoesNotClaimASubtractionThatDidNotHappen is R2.3, and
// the sentence this story exists to stop printing.
//
// A third point can be accepted and attribute nothing — that is exactly what
// preference 2 does for a package whose every difference predates its bump — and
// the report then reads `reduced against a third point spanning N release steps`
// over a version-move count of zero. A confident sentence about work that did
// not happen is worse than the honest one it replaces.
//
// The control assertion is not decoration: it holds the phrase R2.3 names to the
// state that is allowed to use it, so that rewording the successful sentence
// cannot quietly make the real assertion vacuous.
//
// _Requirements: R2, R2.2, R2.3, R2.5_
func TestClassificationReachDoesNotClaimASubtractionThatDidNotHappen(t *testing.T) {
	accepted := baselineReachProse(fifthReachAccepted())
	explainedNothing := baselineReachProse(fifthReachExplainedNothing())

	if !strings.Contains(accepted, fifthReachSubtractionClaim) {
		t.Fatalf("the accepted state reads %q and no longer contains %q, which is the phrase R2.3 forbids over an empty subtraction; the assertion below would pass vacuously, so fix this row or restate the claim",
			accepted, fifthReachSubtractionClaim)
	}

	if strings.Contains(explainedNothing, fifthReachSubtractionClaim) {
		t.Errorf("a third point that attributed NOTHING reports %q; R2.3 is that the report shall not claim a reduction for a run in which nothing was subtracted, and the version-move count beside this sentence is 0",
			explainedNothing)
	}
	if explainedNothing == accepted {
		t.Errorf("a classification that explained nothing and one that explained 4 differences report the same reach, %q — the version-move count is not being read at all (D5)", explainedNothing)
	}
	if explainedNothing == "" {
		t.Fatal("the reach of the classification is not reported at all; R2.5 is the requirement that it be stated beside the counts")
	}

	// A third point EXISTED here, so its width is still owed: the reader has to
	// be able to tell "accepted and explained nothing" from "none was offered",
	// and the span is the only thing that distinguishes them.
	if span := strconv.Itoa(fifthReachExplainedNothing().Span); !strings.Contains(explainedNothing, span) {
		t.Errorf("the reach reads %q and never states the third point's width (%s release steps); a state that had a third point must say how wide it was, or it becomes indistinguishable from one that had none (R2.5)",
			explainedNothing, span)
	}

	// And it must reach the line the operator actually reads. R2.3 is about the
	// REPORT, not about a helper nobody prints.
	t.Run("on the rendered line", func(t *testing.T) {
		lines := classificationLines(CompareResult{
			Category: "net-libs", Package: "nodejs",
			Classified: fifthReachExplainedNothing(),
		})
		if len(lines) == 0 {
			t.Fatal("a classified package rendered no lines; the reduction reached no report (R2.4)")
		}
		if strings.Contains(lines[0], fifthReachSubtractionClaim) {
			t.Errorf("the lead line still claims a subtraction over a version-move count of 0:\n  %s", lines[0])
		}
		if !strings.Contains(lines[0], explainedNothing) {
			t.Errorf("the lead line is %q and does not carry the reach %q; the total is printed without saying what it is worth (R2.5)", lines[0], explainedNothing)
		}
	})
}

// TestClassificationReachFiveStatesArePairwiseDistinct is the collapse guard,
// and it is the reason this file exists at all: story 034's own reach states
// survived a mutation that read `Reduced` alone, because a collapse is invisible
// from one state at a time.
//
// The three states that involve a third point are given the SAME span here, so
// distinctness cannot be satisfied by the number alone — it has to come from
// what each sentence says about the third point.
//
// _Requirements: R2, R2.2, R2.3, R2.5_
func TestClassificationReachFiveStatesArePairwiseDistinct(t *testing.T) {
	states := []struct {
		name       string
		classified Classified
	}{
		{"attributed with no third point needed", Classified{VersionMove: 0, Ours: 492, Reduced: true, Span: 0}},
		{"accepted and explained something", Classified{VersionMove: 4, Ours: 8, Reduced: true, Span: 2}},
		{"accepted and explained nothing", Classified{VersionMove: 0, Ours: 12, Reduced: true, Span: 2}},
		{"refused for being wider than the baseline", Classified{VersionMove: 0, Ours: 12, Reduced: false, Span: 2}},
		{"none was available", Classified{VersionMove: 0, Ours: 12, Reduced: false, Span: 0}},
	}

	seen := make(map[string]string, len(states))
	for _, state := range states {
		prose := baselineReachProse(state.classified)
		if other, clash := seen[prose]; clash {
			t.Errorf("%q and %q report the same reach, %q — five different facts about the third point now read alike, and a classification whose reach is invisible is indistinguishable from a guess (R2.5)",
				state.name, other, prose)
		}
		seen[prose] = state.name
	}
}
