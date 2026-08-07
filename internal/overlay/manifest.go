// Package overlay provides business logic for overlay management operations.
package overlay

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

	"github.com/obentoo/bentoolkit/internal/common/config"
	"github.com/obentoo/bentoolkit/internal/common/distfiles"
	"github.com/obentoo/bentoolkit/internal/common/tui"
)

// execCommand and lookPath are package-level seams over os/exec so tests can
// stub pkgdev discovery and invocation without a real binary. Both default to
// the real functions.
var (
	execCommand = exec.CommandContext
	lookPath    = exec.LookPath
)

// Errors for manifest operations.
var (
	ErrPkgdevNotFound       = errors.New("pkgdev not found; install dev-util/pkgdev")
	ErrManifestNoTargets    = errors.New("no packages found to update")
	ErrManifestInvalidScope = errors.New("invalid manifest scope")
)

// DefaultManifestJobs is the default number of pkgdev workers run in parallel
// when ManifestOptions.Jobs is not set (or set to a non-positive value).
const DefaultManifestJobs = 10

// DefaultDistfilesCache is the system path queried, when readable, to skip
// re-downloading distfiles already present in the portage cache. Used as the
// default for ManifestOptions.DistfilesCache.
//
// The value lives in internal/common/distfiles alongside the resolution logic
// that consumes it; it is re-exported here under the name the CLI has always
// used for the --distfiles-cache flag default.
const DefaultDistfilesCache = distfiles.DefaultCache

// ManifestScope identifies one or more packages to regenerate Manifests for.
//
// Resolution rules:
//   - Empty Category and Package: every package in the overlay.
//   - Non-empty Category, empty Package: every package in that category.
//   - Both set: that single package.
type ManifestScope struct {
	Category string
	Package  string
}

// ManifestOptions controls Manifest regeneration behavior.
type ManifestOptions struct {
	// Keep, if true, leaves the existing Manifest in place and lets pkgdev
	// reconcile it. By default, the existing Manifest is moved to a backup
	// before regeneration and restored only on failure (clean regen).
	Keep bool
	// DryRun, if true, lists the packages that would be processed without
	// running pkgdev or touching files.
	DryRun bool
	// Distdir, when non-empty, is used as pkgdev's --distdir. The path is
	// expanded (~ and relative paths) and created if missing, and is
	// preserved across runs as a persistent download cache. When empty,
	// a temporary directory is created under os.TempDir() and removed
	// when the run completes.
	Distdir string
	// Jobs is the maximum number of pkgdev invocations to run in parallel.
	// Values <= 0 fall back to DefaultManifestJobs. Internally clamped to
	// the number of targets so we never spin idle workers.
	Jobs int
	// DistfilesCache, when non-empty, points to a read-only distfiles cache
	// (typically /var/cache/distfiles) that is consulted before each pkgdev
	// invocation. For every DIST entry listed in the package's existing
	// Manifest, if a file with the same name exists in this cache, a symlink
	// is created in the working distdir so pkgdev can reuse it instead of
	// downloading. Empty string disables the optimization entirely. The
	// cache is never written to.
	DistfilesCache string
	// Reporter receives lifecycle events as workers process targets.
	// Nil means silent (no progress output) — it is normalized to tui.Noop().
	// The CLI typically wires a live TUI or plain reporter here.
	Reporter tui.Reporter
	// Ctx, when non-nil, is propagated to the pkgdev sub-processes via
	// exec.CommandContext so callers can cancel an in-flight run (e.g.
	// on SIGINT). Nil is treated as context.Background().
	Ctx context.Context
}

// ManifestResult collects per-package results of a regeneration run.
type ManifestResult struct {
	Updates []ManifestUpdate
}

// ParseManifestScope parses a single CLI argument into a ManifestScope.
//
// Accepted forms:
//   - ""                      -> whole overlay
//   - "<category>"            -> all packages in category
//   - "<category>/<package>"  -> single package
func ParseManifestScope(arg string) (ManifestScope, error) {
	arg = strings.TrimSpace(arg)
	if arg == "" {
		return ManifestScope{}, nil
	}
	parts := strings.Split(arg, "/")
	switch len(parts) {
	case 1:
		cat := strings.TrimSpace(parts[0])
		if cat == "" {
			return ManifestScope{}, fmt.Errorf("%w: empty category", ErrManifestInvalidScope)
		}
		return ManifestScope{Category: cat}, nil
	case 2:
		cat := strings.TrimSpace(parts[0])
		pkg := strings.TrimSpace(parts[1])
		if cat == "" || pkg == "" {
			return ManifestScope{}, fmt.Errorf("%w: expected <category>/<package>", ErrManifestInvalidScope)
		}
		return ManifestScope{Category: cat, Package: pkg}, nil
	default:
		return ManifestScope{}, fmt.Errorf("%w: too many '/' separators in %q", ErrManifestInvalidScope, arg)
	}
}

