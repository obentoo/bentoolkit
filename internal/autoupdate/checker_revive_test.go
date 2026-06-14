package autoupdate

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/obentoo/bentoolkit/internal/common/provider"
)

// fakeProvider is a minimal provider.Provider for the revive report tests. It
// returns a fixed gentoo version per package, or provider.ErrNotFound for
// packages absent from the map, so a test can model "::gentoo carries X" and
// "::gentoo does not carry this package" without any network/git access.
type fakeProvider struct {
	// versions maps "category/pkg" -> the gentoo versions to report.
	versions map[string][]string
}

func (f *fakeProvider) GetPackageVersions(category, pkg string) ([]string, error) {
	key := category + "/" + pkg
	v, ok := f.versions[key]
	if !ok {
		return nil, provider.ErrNotFound
	}
	return v, nil
}

func (f *fakeProvider) GetName() string   { return "fake" }
func (f *fakeProvider) SupportsAPI() bool { return true }
func (f *fakeProvider) Close() error      { return nil }

// boolPtr returns a pointer to b, for setting PackageConfig.Enabled explicitly.
func boolPtr(b bool) *bool { return &b }

// newReviveChecker builds a Checker over the given packages config wired with a
// no-op rate limiter and isolated temp dirs, mirroring the cursor/buildid test
// setup. The overlay dir is returned so callers can drop ebuilds if needed.
func newReviveChecker(t *testing.T, pkgs map[string]PackageConfig) *Checker {
	t.Helper()
	overlayDir := t.TempDir()
	configDir := t.TempDir()
	checker, err := NewChecker(overlayDir,
		WithConfigDir(configDir),
		WithPackagesConfig(&PackagesConfig{Packages: pkgs}),
		WithRateLimiter(unlimitedRateLimiter()),
	)
	if err != nil {
		t.Fatalf("NewChecker: %v", err)
	}
	return checker
}

