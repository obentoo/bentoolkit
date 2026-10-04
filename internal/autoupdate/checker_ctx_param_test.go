package autoupdate

// Story 059, sub-task 2.1 (R1.2, R3.1): one Checker serves many calls, and each
// call's upstream fetch is bounded by THAT call's context. The Checker holds no
// context of its own, so a cancelled call cannot poison a later live one, and a
// live call cannot lend its liveness to a later cancelled one.
//
// Both directions are asserted on ONE Checker. A Checker that accepted the ctx
// parameter and ignored it would let the cancelled call succeed; one that kept
// the first context it saw would fail the later live call.

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

const checkerCtx059Pkg = "test-cat/ctx-pkg"

func checkerCtx059Upstream(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"version":"2.0.0"}`))
	}))
	t.Cleanup(srv.Close)
	return srv
}

func newCheckerCtx059(t *testing.T, upstreamURL string, pkgs ...string) *Checker {
	t.Helper()
	tmp := t.TempDir()
	overlayDir := filepath.Join(tmp, "overlay")
	cfg := map[string]PackageConfig{}
	for _, pkg := range pkgs {
		createTestEbuild(t, overlayDir, pkg, "1.0.0")
		cfg[pkg] = PackageConfig{URL: upstreamURL, Parser: "json", Path: "version"}
	}
	c, err := NewChecker(overlayDir,
		WithConfigDir(filepath.Join(tmp, "config")),
		WithPackagesConfig(&PackagesConfig{Packages: cfg}),
		WithRateLimiter(unlimitedRateLimiter()),
		WithOpTimeout(10*time.Second),
	)
	if err != nil {
		t.Fatalf("NewChecker: %v", err)
	}
	return c
}

func checkerCtx059Cancelled() context.Context {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	return ctx
}

func assertCheckerCtx059Live(t *testing.T, res *CheckResult, err error) {
	t.Helper()
	if err != nil {
		t.Fatalf("CheckPackage with a live context: %v", err)
	}
	if res == nil || res.UpstreamVersion != "2.0.0" {
		t.Fatalf("CheckPackage with a live context = %+v; want UpstreamVersion 2.0.0", res)
	}
}

func assertCheckerCtx059Cancelled(t *testing.T, err error) {
	t.Helper()
	if err == nil {
		t.Fatal("CheckPackage with a cancelled context succeeded; the fetch is not bounded by the call's own context (R3.1)")
	}
	if !errors.Is(err, context.Canceled) {
		t.Errorf("CheckPackage with a cancelled context: err = %v; want errors.Is(err, context.Canceled)", err)
	}
}

// checkerCtx059Key labels a context so a fake provider can tell which call's
// context reached it.
type checkerCtx059Key struct{}

// checkerCtx059Provider is a ::gentoo provider that records the label of every
// context GetPackageVersions receives.
type checkerCtx059Provider struct {
	mu     sync.Mutex
	labels []any
}

func (p *checkerCtx059Provider) GetPackageVersions(ctx context.Context, category, pkg string) ([]string, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.labels = append(p.labels, ctx.Value(checkerCtx059Key{}))
	return []string{"1.0.0"}, nil
}
func (p *checkerCtx059Provider) GetName() string   { return "ctx059" }
func (p *checkerCtx059Provider) SupportsAPI() bool { return true }
func (p *checkerCtx059Provider) Close() error      { return nil }

// TestCheckerEachCallIsBoundedByItsOwnContext pins R3.1 in both orders, and
// that FindRevivableOrphans hands each call's own context to the ::gentoo
// provider (R1.2).
func TestCheckerEachCallIsBoundedByItsOwnContext(t *testing.T) {
	upstream := checkerCtx059Upstream(t)

	t.Run("a cancelled first call does not fail a later live call", func(t *testing.T) {
		c := newCheckerCtx059(t, upstream.URL, checkerCtx059Pkg)
		_, err := c.CheckPackage(checkerCtx059Cancelled(), checkerCtx059Pkg, true)
		assertCheckerCtx059Cancelled(t, err)
		res, err := c.CheckPackage(context.Background(), checkerCtx059Pkg, true)
		assertCheckerCtx059Live(t, res, err)
	})

	t.Run("a live first call does not let a later cancelled call succeed", func(t *testing.T) {
		c := newCheckerCtx059(t, upstream.URL, checkerCtx059Pkg)
		res, err := c.CheckPackage(context.Background(), checkerCtx059Pkg, true)
		assertCheckerCtx059Live(t, res, err)
		_, err = c.CheckPackage(checkerCtx059Cancelled(), checkerCtx059Pkg, true)
		assertCheckerCtx059Cancelled(t, err)
	})

	t.Run("FindRevivableOrphans hands each call's context to the provider", func(t *testing.T) {
		const orphan = "app-editors/ctx-orphan"
		disabled := false
		c, err := NewChecker(t.TempDir(),
			WithConfigDir(t.TempDir()),
			WithPackagesConfig(&PackagesConfig{Packages: map[string]PackageConfig{
				orphan: {URL: upstream.URL, Parser: "json", Path: "version", Enabled: &disabled},
			}}),
			WithRateLimiter(unlimitedRateLimiter()),
		)
		if err != nil {
			t.Fatalf("NewChecker: %v", err)
		}
		prov := &checkerCtx059Provider{}
		for _, label := range []string{"first-scan", "second-scan"} {
			ctx := context.WithValue(context.Background(), checkerCtx059Key{}, label)
			got, err := c.FindRevivableOrphans(ctx, prov)
			if err != nil {
				t.Fatalf("FindRevivableOrphans(%s): %v", label, err)
			}
			if len(got) != 1 {
				t.Fatalf("FindRevivableOrphans(%s) = %d candidates; want 1 (upstream 2.0.0 > ::gentoo 1.0.0)", label, len(got))
			}
		}
		prov.mu.Lock()
		defer prov.mu.Unlock()
		want := []any{"first-scan", "second-scan"}
		if len(prov.labels) != len(want) {
			t.Fatalf("provider was asked %d time(s); want %d", len(prov.labels), len(want))
		}
		for i := range want {
			if prov.labels[i] != want[i] {
				t.Errorf("lookup %d carried context label %v; want %v (the scan must pass its own context)", i, prov.labels[i], want[i])
			}
		}
	})
}

// TestCheckAllIsBoundedByItsOwnContext pins R1.2/R3.1 for CheckAll on one
// Checker in both orders: a cancelled batch fails every package with
// context.Canceled, and a live batch on the same Checker checks every package.
func TestCheckAllIsBoundedByItsOwnContext(t *testing.T) {
	upstream := checkerCtx059Upstream(t)
	pkgs := []string{"test-cat/ctx-a", "test-cat/ctx-b"}

	assertCancelled := func(t *testing.T, b BatchResult[CheckResult]) {
		t.Helper()
		if len(b.Items) != 0 {
			t.Errorf("cancelled CheckAll produced %d item(s); want 0 (R3.1)", len(b.Items))
		}
		if len(b.Failures) != len(pkgs) {
			t.Fatalf("cancelled CheckAll recorded %d failure(s); want %d", len(b.Failures), len(pkgs))
		}
		for pkg, err := range b.Failures {
			if !errors.Is(err, context.Canceled) {
				t.Errorf("failure for %s = %v; want errors.Is(err, context.Canceled)", pkg, err)
			}
		}
	}
	assertLive := func(t *testing.T, b BatchResult[CheckResult]) {
		t.Helper()
		if len(b.Failures) != 0 {
			t.Errorf("live CheckAll recorded failures %v; want none", b.Failures)
		}
		if len(b.Items) != len(pkgs) {
			t.Errorf("live CheckAll produced %d item(s); want %d", len(b.Items), len(pkgs))
		}
	}

	t.Run("cancelled batch, then live batch", func(t *testing.T) {
		c := newCheckerCtx059(t, upstream.URL, pkgs...)
		assertCancelled(t, c.CheckAll(checkerCtx059Cancelled(), true))
		assertLive(t, c.CheckAll(context.Background(), true))
	})

	t.Run("live batch, then cancelled batch", func(t *testing.T) {
		c := newCheckerCtx059(t, upstream.URL, pkgs...)
		assertLive(t, c.CheckAll(context.Background(), true))
		assertCancelled(t, c.CheckAll(checkerCtx059Cancelled(), true))
	})
}
