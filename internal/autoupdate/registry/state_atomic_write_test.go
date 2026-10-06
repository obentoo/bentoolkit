package registry

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

func s056Entries(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("reading %s: %v", dir, err)
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		names = append(names, e.Name())
	}
	sort.Strings(names)
	return names
}

func s056AssertEntries(t *testing.T, dir string, want ...string) {
	t.Helper()
	sort.Strings(want)
	if got := s056Entries(t, dir); strings.Join(got, "\x00") != strings.Join(want, "\x00") {
		t.Errorf("%s holds %q, want exactly %q", dir, got, want)
	}
}

// s056PlantStaleTmp puts "<name>.tmp" in dir as a symlink to a victim file in
// another directory and returns the victim's path.
func s056PlantStaleTmp(t *testing.T, dir, name string) string {
	t.Helper()
	victim := filepath.Join(t.TempDir(), "victim")
	if err := os.WriteFile(victim, []byte("victim"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(victim, filepath.Join(dir, name+".tmp")); err != nil {
		t.Fatal(err)
	}
	return victim
}
