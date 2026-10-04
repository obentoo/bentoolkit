package overlay

// Authored for story 057, sub-task 3.1 — R2.1-R2.8 (R6.5 and R6.10 are pinned by
// the existing review_reading_test.go and review_test.go cases).
//
// Contract names used here: CompareResult.ReviewFailure, CompareResult.FailureText,
// CompareOptions.Redact ([]string), and the four review sentinels declared by
// internal/overlay: ErrReviewTimedOut, ErrReviewCouldNotStart,
// ErrReviewExitedNonZero, ErrReviewUnusableReply. A cause is read through
// fmt.Sprint, so a string type or a Stringer both satisfy the test.
//
// Hostile first: the fixtures that would make the classifier fire WRONGLY — a
// bare deadline read as a cancellation or a timeout, a sentence that merely SAYS
// "ran out of time" read as the sentinel, two packages' causes swapped, one
// cancellation smeared over a result that already had its own cause.
//
// Reused rather than restated: reviewFixture, resultFor, annotateReviewer,
// reviewedAtoms and unreviewableAtoms (review_test.go).
//
// RED ON ARRIVAL: none of the names above exist.

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// s057ReviewWords is the closed review vocabulary, in R2 order (story Assumptions).
var s057ReviewWords = []string{
	"timed out", "could not start", "exited non-zero", "empty or unusable reply",
	"cancelled", "ebuild unreadable", "other",
}

func s057ReviewWord(v any) string { return fmt.Sprint(v) }

func s057IsReviewWord(w string) bool {
	for _, v := range s057ReviewWords {
		if v == w {
			return true
		}
	}
	return false
}

// s057AssertFailed checks one result's state, cause and (when wantText is not
// "-") its recorded text.
func s057AssertFailed(t *testing.T, r CompareResult, wantCause, wantText string) {
	t.Helper()
	atom := r.Category + "/" + r.Package
	if r.Reading != ReadingFailed {
		t.Errorf("%s: Reading %d, want ReadingFailed (%d) — the cause sits BESIDE the single failed state (story 047, requirement 5.5)", atom, r.Reading, ReadingFailed)
	}
	if got := s057ReviewWord(r.ReviewFailure); got != wantCause {
		t.Errorf("%s: ReviewFailure %q, want %q", atom, got, wantCause)
	}
	if wantText != "-" && r.FailureText != wantText {
		t.Errorf("%s: FailureText %q, want the error's full text %q (R2.8)", atom, r.FailureText, wantText)
	}
}

