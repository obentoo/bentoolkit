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

	"github.com/obentoo/bentoolkit/internal/common/distfiles"
	"github.com/obentoo/bentoolkit/internal/common/ebuild"
	"github.com/obentoo/bentoolkit/internal/common/logger"
	"github.com/obentoo/bentoolkit/internal/common/tui"
)

// resolveDistdir is the seam the manifest step resolves its distdir through, and
// it is a variable for the same reason execCommand and lookPath are variables in
// this package: the production value reaches a directory that belongs to the
// whole machine.
//
// distfiles.Resolve with nothing configured lands on the host's own DISTDIR
// (/var/cache/distfiles), which is the point of this story — and which the test
// suite must never write into. Tests reach runManifest from two directions: a
// sweeper they build themselves, and Applier.sweeper(), which takes no distdir
// option at all, so there is no per-test way to redirect the second one. One
// seam is what makes "no test can quarantine, lock or probe inside the host's
// DISTDIR" a property of the package rather than a promise each test keeps.
//
// Only tests replace it, and they replace it for the whole package.
var resolveDistdir = distfiles.Resolve

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
	// distdir and configuredDistdir are the two configurable rungs of
	// distfiles.Resolve's precedence (S030-D2): the --distdir flag and the
	// autoupdate.distdir config key. Both are empty today, and two empty
	// strings ARE the production default — Resolve then asks the host itself
	// (`portageq distdir`), which is S030-R1.2. Sub-task 5.2 adds the flag and
	// the config key that fill them.
	distdir           string
	configuredDistdir string
	// distfilesCache is the read-only cache the manifest step symlinks already
	// downloaded distfiles from, under the name `overlay manifest` uses for the
	// same thing (S030-R1.3). Empty disables prepopulation entirely, and empty
	// is this struct's DEFAULT on purpose: the CLI layer owns the
	// /var/cache/distfiles default, so a sweeper built inside a test reads no
	// directory that test did not name.
	distfilesCache string
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

// withSweeperDistdir supplies the two configurable rungs of the distdir
// precedence: explicit is the --distdir flag, configured is autoupdate.distdir
// from the config file. Leaving both empty — what every production caller does
// until sub-task 5.2 wires the flag and the key — makes Resolve fall through to
// the DISTDIR the host itself names.
func withSweeperDistdir(explicit, configured string) sweeperOption {
	return func(s *sweeper) {
		s.distdir = explicit
		s.configuredDistdir = configured
	}
}

