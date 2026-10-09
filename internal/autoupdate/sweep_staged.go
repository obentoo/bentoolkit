package autoupdate

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/obentoo/bentoolkit/internal/autoupdate/ebuilds"
	"github.com/obentoo/bentoolkit/internal/common/distfiles"
	"github.com/obentoo/bentoolkit/internal/common/procgroup"
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
func (s *sweeper) runStagedManifest(ctx context.Context, stagedPkgDir, pkg, version string) (string, error) {
	// An empty supplied distdir means "create one", and that is the entire
	// difference between the two entry points.
	return s.runStagedManifestIn(ctx, "", stagedPkgDir, pkg, version)
}

// runStagedManifestIn regenerates the Manifest of a STAGED package directory —
// the `<category>/<package>/` inside the single-package repo validate.Stage
// builds — without any shared directory of the host changing while it runs.
//
// It is not runManifest with another cmd.Dir: runManifest's LockFetch,
// Quarantine and RecordFetchScope guard a distdir the whole machine shares, and
// Quarantine would move aside the HOST'S distfiles a staged Manifest does not
// name yet. A private distdir has nothing for them to guard; only
// PrepopulateFromCache stays, re-pointed to read the shared directories only.
//
// suppliedDistdir "" creates a private distdir under fixSandboxRoot (not
// os.TempDir: /tmp may be a tmpfs, landing distfiles in RAM). A non-empty one —
// the post-fix re-check, given the directory the LLM fixer downloaded into — is
// used AS IT STANDS: no seeding, and `--force` so pkgdev recomputes the agent's
// digests rather than accepting its complete Manifest.
//
// The caller removes the returned distdir, on every path including errors: the
// same run's static gates read the fetched archive from it, and removing it here
// once let them read the shared distdir, report SKIPPED and publish unread.
func (s *sweeper) runStagedManifestIn(ctx context.Context, suppliedDistdir, stagedPkgDir, pkg, version string) (string, error) {
	// These two return suppliedDistdir rather than a literal "": on the ordinary
	// path it IS "", byte for byte what they always returned, and on the supplied
	// path the directory is known before the first check runs, so there is no
	// return here that could lose it.
	if _, _, ok := ebuilds.SplitPkgAtom(pkg); !ok {
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
		sandboxRoot := fixSandboxRoot(ctx)
		var err error
		distdir, err = os.MkdirTemp(sandboxRoot, "bentoo-staged-distfiles-")
		if err != nil {
			return "", fmt.Errorf("%w: creating a private distdir for %s-%s under %q: %w",
				ErrManifestFailed, pkg, version, sandboxRoot, err)
		}
	}
	// From here down every return carries `distdir`, including the failing ones:
	// the directory now exists, and the caller cannot remove what it was not told
	// about. Removal stays mandatory — a sweep over forty packages keeping every
	// tarball would fill the scratch filesystem — it just happens in the caller,
	// after the gates have read.

	// The names this version is expected to need, derived from the PUBLISHED
	// package (see stagedExpectedDistfiles): the staged tree holds the candidate
	// ebuild alone, so it has nothing to derive from. The list is legitimately
	// empty (an upstream that renames its archive gives nothing to guess from),
	// and empty simply means nothing to reuse: pkgdev downloads as it would have.
	//
	// Not on a supplied directory, which arrives holding this run's own downloads:
	// seeding it would plant a symlink beside the bytes — a second claim that
	// dangles once its cache entry goes — rather than supply an answer.
	if !supplied {
		expected := s.stagedExpectedDistfiles(pkg, version)
		if len(expected) > 0 {
			for _, src := range s.stagedDistfileSources(ctx, distdir) {
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
	// It runs on the supplied path too: pkgdev, the agent's tool as well, cannot
	// fetch such a file, and `--force` re-digests from scratch, so it must be here.
	if err := s.prefetchAuthDistfile(ctx, pkg, version, distdir); err != nil {
		return distdir, fmt.Errorf("%w: staged manifest for %s-%s: %w", ErrManifestFailed, pkg, version, err)
	}

	// Bound the invocation exactly as the apply path does: a stalled distfile
	// fetch must not hang a gate forever, and cancelling either the parent
	// (SIGINT) or this child (timeout) stops pkgdev and every process it
	// started, within procgroup.GracePeriod.
	opCtx, cancel := context.WithTimeout(ctx, manifestTimeout)
	defer cancel()

	// `--force` ONLY where the caller supplied the directory. There it is the
	// whole point — a complete Manifest is not re-manifested without it, so the
	// re-check would accept the agent's digests and fetch nothing. The files are
	// already where pkgdev looks, so it re-digests rather than re-fetches.
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
	cmd := s.execCommand(opCtx, "pkgdev", args...)
	cmd.Dir = stagedPkgDir
	// Group mode, for the reason runManifest gives: pkgdev's fetchers hold the
	// output pipe, so stopping pkgdev alone would leave the gate waiting on them.
	// Group owns cmd.Cancel and cmd.WaitDelay; cmd.Stdin stays nil (/dev/null).
	procgroup.Group(cmd)

	// Streamed live as TaskLine events under the package's own task id, and
	// captured into the error, so a red gate says what pkgdev said. One
	// StreamCapture for both streams gives the child a single pipe, which keeps
	// the captured bytes identical to CombinedOutput's.
	sc := tui.NewStreamCapture(s.reporter, pkg, tui.StreamStdout)
	cmd.Stdout = sc
	cmd.Stderr = sc
	// A lingering helper after a 0 exit is a success, not exec.ErrWaitDelay.
	runErr := procgroup.Result(cmd, cmd.Run())
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

// stagedExpectedDistfiles names the distfiles a staged candidate of pkg at
// version is expected to need. It asks expectedDistfiles of the PUBLISHED
// package directory, never of the staged one: validate.Stage carries the
// candidate ebuild alone, so the staged tree has no Manifest and no older
// version to substitute from, and would yield nothing but the authenticated
// fetch's filename. The published directory is read, never written, and still
// holds the current version until promotion.
//
// An atom that does not split, or a published directory with no Manifest or no
// ebuild, yields what expectedDistfiles yields for it: at most the
// authenticated fetch's filename.
func (s *sweeper) stagedExpectedDistfiles(pkg, version string) []string {
	category, pkgName, ok := ebuilds.SplitPkgAtom(pkg)
	if !ok {
		return nil
	}
	publishedDir := filepath.Join(s.overlayPath, category, pkgName)
	manifestNames := distfiles.ParseManifestDistFilenames(filepath.Join(publishedDir, "Manifest"))
	return s.expectedDistfiles(pkg, publishedDir, pkgName, manifestNames, []string{version})
}

// stagedDistfileSources lists the directories a staged manifest may READ already
// downloaded distfiles out of, in precedence order: the configured
// --distfiles-cache first, because an operator named it, then the host's own
// DISTDIR.
//
// Every entry is a source and never a destination — PrepopulateFromCache links
// FROM these INTO distdir — so the host DISTDIR is read through the cache and
// never written. distfiles.Locate is the accessor that
// makes the read-only part true rather than merely intended: unlike Resolve it
// creates no directory and proves nothing writable by writing into it, and it
// answers "there is nothing here to read" instead of conjuring an empty
// directory.
//
// distdir itself is excluded, and so is a duplicate: linking a directory into
// itself is the case ResolveCache already refuses, and doing the host twice
// would report the same reuse twice.
func (s *sweeper) stagedDistfileSources(ctx context.Context, distdir string) []string {
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
	if host, ok := distfiles.Locate(ctx, s.distdir, s.configuredDistdir); ok {
		add(host)
	}
	return out
}
