package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/obentoo/bentoolkit/internal/autoupdate"
	"github.com/obentoo/bentoolkit/internal/common/config"
	"github.com/obentoo/bentoolkit/internal/common/provider"
)

// fakeReviveProvider is a binary-free stand-in for the ::gentoo provider used by
// the revive flow. It implements BOTH provider.Provider and
// provider.PackageDirProvider so reviveOne can type-assert to the dir provider
// without a real git clone. versions is keyed by "category/pkg"; dir is the
// on-disk source package directory SeedFromGentoo copies from; localErr forces a
// LocalPackagePath failure for the error-path tests.
type fakeReviveProvider struct {
	versions map[string][]string
	dir      string
	localErr error
}

func (f *fakeReviveProvider) GetPackageVersions(category, pkg string) ([]string, error) {
	if vs, ok := f.versions[category+"/"+pkg]; ok {
		return vs, nil
	}
	return nil, provider.ErrNotFound
}

func (f *fakeReviveProvider) LocalPackagePath(category, pkg string) (string, error) {
	if f.localErr != nil {
		return "", f.localErr
	}
	return f.dir, nil
}

func (f *fakeReviveProvider) GetName() string   { return "fake" }
func (f *fakeReviveProvider) SupportsAPI() bool { return true }
func (f *fakeReviveProvider) Close() error      { return nil }

// pinReviveConcurrency pins autoupdateConcurrency to a valid value for the
// duration of the test. reviveCheckerOptions feeds it into WithConcurrency,
// which rejects values outside [1, 100].
func pinReviveConcurrency(t *testing.T) {
	t.Helper()
	orig := autoupdateConcurrency
	autoupdateConcurrency = autoupdate.DefaultConcurrency
	t.Cleanup(func() { autoupdateConcurrency = orig })
}

// TestSplitPackage covers the "category/package" split: exactly two non-empty
// segments succeed; anything else fails.
func TestSplitPackage(t *testing.T) {
	tests := []struct {
		name     string
		pkg      string
		wantCat  string
		wantName string
		wantOK   bool
	}{
		{name: "valid", pkg: "cat/pkg", wantCat: "cat", wantName: "pkg", wantOK: true},
		{name: "no slash", pkg: "noslash", wantOK: false},
		{name: "empty name", pkg: "a/", wantOK: false},
		{name: "empty category", pkg: "/b", wantOK: false},
		{name: "three segments", pkg: "a/b/c", wantOK: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cat, name, ok := splitPackage(tt.pkg)
			if ok != tt.wantOK {
				t.Fatalf("splitPackage(%q) ok = %v, want %v", tt.pkg, ok, tt.wantOK)
			}
			if ok {
				if cat != tt.wantCat || name != tt.wantName {
					t.Errorf("splitPackage(%q) = (%q, %q), want (%q, %q)",
						tt.pkg, cat, name, tt.wantCat, tt.wantName)
				}
			}
		})
	}
}

// TestHighestVersion verifies highestVersion picks the highest valid Gentoo
// version, skips invalid entries, and returns "" when none are comparable.
func TestHighestVersion(t *testing.T) {
	tests := []struct {
		name     string
		versions []string
		want     string
	}{
		{name: "empty slice", versions: nil, want: ""},
		{name: "single valid", versions: []string{"1.2.3"}, want: "1.2.3"},
		{name: "picks highest", versions: []string{"1.0.0", "2.5.1", "1.9.9"}, want: "2.5.1"},
		{name: "skips invalid", versions: []string{"not-a-version", "1.4.0", "???"}, want: "1.4.0"},
		{name: "all invalid", versions: []string{"abc", "x.y.z"}, want: ""},
		{name: "trims whitespace", versions: []string{"  1.1.0  ", "1.0.0"}, want: "1.1.0"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := highestVersion(tt.versions); got != tt.want {
				t.Errorf("highestVersion(%v) = %q, want %q", tt.versions, got, tt.want)
			}
		})
	}
}

