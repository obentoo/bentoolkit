package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/obentoo/bentoolkit/internal/autoupdate/validate"
	"github.com/obentoo/bentoolkit/internal/realign"
)

// ---------------------------------------------------------------------------
// Story 034, sub-task 10.1. Pinned contract:
//
//	var realignPromote            = realign.Promote           // seam: reporting tests stub it, writing tests drive it
//	var confirmRealignPublishFn   = confirmAction              // seam: the per-package question
//	var realignPublishIsInteractive = registryPromptIsInteractive
//	func realignProofCarriesEvidence(proof realign.Proof) bool
//	func offerRealignPublish(c realignCandidate, proof realign.Proof, overlayRoot string)
//
// The publish question is D8's third authority being asked, and these tests pin
// the three properties D8c states: it is asked PER PACKAGE and names the atom;
// it is asked ONLY ON EVIDENCE (at least one gate reporting PASS — an
// all-SKIPPED proof is a proof of nothing); and NO FLAG reaches it (`--yes`
// covers the build prompt only, a non-interactive run is told how to proceed).
// ---------------------------------------------------------------------------

// stubRealignPublishSeams substitutes the terminal gate and the question, and
// returns the prompts the question was asked with, in order. Both are restored
// by t.Cleanup, exactly as realignStubProver restores the prover.
func stubRealignPublishSeams(t *testing.T, interactive, answer bool) *[]string {
	t.Helper()
	origInteractive, origConfirm := realignPublishIsInteractive, confirmRealignPublishFn
	t.Cleanup(func() {
		realignPublishIsInteractive, confirmRealignPublishFn = origInteractive, origConfirm
	})
	prompts := &[]string{}
	realignPublishIsInteractive = func() bool { return interactive }
	confirmRealignPublishFn = func(question string) bool {
		*prompts = append(*prompts, question)
		return answer
	}
	return prompts
}

// stubRealignProverProof replaces the prover with one returning exactly the
// given proof, so a case states in one place what kind of evidence the ladder
// produced. No build runs in a test.
func stubRealignProverProof(t *testing.T, proof realign.Proof) {
	t.Helper()
	orig := realignProve
	t.Cleanup(func() { realignProve = orig })
	realignProve = func(ctx context.Context, p realign.Proposal, opts realign.Options) (realign.Proof, error) {
		return proof, nil
	}
}

// realignProofWithEvidence is a proof one gate actually read: the shape the
// ladder produces once a staged tree can build (story 037's seam), and the only
// shape the publish question may be asked on.
func realignProofWithEvidence() realign.Proof {
	return realign.Proof{
		Gates:  []validate.GateResult{{Gate: "configure", Outcome: validate.OutcomePass}},
		Passed: true,
	}
}

// realignPublishedEbuild reads what the fixture overlay currently publishes for
// the one candidate package.
func realignPublishedEbuild(t *testing.T, fx realignFixture) string {
	t.Helper()
	path := filepath.Join(fx.overlayPath, "media-libs", "gst-plugins-qt6", "gst-plugins-qt6-1.29.2.ebuild")
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading the published ebuild: %v", err)
	}
	return string(body)
}

// TestRealignPublishAsksPerPackageAndPublishesTheProvedBytes is R5.3 and R5.5
// end to end: the question names the atom, and a yes replaces the published
// ebuild with the baseline's exact bytes — the same slice the gates read,
// identity rather than resemblance.
//
// --yes is given, and that is half the assertion: it buys past the BUILD
// prompt and must not swallow the publish question (D8c — no flag reaches it).
//
// _Requirements: R5, R5.3, R5.5_
func TestRealignPublishAsksPerPackageAndPublishesTheProvedBytes(t *testing.T) {
	fx := realignSetup(t, true, true)
	realignFlags(t, true, true)
	realignDepthFlags(t, "configure", true)
	stubRealignProverProof(t, realignProofWithEvidence())
	prompts := stubRealignPublishSeams(t, true, true)

	out, code := realignRun(t, nil)

	if code != 0 {
		t.Fatalf("exit code is %d, want 0 — publishing on approval is the run working (D9)", code)
	}
	if len(*prompts) != 1 {
		t.Fatalf("the publish question was asked %d time(s), want exactly 1 — one question per proved package:\n%s", len(*prompts), out)
	}
	if !strings.Contains((*prompts)[0], "media-libs/gst-plugins-qt6-1.29.2") {
		t.Errorf("the question %q does not name the atom; a maintainer cannot judge a publication that does not say which package it publishes", (*prompts)[0])
	}
	if got := realignPublishedEbuild(t, fx); got != realignBaselineEbuild {
		t.Errorf("the published ebuild does not carry the proved bytes after a yes; R5.5 is identity, not resemblance.\ngot:\n%s", got)
	}
	if !strings.Contains(out, "published") {
		t.Errorf("the run never says the publication happened; a write nobody is told about is a write nobody reviews:\n%s", out)
	}
}

