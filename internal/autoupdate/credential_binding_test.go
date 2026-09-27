package autoupdate

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// Story 052, sub-task 1.2 — S052-R1.1..R1.3, R1.5..R1.8: a header credential
// reaches only the hosts it is bound to, and GetWithHeadersContext refuses
// before any network I/O when it is not.
//
// These tests are behaviour-level: they drive GetWithHeadersContext (no
// package scope, S052-R1.5) and observe what reaches the transport. The scoped
// BENTOO_* rule (S052-R1.4, url/base_url hosts) is exercised through the
// checker in checker_credential_binding_test.go (sub-task 1.3).

// bindingTransport stands in for the network: it records every request that
// would have left the process, keyed by URL, and answers 200 "1.2.3".
type bindingTransport struct {
	mu   sync.Mutex
	reqs []*http.Request
}

func (bt *bindingTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	bt.mu.Lock()
	bt.reqs = append(bt.reqs, req.Clone(context.Background()))
	bt.mu.Unlock()
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{},
		Body:       io.NopCloser(strings.NewReader("1.2.3")),
		Request:    req,
	}, nil
}

func (bt *bindingTransport) sent() []*http.Request {
	bt.mu.Lock()
	defer bt.mu.Unlock()
	return append([]*http.Request(nil), bt.reqs...)
}

func newBindingClient(t *testing.T) (*RetryableHTTPClient, *bindingTransport) {
	t.Helper()
	bt := &bindingTransport{}
	c := NewRetryableHTTPClient()
	c.SetHTTPClient(&http.Client{Transport: bt})
	c.SetDelayFunc(func(time.Duration) {})
	return c, bt
}

// expectRefused asserts the request was refused with ErrCredentialHostMismatch,
// that the message names header, variable and host, and that nothing left the
// process.
func expectRefused(t *testing.T, rawURL, header, value, variable string) {
	t.Helper()
	c, bt := newBindingClient(t)
	resp, err := c.GetWithHeadersContext(context.Background(), rawURL, map[string]string{header: value})
	if resp != nil && resp.Body != nil {
		_ = resp.Body.Close()
	}
	if err == nil {
		t.Fatalf("%s with %s=%q was sent; want a refusal", rawURL, header, value)
	}
	if !errors.Is(err, ErrCredentialHostMismatch) {
		t.Errorf("err = %v; want errors.Is(err, ErrCredentialHostMismatch)", err)
	}
	host := mustHostname(t, rawURL)
	for _, needle := range []string{header, variable, host} {
		if !strings.Contains(err.Error(), needle) {
			t.Errorf("refusal %q does not name %q", err.Error(), needle)
		}
	}
	if n := len(bt.sent()); n != 0 {
		t.Errorf("%d request(s) reached the transport; a refusal must precede any network I/O", n)
	}
}

// expectSent asserts the request went out once and carried want in header.
func expectSent(t *testing.T, rawURL, header, value, want string) {
	t.Helper()
	c, bt := newBindingClient(t)
	resp, err := c.GetWithHeadersContext(context.Background(), rawURL, map[string]string{header: value})
	if err != nil {
		t.Fatalf("%s with %s=%q was refused or failed: %v", rawURL, header, value, err)
	}
	_ = resp.Body.Close()
	sent := bt.sent()
	if len(sent) != 1 {
		t.Fatalf("transport saw %d requests, want 1", len(sent))
	}
	if got := sent[0].Header.Get(header); got != want {
		t.Errorf("%s received %s = %q, want %q", rawURL, header, got, want)
	}
}

func mustHostname(t *testing.T, rawURL string) string {
	t.Helper()
	u, err := url.Parse(rawURL)
	if err != nil {
		t.Fatalf("parse %q: %v", rawURL, err)
	}
	return u.Hostname()
}

func TestCheckCredentialBinding_GitHubHosts(t *testing.T) {
	t.Setenv("GITHUB_TOKEN", "ghp_example")

	// Hostile first: every way the rule could wrongly let the token out.
	for _, rawURL := range []string{
		"http://api.github.com/repos/o/r/releases",      // right host, cleartext
		"https://evil.api.github.com/repos/o/r",         // subdomain of a bound host
		"https://api.github.com.evil.example/repos/o/r", // suffix lookalike
		"https://github.com.evil.example/o/r",           // suffix lookalike
		"https://gist.github.com/o",                     // a GitHub host outside the set
		"https://127.0.0.1/latest",                      // the reproduction's listener
		"https://evil.example/latest",                   // the audit's example
	} {
		t.Run("refused "+rawURL, func(t *testing.T) {
			expectRefused(t, rawURL, "X-Api-Key", "${GITHUB_TOKEN}", "GITHUB_TOKEN")
		})
	}

	// Converse: the token must still reach each bound host — including the
	// spellings that differ only in case or port (S052-R1.7).
	for _, rawURL := range []string{
		"https://api.github.com/repos/o/r/releases",
		"https://github.com/o/r/releases.atom",
		"https://codeload.github.com/o/r/tar.gz/v1",
		"https://objects.githubusercontent.com/x",
		"https://raw.githubusercontent.com/o/r/main/VERSION",
		"https://API.GitHub.com/repos/o/r",
		"https://api.github.com:8443/repos/o/r",
	} {
		t.Run("sent "+rawURL, func(t *testing.T) {
			expectSent(t, rawURL, "Authorization", "token ${GITHUB_TOKEN}", "token ghp_example")
		})
	}
}

