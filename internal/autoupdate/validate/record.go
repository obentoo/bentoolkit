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

// Beside every retained staged tree sits one record: per gate, what it answered
// and how deep the run got, plus the digests of the inputs those answers were
// about.
//
// The record lives INSIDE the tree it describes. Staging has no index and no
// lock on purpose — the PATH <staging>/<category>/<package>/<version> is the
// retention rule, so packages stage concurrently with no coordination. A central
// index of proofs would bring back a shared file, a lock, and stale entries for
// restaged trees. Inside the tree, the record is created, replaced and destroyed
// by the same operations as the tree: Stage's RemoveAll takes the proof away
// with it, which is right — a restaged tree has not been proved yet.
//
// It holds no timestamp. "The inputs are unchanged" is a question about
// CONTENT: an mtime moves on checkout, rsync or restore without a byte
// changing, and stays put when a time-preserving tool replaces a file. So the
// record stores digests and nothing that could be mistaken for freshness.

// stageRecordName is the file the record lives in, inside the staged tree.
//
// The leading dot keeps it out of the way of everything that reads the tree as a
// Portage repository: a scan looks for category directories, and a dotfile at the
// repository root is neither a category nor an ebuild.
const stageRecordName = ".bentoo-stage-record.json"

// ErrNoStageRecord reports that a staged tree carries no record at all.
//
// It is a sentinel because a decision is made ON it — an unrecorded tree is
// REVALIDATED — and the caller must be able to tell "no claim" from "the record
// could not be read". Both revalidate today, but they are different facts about
// the operator's machine and only one of them is worth a warning.
var ErrNoStageRecord = errors.New("the staged tree carries no validation record")

// ProducedByApplier and ProducedByValidate name what wrote a stage record.
// They are the only two values StageRecord.ProducedBy is ever written with.
//
// They are constants because these STRINGS are on-disk vocabulary rather than
// internal spelling: a record has to be readable by the version that wrote it AND
// by the next one, so renaming a value in the file is the one change that breaks
// reuse silently — nothing fails to compile, and a later run simply stops
// recognising the records an earlier release left beside its trees.
//
// # Why the producer decides anything at all
//
// A stage record is a licence to publish WITHOUT running the gates again: a
// later `--apply` promotes a bump on the strength of what an earlier
// run recorded. The applier writes such a record having staged the very
// candidate it means to publish. A read-only validation run writes one having
// only MEASURED a tree — it promotes nothing, by contract — so its record is
// evidence about an outcome and NOT a licence, and the reuse path has to be able
// to tell the two apart from the bytes on disk alone.
const (
	ProducedByApplier  = "applier"
	ProducedByValidate = "validate"
)

// StageRecord is what one validation run leaves beside its staged tree so that a
// later run can promote it without paying for the same hours twice.
//
// # It is evidence, not a cache
//
// A cache answers "have I seen this key". This answers "was this exact candidate
// PROVED, how deeply, and over which inputs" — and it records failures just as
// faithfully as passes, because the staged tree of every bump that was NOT
// promoted is retained too. Feed retention straight into promotion with no record in
// between and yesterday's rejected bump publishes today; Gates is what stops
// that, and it is why the record stores each gate's own outcome rather than a
// single boolean.
type StageRecord struct {
	// Package and Version name the bump, for the human who opens the file. They
	// are deliberately NOT what identifies the record: the path does that,
	// so a tree moved into the wrong directory is already the wrong tree
	// whatever these two say.
	Package string `json:"package,omitempty"`
	Version string `json:"version,omitempty"`

	// ProducedBy names what wrote this record: ProducedByApplier or
	// ProducedByValidate. A record written by a read-only validation run is
	// evidence about a tree's outcome, NOT a licence to publish it.
	//
	// EMPTY MEANS ProducedByApplier, and the read path is where that is said —
	// see ReadStageRecord. Every record already on an operator's disk was
	// written before this field existed, and every one of them came from the
	// applier.
	ProducedBy string `json:"produced_by,omitempty"`

	// Depth is the depth this run SELECTED, which is the depth the gates below
	// were asked to cover — not the depth they reached. Reuse compares it
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
	// invisible: the build fixer edits the staged ebuild in place, so a digest
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
	ProducedBy         string       `json:"produced_by,omitempty"`
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

// WriteStageRecord puts rec beside the staged tree at stagedRoot.
//
// stagedRoot must already be a directory: this writes a record ABOUT a tree, and
// creating the directory here would leave a proof standing where the thing it
// describes never was.
//
// A failure is the caller's to decide about, and every caller in this repository
// treats it as a warning rather than as a failed bump: the cost of an unwritten
// record is one revalidation, which is the behaviour that predates records.
//
// rec.ProducedBy is written exactly as the caller stated it and is never
// defaulted here. A writer is the one thing that KNOWS what it is, so filling a
// producer in on this side would let a future one inherit the applier's
// provenance by merely forgetting to name itself. Absence is given its meaning
// on the way back in, in ReadStageRecord, where the records that predate the
// field are.
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
		ProducedBy:         rec.ProducedBy,
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
// A missing record answers ErrNoStageRecord, and the reuse decision is: an
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

	// A record carrying no producer at all is applier-produced.
	//
	// The normalization lives here, beside ParseDepth, because this is where the
	// on-disk shape is turned into what the value MEANS — but it is deliberately
	// not ParseDepth's shape. An unknown depth is a malformed record and fails
	// loudly; an ABSENT producer is a perfectly well-formed record from before
	// the field existed, and all of those were written by the applier. Read
	// absence as anything else and the provenance check on the reuse path refuses
	// every tree an earlier release staged: the whole saving switched off in
	// silence, every bump paying for its gates a second time with nothing in the
	// output saying why.
	//
	// A producer this version does not RECOGNISE is passed through as it stands
	// rather than rejected. The record is still well-formed; the reuse path is
	// fail-closed on anything that is not ProducedByApplier, so the worst an
	// unknown name can cost is one revalidation — while refusing to read it here
	// would break the promise the wire shape exists to keep, that a record stays
	// readable by the next version of this program.
	producer := wire.ProducedBy
	if producer == "" {
		producer = ProducedByApplier
	}

	return StageRecord{
		Package:            wire.Package,
		Version:            wire.Version,
		ProducedBy:         producer,
		Depth:              depth,
		Gates:              wire.Gates,
		EbuildDigest:       wire.EbuildDigest,
		SubstitutionDigest: wire.SubstitutionDigest,
		DistfileDigest:     wire.DistfileDigest,
	}, nil
}

// Proves answers the record's half of the promotion condition: does it show a
// candidate that was actually MEASURED, at a depth not below want. The string
// explains the answer either way: a promotion that cannot say why it skipped
// the gates is indistinguishable from a bump nobody validated.
//
// At least one deciding gate must PASS. A run that stopped before any gate read
// the tree (no Manifest, an interrupt) records its requested depth beside a
// list of SKIPPED gates, and the reuse path consults neither refuseUnproved nor
// PromotionDecision, so accepting that would publish an unmeasured bump. A skip
// BESIDE a pass still proves: requiring every gate to PASS would revalidate,
// and re-skip, every bump whose host lacks a build dependency, on every run.
//
// This keys on the OUTCOME where PromotionDecision keys on the CAUSE, and the
// two must not be unified: Declined is `json:"-"`, so a reloaded record reads
// DeclineUnrecorded and a cause-keyed refusal would never fire; and a wrong
// refusal here costs one revalidation, where there it stops an apply.
//
// The QA gate never decides (pre-existing pkgcheck findings must not stop every
// bump), and an empty gate list proves nothing.
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
