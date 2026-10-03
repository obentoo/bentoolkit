package main

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/obentoo/bentoolkit/internal/autoupdate"
	"github.com/obentoo/bentoolkit/internal/overlay"
)

// Authored for story 048, sub-task 2.1 — S048-R1.4, S048-R5.1, S048-R5.3.
//
// Both review adapters wrap EVERY failure the `claude` seam returns in one
// sentence: "the claude CLI could not read the two ebuilds"
// (overlay_compare_review.go:184, overlay_compare_realign.go:397). A review
// killed by its own 120-second deadline therefore reaches the operator as a
// file-reading problem, and the maintainer's measured run — five divergences,
// five killed reviews, zero readings — printed that sentence five times about a
// deadline in this program's own code.
//
// WHY THE SENTENCE CAN NEVER BE RIGHT AT THIS SEAM, which is what makes every
// case below hostile rather than merely negative. Neither adapter opens a file.
// ReviewDivergence and ReviewRealignment are handed a request whose Ours and
// Theirs/Baseline are ALREADY BYTES IN MEMORY — the two ebuilds were read
// upstream, by resolvePackagePaths, before either of these functions was
// called — and all this code does with them is put them on the CLI's stdin. So
// there is no benign fixture in which "reading the two ebuilds" is the true
// cause of an error returned from here, and the converse half of S048-R1.4 has
// no fixture to be written against at this seam. What CAN be written is every
// shape of failure the seam returns, and none of them may be reported as a
// reading failure.
//
// THE CONVERSE THAT IS WRITEABLE, and the reason the deadline is not the only
// case here. A fix that special-cased the deadline — "if the cause is a
// deadline say deadline, otherwise keep today's sentence" — would answer the
// deadline case and leave the misattribution alive for every other failure: a
// binary that could not start, and a non-zero exit, would both still be
// reported as unread ebuilds. So the deadline case is written FIRST, and the
// two failures that are not deadlines are written beside it; a rule half-fixed
// fails here rather than shipping.
//
// It also asserts what must NOT be lost. Sub-task 1.1 owns the classification
// and 1.2 owns the sentence naming the elapsed budget; this seam's whole job is
// to add the operation's own noun and let the classified cause through
// UNALTERED. A wrapper that dropped the cause, or re-classified it into a
// second answer free to disagree with the first, is caught by the
// cause-survives assertions.
//
// It lives in a file of its own rather than beside the adapters' existing cases
// so that the two frozen fragments stay exactly as they were authored, and so
// this guard can be materialised on its own.

// blameForbiddenFragments are the ways a message attributes a failure to
// reading the two ebuilds. Today's wrapper produces the first two; the third
// and fourth are the rewordings a fix could reach for while keeping the
// attribution, and a check that pinned only today's spelling would wave them
// through.
var blameForbiddenFragments = []string{
	"could not read",
	"read the two ebuilds",
	"reading the two ebuilds",
	"failed to read",
}

// assertNoReadingBlame fails when a message attributes the failure to reading
// the ebuilds, and reports WHICH fragment did it.
func assertNoReadingBlame(t *testing.T, path, got string) {
	t.Helper()
	lowered := strings.ToLower(got)
	for _, fragment := range blameForbiddenFragments {
		if strings.Contains(lowered, fragment) {
			t.Errorf("the %s review reported a failure as %q, which attributes it to reading the two ebuilds (%q).\n"+
				"S048-R1.4 forbids that attribution unless reading them is what failed, and it never is at this seam: both\n"+
				"ebuilds arrive as bytes in the request, already read upstream. The operator sent to the filesystem by this\n"+
				"sentence is debugging a problem that does not exist.", path, got, fragment)
			return
		}
	}
}

// assertCauseSurvives fails when the wrapper swallowed, replaced or re-answered
// the classified cause it was handed.
func assertCauseSurvives(t *testing.T, path string, err, cause error, causeText string) {
	t.Helper()
	if err == nil {
		t.Fatalf("the %s review returned no error for a failed invocation; the failure must reach the caller", path)
	}
	if !errors.Is(err, cause) {
		t.Errorf("the %s review returned %v, which does not wrap the cause it was handed.\n"+
			"S048-R1.4 asks the wrapper to name its own operation and pass the CLASSIFIED cause through unaltered; a cause\n"+
			"that does not survive %%w cannot be examined by anything downstream.", path, err)
	}
	if !strings.Contains(err.Error(), causeText) {
		t.Errorf("the %s review reported %q, which no longer carries the classified cause %q.\n"+
			"Sub-task 1.1 owns the distinction between a deadline, a failure to start and a non-zero exit; a wrapper that\n"+
			"restates it is a second answer free to drift from the first.", path, err.Error(), causeText)
	}
	if err.Error() == causeText {
		t.Errorf("the %s review returned the bare cause %q and said nothing about which review failed.\n"+
			"S048-R1.4 replaces a wrong noun with the right one, not with silence: the operator still needs to know which\n"+
			"of the two review paths produced this.", path, err.Error())
	}
}

