package overlay

// Authored for story 047, sub-task 1.2 — S047-R4.1, S047-R4.2, S047-R3.3.
//
// The rules:
//
//   - S047-R4.1: "WHEN AnnotateReviews finishes THE SYSTEM SHALL have recorded,
//     on each CompareResult, which of the four reading states applies to it."
//   - S047-R4.2: "WHEN a review fails THE SYSTEM SHALL NOT emit a logger.Warn
//     describing it."
//   - S047-R3.3: "WHERE a package was not compared because its two versions
//     differ THE SYSTEM SHALL say so."
//
// This is the fact that replaces the warning. The measured cost the story opens
// with is eleven redundant packages of which ZERO carry a reading — five whose
// review was killed and six that were never content-compared — and the reason
// the screen cannot tell them apart is that both outcomes left the model
// untouched and went out as log lines above the report instead.
//
// The hostile cases come first and they are the ones that fire wrongly:
// a failed review reading as "nobody asked", and a package the content check
// refused reading as one nobody wanted read.
//
// RED ON ARRIVAL: CompareResult.Reading does not exist, and review.go warns at
// four sites this file asserts are silent.
//
// Reused rather than restated: reviewFixture, reviewFixtureIn, resultFor,
// annotateReviewer, reviewedAtoms and unreviewableAtoms come from
// review_test.go; captureReviewWarnings from review_cache_test.go. A second
// spelling of "which packages carry an undeclared divergence" would be a second
// answer to it.
//
// Every name carries the TestReviewReading prefix.

import (
	"errors"
	"testing"
)

// reviewReadingUsableNote is a reply that speaks: an origin and a summary, which
// is what reviewNoteSpeaks asks for.
var reviewReadingUsableNote = ReviewNote{
	Origin:      OriginOverlay,
	Summary:     "adds a patch ::gentoo does not ship",
	Declaration: "patched = true",
}

// TestReviewReadingAFailedReviewIsNotAnUnaskedOne is the hostile half, written
// first: the case where the rule fires WRONGLY by collapsing two facts.
//
// A reviewer that errors leaves the zero ReviewNote behind, and the zero
// ReviewNote is what every un-reviewed package carries too. So the cheapest
// implementation of S047-R4.1 — set Reading only where a note is stored — is
// green for the packages that were read and leaves the five killed reviews of
// the measured run reading as "not requested". That is the screen the operator
// already has.
func TestReviewReadingAFailedReviewIsNotAnUnaskedOne(t *testing.T) {
	report, prov, opts := reviewFixture(t)
	rev := &annotateReviewer{t: t, offLimits: unreviewableAtoms, err: errors.New("claude: context deadline exceeded")}

	AnnotateReviews(t.Context(), report, rev, prov, opts)

	for _, atom := range reviewedAtoms {
		got := resultFor(t, report, atom).Reading
		if got == ReadingNotRequested {
			t.Errorf("%s had its review attempted and killed, and carries ReadingNotRequested — the state every package "+
				"nobody asked about carries. \"the deadline killed it\" and \"nobody asked\" are the two facts S047-R3.4 "+
				"exists to separate", atom)
		}
		if got != ReadingFailed {
			t.Errorf("%s carries Reading %d after a reviewer error, want ReadingFailed (%d)", atom, got, ReadingFailed)
		}
	}
}

// TestReviewReadingAnUnusableAnswerFailsLikeAnError is the same hostile shape
// one step further in: a reply that PARSED and said nothing.
//
// review.go answers it at a different call site from the error above, so an
// implementation that records the state beside one of the two warnings it is
// replacing leaves the other silent — and a note with no classification is
// indistinguishable, in the model, from no note at all.
func TestReviewReadingAnUnusableAnswerFailsLikeAnError(t *testing.T) {
	report, prov, opts := reviewFixture(t)
	rev := &annotateReviewer{t: t, offLimits: unreviewableAtoms, note: ReviewNote{Summary: "  "}}

	AnnotateReviews(t.Context(), report, rev, prov, opts)

	for _, atom := range reviewedAtoms {
		if got := resultFor(t, report, atom).Reading; got != ReadingFailed {
			t.Errorf("%s came back without a classification or a summary and carries Reading %d, want ReadingFailed (%d): "+
				"S047-R5.5's four failures are one answer to the operator, so they must be one answer in the model",
				atom, got, ReadingFailed)
		}
	}
}

// TestReviewReadingARefusedPairSaysSo is the second hostile half, on the axis
// S047-R3.3 names: six of the eleven were never content-compared BECAUSE their
// two versions differ, and the report today leaves that indistinguishable from
// the other three causes of NotVerified.
//
// net-libs/nodejs is the fixture's refused pair. It must never reach a reviewer
// — annotateReviewer asserts that itself through offLimits — and it must not
// come out reading as "not requested" either: nothing was requested because
// nothing COULD be, which is a different sentence and the one the operator acts
// on.
func TestReviewReadingARefusedPairSaysSo(t *testing.T) {
	report, prov, opts := reviewFixture(t)
	rev := &annotateReviewer{t: t, offLimits: unreviewableAtoms, note: reviewReadingUsableNote}

	AnnotateReviews(t.Context(), report, rev, prov, opts)

	refused := resultFor(t, report, "net-libs/nodejs")
	if refused.Verified != NotVerified {
		t.Fatalf("the fixture is wrong: net-libs/nodejs is Verified %d, and this case needs the pair the content check refused", refused.Verified)
	}
	if refused.Reading != ReadingNotComparable {
		t.Errorf("net-libs/nodejs carries Reading %d, want ReadingNotComparable (%d): its two versions differ, so no content "+
			"was compared, and S047-R3.3 asks the report to say which of NotVerified's four causes applies",
			refused.Reading, ReadingNotComparable)
	}
}

