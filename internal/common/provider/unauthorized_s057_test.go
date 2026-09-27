package provider

// Authored for story 057, sub-task 1.1 — R1.8, R6.7, R6.8.
//
// A 401 must become recognisable as an auth failure WITHOUT ceasing to be what
// it is today: an ErrAPIError whose text names the status. The hostile halves
// come first: the statuses that must NOT be read as auth (GitHub's 403 stays a
// rate limit, a 500 stays a plain API error, a 404 stays not-found).
//
// RED ON ARRIVAL: provider.ErrUnauthorized does not exist.

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func s057ProviderStatusServer(t *testing.T, status int) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if status == http.StatusForbidden {
			w.Header().Set("X-RateLimit-Reset", "1790000000")
		}
		w.WriteHeader(status)
		w.Write([]byte(`{"message":"denied"}`)) //nolint:errcheck
	}))
	t.Cleanup(srv.Close)
	return srv
}

type s057StatusCase struct {
	name    string
	status  int
	must    []error
	mustNot []error
	text    string
}

func s057CheckStatus(t *testing.T, tc s057StatusCase, err error) {
	t.Helper()
	if err == nil {
		t.Fatalf("HTTP %d returned no error", tc.status)
	}
	for _, want := range tc.must {
		if !errors.Is(err, want) {
			t.Errorf("HTTP %d returned %q, which does not match %q (R1.8/R6.7/R6.8)", tc.status, err, want)
		}
	}
	for _, not := range tc.mustNot {
		if errors.Is(err, not) {
			t.Errorf("HTTP %d returned %q, which matches %q: that status is a different failure and must not be read as it", tc.status, err, not)
		}
	}
	if tc.text != "" && !strings.Contains(err.Error(), tc.text) {
		t.Errorf("HTTP %d returned %q, whose text no longer names %q (R1.8, R6.7)", tc.status, err, tc.text)
	}
}

// TestGitHubProvider_Unauthorized is R1.8 on GitHubProvider.
func TestGitHubProvider_Unauthorized(t *testing.T) {
	cases := []s057StatusCase{
		// Hostile: the statuses that would be wrongly collapsed into auth.
		{name: "a 403 stays a rate limit, not auth", status: http.StatusForbidden,
			must: []error{ErrRateLimit}, mustNot: []error{ErrUnauthorized, ErrNotFound}},
		{name: "a 500 stays a plain API error, not auth", status: http.StatusInternalServerError,
			must: []error{ErrAPIError}, mustNot: []error{ErrUnauthorized, ErrRateLimit, ErrNotFound}, text: "500"},
		{name: "a 404 stays not found, not auth", status: http.StatusNotFound,
			must: []error{ErrNotFound}, mustNot: []error{ErrUnauthorized, ErrAPIError, ErrRateLimit}},
		// The rule itself: a 401 is auth AND still an API error naming 401.
		{name: "a 401 is auth and still an API error", status: http.StatusUnauthorized,
			must: []error{ErrUnauthorized, ErrAPIError}, mustNot: []error{ErrRateLimit, ErrNotFound}, text: "401"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := s057ProviderStatusServer(t, tc.status)
			prov, err := NewGitHubProvider(&RepositoryInfo{Name: "s057", URL: "test/repo", Token: "bad-token"})
			if err != nil {
				t.Fatalf("NewGitHubProvider failed: %v", err)
			}
			prov.BaseURL = srv.URL
			prov.CacheDir = ""
			_, err = prov.GetPackageVersions("app-misc", "hello")
			s057CheckStatus(t, tc, err)
		})
	}
}

// TestGitLabProvider_Unauthorized is R1.8 on GitLabProvider.
func TestGitLabProvider_Unauthorized(t *testing.T) {
	cases := []s057StatusCase{
		{name: "a 429 stays a rate limit, not auth", status: http.StatusTooManyRequests,
			must: []error{ErrRateLimit}, mustNot: []error{ErrUnauthorized, ErrNotFound}},
		{name: "a 500 stays a plain API error, not auth", status: http.StatusInternalServerError,
			must: []error{ErrAPIError}, mustNot: []error{ErrUnauthorized, ErrRateLimit, ErrNotFound}, text: "500"},
		{name: "a 404 stays not found, not auth", status: http.StatusNotFound,
			must: []error{ErrNotFound}, mustNot: []error{ErrUnauthorized, ErrAPIError, ErrRateLimit}},
		{name: "a 401 is auth and still an API error", status: http.StatusUnauthorized,
			must: []error{ErrUnauthorized, ErrAPIError}, mustNot: []error{ErrRateLimit, ErrNotFound}, text: "401"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := s057ProviderStatusServer(t, tc.status)
			prov, err := NewGitLabProvider(&RepositoryInfo{Name: "s057", Provider: "gitlab", URL: "test/repo", Token: "bad-token"})
			if err != nil {
				t.Fatalf("NewGitLabProvider failed: %v", err)
			}
			prov.BaseURL = srv.URL
			prov.CacheDir = ""
			_, err = prov.GetPackageVersions("app-misc", "hello")
			s057CheckStatus(t, tc, err)
		})
	}
}
