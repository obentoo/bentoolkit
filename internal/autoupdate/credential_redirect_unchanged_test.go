package autoupdate

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// Story 052 — S052-R9.8 regression guard (GREEN today): a request that carries
// no credential is still redirected across hosts with its non-credential
// headers — Range, the default User-Agent and an arbitrary X-Trace. It guards
// against a redirect policy that strips or refuses more than credentials.
func TestRedirect_CredentialFreeRequestKeepsHeadersAcrossHosts(t *testing.T) {
	var mu sync.Mutex
	var landed []http.Header
	srvB := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		landed = append(landed, r.Header.Clone())
		mu.Unlock()
		w.WriteHeader(http.StatusPartialContent)
		_, _ = w.Write([]byte("1.2.3"))
	}))
	defer srvB.Close()
	srvA := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, strings.Replace(srvB.URL, "127.0.0.1", "localhost", 1)+"/landed", http.StatusFound)
	}))
	defer srvA.Close()

	client := NewRetryableHTTPClient()
	client.SetDelayFunc(func(time.Duration) {})
	resp, err := client.GetWithHeadersContext(context.Background(), srvA.URL+"/start",
		map[string]string{"Range": "bytes=0-15", "X-Trace": "t1"})
	if err != nil {
		t.Fatalf("credential-free cross-host redirect failed: %v", err)
	}
	_ = resp.Body.Close()

	mu.Lock()
	defer mu.Unlock()
	if len(landed) != 1 {
		t.Fatalf("second host received %d requests, want 1", len(landed))
	}
	for name, want := range map[string]string{"Range": "bytes=0-15", "X-Trace": "t1", "User-Agent": defaultUserAgent()} {
		if got := landed[0].Get(name); got != want {
			t.Errorf("second host received %s = %q, want %q", name, got, want)
		}
	}
}
