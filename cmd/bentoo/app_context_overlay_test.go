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
			orig := overlayFlag
			overlayFlag = tc.flag
			t.Cleanup(func() { overlayFlag = orig })

			cfg := &config.Config{}
			cfg.Overlay.Path = main
			selectOverlay(cfg)
			if !samePath(cfg.Overlay.Path, tc.want) {
				t.Errorf("overlay = %s, want %s", cfg.Overlay.Path, tc.want)
			}
		})
	}
}
