package validate

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// s089sTarGz writes a gzip-compressed tar archive holding exactly the given
// members (member path -> content) and returns its path.
func s089sTarGz(t *testing.T, dir, name string, members map[string]string) string {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	names := make([]string, 0, len(members))
	for n := range members {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		body := members[n]
		if err := tw.WriteHeader(&tar.Header{Name: n, Mode: 0o644, Size: int64(len(body)), Typeflag: tar.TypeReg}); err != nil {
			t.Fatalf("tar header %s: %v", n, err)
		}
		if _, err := tw.Write([]byte(body)); err != nil {
			t.Fatalf("tar body %s: %v", n, err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatalf("tar close: %v", err)
	}
	if err := gz.Close(); err != nil {
		t.Fatalf("gzip close: %v", err)
	}
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, buf.Bytes(), 0o600); err != nil {
		t.Fatalf("write archive: %v", err)
	}
	return p
}

// R3: the build system of an archive is answered from the archive itself.
// Hostile cases first: a Meson project that vendors a CMake tree must stay
// "meson" (the rule must not split one Meson project into "cmake"), and a CMake
// project must not be collapsed into "meson" just because it is a buildable
// project.
func TestS089ArchiveBuildSystem_MesonRootWithVendoredCMakeIsMeson(t *testing.T) {
	archive := s089sTarGz(t, t.TempDir(), "proj-1.0.tar.gz", map[string]string{
		"proj-1.0/meson.build":                         "project('proj', 'c')\n",
		"proj-1.0/meson_options.txt":                   "option('x', type: 'boolean', value: true)\n",
		"proj-1.0/subprojects/vendored/CMakeLists.txt": "cmake_minimum_required(VERSION 3.10)\n",
		"proj-1.0/subprojects/vendored/vendored.c":     "int v;\n",
	})
	got, err := ArchiveBuildSystem(context.Background(), archive)
	if err != nil {
		t.Fatalf("ArchiveBuildSystem(%s) error = %v, want nil", archive, err)
	}
	if got != "meson" {
		t.Fatalf("ArchiveBuildSystem = %q, want %q: a Meson root with a vendored CMake tree is a Meson project", got, "meson")
	}
}

func TestS089ArchiveBuildSystem_CMakeRootIsNotMeson(t *testing.T) {
	archive := s089sTarGz(t, t.TempDir(), "cm-2.0.tar.gz", map[string]string{
		"cm-2.0/CMakeLists.txt": "project(cm C)\n",
		"cm-2.0/src/main.c":     "int main(void){return 0;}\n",
	})
	got, err := ArchiveBuildSystem(context.Background(), archive)
	if err != nil {
		t.Fatalf("ArchiveBuildSystem(%s) error = %v, want nil (a readable non-Meson archive is an answer, not a failure)", archive, err)
	}
	if got != "cmake" {
		t.Fatalf("ArchiveBuildSystem = %q, want %q", got, "cmake")
	}
}

func TestS089ArchiveBuildSystem_MesonOptionsOnlyRootIsMeson(t *testing.T) {
	// meson.options (Meson >= 1.1) beside meson.build, no meson_options.txt.
	archive := s089sTarGz(t, t.TempDir(), "new-3.0.tar.gz", map[string]string{
		"new-3.0/meson.build":   "project('new', 'c')\n",
		"new-3.0/meson.options": "option('y', type: 'boolean', value: false)\n",
	})
	got, err := ArchiveBuildSystem(context.Background(), archive)
	if err != nil {
		t.Fatalf("ArchiveBuildSystem(%s) error = %v, want nil", archive, err)
	}
	if got != "meson" {
		t.Fatalf("ArchiveBuildSystem = %q, want %q", got, "meson")
	}
}

func TestS089ArchiveBuildSystem_NoMarkerIsUndetermined(t *testing.T) {
	archive := s089sTarGz(t, t.TempDir(), "plain-1.0.tar.gz", map[string]string{
		"plain-1.0/README":    "nothing builds this\n",
		"plain-1.0/src/a.txt": "a\n",
	})
	got, err := ArchiveBuildSystem(context.Background(), archive)
	if err != nil {
		t.Fatalf("ArchiveBuildSystem(%s) error = %v, want nil", archive, err)
	}
	if got != "undetermined" {
		t.Fatalf("ArchiveBuildSystem = %q, want %q", got, "undetermined")
	}
}

func TestS089ArchiveBuildSystem_UnreadableArchiveErrorsNamingThePath(t *testing.T) {
	dir := t.TempDir()
	garbage := filepath.Join(dir, "broken-1.0.tar.gz")
	if err := os.WriteFile(garbage, []byte("this is not a tar archive"), 0o600); err != nil {
		t.Fatal(err)
	}
	missing := filepath.Join(dir, "absent-1.0.tar.gz")

	for _, archive := range []string{garbage, missing} {
		got, err := ArchiveBuildSystem(context.Background(), archive)
		if err == nil {
			t.Fatalf("ArchiveBuildSystem(%s) = %q, nil; want an error", archive, got)
		}
		if !strings.Contains(err.Error(), archive) {
			t.Fatalf("ArchiveBuildSystem(%s) error = %q, want it to name the archive path", archive, err)
		}
		if got == "meson" {
			t.Fatalf("ArchiveBuildSystem(%s) answered %q alongside an error", archive, got)
		}
	}
}