func TestCheckCredentialBinding_GitLabHost(t *testing.T) {
	t.Setenv("GITLAB_TOKEN", "glpat-example")
	t.Setenv("GITHUB_TOKEN", "ghp_example")

	for _, rawURL := range []string{
		"http://gitlab.com/api/v4/projects/1",          // cleartext
		"https://gitlab.example.com/api/v4/projects/1", // self-hosted: must use BENTOO_*
		"https://www.gitlab.com/api/v4/projects/1",     // subdomain
		"https://gitlab.com.evil.example/api/v4",       // suffix lookalike
		"https://api.github.com/repos/o/r",             // the OTHER vendor's host
	} {
		t.Run("GITLAB_TOKEN refused "+rawURL, func(t *testing.T) {
			expectRefused(t, rawURL, "Private-Token", "${GITLAB_TOKEN}", "GITLAB_TOKEN")
		})
	}
	// The converse binding: GITHUB_TOKEN may not ride to gitlab.com either.
	t.Run("GITHUB_TOKEN refused on gitlab.com", func(t *testing.T) {
		expectRefused(t, "https://gitlab.com/api/v4/projects/1", "Private-Token", "${GITHUB_TOKEN}", "GITHUB_TOKEN")
	})

	for _, rawURL := range []string{
		"https://gitlab.com/api/v4/projects/1/releases",
		"https://GitLab.com:443/api/v4/projects/1",
	} {
		t.Run("sent "+rawURL, func(t *testing.T) {
			expectSent(t, rawURL, "Private-Token", "${GITLAB_TOKEN}", "glpat-example")
		})
	}
}

// Without a package scope (S052-R1.5) the request host IS the package host, so
// a BENTOO_* credential goes to it — by hostname only, plain http included.
func TestCheckCredentialBinding_BentooOwnHosts(t *testing.T) {
	t.Setenv("BENTOO_MY_TOKEN", "own-secret")
	t.Setenv("GITHUB_TOKEN", "ghp_example")

	for _, rawURL := range []string{
		"https://downloads.vendor.example/latest",
		"http://internal.vendor.example:8080/latest",
	} {
		t.Run("sent "+rawURL, func(t *testing.T) {
			expectSent(t, rawURL, "X-Api-Key", "${BENTOO_MY_TOKEN}", "own-secret")
		})
	}

	// Hostile: a permitted BENTOO_* reference must not launder a GITHUB_TOKEN
	// reference that sits beside it, in the same value or in another header.
	t.Run("a GITHUB_TOKEN beside a BENTOO_ reference in one value is refused", func(t *testing.T) {
		expectRefused(t, "https://downloads.vendor.example/latest",
			"X-Api-Key", "${BENTOO_MY_TOKEN}:${GITHUB_TOKEN}", "GITHUB_TOKEN")
	})
	t.Run("a GITHUB_TOKEN in a second header is refused", func(t *testing.T) {
		c, bt := newBindingClient(t)
		resp, err := c.GetWithHeadersContext(context.Background(), "https://downloads.vendor.example/latest",
			map[string]string{"X-Api-Key": "${BENTOO_MY_TOKEN}", "Authorization": "Bearer ${GITHUB_TOKEN}"})
		if resp != nil && resp.Body != nil {
			_ = resp.Body.Close()
		}
		if !errors.Is(err, ErrCredentialHostMismatch) {
			t.Fatalf("err = %v; want ErrCredentialHostMismatch", err)
		}
		if !strings.Contains(err.Error(), "Authorization") || !strings.Contains(err.Error(), "GITHUB_TOKEN") {
			t.Errorf("refusal %q should name Authorization and GITHUB_TOKEN", err)
		}
		if n := len(bt.sent()); n != 0 {
			t.Errorf("%d request(s) reached the transport", n)
		}
	})
}

// S052-R1.6: the refusal does not depend on the machine's environment.
func TestCheckCredentialBinding_UnsetVariableStillRefused(t *testing.T) {
	t.Run("GITHUB_TOKEN unset", func(t *testing.T) {
		t.Setenv("GITHUB_TOKEN", "placeholder")
		if err := os.Unsetenv("GITHUB_TOKEN"); err != nil {
			t.Fatal(err)
		}
		expectRefused(t, "https://evil.example/latest", "X-Api-Key", "${GITHUB_TOKEN}", "GITHUB_TOKEN")
	})
	t.Run("GITHUB_TOKEN empty", func(t *testing.T) {
		t.Setenv("GITHUB_TOKEN", "")
		expectRefused(t, "https://evil.example/latest", "X-Api-Key", "${GITHUB_TOKEN}", "GITHUB_TOKEN")
	})
	t.Run("GITLAB_TOKEN unset over http", func(t *testing.T) {
		t.Setenv("GITLAB_TOKEN", "placeholder")
		if err := os.Unsetenv("GITLAB_TOKEN"); err != nil {
			t.Fatal(err)
		}
		expectRefused(t, "http://gitlab.com/api/v4/projects/1", "Private-Token", "${GITLAB_TOKEN}", "GITLAB_TOKEN")
	})
	// Converse: unset on a BOUND host is not a refusal — it passes through
	// literally with the existing Warn (S052-R9.1).
	t.Run("GITHUB_TOKEN unset on api.github.com is sent literally", func(t *testing.T) {
		t.Setenv("GITHUB_TOKEN", "placeholder")
		if err := os.Unsetenv("GITHUB_TOKEN"); err != nil {
			t.Fatal(err)
		}
		expectSent(t, "https://api.github.com/repos/o/r", "X-Api-Key", "${GITHUB_TOKEN}", "${GITHUB_TOKEN}")
	})
}

