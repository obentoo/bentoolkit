package validate

// Authored for story 031, sub-task 2.1 — R1, R1.5.
//
// Written from the contract. design.md D4 fixes the approach — the host's
// `tar`, listing then extracting to stdout, nothing written to disk — and the
// guards: member names validated before becoming arguments, and a 4 MiB cap on
// the bytes read. This file pins two unexported names design.md left unnamed,
// `listArchiveMembers` and `extractArchiveMember`; the behaviours are not
// negotiable, those two identifiers are.
//
// The `.tar.gz` fixture is BUILT here with the standard library rather than
// committed, so there is one less file to drift. The `.tar.xz` fixture has to
// be a committed artefact: Go cannot write xz, which is the very reason D4
// shells out to `tar` instead of adding a dependency.
//
// Red is DEFERRED to Run mode: the package does not exist yet.

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// buildTarGz writes a gzipped tarball containing the given member→content
// pairs and returns its path.
func buildTarGz(t *testing.T, members map[string]string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "fixture.tar.gz")
	f, err := os.Create(path)
	if err != nil {
		t.Fatalf("creating fixture: %v", err)
	}
	defer func() { _ = f.Close() }()

	gz := gzip.NewWriter(f)
	tw := tar.NewWriter(gz)
	for name, content := range members {
		hdr := &tar.Header{Name: name, Mode: 0o644, Size: int64(len(content))}
		if err := tw.WriteHeader(hdr); err != nil {
			t.Fatalf("writing header %q: %v", name, err)
		}
		if _, err := tw.Write([]byte(content)); err != nil {
			t.Fatalf("writing body %q: %v", name, err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatalf("closing tar: %v", err)
	}
	if err := gz.Close(); err != nil {
		t.Fatalf("closing gzip: %v", err)
	}
	return path
}

func TestListArchiveMembers_TarGz(t *testing.T) {
	archive := buildTarGz(t, map[string]string{
		"proj-1.0/meson.build":                   "project('proj')\n",
		"proj-1.0/meson.options":                 "option('aalib')\n",
		"proj-1.0/subprojects/sub/meson.options": "option('gl')\n",
	})

	got, err := listArchiveMembers(context.Background(), archive)
	if err != nil {
		t.Fatalf("listArchiveMembers: %v", err)
	}

	for _, want := range []string{"proj-1.0/meson.options", "proj-1.0/subprojects/sub/meson.options"} {
		if !containsString(got, want) {
			t.Errorf("member %q missing from listing %v", want, got)
		}
	}
}

// TestListArchiveMembers_TarXz proves the format that actually matters. Go has
// no xz reader, so this is the case that justifies shelling out at all.
func TestListArchiveMembers_TarXz(t *testing.T) {
	archive := filepath.Join("testdata", "root-meson-options.tar.xz")
	if _, err := os.Stat(archive); err != nil {
		t.Fatalf("fixture %s is missing: %v — the xz fixture is committed because Go cannot write one", archive, err)
	}

	got, err := listArchiveMembers(context.Background(), archive)
	if err != nil {
		t.Fatalf("listArchiveMembers: %v", err)
	}
	if len(got) == 0 {
		t.Fatal("listing an xz archive returned no members")
	}
}

func TestExtractArchiveMember_ReturnsBytes(t *testing.T) {
	const body = "option('aalib', type : 'feature')\n"
	archive := buildTarGz(t, map[string]string{
		"proj-1.0/meson.build":   "project('proj')\n",
		"proj-1.0/meson.options": body,
	})

	got, err := extractArchiveMember(context.Background(), archive, "proj-1.0/meson.options")
	if err != nil {
		t.Fatalf("extractArchiveMember: %v", err)
	}
	if string(got) != body {
		t.Errorf("extracted %q, want %q", got, body)
	}
}

// TestExtractArchiveMember_WritesNothingToDisk is R1.5, and it is also what
// makes archive path traversal structurally impossible rather than something a
// check has to catch.
func TestExtractArchiveMember_WritesNothingToDisk(t *testing.T) {
	archive := buildTarGz(t, map[string]string{
		"proj-1.0/meson.build":   "project('proj')\n",
		"proj-1.0/meson.options": "option('aalib')\n",
	})
	work := t.TempDir()
	before := countEntries(t, work)

	cwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	if err := os.Chdir(work); err != nil {
		t.Fatalf("chdir: %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(cwd) })

	if _, err := extractArchiveMember(context.Background(), archive, "proj-1.0/meson.options"); err != nil {
		t.Fatalf("extractArchiveMember: %v", err)
	}

	if after := countEntries(t, work); after != before {
		t.Errorf("working directory gained %d entries; extraction must go to stdout and never to disk", after-before)
	}
}

// TestExtractArchiveMember_RejectsFlagLikeName guards the argument boundary: a
// crafted member name must never become a `tar` flag.
func TestExtractArchiveMember_RejectsFlagLikeName(t *testing.T) {
	archive := buildTarGz(t, map[string]string{"proj-1.0/meson.build": "project('proj')\n"})

	for _, name := range []string{"--to-command=sh", "-C/etc", ""} {
		if _, err := extractArchiveMember(context.Background(), archive, name); err == nil {
			t.Errorf("member name %q was accepted; a name that can be read as a flag must be rejected before exec", name)
		}
	}
}

// TestExtractArchiveMember_CapsTheBytesRead pins the 4 MiB ceiling from
// design.md's Performance section: a compressed bomb must fail loudly, not
// exhaust memory.
func TestExtractArchiveMember_CapsTheBytesRead(t *testing.T) {
	const overCap = 5 << 20 // 5 MiB, above the 4 MiB cap
	archive := buildTarGz(t, map[string]string{
		"proj-1.0/meson.build":   "project('proj')\n",
		"proj-1.0/meson.options": strings.Repeat("x", overCap),
	})

	_, err := extractArchiveMember(context.Background(), archive, "proj-1.0/meson.options")

	if err == nil {
		t.Fatal("extracting a member above the cap returned no error; the cap exists so a bomb fails loudly")
	}
}

func TestListArchiveMembers_MissingArchiveNamesIt(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "absent.tar.xz")

	_, err := listArchiveMembers(context.Background(), missing)

	if err == nil {
		t.Fatal("listing a missing archive returned no error")
	}
	if !strings.Contains(err.Error(), missing) {
		t.Errorf("error %q does not name the archive %q", err, missing)
	}
}

func containsString(haystack []string, needle string) bool {
	for _, s := range haystack {
		if s == needle {
			return true
		}
	}
	return false
}

func countEntries(t *testing.T, dir string) int {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("reading %q: %v", dir, err)
	}
	return len(entries)
}
