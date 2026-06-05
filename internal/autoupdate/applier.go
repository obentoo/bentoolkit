// Package autoupdate provides update application functionality for ebuild autoupdate.
package autoupdate

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/obentoo/bentoolkit/internal/common/ebuild"
	"github.com/obentoo/bentoolkit/internal/common/fileutil"
	"github.com/obentoo/bentoolkit/internal/common/logger"
)

// manifestTimeout bounds a single `pkgdev manifest` invocation. The manifest
// step touches the network (fetching SRC_URI distfiles to digest), so it gets a
// generous-but-finite budget; without it a stalled fetch would hang Apply
// indefinitely.
const manifestTimeout = 5 * time.Minute

// Error variables for applier errors
var (
	// ErrEbuildNotFound is returned when the source ebuild file is not found
	ErrEbuildNotFound = errors.New("source ebuild file not found")
	// ErrManifestFailed is returned when the manifest command fails
	ErrManifestFailed = errors.New("manifest command failed")
	// ErrCompileFailed is returned when the compile test fails
	ErrCompileFailed = errors.New("compile test failed")
	// ErrNoPrivilegeEscalation is returned when neither sudo nor doas is available
	ErrNoPrivilegeEscalation = errors.New("no privilege escalation tool available (sudo or doas)")
	// ErrUserDeclined is returned when user declines the compile confirmation
	ErrUserDeclined = errors.New("user declined compile test")
	// ErrInvalidNewVersion is returned when the detected upstream version cannot
	// be coerced into a well-formed Gentoo PV (e.g. it carries a tag prefix that
	// survives normalization, or is not a version at all).
	ErrInvalidNewVersion = errors.New("invalid new version for ebuild")
	// ErrObsoletePending is returned (wrapped) when a pending entry no longer
	// matches the live overlay: the package was removed, or the overlay is
	// already at/beyond the target version. The entry is pruned and the outcome
	// is reported as obsolete, not as a failure.
	ErrObsoletePending = errors.New("obsolete pending entry")
)

// ApplyResult represents the result of applying an update.
type ApplyResult struct {
	// Package is the full package name (category/package)
	Package string
	// OldVersion is the version before the update
	OldVersion string
	// NewVersion is the version after the update
	NewVersion string
	// Success indicates whether the apply operation succeeded
	Success bool
	// Error contains any error that occurred during application
	Error error
	// LogPath is the path to the compile log if compilation failed
	LogPath string
	// CleanedOldVersion is the previous version whose ebuild was removed when
	// --clean is set; empty when clean was off, a no-op (same version), or the
	// old ebuild was already absent.
	CleanedOldVersion string
	// CleanWarning records a non-fatal failure of the --clean step (the update
	// itself still succeeded). Empty on success.
	CleanWarning string
	// Obsolete indicates the pending entry no longer corresponds to anything to
	// apply: the package was removed from the overlay, or the overlay is already
	// at/beyond the target version. The entry is pruned from pending.json and
	// this is NOT counted as a failure (Success stays false, Error stays nil).
	Obsolete bool
	// ObsoleteReason explains, in user-facing terms, why the entry was deemed
	// obsolete. Empty unless Obsolete is true.
	ObsoleteReason string
}

// Applier handles update application for packages.
// It coordinates between pending list and file system operations.
type Applier struct {
	// overlayPath is the path to the overlay directory
	overlayPath string
	// pending manages pending updates
	pending *PendingList
	// logsDir is the directory for storing compile logs
	logsDir string
	// confirmFunc is a function to prompt for user confirmation (injectable for testing)
	confirmFunc func(prompt string) bool
	// execCommand is a function to create exec.Cmd bound to a context
	// (injectable for testing). It defaults to exec.CommandContext so a
	// cancelled context kills the spawned manifest/compile process.
	execCommand func(ctx context.Context, name string, arg ...string) *exec.Cmd
	// ctx is the parent context for all spawned external commands. It is set
	// via WithApplierContext and originates in cmd/ (signal.NotifyContext), so a
	// SIGINT or deadline kills in-flight ebuild/compile processes. Defaults to
	// context.Background().
	ctx context.Context
	// pendingDeleteFn is the function Apply invokes to remove a package from
	// pending.json after the full success path (R3.1). It defaults to
	// a.pending.Delete and is overridable via WithApplierPendingDeleteFunc
	// purely for tests that need to simulate a Delete failure (R3.4).
	// Production callers never supply this option.
	pendingDeleteFn func(pkg string) error
	// clean, when true, makes a successful Apply remove the previous version's
	// ebuild and regenerate the Manifest so only the freshly created version
	// remains. Set via WithApplierClean (the --clean / -c CLI flag).
	clean bool
	// configs holds the per-package autoupdate configuration, keyed by
	// "category/package". It is consulted only for the optional [meta] block
	// that drives an authenticated distfile fetch (serial-gated downloads);
	// packages without it follow the normal pkgdev-from-SRC_URI path. Set via
	// WithApplierPackagesConfig; nil disables authenticated fetching entirely.
	configs map[string]PackageConfig
}

