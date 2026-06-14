package autoupdate

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"
)

// newAuxTestChecker builds a Checker wired for direct resolveAuxValue calls:
// an unlimited rate limiter (no per-host gating in tests) and a throwaway
// config dir. The overlay path is irrelevant here — resolveAuxValue only fetches
// and matches against the body.
func newAuxTestChecker(t *testing.T) *Checker {
	t.Helper()
	c, err := NewChecker(t.TempDir(),
		WithConfigDir(t.TempDir()),
		WithPackagesConfig(&PackagesConfig{Packages: map[string]PackageConfig{}}),
		WithRateLimiter(unlimitedRateLimiter()),
	)
	if err != nil {
		t.Fatalf("NewChecker: %v", err)
	}
	return c
}

// TestResolveAuxValue_NoMatch covers the branch where the body is fetched but
// aux_pattern matches no capture group: resolveAuxValue returns "" and records a
// soft error on the CheckResult.
func TestResolveAuxValue_NoMatch(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("nothing relevant on this page"))
	}))
	t.Cleanup(server.Close)

	c := newAuxTestChecker(t)
	cfg := &PackageConfig{URL: server.URL, AuxPattern: `(esr-bb[0-9]+)`}
	result := &CheckResult{}

	if got := c.resolveAuxValue(cfg, result); got != "" {
		t.Errorf("expected empty value on no-match, got %q", got)
	}
	if result.Error == nil {
		t.Error("expected a soft error to be recorded on no-match")
	}
}

// TestResolveAuxValue_EmptyPattern covers the early return: an unset aux_pattern
// yields "" with no fetch and no error.
func TestResolveAuxValue_EmptyPattern(t *testing.T) {
	c := newAuxTestChecker(t)
	result := &CheckResult{}
	if got := c.resolveAuxValue(&PackageConfig{URL: "http://unused"}, result); got != "" {
		t.Errorf("expected empty value for empty aux_pattern, got %q", got)
	}
	if result.Error != nil {
		t.Errorf("expected no error for empty aux_pattern, got %v", result.Error)
	}
}

// TestResolveAuxValue_FetchError covers the fetch-failure branch: a closed server
// yields a connection error, so resolveAuxValue returns "" and records the error.
func TestResolveAuxValue_FetchError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	url := server.URL
	server.Close() // close immediately so the address refuses connections

	c := newAuxTestChecker(t)
	result := &CheckResult{}
	if got := c.resolveAuxValue(&PackageConfig{URL: url, AuxPattern: `(x)`}, result); got != "" {
		t.Errorf("expected empty value on fetch error, got %q", got)
	}
	if result.Error == nil {
		t.Error("expected a soft error to be recorded on fetch failure")
	}
}

