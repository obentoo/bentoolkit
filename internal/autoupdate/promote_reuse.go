package autoupdate

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/obentoo/bentoolkit/internal/autoupdate/validate"
	"github.com/obentoo/bentoolkit/internal/common/logger"
)

// This file is R10: do not spend the same hours twice.
//
// A run that proved a bump — `--check --llm`, or an earlier `--apply` that got as
// far as the gates — leaves its staged tree on disk (R3.6) and a record beside it
// (R10.4). This file is what a later apply does with the two: promote the tree as
// it stands WHERE it still describes this bump and its record shows it passing
// (R10.1), and otherwise validate first (R10.2).
//
// # THIS IS NOT A CACHE, AND THE DIFFERENCE IS ONE CONDITION
//
// R3.6 retains the staged tree of every bump that was NOT promoted — which is to
// say, of every failure. Put that rule next to "promote a retained tree that
// matches the package, the version and the inputs" with nothing in between and
// the two compose into a defect with no error message: yesterday's REJECTED bump
// matches all three, so it is promoted, and the overlay that auto-commits and
// pushes publishes an ebuild whose own gates said no. The record's gate outcomes
// are what stops it, and that is why an unrecorded tree revalidates too (R10.5) —
// absence of a claim is not a passing claim.
//
// # "UNCHANGED INPUTS" IS DECIDED BY CONTENT, NEVER BY A TIMESTAMP
//
// A mtime moves on a git checkout, an rsync, a container build and a restored
// backup without a byte changing, and it can fail to move when a byte does. Every
// comparison below is over a digest of the bytes themselves.

// ValidationSource says where THIS apply's verdict came from, and it has exactly
// two values (R10.3).
//
// It is on the result rather than only in a log line because a fast green and a
// proved green look identical from the outside: an apply that took four seconds
// because the gates ran a year of Tuesdays ago and one that took four seconds
// because nothing was checked are the same four seconds. The operator is told
// which happened, per package.
const (
	// ValidationSourceStaged means the gates were paid for by an earlier run and
	// this one promoted that run's tree (R10.1).
	ValidationSourceStaged = "staged"
	// ValidationSourceThisRun means this run ran the gates itself (R10.2).
	ValidationSourceThisRun = "this-run"
)

// stagedInputs are the three inputs a promotion decision is allowed to depend on,
// each reduced to a digest of its content.
//
// They are captured as a value, once, at the moment the tree is staged, and the
// same function computes them again on the next run. One function for both sides
// is what makes the comparison meaningful: two implementations of "digest the
// inputs" would drift, and the symptom would be silent — nothing would ever be
// reused and nothing would ever say so.
type stagedInputs struct {
	// ebuild is the candidate's bytes AS STAGED: the source ebuild the applier
	// copies, before the per-package substitutions and before any fixer edit.
	ebuild string
	// substitutions covers the rewrites applied to the staged candidate after
	// staging (a tracked commit hash, an auxiliary variable). Empty when the bump
	// declares none, which is the ordinary case.
	substitutions string
	// distfile is the digest the published package Manifest records for this
	// package's distfiles.
	distfile string
}

// stagedInputsFor digests the inputs of one bump, reading only the published
// overlay.
//
// It is deliberately cheap — two file reads and two hashes — because it runs on
// EVERY staged apply, including the ones that go on to spend an hour compiling.
func (a *Applier) stagedInputsFor(pkg, currentVersion string, update *PendingUpdate) (stagedInputs, error) {
	srcPath := a.EbuildPath(pkg, currentVersion)
	if srcPath == "" {
		return stagedInputs{}, fmt.Errorf("invalid package name format: %s", pkg)
	}

	// The bytes handed to validate.Stage, which is what "the candidate as staged"
	// means: staging writes them verbatim, so digesting the source here and the
	// staged copy there would be the same number by construction — and this side
	// is the one that still works when the staged tree does not exist yet.
	body, err := os.ReadFile(srcPath) //nolint:gosec // the path is derived from the overlay this applier was constructed for
	if err != nil {
		return stagedInputs{}, fmt.Errorf("reading the source ebuild %s of %s to digest what would be staged: %w", srcPath, pkg, err)
	}

	distfile, err := publishedDistfileDigest(filepath.Join(filepath.Dir(srcPath), "Manifest"))
	if err != nil {
		return stagedInputs{}, fmt.Errorf("digesting the distfiles of %s: %w", pkg, err)
	}

	return stagedInputs{
		ebuild:        validate.DigestBytes(body),
		substitutions: substitutionDigest(a.configs[pkg].AuxVar, update),
		distfile:      distfile,
	}, nil
}

