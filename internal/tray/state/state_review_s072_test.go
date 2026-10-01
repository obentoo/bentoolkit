package state_test

// Regression tests from the tech review of sub-task 12.4 (story 072, refine
// v2): State.Established ends R6.9's first run, so a first run's state can be
// saved (and its Retry-After kept) without ending the first run.

import (
	"path/filepath"
	"testing"

	"github.com/obentoo/bentoolkit/internal/tray/state"
)

// TestStore_EstablishedLoadRules: a format 1 file was written by a tray past
// its first run, so it loads established; a format 2 file carries the flag
// under "established", and one without the key is still a first run.
func TestStore_EstablishedLoadRules(t *testing.T) {
	// Hostile half first: the migration must not turn a format 1 user into a
	// first run, which would mark every current notice read unnotified.
	t.Run("format 1 loads established", func(t *testing.T) {
		st, err := state.Open(s072WriteState(t, s072FormatOneFile)).Load()
		if err != nil {
			t.Fatalf("Load of a format 1 file: %v", err)
		}
		if !st.Established {
			t.Error("Established = false for a migrated format 1 file; the next start would be a first run (R6.9)")
		}
	})

	for name, tc := range map[string]struct {
		content string
		want    bool
	}{
		"format 2 without the key is a first run": {
			content: `{"format":2,"etag":"","serial":0,"notices":{},"failures":1,"next_fetch":"2026-10-01T13:00:00Z"}`,
			want:    false,
		},
		"format 2 established false is a first run": {
			content: `{"format":2,"notices":{},"established":false}`,
			want:    false,
		},
		"format 2 established true is not": {
			content: `{"format":2,"notices":{},"established":true}`,
			want:    true,
		},
	} {
		t.Run(name, func(t *testing.T) {
			st, err := state.Open(s072WriteState(t, tc.content)).Load()
			if err != nil {
				t.Fatalf("Load: %v", err)
			}
			if !st.Saved {
				t.Error("Saved = false for a loaded file")
			}
			if st.Established != tc.want {
				t.Errorf("Established = %v, want %v", st.Established, tc.want)
			}
		})
	}

	t.Run("a missing file is a first run", func(t *testing.T) {
		st, err := state.Open(filepath.Join(t.TempDir(), "state.json")).Load()
		if err != nil {
			t.Fatalf("Load: %v", err)
		}
		if st.Established {
			t.Error("Established = true with no state file")
		}
	})
}

// TestStore_EstablishedRoundTrips: the flag is stored under "established" and
// read back both ways, and a migrated format 1 file keeps it after its save.
func TestStore_EstablishedRoundTrips(t *testing.T) {
	for _, want := range []bool{false, true} {
		path := filepath.Join(t.TempDir(), "bentoo-notices", "state.json")
		store := state.Open(path)
		in := sample()
		in.Established = want
		if err := store.Save(in); err != nil {
			t.Fatalf("Save: %v", err)
		}
		doc := s072DiskDoc(t, path)
		if got, ok := doc["established"]; !ok || got != want {
			t.Errorf("stored \"established\" = %v (present %v), want %v", got, ok, want)
		}
		out, err := store.Load()
		if err != nil {
			t.Fatalf("Load: %v", err)
		}
		if out.Established != want {
			t.Errorf("Established = %v after a round trip, want %v", out.Established, want)
		}
	}

	path := s072WriteState(t, s072FormatOneFile)
	store := state.Open(path)
	st, err := store.Load()
	if err != nil {
		t.Fatalf("Load of a format 1 file: %v", err)
	}
	if err := store.Save(st); err != nil {
		t.Fatalf("Save after migration: %v", err)
	}
	again, err := store.Load()
	if err != nil {
		t.Fatalf("Load of the migrated file: %v", err)
	}
	if !again.Established {
		t.Error("a migrated format 1 file lost Established on its first format 2 save")
	}
}
