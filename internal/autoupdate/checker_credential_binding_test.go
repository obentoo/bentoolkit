package autoupdate

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/obentoo/bentoolkit/internal/autoupdate/fetch"
	"github.com/obentoo/bentoolkit/internal/autoupdate/registry"
)

// Story 052, sub-task 1.3 — S052-R2.1, R2.2: a refused record fails alone,
// names header/variable/host, and neither the fallback nor the LLM stage runs
// after a refusal. (R1.4 scoped and R2.3 are not reachable behaviour-level
// through the checker today — see the Test Advisor report.)

const cbVersionPattern = `([0-9]+\.[0-9]+\.[0-9]+)`

// cbHostTransport answers by path ("/nomatch" -> a body with no version, any
// other path -> "2.0.0") and records, per hostname, every X-Api-Key it saw.
type cbHostTransport struct {
	mu    sync.Mutex
	byURL map[string][]string
}

func (h *cbHostTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	h.mu.Lock()
	if h.byURL == nil {
		h.byURL = map[string][]string{}
	}
	h.byURL[req.URL.String()] = append(h.byURL[req.URL.String()], req.Header.Get("X-Api-Key"))
	h.mu.Unlock()
	body := "2.0.0"
	if strings.HasSuffix(req.URL.Path, "/nomatch") {
		body = "no version here"
	}
	return &http.Response{StatusCode: http.StatusOK, Header: http.Header{},
		Body: io.NopCloser(strings.NewReader(body)), Request: req}, nil
}

func (h *cbHostTransport) seen(u string) []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]string(nil), h.byURL[u]...)
}

func (h *cbHostTransport) total() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	n := 0
	for _, v := range h.byURL {
		n += len(v)
	}
	return n
}

// newCBChecker builds a Checker over packages, with each package's ebuild at
// 1.0.0, a non-blocking rate limiter and the given HTTP client.
func newCBChecker(t *testing.T, packages map[string]registry.PackageConfig, client *fetch.RetryableHTTPClient, opts ...CheckerOption) *Checker {
	t.Helper()
	tmp := t.TempDir()
	overlay := filepath.Join(tmp, "overlay")
	for name := range packages {
		createTestEbuild(t, overlay, name, "1.0.0")
	}
	all := append([]CheckerOption{
		WithConfigDir(filepath.Join(tmp, "config")),
		WithPackagesConfig(&registry.PackagesConfig{Packages: packages}),
		WithHTTPClient(client),
		WithRateLimiter(&recordingRateLimiter{}),
	}, opts...)
	c, err := NewChecker(overlay, all...)
	if err != nil {
		t.Fatalf("NewChecker: %v", err)
	}
	return c
}

func regexPkg(u string, headers map[string]string) registry.PackageConfig {
	return registry.PackageConfig{URL: u, Parser: "regex", Pattern: cbVersionPattern, Headers: headers}
}

func withFallback(p registry.PackageConfig, fallback string) registry.PackageConfig {
	p.FallbackURL, p.FallbackParser, p.FallbackPattern = fallback, "regex", cbVersionPattern
	return p
}

func succeeded(res BatchResult[CheckResult], pkg string) bool {
	for _, it := range res.Items {
		if it.Package == pkg && it.Error == nil {
			return true
		}
	}
	return false
}

func TestCheckAll_CredentialMismatchFailsOnlyThatPackage(t *testing.T) {
	t.Setenv("GITHUB_TOKEN", "ghp_example")
	t.Setenv("BENTOO_T", "bentoo-secret")

	var leakHits atomic.Int64
	var mu sync.Mutex
	var ownKeys []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/latest":
			leakHits.Add(1)
		case "/own":
			mu.Lock()
			ownKeys = append(ownKeys, r.Header.Get("X-Api-Key"))
			mu.Unlock()
		}
		_, _ = w.Write([]byte("3.0.0"))
	}))
	defer srv.Close()

	client := fetch.NewRetryableHTTPClient()
	client.SetDelayFunc(func(time.Duration) {})
	checker := newCBChecker(t, map[string]registry.PackageConfig{
		"app-misc/leak": regexPkg(srv.URL+"/latest", map[string]string{"X-Api-Key": "${GITHUB_TOKEN}"}),
		"app-misc/fine": regexPkg(srv.URL+"/fine", nil),
		"app-misc/own":  regexPkg(srv.URL+"/own", map[string]string{"X-Api-Key": "${BENTOO_T}"}),
	}, client)

	res := checker.CheckAll(t.Context(), true)

	failure, ok := res.Failures["app-misc/leak"]
	if !ok {
		t.Errorf("app-misc/leak is not in Failures (%v); a refused record must fail", res.Failures)
		failure = errors.New("")
	}
	for _, needle := range []string{"X-Api-Key", "GITHUB_TOKEN", "127.0.0.1"} {
		if !strings.Contains(failure.Error(), needle) {
			t.Errorf("failure %q does not name %q", failure, needle)
		}
	}
	if strings.Contains(failure.Error(), "ghp_example") {
		t.Errorf("failure %q contains the token value", failure)
	}
	if n := leakHits.Load(); n != 0 {
		t.Errorf("the listener received %d request(s) for the refused record; want 0", n)
	}
	for _, pkg := range []string{"app-misc/fine", "app-misc/own"} {
		if !succeeded(res, pkg) {
			t.Errorf("%s was not checked successfully (failures: %v); one refusal must not stop the batch", pkg, res.Failures)
		}
	}
	mu.Lock()
	if len(ownKeys) != 1 || ownKeys[0] != "bentoo-secret" {
		t.Errorf("/own received X-Api-Key %v; want [bentoo-secret]", ownKeys)
	}
	mu.Unlock()
}

