package main

import (
	"errors"
	"fmt"

	"github.com/obentoo/bentoolkit/internal/autoupdate/validate"
	"github.com/obentoo/bentoolkit/internal/common/output"
	"github.com/obentoo/bentoolkit/internal/realign"
)

// This file is the publish half of `--depth`: the per-package question and the
// one call that writes into the published overlay (design D8c). It is separate
// from overlay_compare_depth.go for the same reason that file is separate from
// overlay_compare_realign.go — each file's first paragraph states what it may
// write, and "stages under <configDir>/staging" and "replaces a published
// ebuild" are different promises.
//
// # Three properties, none negotiable
//
// PER PACKAGE: the build confirmation is one decision about machine time; a
// publication is a judgement about one artefact (Promote's own nodejs example:
// every gate can pass and reverting can still be wrong). ONE question per
// proved package, naming its atom.
//
// ON EVIDENCE ONLY: an all-SKIPPED proof satisfies validate.PromotionDecision —
// SKIPPED is acceptable there by design — but nothing read the tree, and a
// question the evidence cannot support is never put to the maintainer. On this
// host that is today's common case: staging carries no Manifest, so `ebuild`
// dies in setup and every gate skips (story 033's 2026-08-10 measurement,
// re-confirmed 2026-08-16; story 037 owns the seam that makes these gates run).
//
// NO FLAG REACHES IT: `--yes` covers the build prompt and nothing else; a
// non-interactive run is told to re-run in a terminal. realign.Promote takes
// `approved bool` precisely so the asking stays here, and the only path to
// `true` is a human answering the question.
//
// _Requirements: R5, R5.3, R5.4, R5.5, R7_

// realignPromote is the publisher, held as a package-level variable on the
// discipline realignProve documents: the var's type is realign.Promote's
// signature, so a change to the contract stops this file compiling instead of
// quietly changing what a publication is. Tests that assert how an outcome is
// REPORTED substitute it; tests that assert what a publication WRITES drive the
// real one against a fixture overlay.
var realignPromote = realign.Promote

// confirmRealignPublishFn is the per-package y/N seam, defaulting to the same
// confirmAction the build prompt uses. It reads os.Stdin and answers "no" on
// any read error, so an EOF is a decline rather than an accident.
var confirmRealignPublishFn = confirmAction

// realignPublishIsInteractive gates the question on a real terminal, through
// the same registryPromptIsInteractive the build prompt uses — BOTH streams
// must be a TTY, so a piped `yes` cannot answer for a human. It is a var so the
// approve path is reachable under `go test`, where neither stream is one.
var realignPublishIsInteractive = registryPromptIsInteractive

// realignProofCarriesEvidence reports whether at least one gate read the
// staged tree and said PASS. SKIPPED is not evidence — it is the gate saying it
// measured nothing — and Passed alone cannot distinguish the two, because
// PromotionDecision accepts SKIPPED by design.
func realignProofCarriesEvidence(proof realign.Proof) bool {
	for _, gate := range proof.Gates {
		if gate.Outcome == validate.OutcomePass {
			return true
		}
	}
	return false
}

// offerRealignPublish puts D8's third authority its question for one package,
// and on a yes calls realign.Promote with the same proposal and proof the
// ladder produced — the identity R5.5 asks for, held by handing over the very
// values rather than anything re-derived.
//
// The caller has already established that the proof PASSED and that its gate
// list is non-empty; what is decided here is whether the evidence supports a
// question, whether anyone is present to answer it, and what the answer is.
//
// Refusals and errors are reported apart, carrying realign.go's own split to
// the operator: a refusal (ErrNotPromoted) is an authority saying no with
// nothing written — the system working — while any other error happened after
// every authority said yes and may have left the package directory needing a
// human before the overlay publishes itself.
func offerRealignPublish(c realignCandidate, proof realign.Proof, overlayRoot string) {
	if !realignProofCarriesEvidence(proof) {
		output.Warning.Printf("    not offered for publication: every gate was SKIPPED, so nothing was proved about it — a proof of nothing is not evidence to publish on.\n")
		return
	}
	if !realignPublishIsInteractive() {
		output.Info.Printf("    publishable — re-run in an interactive terminal to be asked; nothing is published without the maintainer's yes.\n")
		return
	}
	if !confirmRealignPublishFn(fmt.Sprintf("Publish %s — replace the published ebuild with the proved ::gentoo bytes?", c.name)) {
		output.Warning.Printf("    declined: nothing was written, and the published overlay is byte-identical.\n")
		return
	}

	if err := realignPromote(c.proposal, proof, true, overlayRoot); err != nil {
		if errors.Is(err, realign.ErrNotPromoted) {
			output.Warning.Printf("    refused: %v\n", err)
			return
		}
		output.Error.Printf("    the write failed after every authority said yes — %v — the package directory needs a human before the overlay publishes itself.\n", err)
		return
	}
	output.Success.Printf("    published: the overlay now carries the exact bytes the gates read; it commits and pushes on its own.\n")
}
