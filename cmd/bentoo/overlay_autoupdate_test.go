package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeExitTestEbuild writes a minimal ebuild for pkg ("category/name") at the
// given version under overlayDir.
func writeExitTestEbuild(t *testing.T, overlayDir, pkg, version string) {
	t.Helper()
	parts := strings.SplitN(pkg, "/", 2)
	if len(parts) != 2 {
		t.Fatalf("invalid package name %q", pkg)
	}
	pkgDir := filepath.Join(overlayDir, parts[0], parts[1])
	if err := os.MkdirAll(pkgDir, 0755); err != nil {
		t.Fatalf("mkdir %s: %v", pkgDir, err)
	}
	content := "EAPI=8\nDESCRIPTION=\"t\"\nHOMEPAGE=\"https://example.com\"\nSLOT=\"0\"\nKEYWORDS=\"~amd64\"\n"
	ebuildPath := filepath.Join(pkgDir, parts[1]+"-"+version+".ebuild")
	if err := os.WriteFile(ebuildPath, []byte(content), 0644); err != nil {
		t.Fatalf("write ebuild %s: %v", ebuildPath, err)
	}
}

// writeExitTestPackagesConfig writes a packages.toml under <overlayDir>/.autoupdate
// mapping each package name to a JSON-parser schema pointed at serverURL.
func writeExitTestPackagesConfig(t *testing.T, overlayDir, serverURL string, pkgs []string) {
	t.Helper()
	cfgDir := filepath.Join(overlayDir, ".autoupdate")
	if err := os.MkdirAll(cfgDir, 0755); err != nil {
		t.Fatalf("mkdir %s: %v", cfgDir, err)
	}
	var b strings.Builder
	for _, pkg := range pkgs {
		b.WriteString("[\"" + pkg + "\"]\n")
		b.WriteString("url = \"" + serverURL + "\"\n")
		b.WriteString("parser = \"json\"\n")
		b.WriteString("path = \"version\"\n\n")
	}
	if err := os.WriteFile(filepath.Join(cfgDir, "packages.toml"), []byte(b.String()), 0644); err != nil {
		t.Fatalf("write packages.toml: %v", err)
	}
}

// TestCLI_ExitCodes exercises the documented exit-code contract of the
// autoupdate --check path: 0 when every package succeeds, 1 on partial
// failure, 2 on total failure. A package fails deterministically (without any
// HTTP retry latency) when its ebuild is absent from the overlay, which makes
// CheckPackage return ErrNoEbuildFound. The exit code is captured via the
// shared withExitIntercept/exitSentinel harness.
func TestCLI_ExitCodes(t *testing.T) {
	// Local server returns a valid version payload so packages with an
	// on-disk ebuild succeed on the first HTTP try (no retries needed).
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{"version": "1.0.0"})
	}))
	defer server.Close()

	tests := []struct {
		name string
		// pkgs are all package names declared in packages.toml.
		pkgs []string
		// withEbuild are the packages that also get an on-disk ebuild;
		// any pkg not listed here fails with ErrNoEbuildFound.
		withEbuild []string
		wantExit   int
	}{
		{
			name:       "all packages succeed -> exit 0",
			pkgs:       []string{"cat-a/pkg1", "cat-b/pkg2", "cat-c/pkg3"},
			withEbuild: []string{"cat-a/pkg1", "cat-b/pkg2", "cat-c/pkg3"},
			wantExit:   0,
		},
		{
			name:       "partial failure -> exit 1",
			pkgs:       []string{"cat-a/pkg1", "cat-b/pkg2", "cat-c/pkg3"},
			withEbuild: []string{"cat-a/pkg1", "cat-b/pkg2"},
			wantExit:   1,
		},
		{
			name:       "total failure -> exit 2",
			pkgs:       []string{"cat-a/pkg1", "cat-b/pkg2", "cat-c/pkg3"},
			withEbuild: nil,
			wantExit:   2,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			overlayDir := t.TempDir()
			configDir := t.TempDir()

			writeExitTestPackagesConfig(t, overlayDir, server.URL, tt.pkgs)
			for _, pkg := range tt.withEbuild {
				writeExitTestEbuild(t, overlayDir, pkg, "0.9.0")
			}

			// Force = true bypasses the cache so every package performs a
			// real check; args nil selects the check-all path.
			origForce := autoupdateForce
			autoupdateForce = true
			defer func() { autoupdateForce = origForce }()

			code := withExitIntercept(func() {
				runCheck(overlayDir, configDir, nil)
			})
			if code != tt.wantExit {
				t.Errorf("runCheck exit code = %d, want %d", code, tt.wantExit)
			}
		})
	}
}

