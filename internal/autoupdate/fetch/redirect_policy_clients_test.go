package fetch

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// Story 052, sub-task 2.2 — S052-R4.6: every autoupdate client that can carry
// a credential follows redirects under the credential-safe policy. Each client
// is driven through a REAL cross-host 302 (127.0.0.1 -> localhost); Go's
// default policy forwards X-Api-Key, X-Auth-Token and Private-Token there, so
// removing the policy from any one constructor turns its subtest red.
func TestAllHTTPClients_HaveCredentialRedirectPolicy(t *testing.T) {
	constructors := append(autoupdateHTTPClientConstructors(), httpClientConstructor{
		name: "NewRetryableHTTPClientWithConfig.h1Client",
		build: func(t *testing.T) *http.Client {
			t.Helper()
			return NewRetryableHTTPClientWithConfig(DefaultRetryConfig()).h1Client
		},
	})
	credentials := []string{"Authorization", "X-Api-Key", "X-Auth-Token", "PRIVATE-TOKEN"}

	for _, ctor := range constructors {
		t.Run(ctor.name, func(t *testing.T) {
			var mu sync.Mutex
			var landed []http.Header
			srvB := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				mu.Lock()
				landed = append(landed, r.Header.Clone())
				mu.Unlock()
				_, _ = w.Write([]byte("ok"))
			}))
			defer srvB.Close()
			srvA := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				http.Redirect(w, r, strings.Replace(srvB.URL, "127.0.0.1", "localhost", 1)+"/landed", http.StatusFound)
			}))
			defer srvA.Close()

			client := ctor.build(t)
			req, err := http.NewRequest(http.MethodGet, srvA.URL+"/start", nil)
			if err != nil {
				t.Fatal(err)
			}
			for _, h := range credentials {
				req.Header.Set(h, "secret")
			}
			resp, err := client.Do(req)
			if err != nil {
				t.Fatalf("cross-host redirect returned %v; want it followed with credentials dropped", err)
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
	}
}
