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
//
// Promote is the step after, and it is not a fourth authority: it asks neither
// question either — the maintainer's answer reaches it as a bare bool — and its
// whole job is to refuse to publish unless both of the other two already said
// yes. Enforcing an answer is not making one, which is why holding the proving
// step and the publishing step in a single package collapses nothing.
package realign

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

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
// The three fields the prepared build needs and cannot work out — the Manifest
// source, and the two POLICY fields of validate.BuildRequest — are carried
// through here from the caller's effective configuration. They used to be absent
// on the stated grounds that leaving them out was honest: RequireIsolation would
// stay at story 031's unmoved default and a build without a proven namespace
// would say so in its own PASS reason, LogDir would stay empty and BuildRequest
// documents an empty one as retaining nothing and SAYING it retained nothing.
//
// That stance was wrong, and specifically so. An operator's decision that builds
// must be isolated is read from one config key, and it was being applied by one
// of the two commands that build — a policy that governs whichever command you
// happened to run is not a policy, it is a coin toss with a config file. And the
// run whose log somebody needs is exactly the run that FAILED, so "retains
// nothing" was an honest description of the one gap that costs an operator the
// diagnosis. Neither field is decided here: this package still adds no rule of
// its own, it carries the decision the command layer already resolved (R1.3,
// R1.4).
//
// _Requirements: R1.1, R1.3, R1.4, R5.1, R5.2_
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

	// StagedManifest answers, for the PUBLISHED package directory, the Manifest
	// content the staged tree must carry before a build gate can run in it.
	//
	// It is the seam `overlay validate --depth` already goes through, and this
	// package neither produces the bytes nor knows where they come from: design
	// D1 puts Manifest GENERATION in autoupdate and leaves validate accepting
	// only what a caller supplies. Nil means NOTHING TRAVELS — nothing is staged
	// and nothing is built — which is validate's rule verbatim rather than a
	// second one stated here (R1.1).
	StagedManifest func(pkgDir string) ([]byte, error)

	// RequireIsolation is the operator's policy, carried and never asserted. The
	// refusal it governs lives inside RunBuildGates and fires only when the
	// request carries it; hardcoding a true here would refuse every build on a
	// host that cannot create the namespace, which is the ordinary case (R1.3).
	RequireIsolation bool

	// LogDir is where the build's whole transcript is kept for whoever has to go
	// past the summary. Empty is still accepted — the gate's own reason then
	// says the log was not retained — but a FAILED gate with no log is the one
	// case where the summary is not enough (R1.4).
	LogDir string

	// Deps are the process- and host-level seams the build gates run through,
	// passed to RunBuildGates untouched. Its zero value means "use the real
	// thing" — every field is normalised by validate itself — so a caller with
	// nothing to substitute passes nothing.
	Deps validate.BuildDeps
}

