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
	"time"
)

func TestCacheSave_TwoInstancesKeepBothEntries(t *testing.T) {
	dir := t.TempDir()
	a, err := NewCache(dir)
	if err != nil {
		t.Fatal(err)
	}
	b, err := NewCache(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := a.Set("x/a", "1", "u"); err != nil {
		t.Fatal(err)
	}
	if err := b.Set("x/b", "2", "u"); err != nil {
		t.Fatal(err)
	}

	reloaded, err := NewCache(dir)
	if err != nil {
		t.Fatal(err)
	}
	for key, want := range map[string]string{"x/a": "1", "x/b": "2"} {
		if e, ok := reloaded.GetEntry(key); !ok || e.Version != want {
			t.Errorf("cache.json has %s = %+v, %v; want version %s (one run erased the other's entry)", key, e, ok, want)
		}
	}
	// The saver adopts what it wrote, so b now sees a's entry too.
	if _, ok := b.GetEntry("x/a"); !ok {
		t.Error("after its save, b does not hold x/a, which is in the file it wrote")
	}
}

// TestCacheSave_ThreeInstancesKeepEveryEntry: the pair is not the whole rule; a
// third run must not be erased by either of the first two.
func TestCacheSave_ThreeInstancesKeepEveryEntry(t *testing.T) {
	dir := t.TempDir()
	var cs []*Cache
	for range 3 {
		c, err := NewCache(dir)
		if err != nil {
			t.Fatal(err)
		}
		cs = append(cs, c)
	}
	keys := []string{"x/a", "x/b", "x/c"}
	for i, c := range cs {
		if err := c.Set(keys[i], "1", "u"); err != nil {
			t.Fatal(err)
		}
	}
	reloaded, _ := NewCache(dir)
	for _, k := range keys {
		if _, ok := reloaded.GetEntry(k); !ok {
			t.Errorf("cache.json lost %s", k)
		}
	}
}

func TestCacheSave_DeletionSurvivesOtherInstance(t *testing.T) {
	t.Run("deleted by one loader, other loader saves later", func(t *testing.T) {
		dir := t.TempDir()
		seed, _ := NewCache(dir)
		if err := seed.Set("x/a", "1", "u"); err != nil {
			t.Fatal(err)
		}
		a, _ := NewCache(dir)
		b, _ := NewCache(dir)
		if err := a.Delete("x/a"); err != nil {
			t.Fatal(err)
		}
		if err := b.Set("x/b", "2", "u"); err != nil {
			t.Fatal(err)
		}
		reloaded, _ := NewCache(dir)
		if _, ok := reloaded.GetEntry("x/a"); ok {
			t.Error("x/a came back: b wrote back the copy it loaded before a deleted it")
		}
		if _, ok := reloaded.GetEntry("x/b"); !ok {
			t.Error("x/b is missing")
		}
	})

	// The baseline is what an instance last loaded OR SAVED: a wrote x/a
	// itself, b deleted it, and a's next save must not resurrect it.
	t.Run("written by a, deleted by b, a saves again", func(t *testing.T) {
		dir := t.TempDir()
		a, _ := NewCache(dir)
		if err := a.Set("x/a", "1", "u"); err != nil {
			t.Fatal(err)
		}
		b, _ := NewCache(dir)
		if err := b.Delete("x/a"); err != nil {
			t.Fatal(err)
		}
		if err := a.Set("x/c", "3", "u"); err != nil {
			t.Fatal(err)
		}
		reloaded, _ := NewCache(dir)
		if _, ok := reloaded.GetEntry("x/a"); ok {
			t.Error("x/a came back after b deleted it: a's baseline was not updated by its own save")
		}
		if _, ok := reloaded.GetEntry("x/c"); !ok {
			t.Error("x/c is missing")
		}
	})
}

// TestCacheSave_ChangedKeyWinsUntouchedKeyTakesDisk: R5.3 for values. A key an
// instance never touched takes the on-disk value; a key it changed takes its
// own, even over another run's change.
func TestCacheSave_ChangedKeyWinsUntouchedKeyTakesDisk(t *testing.T) {
	dir := t.TempDir()
	seed, _ := NewCache(dir)
	if err := seed.Set("x/a", "1", "u"); err != nil {
		t.Fatal(err)
	}
	if err := seed.Set("x/z", "1", "u"); err != nil {
		t.Fatal(err)
	}
	a, _ := NewCache(dir)
	b, _ := NewCache(dir)
	if err := a.Set("x/a", "2", "u"); err != nil {
		t.Fatal(err)
	}
	if err := a.Set("x/z", "2", "u"); err != nil {
		t.Fatal(err)
	}
	if err := b.Set("x/z", "3", "u"); err != nil {
		t.Fatal(err)
	}
	reloaded, _ := NewCache(dir)
	if e, _ := reloaded.GetEntry("x/a"); e.Version != "2" {
		t.Errorf("x/a = %q; b never touched it, so a's 2 must survive b's save", e.Version)
	}
	if e, _ := reloaded.GetEntry("x/z"); e.Version != "3" {
		t.Errorf("x/z = %q; b changed it last, so it must be 3", e.Version)
	}
}

// TestCacheSave_DirectMutationThenSaveIsMerged: tests and callers mutate
// Entries directly and call Save; the merge keys on value change, not on
// which method was called.
func TestCacheSave_DirectMutationThenSaveIsMerged(t *testing.T) {
	dir := t.TempDir()
	a, _ := NewCache(dir)
	b, _ := NewCache(dir)
	if err := b.Set("x/b", "2", "u"); err != nil {
		t.Fatal(err)
	}
	a.Entries["x/c"] = CacheEntry{Version: "9", Timestamp: time.Now(), Source: "u"}
	if err := a.Save(); err != nil {
		t.Fatal(err)
	}
	reloaded, _ := NewCache(dir)
	for _, k := range []string{"x/b", "x/c"} {
		if _, ok := reloaded.GetEntry(k); !ok {
			t.Errorf("cache.json lost %s", k)
		}
	}
}

// TestCacheSave_ClearKeepsOtherInstancesLaterEntries: Clear deletes what this
// instance loaded, not what another run added afterwards.
func TestCacheSave_ClearKeepsOtherInstancesLaterEntries(t *testing.T) {
	dir := t.TempDir()
	seed, _ := NewCache(dir)
	if err := seed.Set("x/a", "1", "u"); err != nil {
		t.Fatal(err)
	}
	a, _ := NewCache(dir)
	b, _ := NewCache(dir)
	if err := b.Set("x/b", "2", "u"); err != nil {
		t.Fatal(err)
	}
	if err := a.Clear(); err != nil {
		t.Fatal(err)
	}
	reloaded, _ := NewCache(dir)
	if _, ok := reloaded.GetEntry("x/a"); ok {
		t.Error("x/a survived a's Clear")
	}
	if _, ok := reloaded.GetEntry("x/b"); !ok {
		t.Error("a's Clear erased x/b, which b added after a loaded")
	}
}

// TestCacheSave_PreconditionsAreMergedToo: the file holds two maps; merging
// only Entries would still lose the other run's preconditions.
func TestCacheSave_PreconditionsAreMergedToo(t *testing.T) {
	dir := t.TempDir()
	a, _ := NewCache(dir)
	b, _ := NewCache(dir)
	if err := a.SetPrecondition("x/a", "/usr/lib/libfoo.so"); err != nil {
		t.Fatal(err)
	}
	if err := b.Set("x/b", "2", "u"); err != nil {
		t.Fatal(err)
	}
	reloaded, _ := NewCache(dir)
	if rec, ok := reloaded.Precondition("x/a"); !ok || rec.Path != "/usr/lib/libfoo.so" {
		t.Errorf("precondition x/a = %+v, %v after b's save; want it kept", rec, ok)
	}
}

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
	if err := a.Set("x/a", &PackageConfig{URL: "https://example.invalid/a", Parser: "json", Path: "v"}, "https://example.invalid/a"); err != nil {
		t.Fatal(err)
	}
	if err := b.Set("x/b", &PackageConfig{URL: "https://example.invalid/b", Parser: "json", Path: "v"}, "https://example.invalid/b"); err != nil {
		t.Fatal(err)
	}
	reloaded, _ := NewAnalysisCache(dir)
	for _, k := range []string{"x/a", "x/b"} {
		if _, ok := reloaded.GetEntry(k); !ok {
			t.Errorf("analysis_cache.json lost %s", k)
		}
	}
}
