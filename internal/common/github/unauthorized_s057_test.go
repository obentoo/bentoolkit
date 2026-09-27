package github

// Authored for story 057, sub-task 1.1 — R1.8, R6.7, R6.8 on github.Client.
//
// RED ON ARRIVAL: github.ErrUnauthorized does not exist.

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestGetPackageVersionsUnauthorized: a 401 matches ErrUnauthorized and
// ErrAPIError and names 401; the hostile statuses (403 rate limit, 500, 404)
// must not be read as auth.
func TestGetPackageVersionsUnauthorized(t *testing.T) {
	cases := []struct {
		name    string
		status  int
		must    []error
		mustNot []error
		text    string
	}{
		{"a 403 stays a rate limit, not auth", http.StatusForbidden, []error{ErrRateLimit}, []error{ErrUnauthorized, ErrNotFound}, ""},
		{"a 500 stays a plain API error, not auth", http.StatusInternalServerError, []error{ErrAPIError}, []error{ErrUnauthorized, ErrRateLimit, ErrNotFound}, "500"},
		{"a 404 stays not found, not auth", http.StatusNotFound, []error{ErrNotFound}, []error{ErrUnauthorized, ErrAPIError, ErrRateLimit}, ""},
		{"a 401 is auth and still an API error", http.StatusUnauthorized, []error{ErrUnauthorized, ErrAPIError}, []error{ErrRateLimit, ErrNotFound}, "401"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if tc.status == http.StatusForbidden {
					w.Header().Set("X-RateLimit-Reset", "1790000000")
				}
				w.WriteHeader(tc.status)
				w.Write([]byte(`{"message":"Bad credentials"}`)) //nolint:errcheck
			}))
			t.Cleanup(srv.Close)

			client := NewClient()
			client.BaseURL = srv.URL
			client.CacheDir = ""
			client.Token = "bad-token"

			_, err := client.GetPackageVersions("app-misc", "hello")
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
					t.Errorf("HTTP %d returned %q, which matches %q; it is a different failure", tc.status, err, not)
				}
			}
			if tc.text != "" && !strings.Contains(err.Error(), tc.text) {
				t.Errorf("HTTP %d returned %q, whose text no longer names %q (R1.8)", tc.status, err, tc.text)
			}
		})
	}
}
