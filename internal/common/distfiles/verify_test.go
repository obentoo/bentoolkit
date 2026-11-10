package distfiles

import (
	"crypto/sha512"
	"encoding/hex"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"testing"
)

func TestDropUnverified(t *testing.T) {
	full := []byte("the whole archive")
	sum := sha512.Sum512(full)
	record := func(name string, size int, sha string) string {
		return "DIST " + name + " " + strconv.Itoa(size) + " BLAKE2B x SHA512 " + sha
	}
	manifest := s084Body(
		record("good.tar.gz", len(full), hex.EncodeToString(sum[:])),
		record("truncated.tar.gz", len(full), hex.EncodeToString(sum[:])),
		record("corrupt.tar.gz", len(full), hex.EncodeToString(sum[:])),
		"DIST sizeonly.tar.gz "+strconv.Itoa(len(full)),
	)
	corrupt := []byte("the whole archivX")

	for _, keepUnlisted := range []bool{true, false} {
		t.Run("keepUnlisted="+strconv.FormatBool(keepUnlisted), func(t *testing.T) {
			dir := t.TempDir()
			write := func(name string, body []byte) {
				if err := os.WriteFile(filepath.Join(dir, name), body, 0o600); err != nil {
					t.Fatal(err)
				}
			}
			write("good.tar.gz", full)
			write("truncated.tar.gz", full[:5])
			write("corrupt.tar.gz", corrupt)
			write("sizeonly.tar.gz", corrupt)
			write("unlisted.tar.gz", full[:3])
			if err := os.Symlink(filepath.Join(dir, "missing"), filepath.Join(dir, "link.tar.gz")); err != nil {
				t.Fatal(err)
			}

			dropped, failures := DropUnverified(dir, manifest, keepUnlisted)
			if len(failures) != 0 {
				t.Fatalf("failures: %v", failures)
			}
			want := []string{"corrupt.tar.gz", "truncated.tar.gz"}
			if !keepUnlisted {
				want = append(want, "unlisted.tar.gz")
			}
			slices.Sort(dropped)
			if !slices.Equal(dropped, want) {
				t.Errorf("dropped %v, want %v", dropped, want)
			}
			for _, name := range want {
				if _, err := os.Lstat(filepath.Join(dir, name)); !os.IsNotExist(err) {
					t.Errorf("%s still present (err %v)", name, err)
				}
			}
			for _, name := range []string{"good.tar.gz", "sizeonly.tar.gz", "link.tar.gz"} {
				if _, err := os.Lstat(filepath.Join(dir, name)); err != nil {
					t.Errorf("%s was removed: %v", name, err)
				}
			}
		})
	}
}

func TestDropUnverifiedMissingDirIsReported(t *testing.T) {
	_, failures := DropUnverified(filepath.Join(t.TempDir(), "absent"), nil, true)
	if len(failures) != 1 {
		t.Fatalf("want one failure for an unreadable distdir, got %v", failures)
	}
}
