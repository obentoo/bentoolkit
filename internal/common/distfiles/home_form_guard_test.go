package distfiles

import (
	"os"
	"path/filepath"
	"testing"
)

// Regression guard for S050-R7.7, the hostile converse of R6.1: the refusal of
// "~name" must not swallow the forms the current user relies on. Expected
// GREEN before and after the fix.

func TestExpandPathKeepsCurrentUserForms(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Chdir(t.TempDir())
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}

	cases := []struct{ in, want string }{
		{"~", home},
		{"~/", home},
		{"~/alice", filepath.Join(home, "alice")},
		{"~/x/y", filepath.Join(home, "x", "y")},
		// Third element: "~alice" as a NON-first element of a ~/ path is a
		// directory name, not a home form.
		{"~/~alice", filepath.Join(home, "~alice")},
		// A tilde that is not the first character is never home-expanded.
		{"/a/~alice", "/a/~alice"},
		{"rel/~x", filepath.Join(cwd, "rel", "~x")},
	}
	for _, tc := range cases {
		t.Run(tc.in, func(t *testing.T) {
			got, err := expandPath(tc.in)
			if err != nil {
				t.Fatalf("expandPath(%q) error = %v, want %q", tc.in, err, tc.want)
			}
			if got != tc.want {
				t.Errorf("expandPath(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestResolveCacheAndLocateKeepCurrentUserForms(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	for _, sub := range []string{"cache", "distfiles"} {
		if err := os.MkdirAll(filepath.Join(home, sub), 0o750); err != nil {
			t.Fatalf("mkdir %s: %v", sub, err)
		}
	}
	distdir := filepath.Join(t.TempDir(), "distdir")
	if got, want := ResolveCache("~/cache", distdir), filepath.Join(home, "cache"); got != want {
		t.Errorf("ResolveCache(\"~/cache\") = %q, want %q", got, want)
	}
	stubPortageqUnavailable(t)
	if got, ok := Locate("~/distfiles", ""); !ok || got != filepath.Join(home, "distfiles") {
		t.Errorf("Locate(\"~/distfiles\") = (%q, %v), want (%q, true)", got, ok, filepath.Join(home, "distfiles"))
	}
}