// ApplierOption is a functional option for configuring Applier
type ApplierOption func(*Applier)

// WithApplierPendingList sets a custom pending list for the applier
func WithApplierPendingList(pending *PendingList) ApplierOption {
	return func(a *Applier) {
		a.pending = pending
	}
}

// WithLogsDir sets a custom logs directory for the applier
func WithLogsDir(dir string) ApplierOption {
	return func(a *Applier) {
		a.logsDir = dir
	}
}

// WithConfirmFunc sets a custom confirmation function for the applier
func WithConfirmFunc(fn func(prompt string) bool) ApplierOption {
	return func(a *Applier) {
		a.confirmFunc = fn
	}
}

// WithExecCommand sets a custom context-aware exec.Command function for testing.
// The function mirrors exec.CommandContext so injected commands also observe
// context cancellation.
func WithExecCommand(fn func(ctx context.Context, name string, arg ...string) *exec.Cmd) ApplierOption {
	return func(a *Applier) {
		a.execCommand = fn
	}
}

// WithApplierContext sets the parent context for the applier. The context is
// threaded into every spawned external command (pkgdev manifest, compile test),
// so cancelling it (e.g. on SIGINT or a deadline) kills in-flight processes.
// A nil context is ignored, leaving the default context.Background().
func WithApplierContext(ctx context.Context) ApplierOption {
	return func(a *Applier) {
		if ctx != nil {
			a.ctx = ctx
		}
	}
}

// WithApplierPendingDeleteFunc overrides the function Apply invokes to remove
// a package from pending.json after a successful apply (R3.1). The default is
// a.pending.Delete. This option exists for tests that need to simulate a
// Delete failure (R3.4); a nil fn is ignored.
func WithApplierPendingDeleteFunc(fn func(pkg string) error) ApplierOption {
	return func(a *Applier) {
		if fn != nil {
			a.pendingDeleteFn = fn
		}
	}
}

// WithApplierClean enables removal of the previous version's ebuild after a
// successful apply, leaving only the newly created version (and pruning the
// Manifest's now-orphaned distfile entries). Mirrors the --clean / -c CLI flag.
func WithApplierClean(clean bool) ApplierOption {
	return func(a *Applier) {
		a.clean = clean
	}
}

// WithApplierPackagesConfig supplies the per-package autoupdate config so the
// applier can honour a package's [meta] authenticated-fetch instructions before
// running the manifest step. A nil config (or one without a matching package)
// leaves the normal pkgdev-from-SRC_URI behaviour unchanged.
func WithApplierPackagesConfig(cfg *PackagesConfig) ApplierOption {
	return func(a *Applier) {
		if cfg != nil {
			a.configs = cfg.Packages
		}
	}
}

