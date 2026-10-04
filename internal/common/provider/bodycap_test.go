package provider

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
		io.WriteString(w, "[")
		written := int64(1)
		for written < size-1 {
			n := min(int64(len(chunk)), size-1-written)
			if _, err := io.WriteString(w, chunk[:n]); err != nil {
				return
			}
			written += n
		}
		io.WriteString(w, "]")
	}))
	t.Cleanup(srv.Close)
	return srv
}

const overCap = 64 << 20 // 64 MiB, the reproduction's body

func newCappedGitHubProvider(t *testing.T, baseURL string) *GitHubProvider {
	t.Helper()
	p, err := NewGitHubProvider(&RepositoryInfo{Name: "test", Provider: "github", URL: "test/repo"})
	if err != nil {
		t.Fatalf("NewGitHubProvider: %v", err)
	}
	p.BaseURL = baseURL
	p.CacheDir = ""
	return p
}

func newCappedGitLabProvider(t *testing.T, baseURL string) *GitLabProvider {
	t.Helper()
	p, err := NewGitLabProvider(&RepositoryInfo{Name: "test", Provider: "gitlab", URL: "test/repo"})
	if err != nil {
		t.Fatalf("NewGitLabProvider: %v", err)
	}
	p.BaseURL = baseURL
	p.CacheDir = ""
	return p
}

// TestGitHubProviderBodyCapped pins R5.1 for the GitHub provider's success and
// quoted-error bodies. Hostile half: a body of exactly MaxBodyBytes is within
// the cap and must NOT be reported as too large.
func TestGitHubProviderBodyCapped(t *testing.T) {
	t.Run("success body over the cap", func(t *testing.T) {
		p := newCappedGitHubProvider(t, hugeBodyServer(t, http.StatusOK, overCap).URL)
		_, err := p.GetPackageVersions(t.Context(), "app-misc", "foo")
		if !errors.Is(err, httputil.ErrResponseTooLarge) {
			t.Errorf("err = %v, want errors.Is(err, httputil.ErrResponseTooLarge)", err)
		}
	})
	t.Run("error body over the cap", func(t *testing.T) {
		p := newCappedGitHubProvider(t, hugeBodyServer(t, http.StatusInternalServerError, overCap).URL)
		_, err := p.GetPackageVersions(t.Context(), "app-misc", "foo")
		if !errors.Is(err, httputil.ErrResponseTooLarge) {
			t.Errorf("err = %.200v, want errors.Is(err, httputil.ErrResponseTooLarge)", err)
		}
	})
	t.Run("success body exactly at the cap", func(t *testing.T) {
		p := newCappedGitHubProvider(t, hugeBodyServer(t, http.StatusOK, httputil.MaxBodyBytes).URL)
		if _, err := p.GetPackageVersions(t.Context(), "app-misc", "foo"); err != nil {
			t.Errorf("a %d-byte body (exactly the cap) failed: %.200v", httputil.MaxBodyBytes, err)
		}
	})
	t.Run("error body exactly at the cap", func(t *testing.T) {
		p := newCappedGitHubProvider(t, hugeBodyServer(t, http.StatusInternalServerError, httputil.MaxBodyBytes).URL)
		_, err := p.GetPackageVersions(t.Context(), "app-misc", "foo")
		if !errors.Is(err, ErrAPIError) || errors.Is(err, httputil.ErrResponseTooLarge) {
			t.Errorf("err = %.200v, want ErrAPIError and not ErrResponseTooLarge", err)
		}
	})
}

// TestGitHubProviderRateLimitBodyCapped pins R5.1 for the rate-limit body read
// by GetRateLimitInfo.
func TestGitHubProviderRateLimitBodyCapped(t *testing.T) {
	p := newCappedGitHubProvider(t, hugeBodyServer(t, http.StatusOK, overCap).URL)
	_, _, err := p.GetRateLimitInfo(t.Context())
	if !errors.Is(err, httputil.ErrResponseTooLarge) {
		t.Errorf("err = %v, want errors.Is(err, httputil.ErrResponseTooLarge)", err)
	}
}

// TestGitLabProviderBodyCapped pins R5.1 for the GitLab provider's success and
// quoted-error bodies, with the same exactly-at-cap hostile half.
func TestGitLabProviderBodyCapped(t *testing.T) {
	t.Run("success body over the cap", func(t *testing.T) {
		p := newCappedGitLabProvider(t, hugeBodyServer(t, http.StatusOK, overCap).URL)
		_, err := p.GetPackageVersions(t.Context(), "app-misc", "foo")
		if !errors.Is(err, httputil.ErrResponseTooLarge) {
			t.Errorf("err = %v, want errors.Is(err, httputil.ErrResponseTooLarge)", err)
		}
	})
	t.Run("error body over the cap", func(t *testing.T) {
		p := newCappedGitLabProvider(t, hugeBodyServer(t, http.StatusBadGateway, overCap).URL)
		_, err := p.GetPackageVersions(t.Context(), "app-misc", "foo")
		if !errors.Is(err, httputil.ErrResponseTooLarge) {
			t.Errorf("err = %.200v, want errors.Is(err, httputil.ErrResponseTooLarge)", err)
		}
	})
	t.Run("success body exactly at the cap", func(t *testing.T) {
		p := newCappedGitLabProvider(t, hugeBodyServer(t, http.StatusOK, httputil.MaxBodyBytes).URL)
		if _, err := p.GetPackageVersions(t.Context(), "app-misc", "foo"); err != nil {
			t.Errorf("a %d-byte body (exactly the cap) failed: %.200v", httputil.MaxBodyBytes, err)
		}
	})
}
