package autoupdate

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/obentoo/bentoolkit/internal/autoupdate/fetch"
)

// =============================================================================
// Task T14 / R10 — Rate-limit gate on the HTTP hot path
// =============================================================================

// recordingRateLimiter is a test double for httpRateLimiter. It records every
// host WaitHTTP was asked to gate, optionally blocks until the context is
// cancelled, and optionally returns a configured error. It is safe for
// concurrent use so it can be exercised under the -race detector.
type recordingRateLimiter struct {
	mu       sync.Mutex
	hosts    []string
	calls    atomic.Int64
	block    bool  // when true, WaitHTTP blocks until ctx is Done
	failWith error // when non-nil (and not blocking), WaitHTTP returns this

	// blocking, when non-nil, is closed the first time WaitHTTP starts
	// blocking, so a test can cancel a wait that is genuinely in progress.
	blocking     chan struct{}
	blockingOnce sync.Once
}

// WaitHTTP records the host and applies the configured behaviour.
func (m *recordingRateLimiter) WaitHTTP(ctx context.Context, domain string) error {
	m.calls.Add(1)
	m.mu.Lock()
	m.hosts = append(m.hosts, domain)
	m.mu.Unlock()

	if m.block {
		if m.blocking != nil {
			m.blockingOnce.Do(func() { close(m.blocking) })
		}
		<-ctx.Done()
		return ctx.Err()
	}
	return m.failWith
}

// recordedHosts returns a copy of the hosts WaitHTTP was invoked with.
func (m *recordingRateLimiter) recordedHosts() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]string, len(m.hosts))
	copy(out, m.hosts)
	return out
}

// callCount returns the number of times WaitHTTP was invoked.
func (m *recordingRateLimiter) callCount() int64 { return m.calls.Load() }

// newRateLimitTestChecker builds a Checker wired to a single package whose URL
// is pkgURL, with the supplied options applied. It mirrors newContextTestChecker
// but lets callers omit the HTTP-server requirement.
func newRateLimitTestChecker(t *testing.T, pkgURL string, opts ...CheckerOption) *Checker {
	t.Helper()

	tmpDir := t.TempDir()
	overlayDir := filepath.Join(tmpDir, "overlay")
	configDir := filepath.Join(tmpDir, "config")

	pkgName := "test-cat/test-pkg"
	createTestEbuild(t, overlayDir, pkgName, "1.0.0")

	config := &PackagesConfig{
		Packages: map[string]PackageConfig{
			pkgName: {URL: pkgURL, Parser: "json", Path: "version"},
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

// TestChecker_WithRateLimiterOption is a smoke test: the WithRateLimiter option
// installs the supplied limiter onto the Checker (sub-task 14.1).
func TestChecker_WithRateLimiterOption(t *testing.T) {
	mock := &recordingRateLimiter{}
	checker := newRateLimitTestChecker(t, "http://example.com", WithRateLimiter(mock))

	if checker.rateLimiter != mock {
		t.Fatalf("WithRateLimiter did not install the supplied limiter: got %v, want %v",
			checker.rateLimiter, mock)
	}
}

// TestChecker_WithRateLimiterOption_RejectsNil verifies a nil limiter is
// rejected by the option (the field must never be nil after construction).
func TestChecker_WithRateLimiterOption_RejectsNil(t *testing.T) {
	err := WithRateLimiter(nil)(&Checker{})
	if err == nil {
		t.Fatal("expected WithRateLimiter(nil) to return an error, got nil")
	}
}

// TestChecker_DefaultRateLimiter verifies that NewChecker installs a default
// rate limiter when WithRateLimiter is not supplied, so rateLimiter is never
// nil after construction (sub-task 14.2, R10.3).
func TestChecker_DefaultRateLimiter(t *testing.T) {
	checker := newRateLimitTestChecker(t, "http://example.com")

	if checker.rateLimiter == nil {
		t.Fatal("NewChecker left rateLimiter nil; a default limiter must be installed")
	}
	if _, ok := checker.rateLimiter.(*fetch.RateLimiter); !ok {
		t.Errorf("default rateLimiter has type %T, want *RateLimiter", checker.rateLimiter)
	}
}

// TestFetchContent_ParseHostFailure_FailsOpen verifies that a URL which fails
// url.Parse causes fetchContent to FAIL OPEN (R10.1): a Warn line is emitted
// and the rate-limit wait is skipped (the HTTP request is still attempted)
// rather than the fetch being aborted before the request.
func TestFetchContent_ParseHostFailure_FailsOpen(t *testing.T) {
	logs := captureWarnLogs(t)
	mock := &recordingRateLimiter{}

	// ":bad-url:" fails url.Parse with "missing protocol scheme".
	const malformedURL = ":bad-url:"
	checker := newRateLimitTestChecker(t, malformedURL, WithRateLimiter(mock))
	checker.log = logs.logger()

	_, err := checker.fetchContent(t.Context(), malformedURL, nil, fetch.CredentialScope{}, checker.operationTimeout(nil))

	// The fetch proceeds to the HTTP layer (which itself rejects the malformed
	// URL), so an error is expected — but it must NOT be the rate-limiter wait
	// error: failing open means the rate-limit wait was skipped, not that the
	// fetch was aborted.
	if err == nil {
		t.Fatal("expected an error from fetchContent with a malformed URL, got nil")
	}
	if strings.Contains(err.Error(), "rate limiter wait") {
		t.Errorf("fetch should fail open (skip the wait), but it aborted on the wait: %v", err)
	}

	// Fail-open means WaitHTTP must NOT have been consulted.
	if got := mock.callCount(); got != 0 {
		t.Errorf("WaitHTTP was called %d time(s) for an unparseable URL; expected fail-open (0)", got)
	}

	// A Warn line must have been emitted identifying the unparseable URL.
	lines := logs.all()
	if len(lines) == 0 {
		t.Fatal("expected a Warn line for the unparseable URL, got none")
	}
	foundWarn := false
	for _, line := range lines {
		if strings.Contains(line, "rate limiter") && strings.Contains(line, malformedURL) {
			foundWarn = true
			break
		}
	}
	if !foundWarn {
		t.Errorf("no Warn line mentioned the unparseable URL %q; got lines: %v", malformedURL, lines)
	}
}

// TestFetchContent_CallsWaitHTTP verifies that fetchContent gates on the rate
// limiter before issuing the HTTP request and passes the URL's host (R10.1).
func TestFetchContent_CallsWaitHTTP(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, `{"version":"2.0.0"}`)
	}))
	defer server.Close()

	mock := &recordingRateLimiter{}
	checker := newRateLimitTestChecker(t, server.URL, WithRateLimiter(mock))

	if _, err := checker.fetchContent(t.Context(), server.URL, nil, fetch.CredentialScope{}, checker.operationTimeout(nil)); err != nil {
		t.Fatalf("fetchContent returned an unexpected error: %v", err)
	}

	hosts := mock.recordedHosts()
	if len(hosts) != 1 {
		t.Fatalf("expected WaitHTTP to be called exactly once, got %d call(s): %v", len(hosts), hosts)
	}

	// The host recorded by the limiter must match the server's host:port.
	wantHost := strings.TrimPrefix(server.URL, "http://")
	if hosts[0] != wantHost {
		t.Errorf("WaitHTTP was asked to gate host %q, want %q", hosts[0], wantHost)
	}
}

