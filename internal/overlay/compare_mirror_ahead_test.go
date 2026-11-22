package overlay

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/obentoo/bentoolkit/internal/gentoo/repo"
)

// writeMirrorAheadEbuild writes <root>/<cat>/<pn>/<pn>-<v>.ebuild.
func writeMirrorAheadEbuild(t *testing.T, root, cat, pn, v, body string) {
	t.Helper()
	dir := filepath.Join(root, cat, pn)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, pn+"-"+v+".ebuild"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestMirrorAheadAPIProviderYieldsNothing(t *testing.T) {
	overlay := t.TempDir()
	writeMirrorAheadEbuild(t, overlay, "dev-libs", "p", "1.1", "KEYWORDS=\"amd64\"\n")
	pkg := repo.PackageInfo{Category: "dev-libs", Package: "p", Versions: []string{"1.1"}}

	// fakeProvider implements no PackageDirProvider: there is no local content
	// to read, so the finding is not produced (R3) rather than guessed.
	if got := mirrorAheadVersions(pkg, &fakeProvider{versions: map[string][]string{"dev-libs/p": {"1.0"}}}, overlay); len(got) != 0 {
		t.Errorf("an API-only provider produced %v, want nothing", got)
	}
}

func TestMirrorAheadMultiLineKeywordsIsNotBaseline(t *testing.T) {
	overlay, gentoo := t.TempDir(), t.TempDir()
	writeMirrorAheadEbuild(t, gentoo, "dev-libs", "p", "1.0", "KEYWORDS=\"amd64\"\n")
	writeMirrorAheadEbuild(t, overlay, "dev-libs", "p", "1.1", "KEYWORDS=\"~amd64\n\t~arm64\"\n")
	pkg := repo.PackageInfo{Category: "dev-libs", Package: "p", Versions: []string{"1.1"}}

	got := mirrorAheadVersions(pkg, &localRootedFakeProvider{root: gentoo}, overlay)
	if len(got) != 1 || got[0].Version != "1.1" || !got[0].Keywords || got[0].Header {
		t.Errorf("a multi-line KEYWORDS must read as not ~amd64: got %+v", got)
	}
}