// TestReviveCheckerOptions asserts the shared option builder returns a non-empty
// option set for a zero LLM config (no provider configured).
func TestReviveCheckerOptions(t *testing.T) {
	pinReviveConcurrency(t)

	opts := reviveCheckerOptions(context.Background(), t.TempDir(), 0, config.LLMConfig{}, "")
	if len(opts) == 0 {
		t.Fatal("reviveCheckerOptions returned an empty option set")
	}

	// A positive cacheTTL appends WithCacheTTL, so the set must be at least as
	// large as the TTL-less one.
	withTTL := reviveCheckerOptions(context.Background(), t.TempDir(), 1, config.LLMConfig{}, "")
	if len(withTTL) < len(opts) {
		t.Errorf("reviveCheckerOptions with cacheTTL produced fewer options (%d) than without (%d)",
			len(withTTL), len(opts))
	}
}

// TestDisplayReviveCandidates exercises both branches (non-empty table and the
// empty "nothing to revive" note) and asserts neither panics.
func TestDisplayReviveCandidates(t *testing.T) {
	// Non-empty: a full table render.
	displayReviveCandidates([]autoupdate.ReviveCandidate{
		{Package: "dev-test/foo", GentooVersion: "1.2.3", UpstreamVersion: "1.3.0"},
		{Package: "net-misc/bar", GentooVersion: "0.9.0", UpstreamVersion: "1.0.0"},
	})

	// Empty: the "nothing to revive" branch.
	displayReviveCandidates(nil)
}

// TestDisplayReviveSummary asserts the summary returns the count of "failed"
// outcomes and tolerates every status branch.
func TestDisplayReviveSummary(t *testing.T) {
	outcomes := []reviveOutcome{
		{pkg: "a/one", status: "revived", detail: "1.0 → 2.0"},
		{pkg: "a/two", status: "skipped", detail: "already current"},
		{pkg: "a/three", status: "failed", detail: "boom"},
		{pkg: "a/four", status: "failed", detail: "kaboom"},
	}

	if got := displayReviveSummary(outcomes); got != 2 {
		t.Errorf("displayReviveSummary failure count = %d, want 2", got)
	}

	// No outcomes → zero failures, no panic.
	if got := displayReviveSummary(nil); got != 0 {
		t.Errorf("displayReviveSummary(nil) = %d, want 0", got)
	}
}

// writeReviveRegexConfig writes a packages.toml under <overlay>/.autoupdate with a
// single DISABLED regex-parser entry pointing at serverURL. The package is
// disabled to mirror the orphan revive flow (reviveOne re-enables it before
// checking).
func writeReviveRegexConfig(t *testing.T, overlay, pkg, serverURL string) {
	t.Helper()
	cfgDir := filepath.Join(overlay, ".autoupdate")
	if err := os.MkdirAll(cfgDir, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", cfgDir, err)
	}
	content := "[\"" + pkg + "\"]\n" +
		"enabled = false\n" +
		"url = \"" + serverURL + "\"\n" +
		"parser = \"regex\"\n" +
		"pattern = 'version ([0-9.]+)'\n"
	if err := os.WriteFile(filepath.Join(cfgDir, "packages.toml"), []byte(content), 0o644); err != nil {
		t.Fatalf("write packages.toml: %v", err)
	}
}

// writeReviveSrcEbuild writes a minimal EAPI=8 ebuild named "<name>-<ver>.ebuild"
// into srcDir, simulating the ::gentoo package directory SeedFromGentoo copies
// from.
func writeReviveSrcEbuild(t *testing.T, srcDir, name, ver string) {
	t.Helper()
	if err := os.MkdirAll(srcDir, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", srcDir, err)
	}
	content := "EAPI=8\nDESCRIPTION=\"t\"\nHOMEPAGE=\"https://example.com\"\nSLOT=\"0\"\nKEYWORDS=\"~amd64\"\n"
	path := filepath.Join(srcDir, name+"-"+ver+".ebuild")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write src ebuild %s: %v", path, err)
	}
}

