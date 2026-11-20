package autoupdate

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

const (
	digestPkg      = "app-misc/hello"
	gentooManifest = "DIST hello-2.12.3.tar.gz 1189208 BLAKE2B aaaa SHA512 bbbb\n" +
		"DIST hello-2.12.2.tar.gz 1168515 BLAKE2B cccc SHA512 dddd\n"
)

func TestCompareDists(t *testing.T) {
	theirs := parseManifestDists([]byte(gentooManifest + "EBUILD hello-2.12.3.ebuild 1 BLAKE2B x SHA512 y\n"))
	cases := []struct {
		name      string
		ours      string
		wantBad   string
		wantAgree int
	}{
		{"same digests", "DIST hello-2.12.3.tar.gz 1189208 BLAKE2B AAAA SHA512 bbbb\n", "", 1},
		{"file ::gentoo does not list", "DIST hello-9.tar.gz 1 BLAKE2B e SHA512 f\n", "", 0},
		{"different size", "DIST hello-2.12.3.tar.gz 1 BLAKE2B aaaa SHA512 bbbb\n", "size 1 here, 1189208 in ::gentoo", 0},
		{"different hash", "DIST hello-2.12.3.tar.gz 1189208 BLAKE2B aaaa SHA512 ffff\n", "SHA512 ffff here, bbbb in ::gentoo", 0},
		{"no hash type in common", "DIST hello-2.12.3.tar.gz 1189208 SHA256 zz\n", "", 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			bad, agreed := compareDists(parseManifestDists([]byte(tc.ours)), theirs)
			if tc.wantBad == "" && len(bad) != 0 {
				t.Errorf("mismatches = %q, want none", bad)
			}
			if tc.wantBad != "" && (len(bad) != 1 || !strings.Contains(bad[0], tc.wantBad)) {
				t.Errorf("mismatches = %q, want one naming %q", bad, tc.wantBad)
			}
			if agreed != tc.wantAgree {
				t.Errorf("agreed = %d, want %d", agreed, tc.wantAgree)
			}
		})
	}
}

// digestFixture lays out a ::gentoo tree holding app-misc/hello's Manifest
// and a candidate package directory, and returns both.
func digestFixture(t *testing.T, gentooBody string) (gentoo, pkgDir string) {
	t.Helper()
	gentoo = t.TempDir()
	if gentooBody != "" {
		dir := filepath.Join(gentoo, digestPkg)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "Manifest"), []byte(gentooBody), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	pkgDir = filepath.Join(t.TempDir(), digestPkg)
	if err := os.MkdirAll(pkgDir, 0o755); err != nil {
		t.Fatal(err)
	}
	return gentoo, pkgDir
}

func TestCrossCheckGentooDigests(t *testing.T) {
	tampered := "DIST hello-2.12.3.tar.gz 1189208 BLAKE2B 0bad SHA512 bbbb\n"
	cases := []struct {
		name       string
		gentoo     string
		ours       string
		noGentoo   bool
		wantRefuse bool
	}{
		{"tampered archive is refused", gentooManifest, tampered, false, true},
		{"matching archive passes", gentooManifest, "DIST hello-2.12.3.tar.gz 1189208 BLAKE2B aaaa SHA512 bbbb\n", false, false},
		{"package absent from ::gentoo passes", "", tampered, false, false},
		{"no ::gentoo tree configured passes", gentooManifest, tampered, true, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			gentoo, pkgDir := digestFixture(t, tc.gentoo)
			if err := os.WriteFile(filepath.Join(pkgDir, "Manifest"), []byte(tc.ours), 0o644); err != nil {
				t.Fatal(err)
			}
			a := &Applier{gentooPath: gentoo}
			if tc.noGentoo {
				a.gentooPath = ""
			}
			err := a.crossCheckGentooDigests(pkgDir, digestPkg)
			if tc.wantRefuse != errors.Is(err, ErrDistfileDigestMismatch) {
				t.Fatalf("crossCheckGentooDigests = %v, want refused=%v", err, tc.wantRefuse)
			}
			if tc.wantRefuse && !strings.Contains(err.Error(), "hello-2.12.3.tar.gz: BLAKE2B 0bad here, aaaa in ::gentoo") {
				t.Errorf("the refusal does not name the file and both digests: %v", err)
			}
		})
	}
}

// TestRunManifestWithFix_RefusesADigestGentooDisagreesWith: the manifest
// step itself succeeds, so the cross-check is what stops the bump, and the
// LLM fixer, whose job is a manifest that fails, is never asked to edit
// around it.
func TestRunManifestWithFix_RefusesADigestGentooDisagreesWith(t *testing.T) {
	gentoo, pkgDir := digestFixture(t, gentooManifest)
	repo := filepath.Dir(filepath.Dir(pkgDir))
	if err := os.WriteFile(filepath.Join(pkgDir, "hello-2.12.3.ebuild"), []byte("EAPI=8\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	seam := func(ctx context.Context, name string, arg ...string) *exec.Cmd {
		if name == "pkgdev" {
			// The download a compromised host served, digested.
			body := "DIST hello-2.12.3.tar.gz 1189208 BLAKE2B 0bad SHA512 0bad\n"
			if err := os.WriteFile(filepath.Join(pkgDir, "Manifest"), []byte(body), 0o644); err != nil {
				t.Errorf("fake pkgdev: %v", err)
			}
		}
		return exec.CommandContext(ctx, "true")
	}
	fx := &fakeFixer{summary: "rewrote SRC_URI"}
	a, err := NewApplier(t.TempDir(), t.TempDir(),
		WithExecCommand(seam),
		WithApplierFixer(fx),
		WithApplierGentooPath(gentoo),
		WithApplierDistdir(t.TempDir(), ""),
	)
	if err != nil {
		t.Fatalf("NewApplier: %v", err)
	}
	cand := candidatePaths{staged: true, repoRoot: repo, pkgDir: pkgDir, ebuildPath: filepath.Join(pkgDir, "hello-2.12.3.ebuild")}

	_, err = a.runManifestWithFix(t.Context(), cand, digestPkg, "2.12.3", &ApplyResult{})

	if !errors.Is(err, ErrDistfileDigestMismatch) {
		t.Fatalf("runManifestWithFix = %v, want ErrDistfileDigestMismatch", err)
	}
	if fx.called != 0 {
		t.Errorf("the LLM fixer was called %d time(s) for a digest mismatch", fx.called)
	}
}
