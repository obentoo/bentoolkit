package autoupdate

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

// TestFetchDistfileLiveFileZillaProSecretsFile proves that a serial placed in
// ~/.config/bentoo/secrets (NOT an env var) is picked up by the production fetch
// path and successfully downloads the FileZilla Pro distfile.
//
// Unlike TestFetchDistfileLiveFileZillaPro (which reads the serial from
// FILEZILLA_PRO_KEY), this test deliberately blanks that env var so resolveSecret
// must fall back to the secrets file — exercising exactly the configuration a
// user sets up by writing `FILEZILLA_PRO_KEY=<serial>` into ~/.config/bentoo/secrets.
//
// Gated on a dedicated opt-in so it never runs in CI or for contributors:
//
//	FILEZILLA_SECRETS_E2E=1            # opt in
//	FILEZILLA_PRO_VERSION=<x.y.z>      # optional; only labels the temp filename
//
// The endpoint always serves the current Latest release regardless of the version
// in the filename, so this test does not pin a SHA (which would rot as Latest
// moves). Instead it asserts the production guards passed: fetchDistfile rejects a
// non-200, an HTML body (invalid serial / rejected request), and a zero-byte body —
// so a returned file that is a non-trivial binary proves the secrets-file serial
// was accepted by the vendor.
func TestFetchDistfileLiveFileZillaProSecretsFile(t *testing.T) {
	if os.Getenv("FILEZILLA_SECRETS_E2E") != "1" {
		t.Skip("set FILEZILLA_SECRETS_E2E=1 to verify the serial in ~/.config/bentoo/secrets")
	}

	// Force the secrets-file path: if the serial is also exported, blank it for
	// this test so resolveSecret cannot satisfy the lookup from the environment.
	t.Setenv("FILEZILLA_PRO_KEY", "")

	// Fail loudly (rather than silently testing nothing) if the secrets file does
	// not actually contain the key — that is the very thing being verified.
	if v, err := resolveSecret("FILEZILLA_PRO_KEY"); err != nil || v == "" {
		t.Fatalf("FILEZILLA_PRO_KEY not resolvable from %s (err=%v) — add `FILEZILLA_PRO_KEY=<serial>` to it", secretsFilePath(), err)
	}

	version := os.Getenv("FILEZILLA_PRO_VERSION")
	if version == "" {
		version = "0.0.0" // cosmetic: the endpoint serves Latest regardless
	}

	// Mirror the packages.toml [meta] block for net-ftp/filezilla-pro verbatim.
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
	got, err := spec.fetchDistfile(context.Background(), version, dir)
	if err != nil {
		t.Fatalf("fetchDistfile (serial from secrets file): %v", err)
	}

	info, err := os.Stat(got)
	if err != nil {
		t.Fatalf("stat downloaded file: %v", err)
	}
	// A real .tar.xz is comfortably above 1 MiB; a re-shown form or stray payload
	// that slipped past the content-type guard would be far smaller.
	const minSize = 1 << 20
	if info.Size() < minSize {
		t.Fatalf("downloaded %q is only %d bytes (< %d) — likely not the real distfile", filepath.Base(got), info.Size(), minSize)
	}

	t.Logf("OK: serial from %s downloaded %s (%d bytes)", secretsFilePath(), filepath.Base(got), info.Size())
}