// newReviveApplier builds an Applier sharing the given pending list, matching the
// wiring runRevive uses. configDir is the autoupdate config dir.
func newReviveApplier(t *testing.T, overlay, configDir string, pending *autoupdate.PendingList) *autoupdate.Applier {
	t.Helper()
	applier, err := autoupdate.NewApplier(overlay, configDir,
		autoupdate.WithApplierPendingList(pending),
	)
	if err != nil {
		t.Fatalf("NewApplier: %v", err)
	}
	return applier
}

// TestReviveOne covers reviveOne's non-pkgdev paths: the !HasUpdate "skipped"
// branch (a real Checker + CheckPackage against an httptest server) and the four
// early "failed" branches. None of these reaches applier.Apply / `pkgdev
// manifest`.
func TestReviveOne(t *testing.T) {
	pinReviveConcurrency(t)

	// Pin the global flags reviveOne reads transitively. autoupdateOnly feeds
	// WithTypeFilter; autoupdateCompile/Clean only matter past Apply but are kept
	// deterministic.
	origOnly, origCompile, origClean := autoupdateOnly, autoupdateCompile, autoupdateClean
	autoupdateOnly, autoupdateCompile, autoupdateClean = "", false, false
	t.Cleanup(func() {
		autoupdateOnly, autoupdateCompile, autoupdateClean = origOnly, origCompile, origClean
	})

	t.Run("skipped when gentoo equals upstream", func(t *testing.T) {
		const ver = "1.2.3"

		// Upstream server: regex 'version ([0-9.]+)' matches this body at V.
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "text/plain")
			_, _ = w.Write([]byte("version " + ver + "\n"))
		}))
		defer server.Close()

		overlay := setupTestOverlay(t)
		configDir := t.TempDir()
		writeReviveRegexConfig(t, overlay, "dev-test/foo", server.URL)

		// ::gentoo source package dir containing the base ebuild to seed.
		srcDir := filepath.Join(t.TempDir(), "src")
		writeReviveSrcEbuild(t, srcDir, "foo", ver)

		fake := &fakeReviveProvider{
			versions: map[string][]string{"dev-test/foo": {ver}},
			dir:      srcDir,
		}

		pending, err := autoupdate.NewPendingList(configDir)
		if err != nil {
			t.Fatalf("NewPendingList: %v", err)
		}
		applier := newReviveApplier(t, overlay, configDir, pending)

		out := reviveOne(context.Background(), "dev-test/foo", overlay, configDir, 0,
			config.LLMConfig{}, "", fake, fake, applier, pending)

		if out.status != "skipped" {
			t.Fatalf("reviveOne status = %q (detail: %s), want \"skipped\"", out.status, out.detail)
		}
	})

	t.Run("bad package name fails", func(t *testing.T) {
		overlay := setupTestOverlay(t)
		configDir := t.TempDir()
		fake := &fakeReviveProvider{}
		pending, err := autoupdate.NewPendingList(configDir)
		if err != nil {
			t.Fatalf("NewPendingList: %v", err)
		}
		applier := newReviveApplier(t, overlay, configDir, pending)

		out := reviveOne(context.Background(), "noslash", overlay, configDir, 0,
			config.LLMConfig{}, "", fake, fake, applier, pending)

		if out.status != "failed" {
			t.Errorf("reviveOne(%q) status = %q, want \"failed\"", "noslash", out.status)
		}
	})

	t.Run("LocalPackagePath error fails", func(t *testing.T) {
		overlay := setupTestOverlay(t)
		configDir := t.TempDir()
		fake := &fakeReviveProvider{localErr: provider.ErrNotFound}
		pending, err := autoupdate.NewPendingList(configDir)
		if err != nil {
			t.Fatalf("NewPendingList: %v", err)
		}
		applier := newReviveApplier(t, overlay, configDir, pending)

		out := reviveOne(context.Background(), "dev-test/foo", overlay, configDir, 0,
			config.LLMConfig{}, "", fake, fake, applier, pending)

		if out.status != "failed" {
			t.Errorf("reviveOne with LocalPackagePath error status = %q, want \"failed\"", out.status)
		}
	})

	t.Run("missing gentoo versions fails", func(t *testing.T) {
		overlay := setupTestOverlay(t)
		configDir := t.TempDir()
		// dir set so LocalPackagePath succeeds, but versions map omits the package
		// so GetPackageVersions returns ErrNotFound.
		fake := &fakeReviveProvider{dir: t.TempDir()}
		pending, err := autoupdate.NewPendingList(configDir)
		if err != nil {
			t.Fatalf("NewPendingList: %v", err)
		}
		applier := newReviveApplier(t, overlay, configDir, pending)

		out := reviveOne(context.Background(), "dev-test/foo", overlay, configDir, 0,
			config.LLMConfig{}, "", fake, fake, applier, pending)

		if out.status != "failed" {
			t.Errorf("reviveOne with no gentoo versions status = %q, want \"failed\"", out.status)
		}
	})

	t.Run("seed from gentoo fails when base ebuild absent", func(t *testing.T) {
		overlay := setupTestOverlay(t)
		configDir := t.TempDir()
		// dir set + a version present, but the dir has NO "foo-1.2.3.ebuild", so
		// SeedFromGentoo fails with ErrEbuildNotFound.
		srcDir := filepath.Join(t.TempDir(), "src")
		if err := os.MkdirAll(srcDir, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", srcDir, err)
		}
		fake := &fakeReviveProvider{
			versions: map[string][]string{"dev-test/foo": {"1.2.3"}},
			dir:      srcDir,
		}
		pending, err := autoupdate.NewPendingList(configDir)
		if err != nil {
			t.Fatalf("NewPendingList: %v", err)
		}
		applier := newReviveApplier(t, overlay, configDir, pending)

		out := reviveOne(context.Background(), "dev-test/foo", overlay, configDir, 0,
			config.LLMConfig{}, "", fake, fake, applier, pending)

		if out.status != "failed" {
			t.Errorf("reviveOne with missing base ebuild status = %q, want \"failed\"", out.status)
		}
	})

	t.Run("check failed when upstream unparseable", func(t *testing.T) {
		const ver = "1.2.3"

		// Upstream server returns a body that does NOT match the regex pattern, so
		// the FRESH checker reviveOne builds errors in CheckPackage (force=true).
		// This reaches reviveOne past SeedFromGentoo + EnablePackagesInConfig and
		// exercises the "check failed" branch WITHOUT touching applier.Apply.
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "text/plain")
			_, _ = w.Write([]byte("no version string here\n"))
		}))
		defer server.Close()

		overlay := setupTestOverlay(t)
		configDir := t.TempDir()
		writeReviveRegexConfig(t, overlay, "dev-test/foo", server.URL)

		// ::gentoo source dir with the base ebuild so SeedFromGentoo succeeds.
		srcDir := filepath.Join(t.TempDir(), "src")
		writeReviveSrcEbuild(t, srcDir, "foo", ver)

		fake := &fakeReviveProvider{
			versions: map[string][]string{"dev-test/foo": {ver}},
			dir:      srcDir,
		}

		pending, err := autoupdate.NewPendingList(configDir)
		if err != nil {
			t.Fatalf("NewPendingList: %v", err)
		}
		applier := newReviveApplier(t, overlay, configDir, pending)

		out := reviveOne(context.Background(), "dev-test/foo", overlay, configDir, 0,
			config.LLMConfig{}, "", fake, fake, applier, pending)

		if out.status != "failed" {
			t.Fatalf("reviveOne status = %q (detail: %s), want \"failed\"", out.status, out.detail)
		}
	})
}

