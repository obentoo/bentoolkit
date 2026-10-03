package autoupdate

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// TestPerHostConcurrencyCapsInFlightRequests pins the cap: eight packages on one
// slow host, checked with a global concurrency of eight, never have more than
// the per-host limit in flight at once.
func TestPerHostConcurrencyCapsInFlightRequests(t *testing.T) {
	const limit = 2
	var inFlight, peak atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		n := inFlight.Add(1)
		for {
			p := peak.Load()
			if n <= p || peak.CompareAndSwap(p, n) {
				break
			}
		}
		time.Sleep(40 * time.Millisecond)
		inFlight.Add(-1)
		_, _ = w.Write([]byte(`{"version":"2.0.0"}`))
	}))
	t.Cleanup(srv.Close)

	tmpDir := t.TempDir()
	overlayDir := filepath.Join(tmpDir, "overlay")
	pkgs := map[string]PackageConfig{}
	for i := range 8 {
		pkg := fmt.Sprintf("test-cat/pkg%d", i)
		createTestEbuild(t, overlayDir, pkg, "1.0.0")
		// A distinct query per package so the body cache cannot fold them.
		pkgs[pkg] = PackageConfig{URL: fmt.Sprintf("%s/?p=%d", srv.URL, i), Parser: "json", Path: "version"}
	}
	c, err := NewChecker(overlayDir,
		WithConfigDir(filepath.Join(tmpDir, "config")),
		WithPackagesConfig(&PackagesConfig{Packages: pkgs}),
		WithRateLimiter(unlimitedRateLimiter()),
		WithConcurrency(8),
		WithPerHostConcurrency(limit),
	)
	if err != nil {
		t.Fatalf("NewChecker: %v", err)
	}

	res := c.CheckAll(true)
	if res.HasFailures() {
		t.Fatalf("CheckAll failures: %v", res.Failures)
	}
	if p := peak.Load(); p > limit {
		t.Errorf("peak in-flight requests to one host = %d, want <= %d", p, limit)
	}
}

func TestHostSlotsHonoursCancellation(t *testing.T) {
	h := newHostSlots(1)
	release, err := h.acquire(context.Background(), "https://example.org/a")
	if err != nil {
		t.Fatal(err)
	}
	defer release()

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if _, err := h.acquire(ctx, "https://example.org/b"); err == nil {
		t.Error("a second acquire on a full host returned without waiting for a slot")
	}
	if r, err := h.acquire(context.Background(), "https://other.example/"); err != nil {
		t.Errorf("another host was blocked: %v", err)
	} else {
		r()
	}
}

// countingLive counts evaluations per URL.
type countingLive struct {
	mu    sync.Mutex
	calls map[string]int
}

func (f *countingLive) Evaluate(_ context.Context, url, _ string, _ map[string]string) (string, error) {
	f.mu.Lock()
	f.calls[url]++
	f.mu.Unlock()
	time.Sleep(20 * time.Millisecond) // long enough for concurrent records to join
	return "26.8.2.1", nil
}

// TestScriptRecordsShareOneEvaluation pins the script dedup: libreoffice and
// libreoffice-l10n run the same script on the same page and must cost one
// browser navigation, while a different fragment is a different evaluation.
func TestScriptRecordsShareOneEvaluation(t *testing.T) {
	const stable = "https://ftp.fau.de/tdf/libreoffice/stable/#26.8"
	const older = "https://ftp.fau.de/tdf/libreoffice/stable/#26.2"
	live := &countingLive{calls: map[string]int{}}
	orig := newLiveEvaluator
	newLiveEvaluator = func(time.Duration) (liveEvaluator, error) { return live, nil }
	t.Cleanup(func() { newLiveEvaluator = orig })

	tmpDir := t.TempDir()
	overlayDir := filepath.Join(tmpDir, "overlay")
	pkgs := map[string]PackageConfig{
		"app-office/libreoffice":      {URL: stable, Parser: "script", Script: "pick()"},
		"app-office/libreoffice-l10n": {URL: stable, Parser: "script", Script: "pick()"},
		"app-office/libreoffice-bin":  {URL: stable, Parser: "script", Script: "pick()"},
		"app-office/libreoffice-old":  {URL: older, Parser: "script", Script: "pick()"},
	}
	for pkg := range pkgs {
		createTestEbuild(t, overlayDir, pkg, "26.8.1.1")
	}
	c, err := NewChecker(overlayDir,
		WithConfigDir(filepath.Join(tmpDir, "config")),
		WithPackagesConfig(&PackagesConfig{Packages: pkgs}),
		WithRateLimiter(unlimitedRateLimiter()),
		WithConcurrency(4),
	)
	if err != nil {
		t.Fatalf("NewChecker: %v", err)
	}
	if res := c.CheckAll(true); res.HasFailures() {
		t.Fatalf("CheckAll failures: %v", res.Failures)
	}

	live.mu.Lock()
	defer live.mu.Unlock()
	if n := live.calls[stable]; n != 1 {
		t.Errorf("same script on the same page evaluated %d times, want 1", n)
	}
	if n := live.calls[older]; n != 1 {
		t.Errorf("a different fragment evaluated %d times, want 1", n)
	}
}
