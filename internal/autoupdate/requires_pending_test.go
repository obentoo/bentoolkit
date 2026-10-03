package autoupdate

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// The pending entry carries the captured requirement versions (atom → version)
// from --check to --apply, through pending.json on disk. The field is optional,
// so a pending.json written by the previous release still loads.

// TestRequiresPendingRoundTrip: the map survives a write and a fresh load, and
// it stays on the entry it belongs to. The hostile half is the second entry:
// a labelled entry for the required package itself, written with no
// requirement, must come back with none — a map shared between entries, or
// keyed by atom rather than by entry, would hand it flutter's.
func TestRequiresPendingRoundTrip(t *testing.T) {
	dir := t.TempDir()
	pending, err := NewPendingList(dir)
	if err != nil {
		t.Fatalf("NewPendingList: %v", err)
	}
	want := map[string]string{"dev-lang/dart": "3.14.0", "dev-lang/dart-sdk": "3.14.0_rc1"}
	if err := pending.Add(PendingUpdate{
		Package: "dev-lang/flutter", CurrentVersion: "3.47.0", NewVersion: "3.48.0",
		Status: StatusPending, Requires: want,
	}); err != nil {
		t.Fatalf("Add flutter: %v", err)
	}
	if err := pending.Add(PendingUpdate{
		Package: "dev-lang/dart@stable", CurrentVersion: "3.13.5", NewVersion: "3.14.0",
		Status: StatusPending,
	}); err != nil {
		t.Fatalf("Add dart: %v", err)
	}

	reloaded, err := NewPendingList(dir)
	if err != nil {
		t.Fatalf("NewPendingList (reload): %v", err)
	}
	flutter, ok := reloaded.Get("dev-lang/flutter")
	if !ok {
		t.Fatal("flutter entry lost on reload")
	}
	if !reflect.DeepEqual(flutter.Requires, want) {
		t.Errorf("flutter Requires after reload = %#v, want %#v", flutter.Requires, want)
	}
	dart, ok := reloaded.Get("dev-lang/dart@stable")
	if !ok {
		t.Fatal("dart@stable entry lost on reload")
	}
	if len(dart.Requires) != 0 {
		t.Errorf("dart@stable gained requirements it never had: %#v", dart.Requires)
	}
}

// TestRequiresPendingPreviousReleaseFileLoads: a pending.json written before
// this story (no requires key at all) loads unchanged, with no requirement on
// the entry, and saving an entry without requirements writes no requires key —
// so the file a previous release reads back is the shape it always read.
func TestRequiresPendingPreviousReleaseFileLoads(t *testing.T) {
	dir := t.TempDir()
	old := `{
  "updates": {
    "dev-lang/flutter": {
      "package": "dev-lang/flutter",
      "current_version": "3.47.0",
      "new_version": "3.48.0",
      "aux_value": "esr-bb24",
      "status": "pending",
      "detected_at": "2026-09-30T10:00:00Z"
    }
  }
}
`
	if err := os.WriteFile(filepath.Join(dir, "pending.json"), []byte(old), 0o600); err != nil {
		t.Fatalf("write pending.json: %v", err)
	}
	pending, err := NewPendingList(dir)
	if err != nil {
		t.Fatalf("NewPendingList: %v", err)
	}
	got, ok := pending.Get("dev-lang/flutter")
	if !ok {
		t.Fatal("an entry written by the previous release did not load")
	}
	if got.NewVersion != "3.48.0" || got.CurrentVersion != "3.47.0" || got.AuxValue != "esr-bb24" || got.Status != StatusPending {
		t.Errorf("previous-release entry loaded as %+v", got)
	}
	if got.Requires != nil {
		t.Errorf("Requires = %#v on an entry that never had the field, want nil", got.Requires)
	}

	if err := pending.Add(PendingUpdate{
		Package: "app-misc/plain", CurrentVersion: "1.0", NewVersion: "1.1", Status: StatusPending,
	}); err != nil {
		t.Fatalf("Add: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(dir, "pending.json"))
	if err != nil {
		t.Fatalf("read pending.json: %v", err)
	}
	if strings.Contains(string(data), `"requires"`) {
		t.Errorf("pending.json gained a requires key for entries without requirements:\n%s", data)
	}
}
