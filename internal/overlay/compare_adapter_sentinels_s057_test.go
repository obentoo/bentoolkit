package overlay

// Authored for story 057, sub-task 1.2 — R1.9.
//
// The GitHub adapter must translate every client sentinel into the provider one
// (checked with errors.Is), and keep the client's Error() text byte for byte.
// Hostile first: a status must not be translated into a NEIGHBOURING sentinel,
// and a network failure must be translated into none.
//
// RED ON ARRIVAL: provider.ErrUnauthorized does not exist (sub-task 1.1); once it
// does, the 403 and 500 cases fail because the adapter passes github sentinels
// through untranslated.

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/obentoo/bentoolkit/internal/common/github"
	"github.com/obentoo/bentoolkit/internal/common/provider"
)

func TestGitHubAdapterTranslatesSentinels(t *testing.T) {
	cases := []struct {
		name    string
		status  int
		must    []error
		mustNot []error
	}{
		{"a 403 is a rate limit and nothing else", http.StatusForbidden,
			[]error{provider.ErrRateLimit}, []error{provider.ErrUnauthorized, provider.ErrNotFound, provider.ErrAPIError}},
		{"a 500 is an API error and not auth", http.StatusInternalServerError,
			[]error{provider.ErrAPIError}, []error{provider.ErrUnauthorized, provider.ErrRateLimit, provider.ErrNotFound}},
		{"a 401 is auth and an API error", http.StatusUnauthorized,
			[]error{provider.ErrUnauthorized, provider.ErrAPIError}, []error{provider.ErrRateLimit, provider.ErrNotFound}},
		{"a 404 is not found", http.StatusNotFound,
			[]error{provider.ErrNotFound}, []error{provider.ErrUnauthorized, provider.ErrRateLimit, provider.ErrAPIError}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if tc.status == http.StatusForbidden {
					w.Header().Set("X-RateLimit-Reset", "1790000000")
				}
				w.WriteHeader(tc.status)
				w.Write([]byte(`{"message":"no"}`))
			}))
			t.Cleanup(srv.Close)

			client := github.NewClient()
			client.BaseURL = srv.URL
			client.CacheDir = ""

			_, clientErr := client.GetPackageVersions(context.Background(), "app-misc", "hello")
			if clientErr == nil {
				t.Fatalf("the client returned no error for HTTP %d; the fixture is wrong", tc.status)
			}
			_, err := (&githubProviderAdapter{client: client}).GetPackageVersions(context.Background(), "app-misc", "hello")
			if err == nil {
				t.Fatalf("the adapter returned no error for HTTP %d", tc.status)
			}
			for _, want := range tc.must {
				if !errors.Is(err, want) {
					t.Errorf("HTTP %d: the adapter returned %q, which does not match provider sentinel %q (R1.9)", tc.status, err, want)
				}
			}
			for _, not := range tc.mustNot {
				if errors.Is(err, not) {
					t.Errorf("HTTP %d: the adapter returned %q, which matches %q — a neighbouring failure (R1.9)", tc.status, err, not)
				}
			}
			if err.Error() != clientErr.Error() {
				t.Errorf("HTTP %d: the adapter rewrote the text\n got: %q\nwant: %q (R1.9: byte for byte)", tc.status, err.Error(), clientErr.Error())
			}
		})
	}

	t.Run("a network failure matches no provider sentinel and stays a net.Error", func(t *testing.T) {
		srv := httptest.NewServer(http.NotFoundHandler())
		url := srv.URL
		srv.Close()

		client := github.NewClient()
		client.BaseURL = url
		client.CacheDir = ""
		_, clientErr := client.GetPackageVersions(context.Background(), "app-misc", "hello")
		_, err := (&githubProviderAdapter{client: client}).GetPackageVersions(context.Background(), "app-misc", "hello")
		if err == nil || clientErr == nil {
			t.Fatalf("a closed server returned no error (client %v, adapter %v)", clientErr, err)
		}
		for _, not := range []error{provider.ErrNotFound, provider.ErrRateLimit, provider.ErrAPIError, provider.ErrUnauthorized} {
			if errors.Is(err, not) {
				t.Errorf("a refused connection was translated into %q; it is none of the API outcomes", not)
			}
		}
		var netErr net.Error
		if !errors.As(err, &netErr) {
			t.Errorf("a refused connection lost its net.Error through the adapter: %q (R1.3 needs it)", err)
		}
		if err.Error() != clientErr.Error() {
			t.Errorf("the adapter rewrote a network error\n got: %q\nwant: %q", err.Error(), clientErr.Error())
		}
	})
}
