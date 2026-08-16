package distfiles

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestResolveOrTempCreatesTempDirAndMarksItCreated covers the no-path case: with
// nothing configured, ResolveOrTemp makes a directory of its own, and marks it
// Created so Cleanup is allowed to remove it later.
func TestResolveOrTempCreatesTempDirAndMarksItCreated(t *testing.T) {
	dir, err := ResolveOrTemp("")
	if err != nil {
		t.Fatalf("ResolveOrTemp(\"\") error = %v", err)
	}
	t.Cleanup(dir.Cleanup)

	if dir.Path == "" {
		t.Fatal("ResolveOrTemp(\"\") returned an empty path")
	}
	info, err := os.Stat(dir.Path)
	if err != nil {
		t.Fatalf("temp distdir not created: %v", err)
	}
	if !info.IsDir() {
		t.Fatalf("ResolveOrTemp(\"\") path %q is not a directory", dir.Path)
	}
	if !dir.Created {
		t.Errorf("ResolveOrTemp(\"\") Created = false, want true (we made this directory, so it is ours to remove)")
	}
}

// TestResolveOrTempExistingDirIsNotMarkedCreated covers the other half of R1.5: a
// path the caller named is never ours to delete. That holds both when the
// directory is already there and when ResolveOrTemp had to mkdir it — a named
// --distdir is documented as a download cache preserved across runs (and may
// be the host's real DISTDIR), so creating it does not make it ours.
func TestResolveOrTempExistingDirIsNotMarkedCreated(t *testing.T) {
	t.Run("directory already exists", func(t *testing.T) {
		existing := t.TempDir()

		dir, err := ResolveOrTemp(existing)
		if err != nil {
			t.Fatalf("ResolveOrTemp(%q) error = %v", existing, err)
		}
		if dir.Path != existing {
			t.Errorf("ResolveOrTemp(%q) Path = %q, want %q", existing, dir.Path, existing)
		}
		if dir.Created {
			t.Errorf("ResolveOrTemp(%q) Created = true, want false (the directory pre-existed)", existing)
		}
		if _, err := os.Stat(existing); err != nil {
			t.Errorf("ResolveOrTemp must not disturb an existing directory, stat err = %v", err)
		}
	})

	t.Run("named path ResolveOrTemp had to create", func(t *testing.T) {
		target := filepath.Join(t.TempDir(), "nested", "distfiles")

		dir, err := ResolveOrTemp(target)
		if err != nil {
			t.Fatalf("ResolveOrTemp(%q) error = %v", target, err)
		}
		if dir.Path != target {
			t.Errorf("ResolveOrTemp(%q) Path = %q, want %q", target, dir.Path, target)
		}
		if info, err := os.Stat(dir.Path); err != nil || !info.IsDir() {
			t.Fatalf("named distdir not created: stat err = %v", err)
		}
		if dir.Created {
			t.Errorf("ResolveOrTemp(%q) Created = true, want false (a named distdir persists across runs)", target)
		}
	})
}

// TestCleanupRemovesOnlyACreatedDir is the behavioural statement of R1.5: the
// self-made temporary directory goes away, everything else survives, and the
// zero value is safe to call.
func TestCleanupRemovesOnlyACreatedDir(t *testing.T) {
	t.Run("a directory we created is removed", func(t *testing.T) {
		dir, err := ResolveOrTemp("")
		if err != nil {
			t.Fatalf("ResolveOrTemp(\"\") error = %v", err)
		}
		dir.Cleanup()
		if _, err := os.Stat(dir.Path); !os.IsNotExist(err) {
			t.Errorf("temp distdir should be removed after Cleanup, stat err = %v", err)
		}
	})

	t.Run("a directory we did not create survives", func(t *testing.T) {
		target := filepath.Join(t.TempDir(), "nested", "distfiles")
		dir, err := ResolveOrTemp(target)
		if err != nil {
			t.Fatalf("ResolveOrTemp(%q) error = %v", target, err)
		}
		// Seed a file so a wrong Cleanup destroys evidence, not just an
		// empty directory.
		payload := filepath.Join(dir.Path, "hello-1.0.0.tar.gz")
		if err := os.WriteFile(payload, []byte("cached"), 0o644); err != nil {
			t.Fatalf("seed distfile: %v", err)
		}

		dir.Cleanup()

		if _, err := os.Stat(dir.Path); err != nil {
			t.Errorf("named distdir must survive Cleanup, stat err = %v", err)
		}
		data, err := os.ReadFile(payload)
		if err != nil {
			t.Fatalf("cached distfile removed by Cleanup: %v", err)
		}
		if string(data) != "cached" {
			t.Errorf("cached distfile content = %q, want %q", data, "cached")
		}
	})

	t.Run("the zero value is a no-op", func(t *testing.T) {
		// Written as a call rather than a comment: Cleanup runs from a defer
		// placed right after the resolve call, so it must tolerate a zero Dir
		// from the error path.
		Dir{}.Cleanup()
	})
}

