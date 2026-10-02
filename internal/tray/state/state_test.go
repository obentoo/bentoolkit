package state_test

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/obentoo/bentoolkit/internal/tray/state"
)

func sample() state.State {
	ts := time.Date(2026, 10, 2, 14, 0, 0, 0, time.UTC)
	return state.State{
		Format: 1,
		ETag:   `"abc"`,
		Serial: 1790812800,
		Notices: map[string]state.Record{
			"2026-10-02-foo-cve": {
				Title: "foo 1.2 heap overflow", Summary: "Upgrade foo to 1.2.3.", URL: "https://obentoo.org/notices/2026-10-02-foo-cve/",
				Severity: "critical", Type: "security", Read: false, Notified: true,
				LastSeen: ts, Updated: ts,
			},
			"2026-09-25-gui+tray": {Title: "Desktop notices", Severity: "info", Type: "announcement", Read: true, LastSeen: ts},
		},
		PauseUntil: ts.Add(time.Hour),
		Failures:   3,
	}
}

// TestLoad_MissingFileIsAFirstRun is R6.9's trigger: no file means a state that
// was never saved, and loading creates nothing.
func TestLoad_MissingFileIsAFirstRun(t *testing.T) {
	path := filepath.Join(t.TempDir(), "bentoo-notices", "state.json")
	st, err := state.Open(path).Load()
	if err != nil {
		t.Fatalf("Load of a missing file: %v", err)
	}
	if st.Saved {
		t.Error("Saved = true for a state that was never saved")
	}
	if len(st.Notices) != 0 || st.Serial != 0 || st.ETag != "" {
		t.Errorf("first-run state is not empty: %+v", st)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("Load created %s", path)
	}
}

// TestSave_RoundTripsEveryField is R10.1.
func TestSave_RoundTripsEveryField(t *testing.T) {
	path := filepath.Join(t.TempDir(), "bentoo-notices", "state.json")
	store := state.Open(path)
	in := sample()
	if err := store.Save(in); err != nil {
		t.Fatalf("Save: %v", err)
	}
	out, err := store.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !out.Saved {
		t.Error("Saved = false after a save; the next start would be treated as a first run")
	}
	if out.ETag != in.ETag || out.Serial != in.Serial || out.Failures != in.Failures || !out.PauseUntil.Equal(in.PauseUntil) {
		t.Errorf("Load = %+v, want %+v", out, in)
	}
	if len(out.Notices) != len(in.Notices) {
		t.Fatalf("Notices = %d, want %d", len(out.Notices), len(in.Notices))
	}
	for id, w := range in.Notices {
		g := out.Notices[id]
		if g.Title != w.Title || g.Summary != w.Summary || g.URL != w.URL || g.Severity != w.Severity || g.Type != w.Type ||
			g.Read != w.Read || g.Notified != w.Notified || !g.LastSeen.Equal(w.LastSeen) || !g.Updated.Equal(w.Updated) {
			t.Errorf("record %s = %+v, want %+v", id, g, w)
		}
	}
}

// TestSave_WritesMode0600WithAFormatField is R10.2, including the directory it
// creates.
func TestSave_WritesMode0600WithAFormatField(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "state-home", "bentoo-notices")
	path := filepath.Join(dir, "state.json")
	in := sample()
	in.Saved = true
	if err := state.Open(path).Save(in); err != nil {
		t.Fatalf("Save: %v", err)
	}
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Errorf("state.json mode = %v, want 0600", fi.Mode().Perm())
	}
	di, err := os.Stat(dir)
	if err != nil {
		t.Fatal(err)
	}
	if di.Mode().Perm() != 0o700 {
		t.Errorf("state directory mode = %v, want 0700", di.Mode().Perm())
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("state.json is not JSON: %v", err)
	}
	if doc["format"] != float64(2) {
		t.Errorf("format = %v, want 2", doc["format"])
	}
	for _, key := range []string{"etag", "serial", "notices", "pause_until", "failures"} {
		if _, ok := doc[key]; !ok {
			t.Errorf("state.json lacks the %q key of the data model: %s", key, raw)
		}
	}
	// Saved means "a file was loaded or written"; it is derived, never stored.
	for key := range doc {
		if strings.EqualFold(key, "saved") {
			t.Errorf("state.json serializes %q: %s", key, raw)
		}
	}
}

// TestSave_AlwaysWritesTheCurrentFormat: a State built in memory without a
// Format must not be saved as an "unknown format" the next start would move
// aside.
func TestSave_AlwaysWritesTheCurrentFormat(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	store := state.Open(path)
	in := sample()
	in.Format = 0
	if err := store.Save(in); err != nil {
		t.Fatalf("Save: %v", err)
	}
	out, err := store.Load()
	if err != nil || !out.Saved || out.Serial != in.Serial {
		t.Errorf("Load after saving a zero-Format state = %+v, %v; want the saved state back", out, err)
	}
	assertNoCorruptCopy(t, path)
}

func corruptCopies(t *testing.T, path string) []string {
	t.Helper()
	matches, err := filepath.Glob(path + ".corrupt-*")
	if err != nil {
		t.Fatal(err)
	}
	return matches
}