// TestReviewReadingAnUnaskedPackageStaysUnasked is the benign half, and it is
// the one that keeps every fix above from being bought by writing ReadingFailed
// everywhere.
//
// Two packages are deliberately never submitted, for two different reasons: an
// entry declares app-editors/zed's divergence, and dev-libs/libixion's two
// ebuilds are byte-identical. Neither was read, neither could fail, and neither
// was refused by the content check — "not requested" is the true answer for
// both.
func TestReviewReadingAnUnaskedPackageStaysUnasked(t *testing.T) {
	report, prov, opts := reviewFixture(t)
	rev := &annotateReviewer{t: t, offLimits: unreviewableAtoms, note: reviewReadingUsableNote}

	AnnotateReviews(t.Context(), report, rev, prov, opts)

	for _, atom := range []string{"app-editors/zed", "dev-libs/libixion"} {
		if got := resultFor(t, report, atom).Reading; got != ReadingNotRequested {
			t.Errorf("%s carries Reading %d, want ReadingNotRequested (%d): no review was asked for it and the content "+
				"check did not refuse it", atom, got, ReadingNotRequested)
		}
	}

	for _, atom := range reviewedAtoms {
		if got := resultFor(t, report, atom).Reading; got != ReadingDone {
			t.Errorf("%s was read and its note stored, and carries Reading %d, want ReadingDone (%d)", atom, got, ReadingDone)
		}
	}
}

// TestReviewReadingACachedAnswerIsStillARead is the THIRD element the pair
// above never exercises, and the one a fix written against it would miss.
//
// A stored note is a second route to the same state: the second run over an
// unchanged overlay answers from the cache and never calls the model. If the
// state is recorded beside the reviewer call rather than beside the ANSWER,
// then run one reports two packages read and run two reports the same two
// unread, over an overlay in which nothing moved — and the difference between
// the two documents would be attributed to the overlay rather than to the
// cache.
//
// The two fixtures below are separate trees sharing one cache directory. The
// key is the two ebuilds' CONTENT (contentFingerprint), so the second run hits
// every entry the first stored.
func TestReviewReadingACachedAnswerIsStillARead(t *testing.T) {
	shared := t.TempDir()

	first, prov, opts := reviewFixtureIn(t, shared)
	warm := &annotateReviewer{t: t, offLimits: unreviewableAtoms, note: reviewReadingUsableNote}
	AnnotateReviews(t.Context(), first, warm, prov, opts)
	if len(warm.calls) == 0 {
		t.Fatal("the fixture is wrong: the first run asked the model nothing, so there is no cache entry for the second to hit")
	}

	second, prov2, opts2 := reviewFixtureIn(t, shared)
	cold := &annotateReviewer{t: t, offLimits: unreviewableAtoms, err: errors.New("the model must not be asked twice for one unchanged pair")}
	AnnotateReviews(t.Context(), second, cold, prov2, opts2)

	if len(cold.calls) != 0 {
		t.Fatalf("the second run submitted %v, so it did not read from the cache and this case proves nothing about the cached route", cold.atoms())
	}
	for _, atom := range reviewedAtoms {
		if got := resultFor(t, second, atom).Reading; got != ReadingDone {
			t.Errorf("%s was answered from the cache and carries Reading %d, want ReadingDone (%d): two runs over an "+
				"unchanged overlay must not disagree about whether anybody read it", atom, got, ReadingDone)
		}
	}
}

// TestReviewReadingNoOutcomeReachesTheOperatorAsAWarning is S047-R4.2, and it
// is the half that makes the field worth having.
//
// Four warnings used to describe a review outcome (review.go:111, 122, 141, 145).
// Every one of them is now a row in the report, and a warning that duplicates a
// visible row is noise printed ABOVE the thing it duplicates — which is the
// boundary story 046 closed: a library does not format for an operator.
//
// The three warnings elsewhere in this package that describe something
// else are out of scope and are deliberately not asserted here. What is
// asserted is that no warning NAMES a package whose review failed.
func TestReviewReadingNoOutcomeReachesTheOperatorAsAWarning(t *testing.T) {
	warnLog, warnings := captureReviewWarnings(t)
	report, prov, opts := reviewFixture(t)
	opts.Logger = warnLog
	rev := &annotateReviewer{t: t, offLimits: unreviewableAtoms, err: errors.New("claude: exit status 1")}

	AnnotateReviews(t.Context(), report, rev, prov, opts)

	for _, atom := range reviewedAtoms {
		if named := warningsNaming(warnings(), atom); len(named) != 0 {
			t.Errorf("the failed review of %s was warned about %d times: %v.\n"+
				"    S047-R4.2: the outcome travels on the result (Reading), and a warning duplicating a visible row is noise",
				atom, len(named), named)
		}
	}
	// The state must be there INSTEAD. A run that neither warns nor records
	// would satisfy the loop above and lose the fact entirely.
	for _, atom := range reviewedAtoms {
		if got := resultFor(t, report, atom).Reading; got != ReadingFailed {
			t.Errorf("%s carries Reading %d after the warning was removed, want ReadingFailed (%d): the fact must move onto "+
				"the result, not disappear with the log line", atom, got, ReadingFailed)
		}
	}
}