// jsonVersionServer serves a single {"version":"<v>"} body so fetchUpstreamVersion
// (parser=json, path=version) resolves to v.
func jsonVersionServer(t *testing.T, version string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"version":"` + version + `"}`))
	}))
	t.Cleanup(srv.Close)
	return srv
}

// TestFindRevivableOrphans_UpstreamNewer covers case (a): a disabled package
// whose upstream (2.0.0) is strictly newer than the highest gentoo version
// (1.5.0) is reported as a revive candidate with both versions populated.
func TestFindRevivableOrphans_UpstreamNewer(t *testing.T) {
	pkg := "app-editors/orphan"
	srv := jsonVersionServer(t, "2.0.0")

	checker := newReviveChecker(t, map[string]PackageConfig{
		pkg: {Parser: "json", Path: "version", URL: srv.URL, Enabled: boolPtr(false)},
	})
	prov := &fakeProvider{versions: map[string][]string{
		pkg: {"1.4.0", "1.5.0"},
	}}

	got, err := checker.FindRevivableOrphans(prov)
	if err != nil {
		t.Fatalf("FindRevivableOrphans: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("expected 1 candidate, got %d: %+v", len(got), got)
	}
	c := got[0]
	if c.Package != pkg {
		t.Errorf("Package = %q, want %q", c.Package, pkg)
	}
	if c.GentooVersion != "1.5.0" {
		t.Errorf("GentooVersion = %q, want 1.5.0", c.GentooVersion)
	}
	if c.UpstreamVersion != "2.0.0" {
		t.Errorf("UpstreamVersion = %q, want 2.0.0", c.UpstreamVersion)
	}
}

// TestFindRevivableOrphans_PresentSkipped guards the smoke-test finding: a
// disabled entry whose ebuild is STILL PRESENT in the overlay (e.g. a manually
// disabled package) must NOT be reported, even when upstream is newer than
// ::gentoo. Reviving it would seed an older ::gentoo base over the newer overlay
// ebuild. Only genuinely-removed (ErrNoEbuildFound) packages are revivable.
func TestFindRevivableOrphans_PresentSkipped(t *testing.T) {
	pkg := "dev-util/present"
	srv := jsonVersionServer(t, "2.0.0") // upstream newer than both gentoo and overlay

	overlayDir := t.TempDir()
	checker, err := NewChecker(overlayDir,
		WithConfigDir(t.TempDir()),
		WithPackagesConfig(&PackagesConfig{Packages: map[string]PackageConfig{
			pkg: {Parser: "json", Path: "version", URL: srv.URL, Enabled: boolPtr(false)},
		}}),
		WithRateLimiter(unlimitedRateLimiter()),
	)
	if err != nil {
		t.Fatalf("NewChecker: %v", err)
	}

	// Disabled, but the ebuild is still in the overlay (and ahead of gentoo).
	createTestEbuild(t, overlayDir, pkg, "1.6.0")
	prov := &fakeProvider{versions: map[string][]string{pkg: {"1.5.0"}}}

	got, err := checker.FindRevivableOrphans(prov)
	if err != nil {
		t.Fatalf("FindRevivableOrphans: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("expected 0 candidates (disabled but present), got %d: %+v", len(got), got)
	}
}

// TestFindRevivableOrphans_UpstreamNotNewer covers case (b): a disabled package
// whose upstream (1.5.0) equals the highest gentoo version is NOT a candidate
// (upstream must be strictly newer).
func TestFindRevivableOrphans_UpstreamNotNewer(t *testing.T) {
	pkg := "app-editors/orphan"
	srv := jsonVersionServer(t, "1.5.0")

	checker := newReviveChecker(t, map[string]PackageConfig{
		pkg: {Parser: "json", Path: "version", URL: srv.URL, Enabled: boolPtr(false)},
	})
	prov := &fakeProvider{versions: map[string][]string{
		pkg: {"1.5.0"},
	}}

	got, err := checker.FindRevivableOrphans(prov)
	if err != nil {
		t.Fatalf("FindRevivableOrphans: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("expected 0 candidates, got %d: %+v", len(got), got)
	}
}

// TestFindRevivableOrphans_EnabledSkipped covers case (c): an ENABLED package is
// never considered, even when its upstream is newer than gentoo.
func TestFindRevivableOrphans_EnabledSkipped(t *testing.T) {
	pkg := "app-editors/enabled"
	// Server would 500 if hit; an enabled entry must be skipped before any fetch.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "should not be fetched", http.StatusInternalServerError)
	}))
	t.Cleanup(srv.Close)

	checker := newReviveChecker(t, map[string]PackageConfig{
		pkg: {Parser: "json", Path: "version", URL: srv.URL, Enabled: boolPtr(true)},
	})
	prov := &fakeProvider{versions: map[string][]string{
		pkg: {"1.0.0"},
	}}

	got, err := checker.FindRevivableOrphans(prov)
	if err != nil {
		t.Fatalf("FindRevivableOrphans: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("expected 0 candidates for enabled package, got %d: %+v", len(got), got)
	}
}

// TestFindRevivableOrphans_NotInGentoo covers case (d): a disabled package the
// provider reports ErrNotFound for is skipped silently (no candidate, no error).
func TestFindRevivableOrphans_NotInGentoo(t *testing.T) {
	pkg := "app-editors/gone"
	srv := jsonVersionServer(t, "9.9.9")

	checker := newReviveChecker(t, map[string]PackageConfig{
		pkg: {Parser: "json", Path: "version", URL: srv.URL, Enabled: boolPtr(false)},
	})
	// Empty version map => provider returns ErrNotFound for every package.
	prov := &fakeProvider{versions: map[string][]string{}}

	got, err := checker.FindRevivableOrphans(prov)
	if err != nil {
		t.Fatalf("FindRevivableOrphans: unexpected error for ErrNotFound: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("expected 0 candidates, got %d: %+v", len(got), got)
	}
}