// NewApplier creates a new applier instance for the given overlay.
// It initializes the pending list and logs directory.
func NewApplier(overlayPath, configDir string, opts ...ApplierOption) (*Applier, error) {
	logsDir := filepath.Join(configDir, "logs")

	applier := &Applier{
		overlayPath: overlayPath,
		logsDir:     logsDir,
		confirmFunc: defaultConfirmFunc,
		execCommand: exec.CommandContext,
		ctx:         context.Background(), // SAFE: default parent; replaced by WithApplierContext when cmd/ wires signal.NotifyContext
	}

	// Apply options first
	for _, opt := range opts {
		opt(applier)
	}

	// Initialize pending list if not provided
	if applier.pending == nil {
		pending, err := NewPendingList(configDir)
		if err != nil {
			return nil, fmt.Errorf("failed to initialize pending list: %w", err)
		}
		applier.pending = pending
	}

	// Resolve the pending-delete sink: tests can inject a failing variant via
	// WithApplierPendingDeleteFunc; production defaults to the live pending
	// list's Delete method. Bound only after applier.pending is initialised.
	if applier.pendingDeleteFn == nil {
		applier.pendingDeleteFn = applier.pending.Delete
	}

	// Ensure logs directory exists
	if err := os.MkdirAll(applier.logsDir, 0o750); err != nil {
		return nil, fmt.Errorf("failed to create logs directory: %w", err)
	}

	return applier, nil
}

