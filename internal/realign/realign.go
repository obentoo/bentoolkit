// Package realign proves a proposed realignment of an overlay ebuild towards its
// ::gentoo baseline, and proving is the whole of what it does: the staged-bump
// ladder in internal/autoupdate/validate decides whether the realignment
// builds, and this package is only the composition that hands the proposal to
// it.
//
// # Why there is a package here at all
//
// internal/overlay, where the compare that produces a proposal lives, must not
// import internal/autoupdate (prune.go's RegistryKeys comment states the rule),
// so the proving step cannot live there. The validate -> internal/overlay
// import cycle that once also blocked it no longer exists, since the overlay
// scanner moved to internal/gentoo/repo; the import rule is the reason that
// still holds. cmd/bentoo could join the two, as it
// does for buildDivergenceMap, but at the price of putting orchestration in the
// layer that is hardest to test. So the composition gets a package of its own:
// it imports internal/autoupdate/validate for the ladder and internal/overlay
// for the proposal, and neither of those gains an import of the other.
//
// # Three authorities, and this package is the caller of exactly one of them
//
// A realignment has three steps that are never collapsed: the model may say a
// divergence is no longer justified, the GATES say whether the realignment
// still builds, and only the maintainer's approval publishes it. Prove is the
// middle step and nothing else. It adds no gate of its own — in particular no
// "it matches ::gentoo now" check, which is exactly how the party that
// PROPOSES a change would acquire the authority to DECIDE it.
//
// Promote is the step after, and it is not a fourth authority: the
// maintainer's answer reaches it as a bare bool, and its whole job is to
// refuse to publish unless both of the other two already said yes. Enforcing
// an answer is not making one.
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
// Ebuild is proved verbatim. Nothing in this package
// reformats, regenerates or normalises it, for the same reason StageRequest's own
// EbuildBytes are written unchanged: a gate result has to describe a file that
// exists somewhere, and the file it must describe is the one an approval would
// publish. The moment proving and publishing could disagree about a single byte,
// every gate outcome becomes a statement about a file nobody will ever install.
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
func (p Proposal) atom() string { return p.Category + "/" + p.Package }

// Proof is what proving produced: where it was proved, what the gates said, and
// whether that is enough to be ALLOWED to publish — never that it was published.
type Proof struct {
	// StagedRoot is the staged repository the gates read, outside the published
	// overlay. It is named on the proof rather than discarded because it is
	// the evidence: a maintainer asked to approve a realignment can read the exact
	// tree that was judged, and a failure nobody can inspect is a failure nobody
	// can act on.
	StagedRoot string

	// Gates are the ladder's results, in the order and the number it returned
	// them. Not filtered, not re-worded, not appended to: this package adds no
	// gate, so a Proof that carried a different list from the one the ladder
	// produced would be reporting an authority that does not exist.
	Gates []validate.GateResult

	// Passed is validate.PromotionDecision's answer over those gates — "every gate
	// reported PASS or SKIPPED" — and it is only ONE of the two conditions
	// publishing requires. The other is the maintainer's approval, which this package does
	// not ask for and this field must never be read as. Passing gates are a
	// permission to ask; they are not the answer.
	Passed bool
}

// Options is what Prove cannot work out for itself: which overlay the staged tree
// is built from, where the staged tree goes, how deep to gate, and the seams the
// build runs through.
//
// The Manifest source and the two POLICY fields of validate.BuildRequest
// (RequireIsolation, LogDir) are carried through from the caller's effective
// configuration. Leaving them at their zero value made isolation a policy only
// one of the two building commands applied, and discarded the log of exactly
// the run that FAILED, the one somebody needs. Neither is decided here: this
// package adds no rule of its own, it carries the decision the command layer
// already resolved.
type Options struct {
	// Overlay is the published overlay the staged tree copies its eclasses and
	// profiles out of. It is READ here and never written:
	// every write this package causes lands under StagingRoot.
	Overlay, StagingRoot string

	// Depth is how far up the ladder the operator asked to go, passed through
	// unchanged. Prove never substitutes a depth of its own — a
	// realignment proved shallower than requested is a proof of something nobody
	// asked about, and one proved deeper spends machine time nobody agreed to.
	//
	// DepthNone is the zero value, and it covers no build gate at all: the ladder
	// then returns an empty list, nothing has FAILED, and this package reports a
	// proof of nothing as Passed. That vacuity is not resolved here, because the
	// depth is the operator's to choose and validate already answers a
	// depth nobody chose where it is chosen — ParseDepth refuses the empty string
	// rather than answering DepthNone, "so a mistyped config key cannot switch
	// validation off in silence". The command that registers --depth is therefore
	// the place that owes a realign run a depth worth having.
	Depth validate.Depth

	// StagedManifest answers, for the PUBLISHED package directory, the Manifest
	// content the staged tree must carry before a build gate can run in it.
	//
	// It is the seam `overlay validate --depth` already goes through, and this
	// package neither produces the bytes nor knows where they come from:
	// Manifest GENERATION lives in autoupdate, and validate accepts only what a
	// caller supplies. Nil means NOTHING TRAVELS — nothing is staged
	// and nothing is built — which is validate's rule verbatim rather than a
	// second one stated here.
	StagedManifest func(pkgDir string) ([]byte, error)

	// RequireIsolation is the operator's policy, carried and never asserted. The
	// refusal it governs lives inside RunBuildGates and fires only when the
	// request carries it; hardcoding a true here would refuse every build on a
	// host that cannot create the namespace, which is the ordinary case.
	RequireIsolation bool

	// LogDir is where the build's whole transcript is kept for whoever has to go
	// past the summary. Empty is still accepted — the gate's own reason then
	// says the log was not retained — but a FAILED gate with no log is the one
	// case where the summary is not enough.
	LogDir string

	// Distdir is the directory the build reads its archives from, carried into
	// the prepared build and set on the child as DISTDIR.
	//
	// Carried, never resolved: this package adds no rule of its own about where
	// archives come from, exactly as it adds none about depth or isolation. A
	// realignment is a SAME-VERSION edit, so no fetch of its own has produced an
	// archive — whatever directory already holds the release's tarball is the
	// command layer's answer to give.
	//
	// Empty sets nothing and invents nothing: the build then reads
	// the host's own configured DISTDIR, which is what every realign proof did
	// before this field existed.
	Distdir string

	// Deps are the process- and host-level seams the build gates run through,
	// passed to RunBuildGates untouched. Its zero value means "use the real
	// thing" — every field is normalised by validate itself — so a caller with
	// nothing to substitute passes nothing.
	Deps validate.BuildDeps
}

