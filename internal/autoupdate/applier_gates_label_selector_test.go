package autoupdate

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestScanStaticGates_ALabelledKeyFindsItsEbuild reproduces the 2026-11-13
// run: every `@label` record (app-office/libreoffice-l10n@stable and the like)
// was reported as "holds no ebuild for version", because the full registry key
// travelled as the scan's selector and validate's matchesSelector compares it
// with a bare category or atom. A `:slot` key has the same suffix problem.
func TestScanStaticGates_ALabelledKeyFindsItsEbuild(t *testing.T) {
	for _, key := range []string{"app-office/l10n-demo@stable", "app-office/l10n-demo:0", "app-office/l10n-demo"} {
		t.Run(key, func(t *testing.T) {
			root := t.TempDir()
			pkgDir := filepath.Join(root, "app-office", "l10n-demo")
			if err := os.MkdirAll(pkgDir, 0o755); err != nil {
				t.Fatal(err)
			}
			ebuild := filepath.Join(pkgDir, "l10n-demo-2.0.ebuild")
			body := "EAPI=8\nDESCRIPTION=\"t\"\nHOMEPAGE=\"https://example.com\"\nSLOT=\"0\"\nKEYWORDS=\"~amd64\"\n"
			if err := os.WriteFile(ebuild, []byte(body), 0o644); err != nil {
				t.Fatal(err)
			}
			a, err := NewApplier(t.TempDir(), t.TempDir())
			if err != nil {
				t.Fatalf("NewApplier: %v", err)
			}

			gates := a.scanStaticGates(t.Context(), candidatePaths{staged: true, repoRoot: root, pkgDir: pkgDir, ebuildPath: ebuild}, key, "2.0")

			for _, g := range gates {
				if strings.Contains(g.Reason, "holds no ebuild for version") {
					t.Errorf("the scan did not find the staged ebuild for key %q: %s", key, g.Reason)
				}
			}
		})
	}
}