// provedRealignment is story 033's prepared build, held as a package-level
// variable so that a test can answer for it without a build ever running. It is
// the discipline archive.go states for exec.CommandContext, applied one level
// up: production code never reaches past this name.
//
// IT IS THE 033 FUNCTION ITSELF, not an adapter over it, and that is load-bearing
// in two directions. Mechanically, the var's type IS the function's signature, so
// a change to it stops this file compiling instead of quietly changing what a
// realignment is proved by. And by intent: an adapter is where a second copy of
// the ladder starts — the first well-meant "just normalise the request here" is
// how this package would acquire a rule of its own about what gets gated, which
// is precisely the authority design D8 gave to somebody else.
//
// # Why it is ONE name where it used to be two
//
// It was Stage and RunBuildGates, and those two are the LOWER HALF of building a
// candidate. Holding only them meant this package started from none of the upper
// half — no Manifest seam, no Manifest in the staged tree, no host probe, and a
// BuildRequest whose two policy fields stayed at their zero value — so its gates
// reported SKIPPED for a file the run was supposed to write, and a host missing a
// build dependency became a verdict against the ebuild. Assembling that upper
// half here would have been the second copy of the ladder in the most literal
// sense. So the seam moved to the function that holds the WHOLE operation. It is
// still 033's own function, still not an adapter, and the constraint is unchanged
// rather than relaxed: one copy of the ladder, reached by both entry points
// (R1.5).
//
// The build is swapped through this seam rather than driven through
// validate.BuildDeps because driving the real runner means a real
// `ebuild … clean compile`: a network fetch, a writable DISTDIR and portage on
// the host. The real ladder is story 033's own tests to exercise; what this
// package owes is that it CALLS it, with the depth and the policy it was given,
// and adds nothing.
//
// _Requirements: R1, R1.1, R1.5, R5, R5.1, R5.2_
var provedRealignment = validate.RunPreparedBuild

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

	// The published package directory, which is what the Manifest seam is asked
	// about — never the staged copy. The staged tree is the thing that LACKS a
	// Manifest, which is the whole reason there is a seam to ask.
	result := provedRealignment(ctx, validate.PreparedBuildRequest{
		Overlay:          opts.Overlay,
		StagingRoot:      opts.StagingRoot,
		Atom:             atom,
		Version:          p.Version,
		PackageDir:       filepath.Join(opts.Overlay, p.Category, p.Package),
		Ebuild:           p.Ebuild,
		Depth:            opts.Depth,
		StagedManifest:   opts.StagedManifest,
		RequireIsolation: opts.RequireIsolation,
		LogDir:           opts.LogDir,
		Deps:             opts.Deps,
	})

	if err := result.StageErr; err != nil {
		// The ErrStageUnpreparable sentinel survives the wrap, which is the point
		// of wrapping rather than restating: a caller reacts to staging having
		// failed without enumerating the ways it can fail. The staging root is
		// named because it is the thing the operator has to go and fix.
		//
		// The prepared build ALSO rendered this as a list of SKIPPED gates, and
		// they are deliberately dropped here. That rendering is validate.Run's
		// rule — every stopping condition is a reported skip, so the rest of the
		// overlay is still validated — and this package's rule is the opposite
		// one for the opposite reason: a realignment reported as a proof that
		// found nothing is a change abandoned on the strength of an unwritable
		// directory.
		return Proof{}, fmt.Errorf("staging the realignment of %s-%s under %s: %w", atom, p.Version, opts.StagingRoot, err)
	}

	stagedRoot, gates := result.StagedRoot, result.Gates
	if err := result.GatesErr; err != nil {
		// RunBuildGates' error is about the REQUEST — a malformed atom, no
		// version, no staged tree — and never about the build, which reports
		// itself as a FAILED gate. So this is our bug, not the realignment's, and
		// it must not be recorded as a realignment that does not build. The staged
		// root travels on the proof even so: the tree exists, and it is where
		// anyone diagnosing this looks first.
		//
		// An INTERRUPTION arrives here too, and it is not a request that could
		// not be started: the build ran and was killed. Both are alike in the one
		// way that decides this branch — no gate reached a verdict — so neither
		// may be recorded as a realignment that does not build, and the
		// cancellation stays chained for a caller that asks errors.Is about it.
		return Proof{StagedRoot: stagedRoot}, fmt.Errorf("running the build gates for the realignment of %s-%s in %s: %w", atom, p.Version, stagedRoot, err)
	}

	// R5.3's first half — every gate reported PASS or SKIPPED — asked of
	// validate's own rule rather than re-derived here: a second copy of a rule
	// whose entire value is that there is one copy would be the first place the
	// two could disagree about publishing.
	//
	// WHAT THAT RULE DENIES, AND WHAT IT DELIBERATELY DOES NOT (S039-R2.1,
	// R2.6). It denies the vacuity that names the CANDIDATE: a gate list whose
	// every deciding gate declined over the proposal itself is refused, so
	// Passed comes back false and nothing is offered. It does NOT deny the other
	// shape — a list that declined over THIS HOST still promotes (S033-R3.12),
	// because a machine that lacks the build dependencies has said nothing about
	// the realignment, and refusing there would stop the operation on most
	// workstations. So Passed is still NOT sufficient on its own, and the
	// remaining guard is not here: cmd/bentoo's realignProofCarriesEvidence
	// requires at least one gate to have reported PASS before the publish
	// question is put at all.
	//
	// The staging error is nil BY CONSTRUCTION: the only path that reaches this
	// line is one where Stage returned a tree, because the alternative returned
	// above without consulting a gate. That early return denies the STAGING
	// vacuity — a different one from the gate-list vacuity above, and this
	// package's own — exactly as the applier's prepareInStagingTree does.
	//
	// The reason string is deliberately not carried on Proof. Every actionable
	// word of a refusal is already in the failing GateResult's own Reason; the
	// one-line summary belongs to whoever REFUSES TO PUBLISH — the approval step
	// that follows this one — and not to the proof, which reports what happened
	// rather than what should be done about it.
	passed, _ := validate.PromotionDecision(gates, nil)

	return Proof{StagedRoot: stagedRoot, Gates: gates, Passed: passed}, nil
}