// cbCountingLLM counts ExtractVersion calls; every other LLMProvider method is
// unreachable from the check path under test.
type cbCountingLLM struct {
	LLMProvider
	calls atomic.Int64
}

func (l *cbCountingLLM) ExtractVersion(context.Context, []byte, string) (string, error) {
	l.calls.Add(1)
	return "9.9.9", nil
}

func TestFetchUpstreamVersionRaw_MismatchSkipsFallbackAndLLM(t *testing.T) {
	t.Setenv("GITHUB_TOKEN", "ghp_example")

	tr := &cbHostTransport{}
	fake := fetch.NewRetryableHTTPClient()
	fake.SetHTTPClient(&http.Client{Transport: tr})
	fake.SetDelayFunc(func(time.Duration) {})
	llm := &cbCountingLLM{}
	checker := newRateLimitTestChecker(t, "https://unused.example/",
		WithHTTPClient(fake), WithRateLimiter(&recordingRateLimiter{}), WithLLMClient(llm))

	// The fallback sits on a host GITHUB_TOKEN IS bound to, so trying it would
	// be observable as a request rather than a second refusal.
	cfg := withFallback(regexPkg("https://updates.example/latest",
		map[string]string{"X-Api-Key": "${GITHUB_TOKEN}"}), "https://api.github.com/repos/o/r/releases/latest")
	cfg.LLMPrompt = "extract the version"

	version, err := checker.fetchUpstreamVersionRaw(t.Context(), "app-misc/leak", &cfg)
	if !errors.Is(err, fetch.ErrCredentialHostMismatch) {
		t.Fatalf("fetchUpstreamVersionRaw = (%q, %v); want errors.Is(err, ErrCredentialHostMismatch)", version, err)
	}
	if n := tr.total(); n != 0 {
		t.Errorf("%d request(s) left the process (%v); the fallback must not be tried after a refusal", n, tr.byURL)
	}
	if n := llm.calls.Load(); n != 0 {
		t.Errorf("the LLM stage ran %d time(s); it must not run after a refusal", n)
	}

	// Converse: an ordinary primary failure still falls back.
	t.Run("a non-refusal primary failure still tries the fallback", func(t *testing.T) {
		tr2 := &cbHostTransport{}
		fake2 := fetch.NewRetryableHTTPClient()
		fake2.SetHTTPClient(&http.Client{Transport: tr2})
		fake2.SetDelayFunc(func(time.Duration) {})
		c2 := newRateLimitTestChecker(t, "https://unused.example/",
			WithHTTPClient(fake2), WithRateLimiter(&recordingRateLimiter{}))
		cfg2 := withFallback(regexPkg("https://api.github.com/repos/o/r/nomatch",
			map[string]string{"X-Api-Key": "${GITHUB_TOKEN}"}), "https://api.github.com/repos/o/r/releases/latest")
		v, err := c2.fetchUpstreamVersionRaw(t.Context(), "app-misc/ok", &cfg2)
		if err != nil || v != "2.0.0" {
			t.Fatalf("fetchUpstreamVersionRaw = (%q, %v); want (2.0.0, nil) from the fallback", v, err)
		}
		if got := tr2.seen("https://api.github.com/repos/o/r/releases/latest"); len(got) != 1 {
			t.Errorf("fallback received %d request(s); want 1", len(got))
		}
	})
}
