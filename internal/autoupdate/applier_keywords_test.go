package autoupdate

import (
	"os"
	"path/filepath"
	"testing"
)

// gentooTreeWith builds a ::gentoo tree holding one empty ebuild per
// "<cat>/<pn>/<pn>-<pvr>" entry and returns its root.
func gentooTreeWith(t *testing.T, entries ...string) string {
	t.Helper()
	root := t.TempDir()
	for _, e := range entries {
		path := filepath.Join(root, e+".ebuild")
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("EAPI=8\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

func TestPastGentoo(t *testing.T) {
	for name, tc := range map[string]struct {
		tree         []string
		oldPV, newPV string
		past, known  bool
	}{
		// Hostile first: two versions or two packages wrongly read as one.
		"1.10 is not 1.1": {
			tree: []string{"dev-libs/p/p-1.0", "dev-libs/p/p-1.10"}, oldPV: "1.0", newPV: "1.1", past: true, known: true,
		},
		"a like-named package or another category does not count": {
			tree: []string{"dev-libs/p/p-1.0", "dev-libs/p-utils/p-utils-1.1", "dev-python/p/p-1.1"}, oldPV: "1.0", newPV: "1.1", past: true, known: true,
		},
		"::gentoo 1.1-r1 carries the new 1.1": {
			tree: []string{"dev-libs/p/p-1.0", "dev-libs/p/p-1.1-r1"}, oldPV: "1.0", newPV: "1.1", known: true,
		},
		"::gentoo 1.0-r2 carries the old 1.0": {
			tree: []string{"dev-libs/p/p-1.0-r2"}, oldPV: "1.0", newPV: "1.1", past: true, known: true,
		},
		"our old 1.0-r1 matches ::gentoo 1.0": {
			tree: []string{"dev-libs/p/p-1.0"}, oldPV: "1.0-r1", newPV: "1.1", past: true, known: true,
		},
		// Benign.
		"old carried, new not": {
			tree: []string{"dev-libs/p/p-1.0"}, oldPV: "1.0", newPV: "1.1", past: true, known: true,
		},
		"both carried (U1)": {
			tree: []string{"dev-libs/p/p-1.0", "dev-libs/p/p-1.1"}, oldPV: "1.0", newPV: "1.1", known: true,
		},
		"package not in ::gentoo (U2)": {
			tree: []string{"dev-libs/other/other-1.0"}, oldPV: "1.0", newPV: "1.1", known: true,
		},
		"package carried, old version not": {
			tree: []string{"dev-libs/p/p-0.9"}, oldPV: "1.0", newPV: "1.1", known: true,
		},
		"live 9999 is never past (U4)": {
			tree: []string{"dev-libs/p/p-1.0"}, oldPV: "1.0", newPV: "9999", known: true,
		},
	} {
		t.Run(name, func(t *testing.T) {
			past, known := pastGentoo(gentooTreeWith(t, tc.tree...), "dev-libs", "p", tc.oldPV, tc.newPV)
			if past != tc.past || known != tc.known {
				t.Errorf("pastGentoo = (%v, %v), want (%v, %v)", past, known, tc.past, tc.known)
			}
		})
	}
}

func TestPastGentooUnknownTree(t *testing.T) {
	for name, path := range map[string]string{
		"empty path (R3)":   "",
		"missing root (R3)": filepath.Join(t.TempDir(), "absent"),
	} {
		t.Run(name, func(t *testing.T) {
			if past, known := pastGentoo(path, "dev-libs", "p", "1.0", "1.1"); past || known {
				t.Errorf("pastGentoo(%q) = (%v, %v), want (false, false)", path, past, known)
			}
		})
	}
}
