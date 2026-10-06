package autoupdate

// Authored for story 056, sub-task 2.1 (S056-R2.1). Replaces what
// TestCacheAtomicWrite and TestPendingListAtomicWrite only claimed: those check
// that "<name>.tmp" is absent afterwards, which any random temp name satisfies
// vacuously. These plant a hostile "<name>.tmp" — a symlink to a victim file
// outside the config dir — and require the save to never touch it.

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/obentoo/bentoolkit/internal/autoupdate/registry"
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

// s056AssertStateFileSound checks the state file is a regular 0600 file, the
// planted .tmp is still the same symlink, and the victim was never written.
func s056AssertStateFileSound(t *testing.T, dir, name, victim string) {
	t.Helper()
	info, err := os.Lstat(filepath.Join(dir, name))
	if err != nil {
		t.Fatalf("no %s after the save: %v", name, err)
	}
	if !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 {
		t.Errorf("%s is %v, want a regular file at 0600", name, info.Mode())
	}
	if got, _ := os.ReadFile(victim); string(got) != "victim" {
		t.Errorf("the save wrote through the stale %s.tmp symlink: the victim now holds %q", name, got)
	}
	if target, err := os.Readlink(filepath.Join(dir, name+".tmp")); err != nil || target != victim {
		t.Errorf("the stale %s.tmp was touched: readlink = %q, %v", name, target, err)
	}
	s056AssertEntries(t, dir, name, name+".tmp")
}

func TestPendingListAtomicWrite_StaleFixedTempNameIsNeverUsed(t *testing.T) {
	dir := t.TempDir()
	victim := s056PlantStaleTmp(t, dir, "pending.json")
	p, err := NewPendingList(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := p.Add(PendingUpdate{Package: "x/a", CurrentVersion: "1.0", NewVersion: "1.1", Status: StatusPending}); err != nil {
		t.Fatalf("Add with a stale pending.json.tmp beside the list: %v", err)
	}
	s056AssertStateFileSound(t, dir, "pending.json", victim)
	reloaded, err := NewPendingList(dir)
	if err != nil {
		t.Fatal(err)
	}
	if !reloaded.Has("x/a") {
		t.Error("reloaded pending list lost x/a")
	}
}

func TestAnalysisCacheAtomicWrite(t *testing.T) {
	dir := t.TempDir()
	victim := s056PlantStaleTmp(t, dir, "analysis_cache.json")
	c, err := NewAnalysisCache(dir)
	if err != nil {
		t.Fatal(err)
	}
	schema := &registry.PackageConfig{URL: "https://example.invalid/x-a", Parser: "json", Path: "version"}
	if err := c.Set("x/a", schema, "https://example.invalid/x-a"); err != nil {
		t.Fatalf("Set with a stale analysis_cache.json.tmp beside the cache: %v", err)
	}
	s056AssertStateFileSound(t, dir, "analysis_cache.json", victim)
	reloaded, err := NewAnalysisCache(dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := reloaded.GetEntry("x/a"); !ok {
		t.Error("reloaded analysis cache lost x/a")
	}
}