// TestResolveOrTempExpandsTilde pins the path expansion the moved code
// performed: a "~"-prefixed distdir resolves under the user's home, not into a
// literal directory named "~".
func TestResolveOrTempExpandsTilde(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skipf("UserHomeDir unavailable: %v", err)
	}

	rel := filepath.Join(".cache", "bentoo-test-distdir-resolve")
	input := "~/" + rel
	expected := filepath.Join(home, rel)
	t.Cleanup(func() { _ = os.RemoveAll(expected) })

	dir, err := ResolveOrTemp(input)
	if err != nil {
		t.Fatalf("ResolveOrTemp(%q) error = %v", input, err)
	}
	if dir.Path != expected {
		t.Errorf("ResolveOrTemp(%q) Path = %q, want %q", input, dir.Path, expected)
	}
	if dir.Created {
		t.Errorf("ResolveOrTemp(%q) Created = true, want false", input)
	}
}

// TestResolveCacheAcceptsOnlyAnExistingDirectoryDistinctFromDistdir covers the
// four ways the read-only cache lookup is switched off: unset, missing,
// and pointing at the working distdir (where pkgdev already reuses in place).
func TestResolveCacheAcceptsOnlyAnExistingDirectoryDistinctFromDistdir(t *testing.T) {
	existing := t.TempDir()
	dist := t.TempDir()

	tests := []struct {
		name    string
		userDir string
		distdir string
		want    string
	}{
		{"empty disables", "", dist, ""},
		{"missing dir", filepath.Join(t.TempDir(), "nope"), dist, ""},
		{"valid dir", existing, dist, existing},
		{"same as distdir", dist, dist, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ResolveCache(tt.userDir, tt.distdir)
			if got != tt.want {
				t.Errorf("ResolveCache(%q, %q) = %q, want %q", tt.userDir, tt.distdir, got, tt.want)
			}
		})
	}
}

