package main

// Authored for story 057, sub-task 3.3 — R2.10 (R6.6 wording is pinned by
// TestDivergenceReviewWrapperDoesNotBlameTheEbuilds and
// TestRealignReviewWrapperDoesNotBlameTheEbuilds; R6.10 by TestAnchorCitationsResolve).
//
// Contract names used here: the autoupdate outcome sentinels (2.1)
// ErrClaudeTimedOut, ErrClaudeStopped, ErrClaudeCouldNotStart,
// ErrClaudeExitedNonZero, ErrClaudeUnusableOutput, and the overlay review
// sentinels (3.1) ErrReviewTimedOut, ErrReviewCouldNotStart,
// ErrReviewExitedNonZero, ErrReviewUnusableReply.
//
// Hostile first: a STOPPED run translated as a timeout, and a bare
// ErrLLMRequestFailed whose SENTENCE says "ran out of time" translated by reading
// its words. Then the benign one-to-one map, a decode failure, and one case over
// the REAL ClaudeCodeClient so the translation is proved against the error shape
// autoupdate actually produces rather than one this file invented.
//
// Reused: reviewerOver, realignReviewerOverAsker, fakeAsker, cmdReviewRequest,
// realignBlameRequest, stubClaudeAsker (existing cmd/bentoo tests).
//
// RED ON ARRIVAL: none of the nine sentinels exist.

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/obentoo/bentoolkit/internal/autoupdate/llm"
	"github.com/obentoo/bentoolkit/internal/overlay"
)

// s057ClaudeFailure mirrors how autoupdate attaches an outcome: the sentence is
// the error's own, and the sentinels ride on Unwrap() []error.
type s057ClaudeFailure struct {
	text string
	errs []error
}

func (e *s057ClaudeFailure) Error() string   { return e.text }
func (e *s057ClaudeFailure) Unwrap() []error { return e.errs }

var s057ReviewSentinels = map[string]error{
	"timed out":               overlay.ErrReviewTimedOut,
	"could not start":         overlay.ErrReviewCouldNotStart,
	"exited non-zero":         overlay.ErrReviewExitedNonZero,
	"empty or unusable reply": overlay.ErrReviewUnusableReply,
}

// s057Adapters runs one scripted asker through each production adapter.
var s057Adapters = []struct {
	name, failPrefix, decodePrefix string
	call                           func(t *testing.T, asker claudeAsker) error
}{
	{"divergence", "the divergence review failed: ", "the model's reply is not the JSON the review asked for: ",
		func(t *testing.T, asker claudeAsker) error {
			_, err := reviewerOver(t, asker).ReviewDivergence(context.Background(), cmdReviewRequest())
			return err
		}},
	{"realignment", "the realignment review failed: ", "the model's reply is not the JSON the realignment review asked for: ",
		func(t *testing.T, asker claudeAsker) error {
			_, err := realignReviewerOverAsker(t, asker).ReviewRealignment(context.Background(), realignBlameRequest())
			return err
		}},
}

// s057AssertOnly asserts err matches exactly the overlay sentinel named want
// ("" = none of them).
func s057AssertOnly(t *testing.T, err error, want string) {
	t.Helper()
	if err == nil {
		t.Fatal("the adapter returned no error")
	}
	for word, sentinel := range s057ReviewSentinels {
		if got := errors.Is(err, sentinel); got != (word == want) {
			t.Errorf("errors.Is(%q, overlay <%s>) = %v; want exactly %q (R2.10)", err, word, got, want)
		}
	}
}

