package autoupdate

import (
	"os"
	"path/filepath"
	"testing"
)

// filesFixture writes an overlay ebuild for pkg at version with ebuildText and
// the given files under files/, and returns the applier and its stages.
func filesFixture(t *testing.T, pkg, version, ebuildText string, files ...string) (*Applier, *recordingReporter) {
	t.Helper()
	applier, rep := gentooRereadFixture(t, pkg, version, ebuildText, "")
	category, pkgName, _ := splitPkgAtom(pkg)
	for _, f := range files {
		path := filepath.Join(applier.overlayPath, category, pkgName, "files", f)
		if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return applier, rep
}

func TestWarnIfFilesNameOldVersion(t *testing.T) {
	const pkg = "dev-libs/foo"
	const usesP = "PATCHES=( \"${FILESDIR}/${P}-gcc15.patch\" )\n"
	const literal = "PATCHES=( \"${FILESDIR}/foo-gcc15.patch\" )\n"

	t.Run("versioned FILESDIR path and an old-named file warns", func(t *testing.T) {
		a, rep := filesFixture(t, pkg, "1.2.3-r1", usesP, "foo-1.2.3-gcc15.patch")
		a.warnIfFilesNameOldVersion(pkg, "1.2.3-r1", "1.2.4")
		if !sawStage(rep, "files/foo-1.2.3-gcc15.patch named for 1.2.3") || !sawStage(rep, "files/foo-1.2.4-gcc15.patch") {
			t.Fatalf("expected a files/ warning naming both files, got: %v", rep.snapshot())
		}
	})

	t.Run("silent when the ebuild names the file literally", func(t *testing.T) {
		a, rep := filesFixture(t, pkg, "1.2.3", literal, "foo-1.2.3-gcc15.patch")
		a.warnIfFilesNameOldVersion(pkg, "1.2.3", "1.2.4")
		if len(rep.snapshot()) != 0 {
			t.Fatalf("expected silence, got: %v", rep.snapshot())
		}
	})

	t.Run("silent when the new version's file already exists", func(t *testing.T) {
		a, rep := filesFixture(t, pkg, "1.2.3", usesP, "foo-1.2.3-gcc15.patch", "foo-1.2.4-gcc15.patch")
		a.warnIfFilesNameOldVersion(pkg, "1.2.3", "1.2.4")
		if len(rep.snapshot()) != 0 {
			t.Fatalf("expected silence, got: %v", rep.snapshot())
		}
	})

	t.Run("silent on a revbump", func(t *testing.T) {
		a, rep := filesFixture(t, pkg, "1.2.3", usesP, "foo-1.2.3-gcc15.patch")
		a.warnIfFilesNameOldVersion(pkg, "1.2.3", "1.2.3-r1")
		if len(rep.snapshot()) != 0 {
			t.Fatalf("expected silence, got: %v", rep.snapshot())
		}
	})
}