func assertNoCorruptCopy(t *testing.T, path string) {
	t.Helper()
	if c := corruptCopies(t, path); len(c) != 0 {
		t.Errorf("a valid state file was moved aside: %q", c)
	}
}

// TestLoad_DamagedFileIsMovedAsideAndStartsFresh is R10.3.
func TestLoad_DamagedFileIsMovedAsideAndStartsFresh(t *testing.T) {
	suffix := regexp.MustCompile(`\.corrupt-[0-9]+$`)
	for name, content := range map[string]string{
		"not json":       "{this is not json",
		"truncated":      `{"format":1,"etag":"x","notices":{`,
		"unknown format": `{"format":3,"etag":"\"abc\"","serial":5,"notices":{}}`,
		"no format":      `{"etag":"\"abc\"","serial":5,"notices":{}}`,
	} {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "state.json")
			if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
				t.Fatal(err)
			}
			before := time.Now().Unix()
			st, err := state.Open(path).Load()
			if err == nil {
				t.Error("a damaged state file produced no error, so nothing can WARN about it")
			} else if !strings.Contains(err.Error(), path) {
				t.Errorf("error %q does not name %s", err, path)
			}
			if st.Saved || len(st.Notices) != 0 || st.Serial != 0 {
				t.Errorf("Load = %+v, want a fresh first-run state", st)
			}
			if _, err := os.Stat(path); !os.IsNotExist(err) {
				t.Errorf("the damaged file is still at %s", path)
			}
			copies := corruptCopies(t, path)
			if len(copies) != 1 || !suffix.MatchString(copies[0]) {
				t.Fatalf("moved-aside copies = %q, want one %s.corrupt-<unix time>", copies, path)
			}
			var ts int64
			for _, c := range strings.TrimPrefix(copies[0], path+".corrupt-") {
				ts = ts*10 + int64(c-'0')
			}
			if ts < before-5 || ts > time.Now().Unix()+5 {
				t.Errorf("suffix %d is not the current unix time", ts)
			}
			raw, _ := os.ReadFile(copies[0])
			if string(raw) != content {
				t.Errorf("the moved-aside copy does not hold the original bytes")
			}
		})
	}
}

// TestLoad_ValidFileIsNeverMovedAside is the converse of the damage rule: a
// valid file with a field this version does not know is not "damaged".
func TestLoad_ValidFileIsNeverMovedAside(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	content := `{"format":1,"etag":"\"abc\"","serial":7,"notices":{},"pause_until":"0001-01-01T00:00:00Z","failures":0,"future_field":true}`
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	st, err := state.Open(path).Load()
	if err != nil {
		t.Fatalf("Load of a valid format-1 file: %v", err)
	}
	if st.Serial != 7 || !st.Saved {
		t.Errorf("Load = %+v, want serial 7 and Saved", st)
	}
	assertNoCorruptCopy(t, path)
}

// TestSave_InterruptedSaveKeepsThePreviousState is Q18: a save that cannot
// complete leaves the previous file byte-identical.
func TestSave_InterruptedSaveKeepsThePreviousState(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root writes into a read-only directory; the failure cannot be injected")
	}
	dir := filepath.Join(t.TempDir(), "bentoo-notices")
	path := filepath.Join(dir, "state.json")
	store := state.Open(path)
	if err := store.Save(sample()); err != nil {
		t.Fatalf("first Save: %v", err)
	}
	previous, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })

	next := sample()
	next.Serial = 1800000000
	next.ETag = `"changed"`
	err = store.Save(next)
	if err == nil {
		t.Fatal("Save into a read-only directory reported success")
	}
	if !strings.Contains(err.Error(), path) {
		t.Errorf("error %q does not name %s", err, path)
	}
	now, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("the previous state file is gone: %v", err)
	}
	if !bytes.Equal(now, previous) {
		t.Error("the previous state file was modified by a failed save")
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	st, err := store.Load()
	if err != nil || st.Serial != 1790812800 {
		t.Errorf("Load after a failed save = %+v, %v; want the previous state", st, err)
	}
}

// TestPrune_ForgetsRecordsUnseenForMoreThanNinetyDays is R10.4 with both sides
// of the boundary.
func TestPrune_ForgetsRecordsUnseenForMoreThanNinetyDays(t *testing.T) {
	now := time.Date(2026, 12, 31, 12, 0, 0, 0, time.UTC)
	day := 24 * time.Hour
	st := state.State{Format: 1, Notices: map[string]state.Record{
		"old":     {LastSeen: now.Add(-91 * day)},
		"exact90": {LastSeen: now.Add(-90 * day)},
		"recent":  {LastSeen: now.Add(-89 * day)},
		"today":   {LastSeen: now, Read: true},
	}}
	st.Prune(now)
	if _, ok := st.Notices["old"]; ok {
		t.Error("a record unseen for 91 days survived Prune")
	}
	for _, id := range []string{"exact90", "recent", "today"} {
		if _, ok := st.Notices[id]; !ok {
			t.Errorf("record %q was pruned; it was seen within 90 days", id)
		}
	}
}
