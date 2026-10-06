package autoupdate

// Authored for story 056, sub-task 2.2 (S056-R5.3, S056-R5.4, S056-R5.5,
// S056-R8.11). Two instances stand for two `bentoo` runs: each loads the same
// file, changes something, and saves. The merge rule has two converse ways to
// fire wrongly, and both are here before the benign case:
//
//   - wrongly SPLIT: two runs' distinct keys must both survive (lost update);
//   - wrongly REVIVE: a key one run deleted must stay deleted, even when a
//     second run that still holds it in memory saves afterwards.

import (
	"testing"

	"github.com/obentoo/bentoolkit/internal/autoupdate/registry"
)

func TestPendingSave_TwoInstancesKeepBothEntries(t *testing.T) {
	dir := t.TempDir()
	a, _ := NewPendingList(dir)
	b, _ := NewPendingList(dir)
	if err := a.Add(PendingUpdate{Package: "x/a", CurrentVersion: "1", NewVersion: "2", Status: StatusPending}); err != nil {
		t.Fatal(err)
	}
	if err := b.Add(PendingUpdate{Package: "x/b", CurrentVersion: "1", NewVersion: "2", Status: StatusPending}); err != nil {
		t.Fatal(err)
	}
	reloaded, _ := NewPendingList(dir)
	for _, k := range []string{"x/a", "x/b"} {
		if !reloaded.Has(k) {
			t.Errorf("pending.json lost %s", k)
		}
	}
}

func TestPendingSave_DeletionSurvivesOtherInstance(t *testing.T) {
	dir := t.TempDir()
	seed, _ := NewPendingList(dir)
	if err := seed.Add(PendingUpdate{Package: "x/a", CurrentVersion: "1", NewVersion: "2", Status: StatusPending}); err != nil {
		t.Fatal(err)
	}
	a, _ := NewPendingList(dir)
	b, _ := NewPendingList(dir)
	if err := a.Delete("x/a"); err != nil {
		t.Fatal(err)
	}
	if err := b.Add(PendingUpdate{Package: "x/b", CurrentVersion: "1", NewVersion: "2", Status: StatusPending}); err != nil {
		t.Fatal(err)
	}
	reloaded, _ := NewPendingList(dir)
	if reloaded.Has("x/a") {
		t.Error("x/a came back in pending.json after a deleted it")
	}
	if !reloaded.Has("x/b") {
		t.Error("pending.json lost x/b")
	}
}

func TestAnalysisCacheSave_TwoInstancesKeepBothEntries(t *testing.T) {
	dir := t.TempDir()
	a, _ := NewAnalysisCache(dir)
	b, _ := NewAnalysisCache(dir)
	if err := a.Set("x/a", &registry.PackageConfig{URL: "https://example.invalid/a", Parser: "json", Path: "v"}, "https://example.invalid/a"); err != nil {
		t.Fatal(err)
	}
	if err := b.Set("x/b", &registry.PackageConfig{URL: "https://example.invalid/b", Parser: "json", Path: "v"}, "https://example.invalid/b"); err != nil {
		t.Fatal(err)
	}
	reloaded, _ := NewAnalysisCache(dir)
	for _, k := range []string{"x/a", "x/b"} {
		if _, ok := reloaded.GetEntry(k); !ok {
			t.Errorf("analysis_cache.json lost %s", k)
		}
	}
}
