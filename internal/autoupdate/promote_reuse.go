package autoupdate

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"

	"github.com/obentoo/bentoolkit/internal/autoupdate/registry"
	"github.com/obentoo/bentoolkit/internal/autoupdate/validate"
)

// This file exists so the same hours are not spent twice.
//
// A run that proved a bump — `--check --llm`, or an earlier `--apply` that got as
// far as the gates — leaves its staged tree on disk and a record beside it. A
// later apply promotes the tree as it stands WHERE it still describes this bump
// and its record shows it passing, and otherwise validates first.
//
// It is NOT a cache, and the difference is one condition. The staged tree of
// every bump that was NOT promoted — every failure — is retained, so matching
// package, version and inputs alone would promote yesterday's REJECTED bump and
// the auto-committing overlay would publish an ebuild its own gates refused. The
// record's gate outcomes stop that, which is why an unrecorded tree revalidates
// too: absence of a claim is not a passing claim.
//
// "Unchanged inputs" is decided by content, never by a timestamp: a mtime moves
// on a git checkout, an rsync, a container build and a restored backup without a
// byte changing, and can fail to move when a byte does.

// ValidationSource says where THIS apply's verdict came from, and it has exactly
// two values.
//
// It is on the result rather than only in a log line because a fast green and a
// proved green look identical from the outside: an apply that took four seconds
// because the gates ran a year of Tuesdays ago and one that took four seconds
// because nothing was checked are the same four seconds. The operator is told
// which happened, per package.
const (
	// ValidationSourceStaged means the gates were paid for by an earlier run and
	// this one promoted that run's tree.
	ValidationSourceStaged = "staged"
	// ValidationSourceThisRun means this run ran the gates itself.
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
		substitutions: substitutionDigest(a.configs[pkg], update),
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
// The requirement pins are one of them: the pinned atom is rewritten to the
// captured version, so a tree staged before `requires` was declared, before its
// pin changed, or with another captured version must not be promoted as it
// stands. Each requirement is digested as atom, pin and captured version.
//
// A bump declaring none of them answers the empty string, which is the ordinary
// case and keeps the record free of a digest of nothing.
func substitutionDigest(cfg registry.PackageConfig, update *PendingUpdate) string {
	if update == nil || (update.CommitHash == "" && update.AuxValue == "" && len(cfg.Requires) == 0 && len(update.Requires) == 0) {
		return ""
	}
	text := "commit=" + update.CommitHash + "\naux=" + cfg.AuxVar + "=" + update.AuxValue + "\n"
	atoms := slices.Sorted(maps.Keys(cfg.Requires))
	for atom := range update.Requires {
		if _, declared := cfg.Requires[atom]; !declared {
			atoms = append(atoms, atom)
		}
	}
	slices.Sort(atoms)
	for _, atom := range atoms {
		text += "requires=" + atom + "=" + cfg.Requires[atom].Pin + ":" + update.Requires[atom] + "\n"
	}
	return validate.DigestBytes([]byte(text))
}

// publishedDistfileDigest reduces the DIST entries of a published package
// Manifest to one string.
//
// It reads the Manifest, not the tarball: re-hashing the distfile needs it on
// disk, which a promoting run may not have without a download. The Manifest is
// the tree's own statement of which archives the package is digested against,
// written by the `pkgdev manifest` step that fetched them, so a tarball
// re-rolled upstream under the same name changes it.
//
// One DIST entry yields its digest VERBATIM, so the operator can grep a refusal's
// value in the Manifest; several are joined in filename order, stable whatever
// order the Manifest lists them in. No Manifest, or no DIST line, yields "" —
// "no distfile input", a fact two runs can agree on. Each line contributes the
// FIRST hash it names (`DIST <file> <size> BLAKE2B <hash> …`): pkgdev writes
// them in a fixed order and the first is present in every Manifest here.
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
	// promote is the reuse verdict.
	promote bool
	// reached is the depth the retained proof got to, for the report.
	reached string
	// reason explains the verdict in BOTH directions and is never empty: the
	// operator is told per package which of the two happened, and a bump that
	// revalidated silently is indistinguishable from one nobody thought about.
	reason string
	// err is set only for the one mismatch that must STOP the apply rather than
	// merely revalidate it: see reusableStagedTree.
	err error
}