// Apply applies a pending update for a package.
// It copies the ebuild to the new version and runs the manifest command.
// If compile is true, it also runs a compile test with elevated privileges.
//
// The result is returned via a named value so a single deferred cleanup can
// observe whichever error the function ultimately surfaces (result.Error is
// kept in lockstep with the returned error on every path).
func (a *Applier) Apply(pkg string, compile bool) (result *ApplyResult, _ error) {
	result = &ApplyResult{
		Package: pkg,
	}

	// Get pending update
	update, found := a.pending.Get(pkg)
	if !found {
		result.Error = ErrPackageNotInPending
		return result, result.Error
	}

	result.OldVersion = update.CurrentVersion

	// Upstream version detection can carry a leading tag prefix (e.g. the git
	// tag "v9.2.0588"). A Gentoo ebuild filename requires a bare PV, so strip
	// the prefix before it reaches the filename and the manifest step; otherwise
	// `pkgdev manifest` rejects it with "does not follow correct package syntax".
	// Validate up front so a non-version (or a string still invalid after
	// stripping) fails with a clear error instead of a cryptic portage one.
	newVersion := stripVersionPrefix(strings.TrimSpace(update.NewVersion))
	if !ebuild.IsValidVersion(newVersion) {
		result.Error = fmt.Errorf("%w: %q (from %q)", ErrInvalidNewVersion, newVersion, update.NewVersion)
		if err := a.pending.SetStatus(pkg, StatusFailed, result.Error.Error()); err != nil {
			result.Error = fmt.Errorf("%w (also failed to update status: %v)", result.Error, err)
		}
		return result, result.Error
	}
	result.NewVersion = newVersion

	// Re-resolve the current version against the live overlay rather than
	// trusting update.CurrentVersion. That field is a snapshot from check-time
	// and drifts: the overlay may have been bumped past it, or the package
	// removed entirely. Blind trust produced a cryptic "source ebuild not found"
	// when the recorded version's ebuild was already gone. Re-resolution
	// self-heals a stale current_version and lets a genuinely obsolete entry be
	// pruned with a clear outcome instead of a hard failure.
	currentVersion, err := a.resolveCurrentVersion(pkg)
	if err != nil {
		// Package no longer present in the overlay (removed/renamed). The pending
		// entry is obsolete — prune it and report, not as a failure.
		return a.pruneObsolete(pkg, result,
			fmt.Errorf("%w: %s no longer in overlay (%v)", ErrObsoletePending, pkg, err))
	}
	result.OldVersion = currentVersion

	// Overlay already at or beyond the target: the update was already applied or
	// has been superseded by a newer bump. A copy would be pointless (or a
	// downgrade) — prune the stale entry instead.
	if ebuild.CompareVersions(currentVersion, newVersion) >= 0 {
		return a.pruneObsolete(pkg, result,
			fmt.Errorf("%w: overlay already at %s (target %s)", ErrObsoletePending, currentVersion, newVersion))
	}

	// Copy ebuild to new version
	if err := a.copyEbuild(pkg, currentVersion, newVersion); err != nil {
		result.Error = fmt.Errorf("failed to copy ebuild: %w", err)
		if err := a.pending.SetStatus(pkg, StatusFailed, result.Error.Error()); err != nil {
			// Log but don't override the original error
			result.Error = fmt.Errorf("%w (also failed to update status: %v)", result.Error, err)
		}
		return result, result.Error
	}

	// For snapshot packages tracked by commit (track="commit"), substitute the
	// commit hash variable in the copied ebuild so the SRC_URI points to the
	// correct tarball. This must happen before pkgdev manifest, which fetches
	// the URL built from the variable.
	if update.CommitHash != "" {
		dstEbuild := a.EbuildPath(pkg, newVersion)
		if err := substituteCommitHash(dstEbuild, update.CommitHash); err != nil {
			result.Error = fmt.Errorf("failed to substitute commit hash: %w", err)
			if err := a.pending.SetStatus(pkg, StatusFailed, result.Error.Error()); err != nil {
				result.Error = fmt.Errorf("%w (also failed to update status: %v)", result.Error, err)
			}
			return result, result.Error
		}
	}

	// copyEbuild succeeded: a fresh .ebuild now exists in the overlay. If any
	// later step (manifest, status update, compile) fails, that file is an
	// orphan and must be removed so the overlay is not left half-applied.
	// The cleanup keyed on result.Error so it only fires on failure, and it
	// must never replace the original error with a removal error.
	dstPath := a.EbuildPath(pkg, newVersion)
	defer func() {
		if result == nil || result.Error == nil {
			return
		}
		if err := os.Remove(dstPath); err != nil && !errors.Is(err, os.ErrNotExist) {
			// Rollback failed: keep the original error, just record the
			// cleanup miss so the orphan ebuild can be found and removed.
			logger.Warn(
				"failed to roll back orphan ebuild %s for %s (%s) after apply failure: %v "+
					"(original apply error preserved: %v)",
				dstPath, pkg, newVersion, err, result.Error)
		}
	}()

	// Run manifest command
	if err := a.runManifest(pkg, newVersion); err != nil {
		result.Error = fmt.Errorf("%w: %v", ErrManifestFailed, err)
		if err := a.pending.SetStatus(pkg, StatusFailed, result.Error.Error()); err != nil {
			result.Error = fmt.Errorf("%w (also failed to update status: %v)", result.Error, err)
		}
		return result, result.Error
	}

	// Update status to validated
	if err := a.pending.SetStatus(pkg, StatusValidated, ""); err != nil {
		result.Error = fmt.Errorf("failed to update status: %w", err)
		return result, result.Error
	}

	// Run compile test if requested
	if compile {
		logPath, err := a.runCompile(pkg, newVersion)
		if err != nil {
			result.Error = err
			result.LogPath = logPath
			if err := a.pending.SetStatus(pkg, StatusFailed, err.Error()); err != nil {
				result.Error = fmt.Errorf("%w (also failed to update status: %v)", result.Error, err)
			}
			return result, result.Error
		}
	}

	result.Success = true

	// R3.1: remove the now-applied package from pending.json so `--list` no
	// longer surfaces it. R3.4: a Delete failure is a bookkeeping miss, not
	// an apply failure — log a Warn (via the package warnLogf sink so tests
	// can capture it) but keep result.Success == true and result.Error == nil
	// so the deferred orphan-rollback (keyed on result.Error == nil) does not
	// undo the successful apply.
	if err := a.pendingDeleteFn(pkg); err != nil {
		warnLogf("pending: failed to remove %s after successful apply: %v "+
			"(apply itself succeeded; entry can be cleared manually)", pkg, err)
	}

	// --clean (R-clean): drop the previous version's ebuild so only the freshly
	// applied one remains. This runs only on the full success path and is
	// best-effort — a removal or manifest-prune failure is surfaced as a warning
	// on the result but never flips Success, because the update itself is done.
	if a.clean {
		if removed, err := a.cleanOldEbuild(pkg, currentVersion, newVersion); err != nil {
			warnLogf("clean: %v", err)
			result.CleanWarning = err.Error()
		} else if removed {
			result.CleanedOldVersion = currentVersion
		}
	}

	return result, nil
}