// ResolveManifestTargets expands a scope into the concrete list of packages
// (category/package pairs) present in the overlay.
func ResolveManifestTargets(overlayPath string, scope ManifestScope) ([]ManifestUpdate, error) {
	if overlayPath == "" {
		return nil, ErrOverlayPathNotSet
	}

	if scope.Category != "" && scope.Package != "" {
		pkgDir := filepath.Join(overlayPath, scope.Category, scope.Package)
		if !isPackageDir(pkgDir) {
			return nil, fmt.Errorf("package %s/%s not found in overlay", scope.Category, scope.Package)
		}
		return []ManifestUpdate{{Category: scope.Category, Package: scope.Package}}, nil
	}

	scan, err := ScanOverlay(overlayPath)
	if err != nil {
		return nil, fmt.Errorf("scanning overlay: %w", err)
	}

	var targets []ManifestUpdate
	for _, p := range scan.Packages {
		if scope.Category != "" && p.Category != scope.Category {
			continue
		}
		targets = append(targets, ManifestUpdate{Category: p.Category, Package: p.Package})
	}

	sort.Slice(targets, func(i, j int) bool {
		if targets[i].Category != targets[j].Category {
			return targets[i].Category < targets[j].Category
		}
		return targets[i].Package < targets[j].Package
	})

	if len(targets) == 0 {
		if scope.Category != "" {
			return nil, fmt.Errorf("%w: category %q has no packages", ErrManifestNoTargets, scope.Category)
		}
		return nil, ErrManifestNoTargets
	}

	return targets, nil
}

// RegenerateManifests regenerates Manifest files for the given packages using
// pkgdev. Workers are dispatched in parallel up to opts.Jobs (default
// DefaultManifestJobs); each pkgdev process runs against its own package
// directory and shares the resolved distdir as a download cache.
//
// By default, the existing Manifest is moved aside before pkgdev runs so a
// fresh file is produced (clean regeneration). The backup is restored on
// failure. opts.Keep skips this step.
//
// pkgdev output is captured per job and surfaced through opts.Reporter as
// TaskStart/TaskLine/TaskDone events, bracketed by BatchStart/BatchDone. If
// Reporter is nil it is normalized to tui.Noop(), so the call is silent —
// only the returned []ManifestUpdate carries success/error information.
//
// The returned slice preserves the order of the input targets, even when
// workers complete out of order.
func RegenerateManifests(overlayPath string, targets []ManifestUpdate, opts *ManifestOptions) []ManifestUpdate {
	if opts == nil {
		opts = &ManifestOptions{}
	}

	updates := make([]ManifestUpdate, len(targets))
	copy(updates, targets)

	if len(updates) == 0 {
		return updates
	}

	if opts.DryRun {
		return updates
	}

	// pkgdev discovery short-circuits BEFORE any reporter call: a missing
	// binary marks every target failed without opening a batch, so a nil/Noop
	// or recording reporter sees no events at all.
	if _, err := lookPath("pkgdev"); err != nil {
		for i := range updates {
			updates[i].Success = false
			updates[i].Error = ErrPkgdevNotFound.Error()
		}
		return updates
	}

	// ResolveOrTemp, not Resolve: this command documents in its own --help that
	// an unset --distdir means a throwaway temporary directory, which is what
	// lets it run without sudo. distfiles.Resolve implements the autoupdate
	// path's precedence instead (host DISTDIR by default) and would silently
	// change that promise.
	//
	// The resolved directory carries its own provenance: Cleanup removes it
	// only when this process created it, so a caller-supplied --distdir (which
	// may be the host's real DISTDIR) survives the run.
	dir, err := distfiles.ResolveOrTemp(opts.Distdir)
	if err != nil {
		for i := range updates {
			updates[i].Success = false
			updates[i].Error = err.Error()
		}
		return updates
	}
	defer dir.Cleanup()
	distdir := dir.Path

	// Resolve the distfiles cache once: a missing or unreadable directory
	// silently disables the optimization for the whole run, so workers don't
	// repeatedly stat a path that doesn't exist.
	cacheDir := distfiles.ResolveCache(opts.DistfilesCache, distdir)

	jobs := opts.Jobs
	if jobs <= 0 {
		jobs = DefaultManifestJobs
	}
	if jobs > len(updates) {
		jobs = len(updates)
	}

	ctx := opts.Ctx
	if ctx == nil {
		ctx = context.Background() // SAFE: opts.Ctx is an additive field; nil means "no cancellation requested"
	}

	// Normalize the reporter once so workers can emit unconditionally. A nil
	// reporter becomes a no-op (R3.3), matching the previous silent behavior.
	rep := opts.Reporter
	if rep == nil {
		rep = tui.Noop()
	}
	rep.BatchStart(len(updates))

	queue := make(chan int, len(updates))
	for i := range updates {
		queue <- i
	}
	close(queue)

	var wg sync.WaitGroup
	for w := 0; w < jobs; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range queue {
				runOneManifest(ctx, overlayPath, distdir, cacheDir, &updates[i], opts, rep)
			}
		}()
	}
	wg.Wait()

	okCount, failCount := 0, 0
	for i := range updates {
		if updates[i].Success {
			okCount++
		} else {
			failCount++
		}
	}
	rep.BatchDone(fmt.Sprintf("%d ok, %d failed", okCount, failCount))

	return updates
}