// S052-R1.8: a reference in a header that is NOT expansion-eligible carries no
// credential, so it is not refused.
func TestCheckCredentialBinding_UnlistedHeaderNotChecked(t *testing.T) {
	t.Setenv("GITHUB_TOKEN", "ghp_example")

	t.Run("X-Custom carrying ${GITHUB_TOKEN} to a foreign host is sent literally with a Warn", func(t *testing.T) {
		lc := captureWarnLogs(t)
		expectSent(t, "https://evil.example/latest", "X-Custom", "${GITHUB_TOKEN}", "${GITHUB_TOKEN}")
		found := false
		for _, line := range lc.all() {
			if strings.Contains(line, "X-Custom") && strings.Contains(line, "GITHUB_TOKEN") {
				found = true
			}
		}
		if !found {
			t.Errorf("expected the existing denied-header Warn naming X-Custom and GITHUB_TOKEN, got %v", lc.all())
		}
	})
	t.Run("a denied variable in an allow-listed header is not refused", func(t *testing.T) {
		t.Setenv("HOME_SECRET", "x")
		expectSent(t, "https://evil.example/latest", "X-Api-Key", "${HOME_SECRET}", "${HOME_SECRET}")
	})
	// Converse: the same reference moved into an allow-listed header is refused.
	t.Run("the same reference in X-Api-Key is refused", func(t *testing.T) {
		expectRefused(t, "https://evil.example/latest", "X-Api-Key", "${GITHUB_TOKEN}", "GITHUB_TOKEN")
	})
}

// The reproduction, over a real socket: the listener on 127.0.0.1 must receive
// nothing, through both the context and the convenience entry points.
func TestGetWithHeadersContext_RefusesBeforeNetwork(t *testing.T) {
	t.Setenv("GITHUB_TOKEN", "ghp_example")
	t.Setenv("BENTOO_MY_TOKEN", "own-secret")

	var hits atomic.Int64
	var mu sync.Mutex
	var gotKey []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		mu.Lock()
		gotKey = append(gotKey, r.Header.Get("X-Api-Key"))
		mu.Unlock()
		_, _ = w.Write([]byte("1.2.3"))
	}))
	defer srv.Close()

	client := NewRetryableHTTPClient()
	client.SetDelayFunc(func(time.Duration) {})
	leak := map[string]string{"X-Api-Key": "${GITHUB_TOKEN}"}

	for name, call := range map[string]func() (*http.Response, error){
		"GetWithHeadersContext": func() (*http.Response, error) {
			return client.GetWithHeadersContext(context.Background(), srv.URL+"/latest", leak)
		},
		"GetWithHeaders": func() (*http.Response, error) { return client.GetWithHeaders(srv.URL+"/latest", leak) },
	} {
		t.Run(name, func(t *testing.T) {
			resp, err := call()
			if resp != nil && resp.Body != nil {
				_ = resp.Body.Close()
			}
			if !errors.Is(err, ErrCredentialHostMismatch) {
				t.Fatalf("err = %v; want ErrCredentialHostMismatch", err)
			}
			for _, needle := range []string{"X-Api-Key", "GITHUB_TOKEN", "127.0.0.1"} {
				if !strings.Contains(err.Error(), needle) {
					t.Errorf("refusal %q does not name %q", err, needle)
				}
			}
			if strings.Contains(err.Error(), "ghp_example") {
				t.Errorf("refusal %q contains the token value", err)
			}
		})
	}
	if n := hits.Load(); n != 0 {
		t.Errorf("the listener received %d request(s); want 0 — the refusal must precede any I/O", n)
	}

	// Converse over the same socket: a BENTOO_* credential is delivered to the
	// request's own host (S052-R1.5).
	resp, err := client.GetWithHeadersContext(context.Background(), srv.URL+"/own",
		map[string]string{"X-Api-Key": "${BENTOO_MY_TOKEN}"})
	if err != nil {
		t.Fatalf("BENTOO_ credential to its own host was refused or failed: %v", err)
	}
	_ = resp.Body.Close()
	mu.Lock()
	defer mu.Unlock()
	if len(gotKey) != 1 || gotKey[0] != "own-secret" {
		t.Errorf("listener received X-Api-Key %v; want exactly [own-secret]", gotKey)
	}
}
