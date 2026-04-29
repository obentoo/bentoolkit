// Package overlay provides business logic for overlay management operations.
package overlay

import (
	"bytes"
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
	// Reporter receives lifecycle events as workers process targets.
	// Nil means silent (no progress output). The CLI typically wires a
	// TUI or log reporter here.
	Reporter ProgressReporter
	// Ctx, when non-nil, is propagated to the pkgdev sub-processes via
	// exec.CommandContext so callers can cancel an in-flight run (e.g.
	// on SIGINT). Nil is treated as context.Background().
	Ctx context.Context
}

// ProgressReporter receives manifest-regeneration lifecycle events.
//
// Implementations must be safe to call from multiple goroutines. Workers
// invoke Start/Done concurrently as they pick up and finish targets.
type ProgressReporter interface {
	// Total announces the total number of targets and the desired worker
	// concurrency. Called once before any Start.
	Total(n, jobs int)
	// Start signals that worker `worker` (0-indexed) has picked up
	// targets[i] and is about to invoke pkgdev for it.
	Start(i, worker int, target ManifestUpdate)
	// Done signals that targets[i] finished. ok==false carries the failure
	// reason (errMsg) and the captured pkgdev output (output) for display.
	Done(i, worker int, target ManifestUpdate, ok bool, errMsg, output string)
	// Finish is called once after all targets have completed, regardless
	// of individual outcomes.
	Finish()
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
// pkgdev output is captured per job and surfaced through opts.Reporter,
// which receives Start/Done events. If Reporter is nil, the call is silent
// — only the returned []ManifestUpdate carries success/error information.
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

	if _, err := exec.LookPath("pkgdev"); err != nil {
		for i := range updates {
			updates[i].Success = false
			updates[i].Error = ErrPkgdevNotFound.Error()
		}
		return updates
	}

	distdir, cleanup, err := resolveDistdir(opts.Distdir)
	if err != nil {
		for i := range updates {
			updates[i].Success = false
			updates[i].Error = err.Error()
		}
		return updates
	}
	defer cleanup()

	jobs := opts.Jobs
	if jobs <= 0 {
		jobs = DefaultManifestJobs
	}
	if jobs > len(updates) {
		jobs = len(updates)
	}

	ctx := opts.Ctx
	if ctx == nil {
		ctx = context.Background()
	}

	if opts.Reporter != nil {
		opts.Reporter.Total(len(updates), jobs)
		defer opts.Reporter.Finish()
	}

	queue := make(chan int, len(updates))
	for i := range updates {
		queue <- i
	}
	close(queue)

	var wg sync.WaitGroup
	for w := 0; w < jobs; w++ {
		wg.Add(1)
		go func(worker int) {
			defer wg.Done()
			for i := range queue {
				runOneManifest(ctx, overlayPath, distdir, &updates[i], i, worker, opts)
			}
		}(w)
	}
	wg.Wait()

	return updates
}

// runOneManifest performs the backup/regenerate/rollback dance for a single
// target and writes the outcome back into *u. It is invoked from a worker
// goroutine; concurrent calls write to distinct slice indices so no lock is
// required for the result. The reporter, if any, is responsible for being
// goroutine-safe.
func runOneManifest(ctx context.Context, overlayPath, distdir string, u *ManifestUpdate, i, worker int, opts *ManifestOptions) {
	if opts.Reporter != nil {
		opts.Reporter.Start(i, worker, *u)
	}

	pkgPath := filepath.Join(overlayPath, u.Category, u.Package)
	manifestPath := filepath.Join(pkgPath, "Manifest")

	var backupPath string
	if !opts.Keep {
		if _, statErr := os.Stat(manifestPath); statErr == nil {
			backupPath = manifestPath + ".bak"
			if mvErr := os.Rename(manifestPath, backupPath); mvErr != nil {
				u.Success = false
				u.Error = fmt.Sprintf("failed to back up Manifest: %v", mvErr)
				if opts.Reporter != nil {
					opts.Reporter.Done(i, worker, *u, false, u.Error, "")
				}
				return
			}
		}
	}

	var combined bytes.Buffer
	cmd := exec.CommandContext(ctx, "pkgdev", "manifest", "--distdir", distdir)
	cmd.Dir = pkgPath
	cmd.Stdout = &combined
	cmd.Stderr = &combined

	runErr := cmd.Run()
	if runErr != nil {
		u.Success = false
		u.Error = runErr.Error()
		if backupPath != "" {
			if rbErr := os.Rename(backupPath, manifestPath); rbErr != nil {
				u.Error = fmt.Sprintf("%s; rollback failed: %v", u.Error, rbErr)
			}
		}
		if opts.Reporter != nil {
			opts.Reporter.Done(i, worker, *u, false, u.Error, combined.String())
		}
		return
	}

	if backupPath != "" {
		_ = os.Remove(backupPath)
	}
	u.Success = true
	if opts.Reporter != nil {
		opts.Reporter.Done(i, worker, *u, true, "", combined.String())
	}
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

// resolveDistdir returns the directory pkgdev should use for downloads,
// alongside a cleanup function. When userDir is empty, a temporary directory
// is created and the cleanup removes it. When userDir is set, it is expanded
// (~ and absolute path) and created if missing; the cleanup is a no-op so the
// directory persists across runs.
func resolveDistdir(userDir string) (string, func(), error) {
	noop := func() {}

	if userDir == "" {
		tmp, err := os.MkdirTemp("", "bentoo-distfiles-")
		if err != nil {
			return "", noop, fmt.Errorf("failed to create temp distdir: %w", err)
		}
		return tmp, func() { _ = os.RemoveAll(tmp) }, nil
	}

	expanded := userDir
	if strings.HasPrefix(expanded, "~") {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", noop, fmt.Errorf("failed to expand %q: %w", userDir, err)
		}
		expanded = filepath.Join(home, strings.TrimPrefix(expanded, "~"))
	}
	abs, err := filepath.Abs(expanded)
	if err != nil {
		return "", noop, fmt.Errorf("failed to resolve distdir %q: %w", userDir, err)
	}
	if err := os.MkdirAll(abs, 0o755); err != nil {
		return "", noop, fmt.Errorf("failed to create distdir %q: %w", abs, err)
	}
	return abs, noop, nil
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
