package fetch

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

func TestRequireHTTPS(t *testing.T) {
	cases := []struct {
		url  string
		want bool // refused
	}{
		{"https://example.org/releases", false},
		{"http://example.org/releases", true},
		{"HTTP://example.org/", true},
		{"http://127.0.0.1:8080/fixture", false},
		{"http://localhost/fixture", false},
		{"http://[::1]/fixture", false},
		{"ftp://example.org/", false},
	}
	for _, tc := range cases {
		err := RequireHTTPS(tc.url)
		if got := errors.Is(err, ErrInsecureURL); got != tc.want {
			t.Errorf("RequireHTTPS(%q) = %v, want refused=%v", tc.url, err, tc.want)
		}
	}
}

func TestPublicHTTPSGuard_CheckURL(t *testing.T) {
	cases := []struct {
		url  string
		want bool // refused
	}{
		{"https://cdn.example.org/DaVinci_Resolve.zip", false},
		{"http://cdn.example.org/DaVinci_Resolve.zip", true},
		{"https://localhost/x", true},
		{"https://api.localhost/x", true},
		{"https://127.0.0.1/x", true},
		{"https://10.0.0.5/x", true},
		{"https://192.168.1.1/x", true},
		{"https://169.254.169.254/latest/meta-data", true},
		{"https://[::1]/x", true},
		{"https://[fe80::1]/x", true},
		{"https://0.0.0.0/x", true},
		{"https://93.184.215.14/x", false},
	}
	for _, tc := range cases {
		u, err := url.Parse(tc.url)
		if err != nil {
			t.Fatal(err)
		}
		err = publicHTTPSGuard{}.checkURL(u)
		if got := err != nil; got != tc.want {
			t.Errorf("checkURL(%q) = %v, want refused=%v", tc.url, err, tc.want)
		}
	}
}

// TestParseDownloadURL_RefusesNonPublicTargets: the vendor's reply names the
// download; the production guard refuses one aimed at this machine or a
// metadata service before any request is made.
func TestParseDownloadURL_RefusesNonPublicTargets(t *testing.T) {
	for _, body := range []string{"http://169.254.169.254/latest/meta-data", `"https://127.0.0.1:2375/containers/json"`} {
		if _, err := parseDownloadURL(body, "text/plain"); !errors.Is(err, ErrAuthFetchFailed) {
			t.Errorf("parseDownloadURL(%q) = %v, want ErrAuthFetchFailed", body, err)
		}
	}
	if got, err := parseDownloadURL("https://cdn.example.org/f.zip", "text/plain"); err != nil || got != "https://cdn.example.org/f.zip" {
		t.Errorf("a public https URL was refused: %q, %v", got, err)
	}
}

// TestDownloadClient_RefusesAPrivateAddressAtDial: the URL check only sees a
// name; the dialer sees what the name resolved to. "localhost.test-dial"
// stands in for a public-looking name that resolves to loopback.
func TestDownloadClient_RefusesAPrivateAddressAtDial(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("local service"))
	}))
	t.Cleanup(srv.Close)
	t.Setenv("HTTP_PROXY", "")
	t.Setenv("HTTPS_PROXY", "")
	t.Setenv("NO_PROXY", "")

	target, _ := url.Parse(srv.URL + "/payload")
	client := downloadClient(target, 0, nil)
	resp, err := client.Get(target.String())
	if resp != nil {
		_ = resp.Body.Close()
	}
	if err == nil || !strings.Contains(err.Error(), "loopback, private or link-local") {
		t.Fatalf("download from %s: err = %v, want the dialer to refuse the loopback address", srv.URL, err)
	}
}

// TestGuardedDownloadRedirect_RefusesAHopIntoAPrivateNetwork: a public URL
// that redirects to a private address is refused at the hop.
func TestGuardedDownloadRedirect_RefusesAHopIntoAPrivateNetwork(t *testing.T) {
	first, _ := http.NewRequest(http.MethodGet, "https://cdn.example.org/f.zip", nil)
	hop, _ := http.NewRequest(http.MethodGet, "https://10.1.2.3/f.zip", nil)
	if err := guardedDownloadRedirect(hop, []*http.Request{first}); !errors.Is(err, ErrAuthFetchFailed) {
		t.Errorf("redirect into 10.1.2.3: err = %v, want ErrAuthFetchFailed", err)
	}
}
