package autoupdate

import (
	"os"
	"path/filepath"
	"testing"
)

// TestFindReviewArchiveSearchesEachDirectoryOnce: --distdir and the distfiles
// cache are often the same directory, and the 2026-11-24 report printed
// "searched /var/cache/distfiles, /var/cache/distfiles".
func TestFindReviewArchiveSearchesEachDirectoryOnce(t *testing.T) {
	pkgDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(pkgDir, "Manifest"), []byte("DIST m-1.0.tar.gz 3 BLAKE2B aa SHA512 bb\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	_, searched := findReviewArchive(pkgDir, "1.0", []string{dir, dir + "/", "", dir})
	if len(searched.dirs) != 1 {
		t.Errorf("searched = %v, want the one directory once", searched.dirs)
	}
}
