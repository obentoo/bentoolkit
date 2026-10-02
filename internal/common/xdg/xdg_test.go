package xdg_test

import (
	"path/filepath"
	"testing"

	"github.com/obentoo/bentoolkit/internal/common/xdg"
)

// envOf returns a getenv function backed by a fixed map, so the test never
// reads or mutates the real process environment.
func envOf(vars map[string]string) func(string) string {
	return func(key string) string { return vars[key] }
}

// TestStateHome_UsesAbsoluteXDGStateHome is R10.1: an absolute XDG_STATE_HOME
// is the state base directory, verbatim.
func TestStateHome_UsesAbsoluteXDGStateHome(t *testing.T) {
	got := xdg.StateHome(envOf(map[string]string{"XDG_STATE_HOME": "/srv/state"}), "/home/alice")
	if got != "/srv/state" {
		t.Fatalf("StateHome = %q, want /srv/state (an absolute XDG_STATE_HOME is used as given)", got)
	}
}

// TestStateHome_UnsetFallsBackToHomeLocalState is R10.1: with XDG_STATE_HOME
// unset the base is ~/.local/state.
func TestStateHome_UnsetFallsBackToHomeLocalState(t *testing.T) {
	got := xdg.StateHome(envOf(nil), "/home/alice")
	want := filepath.Join("/home/alice", ".local", "state")
	if got != want {
		t.Fatalf("StateHome = %q, want %q", got, want)
	}
}

// TestStateHome_IgnoresInvalidValues is the hostile half: the XDG Base
// Directory spec says a relative path in XDG_STATE_HOME is invalid and must be
// ignored. A relative value used verbatim would put state.json under whatever
// directory the session manager happened to start the process in.
func TestStateHome_IgnoresInvalidValues(t *testing.T) {
	want := filepath.Join("/home/alice", ".local", "state")
	for _, value := range []string{"", "relative/state", "./state", "state"} {
		t.Run(value, func(t *testing.T) {
			got := xdg.StateHome(envOf(map[string]string{"XDG_STATE_HOME": value}), "/home/alice")
			if got != want {
				t.Fatalf("XDG_STATE_HOME=%q: StateHome = %q, want the fallback %q", value, got, want)
			}
		})
	}
}

// TestStateHome_ReadsOnlyXDGStateHome guards the converse: a neighbouring XDG
// variable must not be mistaken for the state home.
func TestStateHome_ReadsOnlyXDGStateHome(t *testing.T) {
	env := envOf(map[string]string{
		"XDG_CONFIG_HOME": "/srv/config",
		"XDG_DATA_HOME":   "/srv/data",
		"XDG_CACHE_HOME":  "/srv/cache",
	})
	got := xdg.StateHome(env, "/home/alice")
	want := filepath.Join("/home/alice", ".local", "state")
	if got != want {
		t.Fatalf("StateHome = %q, want %q (only XDG_STATE_HOME names the state home)", got, want)
	}
}