// ErrNotPromoted is the sentinel every refusal below wraps. It carries one step
// further the split Prove already draws between a FAILED gate and a staging
// failure, into the step where the consequence is a publish.
//
// A refusal is the system WORKING: an authority said no, nothing was written,
// and the published overlay is byte-identical (R5.4). A write that broke is not
// — it happened after every authority had said yes, and it may have left the
// package directory holding something nobody decided on. A caller that reported
// the two the same way would teach its operator to skim past the one that needs
// a human, so the difference is offered as a sentinel rather than as wording a
// command would have to pattern-match.
//
// _Requirements: R5, R5.3, R5.4_
var ErrNotPromoted = errors.New("the realignment stays unpublished")

// Promote writes a proved realignment into the published overlay, and only when
// the two authorities that are NOT this function have both already said yes:
// every gate reported PASS or SKIPPED, and the maintainer approved (R5.3). What
// it writes is the exact bytes that were proved (R5.5). Every refusal returns
// before the first byte of the overlay is touched, which is how a realignment
// that is not approved or does not pass leaves the published tree byte-identical
// (R5.4).
//
// # approved is a bare bool, and that is design D8 held at the signature
//
// The model PROPOSES, the gates DECIDE whether it still builds, the maintainer
// APPROVES — three authorities, and no two of them collapsed. This function is
// none of the three. It reads the answer each of the other two already gave and
// refuses unless both are yes, so there is no prompt here, no TTY check and no
// flag: asking the question belongs to the command layer, where cobra and a
// terminal are. A function that asked it itself could not be tested for refusing
// to publish without an answer, which is the behaviour that matters most.
//
// The shortcut this exists to block is the reasonable-sounding one — "every gate
// passed, so what is the maintainer adding?" What the maintainer adds is the
// judgement no gate can make: the overlay's nodejs carries a 492-line divergence
// that would pass every rung of the ladder, and reverting it would still be
// wrong. The gates answer "does it build"; only a human answers "should we".
//
// # The gates are re-read rather than taken on trust from Passed
//
// A refusal has to name WHICH authority said no. "Not promoted" is one outcome
// for several different reasons, and an operator who cannot tell them apart does
// not know whether to fix the ebuild, re-run the gates or say yes. Proof.Passed
// is a single bit and can name nothing, so the refusal is derived from
// proof.Gates through validate.PromotionDecision — whose reason already names
// the failing gate, in the words that gate used. Passed is then checked as well
// and a disagreement refuses: they are two records of one verdict, and when they
// differ nothing here can tell which of them is the stale one.
//
// # What gets published, and why it is not read back out of the staged tree
//
// The proposal's own bytes. They are the same slice Prove handed to the prepared
// build, which passes them to validate.Stage and writes them into the staged tree
// unchanged, so they ARE the bytes the gates ran against: R5.5 is satisfied by
// identity rather than by resemblance.
// Anything re-derived at this point — the ebuild re-rendered, the diff applied
// again, the proposal rebuilt from the baseline — would publish a file no gate
// ever read, however faithful the derivation.
//
// internal/autoupdate's own promote does the opposite and reads the file out of
// the staged tree, which is not an inconsistency but the same rule under
// different facts: there a fixer may rewrite the staged ebuild between staging
// and promotion, so the staged file is the only record of what was proved.
// Nothing writes into a realignment's staged tree after Stage. And a
// maintainer's answer can arrive hours later, over a staging root that a cleanup
// or a reboot has taken away in the meantime — requiring the staged tree to
// still exist would let an approval expire for a reason that has nothing to do
// with the realignment.
//
// _Requirements: R5, R5.3, R5.4, R5.5_
func Promote(p Proposal, proof Proof, approved bool, overlayRoot string) error {
	// The gates are consulted before the approval, and the order is not
	// arbitrary: when both would refuse, the gate's finding is the one worth
	// reporting. It is a fact about the artefact that has to be fixed either
	// way, while approval is a judgement nobody should be asked for about a
	// realignment that does not build.
	if err := refuseUnprovedRealignment(p, proof); err != nil {
		return err
	}
	if !approved {
		return fmt.Errorf("%w: %s-%s has not been approved by the maintainer; gates reporting PASS or SKIPPED are permission to ASK, never the answer (R5.3)",
			ErrNotPromoted, p.atom(), p.Version)
	}

	return publishProvedEbuild(p, overlayRoot)
}

