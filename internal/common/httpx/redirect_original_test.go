package httpx

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

// Story 052, sub-task 2.1 — S052-R4.4, run-time addition: the https to http
// refusal is decided on the ORIGINAL request. On a hop that also changes host,
// net/http has already stripped Authorization from the redirected request
// before CheckRedirect runs, so a policy that read the hop's own headers would
// see no credential and follow the downgrade. Only a real round trip exercises
// that stripping; the direct-call cases in redirect_test.go clone every header
// onto the hop and cannot tell the two readings apart.
func TestCredentialRedirectPolicy_DowngradeDecidedOnOriginal(t *testing.T) {
	plain := &recorder{}
	srvPlain := httptest.NewServer(plain)
	defer srvPlain.Close()
	srvTLS := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, asLocalhost(srvPlain.URL)+"/cleartext", http.StatusFound)
	}))
	defer srvTLS.Close()

	client := srvTLS.Client()
	client.CheckRedirect = CredentialRedirectPolicy

	req, err := http.NewRequest(http.MethodGet, srvTLS.URL+"/start", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer secret")

	resp, err := client.Do(req)
	if resp != nil {
		_ = resp.Body.Close()
	}
	if !errors.Is(err, ErrInsecureRedirect) {
		t.Errorf("https -> http on another host with only Authorization: err = %v; want ErrInsecureRedirect", err)
	}
	if n := plain.hits.Load(); n != 0 {
		t.Errorf("the http target received %d request(s); want 0", n)
	}
}
