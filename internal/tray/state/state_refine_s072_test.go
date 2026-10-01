package state_test

// Refine v2 of story 072, sub-task 12.3: state format 2 (R10.1, R10.3, R10.5).
// Format 2 adds the earliest next fetch time and each record's source; a
// format 1 file is migrated on load instead of being moved aside.

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"testing"
	"time"

	"github.com/obentoo/bentoolkit/internal/tray/state"
)

// s072FormatOneFile is a state.json exactly as the format 1 tray wrote it: no
// "source" in any record and no "next_fetch".
const s072FormatOneFile = `{
  "format": 1,
  "etag": "\"abc\"",
  "serial": 1790812800,
  "notices": {
    "2026-10-02-foo-cve": {
      "title": "foo 1.2 heap overflow",
      "summary": "Upgrade foo to 1.2.3.",
      "url": "https://obentoo.org/notices/2026-10-02-foo-cve/",
      "severity": "critical",
      "type": "security",
      "read": false,
      "notified": true,
      "last_seen": "2026-10-02T14:00:00Z",
      "updated": "2026-10-02T14:00:00Z"
    },
    "2026-09-25-gui+tray": {
      "title": "Desktop notices",
      "summary": "",
      "url": "https://obentoo.org/notices/2026-09-25-gui+tray/",
      "severity": "info",
      "type": "announcement",
      "read": true,
      "notified": false,
      "last_seen": "2026-10-02T14:00:00Z",
      "updated": "0001-01-01T00:00:00Z"
    },
    "bentoo:2026-09-01-news-item": {
      "title": "A news item",
      "summary": "",
      "url": "",
      "severity": "info",
      "type": "news",
      "read": false,
      "notified": false,
      "last_seen": "2026-10-02T14:00:00Z",
      "updated": "0001-01-01T00:00:00Z"
    }
  },
  "pause_until": "2026-10-02T15:00:00Z",
  "failures": 3
}`

// s072FormatTwoFile is the design's format 2 example: a feed record, a news
// record and a migrated record whose source is still unknown.
const s072FormatTwoFile = `{
  "format": 2,
  "etag": "\"def\"",
  "serial": 1790900000,
  "notices": {
    "2026-09-30-foo-cve": {"title": "t1", "summary": "s1", "url": "https://obentoo.org/notices/2026-09-30-foo-cve/", "severity": "critical", "type": "security", "source": "feed", "read": false, "notified": true, "last_seen": "2026-10-01T10:00:00Z", "updated": "2026-09-30T00:00:00Z"},
    "bentoo:2026-09-01-news-item": {"title": "t2", "summary": "", "url": "", "severity": "info", "type": "news", "source": "news", "read": false, "notified": false, "last_seen": "2026-10-01T10:00:00Z", "updated": "0001-01-01T00:00:00Z"},
    "2026-08-01-old": {"title": "t3", "summary": "", "url": "", "severity": "info", "type": "announcement", "source": "", "read": true, "notified": true, "last_seen": "2026-10-01T10:00:00Z", "updated": "0001-01-01T00:00:00Z"}
  },
  "pause_until": "0001-01-01T00:00:00Z",
  "failures": 0,
  "next_fetch": "2026-10-01T16:30:00Z"
}`

func s072WriteState(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "bentoo-notices", "state.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// s072DiskDoc decodes the state file on disk as generic JSON, so the test sees
// the stored keys and not what Load makes of them.
func s072DiskDoc(t *testing.T, path string) map[string]any {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("%s is not JSON: %v\n%s", path, err, raw)
	}
	return doc
}