// provedRealignment is the ladder's prepared build, held as a package-level
// variable so that a test can answer for it without a build ever running. It is
// the discipline archive.go states for exec.CommandContext, applied one level
// up: production code never reaches past this name.
//
// IT IS validate.RunPreparedBuild ITSELF, not an adapter over it. The var's
// type IS the function's signature, so a change to it stops this file
// compiling instead of quietly changing what a realignment is proved by; and an
// adapter is where a second copy of the ladder, and a rule of this package's
// own about what gets gated, would start. It is the WHOLE operation, not just
// Stage and RunBuildGates: holding only that lower half left out the Manifest
// seam, the host probe and the policy fields, so gates reported SKIPPED and a
// host missing a build dependency became a verdict against the ebuild.
//
// Tests swap this seam rather than validate.BuildDeps because the real runner
// means a real `ebuild … clean compile`. What this package owes is that it
// CALLS the ladder with the depth and policy it was given, and adds nothing.
var provedRealignment = validate.RunPreparedBuild

// Prove materialises a realigned ebuild in a staged tree outside the published
// overlay and runs the ladder's build gates against it, up to the depth the
// caller asked for. It returns what the gates said and whether that is
// enough to be allowed to publish; it publishes nothing.
//
// Outside the overlay is a security property: the overlay auto-commits and
// pushes, so anything staged inside it would be released by the clock, and
// `overlay autoupdate --clean` would delete it. Stage enforces that itself
// (ensureOutsideOverlay); it is not re-checked here.
//
// A FAILED gate comes back as (Proof, nil) with Passed false and the gate's
// reason intact: the realignment was examined. A staging failure comes back as
// an error, because nothing was examined, and "we could not look" must not read
// as "it does not build"; the gates are not consulted after it.
//
// Only the BUILD gates run. validate.Run's options-vs-archive comparison needs
// the upstream archive on disk, and a same-version edit fetched none, so a
// change to the option list is judged by the configure and compile rungs
// instead, and not at all at a depth below them.
func Prove(ctx context.Context, p Proposal, opts Options) (Proof, error) {
	atom := p.atom()

	// The published package directory, which is what the Manifest seam is asked
	// about — never the staged copy. The staged tree is the thing that LACKS a
	// Manifest, which is the whole reason there is a seam to ask.
	result := provedRealignment(ctx, validate.PreparedBuildRequest{
		Overlay:          opts.Overlay,
		StagingRoot:      opts.StagingRoot,
		Key:              atom,
		Version:          p.Version,
		PackageDir:       filepath.Join(opts.Overlay, p.Category, p.Package),
		Ebuild:           p.Ebuild,
		Depth:            opts.Depth,
		StagedManifest:   opts.StagedManifest,
		RequireIsolation: opts.RequireIsolation,
		LogDir:           opts.LogDir,
		Distdir:          opts.Distdir,
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

	// The gate half of publishing — every gate reported PASS or SKIPPED — is
	// asked of validate's own rule rather than re-derived here: a second copy
	// would be the first place the two could disagree about publishing.
	//
	// That rule refuses a gate list whose every deciding gate declined over the
	// CANDIDATE. It does NOT refuse one that declined over THIS HOST, because a
	// machine lacking build dependencies has said nothing about the
	// realignment; so Passed is not sufficient alone, and cmd/bentoo's
	// realignProofCarriesEvidence requires one PASS before asking to publish.
	// The staging error is nil by construction: a staging failure returned
	// above. The refusal reason is not carried on Proof: the failing
	// GateResult's Reason already holds every actionable word, and the summary
	// belongs to the step that refuses to publish.
	passed, _ := validate.PromotionDecision(gates, nil)

	return Proof{StagedRoot: stagedRoot, Gates: gates, Passed: passed}, nil
}

// ErrNotPromoted is the sentinel every refusal below wraps. It carries one step
// further the split Prove already draws between a FAILED gate and a staging
// failure, into the step where the consequence is a publish.
//
// A refusal is the system WORKING: an authority said no, nothing was written,
// and the published overlay is byte-identical. A write that broke is not
// — it happened after every authority had said yes, and it may have left the
// package directory holding something nobody decided on. A caller that reported
// the two the same way would teach its operator to skim past the one that needs
// a human, so the difference is offered as a sentinel rather than as wording a
// command would have to pattern-match.
var ErrNotPromoted = errors.New("the realignment stays unpublished")

// Promote writes a proved realignment into the published overlay, and only when
// the two authorities that are NOT this function have both already said yes:
// every gate reported PASS or SKIPPED, and the maintainer approved. It writes
// the exact bytes that were proved. Every refusal returns before the overlay is
// touched, so a realignment that is not approved or does not pass leaves the
// published tree byte-identical.
//
// approved is a bare bool: asking belongs to the command layer, where the
// terminal is, and a function that asked itself could not be tested for
// refusing without an answer. The maintainer adds what no gate can: the
// overlay's nodejs carries a 492-line divergence that passes every rung and
// would still be wrong to revert.
//
// The gates are re-read through validate.PromotionDecision rather than taken
// from Passed, so a refusal names the gate that said no; Passed is checked as
// well, and a disagreement between the two records refuses. What is published
// is the proposal's own bytes, the slice the gates ran against, never re-read
// from the staged tree: nothing writes there after Stage, and an approval that
// arrives after a cleanup or reboot removed the tree must not expire for it.
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
		return fmt.Errorf("%w: %s-%s has not been approved by the maintainer; gates reporting PASS or SKIPPED are permission to ASK, never the answer",
			ErrNotPromoted, p.atom(), p.Version)
	}

	return publishProvedEbuild(p, overlayRoot)
}

