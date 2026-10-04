package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/obentoo/bentoolkit/internal/autoupdate"
	"github.com/obentoo/bentoolkit/internal/common/config"
	"github.com/obentoo/bentoolkit/internal/common/provider"
)

// fakeReviveProvider is a binary-free stand-in for the ::gentoo provider used by
// the revive flow. It implements BOTH provider.Provider and
// provider.PackageDirProvider, so autoupdate.CanRevive accepts it without a
// real git clone. versions is keyed by "category/pkg"; dir is the on-disk source
// package directory SeedFromGentoo copies from.
type fakeReviveProvider struct {
	versions map[string][]string
	dir      string
}

func (f *fakeReviveProvider) GetPackageVersions(_ context.Context, category, pkg string) ([]string, error) {
	if vs, ok := f.versions[category+"/"+pkg]; ok {
		return vs, nil
	}
	return nil, provider.ErrNotFound
}

func (f *fakeReviveProvider) LocalPackagePath(category, pkg string) (string, error) {
	return f.dir, nil
}

func (f *fakeReviveProvider) GetName() string   { return "fake" }
func (f *fakeReviveProvider) SupportsAPI() bool { return true }
func (f *fakeReviveProvider) Close() error      { return nil }

// pinReviveConcurrency pins the concurrency option to a valid value for the
// duration of the test. reviveCheckerOptions feeds it into WithConcurrency,
// which rejects values outside [1, 100].
func pinReviveConcurrency(t *testing.T, auOpts *autoupdateOptions) {
	t.Helper()
	auOpts.concurrency = autoupdate.DefaultConcurrency
}

// TestReviveCheckerOptions asserts the shared option builder returns a non-empty
// option set for a zero LLM config (no provider configured).
func TestReviveCheckerOptions(t *testing.T) {
	auOpts := testAutoupdateOptions()
	pinReviveConcurrency(t, auOpts)

	opts := testAutoupdateRun(auOpts).reviveCheckerOptions(t.TempDir(), 0, 0, config.LLMConfig{})
	if len(opts) == 0 {
		t.Fatal("reviveCheckerOptions returned an empty option set")
	}

	// A positive cacheTTL appends WithCacheTTL, so the set must be at least as
	// large as the TTL-less one.
	withTTL := testAutoupdateRun(auOpts).reviveCheckerOptions(t.TempDir(), 1, 0, config.LLMConfig{})
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

// TestDisplayReviveSummary pins the revive summary byte for byte (U3): the
// expected text is the output of displayReviveSummary's format strings as they
// stood before story 060 moved the pipeline into autoupdate.Reviver (1462803).
// It also asserts the returned failure count.
func TestDisplayReviveSummary(t *testing.T) {
	outcomes := []autoupdate.ReviveOutcome{
		{Package: "a/one", Status: autoupdate.ReviveRevived, Detail: "1.0 → 2.0"},
		{Package: "a/two", Status: autoupdate.ReviveSkipped, Detail: "already current"},
		{Package: "a/three", Status: autoupdate.ReviveFailed, Detail: "boom"},
		{Package: "a/four", Status: autoupdate.ReviveFailed, Detail: "kaboom"},
	}

	var failures int
	out := captureStdout(t, func() { failures = displayReviveSummary(outcomes) })

	if failures != 2 {
		t.Errorf("displayReviveSummary failure count = %d, want 2", failures)
	}
	const want = "\nRevive Summary\n\n" +
		"  ✓ a/one: 1.0 → 2.0\n" +
		"  - a/two: already current\n" +
		"  ✗ a/three: boom\n" +
		"  ✗ a/four: kaboom\n" +
		"\n" +
		"  Revived: 1\n" +
		"  Skipped: 1\n" +
		"  Failed:  2\n" +
		"Don't forget to commit the changes with 'bentoo overlay commit'\n"
	if out != want {
		t.Errorf("revive summary differs (U3)\ngot:\n%q\nwant:\n%q", out, want)
	}

	// No outcomes: zero failures, and only the zero tally is printed.
	out = captureStdout(t, func() { failures = displayReviveSummary(nil) })
	if failures != 0 {
		t.Errorf("displayReviveSummary(nil) = %d, want 0", failures)
	}
	if want := "\nRevive Summary\n\n\n  Revived: 0\n"; out != want {
		t.Errorf("empty revive summary = %q, want %q", out, want)
	}
}

// writeReviveRegexConfig writes a packages.toml under <overlay>/.autoupdate with a
// single DISABLED regex-parser entry pointing at serverURL. The package is
// disabled to mirror the orphan revive flow (autoupdate.Reviver re-enables it
// before checking).
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
	setupTestHome(t)

	cfg := configWithGentooGitHub()

	prov, err := resolveGentooProvider(cfg)
	if err != nil {
		t.Fatalf("resolveGentooProvider: unexpected error: %v", err)
	}
	if prov == nil {
		t.Fatal("resolveGentooProvider returned nil for a valid config gentoo repo")
	}
	defer prov.Close()

	if _, ok := prov.(provider.PackageDirProvider); ok {
		t.Error("API-only GitHub provider unexpectedly implements PackageDirProvider")
	}
}

// TestRunRevive_NoPackageDirProvider covers runRevive's up-front guard: the
// resolved ::gentoo provider (API-only GitHub) cannot expose an on-disk package
// directory, so runRevive logs guidance and exits 1 BEFORE any checker, seed, or
// applier.Apply work. Fully binary-free (no clone, no pkgdev).
func TestRunRevive_NoPackageDirProvider(t *testing.T) {
	auOpts := testAutoupdateOptions()
	pinReviveConcurrency(t, auOpts)
	setupTestHome(t)

	overlay := setupTestOverlay(t)
	configDir := t.TempDir()
	cfg := configWithGentooGitHub()

	code := exitCodeFor(testAutoupdateRun(auOpts).runRevive(context.Background(), overlay, configDir, "dev-test/foo", 0, cfg, config.LLMConfig{}))
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
	auOpts := testAutoupdateOptions()
	pinReviveConcurrency(t, auOpts)
	setupTestHome(t)

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

	code := exitCodeFor(testAutoupdateRun(auOpts).runReviveList(context.Background(), overlay, configDir, 0, cfg, config.LLMConfig{}))
	if code != 0 {
		t.Fatalf("runReviveList exited with code %d, want 0", code)
	}
}
