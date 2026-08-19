package validate

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

// This file is R10.4: beside every retained staged tree, one line per gate
// saying what it answered and how deep the run got — and the digests of the
// inputs those answers were about.
//
// # Why the record lives INSIDE the tree it describes
//
// D1 chose no index and no lock on purpose: the retention rule is expressed by
// the PATH, <staging>/<category>/<package>/<version>, which is why several
// packages can be staged concurrently with no coordination at all. A central
// index of proofs would put that back — one file every worker writes, needing a
// lock, and a stale entry pointing at a tree that was restaged underneath it.
// The record therefore goes in the one directory that already belongs to this
// package and this version, so it is created, replaced and destroyed by exactly
// the operations that create, replace and destroy the tree: Stage's RemoveAll
// takes the proof away with the tree it was about (R3.7), which is precisely
// right — a restaged tree has not been proved yet.
//
// # Why there is no timestamp in it
//
// "The inputs are unchanged" is a question about CONTENT and never about a
// clock. A mtime moves on checkout, on rsync, on a container build and on a
// restored backup without a byte changing, and it does NOT move when a file is
// replaced by one of the same size through a tool that preserves times. Storing
// a time here would invite exactly the comparison that gets both of those wrong,
// so the record stores digests and nothing else that could be mistaken for
// freshness.

// stageRecordName is the file the record lives in, inside the staged tree.
//
// The leading dot keeps it out of the way of everything that reads the tree as a
// Portage repository: a scan looks for category directories, and a dotfile at the
// repository root is neither a category nor an ebuild.
const stageRecordName = ".bentoo-stage-record.json"

// ErrNoStageRecord reports that a staged tree carries no record at all.
//
// It is a sentinel because R10.5 is a decision made ON it — an unrecorded tree is
// REVALIDATED — and the caller must be able to tell "no claim" from "the record
// could not be read". Both revalidate today, but they are different facts about
// the operator's machine and only one of them is worth a warning.
var ErrNoStageRecord = errors.New("the staged tree carries no validation record")

// StageRecord is what one validation run leaves beside its staged tree so that a
// later run can promote it without paying for the same hours twice (R10.1,
// R10.4).
//
// # It is evidence, not a cache
//
// A cache answers "have I seen this key". This answers "was this exact candidate
// PROVED, how deeply, and over which inputs" — and it records failures just as
// faithfully as passes, because R3.6 retains the staged tree of every bump that
// was NOT promoted. Feed retention straight into promotion with no record in
// between and yesterday's rejected bump publishes today; Gates is what stops
// that, and it is why the record stores each gate's own outcome rather than a
// single boolean.
type StageRecord struct {
	// Package and Version name the bump, for the human who opens the file. They
	// are deliberately NOT what identifies the record: the path does that (D1),
	// so a tree moved into the wrong directory is already the wrong tree
	// whatever these two say.
	Package string `json:"package,omitempty"`
	Version string `json:"version,omitempty"`

	// Depth is the depth this run SELECTED, which is the depth the gates below
	// were asked to cover — not the depth they reached. R10.1 compares it
	// against the depth the promoting run selects, and the comparison is "not
	// below": a compile-deep proof answers a configure-deep question, a
	// options-deep proof does not.
	Depth Depth `json:"depth"`

	// Gates is one entry per gate the run reported, with its outcome and its
	// reason. A record with no gate in it proves nothing and is treated as no
	// record at all — "something was checked" is not a statement anybody can act
	// on.
	Gates []GateResult `json:"gates"`

	// EbuildDigest is the candidate's bytes AS THEY WERE STAGED — before the
	// build fixer edited them.
	//
	// The pre-fixer bytes are the ones that matter, and getting this wrong is
	// invisible: R8.1 has the fixer edit the staged ebuild in place, so a digest
	// taken from the tree as it stands after a repair would never again match
	// what the next run computes from the overlay, and EVERY bump the fixer
	// touched would revalidate — which are exactly the slow ones, the ones that
	// needed a repair in the first place. The bump would still publish; it would
	// just quietly pay for a second build every time.
	EbuildDigest string `json:"ebuild_digest"`

	// SubstitutionDigest covers the per-package rewrites the applier applies to
	// the staged candidate after staging it (a tracked commit hash, an auxiliary
	// variable). It is empty when the bump declares none, which is the ordinary
	// case and the reason it is not folded into EbuildDigest: the two runs being
	// compared read the same source ebuild, so only these values can make their
	// candidates differ.
	SubstitutionDigest string `json:"substitution_digest,omitempty"`

	// DistfileDigest is the digest the published package Manifest records for
	// the package's distfiles. It is the one input that is not in the overlay's
	// text at all, and a tarball re-rolled upstream under the SAME name between
	// staging and promotion moves it — at which point every gate's answer was
	// about a source tree that is no longer the one being published.
	DistfileDigest string `json:"distfile_digest,omitempty"`
}

