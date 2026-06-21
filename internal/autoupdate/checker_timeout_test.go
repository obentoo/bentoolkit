package autoupdate

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// TestDeriveOpTimeout verifies the per-operation budget is sized to fit every
// retry attempt: perReq×(MaxRetries+1) for the attempts, plus the cumulative
// backoff, plus one second of slack. For the default retry config (3 retries,
// 1s base, 4s cap) and a 30s per-request timeout that is 30×4 + (1+2+4) + 1.
func TestDeriveOpTimeout(t *testing.T) {
	rc := DefaultRetryConfig()
	got := deriveOpTimeout(30*time.Second, rc)
	want := 30*4*time.Second + (1+2+4)*time.Second + time.Second // 128s
	if got != want {
		t.Errorf("deriveOpTimeout(30s) = %v, want %v", got, want)
	}

	// The whole point is that the budget exceeds a single request's timeout, so
	// the retry attempts can actually run instead of the first one eating it all.
	if got <= 30*time.Second {
		t.Errorf("derived budget %v must exceed the per-request timeout 30s", got)
	}
}

// TestOperationTimeout checks the per-package override: a positive cfg.Timeout
// wins, while a nil or zero-Timeout config falls back to the global budget.
func TestOperationTimeout(t *testing.T) {
	checker := newContextTestChecker(t, "http://example.invalid", WithOpTimeout(40*time.Second))

	if got := checker.operationTimeout(nil); got != 40*time.Second {
		t.Errorf("operationTimeout(nil) = %v, want global 40s", got)
	}
	if got := checker.operationTimeout(&PackageConfig{}); got != 40*time.Second {
		t.Errorf("operationTimeout(zero cfg) = %v, want global 40s", got)
	}
	if got := checker.operationTimeout(&PackageConfig{Timeout: 90}); got != 90*time.Second {
		t.Errorf("operationTimeout(per-package 90) = %v, want 90s", got)
	}
}

// TestWithHTTPRequestTimeout_WiresAndDerives confirms NewChecker applies the
// per-request timeout to the HTTP client and derives the per-operation budget
// from it, and that an explicit WithOpTimeout is not overwritten.
func TestWithHTTPRequestTimeout_WiresAndDerives(t *testing.T) {
	checker := newContextTestChecker(t, "http://example.invalid",
		WithHTTPRequestTimeout(45*time.Second))

	if got := checker.httpClient.Config().Timeout; got != 45*time.Second {
		t.Errorf("client per-request timeout = %v, want 45s", got)
	}
	want := deriveOpTimeout(45*time.Second, checker.httpClient.Config())
	if checker.opTimeout != want {
		t.Errorf("derived opTimeout = %v, want %v", checker.opTimeout, want)
	}

	// An explicit budget must win over the derived one regardless of option order.
	explicit := newContextTestChecker(t, "http://example.invalid",
		WithHTTPRequestTimeout(45*time.Second), WithOpTimeout(7*time.Second))
	if explicit.opTimeout != 7*time.Second {
		t.Errorf("explicit WithOpTimeout overwritten: got %v, want 7s", explicit.opTimeout)
	}
	// ...but the per-request timeout is still applied to the client.
	if got := explicit.httpClient.Config().Timeout; got != 45*time.Second {
		t.Errorf("client per-request timeout = %v, want 45s", got)
	}
}

// TestWithHTTPRequestTimeout_NoOpOnNonPositive verifies a zero/negative value is
// a no-op so callers (e.g. the revive option builder) can wire it unconditionally.
func TestWithHTTPRequestTimeout_NoOpOnNonPositive(t *testing.T) {
	checker := newContextTestChecker(t, "http://example.invalid", WithHTTPRequestTimeout(0))

	if got := checker.httpClient.Config().Timeout; got != DefaultHTTPTimeout {
		t.Errorf("zero WithHTTPRequestTimeout changed client timeout to %v, want default %v",
			got, DefaultHTTPTimeout)
	}
	if checker.opTimeout != DefaultOpTimeout {
		t.Errorf("zero WithHTTPRequestTimeout changed opTimeout to %v, want default %v",
			checker.opTimeout, DefaultOpTimeout)
	}
}

// TestFetchContent_RetryRecoversAfterTimeout is the behavioural proof of the
// core fix: with a per-operation budget larger than the per-request timeout, a
// first attempt that exceeds the per-request timeout is retried and the second
// attempt succeeds. Before the fix the budget equalled the per-request timeout,
// so the first slow attempt consumed it and the fetch failed with "context
// deadline exceeded" without ever retrying.
func TestFetchContent_RetryRecoversAfterTimeout(t *testing.T) {
	var attempts int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if atomic.AddInt32(&attempts, 1) == 1 {
			// First attempt: block until the client's per-request timeout cancels
			// the request, forcing a timeout-classified failure.
			<-r.Context().Done()
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"version":"1.2.3"}`))
	}))
	defer server.Close()

	// Per-request cap of 150ms, no real backoff sleeps, and a 5s budget that
	// leaves ample room for the retry.
	client := NewRetryableHTTPClient()
	client.SetHTTPClient(&http.Client{Timeout: 150 * time.Millisecond})
	client.SetDelayFunc(func(time.Duration) {})

	checker := newContextTestChecker(t, server.URL,
		WithHTTPClient(client),
		WithOpTimeout(5*time.Second),
	)

	content, err := checker.fetchContent(server.URL, nil, checker.operationTimeout(nil))
	if err != nil {
		t.Fatalf("expected the retry to recover from the first timeout, got error: %v", err)
	}
	if !strings.Contains(string(content), "1.2.3") {
		t.Errorf("unexpected body %q, want it to contain the version", string(content))
	}
	if n := atomic.LoadInt32(&attempts); n < 2 {
		t.Errorf("expected at least 2 attempts (timeout then retry), got %d", n)
	}
}

// TestValidatePackageConfig_NegativeTimeout asserts a negative per-package
// timeout is rejected, while zero (the "use global" sentinel) is accepted.
func TestValidatePackageConfig_NegativeTimeout(t *testing.T) {
	bad := &PackageConfig{URL: "https://example.com", Parser: "regex", Pattern: "v(.+)", Timeout: -5}
	if err := ValidatePackageConfig("cat/pkg", bad); err == nil {
		t.Error("expected an error for a negative timeout, got nil")
	}

	ok := &PackageConfig{URL: "https://example.com", Parser: "regex", Pattern: "v(.+)", Timeout: 0}
	if err := ValidatePackageConfig("cat/pkg", ok); err != nil {
		t.Errorf("zero timeout should be valid (use global), got: %v", err)
	}
}
