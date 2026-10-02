package autoupdate

import (
	"os"
	"path/filepath"
	"testing"
)

// The divergence warning must fire on the path production takes. --apply,
// --apply all and --revive all configure a staging root, so the candidate is
// prepared by prepareInStagingTree, not prepareInOverlay; a warning wired into
// the overlay path alone never reaches a real bump, while the direct-call tests
// above stay green.
func TestWarnIfGentooDiverges_FiresOnTheStagedPath(t *testing.T) {
	tmp := t.TempDir()
	overlay := filepath.Join(tmp, "overlay")
	gentoo := filepath.Join(tmp, "gentoo")
	const pkg, oldVersion, newVersion = "media-libs/mesa", "26.3.0", "26.3.1"

	write := func(root, text string) {
		t.Helper()
		dir := filepath.Join(root, "media-libs", "mesa")
		if err := os.MkdirAll(dir, 0o750); err != nil {
			t.Fatalf("mkdir %s: %v", dir, err)
		}
		if err := os.WriteFile(filepath.Join(dir, "mesa-"+oldVersion+".ebuild"), []byte(text), 0o600); err != nil {
			t.Fatalf("write ebuild under %s: %v", root, err)
		}
	}
	write(overlay, "EAPI=8\nRUST_MIN_VER=\"1.82.0\"\n")
	write(gentoo, "EAPI=8\nRUST_MIN_VER=\"1.85.0\"\n")

	rep := &recordingReporter{}
	applier, err := NewApplier(overlay, filepath.Join(tmp, "config"),
		WithApplierGentooPath(gentoo),
		WithApplierStagingRoot(filepath.Join(tmp, "staging")),
		WithApplierReporter(rep),
	)
	if err != nil {
		t.Fatalf("NewApplier: %v", err)
	}

	if _, err := applier.prepareInStagingTree(pkg, oldVersion, newVersion, &PendingUpdate{}, &ApplyResult{}); err != nil {
		t.Fatalf("prepareInStagingTree: %v", err)
	}

	if !sawStage(rep, "::gentoo also ships "+oldVersion) {
		t.Fatalf("the staged bump of %s carried our %s forward without the divergence warning; events: %v",
			pkg, oldVersion, rep.snapshot())
	}
}
