package autoupdate

import (
	"context"
	"crypto/sha512"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"
)

// TestFetchDistfileLiveFileZillaPro exercises the real production fetch path
// against filezilla-project.org using a purchased serial. It is gated on two
// env vars so it never runs in CI or for contributors without a license:
//
//	FILEZILLA_PRO_E2E=1               # opt in
//	FILEZILLA_PRO_KEY=<your serial>   # the serial (also read by resolveSecret)
//
// It downloads FileZilla Pro 3.70.5 and asserts the SHA512 matches the value in
// the overlay Manifest, proving the authenticated POST + form-field set yields
// the exact distfile pkgdev would digest. The serial is never printed.
func TestFetchDistfileLiveFileZillaPro(t *testing.T) {
	if os.Getenv("FILEZILLA_PRO_E2E") != "1" {
		t.Skip("set FILEZILLA_PRO_E2E=1 and FILEZILLA_PRO_KEY to run the live FileZilla Pro fetch")
	}
	if os.Getenv("FILEZILLA_PRO_KEY") == "" {
		t.Skip("FILEZILLA_PRO_KEY not set")
	}

	const (
		wantVersion = "3.70.5"
		wantSHA512  = "465d27c23c63853cf116bab32e04884cb93664a7333e554f825caf06e8168dbb" +
			"2892264475a309aad80d16959ad05e4f160f7666bd5b7717731f9d00d76d0c80"
		wantName = "FileZilla_Pro_3.70.5_x86_64-linux-gnu.tar.xz"
	)

	// Mirror the packages.toml [meta] block for net-ftp/filezilla-pro.
	spec, ok, err := parseAuthFetchSpec(map[string]string{
		metaFetchMethod:      "post",
		metaFetchURL:         "https://filezilla-project.org/prodownload.php?beta=0",
		metaFetchSerialEnv:   "FILEZILLA_PRO_KEY",
		metaFetchSerialField: "key",
		metaFetchFilename:    "FileZilla_Pro_{version}_x86_64-linux-gnu.tar.xz",
		metaFetchForm: "mail=&number=&platform=linux&platform_cli=win&platform_cli_nonpro=win&" +
			"platform_fzpes=win&download_program=Start download of FileZilla Pro",
	})
	if err != nil || !ok {
		t.Fatalf("parseAuthFetchSpec: ok=%v err=%v", ok, err)
	}

	dir := t.TempDir()
	got, err := spec.fetchDistfile(context.Background(), wantVersion, dir)
	if err != nil {
		t.Fatalf("fetchDistfile: %v", err)
	}
	if filepath.Base(got) != wantName {
		t.Fatalf("filename = %q, want %q", filepath.Base(got), wantName)
	}

	data, err := os.ReadFile(got)
	if err != nil {
		t.Fatalf("read downloaded file: %v", err)
	}
	sum := sha512.Sum512(data)
	if gotSHA := hex.EncodeToString(sum[:]); gotSHA != wantSHA512 {
		t.Fatalf("SHA512 mismatch:\n got %s\nwant %s (size=%d)", gotSHA, wantSHA512, len(data))
	}
	t.Logf("OK: downloaded %s (%d bytes), SHA512 matches Manifest", wantName, len(data))
}
