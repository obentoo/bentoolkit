package autoupdate

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"github.com/obentoo/bentoolkit/internal/common/ebuild"
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
		s.ctx = context.Background() // SAFE: default parent; replaced by withSweeperContext, which every production caller passes
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
func (s *sweeper) execute(pkg string, plan sweepPlan, manifestVersions ...string) (sweepPlan, error) {
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
	if err := s.runManifest(pkg, manifestVersions...); err != nil {
		return plan, fmt.Errorf("removed %s from %s but failed to regenerate the Manifest: %w",
			strings.Join(removed, ", "), pkg, err)
	}
	return plan, nil
}

// SweepDirPlan is one package directory's verdict in an overlay sweep.
//
// Exactly one of Remove / WouldRemove carries versions: a blocked directory
// removes nothing and reports its candidates in WouldRemove instead, so a
// report can state what an authorised sweep would have done without ever
// implying it happened.
type SweepDirPlan struct {
	// Atom is "category/package" — never a registry key. Paths are built from
	// it, so a ":slot" or "@label" here would be destructive (S027-G4).
	Atom string
	// Remove lists the versions to delete, ascending in Gentoo order. Empty on
	// any blocked plan.
	Remove []string
	// Keep maps a kept version to the entry key claiming it. An empty value
	// means the version is kept by a rule (the live-ebuild rule or the
	// last-non-live floor) rather than by an entry.
	Keep map[string]string
	// Blocked names the entry that lacks a pin, when there is one to name. A
	// directory blocked because NO entry claims it has an empty Blocked — see
	// sweepPlan's type comment for why the two are distinguishable.
	Blocked string
	// WouldRemove lists what a blocked directory left alone, ascending.
	WouldRemove []string
}

// IsBlocked reports whether this directory will remove nothing. It is true for
// both block cases — a pinless entry (Blocked names it) and a directory no
// entry claims (Blocked is empty) — which is why callers must not test
// Blocked != "" on its own.
func (p SweepDirPlan) IsBlocked() bool {
	return len(p.WouldRemove) > 0 || p.Blocked != ""
}

// SweepPlanError is a directory that could not be planned at all — an
// unreadable package directory, not a directory with nothing to do. It is
// reported and dropped from the batch rather than aborting the sweep.
type SweepPlanError struct {
	Atom string
	Err  error
}

// SweepBatch is everything an overlay sweep intends to do, ordered so two runs
// over an unchanged overlay produce the identical plan.
type SweepBatch struct {
	// Dirs holds one entry per candidate directory, sorted by Atom.
	Dirs []SweepDirPlan
	// PlanErrors holds the directories that could not be planned.
	PlanErrors []SweepPlanError
	// SkippedHeld holds the atoms that DO hold an unclaimed ebuild but are not
	// swept because a held entry covers them, sorted.
	//
	// It exists so the tool cannot contradict itself. The reconciliation ends
	// its unclaimed group by pointing at `--clean` (S027-R7.1), and without this
	// field a sweep whose only findings were held would answer "every ebuild is
	// claimed by an entry" — which is false twice over: the ebuild is unclaimed,
	// and it is being protected rather than overlooked. Skipping quietly is what
	// makes a safety rule read as a bug.
	SkippedHeld []string
	// TotalRemove is the number of files the batch would delete. It counts
	// Remove only — a blocked directory's WouldRemove is not pending work.
	TotalRemove int
}

// ErrInvalidSweepTarget is returned for a target that is neither a category nor
// a "category/package" atom present in the overlay.
var ErrInvalidSweepTarget = errors.New("invalid sweep target")

