package distfiles

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// These tests pin S050-R6.1..R6.4: a path whose first element is "~" followed
// by anything other than "/" or the end of the string is another user's home
// (or no home at all), and is refused instead of being read as "~/name".
//
// Every case sets $HOME to a sandbox and PRE-CREATES the directory the misread
// would land on ($HOME/alice/...), so a silent misread is observable rather
// than failing for an unrelated "does not exist" reason.

// otherUserHomeForms are refused. "~./x" and "~~/x" are included because the
// rule is "anything other than /", not "a letter": the misread of "~./x" is
// exactly $HOME/x.
var otherUserHomeForms = []string{"~alice", "~alice/distfiles", "~./x", "~~/x"}

func sandboxHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	return home
}

// misreadTarget is where today's expansion puts a "~name..." path.
func misreadTarget(home, path string) string {
	return filepath.Join(home, strings.TrimPrefix(path, "~"))
}

func TestExpandPathRefusesOtherUsersHome(t *testing.T) {
	for _, path := range otherUserHomeForms {
		t.Run(path, func(t *testing.T) {
			home := sandboxHome(t)
			got, err := expandPath(path)
			if !errors.Is(err, ErrUnsupportedHomeForm) {
				t.Fatalf("expandPath(%q) = (%q, %v), want an error wrapping ErrUnsupportedHomeForm (misread target %q)",
					path, got, err, misreadTarget(home, path))
			}
			msg := err.Error()
			if !strings.Contains(msg, path) {
				t.Errorf("error %q does not name the path %q", msg, path)
			}
			if !strings.Contains(msg, "~/") {
				t.Errorf("error %q does not say that only ~ and ~/... are expanded", msg)
			}
		})
	}
}

func TestResolveRefusesOtherUsersHome(t *testing.T) {
	for _, path := range otherUserHomeForms {
		t.Run("Resolve/"+path, func(t *testing.T) {
			home := sandboxHome(t)
			stubPortageqUnavailable(t)
			dir, err := Resolve(path, "")
			if !errors.Is(err, ErrUnsupportedHomeForm) {
				t.Errorf("Resolve(%q) = (%+v, %v), want an error wrapping ErrUnsupportedHomeForm", path, dir, err)
			}
			assertHomeUntouched(t, home)
		})
		t.Run("ResolveOrTemp/"+path, func(t *testing.T) {
			home := sandboxHome(t)
			dir, err := ResolveOrTemp(path)
			if !errors.Is(err, ErrUnsupportedHomeForm) {
				t.Errorf("ResolveOrTemp(%q) = (%+v, %v), want an error wrapping ErrUnsupportedHomeForm", path, dir, err)
			}
			assertHomeUntouched(t, home)
		})
	}
}

// assertHomeUntouched: nothing may be created under the sandbox home.
func assertHomeUntouched(t *testing.T, home string) {
	t.Helper()
	entries, err := os.ReadDir(home)
	if err != nil {
		t.Fatalf("read sandbox home: %v", err)
	}
	if len(entries) != 0 {
		var names []string
		for _, e := range entries {
			names = append(names, e.Name())
		}
		t.Errorf("sandbox $HOME gained %v; a refused path must create nothing", names)
	}
}

func TestResolveCacheSkipsOtherUsersHome(t *testing.T) {
	for _, path := range otherUserHomeForms {
		t.Run(path, func(t *testing.T) {
			home := sandboxHome(t)
			target := misreadTarget(home, path)
			if err := os.MkdirAll(target, 0o750); err != nil {
				t.Fatalf("pre-create misread target: %v", err)
			}
			distdir := filepath.Join(t.TempDir(), "distdir")
			if got := ResolveCache(path, distdir); got != "" {
				t.Errorf("ResolveCache(%q) = %q, want \"\" (prepopulation skipped), not the current user's %q", path, got, target)
			}
		})
	}
}

func TestLocateRefusesOtherUsersHome(t *testing.T) {
	for _, path := range otherUserHomeForms {
		for _, rung := range []string{"explicit", "configured"} {
			t.Run(rung+"/"+path, func(t *testing.T) {
				home := sandboxHome(t)
				stubPortageqUnavailable(t)
				target := misreadTarget(home, path)
				if err := os.MkdirAll(target, 0o750); err != nil {
					t.Fatalf("pre-create misread target: %v", err)
				}
				explicit, configured := path, ""
				if rung == "configured" {
					explicit, configured = "", path
				}
				got, ok := Locate(explicit, configured)
				if ok || got != "" {
					t.Errorf("Locate(%q, %q) = (%q, %v), want (\"\", false), not the current user's %q", explicit, configured, got, ok, target)
				}
			})
		}
	}
}
