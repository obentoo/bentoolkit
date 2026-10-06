package httpx

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
)

// Story 052, sub-task 2.1 — S052-R1.7, R4.1..R4.5: one redirect policy that
// never carries a credential to another hostname or to plain http.
//
// net/http re-copies headers from the INITIAL request onto every hop and only
// then calls CheckRedirect, so the direct-call cases below build each hop's
// request the same way: its headers are a clone of via[0]'s.

var credentialHeaders = []string{"Authorization", "X-Api-Key", "X-Auth-Token", "PRIVATE-TOKEN"}

func withCredentials(h http.Header) {
	for _, name := range credentialHeaders {
		h.Set(name, "secret-"+name)
	}
	h.Set("X-Trace", "trace-1")
}

// hop builds the request net/http would hand to CheckRedirect for target.
func hop(t *testing.T, target string, via []*http.Request) *http.Request {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, target, nil)
	if err != nil {
		t.Fatalf("NewRequest(%s): %v", target, err)
	}
	if len(via) > 0 {
		req.Header = via[0].Header.Clone()
	}
	return req
}

func origin(t *testing.T, u string, creds bool) *http.Request {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, u, nil)
	if err != nil {
		t.Fatalf("NewRequest(%s): %v", u, err)
	}
	if creds {
		withCredentials(req.Header)
	}
	return req
}

func credentialsLeft(h http.Header) []string {
	var left []string
	for _, name := range credentialHeaders {
		if h.Get(name) != "" {
			left = append(left, name)
		}
	}
	return left
}

// recorder is an httptest handler that records the credential headers each
// request carried.
type recorder struct {
	hits atomic.Int64
	mu   sync.Mutex
	seen [][]string
	path []string
}

func (r *recorder) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	r.hits.Add(1)
	r.mu.Lock()
	r.seen = append(r.seen, credentialsLeft(req.Header))
	r.path = append(r.path, req.URL.Path)
	r.mu.Unlock()
	_, _ = w.Write([]byte("ok"))
}

func asLocalhost(u string) string { return strings.Replace(u, "127.0.0.1", "localhost", 1) }

func TestCredentialRedirectPolicy_DropsOnCrossHost(t *testing.T) {
	// Real round trip: 127.0.0.1 -> localhost (same listener family, different
	// hostname). Go's default policy forwards every custom header here.
	b := &recorder{}
	srvB := httptest.NewServer(b)
	defer srvB.Close()
	srvA := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, asLocalhost(srvB.URL)+"/landed", http.StatusFound)
	}))
	defer srvA.Close()

	client := &http.Client{CheckRedirect: CredentialRedirectPolicy}
	req := origin(t, srvA.URL+"/start", true)
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("a cross-host redirect must be FOLLOWED (headers dropped), got error: %v", err)
	}
	_ = resp.Body.Close()
	if b.hits.Load() != 1 {
		t.Fatalf("second host received %d requests, want 1", b.hits.Load())
	}
	if left := b.seen[0]; len(left) != 0 {
		t.Errorf("second host received credential headers %v; want none", left)
	}

	// Direct-call matrix for the hostname rule (S052-R1.7): exact,
	// case-insensitive, port ignored, no subdomain match either way.
	for _, tc := range []struct {
		name, from, to string
		keep           bool
	}{
		{"to a subdomain", "https://example.com/a", "https://sub.example.com/b", false},
		{"to the parent domain", "https://sub.example.com/a", "https://example.com/b", false},
		{"to an unrelated host", "https://example.com/a", "https://other.example/b", false},
		{"to the same hostname in another case and port", "https://example.com/a", "https://EXAMPLE.com:8443/b", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			via := []*http.Request{origin(t, tc.from, true)}
			req := hop(t, tc.to, via)
			if err := CredentialRedirectPolicy(req, via); err != nil {
				t.Fatalf("policy returned %v; want the redirect followed", err)
			}
			left := credentialsLeft(req.Header)
			if tc.keep && len(left) != len(credentialHeaders) {
				t.Errorf("kept %v; want all four credential headers on the same hostname", left)
			}
			if !tc.keep && len(left) != 0 {
				t.Errorf("kept %v on %s -> %s; want none", left, tc.from, tc.to)
			}
			if req.Header.Get("X-Trace") != "trace-1" {
				t.Errorf("a non-credential header was dropped: X-Trace = %q", req.Header.Get("X-Trace"))
			}
		})
	}
}

func TestCredentialRedirectPolicy_StaysDroppedOnReturn(t *testing.T) {
	// Real chain A(127.0.0.1) -> B(localhost) -> A/final.
	a := &recorder{}
	var srvA *httptest.Server
	srvB := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, srvA.URL+"/final", http.StatusFound)
	}))
	defer srvB.Close()
	srvA = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/final" {
			a.ServeHTTP(w, r)
			return
		}
		http.Redirect(w, r, asLocalhost(srvB.URL)+"/bounce", http.StatusFound)
	}))
	defer srvA.Close()

	client := &http.Client{CheckRedirect: CredentialRedirectPolicy}
	resp, err := client.Do(origin(t, srvA.URL+"/start", true))
	if err != nil {
		t.Fatalf("chain returned %v", err)
	}
	_ = resp.Body.Close()
	if a.hits.Load() != 1 {
		t.Fatalf("/final received %d requests, want 1", a.hits.Load())
	}
	if left := a.seen[0]; len(left) != 0 {
		t.Errorf("the hop back to the original host carried %v; once dropped, credentials stay dropped", left)
	}

	// Direct call: via = [A, B], hop back to A.
	via := []*http.Request{origin(t, "https://a.example/start", true), origin(t, "https://b.example/bounce", false)}
	req := hop(t, "https://a.example/final", via)
	if err := CredentialRedirectPolicy(req, via); err != nil {
		t.Fatalf("policy returned %v", err)
	}
	if left := credentialsLeft(req.Header); len(left) != 0 {
		t.Errorf("hop back to a.example kept %v; want none", left)
	}
}

