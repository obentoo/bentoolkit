package ebuilds

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestSplitPkgSlot(t *testing.T) {
	tests := []struct {
		key      string
		wantAtom string
		wantSlot string
	}{
		{"net-libs/webkit-gtk", "net-libs/webkit-gtk", ""},
		{"net-libs/webkit-gtk:4.1", "net-libs/webkit-gtk", "4.1"},
		{"net-libs/webkit-gtk:6", "net-libs/webkit-gtk", "6"},
		// A subslot in the key is kept verbatim: it simply will not match the
		// slot-without-subslot readEbuildSlot returns, which is a loud miss
		// rather than a silent wrong-slot selection.
		{"net-libs/webkit-gtk:4.1/0", "net-libs/webkit-gtk", "4.1/0"},
		{"", "", ""},
	}
	for _, tt := range tests {
		atom, slot := SplitPkgSlot(tt.key)
		if atom != tt.wantAtom || slot != tt.wantSlot {
			t.Errorf("splitPkgSlot(%q) = (%q, %q), want (%q, %q)", tt.key, atom, slot, tt.wantAtom, tt.wantSlot)
		}
	}
}

func TestSplitPkgAtom(t *testing.T) {
	tests := []struct {
		key      string
		wantCat  string
		wantName string
		wantOK   bool
	}{
		{"app-misc/hello", "app-misc", "hello", true},
		{"net-libs/webkit-gtk:4.1", "net-libs", "webkit-gtk", true},
		{"dev-lang/rust:1.89", "dev-lang", "rust", true},
		{"nocategory", "", "", false},
		{"too/many/parts", "", "", false},
		{"/hello", "", "", false},
		{"app-misc/", "", "", false},
		{":4.1", "", "", false},
		{"", "", "", false},
	}
	for _, tt := range tests {
		cat, name, ok := SplitPkgAtom(tt.key)
		if cat != tt.wantCat || name != tt.wantName || ok != tt.wantOK {
			t.Errorf("splitPkgAtom(%q) = (%q, %q, %v), want (%q, %q, %v)",
				tt.key, cat, name, ok, tt.wantCat, tt.wantName, tt.wantOK)
		}
	}
}

// TestSplitPackageKey pins the exported form callers outside this package build
// their paths with; it must drop the slot exactly as splitPkgAtom does.
func TestSplitPackageKey(t *testing.T) {
	cat, name, ok := SplitPackageKey("net-libs/webkit-gtk:4.1")
	if !ok || cat != "net-libs" || name != "webkit-gtk" {
		t.Errorf("SplitPackageKey = (%q, %q, %v), want (%q, %q, true)", cat, name, ok, "net-libs", "webkit-gtk")
	}
	if _, _, ok := SplitPackageKey("nocategory"); ok {
		t.Error("SplitPackageKey(\"nocategory\") ok = true, want false")
	}
}

func TestReadEbuildSlot(t *testing.T) {
	tests := []struct {
		name    string
		content string
		want    string
	}{
		{"quoted with subslot and comment", "EAPI=8\nSLOT=\"4.1/0\" # soname version of libwebkit2gtk-4.1\n", "4.1"},
		{"quoted plain", "SLOT=\"0\"\n", "0"},
		{"single quoted", "SLOT='6/0'\n", "6"},
		{"bare", "SLOT=0\n", "0"},
		{"bare with comment", "SLOT=6/0 # comment\n", "6"},
		{"absent", "EAPI=8\nKEYWORDS=\"~amd64\"\n", ""},
		// A SLOT mentioned mid-line (a dependency atom, a here-doc) is not an
		// assignment and must not be picked up.
		{"not an assignment", "RDEPEND=\"net-libs/webkit-gtk:4.1\"\n", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "x.ebuild")
			if err := os.WriteFile(path, []byte(tt.content), 0o600); err != nil {
				t.Fatalf("write: %v", err)
			}
			if got := readEbuildSlot(path); got != tt.want {
				t.Errorf("readEbuildSlot() = %q, want %q", got, tt.want)
			}
		})
	}

	t.Run("unreadable file yields no slot", func(t *testing.T) {
		if got := readEbuildSlot(filepath.Join(t.TempDir(), "absent.ebuild")); got != "" {
			t.Errorf("readEbuildSlot(missing) = %q, want %q", got, "")
		}
	})
}

func TestSelectCurrentEbuildMissingPackage(t *testing.T) {
	_, err := SelectCurrentEbuild(nil, t.TempDir(), "app-misc/absent", "")
	if !errors.Is(err, ErrNoEbuildFound) {
		t.Fatalf("selectCurrentEbuild on an absent package: got %v, want %v", err, ErrNoEbuildFound)
	}
}

func TestApplyRevision(t *testing.T) {
	tests := []struct {
		version  string
		revision int
		want     string
	}{
		// Default: no revision configured, plain PV — every ordinary package.
		{"1.2.4", 0, "1.2.4"},
		{"2.52.5", 410, "2.52.5-r410"},
		{"2.52.5", 600, "2.52.5-r600"},
		// A revision already on the input is replaced, not appended: the source
		// slot's -r411 must not survive into the new PV.
		{"2.52.5-r411", 410, "2.52.5-r410"},
		// And with no configured revision it is dropped, which is the Gentoo
		// rule that a PV change resets the revision.
		{"1.2.4-r1", 0, "1.2.4"},
		{"1.2.4", -3, "1.2.4"},
	}
	for _, tt := range tests {
		if got := ApplyRevision(tt.version, tt.revision); got != tt.want {
			t.Errorf("applyRevision(%q, %d) = %q, want %q", tt.version, tt.revision, got, tt.want)
		}
	}
}