// normaliseSweepTarget validates target against the overlay and returns the
// atom and category it selects. An empty target selects everything.
//
// A registry key is accepted and normalised to its atom ("net-libs/webkit-gtk:6"
// selects net-libs/webkit-gtk), because pasting a key from packages.toml is the
// obvious thing to do and the resulting path is built from the atom either way.
//
// The directory must exist. Failing here rather than returning an empty batch is
// what makes a typo obvious instead of reading as "nothing to clean" (S027-R1.4).
func normaliseSweepTarget(overlayPath, target string) (atom, category string, err error) {
	if target == "" {
		return "", "", nil
	}
	if strings.Contains(target, "/") {
		cat, pkgName, ok := splitPkgAtom(target)
		if !ok {
			return "", "", fmt.Errorf("%w: %q is not a category/package atom", ErrInvalidSweepTarget, target)
		}
		atom = cat + "/" + pkgName
		if err := dirMustExist(filepath.Join(overlayPath, cat, pkgName)); err != nil {
			return "", "", fmt.Errorf("%w: %q: %v", ErrInvalidSweepTarget, target, err)
		}
		return atom, "", nil
	}
	if strings.ContainsAny(target, ":@") {
		return "", "", fmt.Errorf("%w: %q is not a category/package atom", ErrInvalidSweepTarget, target)
	}
	if err := dirMustExist(filepath.Join(overlayPath, target)); err != nil {
		return "", "", fmt.Errorf("%w: %q is not a category in this overlay: %v", ErrInvalidSweepTarget, target, err)
	}
	return "", target, nil
}

func dirMustExist(path string) error {
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return fmt.Errorf("%s is not a directory", path)
	}
	return nil
}

// scopeConfigs narrows the registry to the entries a scoped sweep must
// consider. It returns cfgs unchanged for an empty scope.
//
// # Why this keeps more than it looks like it should
//
// It is an optimisation — sparing a one-package sweep the directory read
// Reconcile performs per enabled entry — and it is the most dangerous function
// in this story, because the sweep it feeds deletes files.
//
// Matching is by ATOM, never by exact key, and disabled and held entries are
// kept. Both rules exist for the same reason: unclaimedIn collects claims from
// EVERY entry of an atom, switched-off ones included, because a switched-off
// entry still holds its ebuild. Drop "media-plugins/gst-plugins-vpx@dev" while
// keeping "@stable" and the dev line's ebuild becomes claimed by nobody — so
// the sweep deletes a maintained release line (S027-G1).
func scopeConfigs(cfgs map[string]PackageConfig, atom, category string) map[string]PackageConfig {
	if atom == "" && category == "" {
		return cfgs
	}
	scoped := make(map[string]PackageConfig, len(cfgs))
	for key, cfg := range cfgs {
		cat, pkgName, ok := splitPkgAtom(key)
		if !ok {
			continue
		}
		switch {
		case atom != "" && cat+"/"+pkgName == atom:
			scoped[key] = cfg
		case category != "" && cat == category:
			scoped[key] = cfg
		}
	}
	return scoped
}

// atomHasHeldEntry reports whether any registry entry for atom is held.
//
// It asks about the whole DIRECTORY, not one key, because planSweep plans a
// directory at a time: a held `:9` slot and an active `:10` slot share one set
// of files, so there is no way to sweep half of it. Any hold over an atom
// therefore protects the whole directory — the fail-safe side of a choice that
// deletes files.
//
// cfg is copied out of the map before the call because IsHeld has a pointer
// receiver and a map value is not addressable.
func atomHasHeldEntry(cfgs map[string]PackageConfig, atom string) bool {
	for key, cfg := range cfgs {
		cat, pkgName, ok := splitPkgAtom(key)
		if !ok || cat+"/"+pkgName != atom {
			continue
		}
		if cfg.IsHeld() {
			return true
		}
	}
	return false
}

