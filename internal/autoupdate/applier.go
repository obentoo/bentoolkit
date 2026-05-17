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
	"strings"
	"time"

	"github.com/obentoo/bentoolkit/internal/common/fileutil"
	"github.com/obentoo/bentoolkit/internal/common/logger"
)

// manifestTimeout bounds a single `ebuild manifest` invocation. The manifest
// step touches the network (fetching SRC_URI distfiles to digest), so it gets a
// generous-but-finite budget; without it a stalled fetch would hang Apply
// indefinitely.
const manifestTimeout = 5 * time.Minute

// Error variables for applier errors
var (
	// ErrEbuildNotFound is returned when the source ebuild file is not found
	ErrEbuildNotFound = errors.New("source ebuild file not found")
	// ErrManifestFailed is returned when the ebuild manifest command fails
	ErrManifestFailed = errors.New("ebuild manifest command failed")
	// ErrCompileFailed is returned when the compile test fails
	ErrCompileFailed = errors.New("compile test failed")
	// ErrNoPrivilegeEscalation is returned when neither sudo nor doas is available
	ErrNoPrivilegeEscalation = errors.New("no privilege escalation tool available (sudo or doas)")
	// ErrUserDeclined is returned when user declines the compile confirmation
	ErrUserDeclined = errors.New("user declined compile test")
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
// threaded into every spawned external command (ebuild manifest, compile test),
// so cancelling it (e.g. on SIGINT or a deadline) kills in-flight processes.
// A nil context is ignored, leaving the default context.Background().
func WithApplierContext(ctx context.Context) ApplierOption {
	return func(a *Applier) {
		if ctx != nil {
			a.ctx = ctx
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
	result.NewVersion = update.NewVersion

	// Copy ebuild to new version
	if err := a.copyEbuild(pkg, update.CurrentVersion, update.NewVersion); err != nil {
		result.Error = fmt.Errorf("failed to copy ebuild: %w", err)
		if err := a.pending.SetStatus(pkg, StatusFailed, result.Error.Error()); err != nil {
			// Log but don't override the original error
			result.Error = fmt.Errorf("%w (also failed to update status: %v)", result.Error, err)
		}
		return result, result.Error
	}

	// copyEbuild succeeded: a fresh .ebuild now exists in the overlay. If any
	// later step (manifest, status update, compile) fails, that file is an
	// orphan and must be removed so the overlay is not left half-applied.
	// The cleanup keyed on result.Error so it only fires on failure, and it
	// must never replace the original error with a removal error.
	dstPath := a.EbuildPath(pkg, update.NewVersion)
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
				dstPath, pkg, update.NewVersion, err, result.Error)
		}
	}()

	// Run manifest command
	if err := a.runManifest(pkg, update.NewVersion); err != nil {
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
		logPath, err := a.runCompile(pkg, update.NewVersion)
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
	return result, nil
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

// runManifest runs the ebuild manifest command to regenerate the Manifest file.
// Command: ebuild {category}/{package}/{package}-{version}.ebuild manifest
func (a *Applier) runManifest(pkg, version string) error {
	// Parse package name
	parts := strings.Split(pkg, "/")
	if len(parts) != 2 {
		return fmt.Errorf("invalid package name format: %s", pkg)
	}
	category := parts[0]
	pkgName := parts[1]

	// Build ebuild path
	ebuildPath := filepath.Join(a.overlayPath, category, pkgName, fmt.Sprintf("%s-%s.ebuild", pkgName, version))

	// Bound the manifest invocation: derive a child context from the applier's
	// parent context with a finite deadline so a stalled distfile fetch cannot
	// hang Apply forever. Cancelling either the parent (SIGINT) or this child
	// (timeout) kills the spawned process via exec.CommandContext.
	ctx, cancel := context.WithTimeout(a.ctx, manifestTimeout)
	defer cancel()

	// Run ebuild manifest command.
	cmd := a.execCommand(ctx, "ebuild", ebuildPath, "manifest")
	cmd.Dir = a.overlayPath

	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("command failed: %w\nOutput: %s", err, string(output))
	}

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