// TestRealignPublishDeclineLeavesThePublishedOverlayByteIdentical is R5.4 at
// the command layer: a declined question writes nothing, and the refusal is
// reported as the system working rather than as a failure.
//
// _Requirements: R5, R5.3, R5.4_
func TestRealignPublishDeclineLeavesThePublishedOverlayByteIdentical(t *testing.T) {
	fx := realignSetup(t, true, true)
	realignFlags(t, true, true)
	realignDepthFlags(t, "configure", true)
	stubRealignProverProof(t, realignProofWithEvidence())
	prompts := stubRealignPublishSeams(t, true, false)

	out, code := realignRun(t, nil)

	if code != 0 {
		t.Fatalf("exit code is %d, want 0 — declining is a decision, not a failure (D9)", code)
	}
	if len(*prompts) != 1 {
		t.Fatalf("the publish question was asked %d time(s), want exactly 1:\n%s", len(*prompts), out)
	}
	if got := realignPublishedEbuild(t, fx); got != realignOursEbuild {
		t.Errorf("the published ebuild changed although the maintainer declined; R5.4 promises byte-identity.\ngot:\n%s", got)
	}
	if !strings.Contains(out, "declined") {
		t.Errorf("the run never says the decline was heard; silence after a no reads as a hang, not a decision:\n%s", out)
	}
}

// TestRealignPublishIsNotOfferedOnAnAllSkippedProof is D8c's evidence rule. An
// all-SKIPPED proof satisfies PromotionDecision — SKIPPED is acceptable there
// by design — but nothing read the tree, and a question the evidence cannot
// support must not be put to the maintainer. On this host that is today's
// COMMON case: staging carries no Manifest, so `ebuild` dies in setup and every
// gate skips (story 033's measurement, re-confirmed 2026-08-16).
//
// _Requirements: R5, R5.3_
func TestRealignPublishIsNotOfferedOnAnAllSkippedProof(t *testing.T) {
	fx := realignSetup(t, true, true)
	realignFlags(t, true, true)
	realignDepthFlags(t, "configure", true)
	stubRealignProverProof(t, realign.Proof{
		Gates: []validate.GateResult{
			{Gate: "patches", Outcome: validate.OutcomeSkipped, Reason: "the run failed in the setup or unpack phase"},
			{Gate: "configure", Outcome: validate.OutcomeSkipped, Reason: "the run failed in the setup or unpack phase"},
		},
		Passed: true,
	})
	prompts := stubRealignPublishSeams(t, true, true)

	out, code := realignRun(t, nil)

	if code != 0 {
		t.Fatalf("exit code is %d, want 0", code)
	}
	if len(*prompts) != 0 {
		t.Fatalf("the publish question was asked %d time(s) on a proof no gate read; every answer to it would publish on no evidence:\n%s", len(*prompts), out)
	}
	if got := realignPublishedEbuild(t, fx); got != realignOursEbuild {
		t.Errorf("the published ebuild changed although nothing was proved about it.\ngot:\n%s", got)
	}
	if !strings.Contains(out, "proof of nothing") {
		t.Errorf("the run does not say WHY publication was not offered; a silent skip is the unverified green this whole ladder exists to remove:\n%s", out)
	}
}

