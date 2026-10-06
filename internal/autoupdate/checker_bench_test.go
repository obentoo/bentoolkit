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

	"github.com/obentoo/bentoolkit/internal/autoupdate/registry"
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
	return newCheckerOverDistinctURLs(tb, numPkgs, sleepingRateLimiter{d: perPkg}, concurrency)
}

// newCheckerOverDistinctURLs builds the same Checker as newSpeedupChecker with
// the per-package wait supplied by limiter.
func newCheckerOverDistinctURLs(tb testing.TB, numPkgs int, limiter httpRateLimiter, concurrency int) *Checker {
	tb.Helper()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		json.NewEncoder(w).Encode(map[string]string{"version": "1.0.0"})
	}))
	tb.Cleanup(server.Close)

	tmpDir := tb.TempDir()
	overlayDir := filepath.Join(tmpDir, "overlay")
	configDir := filepath.Join(tmpDir, "config")

	packages := make(map[string]registry.PackageConfig, numPkgs)
	for i := 0; i < numPkgs; i++ {
		name := fmt.Sprintf("cat-%03d/pkg-%03d", i, i)
		packages[name] = registry.PackageConfig{
			// A DISTINCT path per package, which is what makes this fixture
			// measure parallelism rather than deduplication (S024-R2.1). The
			// per-package latency below is injected through the rate limiter,
			// and story 024's body cache lets only ONE read per distinct URL
			// reach the limiter at all — so with every package sharing a single
			// URL, 49 of 50 would skip the injected latency entirely and the
			// serial baseline would collapse from ~5s to ~100ms, reporting a
			// 1.0x speedup for a checker that parallelises perfectly.
			//
			// Distinct URLs are also the shape production actually has: the
			// live registry holds 230 distinct URLs across 411 records.
			URL:    fmt.Sprintf("%s/pkg-%03d", server.URL, i),
			Parser: "json",
			Path:   "version",
		}
		createBenchEbuild(tb, overlayDir, name, "0.9.0")
	}

	checker, err := NewChecker(overlayDir,
		WithConfigDir(configDir),
		WithPackagesConfig(&registry.PackagesConfig{Packages: packages}),
		WithRateLimiter(limiter),
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
		batch := checker.CheckAll(b.Context(), true)
		if total := len(batch.Items) + len(batch.Failures); total != numPkgs {
			b.Fatalf("CheckAll produced %d results, want %d", total, numPkgs)
		}
	}
}

// barrierRateLimiter holds every package check at an overlapBarrier, so a test
// can see how many checks CheckAll keeps in flight at once.
type barrierRateLimiter struct{ b *overlapBarrier }

func (l barrierRateLimiter) WaitHTTP(context.Context, string) error {
	l.b.arrive()
	return nil
}

// TestBenchmarkSpeedup is the CI gate (DoD #10) on what the speedup comes from:
// at concurrency 10, CheckAll over 50 packages keeps exactly 10 package checks
// in flight at once. Each check is held at a barrier until 10 have arrived, so
// the run cannot finish unless the overlap happens, and no eleventh check can
// arrive while the ten are held.
//
// It used to require a 4x wall-clock speedup over a serial run. That ratio
// depends on the machine: under -race with every package's tests running at
// once it measured 2.9x in the CI container, against 9.4x on an idle host.
// BenchmarkCheckAll_Speedup still reports the wall-clock figure.
func TestBenchmarkSpeedup(t *testing.T) {
	const numPkgs = 50
	const concurrency = 10

	b := newOverlapBarrier(t)
	checker := newCheckerOverDistinctURLs(t, numPkgs, barrierRateLimiter{b: b}, concurrency)

	done := make(chan struct{})
	var batch BatchResult[CheckResult]
	go func() {
		defer close(done)
		batch = checker.CheckAll(t.Context(), true)
	}()

	var peak int64
	b.openWhen(t, fmt.Sprintf("%d package checks in flight at once", concurrency), func() (bool, string) {
		peak = b.arrived.Load()
		return peak >= concurrency, fmt.Sprintf("%d arrived", peak)
	})
	waitReturned(t, "CheckAll", done)

	if peak != concurrency {
		t.Errorf("%d package checks were in flight at once, want exactly the concurrency %d", peak, concurrency)
	}
	if total := len(batch.Items) + len(batch.Failures); total != numPkgs {
		t.Fatalf("CheckAll produced %d results, want %d", total, numPkgs)
	}
}
