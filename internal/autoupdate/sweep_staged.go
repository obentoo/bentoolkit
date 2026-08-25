package autoupdate

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/obentoo/bentoolkit/internal/common/distfiles"
	"github.com/obentoo/bentoolkit/internal/common/tui"
)

// runStagedManifest regenerates the Manifest of a STAGED package directory
// against a private distdir this call creates for it.
//
// It is runStagedManifestIn with nothing supplied, which is what every caller
// but the post-fix re-check wants, and it is the form whose behaviour has not
// changed. The whole argument — what is dropped from runManifest and why, where
// the private directory is created, and who removes it — lives on
// runStagedManifestIn below.
func (s *sweeper) runStagedManifest(stagedPkgDir, pkg, version string) (string, error) {
	// An empty supplied distdir means "create one", and that is the entire
	// difference between the two entry points.
	return s.runStagedManifestIn("", stagedPkgDir, pkg, version)
}

// runStagedManifestIn regenerates the Manifest of a STAGED package directory —
// the `<category>/<package>/` inside the single-package repo validate.Stage
// builds — without any shared directory of the host changing while it runs
// (S033-R3, S033-R3.2, design D3).
//
// suppliedDistdir decides WHICH directory the step runs against. Empty means
// "create a private one", and that path is byte-identical to what this function
// has always done. A non-empty one is used AS IT STANDS, and exactly two things
// change with it: the seeding step does not run, and `--force` joins the pkgdev
// argv (S043-R2.2, S043-R2.3, S043-D2 — see "a distdir the caller already
// holds" below).
//
// # Why this is not runManifest with a different cmd.Dir
//
// runManifest wraps `pkgdev manifest` in four helpers — LockFetch, Quarantine,
// PrepopulateFromCache and RecordFetchScope — and all four are keyed on the
// PACKAGE DIRECTORY'S Manifest against a distdir the whole machine shares
// (sweep.go:538-591). Pointing that pair at a staged tree crosses the two:
//
//	Quarantine's contract is "a distfile present under a name the current
//	Manifest does not list cannot be verified, so move it aside". A staged tree's
//	Manifest does not YET name the new version's distfile — that is what this run
//	is about to compute — so the names Quarantine would find unverifiable in the
//	host's DISTDIR are the HOST'S REAL DISTFILES. And a quarantine failure is
//	fatal by contract (sweep.go:563), so the run would also stop on it.
//
// That is how a validation run — a gate whose entire promise is that it changes
// nothing — would come to rearrange /var/cache/distfiles.
//
// # Which of the four survive, and why the rest are not merely skipped
//
// The three that are gone are gone because a PRIVATE distdir removes what each
// of them defends, not because this path cares less:
//
//   - LockFetch arbitrates concurrent writers of a shared directory. A directory
//     this call created a line ago has exactly one writer by construction, so
//     there is no window left to close. A supplied one was created by this same
//     run, for this one bump, and is handed over rather than shared.
//   - Quarantine moves aside somebody else's unverifiable leftovers. A directory
//     this call created is empty; there is nobody else's leftover in it. In a
//     supplied one every file is this run's own download — the very bytes the
//     Manifest is about to be computed from — so moving them aside would take
//     away the answer. Against the host it is the hazard above.
//   - RecordFetchScope + cleanupFailedFetch take away what a failed fetch left in
//     a directory that outlives the run. This one does not outlive the run: the
//     CALLER takes the whole thing, on every path — see "who owns the distdir"
//     below.
//
// PrepopulateFromCache is the one that stays on the path that creates its own
// directory, RE-POINTED so the private distdir is its destination and the shared
// directories are only ever its source. Both sources are read-only here: the
// configured --distfiles-cache, and the host's own DISTDIR through
// distfiles.Locate — which exists precisely for a read-only caller, since it
// neither creates the directory nor probes it by writing into it
// (distfiles.go:192-224). So a distfile already on the machine is reused by
// symlink instead of downloaded a second time, and the direction of every byte
// is: out of the shared directories, into the private one.
//
// The private distdir is created under fixSandboxRoot() rather than os.TempDir()
// for the reason runManifestWithFix already does it (applier.go:1112) — on the
// host S030 was measured on /tmp is a 31 GB tmpfs, so a distfile downloaded into
// a default temporary directory lands in RAM.
//
// # A distdir the caller already holds (S043-D2)
//
// After the LLM fixer repairs an ebuild the agent runs `pkgdev manifest` itself —
// it holds `Bash(pkgdev *)` — so it leaves behind a COMPLETE, valid Manifest and,
// in the private distdir it was given, the archives it downloaded to compute it.
// The authoritative re-check is then handed THAT directory, and both halves of
// the hand-off are load-bearing:
//
//   - `--force`, because pkgdev does not re-manifest a package whose Manifest is
//     already complete. Without the flag the re-check reads the file, finds
//     nothing to do, downloads nothing and exits 0 — so it verifies nothing and
//     accepts the agent's own digests (S043-R2.2). That is the defect measured on
//     2026-08-22: the freshly created distdir also stayed EMPTY, the static gate
//     found nothing in it, fell back to the host DISTDIR and reported SKIPPED,
//     and for media-libs/mesa the 134 MB the repair had fetched were discarded
//     unread. With the flag the digests are recomputed here, from the bytes in
//     this directory, so the Manifest that survives is bentoo's own.
//   - No seeding, and skipped rather than merely redundant. PrepopulateFromCache
//     seeds by os.Symlink, so pointing it at a directory that already holds this
//     run's downloads plants a LINK beside the bytes: a second claim about what
//     this version needs, which outlives the cache entry it points at and which
//     the caller then hands on to the static gates. A dangling link in the
//     directory the gate reads is a subtler version of the empty directory this
//     change removes. The directory already is the answer.
//
// Nothing is downloaded twice either, which is the other half of R2.3: the files
// are where pkgdev looks for them, so `--force` re-digests rather than re-fetches.
//
// The serial-gated prefetch below is NOT part of that skip and still runs on both
// paths. It is not a seed out of something this machine already has: it is the
// only way a distfile pkgdev cannot fetch from SRC_URI arrives at all, and
// `--force` re-digests from scratch, so the file has to be present. The repair
// cannot have brought it either — the agent's tool for this is pkgdev, which is
// exactly what cannot fetch it. A package with no [meta] fetch block, which is
// nearly all of them, is unaffected either way.
//
// # Who owns the distdir (S035-D1)
//
// When it creates the private distdir — being the only one that knows what
// seeding and prefetching the manifest step needs — it does not OWN it. The path
// is returned and the caller removes it once the whole staged sequence for that
// bump is done. A supplied one was the caller's already; it is returned all the
// same, so one removal at the caller covers both shapes.
//
// That split is the fix for a defect this function's own cleanup caused. The
// distfile this step fetches is the ONLY copy of the candidate's archive on a
// host that has never fetched the release, and the static gates of the same run
// are its consumer. A `defer os.RemoveAll(distdir)` here deleted it before the
// gate looked, the gate then read the SHARED distdir, found only the previous
// version's tarball, declined it as belonging to another version — correctly —
// and reported SKIPPED. R3.3 promotes on "PASS or SKIPPED", so the bump was
// published unread.
//
// The removal is not optional and did not become the caller's choice: R2 keeps
// it mandatory on every path including failure, because a sweep over forty
// packages retaining every fetched tarball would fill the very scratch
// filesystem the private directory was created to protect. What changed is only
// WHERE it happens — one level up, after the consumer has read.
//
// A distdir is returned on the error paths too, and that is deliberate: once the
// directory is known, the caller must be able to remove it whatever went wrong
// next. Only the failures above the point it becomes known return "" — which on
// the supplied path is no failure at all, since it is known on entry.
func (s *sweeper) runStagedManifestIn(suppliedDistdir, stagedPkgDir, pkg, version string) (string, error) {
	// These two return suppliedDistdir rather than a literal "": on the ordinary
	// path it IS "", byte for byte what they always returned, and on the supplied
	// path the directory is known before the first check runs, so there is no
	// return here that could lose it.
	_, pkgName, ok := splitPkgAtom(pkg)
	if !ok {
		return suppliedDistdir, fmt.Errorf("%w: invalid package name format: %s", ErrManifestFailed, pkg)
	}
	if stagedPkgDir == "" {
		return suppliedDistdir, fmt.Errorf("%w: no staged package directory for %s-%s", ErrManifestFailed, pkg, version)
	}

	distdir := suppliedDistdir
	supplied := distdir != ""
	if !supplied {
		// The private distdir. Read through the fixSandboxRoot var, never through
		// distfiles.TempRoot directly: the production value asks the host a question
		// (PORTAGE_TMPDIR) and a test must be able to answer it without a portageq on
		// the machine running the suite. "" is a valid answer and means os.TempDir(),
		// which is what os.MkdirTemp already means by an empty root.
		sandboxRoot := fixSandboxRoot()
		var err error
		distdir, err = os.MkdirTemp(sandboxRoot, "bentoo-staged-distfiles-")
		if err != nil {
			return "", fmt.Errorf("%w: creating a private distdir for %s-%s under %q: %w",
				ErrManifestFailed, pkg, version, sandboxRoot, err)
		}
	}
	// From here down every return carries `distdir`, including the failing ones:
	// the directory now exists, and the caller cannot remove what it was not told
	// about. The sweep-fills-the-scratch-filesystem argument that used to justify
	// a defer here is unchanged — it just names the caller now (S035-D1, R2.2).

	// The names this version is expected to need, derived from the STAGED
	// Manifest and the STAGED directory's ebuilds — the same derivation the apply
	// path uses, asked of the tree actually being manifested. It is legitimately
	// empty (an upstream that renames its archive gives nothing to guess from),
	// and empty simply means nothing to reuse: pkgdev downloads as it would have.
	//
	// Not on a supplied directory, which arrives holding this run's own downloads:
	// seeding it would symlink over the answer rather than supply one (S043-D2).
	if !supplied {
		manifestNames := distfiles.ParseManifestDistFilenames(filepath.Join(stagedPkgDir, "Manifest"))
		expected := s.expectedDistfiles(pkg, stagedPkgDir, pkgName, manifestNames, []string{version})
		if len(expected) > 0 {
			for _, src := range s.stagedDistfileSources(distdir) {
				s.reportPrepopulated(pkg, src, distfiles.PrepopulateFromCache(distdir, src, expected))
			}
		}
	}

	// Serial-gated packages: pkgdev cannot fetch their distfile from SRC_URI, so
	// the vendor's download form is submitted with the serial and the file is put
	// where pkgdev will digest it. A package without a [meta] fetch block, and a
	// sweeper built without configs at all, are both no-ops. It writes into the
	// distdir — private either way, this call's or the caller's — and it needs no
	// cleanup branch of its own for the same reason nothing else here does: the
	// returned path carries it to the caller, whose removal covers every path.
	if err := s.prefetchAuthDistfile(pkg, version, distdir); err != nil {
		return distdir, fmt.Errorf("%w: staged manifest for %s-%s: %w", ErrManifestFailed, pkg, version, err)
	}

	// Bound the invocation exactly as the apply path does: a stalled distfile
	// fetch must not hang a gate forever, and cancelling either the parent
	// (SIGINT) or this child (timeout) kills the spawned process.
	ctx, cancel := context.WithTimeout(s.ctx, manifestTimeout)
	defer cancel()

	// `--force` ONLY where the caller supplied the directory. There it is the
	// whole point — a complete Manifest is not re-manifested without it, so the
	// re-check would accept the agent's digests and fetch nothing (S043-R2.2).
	// Here it would be a demand to redo work pkgdev has correctly decided is
	// already done, so the ordinary path's argv stays exactly what it was.
	args := []string{"manifest"}
	if supplied {
		args = append(args, "--force")
	}
	args = append(args, "--distdir", distdir)

	// pkgdev discovers the ebuild from its own working directory, so cmd.Dir is
	// what decides WHICH package is manifested. Anything but the staged directory
	// here would manifest the published one — the opposite of what staging is for.
	cmd := s.execCommand(ctx, "pkgdev", args...)
	cmd.Dir = stagedPkgDir

	// Streamed live as TaskLine events under the package's own task id, and
	// captured into the error, so a red gate says what pkgdev said. One
	// StreamCapture for both streams gives the child a single pipe, which keeps
	// the captured bytes identical to CombinedOutput's.
	sc := tui.NewStreamCapture(s.reporter, pkg, tui.StreamStdout)
	cmd.Stdout = sc
	cmd.Stderr = sc
	runErr := cmd.Run()
	_ = sc.Close()
	if runErr != nil {
		// ErrManifestFailed first, because the promotion decision classifies on
		// that sentinel; the package and version next, because inside a sweep one
		// line of output has to say which of forty packages it belongs to.
		return distdir, fmt.Errorf("%w: staged manifest for %s-%s in %s: %w\nOutput: %s",
			ErrManifestFailed, pkg, version, stagedPkgDir, runErr, sc.Captured())
	}
	return distdir, nil
}

// stagedDistfileSources lists the directories a staged manifest may READ already
// downloaded distfiles out of, in precedence order: the configured
// --distfiles-cache first, because an operator named it, then the host's own
// DISTDIR.
//
// Every entry is a source and never a destination — PrepopulateFromCache links
// FROM these INTO distdir — which is the whole of D3's "the host DISTDIR is read
// through the cache and never written". distfiles.Locate is the accessor that
// makes the read-only part true rather than merely intended: unlike Resolve it
// creates no directory and proves nothing writable by writing into it, and it
// answers "there is nothing here to read" instead of conjuring an empty
// directory.
//
// distdir itself is excluded, and so is a duplicate: linking a directory into
// itself is the case ResolveCache already refuses, and doing the host twice
// would report the same reuse twice.
func (s *sweeper) stagedDistfileSources(distdir string) []string {
	var out []string
	seen := map[string]bool{distdir: true}
	add := func(dir string) {
		if dir == "" || seen[dir] {
			return
		}
		seen[dir] = true
		out = append(out, dir)
	}

	add(distfiles.ResolveCache(s.distfilesCache, distdir))
	if host, ok := distfiles.Locate(s.distdir, s.configuredDistdir); ok {
		add(host)
	}
	return out
}
