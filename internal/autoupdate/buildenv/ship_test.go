package buildenv

import (
	"archive/tar"
	"bytes"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// shippedEntries is what writeTar sends for root: each entry by name.
func shippedEntries(t *testing.T, root string) map[string]*tar.Header {
	t.Helper()
	var buf bytes.Buffer
	if err := writeTar(&buf, []string{root}); err != nil {
		t.Fatal(err)
	}
	entries := map[string]*tar.Header{}
	tr := tar.NewReader(&buf)
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return entries
		}
		if err != nil {
			t.Fatal(err)
		}
		entries[hdr.Name] = hdr
	}
}

// A staged tree as validate writes it: private to the uid that staged it.
func privateTree(t *testing.T) string {
	t.Helper()
	root := filepath.Join(t.TempDir(), "staged")
	pkg := filepath.Join(root, "dev-util", "act")
	if err := os.MkdirAll(pkg, 0o750); err != nil {
		t.Fatal(err)
	}
	for path, mode := range map[string]os.FileMode{
		filepath.Join(pkg, "act-0.2.98.ebuild"): 0o600,
		filepath.Join(pkg, "files.sh"):          0o700,
	} {
		if err := os.WriteFile(path, []byte("EAPI=8\n"), mode); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(path, mode); err != nil { // past the umask
			t.Fatal(err)
		}
	}
	for _, dir := range []string{root, filepath.Join(root, "dev-util"), pkg} {
		if err := os.Chmod(dir, 0o750); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink("act-0.2.98.ebuild", filepath.Join(pkg, "latest.ebuild")); err != nil {
		t.Fatal(err)
	}
	return root
}

func name(path string) string { return strings.TrimPrefix(path, "/") }

// TestShip_APrivateStagedTreeArrivesReadableByThePortageUser is the
// 2026-11-16 failure: a 0600 ebuild under 0750 directories, owned by the uid
// that staged it, could not be sourced by Portage inside the build VM.
func TestShip_APrivateStagedTreeArrivesReadableByThePortageUser(t *testing.T) {
	root := privateTree(t)
	entries := shippedEntries(t, root)
	pkg := filepath.Join(root, "dev-util", "act")

	for path, want := range map[string]int64{
		name(root) + "/": 0o755,
		name(filepath.Join(root, "dev-util")) + "/": 0o755,
		name(pkg) + "/": 0o755,
		name(filepath.Join(pkg, "act-0.2.98.ebuild")): 0o644,
		name(filepath.Join(pkg, "files.sh")):          0o755,
	} {
		hdr, ok := entries[path]
		if !ok {
			t.Fatalf("%s was not shipped; shipped: %v", path, entries)
		}
		if hdr.Mode != want {
			t.Errorf("%s ships with mode %o, want %o: the portage user must read it", path, hdr.Mode, want)
		}
		if hdr.Uid != 0 || hdr.Gid != 0 || hdr.Uname != "root" || hdr.Gname != "root" {
			t.Errorf("%s ships owned by %d:%d (%s:%s), want root: the uid that staged it means nothing inside",
				path, hdr.Uid, hdr.Gid, hdr.Uname, hdr.Gname)
		}
	}
}

func TestShip_ASymlinkIsShippedAsALink(t *testing.T) {
	root := privateTree(t)
	hdr := shippedEntries(t, root)[name(filepath.Join(root, "dev-util", "act", "latest.ebuild"))]
	if hdr == nil || hdr.Typeflag != tar.TypeSymlink || hdr.Linkname != "act-0.2.98.ebuild" {
		t.Fatalf("the symlink was not shipped as a link to its target: %+v", hdr)
	}
}

func TestShip_TheContentIsUnchanged(t *testing.T) {
	root := privateTree(t)
	var buf bytes.Buffer
	if err := writeTar(&buf, []string{root}); err != nil {
		t.Fatal(err)
	}
	tr := tar.NewReader(&buf)
	want := name(filepath.Join(root, "dev-util", "act", "act-0.2.98.ebuild"))
	for {
		hdr, err := tr.Next()
		if err != nil {
			t.Fatalf("%s not found: %v", want, err)
		}
		if hdr.Name != want {
			continue
		}
		got, err := io.ReadAll(tr)
		if err != nil {
			t.Fatal(err)
		}
		if string(got) != "EAPI=8\n" {
			t.Errorf("the ebuild arrived as %q", got)
		}
		return
	}
}

// A directory is made traversable whatever its own bits said: a staged
// directory without them would keep everything under it from the portage user.
func TestShip_ADirectoryIsAlwaysTraversableInside(t *testing.T) {
	hdr := &tar.Header{Mode: 0o640, Uid: 1000, Gid: 1000}
	inEnvironment(hdr, fs.ModeDir|0o640)
	if hdr.Mode != 0o755 {
		t.Errorf("a directory without execute bits ships with mode %o, want 755", hdr.Mode)
	}
}