// refuseUnprovedRealignment answers R5.3's first half — every gate reported PASS
// or SKIPPED — and returns the refusal, named, when it did not.
//
// The empty list is refused first and separately. validate.PromotionDecision
// answers TRUE over no gates at all, correctly: it judges a list, and the
// question of whether the list was ever assembled is documented there as
// belonging to whoever ran the gates. For a realignment that question lands
// here, because RunBuildGates returns "an empty list, not a hollow pass" for
// every depth below DepthPatches — so a realignment proved at DepthNone or
// DepthOptions was examined by nothing, and "every gate reported PASS or
// SKIPPED" would be satisfied vacuously by a proof of nothing. That is the same
// vacuity PromotionDecision was written to deny for a staging failure, arriving
// through a door story 033 did not have; and it is D8 again, because approval
// would then be the ONLY authority that ever spoke. Prove's own note leaves the
// CHOICE of a worthwhile depth to the command that registers --depth, which is
// where it belongs — this is the different question of what may be PUBLISHED on
// no evidence, asked in the one function whose job is to refuse.
//
// PromotionDecision skips the QA gate, so a list holding nothing else would be
// as empty as none for its purposes and is not caught by the count above. That
// case cannot arise from this package: RunBuildGates reports build gates only,
// and re-deriving the exclusion here would be the second copy of a rule whose
// whole value is that there is one.
//
// The proposal is passed whole rather than pre-split into its name, so that no
// caller can hand this function identifiers belonging to a different
// realignment from the one it is judging.
//
// _Requirements: R5, R5.3_
func refuseUnprovedRealignment(p Proposal, proof Proof) error {
	atom, version := p.atom(), p.Version

	if len(proof.Gates) == 0 {
		return fmt.Errorf("%w: no gate ever read %s-%s, so there is nothing to publish it on; an empty gate list satisfies \"every gate reported PASS or SKIPPED\" only vacuously (R5.3) — prove the realignment at a depth that builds",
			ErrNotPromoted, atom, version)
	}

	// The staging error is nil BY CONSTRUCTION here: a Proof exists, so a tree
	// was staged. Prove refuses the other case before a gate is ever consulted.
	if mayPromote, reason := validate.PromotionDecision(proof.Gates, nil); !mayPromote {
		// The reason travels in validate's own words rather than being
		// re-worded: it names the gate that failed, and a second spelling of the
		// same verdict is the first place the two could drift apart.
		return fmt.Errorf("%w: %s-%s: %s", ErrNotPromoted, atom, version, reason)
	}

	if !proof.Passed {
		return fmt.Errorf("%w: the proof of %s-%s records that it did not pass, although all %d of its gates read as PASS or SKIPPED; the two records of one verdict disagree and nothing here can tell which is stale, so the safe reading is the refusal",
			ErrNotPromoted, atom, version, len(proof.Gates))
	}
	return nil
}

