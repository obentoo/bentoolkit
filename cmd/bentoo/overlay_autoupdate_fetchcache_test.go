package main

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync/atomic"
	"testing"

	"github.com/obentoo/bentoolkit/internal/common/config"
)

// =============================================================================
// Story 024 / task 3.1 — the --no-fetch-cache escape hatch (S024-R7.1, R7.2)
// =============================================================================

// pinNoFetchCache pins the process-global flag for one test and restores it,
// so a test never leaks the un-deduplicated mode into the rest of the suite.
func pinNoFetchCache(t *testing.T, v bool) {
	t.Helper()
	orig := autoupdateNoFetchCache
	t.Cleanup(func() { autoupdateNoFetchCache = orig })
	autoupdateNoFetchCache = v
}

// runFetchCacheCheck drives the REAL --check path over a two-record registry in
// which both records declare the same URL, and returns how many requests the
// server actually received.
//
// The request count is the assertion, not the printed report: this flag exists
// to change how many requests go out and nothing else, so any weaker check
// (exit code, output text) would pass with the flag unwired.
func runFetchCacheCheck(t *testing.T, noFetchCache bool) int64 {
	t.Helper()

	var requests atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		fmt.Fprint(w, `{"version":"1.2.3"}`)
	}))
	t.Cleanup(server.Close)

	overlayDir := t.TempDir()
	configDir := t.TempDir()
	t.Setenv("HOME", t.TempDir()) // keep the run off the developer's real config

	pkgs := []string{"cat-a/pkg1", "cat-b/pkg2"}
	writeExitTestPackagesConfig(t, overlayDir, server.URL, pkgs)
	for _, pkg := range pkgs {
		writeExitTestEbuild(t, overlayDir, pkg, "0.9.0")
	}

	pinCheckFlags(t) // force = true, sane concurrency, quiet
	pinNoFetchCache(t, noFetchCache)
	setReconcileYes(t, false)
	setReconcileInteractive(t, func() bool { return false }) // never prompt, never write

	captureStdout(t, func() {
		withExitIntercept(func() {
			runCheck(context.Background(), overlayDir, configDir, nil, 0, &config.Config{}, config.LLMConfig{})
		})
	})

	return requests.Load()
}

// TestNoFetchCacheFlag pins the operator-facing contract: the deduplication is
// on unless it is switched off, and switching it off reproduces the pre-story
// behaviour so a suspicious result can be compared against an un-deduplicated
// run.
func TestNoFetchCacheFlag(t *testing.T) {
	// -------------------------------------------------------------------------
	// S024-R7.2 — the default is "deduplicate". The flag is an opt-OUT.
	// -------------------------------------------------------------------------
	t.Run("the flag is registered as a bool defaulting to false", func(t *testing.T) {
		f := autoupdateCmd.Flags().Lookup("no-fetch-cache")
		if f == nil {
			t.Fatal("autoupdate has no --no-fetch-cache flag")
		}
		if got := f.Value.Type(); got != "bool" {
			t.Errorf("--no-fetch-cache has type %q, want %q", got, "bool")
		}
		if f.DefValue != "false" {
			t.Errorf("--no-fetch-cache defaults to %q, want %q — deduplication is on by default (S024-R7.2)",
				f.DefValue, "false")
		}
	})

	t.Run("the bare flag sets the switch", func(t *testing.T) {
		orig := autoupdateNoFetchCache
		t.Cleanup(func() {
			autoupdateNoFetchCache = orig
			//nolint:errcheck // restoring a bool flag on a known-registered name cannot fail
			_ = autoupdateCmd.Flags().Set("no-fetch-cache", strconv.FormatBool(orig))
		})

		if err := autoupdateCmd.ParseFlags([]string{"--no-fetch-cache"}); err != nil {
			t.Fatalf("parsing --no-fetch-cache failed: %v", err)
		}
		if !autoupdateNoFetchCache {
			t.Error("--no-fetch-cache parsed but the switch stayed false; the flag is not bound to the variable")
		}
	})

	// -------------------------------------------------------------------------
	// The wiring, proven where it is visible: on the wire.
	// -------------------------------------------------------------------------
	t.Run("omitting the flag fetches the shared URL once", func(t *testing.T) {
		if got := runFetchCacheCheck(t, false); got != 1 {
			t.Errorf("the server saw %d request(s) for two records declaring one URL; want 1 "+
				"— the default run must deduplicate (S024-R7.2)", got)
		}
	})

	// NOTE ON RUNTIME: this case really issues two requests to one host, and the
	// per-host rate limiter is one request per 6 s, so it takes ~6 s. That cost
	// IS the finding this story is about — it is what the deduplicated case above
	// no longer pays.
	t.Run("passing the flag fetches the shared URL once per record", func(t *testing.T) {
		if got := runFetchCacheCheck(t, true); got != 2 {
			t.Errorf("the server saw %d request(s) with --no-fetch-cache; want 2 — the flag must "+
				"reproduce the pre-story behaviour, one request per read (S024-R7.1)", got)
		}
	})
}
