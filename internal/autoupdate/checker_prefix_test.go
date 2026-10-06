package autoupdate

// The upstream tag prefix is stripped ONCE, at the checker's convergence point,
// and never reaches a consumer. Before this, "v3.2.3" flowed raw into
// CheckResult and pending.json: the applier stripped it defensively at apply
// time, but the validation plan classified the raw value, could not read it as
// a Gentoo version, and charged a patch bump the deepest class — pricing hours
// of configure the run would never spend. These cases hold the two outputs the
// strip must clean: the check result the operator reads, and the pending entry
// every later plan is built from.

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/obentoo/bentoolkit/internal/autoupdate/registry"
)

// TestCheckPackageStripsUpstreamTagPrefix holds the check-result half: a GitHub
// style "vX.Y.Z" tag is reported and compared as a bare version.
func TestCheckPackageStripsUpstreamTagPrefix(t *testing.T) {
	tmpDir := t.TempDir()
	overlayDir := filepath.Join(tmpDir, "overlay")
	configDir := filepath.Join(tmpDir, "config")

	pkgName := "test-cat/test-pkg"

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]string{"tag_name": "v2.0.0"})
	}))
	defer server.Close()

	createTestEbuild(t, overlayDir, pkgName, "1.0.0")

	config := &registry.PackagesConfig{
		Packages: map[string]registry.PackageConfig{
			pkgName: {URL: server.URL, Parser: "json", Path: "tag_name"},
		},
	}

	checker, err := NewChecker(overlayDir,
		WithConfigDir(configDir),
		WithPackagesConfig(config),
		WithRateLimiter(unlimitedRateLimiter()),
	)
	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}

	result, err := checker.CheckPackage(t.Context(), pkgName, true)
	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}

	if !result.HasUpdate {
		t.Error("Expected HasUpdate to be true for v2.0.0 over 1.0.0")
	}
	if result.UpstreamVersion != "2.0.0" {
		t.Errorf("Expected the upstream version stripped to %q, got %q", "2.0.0", result.UpstreamVersion)
	}
}

// TestCheckPackageStoresStrippedVersionInPending holds the pending half: the
// value written for the applier and the validation plan carries no prefix, so
// a plan built from this entry classifies the bump the run will execute.
func TestCheckPackageStoresStrippedVersionInPending(t *testing.T) {
	tmpDir := t.TempDir()
	overlayDir := filepath.Join(tmpDir, "overlay")
	configDir := filepath.Join(tmpDir, "config")

	pkgName := "test-cat/test-pkg"

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]string{"tag_name": "v1.0.1"})
	}))
	defer server.Close()

	createTestEbuild(t, overlayDir, pkgName, "1.0.0")

	config := &registry.PackagesConfig{
		Packages: map[string]registry.PackageConfig{
			pkgName: {URL: server.URL, Parser: "json", Path: "tag_name"},
		},
	}

	checker, err := NewChecker(overlayDir,
		WithConfigDir(configDir),
		WithPackagesConfig(config),
		WithRateLimiter(unlimitedRateLimiter()),
	)
	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}

	if _, err := checker.CheckPackage(t.Context(), pkgName, true); err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}

	pending, err := NewPendingList(configDir)
	if err != nil {
		t.Fatalf("Unexpected error reading pending list: %v", err)
	}
	update, found := pending.Get(pkgName)
	if !found {
		t.Fatal("Expected a pending entry for the detected update")
	}
	if update.NewVersion != "1.0.1" {
		t.Errorf("Expected pending NewVersion stripped to %q, got %q", "1.0.1", update.NewVersion)
	}
}
