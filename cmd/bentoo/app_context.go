package main

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/obentoo/bentoolkit/internal/common/config"
	"github.com/obentoo/bentoolkit/internal/common/logger"
	"github.com/spf13/cobra"
)

// overlayFlagName is the root persistent flag naming the overlay to work on for
// this run, outranking both the current directory and overlay.path. It is read
// off the running command (overlayFlagValue), never kept in a package variable:
// cmd/bentoo holds no new globals (story 060, R8.4).
const overlayFlagName = "overlay"

// overlayFlagValue returns --overlay as the running command sees it: the root
// declares it persistent, so every subcommand inherits it. A command built
// outside the root tree (or a nil one) has no such flag and gets "".
func overlayFlagValue(cmd *cobra.Command) string {
	if cmd == nil {
		return ""
	}
	f := cmd.Flag(overlayFlagName)
	if f == nil {
		return ""
	}
	return f.Value.String()
}

// appContext holds shared CLI dependencies loaded once per command invocation.
type appContext struct {
	Config      *config.Config
	OverlayPath string
}

// loadAppContext loads config and validates the overlay path.
// Use for commands that require a valid, existing overlay directory.
func loadAppContext(cmd *cobra.Command) (*appContext, error) {
	cfg, err := config.Load()
	if err != nil {
		return nil, err
	}
	selectOverlay(cfg, overlayFlagValue(cmd))
	overlayPath, err := cfg.GetOverlayPath()
	if err != nil {
		return nil, err
	}
	return &appContext{Config: cfg, OverlayPath: overlayPath}, nil
}

// loadAppContextNoValidation loads config and resolves the overlay path
// without validating the overlay directory structure.
// Use for commands like analyze and autoupdate that work with unconfigured overlays.
func loadAppContextNoValidation(cmd *cobra.Command) (*appContext, error) {
	cfg, err := config.Load()
	if err != nil {
		return nil, err
	}
	selectOverlay(cfg, overlayFlagValue(cmd))
	overlayPath, err := cfg.GetOverlayPathNoValidation()
	if err != nil {
		return nil, err
	}
	return &appContext{Config: cfg, OverlayPath: overlayPath}, nil
}

// selectOverlay decides which overlay this run works on, in place on cfg:
//
//  1. --overlay, when given;
//  2. the checkout the current directory is in, when it is the SAME overlay
//     (its profiles/repo_name matches overlay.path's) at another path — a
//     worktree or a second clone;
//  3. overlay.path from the config.
//
// Rule 2 is what keeps `--lint` in a worktree from validating the main
// checkout and reporting green on code it never read. It only ever moves to a
// checkout of the configured overlay, never to an unrelated repository, and it
// says so at INFO, because the run is then not acting where the config says.
func selectOverlay(cfg *config.Config, flag string) {
	if flag != "" {
		cfg.Overlay.Path = flag
		return
	}
	if p, ok := overlayCheckoutAtCwd(cfg.Overlay.Path); ok {
		logger.Info("using the overlay checkout at %s (current directory) instead of overlay.path %s; pass --overlay to choose explicitly", p, cfg.Overlay.Path)
		cfg.Overlay.Path = p
	}
}

// overlayCheckoutAtCwd returns the root of the repository holding the current
// directory when that repository is a checkout of configured — same
// profiles/repo_name — at a different path.
func overlayCheckoutAtCwd(configured string) (string, bool) {
	if configured == "" {
		return "", false
	}
	if strings.HasPrefix(configured, "~") {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", false
		}
		configured = filepath.Join(home, configured[1:])
	}
	want := repoName(configured)
	if want == "" {
		return "", false
	}
	cwd, err := os.Getwd()
	if err != nil {
		return "", false
	}
	for dir := cwd; ; dir = filepath.Dir(dir) {
		if name := repoName(dir); name != "" {
			if name != want || samePath(dir, configured) {
				return "", false
			}
			return dir, true
		}
		if parent := filepath.Dir(dir); parent == dir {
			return "", false
		}
	}
}

// repoName reads dir/profiles/repo_name, or "" when dir is not a repository.
func repoName(dir string) string {
	b, err := os.ReadFile(filepath.Join(dir, "profiles", "repo_name")) //nolint:gosec // G304: a fixed relative name under a directory on the path from cwd to /; reading it only names the repository
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

// samePath reports whether a and b name the same directory, symlinks resolved.
func samePath(a, b string) bool {
	ra, errA := filepath.EvalSymlinks(a)
	rb, errB := filepath.EvalSymlinks(b)
	if errA != nil || errB != nil {
		return filepath.Clean(a) == filepath.Clean(b)
	}
	return ra == rb
}