// TestStore_LoadAcceptsFormatsOneAndTwoAndMovesAsideOthers is R10.3 with both
// halves of the version rule. The hostile half comes first: a format past 2
// must still be treated as damage, not accepted because it is "at least 1".
// Then the converse: formats 1 and 2 must never be moved aside.
func TestStore_LoadAcceptsFormatsOneAndTwoAndMovesAsideOthers(t *testing.T) {
	suffix := regexp.MustCompile(`\.corrupt-[0-9]+$`)
	for _, format := range []string{"3", "99"} {
		t.Run("format "+format+" is moved aside", func(t *testing.T) {
			content := `{"format":` + format + `,"etag":"\"abc\"","serial":5,"notices":{"x":{"title":"t","source":"feed"}},"next_fetch":"2026-10-01T16:30:00Z"}`
			path := s072WriteState(t, content)
			st, err := state.Open(path).Load()
			if !errors.Is(err, state.ErrCorrupt) {
				t.Errorf("Load of format %s: err = %v, want ErrCorrupt", format, err)
			}
			if st.Saved || len(st.Notices) != 0 || st.Serial != 0 || !st.NextFetch.IsZero() {
				t.Errorf("Load of format %s = %+v, want a fresh first-run state", format, st)
			}
			if _, err := os.Stat(path); !os.IsNotExist(err) {
				t.Errorf("the format %s file is still at %s", format, path)
			}
			copies := corruptCopies(t, path)
			if len(copies) != 1 || !suffix.MatchString(copies[0]) {
				t.Fatalf("moved-aside copies = %q, want one %s.corrupt-<unix time>", copies, path)
			}
			if raw, _ := os.ReadFile(copies[0]); string(raw) != content {
				t.Error("the moved-aside copy does not hold the original bytes")
			}
		})
	}
	for name, content := range map[string]string{"format 1": s072FormatOneFile, "format 2": s072FormatTwoFile} {
		t.Run(name+" loads", func(t *testing.T) {
			path := s072WriteState(t, content)
			st, err := state.Open(path).Load()
			if err != nil {
				t.Fatalf("Load of a valid %s file: %v", name, err)
			}
			if !st.Saved || len(st.Notices) != 3 {
				t.Errorf("Load of %s = Saved %v, %d notices; want Saved and 3 notices", name, st.Saved, len(st.Notices))
			}
			assertNoCorruptCopy(t, path)
			if raw, err := os.ReadFile(path); err != nil || string(raw) != content {
				t.Errorf("Load of %s changed or removed the file (err %v)", name, err)
			}
		})
	}
}

// TestStore_FormatOneFileMigratesToFormatTwo is R10.5: every record of a
// format 1 file is kept with an unknown source, nothing is moved aside, and the
// next Save writes format 2 that loads back with the same records.
func TestStore_FormatOneFileMigratesToFormatTwo(t *testing.T) {
	path := s072WriteState(t, s072FormatOneFile)
	store := state.Open(path)
	st, err := store.Load()
	if err != nil {
		t.Fatalf("Load of a format 1 file: %v", err)
	}
	assertNoCorruptCopy(t, path)
	if !st.Saved {
		t.Error("Saved = false for a migrated file; the next start would be a first run (R6.9)")
	}
	if st.ETag != `"abc"` || st.Serial != 1790812800 || st.Failures != 3 ||
		!st.PauseUntil.Equal(time.Date(2026, 10, 2, 15, 0, 0, 0, time.UTC)) {
		t.Errorf("Load of format 1 = %+v; ETag, serial, failures or pause end were lost", st)
	}
	if !st.NextFetch.IsZero() {
		t.Errorf("NextFetch = %v for a format 1 file that has none, want zero", st.NextFetch)
	}
	want := map[string]struct {
		read, notified bool
		typ            string
	}{
		"2026-10-02-foo-cve":          {false, true, "security"},
		"2026-09-25-gui+tray":         {true, false, "announcement"},
		"bentoo:2026-09-01-news-item": {false, false, "news"},
	}
	if len(st.Notices) != len(want) {
		t.Fatalf("migrated notices = %d, want %d: %+v", len(st.Notices), len(want), st.Notices)
	}
	for id, w := range want {
		r, ok := st.Notices[id]
		if !ok {
			t.Errorf("record %q was dropped by the migration", id)
			continue
		}
		if r.Source != "" {
			t.Errorf("record %q Source = %q, want \"\" (unknown) for a format 1 record", id, r.Source)
		}
		if r.Read != w.read || r.Notified != w.notified || r.Type != w.typ {
			t.Errorf("record %q = %+v; read %v, notified %v, type %q not kept", id, r, w.read, w.notified, w.typ)
		}
	}

	if err := store.Save(st); err != nil {
		t.Fatalf("Save after migration: %v", err)
	}
	doc := s072DiskDoc(t, path)
	if doc["format"] != float64(2) {
		t.Errorf("the save after a format 1 load wrote format %v, want 2", doc["format"])
	}
	again, err := store.Load()
	if err != nil {
		t.Fatalf("Load of the migrated file: %v", err)
	}
	assertNoCorruptCopy(t, path)
	if len(again.Notices) != len(want) || again.Serial != 1790812800 {
		t.Errorf("reload after migration = %d notices, serial %d; want %d and 1790812800", len(again.Notices), again.Serial, len(want))
	}
	for id := range want {
		if r := again.Notices[id]; r.Source != "" {
			t.Errorf("record %q gained Source %q on save; a migrated source stays unknown until a source lists it", id, r.Source)
		}
	}
}