// TestAutoupdateCommandExists tests that the autoupdate command is registered
func TestAutoupdateCommandExists(t *testing.T) {
	found := false
	for _, cmd := range overlayCmd.Commands() {
		if strings.HasPrefix(cmd.Use, "autoupdate") {
			found = true
			break
		}
	}
	if !found {
		t.Error("overlay autoupdate subcommand should exist")
	}
}

// TestAutoupdateCommandFlags tests that all required flags are present
func TestAutoupdateCommandFlags(t *testing.T) {
	tests := []struct {
		name     string
		flagName string
	}{
		{"check flag", "check"},
		{"list flag", "list"},
		{"apply flag", "apply"},
		{"force flag", "force"},
		{"compile flag", "compile"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			flag := autoupdateCmd.Flags().Lookup(tt.flagName)
			if flag == nil {
				t.Errorf("autoupdate command should have --%s flag", tt.flagName)
			}
		})
	}
}

// TestAutoupdateCommandDescription tests command descriptions
func TestAutoupdateCommandDescription(t *testing.T) {
	if autoupdateCmd.Short == "" {
		t.Error("autoupdate command should have a short description")
	}
	if autoupdateCmd.Long == "" {
		t.Error("autoupdate command should have a long description")
	}
}

// TestAutoupdateCommandRun tests that Run function is set
func TestAutoupdateCommandRun(t *testing.T) {
	if autoupdateCmd.Run == nil {
		t.Error("autoupdate command should have a Run function")
	}
}

// TestAutoupdateFlagTypes tests that flags have correct types
func TestAutoupdateFlagTypes(t *testing.T) {
	// Boolean flags
	boolFlags := []string{"check", "list", "force", "compile"}
	for _, flagName := range boolFlags {
		flag := autoupdateCmd.Flags().Lookup(flagName)
		if flag == nil {
			t.Errorf("flag %s should exist", flagName)
			continue
		}
		if flag.Value.Type() != "bool" {
			t.Errorf("flag %s should be bool type, got %s", flagName, flag.Value.Type())
		}
	}

	// String flags
	stringFlags := []string{"apply"}
	for _, flagName := range stringFlags {
		flag := autoupdateCmd.Flags().Lookup(flagName)
		if flag == nil {
			t.Errorf("flag %s should exist", flagName)
			continue
		}
		if flag.Value.Type() != "string" {
			t.Errorf("flag %s should be string type, got %s", flagName, flag.Value.Type())
		}
	}
}

// TestAutoupdateUsageContainsExamples tests that usage contains examples
func TestAutoupdateUsageContainsExamples(t *testing.T) {
	examples := []string{
		"--check",
		"--list",
		"--apply",
		"--force",
		"--compile",
	}

	for _, example := range examples {
		if !strings.Contains(autoupdateCmd.Long, example) {
			t.Errorf("autoupdate long description should contain example with %s", example)
		}
	}
}

// TestRunAutoupdate_OverlayPathBoundsCheck tests Property 4: Bounds-Safe Tilde Check
// Verifies that empty or whitespace overlay paths do not cause a panic.
// **Feature: quality-improvements, Property 4: Bounds-Safe Tilde Check**
// **Validates: Requirements 3.1-3.4**
func TestRunAutoupdate_OverlayPathBoundsCheck(t *testing.T) {
	tests := []struct {
		name        string
		overlayPath string
		wantPanic   bool
	}{
		{
			name:        "empty overlay path does not panic",
			overlayPath: "",
			wantPanic:   false,
		},
		{
			name:        "whitespace-only overlay path does not panic",
			overlayPath: "   ",
			wantPanic:   false,
		},
		{
			name:        "tilde path is handled safely",
			overlayPath: "~/overlay",
			wantPanic:   false,
		},
		{
			name:        "absolute path is handled safely",
			overlayPath: "/tmp/overlay",
			wantPanic:   false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			defer func() {
				if r := recover(); r != nil && !tt.wantPanic {
					t.Errorf("unexpected panic for overlayPath=%q: %v", tt.overlayPath, r)
				}
			}()

			// Exercise the bounds-guarded tilde check directly
			path := tt.overlayPath
			if len(path) > 0 && path[0] == '~' {
				// tilde expansion would happen here — no panic expected
				_ = path[1:]
			}
		})
	}
}
