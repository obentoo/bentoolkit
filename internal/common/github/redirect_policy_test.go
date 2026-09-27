package github

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// Story 052, sub-task 2.3 — S052-R4.6: github.NewClient's HTTP client follows
// redirects under the credential-safe policy. Two real round trips: a
// cross-host 302 (custom credential headers must be dropped) and an https ->
// http downgrade on the same host, where Go's default policy keeps
// Authorization and sends the token in cleartext.
func TestNewClient_HasCredentialRedirectPolicy(t *testing.T) {
	credentials := []string{"Authorization", "X-Api-Key", "X-Auth-Token", "PRIVATE-TOKEN"}

	t.Run("cross-host redirect drops credentials", func(t *testing.T) {
		var mu sync.Mutex
		var landed []http.Header
		srvB := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			mu.Lock()
			landed = append(landed, r.Header.Clone())
			mu.Unlock()
			_, _ = w.Write([]byte("{}"))
		}))
		defer srvB.Close()
		srvA := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			http.Redirect(w, r, strings.Replace(srvB.URL, "127.0.0.1", "localhost", 1)+"/landed", http.StatusFound)
		}))
		defer srvA.Close()

		req, _ := http.NewRequest(http.MethodGet, srvA.URL+"/start", nil)
		for _, h := range credentials {
			req.Header.Set(h, "secret")
		}
		resp, err := NewClient().HTTPClient.Do(req)
		if err != nil {
			t.Fatalf("cross-host redirect returned %v", err)
		}
		_ = resp.Body.Close()
		mu.Lock()
		defer mu.Unlock()
		if len(landed) != 1 {
			t.Fatalf("second host received %d requests, want 1", len(landed))
		}
		for _, h := range credentials {
			if v := landed[0].Get(h); v != "" {
				t.Errorf("second host received %s = %q; want it dropped", h, v)
			}
		}
	})

	t.Run("https to http with Authorization is refused", func(t *testing.T) {
		var plainHits int
		var mu sync.Mutex
		srvPlain := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			mu.Lock()
			plainHits++
			mu.Unlock()
		}))
		defer srvPlain.Close()
		srvTLS := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			http.Redirect(w, r, srvPlain.URL+"/cleartext", http.StatusFound)
		}))
		defer srvTLS.Close()

		hc := *NewClient().HTTPClient // keep its CheckRedirect; trust the test certificate
		hc.Transport = srvTLS.Client().Transport
		req, _ := http.NewRequest(http.MethodGet, srvTLS.URL+"/repos/o/r", nil)
		req.Header.Set("Authorization", "Bearer ghp_secret")
		resp, err := hc.Do(req)
		if resp != nil {
			_ = resp.Body.Close()
		}
		if err == nil {
			t.Errorf("https -> http redirect carrying Authorization was followed; want it refused")
		}
		mu.Lock()
		defer mu.Unlock()
		if plainHits != 0 {
			t.Errorf("the http target received %d request(s); want 0", plainHits)
		}
	})
}
