package snapshot

import (
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// withStateDir redirects the package StateDir var at a t.TempDir() for the
// duration of the test, mirroring the var-redirect style in result_test.go and
// detect_test.go (save the original, restore via t.Cleanup).
func withStateDir(t *testing.T) string {
	t.Helper()
	orig := StateDir
	t.Cleanup(func() { StateDir = orig })
	dir := t.TempDir()
	StateDir = func() string { return dir }
	return dir
}

func sampleSnapshot() Snapshot {
	return Snapshot{
		ID:        "btrbk-home-20260608",
		Subvolume: "/home",
		Path:      "/.snapshots/home.20260608",
		CreatedAt: time.Date(2026, 6, 8, 12, 0, 0, 0, time.UTC),
		ReadOnly:  true,
		ParentID:  "btrbk-home-20260607",
	}
}

func TestParentStore_RoundTrip(t *testing.T) {
	withStateDir(t)
	ps := newParentStore()
	snap := sampleSnapshot()

	if err := ps.Record("/home", "offsite", snap); err != nil {
		t.Fatalf("Record: %v", err)
	}

	got, ok, err := ps.Last("/home", "offsite")
	if err != nil {
		t.Fatalf("Last: %v", err)
	}
	if !ok {
		t.Fatalf("Last ok = false, want true after Record")
	}
	if got.ID != snap.ID || got.Subvolume != snap.Subvolume || got.Path != snap.Path {
		t.Errorf("round-trip mismatch: got %+v, want ID/Subvolume/Path of %+v", got, snap)
	}
	if got.ParentID != snap.ParentID {
		t.Errorf("ParentID = %q, want %q", got.ParentID, snap.ParentID)
	}
	if !got.CreatedAt.Equal(snap.CreatedAt) {
		t.Errorf("CreatedAt = %v, want %v", got.CreatedAt, snap.CreatedAt)
	}
}

func TestParentStore_FirstRunNotAnError(t *testing.T) {
	withStateDir(t)
	ps := newParentStore()

	got, ok, err := ps.Last("/home", "offsite")
	if err != nil {
		t.Fatalf("Last on unrecorded key returned error: %v (want nil)", err)
	}
	if ok {
		t.Errorf("ok = true for unrecorded key, want false")
	}
	if got != (Snapshot{}) {
		t.Errorf("snap = %+v, want zero value on first run", got)
	}
}

func TestParentStore_DistinctKeysNoCollision(t *testing.T) {
	dir := withStateDir(t)
	ps := newParentStore()

	// Subvolumes with slashes and other unsafe path chars, plus distinct ships,
	// must map to distinct files that round-trip independently.
	type entry struct {
		subvol, ship, id string
	}
	entries := []entry{
		{"/home", "offsite", "id-home-offsite"},
		{"/home", "backup2", "id-home-backup2"},
		{"/", "offsite", "id-root-offsite"},
		{"/mnt/data", "offsite", "id-mnt-data-offsite"},
		{"/mnt/data", "backup2", "id-mnt-data-backup2"},
	}

	for _, e := range entries {
		snap := Snapshot{ID: e.id, Subvolume: e.subvol, Path: "/snap/" + e.id}
		if err := ps.Record(e.subvol, e.ship, snap); err != nil {
			t.Fatalf("Record(%q,%q): %v", e.subvol, e.ship, err)
		}
	}

	// Each key must read back its own ID — proves no two keys collided onto one file.
	for _, e := range entries {
		got, ok, err := ps.Last(e.subvol, e.ship)
		if err != nil {
			t.Fatalf("Last(%q,%q): %v", e.subvol, e.ship, err)
		}
		if !ok {
			t.Fatalf("Last(%q,%q) ok = false, want true", e.subvol, e.ship)
		}
		if got.ID != e.id {
			t.Errorf("Last(%q,%q).ID = %q, want %q (collision?)", e.subvol, e.ship, got.ID, e.id)
		}
	}

	// All persisted files must live UNDER the parents dir (no path-escape from the
	// slash-laden subvol names).
	parentsDir := filepath.Join(dir, "parents")
	matches, err := filepath.Glob(filepath.Join(parentsDir, "*"))
	if err != nil {
		t.Fatalf("glob: %v", err)
	}
	if len(matches) != len(entries) {
		t.Errorf("got %d files under parents dir, want %d (collision or stray file): %v",
			len(matches), len(entries), matches)
	}
	for _, m := range matches {
		if !strings.HasPrefix(filepath.Clean(m), filepath.Clean(parentsDir)+string(filepath.Separator)) {
			t.Errorf("persisted file %q escaped parents dir %q", m, parentsDir)
		}
	}
}

func TestParentStore_OverwriteLatestWins(t *testing.T) {
	withStateDir(t)
	ps := newParentStore()

	first := Snapshot{ID: "first", Subvolume: "/home", Path: "/snap/first"}
	second := Snapshot{ID: "second", Subvolume: "/home", Path: "/snap/second"}

	if err := ps.Record("/home", "offsite", first); err != nil {
		t.Fatalf("Record first: %v", err)
	}
	if err := ps.Record("/home", "offsite", second); err != nil {
		t.Fatalf("Record second: %v", err)
	}

	got, ok, err := ps.Last("/home", "offsite")
	if err != nil || !ok {
		t.Fatalf("Last after overwrite: ok=%v err=%v", ok, err)
	}
	if got.ID != "second" {
		t.Errorf("ID = %q, want %q (latest Record must win)", got.ID, "second")
	}
}
