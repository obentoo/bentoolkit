package autoupdate

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

// Story 052, sub-task 2.4 — S052-R7.1: a form-leg 307/308 (method and body
// preserved) to a hostname other than fetch_url's is refused, and nothing
// reaches that host. The converse, S052-R7.2, is pinned green today by
// TestFormRedirectOnSameHostIsFollowed in authfetch_form_redirect_samehost_test.go.
func TestFormRedirectToAnotherHostIsRefused(t *testing.T) {
	for _, code := range []int{http.StatusTemporaryRedirect, http.StatusPermanentRedirect} {
		t.Run(fmt.Sprintf("HTTP %d", code), func(t *testing.T) {
			var otherHits atomic.Int64
			other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				otherHits.Add(1)
				w.Header().Set("Content-Type", "application/octet-stream")
				_, _ = w.Write([]byte(strings.Repeat("x", 4096)))
			}))
			defer other.Close()
			otherURL := strings.Replace(other.URL, "127.0.0.1", "localhost", 1)

			vendor := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				http.Redirect(w, r, otherURL+"/dl", code)
			}))
			defer vendor.Close()

			spec, _, err := parseAuthFetchSpec(map[string]string{
				metaFetchURL:      vendor.URL + "/submit",
				metaFetchFilename: "x-{version}.zip",
				metaFetchForm:     "product=Example",
			})
			if err != nil {
				t.Fatalf("parse: %v", err)
			}

			_, err = spec.fetchDistfile(context.Background(), "1.0", t.TempDir())
			if !errors.Is(err, ErrAuthFetchFailed) {
				t.Fatalf("err = %v; want ErrAuthFetchFailed — the form body must not move to another host", err)
			}
			if !strings.Contains(err.Error(), "localhost") {
				t.Errorf("err %q does not name the new host", err)
			}
			if n := otherHits.Load(); n != 0 {
				t.Errorf("the other host received %d request(s) carrying the form; want 0", n)
			}
		})
	}
}