// resolveCurrentVersion returns the highest-version, non-live ebuild version
// actually present in the overlay for pkg. It mirrors the checker's selection
// (getCurrentVersion) so Apply works off the live overlay state instead of the
// pending entry's possibly-stale current_version. Returns ErrNoEbuildFound when
// the package directory is absent or holds no parsable, non-live ebuild.
func (a *Applier) resolveCurrentVersion(pkg string) (string, error) {
	parts := strings.Split(pkg, "/")
	if len(parts) != 2 {
		return "", fmt.Errorf("invalid package name format: %s", pkg)
	}
	pkgDir := filepath.Join(a.overlayPath, parts[0], parts[1])

	entries, err := os.ReadDir(pkgDir)
	if err != nil {
		if os.IsNotExist(err) {
			return "", fmt.Errorf("%w: %s", ErrNoEbuildFound, pkg)
		}
		return "", fmt.Errorf("failed to read package directory: %w", err)
	}

	var best string
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		if !strings.HasSuffix(name, ".ebuild") || strings.Contains(name, "-9999.ebuild") {
			continue
		}
		eb, err := ebuild.ParsePath(filepath.Join(parts[0], parts[1], name))
		if err != nil {
			continue // Skip invalid ebuild files
		}
		if best == "" || ebuild.CompareVersions(eb.Version, best) > 0 {
			best = eb.Version
		}
	}

	if best == "" {
		return "", fmt.Errorf("%w: %s", ErrNoEbuildFound, pkg)
	}
	return best, nil
}

// pruneObsolete marks result as an obsolete pending entry, removes it from the
// pending list (best-effort), and returns it with a nil error so callers do not
// count it as a failure. reason is surfaced to the user verbatim via
// ObsoleteReason.
func (a *Applier) pruneObsolete(pkg string, result *ApplyResult, reason error) (*ApplyResult, error) {
	result.Obsolete = true
	result.ObsoleteReason = reason.Error()
	if err := a.pendingDeleteFn(pkg); err != nil {
		warnLogf("pending: failed to prune obsolete entry %s: %v "+
			"(entry can be cleared manually)", pkg, err)
	}
	return result, nil
}

// cleanOldEbuild removes the previous version's ebuild and regenerates the
// Manifest so the now-orphaned distfiles are pruned. It returns (true, nil) when
// an ebuild was actually removed, (false, nil) when there was nothing to remove
// (same version, or the old file is already gone), and a non-nil error when the
// removal or the manifest regeneration fails. The new ebuild is left untouched.
func (a *Applier) cleanOldEbuild(pkg, oldVersion, newVersion string) (bool, error) {
	if oldVersion == newVersion {
		return false, nil
	}
	oldPath := a.EbuildPath(pkg, oldVersion)
	if err := os.Remove(oldPath); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return false, nil
		}
		return false, fmt.Errorf("failed to remove old ebuild %s: %w", oldPath, err)
	}
	// The old ebuild is gone; regenerate the Manifest against the remaining
	// ebuild(s) so its distfile entries no longer reference the removed version.
	if err := a.runManifest(pkg, newVersion); err != nil {
		return true, fmt.Errorf("removed old ebuild %s but failed to regenerate manifest: %w", oldPath, err)
	}
	return true, nil
}

// copyEbuild copies the source ebuild to a new file with the updated version.
// Source: {category}/{package}/{package}-{oldVersion}.ebuild
// Destination: {category}/{package}/{package}-{newVersion}.ebuild
func (a *Applier) copyEbuild(pkg, oldVersion, newVersion string) error {
	// Parse package name
	parts := strings.Split(pkg, "/")
	if len(parts) != 2 {
		return fmt.Errorf("invalid package name format: %s", pkg)
	}
	category := parts[0]
	pkgName := parts[1]

	// Reject same-version copy: srcPath and dstPath would coincide, and
	// os.Create truncates the destination before io.Copy reads, silently
	// zeroing the source ebuild.
	if oldVersion == newVersion {
		return fmt.Errorf("source and destination versions are equal: %s", newVersion)
	}

	// Build paths
	pkgDir := filepath.Join(a.overlayPath, category, pkgName)
	srcPath := filepath.Join(pkgDir, fmt.Sprintf("%s-%s.ebuild", pkgName, oldVersion))
	dstPath := filepath.Join(pkgDir, fmt.Sprintf("%s-%s.ebuild", pkgName, newVersion))

	// Check source exists
	if _, err := os.Stat(srcPath); os.IsNotExist(err) {
		return fmt.Errorf("%w: %s", ErrEbuildNotFound, srcPath)
	}

	// Open source file
	src, err := os.Open(srcPath)
	if err != nil {
		return fmt.Errorf("failed to open source ebuild: %w", err)
	}
	defer src.Close() //nolint:errcheck

	// Create destination file
	dst, err := os.Create(dstPath)
	if err != nil {
		return fmt.Errorf("failed to create destination ebuild: %w", err)
	}
	defer dst.Close() //nolint:errcheck

	// Copy content
	if _, err := io.Copy(dst, src); err != nil {
		return fmt.Errorf("failed to copy ebuild content: %w", err)
	}

	// Sync to ensure data is written
	if err := dst.Sync(); err != nil {
		return fmt.Errorf("failed to sync destination ebuild: %w", err)
	}

	return nil
}