// configWithGentooGitHub returns a *config.Config whose "gentoo" repository is an
// API-only GitHub provider. resolveGentooProvider constructs it WITHOUT any git
// clone or network call, and the resulting GitHubProvider deliberately does NOT
// implement provider.PackageDirProvider — so runRevive hits its up-front guard.
func configWithGentooGitHub() *config.Config {
	return &config.Config{
		Repositories: map[string]*config.RepoConfig{
			"gentoo": {
				Provider: "github",
				URL:      "gentoo/gentoo",
				Branch:   "master",
			},
		},
	}
}

// TestResolveGentooProvider_SuccessAPIOnly drives resolveGentooProvider's full
// success path via a config-supplied GitHub repo (ResolveRepository returns the
// config entry, so the network registry is never consulted). It asserts a
// non-nil provider that, being API-only, is NOT a provider.PackageDirProvider.
func TestResolveGentooProvider_SuccessAPIOnly(t *testing.T) {
	_, cleanup := setupTestHome(t)
	defer cleanup()

	cfg := configWithGentooGitHub()

	var prov provider.Provider
	code := withExitIntercept(func() { prov = resolveGentooProvider(cfg, "") })
	if code != -1 {
		t.Fatalf("resolveGentooProvider exited with code %d, want no exit", code)
	}
	if prov == nil {
		t.Fatal("resolveGentooProvider returned nil for a valid config gentoo repo")
	}
	defer prov.Close() //nolint:errcheck

	if _, ok := prov.(provider.PackageDirProvider); ok {
		t.Error("API-only GitHub provider unexpectedly implements PackageDirProvider")
	}
}

