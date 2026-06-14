package autoupdate

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// seedWriteFile is a small test helper that creates parent dirs and writes
// content. (Named distinctly to avoid colliding with the package's existing
// writeFile helper in authfetch_apply_live_test.go.)
func seedWriteFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("MkdirAll(%s): %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("WriteFile(%s): %v", path, err)
	}
}

// seedReadFile is a small test helper that reads a file and fails on error.
func seedReadFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile(%s): %v", path, err)
	}
	return string(data)
}

// TestSeedFromGentooCopiesPackageParts verifies that SeedFromGentoo copies the
// gentoo ebuild, metadata.xml, and the files/ subtree into a freshly-created
// overlay package dir, while NOT copying the Manifest.
func TestSeedFromGentooCopiesPackageParts(t *testing.T) {
	tmpDir := t.TempDir()
	overlayDir := filepath.Join(tmpDir, "overlay")
	configDir := filepath.Join(tmpDir, "config")

	const (
		pkg           = "app-misc/foo"
		pkgName       = "foo"
		gentooVersion = "1.2.3"
	)

	// Build a fake ::gentoo source package dir.
	srcPkgDir := filepath.Join(tmpDir, "gentoo", "app-misc", "foo")
	ebuildContent := "EAPI=8\nDESCRIPTION=\"seed me\"\n"
	metaContent := "<?xml version=\"1.0\"?>\n<pkgmetadata/>\n"
	patchContent := "--- a/foo\n+++ b/foo\n@@ patch @@\n"

	seedWriteFile(t, filepath.Join(srcPkgDir, pkgName+"-"+gentooVersion+".ebuild"), ebuildContent)
	seedWriteFile(t, filepath.Join(srcPkgDir, "metadata.xml"), metaContent)
	seedWriteFile(t, filepath.Join(srcPkgDir, "files", "foo.patch"), patchContent)
	// Manifest must be ignored by SeedFromGentoo.
	seedWriteFile(t, filepath.Join(srcPkgDir, "Manifest"), "DIST foo-1.2.3.tar.gz 123 BLAKE2B deadbeef\n")

	applier, err := NewApplier(overlayDir, configDir)
	if err != nil {
		t.Fatalf("NewApplier: %v", err)
	}

	if err := applier.SeedFromGentoo(pkg, srcPkgDir, gentooVersion); err != nil {
		t.Fatalf("SeedFromGentoo: %v", err)
	}

	dst := filepath.Join(overlayDir, "app-misc", pkgName)

	// Ebuild landed with matching content.
	if got := seedReadFile(t, filepath.Join(dst, pkgName+"-"+gentooVersion+".ebuild")); got != ebuildContent {
		t.Errorf("ebuild content mismatch: got %q want %q", got, ebuildContent)
	}

	// metadata.xml landed with matching content.
	if got := seedReadFile(t, filepath.Join(dst, "metadata.xml")); got != metaContent {
		t.Errorf("metadata.xml content mismatch: got %q want %q", got, metaContent)
	}

	// files/foo.patch landed with matching content at the right relative path.
	if got := seedReadFile(t, filepath.Join(dst, "files", "foo.patch")); got != patchContent {
		t.Errorf("files/foo.patch content mismatch: got %q want %q", got, patchContent)
	}

	// Manifest must NOT have been copied.
	if _, err := os.Stat(filepath.Join(dst, "Manifest")); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("expected Manifest to be absent in overlay, stat err = %v", err)
	}
}

// TestSeedFromGentooWithoutOptionalParts verifies the optional metadata.xml and
// files/ are skipped silently when ::gentoo does not ship them.
func TestSeedFromGentooWithoutOptionalParts(t *testing.T) {
	tmpDir := t.TempDir()
	overlayDir := filepath.Join(tmpDir, "overlay")
	configDir := filepath.Join(tmpDir, "config")

	const (
		pkg           = "dev-libs/bar"
		pkgName       = "bar"
		gentooVersion = "0.1.0"
	)

	srcPkgDir := filepath.Join(tmpDir, "gentoo", "dev-libs", "bar")
	seedWriteFile(t, filepath.Join(srcPkgDir, pkgName+"-"+gentooVersion+".ebuild"), "EAPI=8\n")

	applier, err := NewApplier(overlayDir, configDir)
	if err != nil {
		t.Fatalf("NewApplier: %v", err)
	}

	if err := applier.SeedFromGentoo(pkg, srcPkgDir, gentooVersion); err != nil {
		t.Fatalf("SeedFromGentoo: %v", err)
	}

	dst := filepath.Join(overlayDir, "dev-libs", pkgName)
	if _, err := os.Stat(filepath.Join(dst, pkgName+"-"+gentooVersion+".ebuild")); err != nil {
		t.Errorf("expected seeded ebuild, stat err = %v", err)
	}
	if _, err := os.Stat(filepath.Join(dst, "metadata.xml")); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("expected metadata.xml to be absent, stat err = %v", err)
	}
	if _, err := os.Stat(filepath.Join(dst, "files")); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("expected files/ to be absent, stat err = %v", err)
	}
}

// TestSeedFromGentooMissingEbuild verifies a missing source ebuild for the
// requested version returns an error (wrapping ErrEbuildNotFound).
func TestSeedFromGentooMissingEbuild(t *testing.T) {
	tmpDir := t.TempDir()
	overlayDir := filepath.Join(tmpDir, "overlay")
	configDir := filepath.Join(tmpDir, "config")

	const (
		pkg           = "app-misc/baz"
		pkgName       = "baz"
		gentooVersion = "9.9.9"
	)

	// Source dir exists but only carries a different version.
	srcPkgDir := filepath.Join(tmpDir, "gentoo", "app-misc", "baz")
	seedWriteFile(t, filepath.Join(srcPkgDir, pkgName+"-1.0.0.ebuild"), "EAPI=8\n")

	applier, err := NewApplier(overlayDir, configDir)
	if err != nil {
		t.Fatalf("NewApplier: %v", err)
	}

	err = applier.SeedFromGentoo(pkg, srcPkgDir, gentooVersion)
	if err == nil {
		t.Fatal("expected error for missing source ebuild, got nil")
	}
	if !errors.Is(err, ErrEbuildNotFound) {
		t.Errorf("expected error wrapping ErrEbuildNotFound, got %v", err)
	}
}

// TestSeedFromGentooEmptyArgs verifies input validation at the boundary.
func TestSeedFromGentooEmptyArgs(t *testing.T) {
	tmpDir := t.TempDir()
	applier, err := NewApplier(filepath.Join(tmpDir, "overlay"), filepath.Join(tmpDir, "config"))
	if err != nil {
		t.Fatalf("NewApplier: %v", err)
	}

	cases := []struct {
		name          string
		pkg           string
		srcPkgDir     string
		gentooVersion string
	}{
		{"empty pkg", "", "/some/src", "1.0.0"},
		{"empty srcPkgDir", "app-misc/foo", "", "1.0.0"},
		{"empty version", "app-misc/foo", "/some/src", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := applier.SeedFromGentoo(tc.pkg, tc.srcPkgDir, tc.gentooVersion); err == nil {
				t.Errorf("expected error for %s, got nil", tc.name)
			}
		})
	}

	// Malformed package name (missing category) must also error.
	if err := applier.SeedFromGentoo("foo", filepath.Join(tmpDir, "src"), "1.0.0"); err == nil {
		t.Error("expected error for malformed package name, got nil")
	}
}