// stageRecordWire is the on-disk shape.
//
// It exists for one field: Depth is an int whose ORDERING is its contract, so
// writing it as a number would put the ladder's numbering in a file that
// outlives the process, and inserting a rung between two existing ones would
// silently re-interpret every record already on disk. The name is the same
// spelling `--depth` accepts and Depth.String prints, so a record stays readable
// by a human and by the next version of this program.
type stageRecordWire struct {
	Package            string       `json:"package,omitempty"`
	Version            string       `json:"version,omitempty"`
	Depth              string       `json:"depth"`
	Gates              []GateResult `json:"gates"`
	EbuildDigest       string       `json:"ebuild_digest"`
	SubstitutionDigest string       `json:"substitution_digest,omitempty"`
	DistfileDigest     string       `json:"distfile_digest,omitempty"`
}

// StageRecordPath names the record of one staged tree. It is exported so an
// operator can be TOLD where the evidence is rather than having to know.
func StageRecordPath(stagedRoot string) string {
	return filepath.Join(stagedRoot, stageRecordName)
}

// WriteStageRecord puts rec beside the staged tree at stagedRoot (R10.4).
//
// stagedRoot must already be a directory: this writes a record ABOUT a tree, and
// creating the directory here would leave a proof standing where the thing it
// describes never was.
//
// A failure is the caller's to decide about, and every caller in this repository
// treats it as a warning rather than as a failed bump: the cost of an unwritten
// record is one revalidation, which is the behaviour that predates R10 entirely.
func WriteStageRecord(stagedRoot string, rec StageRecord) error {
	info, err := os.Stat(stagedRoot)
	if err != nil {
		return fmt.Errorf("recording the validation of the staged tree %s: %w", stagedRoot, err)
	}
	if !info.IsDir() {
		return fmt.Errorf("recording the validation of %s: it is not a staged tree directory", stagedRoot)
	}

	body, err := json.MarshalIndent(stageRecordWire{
		Package:            rec.Package,
		Version:            rec.Version,
		Depth:              rec.Depth.String(),
		Gates:              rec.Gates,
		EbuildDigest:       rec.EbuildDigest,
		SubstitutionDigest: rec.SubstitutionDigest,
		DistfileDigest:     rec.DistfileDigest,
	}, "", "  ")
	if err != nil {
		return fmt.Errorf("encoding the validation record of %s: %w", stagedRoot, err)
	}
	body = append(body, '\n')

	// writeStagedFile removes any previous record before writing, so a rewritten
	// record cannot keep the mode of the one it replaces, and it gives the file
	// the staging mode rather than whatever the umask would have chosen.
	if err := writeStagedFile(stagedRoot, stageRecordName, body); err != nil {
		return fmt.Errorf("recording the validation of the staged tree %s: %w", stagedRoot, err)
	}
	return nil
}

// ReadStageRecord reads the record beside the staged tree at stagedRoot.
//
// A missing record answers ErrNoStageRecord, which is R10.5's whole input: an
// unrecorded tree is revalidated. So is an unreadable or malformed one — a
// half-written record is not a weaker claim than a whole one, it is no claim,
// and the only safe reading of "I cannot tell what this tree proved" is to prove
// it again.
func ReadStageRecord(stagedRoot string) (StageRecord, error) {
	path := StageRecordPath(stagedRoot)

	body, err := os.ReadFile(path) //nolint:gosec // the path is derived from the staging root the caller was constructed with
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return StageRecord{}, fmt.Errorf("%w: %s", ErrNoStageRecord, path)
	case err != nil:
		return StageRecord{}, fmt.Errorf("reading the validation record %s: %w", path, err)
	}

	var wire stageRecordWire
	if err := json.Unmarshal(body, &wire); err != nil {
		return StageRecord{}, fmt.Errorf("reading the validation record %s: %w", path, err)
	}

	depth, err := ParseDepth(wire.Depth)
	if err != nil {
		return StageRecord{}, fmt.Errorf("reading the validation record %s: %w", path, err)
	}

	return StageRecord{
		Package:            wire.Package,
		Version:            wire.Version,
		Depth:              depth,
		Gates:              wire.Gates,
		EbuildDigest:       wire.EbuildDigest,
		SubstitutionDigest: wire.SubstitutionDigest,
		DistfileDigest:     wire.DistfileDigest,
	}, nil
}