// TestRealignPublishOffersNothingWithoutATerminal: outside an interactive
// terminal nobody can answer, so nothing is asked and nothing is written — and
// the run says how to proceed, because a gate that cannot be passed is a dead
// end.
//
// _Requirements: R5, R5.3, R5.4_
func TestRealignPublishOffersNothingWithoutATerminal(t *testing.T) {
	fx := realignSetup(t, true, true)
	realignFlags(t, true, true)
	realignDepthFlags(t, "configure", true)
	stubRealignProverProof(t, realignProofWithEvidence())
	prompts := stubRealignPublishSeams(t, false, true)

	out, code := realignRun(t, nil)

	if code != 0 {
		t.Fatalf("exit code is %d, want 0", code)
	}
	if len(*prompts) != 0 {
		t.Fatalf("the publish question was asked %d time(s) with no terminal to answer it on:\n%s", len(*prompts), out)
	}
	if got := realignPublishedEbuild(t, fx); got != realignOursEbuild {
		t.Errorf("the published ebuild changed although nobody approved anything.\ngot:\n%s", got)
	}
	if !strings.Contains(out, "terminal") {
		t.Errorf("the run does not say how to get the question asked; the operator has to be told the way through:\n%s", out)
	}
}

// TestRealignPublishReportsRefusalAndErrorDifferently carries realign.go's own
// split to the operator: a refusal is an authority saying no with nothing
// written — the system WORKING — while a write error happened after every
// authority said yes and may have left the package directory needing a human.
// A command that reported the two the same way would teach its operator to skim
// past the one that matters.
//
// _Requirements: R5, R5.4_
func TestRealignPublishReportsRefusalAndErrorDifferently(t *testing.T) {
	cases := []struct {
		name    string
		err     error
		mustSay string
		mustNot string
	}{
		{
			name:    "a refusal is the system working",
			err:     fmt.Errorf("%w: the proof and the approval disagree", realign.ErrNotPromoted),
			mustSay: "refused",
			mustNot: "needs a human",
		},
		{
			name:    "a write error needs a human",
			err:     errors.New("replacing the published ebuild: disk went away"),
			mustSay: "needs a human",
			mustNot: "refused",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			realignSetup(t, true, true)
			realignFlags(t, true, true)
			realignDepthFlags(t, "configure", true)
			stubRealignProverProof(t, realignProofWithEvidence())
			stubRealignPublishSeams(t, true, true)

			origPromote := realignPromote
			t.Cleanup(func() { realignPromote = origPromote })
			realignPromote = func(p realign.Proposal, proof realign.Proof, approved bool, overlayRoot string) error {
				return tc.err
			}

			out, code := realignRun(t, nil)

			if code != 0 {
				t.Fatalf("exit code is %d, want 0 — the report is the outcome, and D9 keeps the exit code for the review", code)
			}
			if !strings.Contains(out, tc.mustSay) {
				t.Errorf("the outcome does not read as %q:\n%s", tc.mustSay, out)
			}
			if strings.Contains(out, tc.mustNot) {
				t.Errorf("the outcome also reads as %q; the two must never be readable as one another:\n%s", tc.mustNot, out)
			}
		})
	}
}

// TestRealignProofCarriesEvidence pins the evidence rule itself: at least one
// gate must have read the tree and said PASS. SKIPPED is not evidence — it is
// the gate saying it measured nothing.
//
// _Requirements: R5, R5.3_
func TestRealignProofCarriesEvidence(t *testing.T) {
	cases := []struct {
		name  string
		proof realign.Proof
		want  bool
	}{
		{name: "no gates at all", proof: realign.Proof{}, want: false},
		{
			name: "every gate skipped",
			proof: realign.Proof{Gates: []validate.GateResult{
				{Gate: "patches", Outcome: validate.OutcomeSkipped, Reason: "setup died"},
			}},
			want: false,
		},
		{
			name: "one gate passed",
			proof: realign.Proof{Gates: []validate.GateResult{
				{Gate: "patches", Outcome: validate.OutcomeSkipped, Reason: "setup died"},
				{Gate: "configure", Outcome: validate.OutcomePass},
			}},
			want: true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := realignProofCarriesEvidence(tc.proof); got != tc.want {
				t.Errorf("realignProofCarriesEvidence = %v, want %v", got, tc.want)
			}
		})
	}
}
