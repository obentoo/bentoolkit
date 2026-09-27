package git

// Authored for story 056, sub-task 5.2 (S056-R6.3). A real repository: the
// question is what `git add` actually stages, which no fake runner can answer.

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

func s056Git(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return string(out)
}

func s056Staged(t *testing.T, dir string) []string {
	t.Helper()
	var staged []string
	for _, line := range strings.Split(s056Git(t, dir, "diff", "--cached", "--name-only"), "\n") {
		if line != "" {
			staged = append(staged, line)
		}
	}
	sort.Strings(staged)
	return staged
}

// TestAddExcludesBentooScratchFiles: every temp and lock file named
// .<something>.bentoo-* stays unstaged in any directory — the atomic helper's
// temps, writeThenRename's, realign's and both lock files — while the real
// files next to them are still staged.
func TestAddExcludesBentooScratchFiles(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	scratch := []string{
		".autoupdate.bentoo-lock",
		filepath.Join("app-misc", "hello", ".hello-1.1.ebuild.bentoo-4242-abc"),
		filepath.Join("app-misc", "hello", ".Manifest.bentoo-918273"),
		filepath.Join("app-misc", "hello", ".hello-1.0.ebuild.bentoo-realign-55"),
		filepath.Join(".autoupdate", ".packages.toml.bentoo-4242-x"),
	}
	real := []string{
		filepath.Join("app-misc", "hello", "hello-1.1.ebuild"),
		filepath.Join(".autoupdate", "packages.toml"),
	}

	for _, tc := range []struct {
		name  string
		paths []string
		want  []string
	}{
		{"default path", nil, real},
		{"explicit directories", []string{"app-misc", ".autoupdate"}, real},
		{"explicit subdirectory", []string{filepath.Join("app-misc", "hello")}, real[:1]},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			s056Git(t, dir, "init", "-q")
			for _, p := range append(append([]string{}, scratch...), real...) {
				full := filepath.Join(dir, p)
				if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(full, []byte("x\n"), 0o644); err != nil {
					t.Fatal(err)
				}
			}

			if err := NewGitRunner(dir).Add(context.Background(), tc.paths...); err != nil {
				t.Fatalf("Add(%q): %v", tc.paths, err)
			}

			want := append([]string{}, tc.want...)
			sort.Strings(want)
			if got := s056Staged(t, dir); strings.Join(got, "\n") != strings.Join(want, "\n") {
				t.Errorf("Add(%q) staged\n  %q\nwant exactly\n  %q", tc.paths, got, want)
			}
		})
	}
}
