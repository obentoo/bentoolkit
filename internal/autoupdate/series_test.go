package autoupdate

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/obentoo/bentoolkit/internal/autoupdate/ebuilds"
	"github.com/obentoo/bentoolkit/internal/autoupdate/registry"
)

// TestSelectCurrentEbuildSeries is the fix for the zed-bin bug: with a stable
// and a preview ebuild side by side under one SLOT, an unfiltered scan returns
// the preview and the stable line silently stops being updated.
func TestSelectCurrentEbuildSeries(t *testing.T) {
	overlay := t.TempDir()
	pkg := "app-editors/zed-bin"
	for _, v := range []string{"1.13.1", "1.14.1_pre"} {
		createTestEbuild(t, overlay, pkg, v)
	}

	t.Run("no series returns the directory's highest version", func(t *testing.T) {
		got, err := ebuilds.SelectCurrentEbuild(nil, overlay, pkg, "")
		if err != nil {
			t.Fatalf("select: %v", err)
		}
		if got.Version != "1.14.1_pre" {
			t.Fatalf("got %q, want %q", got.Version, "1.14.1_pre")
		}
	})

	t.Run("series pins the stable line", func(t *testing.T) {
		got, err := ebuilds.SelectCurrentEbuild(nil, overlay, pkg, `^1\.13\.`)
		if err != nil {
			t.Fatalf("select: %v", err)
		}
		if got.Version != "1.13.1" {
			t.Fatalf("got %q, want %q", got.Version, "1.13.1")
		}
	})

	t.Run("series pins the preview line", func(t *testing.T) {
		got, err := ebuilds.SelectCurrentEbuild(nil, overlay, pkg, `^1\.14\.`)
		if err != nil {
			t.Fatalf("select: %v", err)
		}
		if got.Version != "1.14.1_pre" {
			t.Fatalf("got %q, want %q", got.Version, "1.14.1_pre")
		}
	})

	t.Run("a series matching nothing is a config error, not an orphan", func(t *testing.T) {
		_, err := ebuilds.SelectCurrentEbuild(nil, overlay, pkg, `^9\.`)
		if !errors.Is(err, ebuilds.ErrSeriesNotFound) {
			t.Fatalf("got %v, want ErrSeriesNotFound", err)
		}
		// Critically NOT ErrNoEbuildFound: that one makes the checker write
		// enabled = false, turning a typo into a package that stops updating.
		if errors.Is(err, ebuilds.ErrNoEbuildFound) {
			t.Fatal("a series typo must not be reported as a removed package")
		}
	})

	t.Run("an uncompilable series does not narrow the scan", func(t *testing.T) {
		got, err := ebuilds.SelectCurrentEbuild(nil, overlay, pkg, `^(1\.13`)
		if err != nil {
			t.Fatalf("select: %v", err)
		}
		if got.Version != "1.14.1_pre" {
			t.Fatalf("got %q, want the unfiltered result %q", got.Version, "1.14.1_pre")
		}
	})
}

// TestSelectCurrentEbuildSeriesIgnoresRevision pins that a series is matched
// against the PV: an exact `^1\.8\.3$` must keep selecting its line after a
// revbump to 1.8.3-r1, and still keep the next line out.
func TestSelectCurrentEbuildSeriesIgnoresRevision(t *testing.T) {
	overlay := t.TempDir()
	pkg := "net-dns/bind-tools"
	for _, v := range []string{"1.8.3-r1", "1.9.0"} {
		createTestEbuild(t, overlay, pkg, v)
	}

	got, err := ebuilds.SelectCurrentEbuild(nil, overlay, pkg, `^1\.8\.3$`)
	if err != nil {
		t.Fatalf("select: %v", err)
	}
	if got.Version != "1.8.3-r1" {
		t.Fatalf("got %q, want %q", got.Version, "1.8.3-r1")
	}
}

