package autoupdate

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// Story 052, sub-task 1.3 — the two cases the Test Advisor could not reach
// through CheckAll, authored at run time against the scoped fetchContent:
//
//   - S052-R1.4 hostile half: a BENTOO_* credential goes to the package's own
//     url/base_url hosts and nowhere else, even when the request host would be
//     the "own" host of an unscoped call.
//   - S052-R2.3: a body cached for the same URL and headers does not route
//     around the refusal, because the binding is checked before the cache.

// scopeTestServer counts requests and records the X-Api-Key each one carried.
func scopeTestServer(t *testing.T) (*httptest.Server, *atomic.Int64, func() []string) {
	t.Helper()
	var hits atomic.Int64
	keys := make(chan string, 16)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		keys <- r.Header.Get("X-Api-Key")
		_, _ = w.Write([]byte(`{"version": "3.0.0"}`))
	}))
	t.Cleanup(srv.Close)
	return srv, &hits, func() []string {
		var got []string
		for {
			select {
			case k := <-keys:
				got = append(got, k)
			default:
				return got
			}
		}
	}
}

// localhostURL rewrites srv.URL's host to "localhost" with the same port, so a
// test can address one listener under two different hostnames.
func localhostURL(t *testing.T, srvURL string) string {
	t.Helper()
	u, err := url.Parse(srvURL)
	if err != nil {
		t.Fatalf("parse %q: %v", srvURL, err)
	}
	u.Host = "localhost:" + u.Port()
	return u.String()
}

func TestFetchContent_ScopedBentooBinding(t *testing.T) {
	t.Setenv("BENTOO_T", "bentoo-secret")
	headers := map[string]string{"X-Api-Key": "${BENTOO_T}"}

	// Hostile: the request goes to 127.0.0.1, but the package's url and
	// base_url name other hosts — including a lookalike that merely starts with
	// the request host. Unscoped, 127.0.0.1 would count as its own host and be
	// sent.
	for name, cfg := range map[string]*PackageConfig{
		"url on another host":             {URL: "https://vendor.example/latest"},
		"url and base_url on other hosts": {URL: "https://vendor.example/latest", BaseURL: "https://cdn.vendor.example/"},
		"url on a lookalike of the host":  {URL: "https://127.0.0.1.nip.io/latest"},
		"no readable url":                 {URL: "://broken"},
	} {
		t.Run("refused: "+name, func(t *testing.T) {
			srv, hits, _ := scopeTestServer(t)
			checker := newRateLimitTestChecker(t, srv.URL, WithRateLimiter(&recordingRateLimiter{}))
			_, err := checker.fetchContent(srv.URL+"/data", headers, packageCredentialScope(cfg), time.Second)
			if !errors.Is(err, ErrCredentialHostMismatch) {
				t.Fatalf("err = %v; want ErrCredentialHostMismatch", err)
			}
			for _, needle := range []string{"X-Api-Key", "BENTOO_T", "127.0.0.1"} {
				if !strings.Contains(err.Error(), needle) {
					t.Errorf("refusal %q does not name %q", err, needle)
				}
			}
			if strings.Contains(err.Error(), "bentoo-secret") {
				t.Errorf("refusal %q contains the credential value", err)
			}
			if n := hits.Load(); n != 0 {
				t.Errorf("the listener received %d request(s); want 0", n)
			}
		})
	}

	// Converse: the host of base_url is an own host, compared case-insensitively
	// and without its port (S052-R1.7).
	t.Run("sent: request host equals base_url host in another case and port", func(t *testing.T) {
		srv, _, keys := scopeTestServer(t)
		checker := newRateLimitTestChecker(t, srv.URL, WithRateLimiter(&recordingRateLimiter{}))
		cfg := &PackageConfig{URL: "https://vendor.example/latest", BaseURL: "http://LOCALHOST:1/"}
		if _, err := checker.fetchContent(localhostURL(t, srv.URL)+"/data", headers, packageCredentialScope(cfg), time.Second); err != nil {
			t.Fatalf("fetch to the base_url host failed: %v", err)
		}
		if got := keys(); !slices.Equal(got, []string{"bentoo-secret"}) {
			t.Errorf("listener received X-Api-Key %v; want [bentoo-secret]", got)
		}
	})
}

func TestFetchContent_BindingCheckedBeforeBodyCache(t *testing.T) {
	t.Setenv("BENTOO_T", "bentoo-secret")
	headers := map[string]string{"X-Api-Key": "${BENTOO_T}"}

	srv, hits, _ := scopeTestServer(t)
	checker := newRateLimitTestChecker(t, srv.URL, WithRateLimiter(&recordingRateLimiter{}))
	if checker.bodies == nil {
		t.Fatal("the body cache is off; this test needs the default per-run cache")
	}
	target := srv.URL + "/data"

	// A record whose own host IS the listener fetches first; its body is
	// admitted to the per-run cache.
	own := packageCredentialScope(&PackageConfig{URL: target})
	if _, err := checker.fetchContent(target, headers, own, time.Second); err != nil {
		t.Fatalf("priming fetch failed: %v", err)
	}
	if n := hits.Load(); n != 1 {
		t.Fatalf("priming fetch reached the listener %d time(s); want 1", n)
	}

	// A second record, bound elsewhere, asks for the same URL with the same
	// declared headers: same cache key. It must be refused, not served.
	foreign := packageCredentialScope(&PackageConfig{URL: "https://vendor.example/latest"})
	body, err := checker.fetchContent(target, headers, foreign, time.Second)
	if !errors.Is(err, ErrCredentialHostMismatch) {
		t.Fatalf("fetchContent = (%q, %v); want ErrCredentialHostMismatch even with a cached body", body, err)
	}
	if body != nil {
		t.Errorf("a refused fetch returned %d cached byte(s)", len(body))
	}
	if n := hits.Load(); n != 1 {
		t.Errorf("the listener received %d request(s) in total; want 1 (the refusal sends none)", n)
	}
}

func TestPackageCredentialScope(t *testing.T) {
	for _, tc := range []struct {
		name string
		cfg  *PackageConfig
		want []string
	}{
		{"nil record", nil, nil},
		{"url only", &PackageConfig{URL: "https://Vendor.example:8443/latest"}, []string{"Vendor.example"}},
		{"url and base_url", &PackageConfig{URL: "https://a.example/x", BaseURL: "http://b.example/"}, []string{"a.example", "b.example"}},
		{"fallback_url is not an own host", &PackageConfig{URL: "https://a.example/x", FallbackURL: "https://c.example/"}, []string{"a.example"}},
		{"unparseable and empty fields contribute nothing", &PackageConfig{URL: "://broken", BaseURL: ""}, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := packageCredentialScope(tc.cfg).ownHosts; !slices.Equal(got, tc.want) {
				t.Errorf("ownHosts = %v, want %v", got, tc.want)
			}
		})
	}
}