// TestResolveAuxSHA covers the sibling helper resolveAuxValue parallels: an
// unset path is an early return; a successful JSON parse yields the trimmed SHA;
// a fetch failure and a parse failure both yield "" and record a soft error.
func TestResolveAuxSHA(t *testing.T) {
	t.Run("empty path", func(t *testing.T) {
		c := newAuxTestChecker(t)
		result := &CheckResult{}
		if got := c.resolveAuxSHA(&PackageConfig{}, result); got != "" || result.Error != nil {
			t.Errorf("empty path: got %q err %v", got, result.Error)
		}
	})

	t.Run("success", func(t *testing.T) {
		sha := "b887a26c4f70bd8136bfffeda812b24194ec9ce0"
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"commitSha":"` + sha + `"}`))
		}))
		t.Cleanup(server.Close)
		c := newAuxTestChecker(t)
		result := &CheckResult{}
		if got := c.resolveAuxSHA(&PackageConfig{URL: server.URL, CommitSHAPath: "commitSha"}, result); got != sha {
			t.Errorf("success: got %q, want %q (err %v)", got, sha, result.Error)
		}
	})

	t.Run("parse error", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte(`not json`))
		}))
		t.Cleanup(server.Close)
		c := newAuxTestChecker(t)
		result := &CheckResult{}
		if got := c.resolveAuxSHA(&PackageConfig{URL: server.URL, CommitSHAPath: "commitSha"}, result); got != "" {
			t.Errorf("parse error: expected empty, got %q", got)
		}
		if result.Error == nil {
			t.Error("parse error: expected a soft error")
		}
	})

	t.Run("fetch error", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
		url := server.URL
		server.Close()
		c := newAuxTestChecker(t)
		result := &CheckResult{}
		if got := c.resolveAuxSHA(&PackageConfig{URL: url, CommitSHAPath: "commitSha"}, result); got != "" {
			t.Errorf("fetch error: expected empty, got %q", got)
		}
		if result.Error == nil {
			t.Error("fetch error: expected a soft error")
		}
	})
}

// TestSubstituteAuxVar_ReadError covers the read-failure branch: a path that does
// not exist returns an error instead of silently succeeding.
func TestSubstituteAuxVar_ReadError(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "does-not-exist.ebuild")
	if err := substituteAuxVar(missing, "MY_BUILD", "esr-bb24"); err == nil {
		t.Error("expected an error reading a non-existent ebuild")
	}
}

// TestDisableOrphans covers the Checker.DisableOrphans method (the sibling of
// EnablePackagesInConfig used by the revive flow): it must both rewrite the
// packages.toml on disk and flip the in-memory config so a subsequent run skips
// the package.
func TestDisableOrphans(t *testing.T) {
	content := `["a/b"]
url = "https://x/y"
parser = "json"
path = "v"

["c/d"]
url = "https://x/z"
parser = "json"
path = "v"
`
	overlay, _ := writePackagesTOML(t, content)
	cfg, err := LoadPackagesConfig(overlay)
	if err != nil {
		t.Fatalf("LoadPackagesConfig: %v", err)
	}

	c, err := NewChecker(overlay,
		WithConfigDir(t.TempDir()),
		WithPackagesConfig(cfg),
		WithRateLimiter(unlimitedRateLimiter()),
	)
	if err != nil {
		t.Fatalf("NewChecker: %v", err)
	}

	if err := c.DisableOrphans([]string{"a/b"}); err != nil {
		t.Fatalf("DisableOrphans: %v", err)
	}

	// In-memory config flipped.
	if pc := c.Config().Packages["a/b"]; pc.IsEnabled() {
		t.Error("a/b should be disabled in the in-memory config")
	}
	if pc := c.Config().Packages["c/d"]; !pc.IsEnabled() {
		t.Error("c/d should remain enabled in the in-memory config")
	}

	// On-disk file flipped too (reload and re-check).
	reloaded, err := LoadPackagesConfig(overlay)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if pc := reloaded.Packages["a/b"]; pc.IsEnabled() {
		t.Error("a/b should be disabled on disk")
	}

	// An empty list is a no-op (covers the early return).
	if err := c.DisableOrphans(nil); err != nil {
		t.Errorf("DisableOrphans(nil): %v", err)
	}
}

// TestExtractPackageAtom covers the atom-parsing helper across its branches:
// leading operators, a version boundary, slot, USE flags, the no-slash case, and
// a bare atom.
func TestExtractPackageAtom(t *testing.T) {
	cases := map[string]string{
		">=dev-util/foo-1.2.3":   "dev-util/foo",
		"=app-editors/vim-9.0":   "app-editors/vim",
		"~net-misc/curl-8.1":     "net-misc/curl",
		"dev-libs/openssl:0/3":   "dev-libs/openssl",
		"x11-libs/gtk+[wayland]": "x11-libs/gtk+",
		"sys-apps/portage":       "sys-apps/portage",
		"noslash":                "",
		"":                       "",
	}
	for atom, want := range cases {
		if got := extractPackageAtom(atom); got != want {
			t.Errorf("extractPackageAtom(%q) = %q, want %q", atom, got, want)
		}
	}
}

// TestOptionSetters exercises the small functional-option closures that are
// otherwise only wired from cmd/ (and so show as uncovered): the checker's
// GitHub token, the applier's packages config, and the analyzer's context and
// timeouts. Construction is allowed to fail (e.g. analyzer config loading) — the
// options still run inside the constructor's apply loop, which is the point.
func TestOptionSetters(t *testing.T) {
	empty := &PackagesConfig{Packages: map[string]PackageConfig{}}

	if _, err := NewChecker(t.TempDir(),
		WithConfigDir(t.TempDir()),
		WithPackagesConfig(empty),
		WithGitHubToken("ghp_test_token"),
		WithRateLimiter(unlimitedRateLimiter()),
	); err != nil {
		t.Fatalf("NewChecker with token: %v", err)
	}

	if _, err := NewApplier(t.TempDir(), t.TempDir(),
		WithApplierPackagesConfig(empty),
	); err != nil {
		t.Fatalf("NewApplier with packages config: %v", err)
	}

	// The analyzer constructor may fail to load a packages config from an empty
	// overlay; that is fine — the option closures already ran.
	_, _ = NewAnalyzer(t.TempDir(),
		WithAnalyzerContext(context.Background()),
		WithAnalyzerOpTimeout(2*time.Second),
		WithAnalyzerLLMTimeout(3*time.Second),
	)
}