// runOneManifest performs the backup/regenerate/rollback dance for a single
// target and writes the outcome back into *u. It is invoked from a worker
// goroutine; concurrent calls write to distinct slice indices so no lock is
// required for the result. Lifecycle events are emitted through rep, which is
// always non-nil (normalized by the caller) and goroutine-safe.
func runOneManifest(ctx context.Context, overlayPath, distdir, cacheDir string, u *ManifestUpdate, opts *ManifestOptions, rep tui.Reporter) {
	id := u.Category + "/" + u.Package
	rep.TaskStart(id, id)

	pkgPath := filepath.Join(overlayPath, u.Category, u.Package)
	manifestPath := filepath.Join(pkgPath, "Manifest")

	// Snapshot DIST filenames from the existing Manifest before any backup
	// move, so prepopulation works under both --keep and the default flow.
	// On error or missing Manifest, the slice is empty and prepopulation
	// degrades to a no-op.
	var distNames []string
	if cacheDir != "" {
		distNames = distfiles.ParseManifestDistFilenames(manifestPath)
	}

	var backupPath string
	if !opts.Keep {
		if _, statErr := os.Stat(manifestPath); statErr == nil {
			backupPath = manifestPath + ".bak"
			if mvErr := os.Rename(manifestPath, backupPath); mvErr != nil {
				u.Success = false
				u.Error = fmt.Sprintf("failed to back up Manifest: %v", mvErr)
				rep.TaskDone(id, false, u.Error, "")
				return
			}
		}
	}

	// Stream pkgdev output through a StreamCapture: it tails live lines to the
	// reporter while keeping a verbatim copy for the error path (R7.1). Each
	// worker owns its own StreamCapture but they all forward to the same rep,
	// which is goroutine-safe (R7.4).
	sc := tui.NewStreamCapture(rep, id, tui.StreamStdout)
	if cacheDir != "" && len(distNames) > 0 {
		reused := distfiles.PrepopulateFromCache(distdir, cacheDir, distNames)
		u.Reused = reused
		if reused > 0 {
			fmt.Fprintf(sc, "[bentoo] reused %d distfile(s) from %s\n", reused, cacheDir)
		}
	}
	cmd := execCommand(ctx, "pkgdev", "manifest", "--distdir", distdir)
	cmd.Dir = pkgPath
	cmd.Stdout = sc
	cmd.Stderr = sc

	runErr := cmd.Run()
	_ = sc.Close()
	if runErr != nil {
		u.Success = false
		u.Error = runErr.Error()
		if backupPath != "" {
			if rbErr := os.Rename(backupPath, manifestPath); rbErr != nil {
				u.Error = fmt.Sprintf("%s; rollback failed: %v", u.Error, rbErr)
			}
		}
		rep.TaskDone(id, false, u.Error, sc.Captured())
		return
	}

	if backupPath != "" {
		_ = os.Remove(backupPath)
	}
	u.Success = true
	rep.TaskDone(id, true, "", sc.Captured())
}

// RegenerateManifestsForScope is a convenience wrapper that resolves a scope
// and runs RegenerateManifests.
func RegenerateManifestsForScope(cfg *config.Config, scope ManifestScope, opts *ManifestOptions) (*ManifestResult, error) {
	if cfg == nil {
		return nil, ErrOverlayPathNotSet
	}
	overlayPath, err := cfg.GetOverlayPath()
	if err != nil {
		return nil, err
	}
	targets, err := ResolveManifestTargets(overlayPath, scope)
	if err != nil {
		return nil, err
	}
	return &ManifestResult{
		Updates: RegenerateManifests(overlayPath, targets, opts),
	}, nil
}

// FormatManifestResult renders a ManifestResult for display.
func FormatManifestResult(result *ManifestResult, dryRun bool) string {
	var sb strings.Builder

	if result == nil || len(result.Updates) == 0 {
		return "No packages processed"
	}

	if dryRun {
		fmt.Fprintf(&sb, "Dry run: %d package(s) would have Manifest regenerated\n\n", len(result.Updates))
		for _, u := range result.Updates {
			fmt.Fprintf(&sb, "  %s/%s\n", u.Category, u.Package)
		}
		return sb.String()
	}

	var success, failed int
	for _, u := range result.Updates {
		if u.Success {
			success++
		} else {
			failed++
		}
	}

	fmt.Fprintf(&sb, "Manifest regeneration: %d succeeded, %d failed (of %d)\n",
		success, failed, len(result.Updates))

	if failed > 0 {
		sb.WriteString("\nFailures:\n")
		for _, u := range result.Updates {
			if !u.Success {
				fmt.Fprintf(&sb, "  %s/%s: %s\n", u.Category, u.Package, u.Error)
			}
		}
	}

	return sb.String()
}

// isPackageDir reports whether the path looks like a valid package directory
// (exists, is a directory, contains at least one .ebuild file).
func isPackageDir(path string) bool {
	info, err := os.Stat(path)
	if err != nil || !info.IsDir() {
		return false
	}
	entries, err := os.ReadDir(path)
	if err != nil {
		return false
	}
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".ebuild") {
			return true
		}
	}
	return false
}