// substituteCommitHash replaces the commit-hash variable assignment in an
// ebuild with newHash. It handles the three variable names used in the overlay:
//
//	EGIT_COMMIT="<sha>"   (vulkan-*, spirv-*)
//	GIT_COMMIT="<sha>"    (glslang, modemmanager)
//	COMMIT=<sha>          (sqlitebrowser — no quotes)
//
// The substitution is deliberately narrow (anchored to known variable names +
// 40-hex-char SHA) so it cannot accidentally corrupt other content.
func substituteCommitHash(ebuildPath, newHash string) error {
	content, err := os.ReadFile(ebuildPath)
	if err != nil {
		return fmt.Errorf("failed to read ebuild for hash substitution: %w", err)
	}

	reQuoted := regexp.MustCompile(`((?:EGIT_COMMIT|GIT_COMMIT)=")[0-9a-f]{40}(")`)
	reBare := regexp.MustCompile(`(COMMIT=)[0-9a-f]{40}\b`)

	updated := reQuoted.ReplaceAllString(string(content), "${1}"+newHash+"${2}")
	updated = reBare.ReplaceAllString(updated, "${1}"+newHash)

	if updated == string(content) {
		return fmt.Errorf("no commit hash variable (EGIT_COMMIT/GIT_COMMIT/COMMIT) found in %s", ebuildPath)
	}

	if err := os.WriteFile(ebuildPath, []byte(updated), 0o600); err != nil {
		return fmt.Errorf("failed to write ebuild after hash substitution: %w", err)
	}

	return nil
}

// runManifest regenerates the Manifest file with pkgdev. Unlike `ebuild
// manifest`, pkgdev neither requires root nor writes to the system DISTDIR
// (/var/cache/distfiles): it digests against a private --distdir we own, so the
// step works as an unprivileged user without write access to Portage's caches.
// Command: pkgdev manifest --distdir {tmpdir}  (run from the package directory)
func (a *Applier) runManifest(pkg, version string) error {
	// Parse package name
	parts := strings.Split(pkg, "/")
	if len(parts) != 2 {
		return fmt.Errorf("invalid package name format: %s", pkg)
	}
	category := parts[0]
	pkgName := parts[1]

	// Package directory pkgdev operates in (it discovers the ebuild itself).
	pkgDir := filepath.Join(a.overlayPath, category, pkgName)

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
	if err := a.prefetchAuthDistfile(pkg, version, distdir); err != nil {
		return err
	}

	// Bound the manifest invocation: derive a child context from the applier's
	// parent context with a finite deadline so a stalled distfile fetch cannot
	// hang Apply forever. Cancelling either the parent (SIGINT) or this child
	// (timeout) kills the spawned process via exec.CommandContext.
	ctx, cancel := context.WithTimeout(a.ctx, manifestTimeout)
	defer cancel()

	// Run pkgdev manifest from the package directory.
	cmd := a.execCommand(ctx, "pkgdev", "manifest", "--distdir", distdir)
	cmd.Dir = pkgDir

	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("command failed: %w\nOutput: %s", err, string(output))
	}

	return nil
}

// prefetchAuthDistfile downloads a serial-gated distfile into distdir when the
// package's [meta] block configures an authenticated fetch. It is a no-op for
// packages without that config (the overwhelming majority) and when no config
// was supplied to the applier at all. The download is bounded by the applier's
// parent context so SIGINT cancels it, and the serial never appears in logs.
func (a *Applier) prefetchAuthDistfile(pkg, version, distdir string) error {
	cfg, ok := a.configs[pkg]
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

	dest, err := spec.fetchDistfile(a.ctx, version, distdir)
	if err != nil {
		return err
	}
	logger.Info("authenticated fetch: wrote %s", filepath.Base(dest))
	return nil
}