// refuseUnprovedRealignment answers the gate half of publishing — every gate
// reported PASS or SKIPPED — and returns the refusal, named, when it did not.
//
// The empty list is refused first and separately. validate.PromotionDecision
// answers TRUE over no gates, correctly for a list; but RunBuildGates returns
// an empty list for every depth below DepthPatches, so a realignment proved at
// DepthNone or DepthOptions was examined by nothing, and approval would be the
// ONLY authority that ever spoke. Choosing a worthwhile depth stays with the
// command that registers --depth; this is the question of what may be
// PUBLISHED on no evidence. A list holding only the QA gate (which
// PromotionDecision skips) cannot arise here: RunBuildGates reports build
// gates only.
//
// The proposal is passed whole so no caller can hand this function another
// realignment's identifiers.
func refuseUnprovedRealignment(p Proposal, proof Proof) error {
	atom, version := p.atom(), p.Version

	if len(proof.Gates) == 0 {
		return fmt.Errorf("%w: no gate ever read %s-%s, so there is nothing to publish it on; an empty gate list satisfies \"every gate reported PASS or SKIPPED\" only vacuously — prove the realignment at a depth that builds",
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
// The destination must already exist: a realignment rewrites an ALREADY
// PUBLISHED version. Its absence means the overlay moved on since the compare
// (the version dropped, or revised to a -r1), and writing anyway would ADD an
// unapproved ebuild no registry pin claims, which `overlay autoupdate --clean`
// deletes. It mirrors internal/autoupdate's guard (where the destination must
// NOT exist), and like it is checked now because the gates take minutes.
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
// The overlay auto-commits and pushes, so both possible mistakes are published
// ones. An in-place write is refused: the destination exists, and an
// interruption would leave a file that is neither the old bytes nor the proved
// ones under the real name, for the timer to commit. The rename's risk is taken
// knowingly: for one write and one chmod, a dotted temporary file Portage
// ignores exists in the overlay. Every exit but the rename removes it, and a
// failed removal is JOINED to the returned error, since this package logs
// nothing. A reader of the destination sees the old bytes or the proved ones,
// never a mixture.
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
	if chmodErr := os.Chmod(tmpName, mode); chmodErr != nil {
		return fmt.Errorf("setting mode %04o on %s before publishing it as %s: %w", mode.Perm(), tmpName, path, chmodErr)
	}
	if renameErr := os.Rename(tmpName, path); renameErr != nil {
		return fmt.Errorf("renaming %s into place as %s: %w", tmpName, path, renameErr)
	}
	committed = true
	return nil
}