// reusableStagedTree decides whether the staged tree already on disk may be
// promoted as it stands, or whether this run has to validate first.
//
// Every "is this tree about THIS bump" check comes first; only a tree passing
// all of them reaches the distfile comparison, so no apply is refused over the
// distfile of a bump nobody is trying to promote.
//
// A record is a licence only if the applier wrote it. A read-only `overlay
// validate --depth` (or a realign proposal, or `overlay compare --depth`) can
// record the very candidate this run would stage, and promoting on it would
// publish on the output of a command that promises to change nothing. So
// provenance is asked BEFORE the record's claims are read, but AFTER the record
// is read, so an unreadable record keeps its own, more specific reason. It is
// compared against the applier's OWN name: an unknown producer costs one
// revalidation instead of a publication.
//
// A changed distfile digest under an otherwise matching tree is a refusal, not
// a revalidation: the archive was replaced upstream under the same name, so the
// refusal names BOTH digests and the recovery (re-run `--check`).
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
		// Both spellings revalidate; only the unreadable one is worth a
		// line in the log, because it says something about the machine.
		if !errors.Is(err, validate.ErrNoStageRecord) {
			a.logger().Warn("the validation record beside the retained tree could not be read; the bump is validated again",
				"root", root, "err", err, "package", pkg, "version", newVersion)
		}
		return stagedReuse{root: root, reason: fmt.Sprintf("the retained tree %s carries no readable validation record, and absence of a claim is not a passing claim", root)}
	}

	// A producer this version does not RECOGNISE lands here too, by
	// construction: ReadStageRecord passes an unknown name through verbatim rather
	// than refusing to read a well-formed record left by a release this one is too
	// old to know about, and this side refuses everything that is not the
	// applier's own name. An ABSENT producer never arrives here as absence —
	// ReadStageRecord reads it as the applier, because every record already
	// on an operator's disk predates the field and came from the applier.
	//
	// The name is quoted because it is text found on disk rather than a value this
	// run chose, and an operator diagnosing a refusal has to see what is really in
	// the file.
	if record.ProducedBy != validate.ProducedByApplier {
		return stagedReuse{root: root, reason: fmt.Sprintf(
			"the retained tree's record was produced by %q rather than by the applier, so it is evidence about a "+
				"different question — what a run MEASURED about this tree, not whether this bump may be published "+
				"without running a gate again",
			record.ProducedBy)}
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
		reason:  why + ", over inputs that have not changed since, so it is promoted without running a gate again",
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

// recordStagedProof writes the record beside the staged tree this run built.
//
// # It runs for failures too, and that is the requirement
//
// The tree of every bump that was not promoted is retained, so the record beside
// a failure is what tells the NEXT run that this tree is evidence of a rejection
// rather than of a proof. Writing one only for successes would leave every failed
// tree unrecorded — and an unrecorded tree revalidates, which is safe but
// throws away the one fact worth keeping.
//
// # A record that cannot be written never fails a bump
//
// The cost of a missing record is exactly one revalidation, which is the
// behaviour every release before this one had. Failing an otherwise good apply
// over a bookkeeping file would trade an hour saved for a bump not published.
func (a *Applier) recordStagedProof(ctx context.Context, root, pkg, version string, inputs stagedInputs, gates []validate.GateResult, depth validate.Depth) {
	if root == "" {
		return
	}

	// THE INTERRUPT INVARIANT, EXTENDED ACROSS RUNS — and it has to be, because
	// this record is the one thing here that OUTLIVES the run.
	//
	// refuseOnInterrupt stops a cancelled run from publishing. It cannot stop the
	// NEXT one: gates that were never asked report SKIPPED, Proves() accepts any
	// list of PASS-or-SKIPPED at the requested depth, and the following --apply
	// then takes the staged-tree reuse path — which consults neither
	// refuseUnproved nor PromotionDecision — under a context that is not cancelled. Writing this
	// record would turn "Ctrl-C does not publish" into "Ctrl-C publishes one run
	// later", which is the same bump reaching the overlay by a slower route.
	//
	// Nothing else has to change, and nothing is lost: a retained tree carrying no
	// readable record already revalidates, because absence of a claim is not a
	// passing claim. The tree itself still stays on disk as the failure's
	// evidence.
	if ctxErr := ctx.Err(); ctxErr != nil {
		a.logger().Warn("the run was interrupted, so what the gates reported is NOT recorded beside the retained tree: "+
			"they were stopped rather than answered, and the next run validates this bump again instead of "+
			"promoting it on their silence", "package", pkg, "version", version, "root", root, "err", ctxErr)
		return
	}

	// Naming the producer is what keeps the provenance check from being a rule
	// that can only refuse. The reuse path above promotes on this exact value and
	// on no other, so an applier that stopped naming itself — or named itself in
	// some other spelling — would see every tree it retains refuse its own
	// evidence on the next run: the whole reuse saving switched off with nothing
	// in the output saying why. The named constant rather than a literal is that
	// spelling being fixed in one place.
	err := validate.WriteStageRecord(root, validate.StageRecord{
		Package:            pkg,
		Version:            version,
		ProducedBy:         validate.ProducedByApplier,
		Depth:              depth,
		Gates:              gates,
		EbuildDigest:       inputs.ebuild,
		SubstitutionDigest: inputs.substitutions,
		DistfileDigest:     inputs.distfile,
	})
	if err != nil {
		a.logger().Warn("could not record what the gates said beside the retained tree "+
			"(the bump itself is unaffected; the next run validates it again instead of promoting the retained tree)",
			"package", pkg, "version", version, "root", root, "err", err)
	}
}