// publishProvedEbuild replaces the published ebuild with the bytes that were
// proved, and refuses — still without writing anything — anything about the
// destination that would make the write mean something other than "this version
// is now built differently".
//
// # The destination has to exist already, and that is the point of a realignment
//
// A realignment is not a bump: it rewrites how an ALREADY PUBLISHED version is
// built, and the archive it points at has not moved. So the file it replaces is
// expected to be there, and its absence means the overlay moved on since the
// comparison that produced this proposal — the version was dropped, or revised
// to a -r1 the proposal does not name. Writing anyway would ADD an ebuild the
// overlay never had, which is a bump-shaped act nobody approved, and it would
// land unclaimed by any registry pin: `overlay autoupdate --clean` deletes
// exactly that. It is the mirror image of the guard internal/autoupdate takes at
// the same moment for the opposite reason — there the destination must NOT
// exist — and, as there, it is taken now rather than earlier because the gates
// in between take minutes and a check that old describes a package directory
// that may have moved on.
//
// _Requirements: R5, R5.4, R5.5_
func publishProvedEbuild(p Proposal, overlayRoot string) error {
	atom := p.atom()

	if len(p.Ebuild) == 0 {
		return fmt.Errorf("%w: the proposal for %s-%s carries no ebuild body, and publishing it would empty a published ebuild rather than realign it",
			ErrNotPromoted, atom, p.Version)
	}

	dst, err := publishedEbuildPath(overlayRoot, p)
	if err != nil {
		return err
	}

	// One Lstat answers all three questions this step has about the
	// destination: that it is there, that it is a file and not a symlink or a
	// directory, and what mode the promotion must leave behind.
	info, err := os.Lstat(dst)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return fmt.Errorf("%w: %s holds no ebuild for %s-%s to realign; a realignment rewrites a version that is already published, so publishing here would add one nobody approved",
			ErrNotPromoted, filepath.Dir(dst), atom, p.Version)
	case err != nil:
		return fmt.Errorf("reading the published ebuild %s of %s-%s before replacing it: %w", dst, atom, p.Version, err)
	case !info.Mode().IsRegular():
		return fmt.Errorf("%w: the published %s of %s-%s is %v and not a regular file, so what a promotion would replace cannot be established",
			ErrNotPromoted, dst, atom, p.Version, info.Mode())
	}

	// The mode is the one the overlay already had, not a mode chosen here. A
	// realignment changes how a version is BUILT; changing who may read the file
	// as a side effect of that would be a second, unapproved change riding along
	// — and the overlay is a git repository Portage reads as an unprivileged
	// user, where 0600 arrived at by accident is a package nobody can install.
	if err := replacePublishedEbuild(dst, p.Ebuild, info.Mode().Perm()); err != nil {
		return fmt.Errorf("publishing the proved bytes of %s-%s as %s: %w", atom, p.Version, dst, err)
	}
	return nil
}

// publishedEbuildPath names <overlay>/<category>/<package>/<package>-<version>.ebuild,
// refusing any component that would make that join mean something else.
//
// Proposal.atom deliberately validates nothing, because Stage's own
// splitStagedAtom guards the tree Stage creates and a copy of that check would
// be a second rule free to disagree with it. This is not that copy: nothing in
// validate ever joins a path inside the PUBLISHED overlay, so this is the only
// guard on a different path — the one that leads to a directory which commits
// and pushes itself. The version is checked too, and not only the two halves of
// the atom, because it is part of a filename here.
//
// _Requirements: R5, R5.4, R5.5_
func publishedEbuildPath(overlayRoot string, p Proposal) (string, error) {
	if strings.TrimSpace(overlayRoot) == "" {
		return "", fmt.Errorf("%w: no published overlay was named to promote %s into", ErrNotPromoted, p.atom())
	}
	for _, part := range []struct{ kind, value string }{
		{"category", p.Category},
		{"package", p.Package},
		{"version", p.Version},
	} {
		if err := refusePathElement(part.kind, part.value); err != nil {
			return "", fmt.Errorf("%w: %w", ErrNotPromoted, err)
		}
	}
	return filepath.Join(overlayRoot, p.Category, p.Package, p.Package+"-"+p.Version+".ebuild"), nil
}

