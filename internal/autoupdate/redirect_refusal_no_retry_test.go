package autoupdate

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/obentoo/bentoolkit/internal/common/httputil"
)

// Story 052 — S052-R4.4, run-time addition from the task-2 tech review: a
// refused https -> http redirect is a verdict, not a transient failure. It is
// returned after ONE attempt (a retry would only re-send the credential to the
// original host and count against the circuit breaker), and callers can
// detect it with errors.Is.
func TestDoWithContext_InsecureRedirectIsNotRetried(t *testing.T) {
	var plainHits, tlsHits atomic.Int64
	srvPlain := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		plainHits.Add(1)
	}))
	defer srvPlain.Close()
	srvTLS := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		tlsHits.Add(1)
		http.Redirect(w, r, srvPlain.URL+"/x", http.StatusFound)
	}))
	defer srvTLS.Close()

	c := NewRetryableHTTPClient()
	c.SetDelayFunc(func(time.Duration) {})
	c.client.Transport = srvTLS.Client().Transport // trust the test certificate, keep the policy

	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, srvTLS.URL+"/start", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer secret")
	resp, err := c.DoWithContext(context.Background(), req)
	if resp != nil && resp.Body != nil {
		_ = resp.Body.Close()
	}
	if !errors.Is(err, httputil.ErrInsecureRedirect) {
		t.Errorf("err = %v; want errors.Is(err, httputil.ErrInsecureRedirect)", err)
	}
	if n := tlsHits.Load(); n != 1 {
		t.Errorf("the https host received %d request(s); want 1 — a refusal is not retried", n)
	}
	if n := plainHits.Load(); n != 0 {
		t.Errorf("the http host received %d request(s); want 0", n)
	}
}