// withSweeperDistfilesCache supplies the read-only distfiles cache the manifest
// step prepopulates the working distdir from. Empty — the default — skips
// prepopulation, and so does a cache that resolves to the distdir itself, which
// is what the common production case does: the host's DISTDIR and
// /var/cache/distfiles are the same directory, so there is nothing to link.
func withSweeperDistfilesCache(dir string) sweeperOption {
	return func(s *sweeper) { s.distfilesCache = dir }
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
//
// # The distdir is resolved now, and the order around it is not free
//
// The directory below is the host's real DISTDIR unless something named another
// one, so it is shared with the system package manager and with every other
// worker of this sweep (the batch runs its packages concurrently, see
// ExecuteOverlaySweep). That is what the steps around pkgdev are for, and they
// have to happen in this order (S030-D3/D4; the reasoning lives on each function
// in internal/common/distfiles, since no signature can enforce it):
//
//	resolve -> lock -> quarantine -> record -> fetch (auth prefetch, pkgdev)
//	        -> on failure: cleanup -> release -> cleanup the directory
//
// Lock BEFORE quarantine, because quarantine's look-then-move has a window the
// lock is what closes. Record AFTER quarantine, because a name we have just
// moved aside is absent, and the file that appears under it next is ours to
// clean up. Release AFTER the cleanup, because releasing earlier reopens the
// window the record depends on.
func (s *sweeper) runManifest(pkg string, versions ...string) error {
	// Parse package name
	category, pkgName, ok := splitPkgAtom(pkg)
	if !ok {
		return fmt.Errorf("invalid package name format: %s", pkg)
	}

	// Package directory pkgdev operates in (it discovers the ebuild itself).
	pkgDir := filepath.Join(s.overlayPath, category, pkgName)

	// A distdir backed by a disk, resolved and never invented: the --distdir
	// flag, then autoupdate.distdir, then the DISTDIR the host's own package
	// manager names (S030-R1.1, S030-R1.2). What stood here created a temporary
	// directory instead, and on the machine the defect was measured on /tmp is a
	// 31 GB tmpfs — so every distfile a bump fetched went into RAM, several
	// packages at a time.
	//
	// The reasoning that comment carried is still true of the FILE and is now
	// carried by the quarantine below: an upstream bump's distfile is a new name,
	// absent from any cache AND from the package's current Manifest, so nothing
	// can vouch for whatever is sitting under it. That was never a reason for the
	// directory to be temporary — it is a reason to check the file.
	dir, err := resolveDistdir(s.distdir, s.configuredDistdir)
	if err != nil {
		// dir.Path is the directory that could not be prepared. It is carried
		// for the diagnostic and is never used as a directory (S030-R1.4).
		return fmt.Errorf("%w: distdir %s: %w", ErrManifestFailed, dir.Path, err)
	}
	// Removes the directory only when THIS run created it, so the host's DISTDIR
	// — and any directory an operator named — survives the run (S030-R1.5).
	// Registered first, so it runs last: after the locks are released.
	defer dir.Cleanup()
	distdir := dir.Path

	// Two lists, and they are deliberately not the same list. manifestNames is
	// what the package's CURRENT Manifest already vouches for; expected is what
	// the versions being manifested still need.
	manifestNames := distfiles.ParseManifestDistFilenames(filepath.Join(pkgDir, "Manifest"))
	expected := s.expectedDistfiles(pkg, pkgDir, pkgName, manifestNames, versions)

	// Lock first: everything below reads or writes those names, and both the
	// quarantine and the record/cleanup pair have a window between looking and
	// acting. The claim is all-or-nothing and is held for the whole pkgdev
	// invocation (S030-R2.4).
	lock, err := distfiles.LockFetch(distdir, expected)
	if err != nil {
		return fmt.Errorf("%w: claiming the distfiles for %s in %s: %w", ErrManifestFailed, pkg, distdir, err)
	}
	defer lock.Release()

	// Quarantine: a distfile present under a name the current Manifest does not
	// list cannot be verified by anything, and a fetch killed midway leaves
	// exactly that — under the FINAL name, because portage's fetcher writes
	// straight to it (S030-R2.2). Moved aside, never deleted: the directory is
	// the host's (S030-R2.5). A name the Manifest DOES list is left alone, which
	// is S030-R2.1's reuse.
	moved, qErr := distfiles.Quarantine(distdir, manifestNames, expected)
	// Reported even when the call failed: what moved still moved.
	s.reportQuarantined(pkg, distdir, moved)
	if qErr != nil {
		// Fatal by contract: the file this was asked to move aside is still
		// sitting there, and pkgdev must not digest it.
		return fmt.Errorf("%w: quarantining unverifiable distfiles in %s: %w", ErrManifestFailed, distdir, qErr)
	}

	// Prepopulate from the read-only cache (--distfiles-cache /
	// autoupdate.distfiles_cache), which is S030-R2.1's reuse across two
	// directories rather than within one.
	//
	// The position is the contract, not a preference. AFTER the quarantine,
	// because quarantine moves aside every expected name it cannot verify and a
	// symlink placed first would be moved aside as exactly that. BEFORE the
	// record, because the record is the list of expected names ABSENT right now
	// — a link this run placed must already be there when that snapshot is
	// taken, or a later failure would "clean up" a link pointing into a cache
	// that is not ours (see internal/common/distfiles/cleanup.go).
	//
	// In the common production case this is a no-op: the resolved distdir IS
	// /var/cache/distfiles, ResolveCache refuses a cache equal to the distdir,
	// and nothing is linked. It earns its keep when an operator points --distdir
	// somewhere else and keeps the machine's own cache readable.
	if cacheDir := distfiles.ResolveCache(s.distfilesCache, distdir); cacheDir != "" && len(expected) > 0 {
		s.reportPrepopulated(pkg, cacheDir, distfiles.PrepopulateFromCache(distdir, cacheDir, expected))
	}

	// Record what a failed fetch may take away: the expected names that are
	// ABSENT right now. Everything else in this directory belongs to somebody
	// else (S030-R2.3).
	scope, err := distfiles.RecordFetchScope(distdir, expected)
	if err != nil {
		return fmt.Errorf("%w: recording the fetch scope in %s: %w", ErrManifestFailed, distdir, err)
	}

	// Serial-gated packages: their distfile cannot be fetched by pkgdev from
	// SRC_URI, so pre-populate the distdir by submitting the vendor's download
	// form with the serial. pkgdev then digests the local file. A package
	// without fetch instructions is a no-op; a configured-but-failing fetch
	// aborts here with a clear, serial-free error.
	//
	// It sits HERE, and it used to sit before all of the above, because the
	// directory it writes into is no longer private. Inside the lock, so no
	// other worker is writing the same name; after the quarantine, so it cannot
	// land on top of an unverifiable leftover; after the record, so a run that
	// fails later takes its own download away again instead of leaving an
	// undigested file in the host's DISTDIR.
	for _, version := range versions {
		if err := s.prefetchAuthDistfile(pkg, version, distdir); err != nil {
			s.cleanupFailedFetch(pkg, distdir, scope)
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
		// Before the error goes anywhere: take away whatever this run created
		// under a name that was absent when it started. A truncated distfile
		// left behind is what the next run would digest (S030-R2.3).
		s.cleanupFailedFetch(pkg, distdir, scope)
		// The message is unchanged, byte for byte — existing tests pin it and an
		// operator reads it. What is added is structure AROUND it, so the caller
		// can classify the failure with errors.As instead of re-deriving state
		// that has already moved on (S030-D6).
		return &manifestRunError{
			Distdir:  distdir,
			Expected: expected,
			Err:      fmt.Errorf("command failed: %w\nOutput: %s", runErr, sc.Captured()),
		}
	}

	return nil
}

// manifestRunError is a failed `pkgdev manifest` together with the state it ran
// in: the directory it was given, and the distfile names this run knew it would
// need.
//
// It exists because that state is not recoverable after the fact. Both are
// derived inside runManifest — the distdir from a precedence that consults the
// host, the names from the package's Manifest and the ebuilds on disk — so a
// caller handed a bare error would have to derive them again, and would then be
// inspecting a DIFFERENT directory from the one that failed. The gate in
// runManifestWithFix reads them with errors.As.
//
// Error() delegates, so the message is byte-identical to the one this step has
// always produced. Unwrap keeps errors.Is/errors.As reaching the exec failure
// underneath.
type manifestRunError struct {
	// Distdir is the resolved directory pkgdev was given as --distdir.
	Distdir string
	// Expected are the distfile names this run derived for the versions being
	// manifested. It is legitimately empty when nothing could be derived — see
	// expectedDistfiles — and every consumer must treat it as "no evidence"
	// rather than "no distfiles".
	Expected []string
	// Err is the failure exactly as this step has always built it.
	Err error
}

func (e *manifestRunError) Error() string {
	if e == nil || e.Err == nil {
		return ErrManifestFailed.Error()
	}
	return e.Err.Error()
}

func (e *manifestRunError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Err
}

// reportQuarantined says which distfiles were moved aside, one line each.
//
// internal/common/distfiles is a library and never logs; it returns the names
// and this is the layer that reports them. The report is what makes a rename
// auditable, and one name is enough to act on: the quarantine name carries the
// original filename as its prefix, so it says both what was moved and where it
// went, in a directory that holds thousands of files.
func (s *sweeper) reportQuarantined(pkg, distdir string, moved []string) {
	for _, name := range moved {
		msg := fmt.Sprintf("quarantined an unverifiable distfile before manifesting %s: %s (in %s)", pkg, name, distdir)
		logger.Warn("%s", msg)
		s.reporter.Log("warn", msg)
	}
}

// reportPrepopulated says how many distfiles were reused from the cache instead
// of downloaded, and from where.
//
// Silent when nothing was linked: "reused 0" on every package of a 200-package
// sweep is noise that hides the lines that matter. internal/common/distfiles is
// a library and never logs — it returns the count and this is the layer that
// reports it, exactly as with reportQuarantined.
func (s *sweeper) reportPrepopulated(pkg, cacheDir string, reused int) {
	if reused <= 0 {
		return
	}
	msg := fmt.Sprintf("reused %d distfile(s) from %s while manifesting %s", reused, cacheDir, pkg)
	logger.Info("%s", msg)
	s.reporter.Log("info", msg)
}

// cleanupFailedFetch removes the artefacts this run created for a fetch that
// failed, and reports them.
//
// It is called from the failure branches ONLY, and the name says so because
// nothing in the type can enforce it: after a success those same names are the
// distfiles pkgdev has just fetched and digested, and removing them would delete
// verified files out of the host's DISTDIR (S030-R2.1).
//
// Nothing here can abort anything — the package has already failed. A removal
// that could not be done is still said out loud rather than swallowed, because
// what is left behind in a shared directory is an operator's problem.
func (s *sweeper) cleanupFailedFetch(pkg, distdir string, scope distfiles.FetchScope) {
	removed, err := scope.CleanupFailedFetch()
	for _, name := range removed {
		msg := fmt.Sprintf("removed the incomplete distfile %s the failed manifest step for %s left in %s", name, pkg, distdir)
		logger.Warn("%s", msg)
		s.reporter.Log("warn", msg)
	}
	if err != nil {
		msg := fmt.Sprintf("could not remove every distfile the failed manifest step for %s left in %s: %v", pkg, distdir, err)
		logger.Warn("%s", msg)
		s.reporter.Log("warn", msg)
	}
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

// expectedDistfiles returns the distfile names the versions about to be
// manifested are expected to need — the `expected` list Quarantine, LockFetch
// and RecordFetchScope all take.
//
// # Why this is a derivation and not a lookup
//
// The authoritative answer is the new ebuild's SRC_URI, and reading it means
// expanding bash: ${P}, ${MY_P}, $(ver_cut …), the "-> renamed.tar.gz" arrow,
// USE-conditional groups, and variables an eclass sets. pkgdev knows the answer
// because it asks portage's own parser — and it only knows it once it runs,
// which is after the moment every caller here needs it. Nothing in this
// repository expands SRC_URI (internal/common/ebuild parses paths, and
// ebuild_meta.go extracts the raw text with its ${…} intact), and a
// half-correct expander would be worse than none: a name invented here is a name
// Quarantine moves a real file out from under.
//
// So it derives from what is already known, from two sources that name only
// files this package is itself about to write or fetch:
//
//  1. The authenticated-fetch filename, when the package's [meta] block
//     configures one. That is exact rather than guessed — it is the same
//     template prefetchAuthDistfile resolves and writes, just above.
//  2. The current Manifest's own DIST names, with the version of an ebuild
//     still sitting in the directory replaced by the version being manifested.
//     That covers the ordinary ${P} shape — zed-0.209.4.tar.gz becomes
//     zed-0.212.0.tar.gz — which is the shape the defect this story fixes was
//     measured on.
//
// # What it misses, and why missing is safe
//
// An upstream that renames its archive between releases (a changed tag scheme, a
// per-release hash, a SRC_URI arrow) produces a name this cannot guess, and the
// name it does produce then simply never exists. Every consumer is a no-op on a
// name that is not there: Quarantine finds nothing to move, RecordFetchScope
// records an absence nothing fills, LockFetch claims a name nobody contends,
// and the classifier's zero-length check has nothing to inspect. R2.1-R2.3
// therefore protect the common case and stand down on the rest — less
// protection, never a wrong action.
//
// A guessed name can also never authorise a deletion of something that was
// already there: RecordFetchScope records only names that are ABSENT when the
// step begins, so a name that happens to match a real, pre-existing distfile is
// excluded by the very check that would otherwise remove it.
//
// Deliberately NOT included: the current Manifest's names as they stand. A file
// already listed there is checksum-protected — pkgdev compares it and refetches
// on a mismatch — which is exactly why Quarantine leaves such a file alone
// (S030-D3, case 2). Adding them would widen the lock and the removable set for
// no protection gained.
func (s *sweeper) expectedDistfiles(pkg, pkgDir, pkgName string, manifestNames, versions []string) []string {
	seen := make(map[string]bool)
	var out []string
	add := func(name string) {
		if name == "" || seen[name] {
			return
		}
		seen[name] = true
		out = append(out, name)
	}

	// (1) The authenticated fetch's own filename. A malformed [meta] block is
	// not reported here: prefetchAuthDistfile runs minutes later on the same
	// config and fails the package with the message that belongs to it.
	if cfg, ok := s.configs[pkg]; ok {
		if spec, enabled, err := parseAuthFetchSpec(cfg.Meta); err == nil && enabled {
			for _, version := range versions {
				add(spec.resolvedFilename(version))
			}
		}
	}

	// (2) The current DIST names with an on-disk version substituted.
	olds := otherEbuildVersions(pkgDir, pkgName, versions)
	for _, name := range manifestNames {
		for _, version := range versions {
			add(substituteVersion(name, olds, baseVersion(version)))
		}
	}

	// Sorted so a failure reports the same list twice and a test can pin it.
	// LockFetch sorts its own copy for deadlock-freedom regardless.
	sort.Strings(out)
	return out
}

// otherEbuildVersions lists the versions of the ebuilds sitting in pkgDir, minus
// the ones being manifested. On the apply path that is the version being
// replaced — the one whose distfile the current Manifest lists — which is the
// substitution's whole input.
//
// A directory it cannot read yields nothing, and nothing is the safe answer: no
// substitution is attempted, so no name is invented. Live ebuilds are skipped
// because a 9999 version names no distfile at all.
func otherEbuildVersions(pkgDir, pkgName string, versions []string) []string {
	entries, err := os.ReadDir(pkgDir)
	if err != nil {
		return nil
	}
	skip := make(map[string]bool, len(versions))
	for _, v := range versions {
		skip[baseVersion(v)] = true
	}
	seen := make(map[string]bool)
	var out []string
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		if !strings.HasSuffix(name, ".ebuild") || !strings.HasPrefix(name, pkgName+"-") {
			continue
		}
		v := baseVersion(strings.TrimSuffix(strings.TrimPrefix(name, pkgName+"-"), ".ebuild"))
		if v == "" || strings.Contains(v, "9999") || skip[v] || seen[v] {
			continue
		}
		seen[v] = true
		out = append(out, v)
	}
	return out
}

// baseVersion strips the -rN revision from a PV. A distfile is named for the
// upstream release and not for the ebuild's revision of it: bumping foo-1.2.3-r1
// does not rename foo-1.2.3.tar.gz.
func baseVersion(version string) string {
	return revisionSuffixRegex.ReplaceAllString(version, "")
}

// substituteVersion rewrites one DIST name for a new version, and returns ""
// when it cannot do so with any confidence.
//
// The LONGEST old version that occurs in the name wins, so a directory holding
// both foo-1 and foo-1.2.11 substitutes the version that actually names the file
// rather than a digit inside it. An empty result is the ordinary outcome for a
// name no on-disk version appears in, and for a substitution that would change
// nothing.
func substituteVersion(distName string, olds []string, newVersion string) string {
	if distName == "" || newVersion == "" {
		return ""
	}
	best, result := "", ""
	for _, old := range olds {
		if old == "" || old == newVersion || len(old) <= len(best) {
			continue
		}
		if out, ok := replaceBounded(distName, old, newVersion); ok {
			best, result = old, out
		}
	}
	return result
}

// replaceBounded replaces every occurrence of old in s that is not glued to a
// longer number, and reports whether it replaced any.
//
// The boundary is what stops a short version from manufacturing a filename out
// of a stray digit: in "zlib-1.2.11.tar.gz" the version "1" occurs three times
// and every one of them is either preceded by a digit or a dot, or followed by a
// digit, so none of them counts — while "1.2.11" is preceded by "-" and followed
// by "." and does.
func replaceBounded(s, old, replacement string) (string, bool) {
	if old == "" {
		return s, false
	}
	var b strings.Builder
	replaced := false
	for i := 0; i < len(s); {
		if strings.HasPrefix(s[i:], old) && boundedAt(s, i, len(old)) {
			b.WriteString(replacement)
			i += len(old)
			replaced = true
			continue
		}
		b.WriteByte(s[i])
		i++
	}
	if !replaced {
		return s, false
	}
	return b.String(), true
}

// boundedAt reports whether the match of length n at index i in s stands on its
// own rather than inside a longer number: nothing numeric may lead into it, and
// no digit may follow it. A dot may follow, because "1.2.3" in "foo-1.2.3.tar.gz"
// is exactly the case this has to accept.
func boundedAt(s string, i, n int) bool {
	if i > 0 && (isDigitByte(s[i-1]) || s[i-1] == '.') {
		return false
	}
	if j := i + n; j < len(s) && isDigitByte(s[j]) {
		return false
	}
	return true
}

// isDigitByte reports whether b is an ASCII digit. Versions and the filenames
// built from them are ASCII here, so a byte test is the whole of it.
func isDigitByte(b byte) bool { return b >= '0' && b <= '9' }

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
	concurrency       int
	execCommand       func(ctx context.Context, name string, arg ...string) *exec.Cmd
	reporter          tui.Reporter
	configs           map[string]PackageConfig
	distdir           string
	configuredDistdir string
	distfilesCache    string
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

// WithSweepDistdir supplies the two configurable rungs of the distdir
// precedence: explicit is the --distdir flag, configured is autoupdate.distdir
// from the config file (S030-R1.3). Both empty is the production default and
// resolves to the DISTDIR the host itself names.
func WithSweepDistdir(explicit, configured string) SweepOption {
	return func(o *sweepOptions) {
		o.distdir = explicit
		o.configuredDistdir = configured
	}
}

// WithSweepDistfilesCache supplies the read-only cache the manifest step reuses
// already downloaded distfiles from (--distfiles-cache /
// autoupdate.distfiles_cache). Empty disables the lookup.
func WithSweepDistfilesCache(dir string) SweepOption {
	return func(o *sweepOptions) { o.distfilesCache = dir }
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
		withSweeperDistdir(o.distdir, o.configuredDistdir),
		withSweeperDistfilesCache(o.distfilesCache),
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
			// Why the directive on the next line is safe: the sweeper was
			// built with THIS ctx (withSweeperContext above), so runManifest
			// derives its deadline from it and a cancellation still kills the
			// spawned pkgdev. The linter cannot see a context carried on a
			// struct, which is the same shape Applier uses.
			//
			// This justification is deliberately NOT written as a second
			// `nolint` line. A directive golangci-lint recognises has no space
			// after the slashes and separates its reason with `//`, so a
			// `// nolint:... —` line suppresses nothing; leaving one here would
			// read as the suppression and invite deleting the inline directive
			// that actually does the work.
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
