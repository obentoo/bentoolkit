package fetch

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// Story 052, sub-task 2.4 — S052-R7.2 (hostile half of R7.1, GREEN today): a
// form-leg 307/308 that stays on fetch_url's hostname is followed with method
// and body intact. It guards against a fix that refuses every 307/308.
func TestFormRedirectOnSameHostIsFollowed(t *testing.T) {
	for _, code := range []int{http.StatusTemporaryRedirect, http.StatusPermanentRedirect} {
		t.Run(fmt.Sprintf("HTTP %d", code), func(t *testing.T) {
			var mu sync.Mutex
			var method, body string
			vendor := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/dl" {
					b, _ := io.ReadAll(r.Body)
					mu.Lock()
					method, body = r.Method, string(b)
					mu.Unlock()
					w.Header().Set("Content-Type", "application/octet-stream")
					_, _ = w.Write([]byte(strings.Repeat("x", 4096)))
					return
				}
				http.Redirect(w, r, "/dl", code)
			}))
			defer vendor.Close()

			spec, _, err := ParseAuthFetchSpec(map[string]string{
				MetaFetchURL:      vendor.URL + "/submit",
				MetaFetchFilename: "x-{version}.zip",
				MetaFetchForm:     "product=Example",
			})
			if err != nil {
				t.Fatalf("parse: %v", err)
			}
			if _, err := spec.FetchDistfile(context.Background(), "1.0", t.TempDir()); err != nil {
				t.Fatalf("same-host %d was not followed: %v", code, err)
			}
			mu.Lock()
			defer mu.Unlock()
			if method != http.MethodPost || !strings.Contains(body, "product=Example") {
				t.Errorf("/dl received %s with body %q; want POST carrying product=Example", method, body)
			}
		})
	}
}
