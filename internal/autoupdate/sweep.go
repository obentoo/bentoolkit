package autoupdate

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/obentoo/bentoolkit/internal/common/logger"
	"github.com/obentoo/bentoolkit/internal/common/tui"
)

// sweeper executes a sweepPlan: it removes the ebuilds the plan condemns and
// regenerates the package directory's Manifest. It is the ONLY place either
// happens — Applier.cleanPackageDir delegates its tail here, and the standalone
// overlay sweep calls it directly.
//
// # Why it is not a method on Applier
//
// The executor used to live on Applier, which put it behind Apply's pending.json
// guard: a package with no pending update never reached it, so a directory could
// only be swept in the same run that bumped it (S027 Summary). Building a real
// Applier just to sweep is not the fix either — NewApplier initialises a
// PendingList and creates a logs directory, state a sweep has no business
// creating. So the executor is lifted off Applier entirely and Applier holds one
// of these instead.
//
// The fields are exactly what the removal loop and the Manifest step need, and
// nothing else. In particular there is no fixer: runManifestWithFix stays on
// Applier, so no sweep can reach the LLM manifest repair (S027-R4.5).
type sweeper struct {
	// overlayPath is the overlay root every path is built from.
	overlayPath string
	// ctx is the parent context for spawned commands, so a SIGINT kills an
	// in-flight `pkgdev manifest`.
	ctx context.Context
	// execCommand builds the command to run (injectable for tests).
	execCommand func(ctx context.Context, name string, arg ...string) *exec.Cmd
	// reporter receives the manifest step's streamed output.
	reporter tui.Reporter
	// configs is read for the optional [meta] block that drives an
	// authenticated distfile fetch. A nil map simply disables that path.
	configs map[string]PackageConfig
}

// sweeperOption configures a sweeper at construction.
type sweeperOption func(*sweeper)

func withSweeperContext(ctx context.Context) sweeperOption {
	return func(s *sweeper) { s.ctx = ctx }
}

func withSweeperExec(fn func(ctx context.Context, name string, arg ...string) *exec.Cmd) sweeperOption {
	return func(s *sweeper) { s.execCommand = fn }
}

func withSweeperReporter(r tui.Reporter) sweeperOption {
	return func(s *sweeper) { s.reporter = r }
}

func withSweeperConfigs(cfgs map[string]PackageConfig) sweeperOption {
	return func(s *sweeper) { s.configs = cfgs }
}

// newSweeper builds a sweeper and normalises every injectable field.
//
// The normalisation is not test scaffolding, it is the production contract. A
// sweeper built outside NewApplier — which is the whole point of this type —
// arrives with a nil ctx and a nil execCommand unless a caller remembers every
// option, and both panic on the first Manifest run rather than failing with an
// error. A suite that only ever constructs one through a fully-populated helper
// never sees it (S027-G5).
func newSweeper(overlayPath string, opts ...sweeperOption) *sweeper {
	s := &sweeper{overlayPath: overlayPath}
	for _, opt := range opts {
		if opt != nil {
			opt(s)
		}
	}
	if s.ctx == nil {
		s.ctx = context.Background()
	}
	if s.execCommand == nil {
		s.execCommand = exec.CommandContext
	}
	if s.reporter == nil {
		s.reporter = tui.Noop()
	}
	return s
}

// ebuildPath returns the path of one version's ebuild inside pkg's directory,
// or "" when pkg is not a well-formed atom. The path is always built from the
// split components, never from the raw key: a ":slot" or "@label" leaking into
// a path is destructive here rather than merely wrong (S027-G4).
func (s *sweeper) ebuildPath(pkg, version string) string {
	category, pkgName, ok := splitPkgAtom(pkg)
	if !ok {
		return ""
	}
	return filepath.Join(s.overlayPath, category, pkgName, fmt.Sprintf("%s-%s.ebuild", pkgName, version))
}

// execute removes every version in plan.Remove and regenerates the Manifest
// once, returning the plan with Remove narrowed to what actually went away.
//
// manifestVersion is the version handed to the Manifest step. It is NOT
// cosmetic: runManifest forwards it to prefetchAuthDistfile, which downloads a
// serial-gated distfile for exactly that version. An apply passes the version it
// just created; a sweep must pass a version that REMAINS in the directory, never
// one it is about to delete (S027-G2).
//
// Every failure returns the plan as executed so far, so a caller can report what
// really happened rather than what was intended.
func (s *sweeper) execute(pkg string, plan sweepPlan, manifestVersion string) (sweepPlan, error) {
	// Remove is ascending, so a sweep cut short by a failure still hands back an
	// ascending prefix of what it intended.
	planned := plan.Remove
	var removed []string
	for _, version := range planned {
		path := s.ebuildPath(pkg, version)
		if err := os.Remove(path); err != nil {
			if errors.Is(err, os.ErrNotExist) {
				// Already gone: the sweep's goal for this file is met. It is not
				// counted as a removal, so a directory whose candidates had all
				// vanished does not trigger a Manifest regeneration for a
				// change that never happened.
				continue
			}
			plan.Remove = removed
			return plan, fmt.Errorf("swept %d of %d ebuild(s) from %s, then failed to remove %s: %w",
				len(removed), len(planned), pkg, path, err)
		}
		removed = append(removed, version)
	}
	plan.Remove = removed

	// R4.2: exactly once, after the last removal, and only when a file actually
	// went away. The Manifest is regenerated so its distfile entries stop
	// referencing the removed versions; with nothing removed there is nothing to
	// prune, and the run would only re-fetch distfiles for an untouched
	// directory.
	if len(removed) == 0 {
		return plan, nil
	}
	if err := s.runManifest(pkg, manifestVersion); err != nil {
		return plan, fmt.Errorf("removed %s from %s but failed to regenerate the Manifest: %w",
			strings.Join(removed, ", "), pkg, err)
	}
	return plan, nil
}