func TestReviewFailureRecordsEachCause(t *testing.T) {
	observed := map[string]string{} // cause word -> the case that produced it

	// ---- hostile: errors that would be classified wrongly ------------------

	t.Run("a bare DeadlineExceeded is other, neither cancelled nor timed out", func(t *testing.T) {
		report, prov, opts := reviewFixture(t)
		err := fmt.Errorf("the divergence review failed: %w", context.DeadlineExceeded)
		AnnotateReviews(t.Context(), report, &annotateReviewer{t: t, offLimits: unreviewableAtoms, err: err}, prov, opts)
		for _, atom := range reviewedAtoms {
			s057AssertFailed(t, resultFor(t, report, atom), "other", err.Error())
		}
		observed["other"] = t.Name()
	})

	t.Run("a sentence that says it ran out of time, without the sentinel, is other", func(t *testing.T) {
		report, prov, opts := reviewFixture(t)
		err := errors.New("the divergence review failed: LLM API request failed: claude CLI ran out of time: its 2m0s budget elapsed before it answered")
		AnnotateReviews(t.Context(), report, &annotateReviewer{t: t, offLimits: unreviewableAtoms, err: err}, prov, opts)
		for _, atom := range reviewedAtoms {
			s057AssertFailed(t, resultFor(t, report, atom), "other", err.Error())
		}
	})

	t.Run("two packages with two causes keep their own, not swapped", func(t *testing.T) {
		report, prov, opts := reviewFixture(t)
		errs := map[string]error{
			"kde-plasma/kwin":      fmt.Errorf("the divergence review failed: %w", ErrReviewTimedOut),
			"kde-plasma/spectacle": fmt.Errorf("the divergence review failed: %w", ErrReviewCouldNotStart),
		}
		rev := &annotateReviewer{t: t, offLimits: unreviewableAtoms, answer: func(req ReviewRequest) (ReviewNote, error) {
			return ReviewNote{}, errs[req.Category+"/"+req.Package]
		}}
		AnnotateReviews(t.Context(), report, rev, prov, opts)
		s057AssertFailed(t, resultFor(t, report, "kde-plasma/kwin"), "timed out", errs["kde-plasma/kwin"].Error())
		s057AssertFailed(t, resultFor(t, report, "kde-plasma/spectacle"), "could not start", errs["kde-plasma/spectacle"].Error())
	})

	t.Run("a cancellation mid-run marks the rest cancelled and leaves the earlier cause alone", func(t *testing.T) {
		report, prov, opts := reviewFixture(t)
		ctx, cancel := context.WithCancel(context.Background())
		t.Cleanup(cancel)
		first := fmt.Errorf("the divergence review failed: %w", ErrReviewExitedNonZero)
		rev := &annotateReviewer{t: t, offLimits: unreviewableAtoms, answer: func(ReviewRequest) (ReviewNote, error) {
			cancel() // Ctrl-C arrives while the first review is failing on its own
			return ReviewNote{}, first
		}}
		AnnotateReviews(ctx, report, rev, prov, opts)
		if n := len(rev.calls); n != 1 {
			t.Fatalf("the reviewer was called %d times, want 1: the fixture needs the second package to be stopped by the context", n)
		}
		s057AssertFailed(t, resultFor(t, report, reviewedAtoms[0]), "exited non-zero", first.Error())
		s057AssertFailed(t, resultFor(t, report, reviewedAtoms[1]), "cancelled", "-")
		observed["exited non-zero"] = t.Name()
	})

	t.Run("a context already done marks every pending result cancelled", func(t *testing.T) {
		report, prov, opts := reviewFixture(t)
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		rev := &annotateReviewer{t: t, offLimits: unreviewableAtoms, note: reviewReadingUsableNote}
		AnnotateReviews(ctx, report, rev, prov, opts)
		for _, atom := range reviewedAtoms {
			s057AssertFailed(t, resultFor(t, report, atom), "cancelled", "-")
		}
		observed["cancelled"] = t.Name()
	})

	t.Run("an ebuild that vanished is ebuild unreadable, and the other package is untouched", func(t *testing.T) {
		report, prov, opts := reviewFixture(t)
		ours := filepath.Join(opts.OverlayPath, "kde-plasma", "kwin", "kwin-6.7.4.ebuild")
		if err := os.Remove(ours); err != nil {
			t.Fatalf("the fixture is wrong: removing %s: %v", ours, err)
		}
		rev := &annotateReviewer{t: t, offLimits: unreviewableAtoms, note: reviewReadingUsableNote}
		AnnotateReviews(t.Context(), report, rev, prov, opts)
		s057AssertFailed(t, resultFor(t, report, "kde-plasma/kwin"), "ebuild unreadable", "-")
		ok := resultFor(t, report, "kde-plasma/spectacle")
		if ok.Reading != ReadingDone {
			t.Fatalf("kde-plasma/spectacle carries Reading %d, want ReadingDone", ok.Reading)
		}
		if w := s057ReviewWord(ok.ReviewFailure); s057IsReviewWord(w) || ok.FailureText != "" {
			t.Errorf("a review that succeeded carries cause %q text %q; the neighbour's failure leaked onto it", w, ok.FailureText)
		}
		observed["ebuild unreadable"] = t.Name()
	})

	t.Run("a reply that parsed and says nothing is empty or unusable reply", func(t *testing.T) {
		report, prov, opts := reviewFixture(t)
		rev := &annotateReviewer{t: t, offLimits: unreviewableAtoms, note: ReviewNote{Summary: "  "}}
		AnnotateReviews(t.Context(), report, rev, prov, opts)
		for _, atom := range reviewedAtoms {
			s057AssertFailed(t, resultFor(t, report, atom), "empty or unusable reply", "-")
		}
		observed["empty or unusable reply"] = t.Name()
	})

	// ---- benign: every sentinel gives its own word --------------------------

	for _, tc := range []struct {
		word string
		err  error
	}{
		{"timed out", ErrReviewTimedOut},
		{"could not start", ErrReviewCouldNotStart},
		{"exited non-zero", ErrReviewExitedNonZero},
		{"empty or unusable reply", ErrReviewUnusableReply},
		{"cancelled", context.Canceled},
	} {
		t.Run("a wrapped sentinel gives "+tc.word, func(t *testing.T) {
			report, prov, opts := reviewFixture(t)
			err := fmt.Errorf("the divergence review failed: %w", tc.err)
			AnnotateReviews(t.Context(), report, &annotateReviewer{t: t, offLimits: unreviewableAtoms, err: err}, prov, opts)
			for _, atom := range reviewedAtoms {
				s057AssertFailed(t, resultFor(t, report, atom), tc.word, err.Error())
			}
			observed[tc.word] = t.Name()
		})
	}

	t.Run("a review that was read carries no cause and no text", func(t *testing.T) {
		report, prov, opts := reviewFixture(t)
		AnnotateReviews(t.Context(), report, &annotateReviewer{t: t, offLimits: unreviewableAtoms, note: reviewReadingUsableNote}, prov, opts)
		for _, r := range report.Results {
			if w := s057ReviewWord(r.ReviewFailure); s057IsReviewWord(w) || r.FailureText != "" {
				t.Errorf("%s/%s (Reading %d) carries cause %q text %q, want neither", r.Category, r.Package, r.Reading, w, r.FailureText)
			}
		}
	})

	t.Run("the seven words are distinct and all reachable", func(t *testing.T) {
		seen := map[string]bool{}
		for _, w := range s057ReviewWords {
			if seen[w] {
				t.Errorf("the vocabulary spells %q twice", w)
			}
			seen[w] = true
			if observed[w] == "" {
				t.Errorf("no case above produced the cause %q; every word of R2's vocabulary must be reachable", w)
			}
		}
	})
}