// PlanOverlaySweep computes what a sweep would do to every package directory
// holding an ebuild no entry claims, for the whole overlay or for one target.
//
// # Why the candidates come from Reconcile
//
// Reconcile already walks the registry and reports exactly this class of
// finding (UnclaimedEbuild), including its enabled filter. Taking the
// candidates from it means what a sweep touches is what `--check` printed —
// parity by construction rather than by two implementations agreeing (S027-R2.1,
// S027-R2.2). Its Key for that class is already the bare atom.
//
// The one place that parity is deliberately broken is `hold`, and it is broken
// in the reporting direction only: see the filter below (S026-R1.1, S027-R1.3).
//
// # Why the verdict does NOT come from Reconcile
//
// A divergence says a file is unclaimed. Whether removing it is allowed is a
// different question, and planSweep is the only thing that answers it: the
// live-ebuild rule, the last-non-live floor, and both block cases live there.
// Deleting straight from the divergence list would drop all of them — which is
// precisely the failure story 021 exists to prevent (S027-D1).
//
// Nothing here touches the filesystem beyond reading directories.
func PlanOverlaySweep(overlayPath string, cfgs map[string]PackageConfig, target string) (SweepBatch, error) {
	atom, category, err := normaliseSweepTarget(overlayPath, target)
	if err != nil {
		return SweepBatch{}, err
	}

	scoped := scopeConfigs(cfgs, atom, category)

	// One divergence is emitted per unclaimed FILE; several files can share a
	// directory, so reduce to unique atoms before planning.
	seen := make(map[string]bool)
	var atoms, skippedHeld []string
	for _, d := range Reconcile(overlayPath, scoped) {
		if d.Kind != UnclaimedEbuild || seen[d.Key] {
			continue
		}
		// A held directory is reported by --check and swept by nothing. Story
		// 026 stopped Reconcile from skipping held entries so their pin could be
		// recorded, and that made it scan their directories for unclaimed
		// ebuilds too — a reporting change that would arrive here as a DELETION
		// candidate if it were not stopped (S026-R1.1).
		//
		// It is stopped because `hold` exists precisely for the case that makes
		// this dangerous: a maintainer bumps a held package by hand and keeps
		// the previous ebuild as a fallback. --check then pins the new version,
		// which leaves the old one unclaimed, and sweeping it would eat the
		// fallback that `hold` was protecting. The apply-time sweep never had
		// this reach — the applier refuses a held package before any removal —
		// so this keeps the two sweeps saying the same thing about `hold`.
		//
		// The filter is HERE and not in scopeConfigs on purpose: dropping a held
		// entry from the config map would remove it as a CLAIMANT, its own
		// ebuild would become unclaimed, and the sweep would delete the held
		// package itself. That is S027-G1, inverted into a worse bug.
		if atomHasHeldEntry(scoped, d.Key) {
			// Recorded, never silent: see SweepBatch.SkippedHeld.
			seen[d.Key] = true
			skippedHeld = append(skippedHeld, d.Key)
			continue
		}
		seen[d.Key] = true
		atoms = append(atoms, d.Key)
	}
	sort.Strings(atoms)
	sort.Strings(skippedHeld)

	batch := SweepBatch{SkippedHeld: skippedHeld}
	for _, a := range atoms {
		plan, err := planSweep(overlayPath, scoped, a)
		if err != nil {
			// An unreadable directory is a fact about that directory. Report it
			// and keep planning the rest: refusing the whole batch because one
			// directory is unreadable would make a single permission problem
			// block every other cleanup.
			batch.PlanErrors = append(batch.PlanErrors, SweepPlanError{Atom: a, Err: err})
			continue
		}
		dir := SweepDirPlan{
			Atom:        a,
			Remove:      plan.Remove,
			Keep:        plan.Keep,
			Blocked:     plan.Blocked,
			WouldRemove: plan.WouldRemove,
		}
		batch.Dirs = append(batch.Dirs, dir)
		batch.TotalRemove += len(dir.Remove)
	}
	return batch, nil
}

