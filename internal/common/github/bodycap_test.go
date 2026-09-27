package github

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/obentoo/bentoolkit/internal/common/httputil"
)

// hugeBodyServer answers with status and a body of `size` bytes, streamed in
// chunks; it stops as soon as the client hangs up.
func hugeBodyServer(t *testing.T, status int, size int64) *httptest.Server {
	t.Helper()
	chunk := strings.Repeat(" ", 64*1024)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		io.WriteString(w, "[") //nolint:errcheck // client may hang up
		written := int64(1)
		for written < size-1 {
			n := min(int64(len(chunk)), size-1-written)
			if _, err := io.WriteString(w, chunk[:n]); err != nil {
				return
			}
			written += n
		}
		io.WriteString(w, "]") //nolint:errcheck
	}))
	t.Cleanup(srv.Close)
	return srv
}

const overCap = 64 << 20 // 64 MiB, the reproduction's body

func newCappedClient(baseURL string) *Client {
	c := NewClient()
	c.BaseURL = baseURL
	c.CacheDir = ""
	return c
}

// TestGitHubClientBodyCapped pins R5.1 for the GitHub client's success and
// quoted-error bodies. Hostile half: exactly MaxBodyBytes is within the cap.
func TestGitHubClientBodyCapped(t *testing.T) {
	t.Run("success body over the cap", func(t *testing.T) {
		c := newCappedClient(hugeBodyServer(t, http.StatusOK, overCap).URL)
		_, err := c.GetPackageVersions(t.Context(), "app-misc", "foo")
		if !errors.Is(err, httputil.ErrResponseTooLarge) {
			t.Errorf("err = %v, want errors.Is(err, httputil.ErrResponseTooLarge)", err)
		}
	})
	t.Run("error body over the cap", func(t *testing.T) {
		c := newCappedClient(hugeBodyServer(t, http.StatusInternalServerError, overCap).URL)
		_, err := c.GetPackageVersions(t.Context(), "app-misc", "foo")
		if !errors.Is(err, httputil.ErrResponseTooLarge) {
			t.Errorf("err = %.200v, want errors.Is(err, httputil.ErrResponseTooLarge)", err)
		}
	})
	t.Run("success body exactly at the cap", func(t *testing.T) {
		c := newCappedClient(hugeBodyServer(t, http.StatusOK, httputil.MaxBodyBytes).URL)
		if _, err := c.GetPackageVersions(t.Context(), "app-misc", "foo"); err != nil {
			t.Errorf("a %d-byte body (exactly the cap) failed: %.200v", httputil.MaxBodyBytes, err)
		}
	})
}

// TestGitHubClientRateLimitBodyCapped pins R5.1 for GetRateLimitInfo.
func TestGitHubClientRateLimitBodyCapped(t *testing.T) {
	c := newCappedClient(hugeBodyServer(t, http.StatusOK, overCap).URL)
	_, _, err := c.GetRateLimitInfo()
	if !errors.Is(err, httputil.ErrResponseTooLarge) {
		t.Errorf("err = %v, want errors.Is(err, httputil.ErrResponseTooLarge)", err)
	}
}
