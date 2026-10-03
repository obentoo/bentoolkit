package autoupdate

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
)

// TestCheckPackageSkipsHeldAndDisabled pins that an explicit `--check <pkg>`
// honours the two keys CheckAll filters on: no fetch, no cache, no pending entry.
func TestCheckPackageSkipsHeldAndDisabled(t *testing.T) {
	disabled := false
	for _, tc := range []struct {
		name       string
		cfg        func(url string) PackageConfig
		wantReason string
	}{
		{"hold", func(url string) PackageConfig {
			return PackageConfig{Hold: true, URL: url, Parser: "json", Path: "tag_name"}
		}, "hold = true"},
		{"disabled", func(url string) PackageConfig {
			return PackageConfig{Enabled: &disabled, URL: url, Parser: "json", Path: "tag_name"}
		}, "enabled = false"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var hits atomic.Int32
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				hits.Add(1)
				_, _ = w.Write([]byte(`{"tag_name":"2.0.0"}`))
			}))
			t.Cleanup(srv.Close)

			overlayDir, configDir := t.TempDir(), t.TempDir()
			pkg := "test-cat/refused-pkg"
			createTestEbuild(t, overlayDir, pkg, "1.0.0")
			c := holdChecker(t, overlayDir, configDir, pkg, tc.cfg(srv.URL))

			result, err := c.CheckPackage(pkg, true)
			if err != nil {
				t.Fatalf("CheckPackage: %v", err)
			}
			if result.Skipped != tc.wantReason {
				t.Errorf("Skipped = %q, want %q", result.Skipped, tc.wantReason)
			}
			if result.HasUpdate || result.UpstreamVersion != "" {
				t.Errorf("a skipped package reported a version: %+v", result)
			}
			if n := hits.Load(); n != 0 {
				t.Errorf("upstream was fetched %d time(s) for a skipped package", n)
			}
			if got, ok := c.pending.Get(pkg); ok {
				t.Errorf("a skipped package was queued: %+v", *got)
			}
		})
	}
}

// TestApplyRefusesDisabledPackage is TestApplyRefusesHeldPackage for
// enabled = false: an update already in pending.json must not be applied.
func TestApplyRefusesDisabledPackage(t *testing.T) {
	tmpDir := t.TempDir()
	overlayDir := filepath.Join(tmpDir, "overlay")
	configDir := filepath.Join(tmpDir, "config")

	pkg := "test-cat/disabled-pkg"
	createTestEbuildFile(t, overlayDir, pkg, "1.0.0")

	pending, _ := NewPendingList(configDir)
	pending.Add(PendingUpdate{
		Package:        pkg,
		CurrentVersion: "1.0.0",
		NewVersion:     "1.1.0",
		Status:         StatusPending,
	})

	disabled := false
	cfg := &PackagesConfig{Packages: map[string]PackageConfig{
		pkg: {Enabled: &disabled, URL: "https://example.invalid/", Parser: "json", Path: "tag_name"},
	}}

	applier, err := NewApplier(overlayDir, configDir,
		WithApplierPendingList(pending),
		WithApplierPackagesConfig(cfg),
	)
	if err != nil {
		t.Fatalf("NewApplier: %v", err)
	}

	result, err := applier.Apply(pkg, false)
	if err != nil {
		t.Errorf("Apply: %v", err)
	}
	if !result.Held || result.HoldReason != "enabled = false" {
		t.Errorf("Held = %v, HoldReason = %q; want true, %q", result.Held, result.HoldReason, "enabled = false")
	}
	if _, found := pending.Get(pkg); !found {
		t.Error("the refused pending entry was pruned; it must be kept")
	}
	if _, err := os.Stat(filepath.Join(overlayDir, pkg, "disabled-pkg-1.1.0.ebuild")); !os.IsNotExist(err) {
		t.Error("an ebuild was written for a disabled package")
	}
}
