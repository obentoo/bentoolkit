package autoupdate

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/obentoo/bentoolkit/internal/autoupdate/ebuilds"
)

// The bump renames our own ebuild forward and never re-reads ::gentoo, which is
// how 40 findings accumulated in the overlay before anyone compared the trees.
// warnIfGentooDiverges is the advisory that fires at the moment of the rename.
//
// Three cases, because the check is only useful if it stays quiet in two of
// them: an advisory that fires on every bump is one nobody reads.

// gentooRereadFixture lays out an overlay ebuild and, optionally, a ::gentoo
// copy of the SAME version, and returns the applier plus the stages it reported.
func gentooRereadFixture(t *testing.T, pkg, version, ourText, theirText string) (*Applier, *recordingReporter) {
	t.Helper()

	tmp := t.TempDir()
	overlay := filepath.Join(tmp, "overlay")
	gentoo := filepath.Join(tmp, "gentoo")

	category, pkgName, ok := ebuilds.SplitPkgAtom(pkg)
	if !ok {
		t.Fatalf("splitPkgAtom(%q) failed", pkg)
	}
	name := pkgName + "-" + version + ".ebuild"

	ourDir := filepath.Join(overlay, category, pkgName)
	if err := os.MkdirAll(ourDir, 0o750); err != nil {
		t.Fatalf("mkdir overlay: %v", err)
	}
	if err := os.WriteFile(filepath.Join(ourDir, name), []byte(ourText), 0o600); err != nil {
		t.Fatalf("write overlay ebuild: %v", err)
	}

	// theirText == "" means ::gentoo does not ship this version at all.
	if theirText != "" {
		theirDir := filepath.Join(gentoo, category, pkgName)
		if err := os.MkdirAll(theirDir, 0o750); err != nil {
			t.Fatalf("mkdir gentoo: %v", err)
		}
		if err := os.WriteFile(filepath.Join(theirDir, name), []byte(theirText), 0o600); err != nil {
			t.Fatalf("write gentoo ebuild: %v", err)
		}
	}

	rep := &recordingReporter{}
	applier, err := NewApplier(overlay, filepath.Join(tmp, "config"),
		WithApplierGentooPath(gentoo),
		WithApplierReporter(rep),
	)
	if err != nil {
		t.Fatalf("NewApplier: %v", err)
	}
	return applier, rep
}

func TestWarnIfGentooDiverges_SamePVDifferentContent_Warns(t *testing.T) {
	applier, rep := gentooRereadFixture(t, "media-libs/mesa", "26.3.0",
		"RUST_MIN_VER=\"1.82.0\"\n",
		"RUST_MIN_VER=\"1.85.0\"\n")

	applier.warnIfGentooDiverges("media-libs/mesa", "26.3.0")

	if !sawStage(rep, "::gentoo also ships 26.3.0") {
		t.Fatalf("expected a divergence warning, got events: %v", rep.snapshot())
	}
}

func TestWarnIfGentooDiverges_SamePVIdentical_Silent(t *testing.T) {
	// Most of the tree: we mirror ::gentoo exactly. Warning here would bury
	// the real findings under the majority case.
	same := "RUST_MIN_VER=\"1.85.0\"\n"
	applier, rep := gentooRereadFixture(t, "media-libs/mesa", "26.3.0", same, same)

	applier.warnIfGentooDiverges("media-libs/mesa", "26.3.0")

	if n := len(rep.snapshot()); n != 0 {
		t.Fatalf("expected silence on identical ebuilds, got: %v", rep.snapshot())
	}
}

func TestWarnIfGentooDiverges_GentooLacksThePV_Silent(t *testing.T) {
	// The overlay is ahead by design on most packages. "::gentoo has no such
	// version" is the normal state, not a finding.
	applier, rep := gentooRereadFixture(t, "sci-ml/ollama", "0.33.3",
		"EAPI=8\n", "")

	applier.warnIfGentooDiverges("sci-ml/ollama", "0.33.3")

	if n := len(rep.snapshot()); n != 0 {
		t.Fatalf("expected silence when ::gentoo lacks the PV, got: %v", rep.snapshot())
	}
}

func TestWarnIfGentooDiverges_NoGentooPath_Silent(t *testing.T) {
	// An unset tree disables the check outright: a missing ::gentoo must never
	// turn into noise on every bump, nor into a failure.
	tmp := t.TempDir()
	rep := &recordingReporter{}
	applier, err := NewApplier(filepath.Join(tmp, "overlay"), filepath.Join(tmp, "config"),
		WithApplierReporter(rep),
	)
	if err != nil {
		t.Fatalf("NewApplier: %v", err)
	}

	applier.warnIfGentooDiverges("media-libs/mesa", "26.3.0")

	if n := len(rep.snapshot()); n != 0 {
		t.Fatalf("expected silence with no gentooPath, got: %v", rep.snapshot())
	}
}

// sawStage: did the applier emit a TaskStage whose text contains want?
func sawStage(r *recordingReporter, want string) bool {
	for _, e := range r.snapshot() {
		if strings.HasPrefix(e, "TaskStage:") && strings.Contains(e, want) {
			return true
		}
	}
	return false
}