// Proves answers R10.1's half of the promotion condition: does this record show
// a candidate that was actually MEASURED, at a depth not below want. The string
// explains the answer in BOTH directions, because a promotion that cannot say
// why it skipped the gates is indistinguishable from a bump nobody validated.
//
// # A record whose deciding gates ALL reported SKIPPED is not a proof
//
// This used to ask "every gate PASS or SKIPPED", which is the same vacuity
// S039-R2.1 denies in PromotionDecision — arriving here one reader later and by
// a different door. A run that staged a tree and then stopped before any gate
// read it (a Manifest that could not be produced, an interrupt) records the
// depth it REQUESTED plus a full list of SKIPPED gates, and this accepted that
// as evidence; the reuse path it gates consults neither refuseUnproved nor
// PromotionDecision, so nothing downstream would have caught it and the bump
// published one run later instead of not at all. At least one deciding gate
// must now report PASS (S039-R2.4).
//
// # Why this keys on the OUTCOME where PromotionDecision keys on the CAUSE
//
// PromotionDecision refuses only when a skip named the CANDIDATE
// (GateResult.Declined == DeclineCandidate), so that a host which simply cannot
// build still promotes (S033-R3.12). The two rules must NOT be unified, for two
// reasons:
//
//   - Declined is `json:"-"`. It does not survive this record's round-trip
//     through disk, so every gate of a RELOADED record reads DeclineUnrecorded.
//     Keyed on the cause, the refusal here would be a rule that silently never
//     fires.
//   - A wrong refusal does not cost the same on the two sides. There it STOPS
//     AN APPLY on any workstation that merely lacks the bump's build
//     dependencies. Here it costs one re-validation — the tree is proved again,
//     which is what every release before R10.1 did. Fail-closed is cheap on
//     this side and expensive on the other.
//
// # SKIPPED still counts, beside a measurement
//
// R3.3 promotes on "PASS or SKIPPED" and R3.12 has the outcome name its own
// reach: a host that could not run the configure gate for want of an installed
// dependency produced a promotable result that says so, and a record holding
// that skip BESIDE a pass is still a proof. Reading this as "every deciding
// gate must PASS" would revalidate — and then re-skip — every such bump on
// every run, which is the loop R10.1 exists to break.
//
// # The QA gate never decides
//
// The same D8 exclusion PromotionDecision, Report.ExitCode and WorstOutcome all
// make: the overlay carries pre-existing pkgcheck findings that have nothing to
// do with any bump, and letting them decide would stop every bump in the tree on
// a metadata.xml typo.
//
// # An empty gate list proves nothing
//
// "Some gate was run" is not a statement anybody can act on, and a record that
// names no gate is far more likely to be a truncated or hand-made file than a
// run that legitimately had nothing to report.
func (r StageRecord) Proves(want Depth) (bool, string) {
	if r.Depth < want {
		return false, fmt.Sprintf("the retained tree was proved at depth %s and this run selected %s, so the proof stops short of the question",
			r.Depth, want)
	}

	deciding := 0
	var failed, passed []string
	for _, gate := range r.Gates {
		if gate.Gate == GateQA {
			continue
		}
		deciding++
		switch gate.Outcome {
		case OutcomeFailed:
			failed = append(failed, gate.Gate)
		case OutcomePass:
			passed = append(passed, gate.Gate)
		case OutcomeSkipped:
			// Named so that a skip is a deliberate non-entry in both lists: the
			// refusal below is "nothing PASSED", never "something SKIPPED", so a
			// skip standing beside a real measurement still promotes.
		}
	}

	switch {
	case deciding == 0:
		return false, "the retained tree's record names no gate that decides anything, so it says nothing about this candidate"
	case len(failed) > 0:
		return false, fmt.Sprintf("the retained tree's own record shows %s FAILED, so it is evidence that this bump did NOT pass",
			gateList(failed))
	case len(passed) == 0:
		return false, "the retained tree's record shows every gate that decides anything reporting SKIPPED, so nothing was ever measured about this candidate and it will be proved again"
	}
	return true, fmt.Sprintf("the retained tree was already proved at depth %s: %s reported PASS and no gate that decides anything FAILED",
		r.Depth, gateList(passed))
}

// DigestBytes is the one spelling of "the content of this input", used for every
// digest a StageRecord carries.
//
// It is one function rather than one per input so that the value WRITTEN by a
// proving run and the value COMPUTED by a promoting run cannot come from two
// implementations that drift apart — a mismatch there would not fail anything
// loudly, it would just revalidate everything forever.
func DigestBytes(body []byte) string {
	sum := sha256.Sum256(body)
	return hex.EncodeToString(sum[:])
}

// StagedTreePath is where Stage puts, and a later run looks for, the tree of one
// package and version: <stagingRoot>/<category>/<package>/<version>.
//
// It is exported and shared with Stage itself so the layout is spelled ONCE. A
// promoting run has to find a tree it did not create, and a second spelling of
// the path would be a second spelling that can go stale — the failure being one
// where nothing is ever reused and no error is ever printed.
func StagedTreePath(stagingRoot, atom, version string) (string, error) {
	category, pkg, err := splitStagedAtom(atom)
	if err != nil {
		return "", err
	}
	if err := usableAsPathElement("version", version); err != nil {
		return "", fmt.Errorf("staging %s: %w", atom, err)
	}
	return filepath.Join(stagingRoot, category, pkg, version), nil
}
