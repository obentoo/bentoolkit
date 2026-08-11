// Package realign proves a proposed realignment of an overlay ebuild towards its
// ::gentoo baseline, and proving is the whole of what it does: story 033's
// staged-bump ladder decides whether the realignment builds, and this package is
// only the composition that hands the proposal to it.
//
// # Why there is a package here at all
//
// The two obvious homes were both refused, and what refuses them is an import
// edge this codebase keeps on purpose. internal/overlay — where the compare that
// produces a realignment proposal lives — imports NOTHING from
// internal/autoupdate, and prune.go's RegistryKeys comment states the rule
// outright: the registry keys are "SUPPLIED by the caller and never looked up
// here, which is what keeps this package free of an import edge to
// internal/autoupdate". Putting the proving step in internal/overlay would have
// overturned that boundary silently, and left that comment false for whoever read
// it next. It would in fact not have compiled at all: validate imports
// internal/overlay itself (run.go), so the edge back is a cycle — which is worth
// knowing, because it means the boundary is not merely a convention somebody
// could relax, and the error the compiler would give names neither of the reasons
// above. cmd/bentoo is this codebase's other answer to "who joins these two"
// (buildDivergenceMap is there for the same reason), and it works — at the price
// of putting orchestration in the one layer that is hardest to test.
//
// So the composition gets a package of its own (design D8b). It imports
// internal/autoupdate/validate for the ladder, and — from the later tasks of this
// group — internal/overlay for the proposal. Neither of those two gains an import
// of the other, and nothing here re-states a rule that either of them owns.
//
// # Three authorities, and this package is the caller of exactly one of them
//
// Design D8 splits a realignment into three steps that are never collapsed: the
// model may say a divergence is no longer justified, the GATES say whether the
// realignment still builds (R5.2), and only the maintainer's approval publishes
// it (R5.3). Prove is the middle step and nothing else. It adds no gate of its
// own — in particular no "it matches ::gentoo now" check, which would be four
// cheap lines here and would look like a gate, and which is exactly how the party
// that PROPOSES a change would acquire the authority to DECIDE it.
package realign

import (
	"context"
	"fmt"

	"github.com/obentoo/bentoolkit/internal/autoupdate/validate"
)

// Proposal is one realigned ebuild: the package it belongs to, the version it is
// a realignment of, and the body to be proved.
//
// The version is the version ALREADY PUBLISHED. A realignment is not a bump — it
// rewrites how the same version is built, which is why nothing here carries an
// "old" and a "new" version and why the archive the ebuild points at has not
// moved.
//
// Ebuild is proved verbatim (R5.5 begins here). Nothing in this package
// reformats, regenerates or normalises it, for the same reason StageRequest's own
// EbuildBytes are written unchanged: a gate result has to describe a file that
// exists somewhere, and the file it must describe is the one an approval would
// publish. The moment proving and publishing could disagree about a single byte,
// every gate outcome becomes a statement about a file nobody will ever install.
//
// _Requirements: R5, R5.1_
type Proposal struct {
	Category, Package, Version string
	Ebuild                     []byte
}

// atom is the full category/package the ladder names this proposal by, e.g.
// "media-libs/gst-plugins-qt6".
//
// It is one expression called twice rather than the same join written twice,
// because its two readers have to agree about a path: Stage writes the candidate
// to <staged>/<category>/<package>/<package>-<version>.ebuild, and RunBuildGates
// SPLITS the atom again to name the very file it invokes `ebuild` on. Two
// spellings that drifted would not fail loudly — they would gate a different file
// from the one that was staged, which is the one error a proof cannot detect
// about itself.
//
// Nothing is validated here on purpose. splitStagedAtom, inside Stage, already
// refuses an empty category, a ".." and an embedded separator, and it refuses
// them before anything is created; a copy of that check here would be a second
// rule free to disagree with the one that actually guards the filesystem.
//
// _Requirements: R5.1_
func (p Proposal) atom() string { return p.Category + "/" + p.Package }