// TestStore_NextFetchAndSourceRoundTrip is R10.1: the next fetch time and each
// record's source are written under "next_fetch" and "source" and read back.
// Records of different sources with the same notice data must stay distinct.
func TestStore_NextFetchAndSourceRoundTrip(t *testing.T) {
	t.Run("save then load", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "bentoo-notices", "state.json")
		store := state.Open(path)
		next := time.Date(2026, 10, 1, 16, 30, 0, 0, time.FixedZone("BRT", -3*3600))
		in := sample()
		in.NextFetch = next
		ts := time.Date(2026, 10, 1, 10, 0, 0, 0, time.UTC)
		in.Notices = map[string]state.Record{
			"same-feed":     {Title: "Same", Severity: "info", Type: "announcement", Source: "feed", LastSeen: ts},
			"same-news":     {Title: "Same", Severity: "info", Type: "announcement", Source: "news", LastSeen: ts},
			"same-migrated": {Title: "Same", Severity: "info", Type: "announcement", Source: "", LastSeen: ts},
		}
		if err := store.Save(in); err != nil {
			t.Fatalf("Save: %v", err)
		}

		doc := s072DiskDoc(t, path)
		if doc["format"] != float64(2) {
			t.Errorf("format = %v, want 2", doc["format"])
		}
		if _, ok := doc["next_fetch"]; !ok {
			t.Errorf("state.json lacks the \"next_fetch\" key: %v", doc)
		}
		notices, _ := doc["notices"].(map[string]any)
		for id, src := range map[string]string{"same-feed": "feed", "same-news": "news"} {
			rec, _ := notices[id].(map[string]any)
			if rec["source"] != src {
				t.Errorf("stored record %q source = %v, want %q", id, rec["source"], src)
			}
		}

		out, err := store.Load()
		if err != nil {
			t.Fatalf("Load: %v", err)
		}
		if !out.NextFetch.Equal(next) {
			t.Errorf("NextFetch = %v, want %v", out.NextFetch, next)
		}
		for id, src := range map[string]string{"same-feed": "feed", "same-news": "news", "same-migrated": ""} {
			if g := out.Notices[id].Source; g != src {
				t.Errorf("record %q Source = %q, want %q", id, g, src)
			}
		}
		if out.ETag != in.ETag || out.Serial != in.Serial || out.Failures != in.Failures || !out.PauseUntil.Equal(in.PauseUntil) {
			t.Errorf("Load = %+v; a format 1 field was lost next to the new ones", out)
		}
	})

	t.Run("a format 2 file decodes", func(t *testing.T) {
		path := s072WriteState(t, s072FormatTwoFile)
		st, err := state.Open(path).Load()
		if err != nil {
			t.Fatalf("Load of a format 2 file: %v", err)
		}
		if want := time.Date(2026, 10, 1, 16, 30, 0, 0, time.UTC); !st.NextFetch.Equal(want) {
			t.Errorf("NextFetch = %v, want %v", st.NextFetch, want)
		}
		for id, src := range map[string]string{"2026-09-30-foo-cve": "feed", "bentoo:2026-09-01-news-item": "news", "2026-08-01-old": ""} {
			r, ok := st.Notices[id]
			if !ok {
				t.Errorf("record %q missing", id)
				continue
			}
			if r.Source != src {
				t.Errorf("record %q Source = %q, want %q", id, r.Source, src)
			}
		}
	})

	t.Run("a zero next fetch survives", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "state.json")
		store := state.Open(path)
		in := sample()
		if err := store.Save(in); err != nil {
			t.Fatalf("Save: %v", err)
		}
		out, err := store.Load()
		if err != nil {
			t.Fatalf("Load: %v", err)
		}
		if !out.NextFetch.IsZero() {
			t.Errorf("NextFetch = %v after saving a zero one, want zero (no stored deadline)", out.NextFetch)
		}
	})
}