// blameCases are the three shapes the `claude` seam fails in. Each is written
// as the error the client hands back, not as a sentence this test invented:
// ErrLLMRequestFailed is what internal/autoupdate wraps every failed
// invocation in (claude_code.go:370-386), so a wrapper that reclassifies is
// reclassifying something real.
//
// The deadline is first because it is the shipped defect. The other two are the
// converse half: they are what a deadline-only fix would leave still lying.
func blameCases() []struct {
	name      string
	causeText string
	cause     error
} {
	return []struct {
		name      string
		causeText string
		cause     error
	}{
		{
			name:      "a deadline elapsed",
			causeText: "claude CLI failed: the review's 90s deadline elapsed",
			cause:     autoupdate.ErrLLMRequestFailed,
		},
		{
			name:      "the CLI could not start",
			causeText: "claude CLI failed: fork/exec /usr/bin/claude: permission denied",
			cause:     autoupdate.ErrLLMRequestFailed,
		},
		{
			name:      "the CLI exited non-zero",
			causeText: "claude CLI failed (error_during_execution): the model returned no result",
			cause:     autoupdate.ErrLLMRequestFailed,
		},
	}
}

// TestDivergenceReviewWrapperDoesNotBlameTheEbuilds is S048-R1.4 on the
// divergence path (overlay_compare_review.go:184).
//
// _Requirements: S048-R1.4, S048-R5.1_
func TestDivergenceReviewWrapperDoesNotBlameTheEbuilds(t *testing.T) {
	for _, tc := range blameCases() {
		t.Run(tc.name, func(t *testing.T) {
			cause := fmt.Errorf("%w: %s", tc.cause, tc.causeText)
			reviewer := reviewerOver(t, &fakeAsker{err: cause})

			note, err := reviewer.ReviewDivergence(context.Background(), cmdReviewRequest())

			assertCauseSurvives(t, "divergence", err, tc.cause, cause.Error())
			if err != nil {
				assertNoReadingBlame(t, "divergence", err.Error())
			}
			if note != (overlay.ReviewNote{}) {
				t.Errorf("a failed review returned %+v, want the zero note", note)
			}
		})
	}
}

// TestRealignReviewWrapperDoesNotBlameTheEbuilds is the same rule on the
// realignment path (overlay_compare_realign.go:397).
//
// It is a separate test rather than a subtest of the one above because the two
// wrappers are two lines in two files carrying the same sentence: fixing one
// and not the other leaves the realign path — the path the maintainer's measured
// run actually used — still blaming the ebuilds, and a guard that covered only
// the divergence path would report that as clean.
//
// _Requirements: S048-R1.4, S048-R5.1_
func TestRealignReviewWrapperDoesNotBlameTheEbuilds(t *testing.T) {
	for _, tc := range blameCases() {
		t.Run(tc.name, func(t *testing.T) {
			cause := fmt.Errorf("%w: %s", tc.cause, tc.causeText)
			reviewer := realignReviewerOverAsker(t, &fakeAsker{err: cause})

			note, err := reviewer.ReviewRealignment(context.Background(), realignBlameRequest())

			assertCauseSurvives(t, "realignment", err, tc.cause, cause.Error())
			if err != nil {
				assertNoReadingBlame(t, "realignment", err.Error())
			}
			if note != (overlay.RealignNote{}) {
				t.Errorf("a failed review returned %+v, want the zero note", note)
			}
		})
	}
}

// realignReviewerOverAsker builds the realignment adapter over a scripted asker
// THROUGH THE PRODUCTION CONSTRUCTOR, so this test never assembles a shape
// production cannot produce — the same reason reviewerOver exists for the
// divergence side.
//
// Sub-task 3.3 moved newRealignReviewer's signature: it is one of the consumers
// that carry the review budget now that it enters through the seam, so this call
// site passes one too (cmdReviewBudget). Nothing this file asserts depends on the
// value — the asker is scripted and no client is built.
func realignReviewerOverAsker(t *testing.T, asker claudeAsker) overlay.RealignReviewer {
	t.Helper()
	stubClaudeAsker(t, func() (claudeAsker, error) { return asker, nil })
	reviewer, err := newRealignReviewer(cmdReviewBudget)
	if err != nil {
		t.Fatalf("newRealignReviewer returned %v, want nil", err)
	}
	if reviewer == nil {
		t.Fatal("newRealignReviewer returned no reviewer over a constructible asker")
	}
	return reviewer
}

// realignBlameRequest is one undeclared divergence as the realign pass submits
// it: both ebuilds are BYTES ALREADY IN MEMORY, which is the fact that makes
// "could not read the two ebuilds" false for every failure this seam can return.
func realignBlameRequest() overlay.RealignRequest {
	return overlay.RealignRequest{
		Category: "kde-plasma",
		Package:  "spectacle",
		Version:  "6.7.4",
		Ours:     []byte(cmdReviewOurs),
		Baseline: []byte(cmdReviewTheirs),
	}
}