// refusePathElement refuses the values that would make a joined path mean
// something other than "one name": empty, the two relative directory names, a
// separator of either flavour, and a NUL byte.
//
// _Requirements: R5.4_
func refusePathElement(kind, value string) error {
	switch {
	case value == "":
		return fmt.Errorf("the %s is empty", kind)
	case value == "." || value == "..":
		return fmt.Errorf("the %s is %q, which names a directory other than itself", kind, value)
	case strings.ContainsAny(value, `/\`) || strings.ContainsRune(value, 0):
		return fmt.Errorf("the %s %q contains a path separator, so it cannot be one name inside the overlay", kind, value)
	}
	return nil
}

// replacePublishedEbuild writes body at path through a temporary file in the
// same directory and renames it into place.
//
// # Which of two risks this takes, in an overlay that publishes itself
//
// The bentoo overlay auto-commits and pushes within minutes, so both candidate
// mistakes here are published mistakes and the choice is which one to run.
//
// Writing in place is the one refused. The destination ALREADY EXISTS — that is
// what a realignment is — so an in-place write truncates a published ebuild and
// then refills it, and anything that interrupts it (a full disk, a signal, the
// machine) leaves a file that is neither the old bytes nor the proved ones,
// under the real filename, where nothing marks it as debris. That is both of
// this sub-task's requirements broken at once: not byte-identical (R5.4) and not
// the bytes that were proved (R5.5). The timer would then commit the mixture.
//
// The rename takes the other risk knowingly: for the moment between creating the
// temporary file and renaming it, a file the overlay never had exists inside it,
// and a commit landing in that window would publish it. The window is one write
// and one chmod wide, every exit but the successful rename takes the file away
// again, and a removal that itself fails is JOINED to the returned error rather
// than swallowed — this package logs nothing, so naming the file the operator
// must delete is the only way it can be said. The temporary name is dotted, so
// Portage ignores it even if it is briefly seen. In exchange, a reader of the
// destination sees either the old bytes or the proved ones and never a mixture,
// which is exactly what R5.4 and R5.5 ask for.
//
// _Requirements: R5, R5.4, R5.5_
func replacePublishedEbuild(path string, body []byte, mode fs.FileMode) (err error) {
	dir := filepath.Dir(path)
	tmp, createErr := os.CreateTemp(dir, "."+filepath.Base(path)+".bentoo-realign-*")
	if createErr != nil {
		return fmt.Errorf("creating a temporary file beside %s: %w", path, createErr)
	}
	tmpName := tmp.Name()

	committed := false
	defer func() {
		// A no-op after the explicit Close below; here for the paths that did
		// not reach it.
		_ = tmp.Close()
		if committed {
			return
		}
		if rmErr := os.Remove(tmpName); rmErr != nil && !errors.Is(rmErr, fs.ErrNotExist) {
			err = errors.Join(err, fmt.Errorf("removing the temporary file %s left inside the overlay beside %s: %w", tmpName, path, rmErr))
		}
	}()

	if _, writeErr := tmp.Write(body); writeErr != nil {
		return fmt.Errorf("writing the proved bytes to %s: %w", tmpName, writeErr)
	}
	// Flushed before the rename rather than after it: a rename that published a
	// name whose contents were still only in the page cache would survive a
	// crash as an empty ebuild, which is the very mixture the rename is here to
	// prevent.
	if syncErr := tmp.Sync(); syncErr != nil {
		return fmt.Errorf("syncing %s before publishing it as %s: %w", tmpName, path, syncErr)
	}
	if closeErr := tmp.Close(); closeErr != nil {
		return fmt.Errorf("closing %s before publishing it as %s: %w", tmpName, path, closeErr)
	}
	// os.CreateTemp creates at 0600 and os.Rename keeps the mode it finds, so
	// the mode has to be set here — after the last write and before the rename,
	// so the file is never reachable under its published name with the wrong one.
	//nolint:gosec // G302: mode is the mode the published ebuild being replaced already carried; see publishProvedEbuild.
	if chmodErr := os.Chmod(tmpName, mode); chmodErr != nil {
		return fmt.Errorf("setting mode %04o on %s before publishing it as %s: %w", mode.Perm(), tmpName, path, chmodErr)
	}
	if renameErr := os.Rename(tmpName, path); renameErr != nil {
		return fmt.Errorf("renaming %s into place as %s: %w", tmpName, path, renameErr)
	}
	committed = true
	return nil
}
