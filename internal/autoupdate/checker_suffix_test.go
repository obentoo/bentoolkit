package autoupdate

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
)

// libreofficeIndex mimics the archive directory listing the LibreOffice record
// probes: the stable 26.2 line and the testing 26.8 one, in one page.
const libreofficeIndex = `<html><body>
<a href="26.2.4.1/">26.2.4.1/</a>
<a href="26.2.5.2/">26.2.5.2/</a>
<a href="26.8.0.1/">26.8.0.1/</a>
</body></html>`

// TestCheckPackageAppliesSuffix walks the whole check for a record that declares
// a pre-release channel, which is the case that produced the bug: the testing
// line's version is numbered like a final release, so without the suffix the
// checker reported a plain 26.8.0.1 and the bump silently dropped the _pre the
// ebuild carried.
func TestCheckPackageAppliesSuffix(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, libreofficeIndex)
	}))
	defer server.Close()

	tmp := t.TempDir()
	overlayDir := filepath.Join(tmp, "overlay")
	pkg := "app-office/libreoffice"
	createTestEbuild(t, overlayDir, pkg, "26.8.0.1_pre")

	cfg := &PackagesConfig{Packages: map[string]PackageConfig{
		pkg: {
			URL:        server.URL,
			Parser:     "regex",
			Pattern:    `href="([0-9]+\.[0-9]+\.[0-9]+\.[0-9]+)/"`,
			Select:     "max",
			Suffix:     "_pre",
			SuffixWhen: `^26\.8\.`,
		},
	}}

	checker, err := NewChecker(overlayDir,
		WithConfigDir(filepath.Join(tmp, "config")),
		WithPackagesConfig(cfg),
		WithRateLimiter(unlimitedRateLimiter()),
	)
	if err != nil {
		t.Fatalf("NewChecker: %v", err)
	}

	res, err := checker.CheckPackage(pkg, true)
	if err != nil {
		t.Fatalf("CheckPackage: %v", err)
	}
	if res.Error != nil {
		t.Fatalf("check reported: %v", res.Error)
	}
	if res.UpstreamVersion != "26.8.0.1_pre" {
		t.Fatalf("UpstreamVersion = %q, want %q", res.UpstreamVersion, "26.8.0.1_pre")
	}
	// The overlay already carries that pre-release, so there is nothing to bump —
	// the pre-fix behavior reported an update from 26.8.0.1_pre to 26.8.0.1.
	if res.HasUpdate {
		t.Fatalf("HasUpdate = true; %q must not count as an update over %q",
			res.UpstreamVersion, res.CurrentVersion)
	}
}

// TestCheckPackageSuffixFiresOnPromotion is the other half: once upstream
// promotes the line, the record stops marking it and the bump fires.
func TestCheckPackageSuffixFiresOnPromotion(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `<a href="26.8.0.4/">26.8.0.4/</a>`)
	}))
	defer server.Close()

	tmp := t.TempDir()
	overlayDir := filepath.Join(tmp, "overlay")
	pkg := "app-office/libreoffice"
	createTestEbuild(t, overlayDir, pkg, "26.8.0.1_pre")

	cfg := &PackagesConfig{Packages: map[string]PackageConfig{
		pkg: {
			URL:     server.URL,
			Parser:  "regex",
			Pattern: `href="([0-9]+\.[0-9]+\.[0-9]+\.[0-9]+)/"`,
			Select:  "max",
			// suffix_when no longer matches the promoted line.
			Suffix:     "_pre",
			SuffixWhen: `^26\.9\.`,
		},
	}}

	checker, err := NewChecker(overlayDir,
		WithConfigDir(filepath.Join(tmp, "config")),
		WithPackagesConfig(cfg),
		WithRateLimiter(unlimitedRateLimiter()),
	)
	if err != nil {
		t.Fatalf("NewChecker: %v", err)
	}

	res, err := checker.CheckPackage(pkg, true)
	if err != nil {
		t.Fatalf("CheckPackage: %v", err)
	}
	if res.UpstreamVersion != "26.8.0.4" {
		t.Fatalf("UpstreamVersion = %q, want %q", res.UpstreamVersion, "26.8.0.4")
	}
	if !res.HasUpdate {
		t.Fatalf("HasUpdate = false; %q must outrank %q", res.UpstreamVersion, res.CurrentVersion)
	}
}
