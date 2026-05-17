package autoupdate

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"
)

// slowServer returns an httptest.Server whose handler blocks until the request
// context is cancelled (client disconnect) or maxHold elapses. Blocking on
// r.Context().Done() lets server.Close() return promptly when the test cancels
// the client context, so the handler goroutine does not leak under goleak.
func slowServer(t *testing.T, maxHold time.Duration) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-time.After(maxHold):
			w.WriteHeader(http.StatusOK)
		}
	}))
}

// newContextTestChecker builds a Checker wired to srvURL for a single package,
// with the supplied context-spine options applied.
func newContextTestChecker(t *testing.T, srvURL string, opts ...CheckerOption) *Checker {
	t.Helper()

	tmpDir := t.TempDir()
	overlayDir := filepath.Join(tmpDir, "overlay")
	configDir := filepath.Join(tmpDir, "config")

	pkgName := "test-cat/test-pkg"
	createTestEbuild(t, overlayDir, pkgName, "1.0.0")

	config := &PackagesConfig{
		Packages: map[string]PackageConfig{
			pkgName: {URL: srvURL, Parser: "json", Path: "version"},
		},
	}

	allOpts := append([]CheckerOption{
		WithConfigDir(configDir),
		WithPackagesConfig(config),
	}, opts...)

	checker, err := NewChecker(overlayDir, allOpts...)
	if err != nil {
		t.Fatalf("NewChecker failed: %v", err)
	}
	return checker
}

// TestChecker_ContextCancelled verifies that cancelling the Checker's parent
// context mid-fetch aborts the in-flight HTTP request promptly (R3.1/R3.2).
func TestChecker_ContextCancelled(t *testing.T) {
	// Server holds each request for up to 10s; the test cancels well before.
	server := slowServer(t, 10*time.Second)
	defer server.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	checker := newContextTestChecker(t, server.URL,
		WithContext(ctx),
		// Generous op timeout so the *cancellation* (not the deadline) is what
		// ends the fetch.
		WithOpTimeout(10*time.Second),
	)

	// Cancel the parent context shortly after the fetch starts.
	go func() {
		time.Sleep(20 * time.Millisecond)
		cancel()
	}()

	start := time.Now()
	_, err := checker.fetchContent(server.URL)
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("expected an error from a cancelled fetch, got nil")
	}
	if elapsed > 100*time.Millisecond {
		t.Errorf("fetch took %v after cancellation, expected to return within ~100ms", elapsed)
	}
	if !errors.Is(err, context.Canceled) {
		t.Errorf("expected error to wrap context.Canceled, got %v", err)
	}
}

// TestChecker_ContextDeadlineExceeded verifies that a per-operation deadline
// expiring naturally aborts the fetch and the returned error satisfies
// errors.Is(err, context.DeadlineExceeded) (R3.2).
func TestChecker_ContextDeadlineExceeded(t *testing.T) {
	// Server holds each request for up to 10s so the short op timeout fires
	// first.
	server := slowServer(t, 10*time.Second)
	defer server.Close()

	checker := newContextTestChecker(t, server.URL,
		WithContext(context.Background()),
		// Tiny per-operation timeout: the WithTimeout deadline derived inside
		// fetchContent expires naturally while the request is in flight.
		WithOpTimeout(50*time.Millisecond),
	)

	start := time.Now()
	_, err := checker.fetchContent(server.URL)
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("expected an error from an expired deadline, got nil")
	}
	if elapsed > 2*time.Second {
		t.Errorf("fetch took %v, expected the 50ms deadline to abort it quickly", elapsed)
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("expected error to wrap context.DeadlineExceeded, got %v", err)
	}
}
