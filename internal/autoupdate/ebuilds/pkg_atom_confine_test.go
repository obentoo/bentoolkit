package ebuilds

import (
	"path/filepath"
	"testing"
)

// TestSplitPkgAtomKeepsWellFormedKeys guards the other direction: the hardening
// must not refuse a real atom, and a well-formed key must still land one
// category and one package below the overlay.
func TestSplitPkgAtomKeepsWellFormedKeys(t *testing.T) {
	for _, key := range []string{"app-misc/hello", "net-libs/webkit-gtk:4.1", "dev-lang/rust:1.89@stable", "dev-qt/qt6..compat"} {
		cat, name, ok := SplitPkgAtom(key)
		if !ok {
			t.Errorf("splitPkgAtom(%q) refused a well-formed key", key)
			continue
		}
		dir := PkgDirFor("/overlay", key)
		if rel, err := filepath.Rel("/overlay", dir); err != nil || rel != cat+"/"+name {
			t.Errorf("pkgDirFor(/overlay, %q) = %q, want /overlay/%s/%s", key, dir, cat, name)
		}
	}
}