// Proof is what proving produced: where it was proved, what the gates said, and
// whether that is enough to be ALLOWED to publish — never that it was published.
//
// _Requirements: R5, R5.1, R5.2_
type Proof struct {
	// StagedRoot is the staged repository the gates read, outside the published
	// overlay (R5.1). It is named on the proof rather than discarded because it is
	// the evidence: a maintainer asked to approve a realignment can read the exact
	// tree that was judged, and a failure nobody can inspect is a failure nobody
	// can act on.
	StagedRoot string

	// Gates are story 033's results, in the order and the number the ladder
	// returned them. Not filtered, not re-worded, not appended to: this story adds
	// no gate, so a Proof that carried a different list from the one the ladder
	// produced would be reporting an authority that does not exist.
	Gates []validate.GateResult

	// Passed is validate.PromotionDecision's answer over those gates — "every gate
	// reported PASS or SKIPPED" — and it is only ONE of the two conditions R5.3
	// requires. The other is the maintainer's approval, which this package does
	// not ask for and this field must never be read as. Passing gates are a
	// permission to ask; they are not the answer.
	Passed bool
}

// Options is what Prove cannot work out for itself: which overlay the staged tree
// is built from, where the staged tree goes, how deep to gate, and the seams the
// build runs through.
//
// Two fields of validate.BuildRequest are deliberately absent rather than
// hard-coded with a note. RequireIsolation stays at story 031's unmoved default
// (design D11 there): a build that runs without a proven network namespace says
// so in its own PASS reason, so the fidelity is reported rather than assumed.
// LogDir stays empty, which BuildRequest documents as retaining nothing and
// SAYING that it retained nothing — an honest gap, and the command layer of this
// group is where an operator-visible log directory would come from if one is
// wanted.
//
// _Requirements: R5.1, R5.2_
type Options struct {
	// Overlay is the published overlay the staged tree copies its eclasses and
	// profiles out of (R3.8 in story 033). It is READ here and never written:
	// every write this package causes lands under StagingRoot.
	Overlay, StagingRoot string

	// Depth is how far up the ladder the operator asked to go, passed through
	// unchanged (R5.2). Prove never substitutes a depth of its own — a
	// realignment proved shallower than requested is a proof of something nobody
	// asked about, and one proved deeper spends machine time nobody agreed to.
	//
	// DepthNone is the zero value, and it covers no build gate at all: the ladder
	// then returns an empty list, nothing has FAILED, and this package reports a
	// proof of nothing as Passed. That vacuity is not resolved here, because the
	// depth is the operator's to choose (R5.2) and story 033 already answers a
	// depth nobody chose where it is chosen — ParseDepth refuses the empty string
	// rather than answering DepthNone, "so a mistyped config key cannot switch
	// validation off in silence". The command that registers --depth is therefore
	// the place that owes a realign run a depth worth having.
	Depth validate.Depth

	// Deps are the process- and host-level seams the build gates run through,
	// passed to RunBuildGates untouched. Its zero value means "use the real
	// thing" — every field is normalised by validate itself — so a caller with
	// nothing to substitute passes nothing.
	Deps validate.BuildDeps
}

// stageRealignment and runRealignGates are story 033's two entry points, held as
// package-level variables so that a test can answer for them without a build ever
// running. It is the discipline archive.go states for exec.CommandContext,
// applied one level up: production code never reaches past these names.
//
// THEY ARE THE 033 FUNCTIONS THEMSELVES, not adapters over them, and that is
// load-bearing in two directions. Mechanically, the var's type IS the function's
// signature, so a change to either signature stops this file compiling instead of
// quietly changing what a realignment is proved by. And by intent: an adapter is
// where a second copy of the ladder starts — the first well-meant "just normalise
// the request here" is how this package would acquire a rule of its own about
// what gets gated, which is precisely the authority design D8 gave to somebody
// else.
//
// The gates are swapped through this seam rather than driven through
// validate.BuildDeps because driving the real runner means a real
// `ebuild … clean compile`: a network fetch, a writable DISTDIR and portage on
// the host. The real ladder is story 033's own tests to exercise; what this
// package owes is that it CALLS it, with the depth it was given, and adds nothing.
//
// _Requirements: R5, R5.1, R5.2_
var (
	stageRealignment = validate.Stage
	runRealignGates  = validate.RunBuildGates
)