// runManifest regenerates the Manifest file with pkgdev, from inside the
// package directory so pkgdev discovers the ebuilds itself.
func (s *sweeper) runManifest(pkg, version string) error {
	// Parse package name
	category, pkgName, ok := splitPkgAtom(pkg)
	if !ok {
		return fmt.Errorf("invalid package name format: %s", pkg)
	}

	// Package directory pkgdev operates in (it discovers the ebuild itself).
	pkgDir := filepath.Join(s.overlayPath, category, pkgName)

	// Writable distdir we own, so fetching/digesting never touches the system
	// DISTDIR. Removed when the manifest step returns; distfiles for an upstream
	// bump are new names absent from any cache, so there is nothing to persist.
	distdir, err := os.MkdirTemp("", "bentoo-distfiles-")
	if err != nil {
		return fmt.Errorf("failed to create temp distdir: %w", err)
	}
	defer func() { _ = os.RemoveAll(distdir) }()

	// Serial-gated packages: their distfile cannot be fetched by pkgdev from
	// SRC_URI, so pre-populate the distdir by submitting the vendor's download
	// form with the serial. pkgdev then digests the local file. A package
	// without fetch instructions is a no-op; a configured-but-failing fetch
	// aborts here with a clear, serial-free error.
	if err := s.prefetchAuthDistfile(pkg, version, distdir); err != nil {
		return err
	}

	// Bound the manifest invocation: derive a child context from the parent
	// context with a finite deadline so a stalled distfile fetch cannot hang the
	// caller forever. Cancelling either the parent (SIGINT) or this child
	// (timeout) kills the spawned process via exec.CommandContext.
	ctx, cancel := context.WithTimeout(s.ctx, manifestTimeout)
	defer cancel()

	// Run pkgdev manifest from the package directory.
	cmd := s.execCommand(ctx, "pkgdev", "manifest", "--distdir", distdir)
	cmd.Dir = pkgDir

	// Stream the long manifest run (distfile download + digest) live as TaskLine
	// events (S010-R1.1; the StreamCapture handles in-place "\r" updates, S010-R1.2). The
	// task id is pkg so the lines are attributed to the same task the reporter
	// lifecycle uses (sub-task 3.1). The SAME StreamCapture instance is used for
	// both stdout and stderr, so exec gives the child a single pipe — the captured
	// bytes are byte-identical to CombinedOutput's, keeping the error string and
	// every existing failure test byte-identical (S010-R7.1). Under the default Noop
	// reporter the TaskLine events are discarded, so behaviour is unchanged (S010-R3.3).
	sc := tui.NewStreamCapture(s.reporter, pkg, tui.StreamStdout)
	cmd.Stdout = sc
	cmd.Stderr = sc
	runErr := cmd.Run()
	_ = sc.Close()
	if runErr != nil {
		return fmt.Errorf("command failed: %w\nOutput: %s", runErr, sc.Captured())
	}

	return nil
}

// prefetchAuthDistfile downloads a serial-gated distfile into distdir when the
// package's [meta] block configures an authenticated fetch. It is a no-op for
// packages without that config (the overwhelming majority) and when no config
// was supplied at all. The download is bounded by the parent context so SIGINT
// cancels it, and the serial never appears in logs.
func (s *sweeper) prefetchAuthDistfile(pkg, version, distdir string) error {
	cfg, ok := s.configs[pkg]
	if !ok {
		return nil
	}
	spec, enabled, err := parseAuthFetchSpec(cfg.Meta)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrManifestFailed, err)
	}
	if !enabled {
		return nil
	}

	logger.Info("authenticated fetch: downloading %s distfile for %s (serial via $%s)",
		pkg, version, spec.serialEnv)

	dest, err := spec.fetchDistfile(s.ctx, version, distdir)
	if err != nil {
		return err
	}
	logger.Info("authenticated fetch: wrote %s", filepath.Base(dest))
	return nil
}
