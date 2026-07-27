package autoupdate

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
		atom, slot := splitPkgSlot(tt.key)
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
		cat, name, ok := splitPkgAtom(tt.key)
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

// writeWebkitOverlay lays out the real net-libs/webkit-gtk shape found in the
// bentoo overlay: SLOT 4.1 carries the -r411 revision marker, SLOT 6 uses a bare
// PV and sits at a HIGHER version. That ordering is the whole problem — the
// slot-blind scan returns 2.52.5 no matter which slot the caller meant.
func writeWebkitOverlay(t *testing.T) string {
	t.Helper()
	overlayDir := filepath.Join(t.TempDir(), "overlay")
	pkg := "net-libs/webkit-gtk"
	createTestEbuildFileWithContent(t, overlayDir, pkg, "2.52.4-r411",
		"EAPI=8\nSLOT=\"4.1/0\" # soname version of libwebkit2gtk-4.1\n")
	createTestEbuildFileWithContent(t, overlayDir, pkg, "2.52.5",
		"EAPI=8\nSLOT=\"6/0\" # soname version of libwebkit2gtk-6.0\n")
	return overlayDir
}

func TestSelectCurrentEbuildSlotFiltering(t *testing.T) {
	overlayDir := writeWebkitOverlay(t)

	tests := []struct {
		key         string
		wantVersion string
		wantFile    string
	}{
		{"net-libs/webkit-gtk:4.1", "2.52.4-r411", "webkit-gtk-2.52.4-r411.ebuild"},
		{"net-libs/webkit-gtk:6", "2.52.5", "webkit-gtk-2.52.5.ebuild"},
		// No slot in the key: unchanged pre-slot behaviour, the highest PV wins.
		{"net-libs/webkit-gtk", "2.52.5", "webkit-gtk-2.52.5.ebuild"},
	}
	for _, tt := range tests {
		t.Run(tt.key, func(t *testing.T) {
			got, err := selectCurrentEbuild(overlayDir, tt.key)
			if err != nil {
				t.Fatalf("selectCurrentEbuild(%q): %v", tt.key, err)
			}
			if got.Version != tt.wantVersion {
				t.Errorf("version = %q, want %q", got.Version, tt.wantVersion)
			}
			if filepath.Base(got.Path) != tt.wantFile {
				t.Errorf("path = %q, want basename %q", got.Path, tt.wantFile)
			}
		})
	}
}

// TestSelectCurrentEbuildUnmatchedSlot pins the distinction the auto-disable
// path depends on: a slot that matches nothing is a config error, NOT the
// "package is gone from the overlay" signal. Conflating the two is how a typo'd
// slot would silently switch a live entry off in packages.toml.
func TestSelectCurrentEbuildUnmatchedSlot(t *testing.T) {
	overlayDir := writeWebkitOverlay(t)

	_, err := selectCurrentEbuild(overlayDir, "net-libs/webkit-gtk:5")
	if !errors.Is(err, ErrSlotNotFound) {
		t.Fatalf("selectCurrentEbuild with an unmatched slot: got %v, want %v", err, ErrSlotNotFound)
	}
	if errors.Is(err, ErrNoEbuildFound) {
		t.Error("an unmatched slot must NOT report as ErrNoEbuildFound: the checker auto-disables on that")
	}
}

// TestCheckAllDoesNotDisableOnSlotTypo is the end-to-end guarantee: an entry
// whose slot matches nothing must be reported as a failure and must leave
// packages.toml untouched. The alternative — what a slot-blind checker does —
// is to write enabled = false and go quiet, which is how both webkit-gtk slot
// entries were switched off without a single error line.
func TestCheckAllDoesNotDisableOnSlotTypo(t *testing.T) {
	const cfg = `["net-libs/webkit-gtk:4.2"]
url = "https://example.invalid/releases/"
parser = "regex"
pattern = 'webkitgtk-([0-9.]+)\.tar\.xz'
`
	overlayPath, configPath := writePackagesTOML(t, cfg)
	createTestEbuildFileWithContent(t, overlayPath, "net-libs/webkit-gtk", "2.52.4-r411",
		"EAPI=8\nSLOT=\"4.1/0\"\n")

	before, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("read config: %v", err)
	}

	checker, err := NewChecker(overlayPath)
	if err != nil {
		t.Fatalf("NewChecker: %v", err)
	}

	batch := checker.CheckAll(true)

	if len(batch.Failures) != 1 {
		t.Errorf("expected the slot typo to be reported as a failure, got %d failures and %d results",
			len(batch.Failures), len(batch.Items))
	}
	for _, r := range batch.Items {
		if r.Orphaned {
			t.Errorf("%s was treated as an orphan; the package directory is right there", r.Package)
		}
	}

	after, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("read config after: %v", err)
	}
	if string(after) != string(before) {
		t.Errorf("packages.toml was rewritten on a slot typo:\n--- before\n%s\n--- after\n%s", before, after)
	}
}

func TestSelectCurrentEbuildSkipsLive(t *testing.T) {
	overlayDir := filepath.Join(t.TempDir(), "overlay")
	createTestEbuildFile(t, overlayDir, "app-misc/hello", "1.0.0")
	createTestEbuildFile(t, overlayDir, "app-misc/hello", "9999")

	got, err := selectCurrentEbuild(overlayDir, "app-misc/hello")
	if err != nil {
		t.Fatalf("selectCurrentEbuild: %v", err)
	}
	if got.Version != "1.0.0" {
		t.Errorf("version = %q, want %q (the live ebuild must be skipped)", got.Version, "1.0.0")
	}
}

func TestSelectCurrentEbuildMissingPackage(t *testing.T) {
	_, err := selectCurrentEbuild(t.TempDir(), "app-misc/absent")
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
		if got := applyRevision(tt.version, tt.revision); got != tt.want {
			t.Errorf("applyRevision(%q, %d) = %q, want %q", tt.version, tt.revision, got, tt.want)
		}
	}
}