// runManifest regenerates the Manifest file with pkgdev, from inside the
// package directory so pkgdev discovers the ebuilds itself.
//
// versions are the versions whose distfiles may need pre-fetching before pkgdev
// digests them. It is variadic because pkgdev manifests the WHOLE directory:
// an apply has exactly one new version to pre-fetch, while a sweep can leave
// several ebuilds behind and every one of them needs its distfile present
// (S027-G2). Passing none is valid — it simply skips the pre-fetch, which is a
// no-op for every package without an authenticated-fetch [meta] block.
func (s *sweeper) runManifest(pkg string, versions ...string) error {
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
	for _, version := range versions {
		if err := s.prefetchAuthDistfile(pkg, version, distdir); err != nil {
			return err
		}
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

// SweepDirResult is what actually happened to one package directory.
type SweepDirResult struct {
	// Atom is the directory, as a bare "category/package".
	Atom string
	// Removed lists the versions that really went away — never what was merely
	// intended. A report that claims a file still on disk is worse than no
	// report.
	Removed []string
	// Kept mirrors the plan's Keep so a report can name the entry claiming each
	// surviving version (S027-R6.1).
	Kept map[string]string
	// Blocked names the entry that blocked this directory, when there was one.
	Blocked string
	// WouldRemove lists what a blocked directory left alone.
	WouldRemove []string
	// Err is this directory's failure. It never aborts the batch (S027-R4.4).
	Err error
}

// SweepReport is the outcome of an executed batch.
type SweepReport struct {
	// Results holds one entry per planned directory, sorted by Atom.
	Results []SweepDirResult
	// Removed counts the files that went away across the batch.
	Removed int
	// Swept counts directories from which at least one file was removed.
	Swept int
	// BlockedN counts directories that removed nothing because they were
	// blocked.
	BlockedN int
	// Failed counts directories whose removal or Manifest step errored.
	Failed int
}

// sweepOptions configures an executed batch.
type sweepOptions struct {
	concurrency int
	execCommand func(ctx context.Context, name string, arg ...string) *exec.Cmd
	reporter    tui.Reporter
	configs     map[string]PackageConfig
}

// SweepOption configures ExecuteOverlaySweep.
type SweepOption func(*sweepOptions)

// WithSweepConcurrency bounds how many directories are swept at once.
func WithSweepConcurrency(n int) SweepOption {
	return func(o *sweepOptions) { o.concurrency = n }
}

// WithSweepExecCommand injects the command seam (tests, and nothing else).
func WithSweepExecCommand(fn func(ctx context.Context, name string, arg ...string) *exec.Cmd) SweepOption {
	return func(o *sweepOptions) { o.execCommand = fn }
}

// WithSweepReporter routes the Manifest step's streamed output.
func WithSweepReporter(r tui.Reporter) SweepOption {
	return func(o *sweepOptions) { o.reporter = r }
}

// WithSweepPackagesConfig supplies the registry, read only for the [meta] block
// that drives an authenticated distfile fetch.
func WithSweepPackagesConfig(cfgs map[string]PackageConfig) SweepOption {
	return func(o *sweepOptions) { o.configs = cfgs }
}

// ExecuteOverlaySweep runs an approved batch and returns what happened.
//
// # Failure isolation
//
// A directory's error is recorded on its own result and never returned as the
// batch's error (S027-R4.4). One unreadable directory, one locked file or one
// failing pkgdev must not strand the other ninety-nine — the sweep's whole
// purpose is clearing an accumulation, and an all-or-nothing batch would make
// the accumulation permanent.
//
// # Why the default is serial
//
// concurrency defaults to 1. Parallel `pkgdev manifest` runs are unproven
// against a repository-level lock or the metadata cache (S027-A3), so a caller
// that forgets to pass a bound gets the safe behaviour rather than the fast one.
func ExecuteOverlaySweep(ctx context.Context, overlayPath string, batch SweepBatch, opts ...SweepOption) SweepReport {
	o := sweepOptions{concurrency: 1}
	for _, opt := range opts {
		if opt != nil {
			opt(&o)
		}
	}
	if o.concurrency < 1 {
		o.concurrency = 1
	}

	s := newSweeper(overlayPath,
		withSweeperContext(ctx),
		withSweeperExec(o.execCommand),
		withSweeperReporter(o.reporter),
		withSweeperConfigs(o.configs),
	)

	results := make([]SweepDirResult, len(batch.Dirs))
	sem := make(chan struct{}, o.concurrency)
	var wg sync.WaitGroup

	for i, dir := range batch.Dirs {
		// Blocked directories are carried into the report untouched, so R5.1 and
		// R5.2 print from the same structure the plan showed rather than from a
		// second, differently-computed one.
		if dir.IsBlocked() {
			results[i] = SweepDirResult{
				Atom:        dir.Atom,
				Kept:        dir.Keep,
				Blocked:     dir.Blocked,
				WouldRemove: dir.WouldRemove,
			}
			continue
		}
		if len(dir.Remove) == 0 {
			results[i] = SweepDirResult{Atom: dir.Atom, Kept: dir.Keep}
			continue
		}

		wg.Add(1)
		go func(i int, dir SweepDirPlan) {
			defer wg.Done()
			// Check cancellation BEFORE the select. A select with two ready
			// cases picks one at random, and an uncontended semaphore is always
			// ready — so an already-cancelled context would still let some
			// directories through, which for a command that deletes files means
			// a SIGINT that removed things anyway.
			if err := ctx.Err(); err != nil {
				results[i] = SweepDirResult{Atom: dir.Atom, Kept: dir.Keep, Err: err}
				return
			}
			// Cancellable acquire, so a SIGINT arriving mid-batch stops it
			// between directories instead of mid-removal.
			select {
			case sem <- struct{}{}:
				defer func() { <-sem }()
			case <-ctx.Done():
				results[i] = SweepDirResult{Atom: dir.Atom, Kept: dir.Keep, Err: ctx.Err()}
				return
			}
			// nolint:contextcheck — the sweeper was built with THIS ctx
			// (withSweeperContext above), so runManifest derives its deadline
			// from it and a cancellation still kills the spawned pkgdev. The
			// linter cannot see a context carried on a struct, which is the
			// same shape Applier uses.
			results[i] = sweepOneDir(s, dir) //nolint:contextcheck
		}(i, dir)
	}
	wg.Wait()

	report := SweepReport{Results: results}
	for _, r := range results {
		report.Removed += len(r.Removed)
		if len(r.Removed) > 0 {
			report.Swept++
		}
		if r.Err != nil {
			report.Failed++
		} else if r.Blocked != "" || len(r.WouldRemove) > 0 {
			report.BlockedN++
		}
	}
	return report
}

// sweepOneDir executes a single directory's plan, choosing the versions the
// Manifest step pre-fetches.
func sweepOneDir(s *sweeper, dir SweepDirPlan) SweepDirResult {
	kept := survivingVersions(dir.Keep)
	if len(kept) == 0 {
		// Nothing would remain. The floor rule (S021-R4.3) makes this
		// unreachable through planSweep, so reaching it means the plan was built
		// some other way — refuse rather than empty a directory of releases,
		// which no overlay can recover from on its own.
		return SweepDirResult{
			Atom: dir.Atom,
			Kept: dir.Keep,
			Err: fmt.Errorf("refusing to sweep %s: the plan keeps no non-live ebuild, "+
				"so the directory would be left with no release", dir.Atom),
		}
	}

	plan := sweepPlan{Keep: dir.Keep, Remove: dir.Remove}
	executed, err := s.execute(dir.Atom, plan, kept...)
	return SweepDirResult{
		Atom:    dir.Atom,
		Removed: executed.Remove,
		Kept:    dir.Keep,
		Err:     err,
	}
}

// survivingVersions returns the non-live versions a plan keeps, highest first.
//
// Highest first because the Manifest step's pre-fetch is per version and the
// current release is the one most likely to matter; live ebuilds are excluded
// because they have no distfile to fetch. Order is total so two runs agree.
func survivingVersions(keep map[string]string) []string {
	out := make([]string, 0, len(keep))
	for v := range keep {
		if isLiveEbuild("", v) {
			continue
		}
		out = append(out, v)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if c := ebuild.CompareVersions(out[i], out[j]); c != 0 {
			return c > 0
		}
		return out[i] > out[j]
	})
	return out
}
