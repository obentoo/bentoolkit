package autoupdate

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync"
	"testing"

	"github.com/sony/gobreaker"
)

// TestFallbackKeepsRecordFields pins that the fallback probe is the same probe
// pointed at another source: the custom User-Agent reaches it, a credential
// header does not, and series filters select = max per candidate instead of
// the out-of-line maximum failing the whole check.
func TestFallbackKeepsRecordFields(t *testing.T) {
	primary := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("not json"))
	}))
	t.Cleanup(primary.Close)

	var (
		mu       sync.Mutex
		gotUA    string
		gotAuthz string
	)
	fallback := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		gotUA, gotAuthz = r.Header.Get("User-Agent"), r.Header.Get("Authorization")
		mu.Unlock()
		_, _ = w.Write([]byte("bind-1.8.5.tar.gz bind-1.9.0.tar.gz"))
	}))
	t.Cleanup(fallback.Close)

	tmpDir := t.TempDir()
	overlayDir := filepath.Join(tmpDir, "overlay")
	pkg := "net-dns/bind-tools"
	createTestEbuild(t, overlayDir, pkg, "1.8.3")

	cfg := PackageConfig{
		URL:             primary.URL,
		Parser:          "json",
		Path:            "version",
		Headers:         map[string]string{"User-Agent": "bentoo-test/1", "Authorization": "Bearer literal"},
		Series:          `^1\.8\.`,
		Select:          "max",
		FallbackURL:     fallback.URL,
		FallbackParser:  "regex",
		FallbackPattern: `bind-([0-9]+\.[0-9]+\.[0-9]+)\.tar`,
	}
	c := holdChecker(t, overlayDir, filepath.Join(tmpDir, "config"), pkg, cfg)

	result, err := c.CheckPackage(pkg, true)
	if err != nil {
		t.Fatalf("CheckPackage: %v", err)
	}
	if result.UpstreamVersion != "1.8.5" {
		t.Errorf("UpstreamVersion = %q, want %q (series must filter the fallback's candidates)", result.UpstreamVersion, "1.8.5")
	}
	mu.Lock()
	defer mu.Unlock()
	if gotUA != "bentoo-test/1" {
		t.Errorf("fallback User-Agent = %q, want the record's %q", gotUA, "bentoo-test/1")
	}
	if gotAuthz != "" {
		t.Errorf("a credential header reached the fallback host: Authorization = %q", gotAuthz)
	}
}

func TestFallbackConfigKeepsTimeoutAndSuffix(t *testing.T) {
	cfg := &PackageConfig{Timeout: 90, Suffix: "_beta", SuffixWhen: `^2\.`, FallbackParser: "regex"}
	got := fallbackConfig(cfg, "x")
	if got.Timeout != 90 || got.Suffix != "_beta" || got.SuffixWhen != `^2\.` {
		t.Errorf("fallbackConfig dropped fields: %+v", *got)
	}
	if got.Parser != "regex" || got.Pattern != "x" {
		t.Errorf("fallback parser/pattern not swapped in: %+v", *got)
	}
}

func TestIsUpstreamUnreachable(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
		want bool
	}{
		{"retries exhausted", fmt.Errorf("%w: tls: EOF", ErrMaxRetriesExceeded), true},
		{"request timeout", fmt.Errorf("%w: i/o timeout", ErrRequestTimeout), true},
		{"breaker open", fmt.Errorf("circuit breaker open: %w", gobreaker.ErrOpenState), true},
		{"operation deadline", fmt.Errorf("request to x deadline exceeded: %w", context.DeadlineExceeded), true},
		{"wrapped by the extraction chain", fmt.Errorf("all version extraction methods failed: %w",
			fmt.Errorf("%w: connection reset", ErrMaxRetriesExceeded)), true},
		{"host does not exist", fmt.Errorf("%w: %w", ErrMaxRetriesExceeded,
			&net.DNSError{Err: "no such host", Name: "dowloads.isc.org", IsNotFound: true}), false},
		{"cancelled by the operator", fmt.Errorf("%w: %w", ErrMaxRetriesExceeded, context.Canceled), false},
		{"no version in the page", ErrNoVersionFound, false},
		{"plain error", errors.New("HTTP 404"), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := isUpstreamUnreachable(tc.err); got != tc.want {
				t.Errorf("isUpstreamUnreachable(%v) = %v, want %v", tc.err, got, tc.want)
			}
			wrapped := fetchFailure(tc.err)
			if !errors.Is(wrapped, ErrFetchFailed) {
				t.Error("fetchFailure lost ErrFetchFailed")
			}
			if errors.Is(wrapped, ErrUpstreamUnreachable) != tc.want {
				t.Errorf("errors.Is(fetchFailure, ErrUpstreamUnreachable) = %v, want %v", !tc.want, tc.want)
			}
		})
	}
}