// Prove materialises a realigned ebuild in a staged tree outside the published
// overlay (R5.1) and runs story 033's build gates against it, up to the depth the
// caller asked for (R5.2). It returns what the gates said and whether that is
// enough to be allowed to publish; it publishes nothing.
//
// # Outside the overlay is a security property, not tidiness
//
// The bentoo overlay auto-commits and pushes within minutes. Anything written
// inside it is therefore PUBLISHED before any gate has spoken — a realignment
// staged in place would be released by the clock rather than by a decision. The
// rule is not re-checked here: Stage enforces it itself, refusing a staging root
// that resolves inside the overlay (its ensureOutsideOverlay), and it holds a
// second reason for the same refusal that this package would not have thought of
// — `overlay autoupdate --clean` deletes any ebuild under the overlay root that
// no registry pin claims, so a staged tree parked there is eaten by the very tool
// that made it. One enforcement, in the function that creates the path.
//
// # A gate that says no is an ANSWER; a staging step that could not run is not
//
// A FAILED gate comes back as (Proof, nil) with Passed false and the gate's own
// reason intact: the ladder examined the realignment and reported on it, which is
// the run working. A staging failure comes back as an error, because nothing was
// ever examined, and reporting it as Passed false would let "we could not look"
// read as "it does not build" — a realignment would then be abandoned on the
// strength of an unwritable directory. The same split is why the gates are not
// consulted after a staging failure: there is nothing to gate, and a list of
// SKIPPED gates is precisely the vacuous "nothing FAILED" that
// validate.PromotionDecision exists to deny.
//
// # Which gates, and what that leaves uncovered
//
// The BUILD gates. validate.Run's options-vs-archive comparison is not run here
// because it needs the upstream archive already on disk, and a realignment is a
// same-version edit — there is no bump that would have fetched one. What that
// costs is stated rather than hidden: a realignment that changes the option list
// passed to the build system, which is exactly the gst-plugins-qt6 case this
// story is about, has that change judged by the configure and compile rungs
// instead. A depth below those judges the change not at all.
//
// _Requirements: R5, R5.1, R5.2_
func Prove(ctx context.Context, p Proposal, opts Options) (Proof, error) {
	atom := p.atom()

	stagedRoot, err := stageRealignment(validate.StageRequest{
		Overlay:     opts.Overlay,
		StagingRoot: opts.StagingRoot,
		Atom:        atom,
		Version:     p.Version,
		EbuildBytes: p.Ebuild,
	})
	if err != nil {
		// The ErrStageUnpreparable sentinel survives the wrap, which is the point
		// of wrapping rather than restating: a caller reacts to staging having
		// failed without enumerating the ways it can fail. The staging root is
		// named because it is the thing the operator has to go and fix.
		return Proof{}, fmt.Errorf("staging the realignment of %s-%s under %s: %w", atom, p.Version, opts.StagingRoot, err)
	}

	gates, err := runRealignGates(ctx, validate.BuildRequest{
		StagedRoot: stagedRoot,
		Atom:       atom,
		Version:    p.Version,
		Depth:      opts.Depth,
	}, opts.Deps)
	if err != nil {
		// RunBuildGates' error is about the REQUEST — a malformed atom, no
		// version, no staged tree — and never about the build, which reports
		// itself as a FAILED gate. So this is our bug, not the realignment's, and
		// it must not be recorded as a realignment that does not build. The staged
		// root travels on the proof even so: the tree exists, and it is where
		// anyone diagnosing this looks first.
		return Proof{StagedRoot: stagedRoot}, fmt.Errorf("running the build gates for the realignment of %s-%s in %s: %w", atom, p.Version, stagedRoot, err)
	}

	// R5.3's first half — every gate reported PASS or SKIPPED — asked of
	// validate's own rule rather than re-derived here. PromotionDecision encodes
	// it together with the vacuity it has to deny, and a second copy of a rule
	// whose entire value is that there is one copy would be the first place the
	// two could disagree about publishing.
	//
	// The staging error is nil BY CONSTRUCTION: the only path that reaches this
	// line is one where Stage returned a tree, because the alternative returned
	// above without consulting a gate. That early return is where this package
	// denies the vacuity, exactly as the applier's prepareInStagingTree does.
	//
	// The reason string is deliberately not carried on Proof. Every actionable
	// word of a refusal is already in the failing GateResult's own Reason; the
	// one-line summary belongs to whoever REFUSES TO PUBLISH — the approval step
	// that follows this one — and not to the proof, which reports what happened
	// rather than what should be done about it.
	passed, _ := validate.PromotionDecision(gates, nil)

	return Proof{StagedRoot: stagedRoot, Gates: gates, Passed: passed}, nil
}
