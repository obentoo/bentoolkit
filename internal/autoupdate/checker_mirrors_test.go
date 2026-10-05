package autoupdate

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

// deadURL returns the URL of a server that is already closed, so a request to
// it fails in transport (connection refused).
func deadURL(t *testing.T) string {
	t.Helper()
	srv := httptest.NewServer(http.NotFoundHandler())
	u := srv.URL
	srv.Close()
	return u
}

// mirrorChecker builds a checker with no retries, so a dead host fails at once.
func mirrorChecker(t *testing.T, pkg string, cfg PackageConfig) *Checker {
	t.Helper()
	tmpDir := t.TempDir()
	overlayDir := filepath.Join(tmpDir, "overlay")
	createTestEbuild(t, overlayDir, pkg, "1.0.0")
	c, err := NewChecker(overlayDir,
		WithConfigDir(filepath.Join(tmpDir, "config")),
		WithPackagesConfig(&PackagesConfig{Packages: map[string]PackageConfig{pkg: cfg}}),
		WithRateLimiter(unlimitedRateLimiter()),
		WithHTTPClient(NewRetryableHTTPClientWithConfig(RetryConfig{MaxRetries: 0, Timeout: 5 * time.Second})),
	)
	if err != nil {
		t.Fatalf("NewChecker: %v", err)
	}
	return c
}

// TestMirrorsServeWhenURLFails pins the chain: url unreachable, the first mirror
// answering 404, the second serving the version. Mirrors get the record's
// User-Agent but never its credential header.
func TestMirrorsServeWhenURLFails(t *testing.T) {
	notFound := httptest.NewServer(http.NotFoundHandler())
	t.Cleanup(notFound.Close)

	var (
		mu    sync.Mutex
		ua    string
		authz string
	)
	good := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		ua, authz = r.Header.Get("User-Agent"), r.Header.Get("Authorization")
		mu.Unlock()
		_, _ = w.Write([]byte(`{"version":"2.0.0"}`))
	}))
	t.Cleanup(good.Close)

	pkg := "app-office/libreoffice"
	c := mirrorChecker(t, pkg, PackageConfig{
		URL:     deadURL(t),
		Parser:  "json",
		Path:    "version",
		Headers: map[string]string{"User-Agent": "bentoo-test/1", "Authorization": "Bearer literal"},
		Mirrors: []string{notFound.URL, good.URL},
	})

	result, err := c.CheckPackage(t.Context(), pkg, true)
	if err != nil {
		t.Fatalf("CheckPackage: %v", err)
	}
	if result.UpstreamVersion != "2.0.0" {
		t.Errorf("UpstreamVersion = %q, want %q from the second mirror", result.UpstreamVersion, "2.0.0")
	}
	mu.Lock()
	defer mu.Unlock()
	if ua != "bentoo-test/1" {
		t.Errorf("mirror User-Agent = %q, want the record's", ua)
	}
	if authz != "" {
		t.Errorf("a credential header reached a mirror: %q", authz)
	}
}

// TestMirrorsAllUnreachableIsATransportFailure pins the classification: every
// source down is ErrUpstreamUnreachable (no registry repair offered), while a
// url that answered with something unreadable stays the record's fault even
// when the mirrors are down.
func TestMirrorsAllUnreachableIsATransportFailure(t *testing.T) {
	pkg := "net-dns/bind-tools"

	t.Run("all down", func(t *testing.T) {
		c := mirrorChecker(t, pkg, PackageConfig{
			URL: deadURL(t), Parser: "json", Path: "version",
			Mirrors: []string{deadURL(t)},
		})
		_, err := c.CheckPackage(t.Context(), pkg, true)
		if !errors.Is(err, ErrUpstreamUnreachable) {
			t.Fatalf("err = %v, want ErrUpstreamUnreachable", err)
		}
	})

	t.Run("url answered, mirror down", func(t *testing.T) {
		page := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte(`{"other":"field"}`))
		}))
		t.Cleanup(page.Close)
		c := mirrorChecker(t, pkg, PackageConfig{
			URL: page.URL, Parser: "json", Path: "version",
			Mirrors: []string{deadURL(t)},
		})
		_, err := c.CheckPackage(t.Context(), pkg, true)
		if err == nil || errors.Is(err, ErrUpstreamUnreachable) {
			t.Fatalf("err = %v, want a record failure that is not ErrUpstreamUnreachable", err)
		}
		if !strings.Contains(err.Error(), "also failed") {
			t.Errorf("the mirror's failure is not reported: %v", err)
		}
	})
}

func TestValidateMirrors(t *testing.T) {
	base := PackageConfig{URL: "https://example.org/v.json", Parser: "json", Path: "version"}
	for _, tc := range []struct {
		name    string
		mirrors []string
		ok      bool
	}{
		{"valid", []string{"https://mirror.example.net/v.json"}, true},
		{"relative", []string{"/v.json"}, false},
		{"ftp", []string{"ftp://mirror.example.net/v.json"}, false},
		{"repeats url", []string{"https://example.org/v.json"}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := base
			cfg.Mirrors = tc.mirrors
			err := ValidatePackageConfig(nil, "app-misc/foo", &cfg)
			if (err == nil) != tc.ok {
				t.Errorf("ValidatePackageConfig(nil, %v) = %v, want ok=%v", tc.mirrors, err, tc.ok)
			}
		})
	}
}

// fakeLive answers Evaluate per URL, recording the order it was asked in.
type fakeLive struct {
	mu      sync.Mutex
	seen    []string
	answers map[string]string
}

func (f *fakeLive) Evaluate(_ context.Context, url, _ string, _ map[string]string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.seen = append(f.seen, url)
	if v, ok := f.answers[url]; ok {
		return v, nil
	}
	return "", fmt.Errorf("page load error at %s: %w", url, context.DeadlineExceeded)
}

// TestMirrorsScriptParser pins mirrors on the script path, which the
// LibreOffice records use: the script is re-run against each mirror in order.
func TestMirrorsScriptParser(t *testing.T) {
	const (
		master = "https://download.documentfoundation.org/libreoffice/src/"
		fau    = "https://ftp.fau.de/tdf/libreoffice/src/"
		osuosl = "https://ftp.osuosl.org/pub/tdf/libreoffice/src/"
	)
	live := &fakeLive{answers: map[string]string{osuosl: "26.2.3.2"}}
	orig := newLiveEvaluator
	newLiveEvaluator = func(time.Duration) (liveEvaluator, error) { return live, nil }
	t.Cleanup(func() { newLiveEvaluator = orig })

	pkg := "app-office/libreoffice"
	c := mirrorChecker(t, pkg, PackageConfig{
		URL: master, Parser: "script", Script: "document.title",
		Mirrors: []string{fau, osuosl},
	})
	result, err := c.CheckPackage(t.Context(), pkg, true)
	if err != nil {
		t.Fatalf("CheckPackage: %v", err)
	}
	if result.UpstreamVersion != "26.2.3.2" {
		t.Errorf("UpstreamVersion = %q, want %q", result.UpstreamVersion, "26.2.3.2")
	}
	live.mu.Lock()
	defer live.mu.Unlock()
	if want := []string{master, fau, osuosl}; !slices.Equal(live.seen, want) {
		t.Errorf("probed %v, want %v", live.seen, want)
	}
}
