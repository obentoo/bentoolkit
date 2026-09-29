package autoupdate

import (
	"errors"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// unsafePkgKeys are packages.toml keys whose category or package half is not
// one directory name. Before story 064 splitPkgAtom accepted every one of them,
// and candidateIn / pkgDirFor then joined the halves under the overlay, so a
// key could name a path outside its category/package directory (S064-R1.4).
var unsafePkgKeys = []struct {
	key      string
	rejected string // the half the error must name
}{
	{"../x", ".."},
	{"../..", ".."},
	{"cat/..", ".."},
	{"./x", "."},
	{"cat/.", "."},
	{"../x:4.1", ".."},
	{"../x@label", ".."},
	{`cat\evil/x`, `cat\evil`},
	{`cat/x\..`, `x\..`},
	{"cat\x00/x", "cat\x00"},
	{"cat/x\x00", "x\x00"},
}

// TestSplitPkgAtomRefusesNonElementHalves pins the confinement itself: every
// path builder reaches the key through splitPkgAtom, so a refusal here is what
// keeps each of them inside the overlay.
func TestSplitPkgAtomRefusesNonElementHalves(t *testing.T) {
	for _, tt := range unsafePkgKeys {
		if cat, name, ok := splitPkgAtom(tt.key); ok {
			t.Errorf("splitPkgAtom(%q) = (%q, %q, true), want refused", tt.key, cat, name)
		}
		if dir := pkgDirFor("/overlay", tt.key); dir != "" {
			t.Errorf("pkgDirFor(/overlay, %q) = %q, want \"\"", tt.key, dir)
		}
		if _, err := candidateIn("/overlay", tt.key, "1.0"); err == nil {
			t.Errorf("candidateIn(/overlay, %q) accepted the key", tt.key)
		}
	}
}

// TestSplitPkgAtomKeepsWellFormedKeys guards the other direction: the hardening
// must not refuse a real atom, and a well-formed key must still land one
// category and one package below the overlay.
func TestSplitPkgAtomKeepsWellFormedKeys(t *testing.T) {
	for _, key := range []string{"app-misc/hello", "net-libs/webkit-gtk:4.1", "dev-lang/rust:1.89@stable", "dev-qt/qt6..compat"} {
		cat, name, ok := splitPkgAtom(key)
		if !ok {
			t.Errorf("splitPkgAtom(%q) refused a well-formed key", key)
			continue
		}
		dir := pkgDirFor("/overlay", key)
		if rel, err := filepath.Rel("/overlay", dir); err != nil || rel != cat+"/"+name {
			t.Errorf("pkgDirFor(/overlay, %q) = %q, want /overlay/%s/%s", key, dir, cat, name)
		}
	}
}

// TestValidatePackageConfigNamesRejectedPathElement pins the report a person
// reads: the error wraps ErrInvalidPackageKey, names the package key, and
// names the half that was refused.
func TestValidatePackageConfigNamesRejectedPathElement(t *testing.T) {
	for _, tt := range unsafePkgKeys {
		cfg := PackageConfig{URL: "https://example.com/x", Parser: "json", Path: "version"}
		err := ValidatePackageConfig(tt.key, &cfg)
		if !errors.Is(err, ErrInvalidPackageKey) {
			t.Errorf("ValidatePackageConfig(%q) = %v, want ErrInvalidPackageKey", tt.key, err)
			continue
		}
		msg := err.Error()
		if !strings.Contains(msg, "package "+tt.key+":") {
			t.Errorf("ValidatePackageConfig(%q) error %q does not name the package", tt.key, msg)
		}
		if !strings.Contains(msg, strconv.Quote(tt.rejected)) {
			t.Errorf("ValidatePackageConfig(%q) error %q does not name the rejected element %q", tt.key, msg, tt.rejected)
		}
	}
}
