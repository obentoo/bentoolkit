package autoupdate

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/obentoo/bentoolkit/internal/autoupdate/ebuilds"
)

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
			got, err := ebuilds.SelectCurrentEbuild(nil, overlayDir, tt.key, "")
			if err != nil {
				t.Fatalf("selectCurrentEbuild(nil, %q): %v", tt.key, err)
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

	_, err := ebuilds.SelectCurrentEbuild(nil, overlayDir, "net-libs/webkit-gtk:5", "")
	if !errors.Is(err, ebuilds.ErrSlotNotFound) {
		t.Fatalf("selectCurrentEbuild with an unmatched slot: got %v, want %v", err, ebuilds.ErrSlotNotFound)
	}
	if errors.Is(err, ebuilds.ErrNoEbuildFound) {
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

	batch := checker.CheckAll(t.Context(), true)

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

	got, err := ebuilds.SelectCurrentEbuild(nil, overlayDir, "app-misc/hello", "")
	if err != nil {
		t.Fatalf("selectCurrentEbuild: %v", err)
	}
	if got.Version != "1.0.0" {
		t.Errorf("version = %q, want %q (the live ebuild must be skipped)", got.Version, "1.0.0")
	}
}