// TestSelectVersionSeries pins that upstream selection stays inside the line:
// an index listing both series must not let the stable entry pick the testing
// release.
func TestSelectVersionSeries(t *testing.T) {
	cands := []string{"26.2.4.1", "26.2.5.2", "26.8.0.1"}

	stable := selectVersion(nil, cands, &registry.PackageConfig{Select: "max", Series: `^26\.2\.`})
	if stable != "26.2.5.2" {
		t.Fatalf("stable entry selected %q, want %q", stable, "26.2.5.2")
	}

	testing_ := selectVersion(nil, cands, &registry.PackageConfig{Select: "max", Series: `^26\.8\.`, Suffix: "_pre"})
	if testing_ != "26.8.0.1_pre" {
		t.Fatalf("testing entry selected %q, want %q", testing_, "26.8.0.1_pre")
	}
}

// TestCheckPackageTwoSeries walks both entries of one package end to end: each
// must compare against its own ebuild and bump only its own line.
func TestCheckPackageTwoSeries(t *testing.T) {
	// One index listing both release lines, as the LibreOffice archive does.
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `<a href="26.2.5.2/">x</a><a href="26.2.6.1/">x</a><a href="26.8.0.1/">x</a>`)
	}))
	defer server.Close()

	tmp := t.TempDir()
	overlay := filepath.Join(tmp, "overlay")
	atom := "app-office/libreoffice"
	createTestEbuild(t, overlay, atom, "26.2.5.2")
	createTestEbuild(t, overlay, atom, "26.8.0.1_pre")

	entry := func(series, suffix string) registry.PackageConfig {
		return registry.PackageConfig{
			URL:     server.URL,
			Parser:  "regex",
			Pattern: `href="([0-9]+\.[0-9]+\.[0-9]+\.[0-9]+)/"`,
			Select:  "max",
			Series:  series,
			Suffix:  suffix,
		}
	}
	stableKey, testingKey := atom+"@stable", atom+"@testing"
	cfg := &registry.PackagesConfig{Packages: map[string]registry.PackageConfig{
		stableKey:  entry(`^26\.2\.`, ""),
		testingKey: entry(`^26\.8\.`, "_pre"),
	}}
	if err := cfg.ValidateAll(nil); err != nil {
		t.Fatalf("config rejected: %v", err)
	}

	checker, err := NewChecker(overlay,
		WithConfigDir(filepath.Join(tmp, "config")),
		WithPackagesConfig(cfg),
		WithRateLimiter(unlimitedRateLimiter()),
	)
	if err != nil {
		t.Fatalf("NewChecker: %v", err)
	}

	// The stable entry sees 26.2.5.2 as current and 26.2.6.1 as newest in its
	// line — NOT 26.8.0.1, which is the other entry's business.
	res, err := checker.CheckPackage(t.Context(), stableKey, true)
	if err != nil {
		t.Fatalf("CheckPackage(stable): %v", err)
	}
	if res.Error != nil {
		t.Fatalf("stable entry reported: %v", res.Error)
	}
	if res.CurrentVersion != "26.2.5.2" || res.UpstreamVersion != "26.2.6.1" {
		t.Fatalf("stable entry: current=%q upstream=%q, want 26.2.5.2 / 26.2.6.1",
			res.CurrentVersion, res.UpstreamVersion)
	}
	if !res.HasUpdate {
		t.Fatal("stable entry: expected an update within its own line")
	}

	// The testing entry sees the pre-release ebuild and its own line's version.
	res, err = checker.CheckPackage(t.Context(), testingKey, true)
	if err != nil {
		t.Fatalf("CheckPackage(testing): %v", err)
	}
	if res.Error != nil {
		t.Fatalf("testing entry reported: %v", res.Error)
	}
	if res.CurrentVersion != "26.8.0.1_pre" || res.UpstreamVersion != "26.8.0.1_pre" {
		t.Fatalf("testing entry: current=%q upstream=%q, want both 26.8.0.1_pre",
			res.CurrentVersion, res.UpstreamVersion)
	}
	if res.HasUpdate {
		t.Fatal("testing entry: nothing to bump")
	}
}