func TestReviewAdaptersTranslateTheClaudeOutcome(t *testing.T) {
	cases := []struct {
		name    string
		outcome error // the autoupdate sentinel riding on the failure, nil = none
		text    string
		want    string // overlay sentinel word, "" = none
	}{
		// Hostile.
		{"a stopped run is not a timeout", llm.ErrClaudeStopped,
			"LLM API request failed: claude CLI was stopped before it answered", ""},
		{"a bare request failure whose sentence says it ran out of time matches nothing", nil,
			"LLM API request failed: claude CLI ran out of time: its 2m0s budget elapsed before it answered", ""},
		// Benign: one outcome, one overlay sentinel.
		{"timed out", llm.ErrClaudeTimedOut,
			"LLM API request failed: claude CLI ran out of time: its 90s budget elapsed before it answered", "timed out"},
		{"could not start", llm.ErrClaudeCouldNotStart,
			"LLM API request failed: claude CLI could not start: fork/exec /usr/bin/claude: permission denied", "could not start"},
		{"exited non-zero", llm.ErrClaudeExitedNonZero,
			"LLM API request failed: claude CLI failed: exit status 3", "exited non-zero"},
		{"unusable output", llm.ErrClaudeUnusableOutput,
			"LLM API request failed: claude CLI emitted non-JSON output", "empty or unusable reply"},
	}
	for _, ad := range s057Adapters {
		for _, tc := range cases {
			t.Run(ad.name+"/"+tc.name, func(t *testing.T) {
				errs := []error{llm.ErrLLMRequestFailed}
				if tc.outcome != nil {
					errs = append(errs, tc.outcome)
				}
				cause := &s057ClaudeFailure{text: tc.text, errs: errs}

				err := ad.call(t, &fakeAsker{err: cause})

				s057AssertOnly(t, err, tc.want)
				if err != nil {
					if got, want := err.Error(), ad.failPrefix+tc.text; got != want {
						t.Errorf("the wrapper's text changed:\n got: %q\nwant: %q (R6.6: no sentence changes)", got, want)
					}
					for _, e := range errs {
						if !errors.Is(err, e) {
							t.Errorf("the translation dropped the original %v; the cause must still travel", e)
						}
					}
				}
			})
		}
		// Maintainer decision (2026-09-23): a stopped run is reported as
		// `cancelled`, so the translation makes it satisfy context.Canceled —
		// and still none of the four overlay sentinels, as the hostile case
		// above asserts.
		t.Run(ad.name+"/a stopped run reads as cancelled", func(t *testing.T) {
			text := "LLM API request failed: claude CLI was stopped before it answered: context canceled"
			cause := &s057ClaudeFailure{text: text, errs: []error{llm.ErrLLMRequestFailed, llm.ErrClaudeStopped}}
			err := ad.call(t, &fakeAsker{err: cause})
			if !errors.Is(err, context.Canceled) {
				t.Errorf("errors.Is(%q, context.Canceled) = false; a stopped run must read as cancelled", err)
			}
			s057AssertOnly(t, err, "")
			if err != nil && err.Error() != ad.failPrefix+text {
				t.Errorf("the wrapper's text changed: %q, want %q (R6.6)", err, ad.failPrefix+text)
			}
		})
		t.Run(ad.name+"/a reply that is not JSON is an unusable reply", func(t *testing.T) {
			err := ad.call(t, &fakeAsker{reply: "this is prose, not JSON"})
			s057AssertOnly(t, err, "empty or unusable reply")
			if err != nil && !strings.HasPrefix(err.Error(), ad.decodePrefix) {
				t.Errorf("the decode failure's text changed: %q, want the prefix %q", err, ad.decodePrefix)
			}
		})
	}
}

// TestReviewAdaptersTranslateARealUnstartableClaude is the fidelity case: the
// real ClaudeCodeClient, a `claude` that LookPath finds, and an exec seam naming a
// binary that does not exist — so the error is autoupdate's own, not a replica.
func TestReviewAdaptersTranslateARealUnstartableClaude(t *testing.T) {
	bin := t.TempDir()
	if err := os.WriteFile(filepath.Join(bin, "claude"), []byte("#!/bin/sh\nexit 0\n"), 0o700); err != nil {
		t.Fatalf("writing the fake claude: %v", err)
	}
	t.Setenv("PATH", bin)
	t.Setenv("ANTHROPIC_API_KEY", "")
	missing := filepath.Join(bin, "no-such-claude")

	for _, ad := range s057Adapters {
		t.Run(ad.name, func(t *testing.T) {
			client, err := llm.NewClaudeCodeClient(llm.LLMConfig{},
				llm.WithClaudeCodeExecCommand(func(ctx context.Context, _ string, arg ...string) *exec.Cmd {
					return exec.CommandContext(ctx, missing, arg...)
				}),
				llm.WithClaudeCodeTimeout(30*time.Second))
			if err != nil {
				t.Fatalf("NewClaudeCodeClient: %v", err)
			}
			err = ad.call(t, client)
			if !errors.Is(err, llm.ErrClaudeCouldNotStart) {
				t.Errorf("the real client's error %q does not match llm.ErrClaudeCouldNotStart (sub-task 2.1 not in place?)", err)
			}
			s057AssertOnly(t, err, "could not start")
		})
	}
}