func TestCredentialRedirectPolicy_KeepsOnSameHost(t *testing.T) {
	// https, same server, another path: all four must arrive.
	rec := &recorder{}
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/next" {
			rec.ServeHTTP(w, r)
			return
		}
		http.Redirect(w, r, "/next", http.StatusFound)
	}))
	defer srv.Close()
	// Same hostname, another port (S052-R1.7 ignores the port).
	other := &recorder{}
	srvPort := httptest.NewTLSServer(other)
	defer srvPort.Close()
	srvHop := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, srvPort.URL+"/port", http.StatusFound)
	}))
	defer srvHop.Close()

	for name, tc := range map[string]struct {
		start string
		rec   *recorder
	}{
		"same host other path": {srv.URL + "/start", rec},
		"same host other port": {srvHop.URL + "/start", other},
	} {
		t.Run(name, func(t *testing.T) {
			client := srv.Client()
			client.CheckRedirect = CredentialRedirectPolicy
			resp, err := client.Do(origin(t, tc.start, true))
			if err != nil {
				t.Fatalf("redirect returned %v", err)
			}
			_ = resp.Body.Close()
			if tc.rec.hits.Load() != 1 {
				t.Fatalf("target received %d requests, want 1", tc.rec.hits.Load())
			}
			if left := tc.rec.seen[0]; len(left) != len(credentialHeaders) {
				t.Errorf("target received %v; want all four credential headers kept", left)
			}
		})
	}
}

func TestCredentialRedirectPolicy_RefusesHTTPSDowngrade(t *testing.T) {
	plain := &recorder{}
	srvPlain := httptest.NewServer(plain)
	defer srvPlain.Close()
	srvTLS := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, srvPlain.URL+"/cleartext", http.StatusFound)
	}))
	defer srvTLS.Close()

	client := srvTLS.Client()
	client.CheckRedirect = CredentialRedirectPolicy

	resp, err := client.Do(origin(t, srvTLS.URL+"/start", true))
	if resp != nil {
		_ = resp.Body.Close()
	}
	if err == nil {
		t.Errorf("https -> http with a credential was followed; want it refused")
	} else if !strings.Contains(err.Error(), "127.0.0.1") {
		t.Errorf("refusal %q does not name the target host", err)
	}
	if n := plain.hits.Load(); n != 0 {
		t.Errorf("the http target received %d request(s); want 0", n)
	}

	// Converse: the same downgrade with no credential is followed.
	resp, err = client.Do(origin(t, srvTLS.URL+"/start", false))
	if err != nil {
		t.Fatalf("a credential-free https -> http redirect must be followed, got %v", err)
	}
	_ = resp.Body.Close()
	if n := plain.hits.Load(); n != 1 {
		t.Errorf("the http target received %d request(s) for the credential-free request; want 1", n)
	}

	// Decided on the ORIGINAL request: refused even when the hop also changes
	// host, and even after an earlier hop already dropped the headers.
	for name, via := range map[string][]*http.Request{
		"downgrade that also changes host": {origin(t, "https://a.example/start", true)},
		"downgrade after a dropping hop": {origin(t, "https://a.example/start", true),
			origin(t, "https://b.example/mid", false)},
	} {
		t.Run(name, func(t *testing.T) {
			req := hop(t, "http://c.example/end", via)
			err := CredentialRedirectPolicy(req, via)
			if err == nil || !strings.Contains(err.Error(), "c.example") {
				t.Errorf("policy returned %v; want a refusal naming c.example", err)
			}
		})
	}
	t.Run("http to http on the same host is not a downgrade", func(t *testing.T) {
		via := []*http.Request{origin(t, "http://a.example/start", true)}
		req := hop(t, "http://a.example:81/end", via)
		if err := CredentialRedirectPolicy(req, via); err != nil {
			t.Errorf("policy returned %v; want nil", err)
		}
		if left := credentialsLeft(req.Header); len(left) != len(credentialHeaders) {
			t.Errorf("kept %v; want all four", left)
		}
	})
}

func TestCredentialRedirectPolicy_StopsAfterTen(t *testing.T) {
	var hits atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := hits.Add(1)
		http.Redirect(w, r, fmt.Sprintf("/r/%d", n), http.StatusFound)
	}))
	defer srv.Close()

	client := &http.Client{CheckRedirect: CredentialRedirectPolicy}
	resp, err := client.Get(srv.URL + "/r/0")
	if resp != nil {
		_ = resp.Body.Close()
	}
	if err == nil {
		t.Errorf("an endless redirect chain returned no error")
	}
	if n := hits.Load(); n != 10 {
		t.Errorf("server received %d requests; want 10 (the initial one plus 9 redirects, as net/http's default)", n)
	}

	mk := func(n int) []*http.Request {
		via := make([]*http.Request, n)
		for i := range via {
			via[i] = origin(t, fmt.Sprintf("https://a.example/%d", i), false)
		}
		return via
	}
	if err := CredentialRedirectPolicy(hop(t, "https://a.example/x", mk(9)), mk(9)); err != nil {
		t.Errorf("9 prior requests: policy returned %v; want nil", err)
	}
	if err := CredentialRedirectPolicy(hop(t, "https://a.example/x", mk(10)), mk(10)); err == nil {
		t.Errorf("10 prior requests: policy returned nil; want the 10-redirect stop")
	}
}
