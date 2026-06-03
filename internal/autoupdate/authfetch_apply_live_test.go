package autoupdate

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// A minimal ebuild whose SRC_URI basename matches the FileZilla Pro distfile.
// It deliberately omits `inherit` so pkgdev needs no eclasses to source it: the
// test exercises the authenticated fetch + manifest digest, not the real build.
const fzTestEbuild = `EAPI=8
MY_PV="${PV/_/-}"
MY_P="FileZilla_Pro_${MY_PV}"
DESCRIPTION="FileZilla Pro (e2e test ebuild)"
HOMEPAGE="https://filezillapro.com/"
SRC_URI="https://distfiles.obentoo.org/${MY_P}_x86_64-linux-gnu.tar.xz"
LICENSE="GPL-2"
SLOT="0"
KEYWORDS="~amd64"
`

// TestApplyLiveFileZillaPro drives the full Apply path end-to-end against the
// real vendor endpoint: it injects a pending 3.70.4 -> 3.70.5 update, runs
// Apply, and asserts that the authenticated fetch populated the distdir so
// `pkgdev manifest` produced a Manifest with the known-good SHA512. Gated on
// FILEZILLA_PRO_E2E=1 + FILEZILLA_PRO_KEY + pkgdev, so it never runs unattended.
func TestApplyLiveFileZillaPro(t *testing.T) {
	if os.Getenv("FILEZILLA_PRO_E2E") != "1" || os.Getenv("FILEZILLA_PRO_KEY") == "" {
		t.Skip("set FILEZILLA_PRO_E2E=1 and FILEZILLA_PRO_KEY to run the live apply")
	}
	if _, err := exec.LookPath("pkgdev"); err != nil {
		t.Skip("pkgdev not installed")
	}

	const (
		pkg        = "net-ftp/filezilla-pro"
		oldVer     = "3.70.4"
		newVer     = "3.70.5"
		wantSHA512 = "465d27c23c63853cf116bab32e04884cb93664a7333e554f825caf06e8168dbb" +
			"2892264475a309aad80d16959ad05e4f160f7666bd5b7717731f9d00d76d0c80"
	)

	overlay := t.TempDir()
	writeFile(t, filepath.Join(overlay, "metadata", "layout.conf"), "masters = gentoo\nthin-manifests = true\n")
	writeFile(t, filepath.Join(overlay, "profiles", "repo_name"), "fztest\n")

	pkgDir := filepath.Join(overlay, "net-ftp", "filezilla-pro")
	writeFile(t, filepath.Join(pkgDir, "filezilla-pro-"+oldVer+".ebuild"), fzTestEbuild)
	// Seed the Manifest with the OLD version's DIST entry, as a real overlay
	// would have it. The vendor only serves the latest release, so the old
	// distfile is no longer downloadable — but pkgdev trusts an existing
	// Manifest entry when the file is absent, so the bump only needs to fetch
	// and digest the NEW distfile. (Hashes here are placeholders of the right
	// shape; pkgdev does not re-verify an absent file.)
	writeFile(t, filepath.Join(pkgDir, "Manifest"),
		"DIST FileZilla_Pro_"+oldVer+"_x86_64-linux-gnu.tar.xz 13000000 BLAKE2B "+
			"914d9c8e542b3f36a49c0696cc1bf7f79bdfe2562ad3f36f9760d89e4172fedcb44d2591a2fcfcab2ab99c2e3675292cb8297113fe7960f0b98f5b47a40a2523 SHA512 "+
			"465d27c23c63853cf116bab32e04884cb93664a7333e554f825caf06e8168dbb2892264475a309aad80d16959ad05e4f160f7666bd5b7717731f9d00d76d0c80\n")

	configDir := t.TempDir()
	pending, err := NewPendingList(configDir)
	if err != nil {
		t.Fatalf("NewPendingList: %v", err)
	}
	if err := pending.Add(PendingUpdate{
		Package: pkg, CurrentVersion: oldVer, NewVersion: newVer,
		Status: StatusPending, DetectedAt: time.Unix(0, 0).UTC(),
	}); err != nil {
		t.Fatalf("pending.Add: %v", err)
	}

	cfg := &PackagesConfig{Packages: map[string]PackageConfig{
		pkg: {URL: "https://x.test", Parser: "regex", Pattern: `(\d)`, Meta: map[string]string{
			metaFetchMethod:      "post",
			metaFetchURL:         "https://filezilla-project.org/prodownload.php?beta=0",
			metaFetchSerialEnv:   "FILEZILLA_PRO_KEY",
			metaFetchSerialField: "key",
			metaFetchFilename:    "FileZilla_Pro_{version}_x86_64-linux-gnu.tar.xz",
			metaFetchForm: "mail=&number=&platform=linux&platform_cli=win&platform_cli_nonpro=win&" +
				"platform_fzpes=win&download_program=Start download of FileZilla Pro",
		}},
	}}

	applier, err := NewApplier(overlay, configDir,
		WithApplierPendingList(pending),
		WithApplierPackagesConfig(cfg),
	)
	if err != nil {
		t.Fatalf("NewApplier: %v", err)
	}

	result, err := applier.Apply(pkg, false)
	if err != nil {
		t.Fatalf("Apply: %v (result=%+v)", err, result)
	}
	if !result.Success {
		t.Fatalf("Apply did not succeed: %+v", result)
	}

	if _, err := os.Stat(filepath.Join(pkgDir, "filezilla-pro-"+newVer+".ebuild")); err != nil {
		t.Fatalf("new ebuild missing: %v", err)
	}
	man, err := os.ReadFile(filepath.Join(pkgDir, "Manifest"))
	if err != nil {
		t.Fatalf("read Manifest: %v", err)
	}
	if !strings.Contains(string(man), wantSHA512) {
		t.Fatalf("Manifest missing expected SHA512:\n%s", man)
	}
	t.Logf("apply OK; Manifest:\n%s", man)
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}
