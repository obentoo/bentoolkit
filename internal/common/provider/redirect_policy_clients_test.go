package provider

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// Story 052, sub-task 2.3 — S052-R4.6: the GitHub and GitLab provider clients
// never carry PRIVATE-TOKEN / Authorization (or any other credential header)
// through a redirect to another host. Driven through a real 127.0.0.1 ->
// localhost 302.
func TestAllHTTPClients_HaveCredentialRedirectPolicy(t *testing.T) {
	t.Setenv("HOME", t.TempDir()) // the constructors create a cache dir under HOME
	credentials := []string{"Authorization", "X-Api-Key", "X-Auth-Token", "PRIVATE-TOKEN"}

	for _, ctor := range providerHTTPClientConstructors() {
		t.Run(ctor.name, func(t *testing.T) {
			var mu sync.Mutex
			var landed []http.Header
			srvB := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				mu.Lock()
				landed = append(landed, r.Header.Clone())
				mu.Unlock()
				_, _ = w.Write([]byte("[]"))
			}))
			defer srvB.Close()
			srvA := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				http.Redirect(w, r, strings.Replace(srvB.URL, "127.0.0.1", "localhost", 1)+"/landed", http.StatusFound)
			}))
			defer srvA.Close()

			req, err := http.NewRequest(http.MethodGet, srvA.URL+"/start", nil)
			if err != nil {
				t.Fatal(err)
			}
			for _, h := range credentials {
				req.Header.Set(h, "secret")
			}
			resp, err := ctor.build(t).Do(req)
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