// TestFetchUpstreamVersionOutsideSeries pins that a single-value extraction path
// (no select) refuses a version from another line instead of comparing it
// against an ebuild it does not track.
func TestFetchUpstreamVersionOutsideSeries(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"version":"26.8.0.1"}`)
	}))
	defer server.Close()

	tmp := t.TempDir()
	overlay := filepath.Join(tmp, "overlay")
	atom := "app-office/libreoffice"
	createTestEbuild(t, overlay, atom, "26.2.5.2")

	key := atom + "@stable"
	cfg := &registry.PackagesConfig{Packages: map[string]registry.PackageConfig{
		key: {URL: server.URL, Parser: "json", Path: "version", Series: `^26\.2\.`},
	}}

	checker, err := NewChecker(overlay,
		WithConfigDir(filepath.Join(tmp, "config")),
		WithPackagesConfig(cfg),
		WithRateLimiter(unlimitedRateLimiter()),
	)
	if err != nil {
		t.Fatalf("NewChecker: %v", err)
	}

	res, err := checker.CheckPackage(t.Context(), key, true)
	if err == nil && res.Error == nil {
		t.Fatalf("a version outside the series was accepted: %+v", res)
	}
	got := err
	if got == nil {
		got = res.Error
	}
	if !strings.Contains(got.Error(), "outside this entry's series") {
		t.Fatalf("unexpected error: %v", got)
	}
}

// TestCleanPackageDirLeavesOtherSeries pins that --clean prunes only the line it
// bumped: the parallel line's ebuild must survive.
//
// The guarantee is unchanged from when this test covered the by-name removal
// this replaced; what changed is what upholds it. Removal used to be by exact
// filename, built from the series-filtered current version, so the other line
// was spared by never being looked at. cleanPackageDir sweeps the directory, so
// the other line
// survives because its OWN registry entry claims it — which is how the overlay
// really describes a two-line package, one entry per line. The fixture gains
// that second entry for exactly that reason; without it the dev line is claimed
// by nobody and R4.1 removes it.
func TestCleanPackageDirLeavesOtherSeries(t *testing.T) {
	tmp := t.TempDir()
	overlay := filepath.Join(tmp, "overlay")
	atom := "app-office/libreoffice"
	createTestEbuild(t, overlay, atom, "26.2.5.2")
	createTestEbuild(t, overlay, atom, "26.2.6.1")
	createTestEbuild(t, overlay, atom, "26.8.0.1_pre")

	key := atom + "@stable"
	applier, err := NewApplier(overlay, filepath.Join(tmp, "config"),
		WithApplierPackagesConfig(&registry.PackagesConfig{Packages: map[string]registry.PackageConfig{
			key:           {URL: "https://example.com", Parser: "json", Path: "v", Series: `^26\.2\.`},
			atom + "@dev": {URL: "https://example.com", Parser: "json", Path: "v", Series: `^26\.8\.`, Version: "26.8.0.1_pre"},
		}}),
		WithExecCommand(mockExecCommandSuccess),
	)
	if err != nil {
		t.Fatalf("NewApplier: %v", err)
	}

	// The stable entry's current version must be the stable line's highest.
	current, err := applier.resolveCurrentVersion(key)
	if err != nil {
		t.Fatalf("resolveCurrentVersion: %v", err)
	}
	if current != "26.2.6.1" {
		t.Fatalf("resolveCurrentVersion = %q, want %q", current, "26.2.6.1")
	}

	if _, err := applier.cleanPackageDir(t.Context(), key, "26.2.6.1"); err != nil {
		t.Fatalf("cleanPackageDir: %v", err)
	}

	pkgDir := filepath.Join(overlay, "app-office", "libreoffice")
	for _, f := range []string{"libreoffice-26.2.6.1.ebuild", "libreoffice-26.8.0.1_pre.ebuild"} {
		if _, err := os.Stat(filepath.Join(pkgDir, f)); err != nil {
			t.Fatalf("%s must survive the clean: %v", f, err)
		}
	}
	if _, err := os.Stat(filepath.Join(pkgDir, "libreoffice-26.2.5.2.ebuild")); !os.IsNotExist(err) {
		t.Fatalf("the superseded ebuild was not removed: %v", err)
	}
}