// substitutionDigest covers the per-package rewrites applySubstitutions applies
// to the staged candidate.
//
// They are digested SEPARATELY from the ebuild rather than by hashing the staged
// file after the rewrite, and the reason is the same one that makes the pre-fixer
// bytes the ones that count: the staged file also carries whatever the build fixer
// wrote, so a digest taken from it would never match again. These two values come
// from the pending entry and the registry, so both runs can compute them without
// touching the staged tree at all.
//
// A bump declaring neither answers the empty string, which is the ordinary case
// and keeps the record free of a digest of nothing.
func substitutionDigest(auxVar string, update *PendingUpdate) string {
	if update == nil || (update.CommitHash == "" && update.AuxValue == "") {
		return ""
	}
	return validate.DigestBytes([]byte("commit=" + update.CommitHash + "\naux=" + auxVar + "=" + update.AuxValue + "\n"))
}

// publishedDistfileDigest reduces the DIST entries of a published package
// Manifest to one string.
//
// # Why the Manifest and not the tarball
//
// The distfile is the one input that is not in the overlay's text at all, and
// re-hashing it would mean having it on disk — which a promoting run may not, and
// which would cost a download to find out. The Manifest is the tree's own
// statement about which archives this package is digested against, it is written
// by the same `pkgdev manifest` step that fetched them, and it is the file
// promotion overwrites. A tarball re-rolled upstream under the same name changes
// it; nothing else that matters does.
//
// # The shape of the answer
//
// One DIST entry yields that entry's digest VERBATIM, so the string an operator
// is shown in a refusal is the string they can grep for in the Manifest. Several
// entries yield their digests joined in filename order, so the value is stable
// across runs whatever order the Manifest happens to list them in. A package with
// no Manifest, or none with a DIST line, yields the empty string — "this package
// has no distfile input", which is a fact two runs can agree on.
//
// The digest taken per line is the FIRST hash the entry names (`DIST <file>
// <size> BLAKE2B <hash> …`), because pkgdev writes them in a fixed order and the
// first one is present in every Manifest this overlay has ever held.
func publishedDistfileDigest(manifestPath string) (string, error) {
	body, err := os.ReadFile(manifestPath) //nolint:gosec // the path is the Manifest of the package directory being applied
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return "", nil
	case err != nil:
		return "", fmt.Errorf("reading the published Manifest %s: %w", manifestPath, err)
	}

	type entry struct{ name, digest string }
	var entries []entry
	for line := range strings.Lines(string(body)) {
		fields := strings.Fields(line)
		// DIST <filename> <size> <HASHNAME> <hash> [<HASHNAME> <hash> …]
		if len(fields) < 5 || fields[0] != "DIST" {
			continue
		}
		entries = append(entries, entry{name: fields[1], digest: fields[4]})
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].name < entries[j].name })

	digests := make([]string, 0, len(entries))
	for _, e := range entries {
		digests = append(digests, e.digest)
	}
	return strings.Join(digests, ","), nil
}

// stagedReuse is the answer to "may this apply promote the tree that is already
// on disk", and why.
type stagedReuse struct {
	// root is the retained staged tree, empty when there is none.
	root string
	// cand names the candidate inside root, ready for promote.
	cand candidatePaths
	// promote is R10.1's verdict.
	promote bool
	// reached is the depth the retained proof got to, for the report.
	reached string
	// reason explains the verdict in BOTH directions and is never empty. R10.3
	// is "state per package which of the two happened", and a bump that
	// revalidated silently is indistinguishable from one nobody thought about.
	reason string
	// err is set only for the one mismatch that must STOP the apply rather than
	// merely revalidate it: see reusableStagedTree.
	err error
}

