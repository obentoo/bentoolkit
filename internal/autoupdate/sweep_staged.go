package autoupdate

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/obentoo/bentoolkit/internal/common/distfiles"
	"github.com/obentoo/bentoolkit/internal/common/tui"
)

// runStagedManifest regenerates the Manifest of a STAGED package directory — the
// `<category>/<package>/` inside the single-package repo validate.Stage builds —
// without any shared directory of the host changing while it runs (S033-R3,
// S033-R3.2, design D3).
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
//     there is no window left to close.
//   - Quarantine moves aside somebody else's unverifiable leftovers. A directory
//     this call created is empty; there is nobody else's leftover in it. Against
//     the host it is the hazard above.
//   - RecordFetchScope + cleanupFailedFetch take away what a failed fetch left in
//     a directory that outlives the run. This one does not outlive the run: the
//     deferred RemoveAll below takes the whole thing, on every path.
//
// PrepopulateFromCache is the one that stays, RE-POINTED so the private distdir
// is its destination and the shared directories are only ever its source. Both
// sources are read-only here: the configured --distfiles-cache, and the host's
// own DISTDIR through distfiles.Locate — which exists precisely for a read-only
// caller, since it neither creates the directory nor probes it by writing into
// it (distfiles.go:192-224). So a distfile already on the machine is reused by
// symlink instead of downloaded a second time, and the direction of every byte
// is: out of the shared directories, into the private one.
//
// The private distdir is created under fixSandboxRoot() rather than os.TempDir()
// for the reason runManifestWithFix already does it (applier.go:1112) — on the
// host S030 was measured on /tmp is a 31 GB tmpfs, so a distfile downloaded into
// a default temporary directory lands in RAM.
func (s *sweeper) runStagedManifest(stagedPkgDir, pkg, version string) error {
	_, pkgName, ok := splitPkgAtom(pkg)
	if !ok {
		return fmt.Errorf("%w: invalid package name format: %s", ErrManifestFailed, pkg)
	}
	if stagedPkgDir == "" {
		return fmt.Errorf("%w: no staged package directory for %s-%s", ErrManifestFailed, pkg, version)
	}

	// The private distdir. Read through the fixSandboxRoot var, never through
	// distfiles.TempRoot directly: the production value asks the host a question
	// (PORTAGE_TMPDIR) and a test must be able to answer it without a portageq on
	// the machine running the suite. "" is a valid answer and means os.TempDir(),
	// which is what os.MkdirTemp already means by an empty root.
	sandboxRoot := fixSandboxRoot()
	distdir, err := os.MkdirTemp(sandboxRoot, "bentoo-staged-distfiles-")
	if err != nil {
		return fmt.Errorf("%w: creating a private distdir for %s-%s under %q: %w",
			ErrManifestFailed, pkg, version, sandboxRoot, err)
	}
	// Registered before anything can fail below, so every exit path — the auth
	// prefetch, the pkgdev failure, the success — leaves the sandbox root as it
	// was found. A sweep over a whole registry that leaked one of these per failed
	// package would fill the scratch filesystem it was created to protect.
	defer func() { _ = os.RemoveAll(distdir) }()

	// The names this version is expected to need, derived from the STAGED
	// Manifest and the STAGED directory's ebuilds — the same derivation the apply
	// path uses, asked of the tree actually being manifested. It is legitimately
	// empty (an upstream that renames its archive gives nothing to guess from),
	// and empty simply means nothing to reuse: pkgdev downloads as it would have.
	manifestNames := distfiles.ParseManifestDistFilenames(filepath.Join(stagedPkgDir, "Manifest"))
	expected := s.expectedDistfiles(pkg, stagedPkgDir, pkgName, manifestNames, []string{version})
	if len(expected) > 0 {
		for _, src := range s.stagedDistfileSources(distdir) {
			s.reportPrepopulated(pkg, src, distfiles.PrepopulateFromCache(distdir, src, expected))
		}
	}

	// Serial-gated packages: pkgdev cannot fetch their distfile from SRC_URI, so
	// the vendor's download form is submitted with the serial and the file is put
	// where pkgdev will digest it. A package without a [meta] fetch block, and a
	// sweeper built without configs at all, are both no-ops. It writes into the
	// PRIVATE distdir, and it needs no cleanup branch of its own for the same
	// reason nothing else here does: the deferred RemoveAll already has it.
	if err := s.prefetchAuthDistfile(pkg, version, distdir); err != nil {
		return fmt.Errorf("%w: staged manifest for %s-%s: %w", ErrManifestFailed, pkg, version, err)
	}

	// Bound the invocation exactly as the apply path does: a stalled distfile
	// fetch must not hang a gate forever, and cancelling either the parent
	// (SIGINT) or this child (timeout) kills the spawned process.
	ctx, cancel := context.WithTimeout(s.ctx, manifestTimeout)
	defer cancel()

	// pkgdev discovers the ebuild from its own working directory, so cmd.Dir is
	// what decides WHICH package is manifested. Anything but the staged directory
	// here would manifest the published one — the opposite of what staging is for.
	cmd := s.execCommand(ctx, "pkgdev", "manifest", "--distdir", distdir)
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
		return fmt.Errorf("%w: staged manifest for %s-%s in %s: %w\nOutput: %s",
			ErrManifestFailed, pkg, version, stagedPkgDir, runErr, sc.Captured())
	}
	return nil
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
