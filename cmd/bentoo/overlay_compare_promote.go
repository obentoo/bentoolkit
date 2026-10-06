package main

import (
	"errors"
	"fmt"
	"os"

	"github.com/obentoo/bentoolkit/internal/autoupdate/validate"
	"github.com/obentoo/bentoolkit/internal/common/output"
	"github.com/obentoo/bentoolkit/internal/realign"
)

// This file is the publish half of `--depth`: the per-package question and the
// one call that writes into the published overlay. It is separate from
// overlay_compare_depth.go because "stages under <configDir>/staging" and
// "replaces a published ebuild" are different promises about what may be written.
//
// PER PACKAGE: a publication is a judgement about one artefact (every gate can
// pass and reverting can still be wrong), so there is ONE question per proved
// package, naming its atom.
//
// ON EVIDENCE ONLY: an all-SKIPPED proof satisfies validate.PromotionDecision by
// design, but nothing read the tree — as when `ebuild` dies in setup on a staged
// tree without a Manifest — and a question the evidence cannot support is never
// put to the maintainer.
//
// NO FLAG REACHES IT: `--yes` covers the build prompt only; a non-interactive run
// is told to re-run in a terminal. realign.Promote takes `approved bool` so the
// asking stays here, and the only path to `true` is a human answering.

// The publisher, the per-package y/N question and the terminal probe that
// gates it are the realignPromote, confirmRealignPublish and
// realignPublishIsInteractive fields of deps (deps.go). The question defaults
// to confirmAction, which reads os.Stdin and answers "no" on any read error, so
// an EOF is a decline rather than an accident.

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

// offerRealignPublish puts the maintainer — the third authority, after the model
// and the gates — its question for one package, and on a yes calls
// realign.Promote with the same proposal and proof the ladder produced, so the
// published bytes are exactly the proved ones: the very values are handed over
// rather than anything re-derived.
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
func offerRealignPublish(c realignCandidate, proof realign.Proof, overlayRoot string, d *deps) {
	if !realignProofCarriesEvidence(proof) {
		output.Warning.Printf("    not offered for publication: every gate was SKIPPED, so nothing was proved about it — a proof of nothing is not evidence to publish on.\n")
		return
	}
	if !d.realignPublishIsInteractive() {
		output.Info.Printf("    publishable — re-run in an interactive terminal to be asked; nothing is published without the maintainer's yes.\n")
		return
	}
	if !d.confirmRealignPublish(fmt.Sprintf("Publish %s — replace the published ebuild with the proved ::gentoo bytes?", c.name)) {
		output.Warning.Printf("    declined: nothing was written, and the published overlay is byte-identical.\n")
		return
	}

	if err := d.realignPromote(c.proposal, proof, true, overlayRoot); err != nil {
		if errors.Is(err, realign.ErrNotPromoted) {
			output.Warning.Printf("    refused: %v\n", err)
			return
		}
		output.Error.Fprintf(os.Stderr, "    the write failed after every authority said yes — %v — the package directory needs a human before the overlay publishes itself.\n", err)
		return
	}
	output.Success.Printf("    published: the overlay now carries the exact bytes the gates read; it commits and pushes on its own.\n")
}