// TestFetchContent_RateLimitContextCancelled verifies that when the rate-limit
// wait is cancelled by the context, fetchContent returns the context error and
// issues NO HTTP request (R10.2): the httptest request counter stays at 0.
func TestFetchContent_RateLimitContextCancelled(t *testing.T) {
	var requestCount atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requestCount.Add(1)
		fmt.Fprint(w, `{"version":"2.0.0"}`)
	}))
	defer server.Close()

	// A limiter that blocks until the context is cancelled.
	mock := &recordingRateLimiter{block: true, blocking: make(chan struct{})}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	checker := newRateLimitTestChecker(t, server.URL,
		WithRateLimiter(mock),
		WithOpTimeout(10*time.Second), // generous: the cancel, not the deadline, ends the wait
	)

	type fetchOutcome struct {
		err     error
		elapsed time.Duration
	}
	outcome := make(chan fetchOutcome, 1)
	go func() {
		start := time.Now()
		_, err := checker.fetchContent(ctx, server.URL, nil, fetch.CredentialScope{}, checker.operationTimeout(nil))
		outcome <- fetchOutcome{err: err, elapsed: time.Since(start)}
	}()

	// Cancel the parent context once the fetch is blocked in the rate-limit wait.
	select {
	case <-mock.blocking:
	case got := <-outcome:
		t.Fatalf("fetchContent returned (err=%v) before it blocked in the rate-limit wait", got.err)
	case <-time.After(5 * time.Second):
		t.Fatalf("fetchContent never blocked in the rate-limit wait within 5s (WaitHTTP calls=%d)", mock.callCount())
	}
	cancel()

	var got fetchOutcome
	select {
	case got = <-outcome:
	case <-time.After(10 * time.Second):
		t.Fatal("fetchContent did not return within 10s of the parent context being cancelled")
	}
	err, elapsed := got.err, got.elapsed

	if err == nil {
		t.Fatal("expected an error when the rate-limit wait is cancelled, got nil")
	}
	if !errors.Is(err, context.Canceled) {
		t.Errorf("expected fetchContent error to wrap context.Canceled, got %v", err)
	}
	if elapsed > 2*time.Second {
		t.Errorf("fetchContent took %v after cancellation; expected a prompt return", elapsed)
	}

	// The defining assertion: no HTTP request was issued.
	if got := requestCount.Load(); got != 0 {
		t.Errorf("an HTTP request was issued despite the cancelled rate-limit wait: count=%d", got)
	}
}