// TestParseManifestDistFilenamesIgnoresMalformedLines checks that only well
// formed `DIST <name>` lines yield a name: other record types, comments, a
// DIST line without a name, and a name carrying a path separator (traversal,
// malformed or hostile) are all dropped.
func TestParseManifestDistFilenamesIgnoresMalformedLines(t *testing.T) {
	dir := t.TempDir()
	manifestPath := filepath.Join(dir, "Manifest")
	content := "" +
		"DIST hello-1.0.0.tar.gz 12345 BLAKE2B abc SHA512 def\n" +
		"DIST world-2.tar.xz 99 BLAKE2B aa SHA512 bb\n" +
		"\n" +
		"# comment line\n" +
		"EBUILD hello-1.0.0.ebuild 0 BLAKE2B 0 SHA512 0\n" +
		"DIST\n" + // malformed: no name
		"DIST ../etc/passwd 0 BLAKE2B 0 SHA512 0\n" // path traversal: must be skipped
	if err := os.WriteFile(manifestPath, []byte(content), 0o644); err != nil {
		t.Fatalf("seed manifest: %v", err)
	}

	got := ParseManifestDistFilenames(manifestPath)
	want := []string{"hello-1.0.0.tar.gz", "world-2.tar.xz"}
	if len(got) != len(want) {
		t.Fatalf("ParseManifestDistFilenames() = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("got[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

// TestParseManifestDistFilenamesReturnsNilForAMissingFile pins the degraded
// path: a package with no Manifest yet is "nothing to reuse", not an error.
func TestParseManifestDistFilenamesReturnsNilForAMissingFile(t *testing.T) {
	got := ParseManifestDistFilenames(filepath.Join(t.TempDir(), "no-such-Manifest"))
	if got != nil {
		t.Errorf("ParseManifestDistFilenames(missing) = %v, want nil", got)
	}
}

// TestPrepopulateFromCacheSymlinksManifestNamesOnly checks that exactly the
// requested names that exist in the cache are linked — a name the cache does
// not hold is left for pkgdev to download, and nothing else is invented.
func TestPrepopulateFromCacheSymlinksManifestNamesOnly(t *testing.T) {
	cache := t.TempDir()
	dist := t.TempDir()

	// Two of three names exist in the cache.
	if err := os.WriteFile(filepath.Join(cache, "hello-1.0.0.tar.gz"), []byte("a"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cache, "world-2.tar.xz"), []byte("b"), 0o644); err != nil {
		t.Fatal(err)
	}
	// Present in the cache but absent from the requested names: it must not
	// be linked, since only the Manifest's DIST entries may be reused.
	if err := os.WriteFile(filepath.Join(cache, "unrelated-9.tar.gz"), []byte("c"), 0o644); err != nil {
		t.Fatal(err)
	}

	names := []string{"hello-1.0.0.tar.gz", "world-2.tar.xz", "ghost.tar.gz"}
	got := PrepopulateFromCache(dist, cache, names)
	if got != 2 {
		t.Fatalf("PrepopulateFromCache() = %d, want 2", got)
	}

	for _, n := range []string{"hello-1.0.0.tar.gz", "world-2.tar.xz"} {
		linkPath := filepath.Join(dist, n)
		fi, err := os.Lstat(linkPath)
		if err != nil {
			t.Fatalf("expected symlink %s: %v", n, err)
		}
		if fi.Mode()&os.ModeSymlink == 0 {
			t.Errorf("%s should be a symlink, mode=%v", n, fi.Mode())
		}
		target, err := os.Readlink(linkPath)
		if err != nil {
			t.Fatalf("readlink %s: %v", n, err)
		}
		if target != filepath.Join(cache, n) {
			t.Errorf("symlink target = %q, want %q", target, filepath.Join(cache, n))
		}
	}
	if _, err := os.Lstat(filepath.Join(dist, "ghost.tar.gz")); !os.IsNotExist(err) {
		t.Errorf("ghost.tar.gz must not be linked, lstat err = %v", err)
	}
	if _, err := os.Lstat(filepath.Join(dist, "unrelated-9.tar.gz")); !os.IsNotExist(err) {
		t.Errorf("unrelated-9.tar.gz was not requested and must not be linked, lstat err = %v", err)
	}
}

// TestPrepopulateFromCacheSkipsAnExistingDestination protects whatever is
// already in the distdir — another worker's link, or a real download from a
// previous run — from being replaced by a cache symlink.
func TestPrepopulateFromCacheSkipsAnExistingDestination(t *testing.T) {
	cache := t.TempDir()
	dist := t.TempDir()

	if err := os.WriteFile(filepath.Join(cache, "hello.tar.gz"), []byte("a"), 0o644); err != nil {
		t.Fatal(err)
	}
	// Pre-existing entry in distdir (e.g. from a concurrent worker).
	if err := os.WriteFile(filepath.Join(dist, "hello.tar.gz"), []byte("local"), 0o644); err != nil {
		t.Fatal(err)
	}

	got := PrepopulateFromCache(dist, cache, []string{"hello.tar.gz"})
	if got != 0 {
		t.Errorf("PrepopulateFromCache() = %d, want 0 (file already in distdir)", got)
	}
	// Original content must remain untouched.
	data, err := os.ReadFile(filepath.Join(dist, "hello.tar.gz"))
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "local" {
		t.Errorf("existing distdir file overwritten: %q", data)
	}
}

// TestManifestDistLinesAgreesWithParseManifestDistFilenames pins the two DIST
// parsers to one notion of what a DIST record looks like.
//
// They read the same file for the same run — one decides which archives the
// option gate looks for, the other decides which records the staged Manifest
// carries — so a line that is a record to one and not to the other produces a
// report that proves and denies the same file at once. ManifestDistLines used
// HasPrefix("DIST "), which a tab or a leading space defeats;
// ParseManifestDistFilenames splits fields, which neither defeats.
func TestManifestDistLinesAgreesWithParseManifestDistFilenames(t *testing.T) {
	body := "DIST plain-1.tar.gz 1 BLAKE2B aa SHA512 bb\n" +
		"DIST\ttabbed-2.tar.gz 2 BLAKE2B aa SHA512 bb\n" +
		"  DIST indented-3.tar.gz 3 BLAKE2B aa SHA512 bb\n" +
		"EBUILD pkg-1.ebuild 4 BLAKE2B aa SHA512 bb\n" +
		"DISTANT not-a-record.tar.gz 5 BLAKE2B aa SHA512 bb\n"

	path := filepath.Join(t.TempDir(), "Manifest")
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("writing the Manifest: %v", err)
	}

	kept := string(ManifestDistLines([]byte(body)))
	for _, name := range ParseManifestDistFilenames(path) {
		if !strings.Contains(kept, name) {
			t.Errorf("ParseManifestDistFilenames finds %q but ManifestDistLines drops its record; the option "+
				"gate would look for an archive the staged Manifest never names", name)
		}
	}

	// The converse, so the agreement is not bought by keeping everything: a
	// record type that is not DIST, and a word that merely starts with it, stay
	// out.
	if strings.Contains(kept, "pkg-1.ebuild") {
		t.Error("an EBUILD record survived into the staged Manifest")
	}
	if strings.Contains(kept, "not-a-record.tar.gz") {
		t.Error("a DISTANT line was read as a DIST record")
	}
}