// runCompile runs a compile test with elevated privileges.
// It prompts for user confirmation before executing.
// Returns the log path if compilation fails.
func (a *Applier) runCompile(pkg, version string) (string, error) {
	// Prompt for confirmation
	prompt := fmt.Sprintf("Run compile test for %s-%s with elevated privileges?", pkg, version)
	if !a.confirmFunc(prompt) {
		return "", ErrUserDeclined
	}

	// Detect privilege escalation tool
	privTool, err := a.detectPrivilegeTool()
	if err != nil {
		return "", err
	}

	// Parse package name
	parts := strings.Split(pkg, "/")
	if len(parts) != 2 {
		return "", fmt.Errorf("invalid package name format: %s", pkg)
	}
	category := parts[0]
	pkgName := parts[1]

	// Build ebuild path
	ebuildPath := filepath.Join(a.overlayPath, category, pkgName, fmt.Sprintf("%s-%s.ebuild", pkgName, version))

	// Run compile test: sudo/doas ebuild <path> clean compile. The command is
	// bound to the applier's parent context so a SIGINT or deadline kills the
	// spawned process.
	cmd := a.execCommand(a.ctx, privTool, "ebuild", ebuildPath, "clean", "compile")
	cmd.Dir = a.overlayPath

	output, err := cmd.CombinedOutput()
	if err != nil {
		// Save log to file
		logPath := a.saveCompileLog(pkg, version, output)
		return logPath, fmt.Errorf("%w: %v", ErrCompileFailed, err)
	}

	return "", nil
}

// detectPrivilegeTool detects whether sudo or doas is available.
func (a *Applier) detectPrivilegeTool() (string, error) {
	// Check for doas first (more secure, preferred on some systems)
	if _, err := exec.LookPath("doas"); err == nil {
		return "doas", nil
	}

	// Check for sudo
	if _, err := exec.LookPath("sudo"); err == nil {
		return "sudo", nil
	}

	return "", ErrNoPrivilegeEscalation
}

// saveCompileLog saves the compile output to a log file.
// Returns the path to the log file.
func (a *Applier) saveCompileLog(pkg, version string, output []byte) string {
	// Create log filename with timestamp
	timestamp := time.Now().Format("20060102-150405")
	safePkg := strings.ReplaceAll(pkg, "/", "_")
	logName := fmt.Sprintf("%s-%s-%s.log", safePkg, version, timestamp)
	logPath := filepath.Join(a.logsDir, logName)

	// Write log file. Compile logs use 0600 (owner-only): they may contain
	// sensitive build details. os.WriteFile applies the mode on creation.
	if err := os.WriteFile(logPath, output, fileutil.CacheFileMode); err != nil {
		// If we can't write the log, return empty path
		return ""
	}

	return logPath
}

// defaultConfirmFunc is the default confirmation function that reads from stdin.
func defaultConfirmFunc(prompt string) bool {
	fmt.Printf("%s [y/N]: ", prompt)
	reader := bufio.NewReader(os.Stdin)
	response, err := reader.ReadString('\n')
	if err != nil {
		return false
	}

	response = strings.TrimSpace(strings.ToLower(response))
	return response == "y" || response == "yes"
}

// Pending returns the pending list instance.
func (a *Applier) Pending() *PendingList {
	return a.pending
}

// OverlayPath returns the overlay path.
func (a *Applier) OverlayPath() string {
	return a.overlayPath
}

// LogsDir returns the logs directory path.
func (a *Applier) LogsDir() string {
	return a.logsDir
}

// EbuildPath returns the full path to an ebuild file.
func (a *Applier) EbuildPath(pkg, version string) string {
	parts := strings.Split(pkg, "/")
	if len(parts) != 2 {
		return ""
	}
	category := parts[0]
	pkgName := parts[1]
	return filepath.Join(a.overlayPath, category, pkgName, fmt.Sprintf("%s-%s.ebuild", pkgName, version))
}
