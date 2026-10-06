package fetch

import (
	"context"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"
)

// Story 052, sub-task 1.4 — S052-R5.1 (and the kept half, S052-R9.3): the
// automatic GitHub token is attached over https only.

// tokenTransport records the Authorization header of every request that
// reaches it and answers 200 without touching the network, so hostnames that
// cannot be served locally (api.github.com) can be exercised.
type tokenTransport struct {
	mu   sync.Mutex
	auth map[string][]string // URL -> Authorization values seen
}

func (tt *tokenTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	tt.mu.Lock()
	if tt.auth == nil {
		tt.auth = map[string][]string{}
	}
	tt.auth[req.URL.String()] = append(tt.auth[req.URL.String()], req.Header.Get("Authorization"))
	tt.mu.Unlock()
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{},
		Body:       io.NopCloser(strings.NewReader("{}")),
		Request:    req,
	}, nil
}

func (tt *tokenTransport) seen(u string) []string {
	tt.mu.Lock()
	defer tt.mu.Unlock()
	return append([]string(nil), tt.auth[u]...)
}

func TestIsGitHubAPIURL_HTTPSOnly(t *testing.T) {
	const token = "ghp_automatic"

	cases := []struct {
		name     string
		url      string
		wantAuth string
	}{
		// Hostile first: the cleartext URL on the right host.
		{"http api.github.com gets no token", "http://api.github.com/repos/o/r/releases", ""},
		{"lookalike suffix host gets no token", "https://api.github.com.evil.example/repos/o/r", ""},
		{"lookalike prefix host gets no token", "https://notapi.github.com/repos/o/r", ""},
		// Benign: https on the API host keeps the token (S052-R9.3).
		{"https api.github.com gets the bearer token", "https://api.github.com/repos/o/r/releases", "Bearer " + token},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tt := &tokenTransport{}
			client := NewRetryableHTTPClient()
			client.SetHTTPClient(&http.Client{Transport: tt})
			client.SetDelayFunc(func(time.Duration) {})
			client.SetGitHubToken(token)

			resp, err := client.GetWithHeadersContext(context.Background(), tc.url, nil)
			if err != nil {
				t.Fatalf("GetWithHeadersContext(%s): %v", tc.url, err)
			}
			_ = resp.Body.Close()

			got := tt.seen(tc.url)
			if len(got) != 1 {
				t.Fatalf("transport saw %d requests for %s, want 1", len(got), tc.url)
			}
			if got[0] != tc.wantAuth {
				t.Errorf("Authorization sent to %s = %q, want %q", tc.url, got[0], tc.wantAuth)
			}
		})
	}
}
