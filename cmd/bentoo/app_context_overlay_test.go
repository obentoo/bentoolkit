package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/obentoo/bentoolkit/internal/common/config"
)

// newRepo creates a directory holding profiles/repo_name = name.
func newRepo(t *testing.T, name string) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "profiles"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "profiles", "repo_name"), []byte(name+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestSelectOverlay(t *testing.T) {
	main := newRepo(t, "bentoo")
	worktree := newRepo(t, "bentoo")
	other := newRepo(t, "guru")
	sub := filepath.Join(worktree, "dev-lang", "dart")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}

	for _, tc := range []struct {
		name string
		flag string
		cwd  string
		want string
	}{
		{"config outside any checkout", "", t.TempDir(), main},
		{"inside the configured checkout", "", main, main},
		{"deep inside a worktree of the same overlay", "", sub, worktree},
		{"inside an unrelated repository", "", other, main},
		{"--overlay outranks the current directory", other, sub, other},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Chdir(tc.cwd)
			cfg := &config.Config{}
			cfg.Overlay.Path = main
			selectOverlay(cfg, tc.flag)
			if !samePath(cfg.Overlay.Path, tc.want) {
				t.Errorf("overlay = %s, want %s", cfg.Overlay.Path, tc.want)
			}
		})
	}
}

// TestOverlayFlagReachesSubcommands pins how --overlay travels now that it has
// no package variable: the root declares it persistent and loadAppContext reads
// it off the running command, so a subcommand sees the value given on its own
// command line, and a fresh tree starts from "" (nothing leaks between runs).
func TestOverlayFlagReachesSubcommands(t *testing.T) {
	for _, path := range [][]string{{"overlay", "status"}, {"overlay", "autoupdate"}} {
		root := newRootCmd()
		sub, _, err := root.Find(path)
		if err != nil {
			t.Fatalf("Find(%v): %v", path, err)
		}
		if err := sub.ParseFlags([]string{"--overlay", "/srv/other"}); err != nil {
			t.Fatalf("ParseFlags(%v): %v", path, err)
		}
		if got := overlayFlagValue(sub); got != "/srv/other" {
			t.Errorf("%v: overlayFlagValue = %q, want /srv/other", path, got)
		}

		fresh, _, err := newRootCmd().Find(path)
		if err != nil {
			t.Fatalf("Find(%v): %v", path, err)
		}
		if got := overlayFlagValue(fresh); got != "" {
			t.Errorf("%v: a fresh tree's overlayFlagValue = %q, want empty", path, got)
		}
	}
	if got := overlayFlagValue(nil); got != "" {
		t.Errorf("overlayFlagValue(nil) = %q, want empty", got)
	}
}