// TestReviewFailureTextIsRedacted is R4.7 on the review side: the text is scrubbed
// of every listed value, the cause is not.
func TestReviewFailureTextIsRedacted(t *testing.T) {
	const tok = "ghp_TESTTOKEN0000"
	report, prov, opts := reviewFixture(t)
	opts.Redact = []string{tok, "", "out"} // "out" is part of the cause word; the cause must survive it
	err := fmt.Errorf("the divergence review failed: %w: token %s was sent, then %s again", ErrReviewTimedOut, tok, tok)
	AnnotateReviews(t.Context(), report, &annotateReviewer{t: t, offLimits: unreviewableAtoms, err: err}, prov, opts)

	want := strings.ReplaceAll(strings.ReplaceAll(err.Error(), tok, "***"), "out", "***")
	for _, atom := range reviewedAtoms {
		r := resultFor(t, report, atom)
		if r.FailureText != want {
			t.Errorf("%s: FailureText\n got: %q\nwant: %q (R4.7)", atom, r.FailureText, want)
		}
		if strings.Contains(r.FailureText, tok) {
			t.Errorf("%s: the token survived into FailureText: %q", atom, r.FailureText)
		}
		if got := s057ReviewWord(r.ReviewFailure); got != "timed out" {
			t.Errorf("%s: the redaction changed the cause to %q, want \"timed out\"", atom, got)
		}
	}
}
