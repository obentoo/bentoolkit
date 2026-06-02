//go:build playwright

// Integration test for the real Playwright-backed liveEvaluator. It is built and
// run only with `-tags playwright` because it launches a headless Chromium, so
// it stays out of the default build/CI. It also skips itself when the Playwright
// browsers cannot be provisioned, keeping a browser-less runner green.
//
//	go test -tags playwright ./internal/autoupdate/ -run Integration
package autoupdate

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/playwright-community/playwright-go"
)

// newIntegrationEvaluator provisions Chromium (a no-op when it is already
// cached) and builds the real evaluator, skipping the whole test when a headless
// browser cannot be started — a runner without Playwright browsers must skip,
// not fail.
func newIntegrationEvaluator(t *testing.T) liveEvaluator {
	t.Helper()
	if err := playwright.Install(&playwright.RunOptions{Browsers: []string{"chromium"}}); err != nil {
		t.Skipf("playwright browsers unavailable; skipping integration test: %v", err)
	}
	eval, err := newLiveEvaluator(30 * time.Second)
	if err != nil {
		t.Skipf("could not start headless browser; skipping: %v", err)
	}
	t.Cleanup(func() {
		if c, ok := eval.(io.Closer); ok {
			_ = c.Close()
		}
	})
	return eval
}

// libreofficeLikeSite serves a two-level index that mirrors
// download.documentfoundation.org/libreoffice/src/: the root lists 3-segment
// release dirs and each dir lists the canonical 4-segment tarball next to decoy
// variants the probe must ignore. It is exactly what .autoupdate/scripts/
// libreoffice.js navigates, so the test exercises the same multi-step
// (DOM + fetch) shape against a local server instead of the live mirror.
func libreofficeLikeSite() *httptest.Server {
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/" {
			_, _ = io.WriteString(w, `<html><body>
				<a href="26.1.9/">26.1.9/</a>
				<a href="26.2.4/">26.2.4/</a>
				<a href="26.2.3/">26.2.3/</a>
				<a href="readme.txt">readme.txt</a>
			</body></html>`)
			return
		}
		// Any version subdirectory: the canonical tarball plus a -dictionaries
		// decoy listed first, so a passing test proves the regex anchors on
		// "libreoffice-" + digit rather than grabbing the first 4-segment number.
		_, _ = io.WriteString(w, `<html><body>
			<a href="libreoffice-dictionaries-26.2.4.1.tar.xz">dict</a>
			<a href="libreoffice-26.2.4.1.tar.xz">libreoffice-26.2.4.1.tar.xz</a>
		</body></html>`)
	})
	return httptest.NewServer(mux)
}

// TestPlaywrightEvaluator_Integration verifies the real backend against a local
// server: post-render DOM access, Promise auto-await (the decisive reason for
// Playwright over a raw chromedp.Evaluate, which returns the unresolved Promise),
// the actual multi-step libreoffice.js logic, and the non-string-result guard.
func TestPlaywrightEvaluator_Integration(t *testing.T) {
	eval := newIntegrationEvaluator(t)
	srv := libreofficeLikeSite()
	defer srv.Close()
	ctx := context.Background()

	t.Run("reads post-render DOM", func(t *testing.T) {
		got, err := eval.Evaluate(ctx, srv.URL, `document.querySelectorAll('a').length.toString()`, nil)
		if err != nil {
			t.Fatal(err)
		}
		if got != "4" {
			t.Fatalf("got %q links, want 4", got)
		}
	})

	t.Run("auto-awaits async IIFE (multi-step fetch)", func(t *testing.T) {
		// The production probe logic, kept in lock-step with
		// .autoupdate/scripts/libreoffice.js: pick the newest 3-segment dir,
		// then read the 4-segment tarball version inside it.
		script := `(async () => {
			const dirs = [...document.querySelectorAll('a')]
				.map(a => (a.getAttribute('href') || '').replace(/\/$/, ''))
				.filter(t => /^\d+\.\d+\.\d+$/.test(t))
				.sort((a, b) => a.localeCompare(b, undefined, { numeric: true }));
			if (!dirs.length) return '';
			const newest = dirs[dirs.length - 1];
			const res = await fetch(location.href + newest + '/');
			if (!res.ok) return '';
			const html = await res.text();
			const m = html.match(/libreoffice-(\d+\.\d+\.\d+\.\d+)\.tar\.xz/);
			return m ? m[1] : '';
		})()`
		got, err := eval.Evaluate(ctx, srv.URL, script, nil)
		if err != nil {
			t.Fatal(err)
		}
		if got != "26.2.4.1" {
			t.Fatalf("got %q, want 26.2.4.1 (newest dir, canonical 4-segment tarball)", got)
		}
	})

	t.Run("rejects non-string result", func(t *testing.T) {
		if _, err := eval.Evaluate(ctx, srv.URL, `1 + 1`, nil); err == nil {
			t.Fatal("expected an error for a non-string script result")
		}
	})
}