// TestRunRevive_NoPackageDirProvider covers runRevive's up-front guard: the
// resolved ::gentoo provider (API-only GitHub) cannot expose an on-disk package
// directory, so runRevive logs guidance and exits 1 BEFORE any checker, seed, or
// applier.Apply work. Fully binary-free (no clone, no pkgdev).
func TestRunRevive_NoPackageDirProvider(t *testing.T) {
	pinReviveConcurrency(t)
	_, cleanup := setupTestHome(t)
	defer cleanup()

	overlay := setupTestOverlay(t)
	configDir := t.TempDir()
	cfg := configWithGentooGitHub()

	code := withExitIntercept(func() {
		runRevive(context.Background(), overlay, configDir, "dev-test/foo", 0, cfg, config.LLMConfig{}, "")
	})
	if code != 1 {
		t.Fatalf("runRevive exit code = %d, want 1 (no PackageDirProvider guard)", code)
	}
}

// TestRunReviveList_NoCandidates covers runReviveList end-to-end with an overlay
// that has NO disabled packages.toml entries: FindRevivableOrphans returns an
// empty set without consulting the provider's network, and
// displayReviveCandidates prints the "nothing to revive" note. No exit, no
// binary. resolveGentooProvider's success path is exercised here too.
func TestRunReviveList_NoCandidates(t *testing.T) {
	pinReviveConcurrency(t)
	_, cleanup := setupTestHome(t)
	defer cleanup()

	overlay := setupTestOverlay(t)
	configDir := t.TempDir()
	// An empty .autoupdate/packages.toml: no disabled entries -> no candidates,
	// so the provider is never queried over the network.
	cfgDir := filepath.Join(overlay, ".autoupdate")
	if err := os.MkdirAll(cfgDir, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", cfgDir, err)
	}
	if err := os.WriteFile(filepath.Join(cfgDir, "packages.toml"), []byte(""), 0o644); err != nil {
		t.Fatalf("write packages.toml: %v", err)
	}
	cfg := configWithGentooGitHub()

	code := withExitIntercept(func() {
		runReviveList(context.Background(), overlay, configDir, 0, cfg, config.LLMConfig{}, "")
	})
	if code != -1 {
		t.Fatalf("runReviveList exited with code %d, want no exit", code)
	}
}