// reusableStagedTree decides whether the staged tree already on disk may be
// promoted as it stands (R10.1), or whether this run has to validate first
// (R10.2).
//
// # The order of the conditions is the point
//
// Everything that asks "is this tree about THIS bump" comes first, and only a
// tree that passes all of them reaches the distfile comparison. A tree describing
// a different candidate, or one whose record shows a failure, is simply not
// evidence here — refusing the apply over ITS distfile would report a mismatch
// about a bump nobody is trying to promote.
//
// # Why a changed distfile digest is a refusal and not a revalidation
//
// Every other mismatch means "the retained tree does not answer this question",
// and the answer to that is to ask the question again. A distfile that changed
// under a tree that otherwise matches exactly means something else: the archive
// this package is digested against was replaced upstream under the same name
// between staging and now. That is the one case where the operator's own inputs
// moved without their knowing, so it is said out loud, with BOTH digests named so
// they can see which way it moved, rather than quietly repaired by a re-run that
// would look like an ordinary slow apply. Re-running `--check` restages the tree
// and records the new digest, which is the recovery and is named in the refusal.
func (a *Applier) reusableStagedTree(pkg, newVersion string, inputs stagedInputs, want validate.Depth) stagedReuse {
	root, err := validate.StagedTreePath(a.stagingRoot, pkg, newVersion)
	if err != nil {
		// A malformed atom is not a promotion question; the staging path below
		// reports it in its own words.
		return stagedReuse{reason: fmt.Sprintf("no retained tree could be looked up for %s-%s: %v", pkg, newVersion, err)}
	}

	info, err := os.Stat(root)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return stagedReuse{reason: "no staged tree was retained for this package and version, so the gates run in this run"}
	case err != nil:
		return stagedReuse{reason: fmt.Sprintf("the retained tree %s could not be read (%v), so the gates run in this run", root, err)}
	case !info.IsDir():
		return stagedReuse{reason: fmt.Sprintf("%s is not a staged tree directory, so the gates run in this run", root)}
	}

	cand, err := stagedCandidate(root, pkg, newVersion)
	if err != nil {
		return stagedReuse{root: root, reason: fmt.Sprintf("the retained tree %s could not be addressed: %v", root, err)}
	}
	if _, err := os.Stat(cand.ebuildPath); err != nil {
		return stagedReuse{root: root, reason: fmt.Sprintf("the retained tree %s holds no candidate ebuild (%v), so there is nothing in it to promote", root, err)}
	}

	record, err := validate.ReadStageRecord(root)
	if err != nil {
		// R10.5. Both spellings revalidate; only the unreadable one is worth a
		// line in the log, because it says something about the machine.
		if !errors.Is(err, validate.ErrNoStageRecord) {
			logger.Warn("the validation record beside %s could not be read (%v); %s-%s is validated again", root, err, pkg, newVersion)
		}
		return stagedReuse{root: root, reason: fmt.Sprintf("the retained tree %s carries no readable validation record, and absence of a claim is not a passing claim (R10.5)", root)}
	}

	proved, why := record.Proves(want)
	if !proved {
		return stagedReuse{root: root, reason: why + ", so the gates run in this run"}
	}

	if record.EbuildDigest != inputs.ebuild {
		return stagedReuse{root: root, reason: fmt.Sprintf(
			"the retained tree was proved over a candidate whose bytes digest to %s and this run would stage %s, so it is evidence about a different bump",
			short(record.EbuildDigest), short(inputs.ebuild))}
	}
	if record.SubstitutionDigest != inputs.substitutions {
		return stagedReuse{root: root, reason: "the per-package substitutions this bump carries changed since the retained tree was proved, so its candidate is not the one this run would stage"}
	}

	if record.DistfileDigest != inputs.distfile {
		return stagedReuse{
			root:   root,
			reason: "the retained tree matches this bump exactly, but the distfile it was validated against changed under it",
			err: fmt.Errorf("not promoted: the distfile of %s-%s was validated as %s and the published Manifest now records %s; "+
				"an archive re-rolled upstream under the same name is a different source tree, and every gate's answer was about the old one. "+
				"Re-run `overlay autoupdate --check` for this package to restage and re-prove it against the archive on disk now",
				pkg, newVersion, record.DistfileDigest, inputs.distfile),
		}
	}

	return stagedReuse{
		root:    root,
		cand:    cand,
		promote: true,
		reached: record.Depth.String(),
		reason:  why + ", over inputs that have not changed since, so it is promoted without running a gate again (R10.1)",
	}
}

// short trims a digest for a sentence a human reads. The full value lives in the
// record; twelve hex characters is enough to see that two differ, which is all
// this sentence claims.
func short(digest string) string {
	if len(digest) <= 12 {
		return digest
	}
	return digest[:12] + "…"
}

// recordStagedProof writes the record beside the staged tree this run built
// (R10.4).
//
// # It runs for failures too, and that is the requirement
//
// R3.6 retains the tree of every bump that was not promoted, so the record beside
// a failure is what tells the NEXT run that this tree is evidence of a rejection
// rather than of a proof. Writing one only for successes would leave every failed
// tree unrecorded — and an unrecorded tree revalidates (R10.5), which is safe but
// throws away the one fact worth keeping.
//
// # A record that cannot be written never fails a bump
//
// The cost of a missing record is exactly one revalidation, which is the
// behaviour every release before this one had. Failing an otherwise good apply
// over a bookkeeping file would trade an hour saved for a bump not published.
func (a *Applier) recordStagedProof(root, pkg, version string, inputs stagedInputs, gates []validate.GateResult, depth validate.Depth) {
	if root == "" {
		return
	}
	err := validate.WriteStageRecord(root, validate.StageRecord{
		Package:            pkg,
		Version:            version,
		Depth:              depth,
		Gates:              gates,
		EbuildDigest:       inputs.ebuild,
		SubstitutionDigest: inputs.substitutions,
		DistfileDigest:     inputs.distfile,
	})
	if err != nil {
		logger.Warn("could not record what the gates said about %s-%s beside %s: %v "+
			"(the bump itself is unaffected; the next run validates it again instead of promoting the retained tree)",
			pkg, version, root, err)
	}
}
