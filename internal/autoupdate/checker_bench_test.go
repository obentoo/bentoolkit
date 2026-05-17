package autoupdate

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// =============================================================================
// Task T15 / sub-task 15.8 — CheckAll speedup benchmark + deterministic gate
// =============================================================================

// sleepingRateLimiter is an httpRateLimiter that simply sleeps for a fixed
// duration on every WaitHTTP call. It models a uniform per-package latency so a
// CheckAll run's wall-clock is dominated by that latency, making the parallel
// speedup measurable and deterministic.
type sleepingRateLimiter struct {
	d time.Duration
}

// WaitHTTP sleeps for the configured duration, honouring context cancellation.
func (s sleepingRateLimiter) WaitHTTP(ctx context.Context, _ string) error {
	select {
	case <-time.After(s.d):
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// newSpeedupChecker builds a Checker over numPkgs packages whose per-package
// wall-clock is dominated by a perPkg sleep injected through the rate limiter,
// at the given concurrency.
//
// Every package points at a single local httptest.Server that answers
// instantly, so the only meaningful latency in CheckPackage is the perPkg
// sleep the rate limiter performs before each (local, sub-millisecond) HTTP
// request. The server is registered for cleanup via tb.Cleanup so it is closed
// after the benchmark/test, satisfying goleak.
func newSpeedupChecker(tb testing.TB, numPkgs int, perPkg time.Duration, concurrency int) *Checker {
	tb.Helper()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		json.NewEncoder(w).Encode(map[string]string{"version": "1.0.0"}) //nolint:errcheck
	}))
	tb.Cleanup(server.Close)

	tmpDir := tb.TempDir()
	overlayDir := filepath.Join(tmpDir, "overlay")
	configDir := filepath.Join(tmpDir, "config")

	packages := make(map[string]PackageConfig, numPkgs)
	for i := 0; i < numPkgs; i++ {
		name := fmt.Sprintf("cat-%03d/pkg-%03d", i, i)
		packages[name] = PackageConfig{
			URL:    server.URL,
			Parser: "json",
			Path:   "version",
		}
		createBenchEbuild(tb, overlayDir, name, "0.9.0")
	}

	checker, err := NewChecker(overlayDir,
		WithConfigDir(configDir),
		WithPackagesConfig(&PackagesConfig{Packages: packages}),
		WithRateLimiter(sleepingRateLimiter{d: perPkg}),
		WithConcurrency(concurrency),
	)
	if err != nil {
		tb.Fatalf("NewChecker failed: %v", err)
	}
	return checker
}

// createBenchEbuild writes a minimal ebuild for a package; it mirrors the test
// helper but takes a testing.TB so it is usable from benchmarks.
func createBenchEbuild(tb testing.TB, overlayDir, pkgName, version string) {
	tb.Helper()
	parts := splitPackageName(pkgName)
	if len(parts) != 2 {
		tb.Fatalf("invalid package name: %s", pkgName)
	}
	pkgDir := filepath.Join(overlayDir, parts[0], parts[1])
	if err := os.MkdirAll(pkgDir, 0o755); err != nil {
		tb.Fatalf("failed to create package dir: %v", err)
	}
	ebuildPath := filepath.Join(pkgDir, parts[1]+"-"+version+".ebuild")
	content := "# Test ebuild\nEAPI=8\nDESCRIPTION=\"Test package\"\n" +
		"HOMEPAGE=\"https://example.com\"\nSRC_URI=\"\"\nLICENSE=\"MIT\"\n" +
		"SLOT=\"0\"\nKEYWORDS=\"~amd64\"\n"
	if err := os.WriteFile(ebuildPath, []byte(content), 0o644); err != nil {
		tb.Fatalf("failed to write ebuild: %v", err)
	}
}

// BenchmarkCheckAll_Speedup measures CheckAll wall-clock at the default
// concurrency. With a 100ms per-package latency injected through the rate
// limiter, a serial run of 50 packages would take ~5s; the benchmark reports
// how much the parallel implementation reduces that.
func BenchmarkCheckAll_Speedup(b *testing.B) {
	const numPkgs = 50
	const perPkg = 100 * time.Millisecond

	checker := newSpeedupChecker(b, numPkgs, perPkg, DefaultConcurrency)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		batch := checker.CheckAll(true)
		if total := len(batch.Items) + len(batch.Failures); total != numPkgs {
			b.Fatalf("CheckAll produced %d results, want %d", total, numPkgs)
		}
	}
}

// TestBenchmarkSpeedup is a deterministic CI gate (DoD #10). It injects a fixed
// 100ms per-package latency and measures the CheckAll wall-clock for a serial
// run (concurrency=1) versus a parallel run (concurrency=10) over 50 packages.
// The parallel run MUST be at least 4x faster, or the test fails.
func TestBenchmarkSpeedup(t *testing.T) {
	const numPkgs = 50
	const perPkg = 100 * time.Millisecond
	const minSpeedup = 4.0

	// Baseline: fully serial.
	serialChecker := newSpeedupChecker(t, numPkgs, perPkg, 1)
	serialStart := time.Now()
	serialBatch := serialChecker.CheckAll(true)
	serialElapsed := time.Since(serialStart)
	if total := len(serialBatch.Items) + len(serialBatch.Failures); total != numPkgs {
		t.Fatalf("serial CheckAll produced %d results, want %d", total, numPkgs)
	}

	// Parallel: concurrency 10.
	parallelChecker := newSpeedupChecker(t, numPkgs, perPkg, 10)
	parallelStart := time.Now()
	parallelBatch := parallelChecker.CheckAll(true)
	parallelElapsed := time.Since(parallelStart)
	if total := len(parallelBatch.Items) + len(parallelBatch.Failures); total != numPkgs {
		t.Fatalf("parallel CheckAll produced %d results, want %d", total, numPkgs)
	}

	if parallelElapsed <= 0 {
		t.Fatalf("parallel run reported a non-positive elapsed time: %v", parallelElapsed)
	}
	speedup := float64(serialElapsed) / float64(parallelElapsed)
	t.Logf("serial=%v parallel=%v speedup=%.2fx (numPkgs=%d, perPkg=%v, concurrency=10)",
		serialElapsed, parallelElapsed, speedup, numPkgs, perPkg)

	if speedup < minSpeedup {
		t.Fatalf("parallel speedup %.2fx is below the required %.1fx (serial=%v, parallel=%v)",
			speedup, minSpeedup, serialElapsed, parallelElapsed)
	}
}
