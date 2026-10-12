//go:build chromedp

// Integration test for the chromedp-backed liveEvaluator. Built and run only
// with `-tags chromedp` because it launches a headless Chrome, so it stays out
// of the default build/CI. It skips itself when no Chrome can be started,
// keeping a browser-less runner green.
//
//	go test -tags chromedp ./internal/autoupdate/ -run Integration
package autoupdate

import (
	"context"
	"html"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// newIntegrationEvaluator builds the real chromedp evaluator, skipping the whole
// test when a headless browser cannot be started — a runner without Chrome must
// skip, not fail.
func newIntegrationEvaluator(t *testing.T) liveEvaluator {
	t.Helper()
	eval, err := newLiveEvaluator(30 * time.Second)
	if err != nil {
		t.Skipf("could not start headless Chrome; skipping: %v", err)
	}
	t.Cleanup(func() {
		if c, ok := eval.(io.Closer); ok {
			_ = c.Close()
		}
	})
	return eval
}

// libreofficeLikeSite serves a two-level index mirroring
// download.documentfoundation.org/libreoffice/src/: the root lists 3-segment
// release dirs and each dir lists the canonical 4-segment tarball next to a
// decoy the probe must ignore. It is exactly what .autoupdate/scripts/
// libreoffice.js navigates, exercising the same multi-step (DOM + fetch) shape
// against a local server.
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
		_, _ = io.WriteString(w, `<html><body>
			<a href="libreoffice-dictionaries-26.2.4.1.tar.xz">dict</a>
			<a href="libreoffice-26.2.4.1.tar.xz">libreoffice-26.2.4.1.tar.xz</a>
		</body></html>`)
	})
	return httptest.NewServer(mux)
}

// TestChromedpEvaluator_Integration verifies what the script parser needs from
// its browser backend: post-render DOM access, Promise auto-await
// (EvalAwaitPromise makes chromedp.Evaluate resolve a returned Promise), the
// real multi-step libreoffice.js logic, and the non-string-result guard.
func TestChromedpEvaluator_Integration(t *testing.T) {
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
		// Kept in lock-step with .autoupdate/scripts/libreoffice.js: pick the
		// newest 3-segment dir, then read the 4-segment tarball version inside.
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

// probeHeader is the header the echo page reflects into its body.
const probeHeader = "X-Bentoo-Probe"

// headerEchoSite serves a page whose body is the value of probeHeader on the
// request that fetched it, so a script can read back what the browser sent.
func headerEchoSite() *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `<html><body><p id="echo">`+html.EscapeString(r.Header.Get(probeHeader))+`</p></body></html>`)
	}))
}

// TestChromedpEvaluator_IntegrationHeadersPromisesDeadline pins the rest of the
// evaluator's contract against a real browser: extra headers reach the server,
// a Promise that settles after a delay is awaited, a JS null reads as "" while
// undefined is an error, and the caller's deadline aborts a script that never
// settles.
func TestChromedpEvaluator_IntegrationHeadersPromisesDeadline(t *testing.T) {
	eval := newIntegrationEvaluator(t)
	srv := headerEchoSite()
	defer srv.Close()
	ctx := context.Background()

	t.Run("sends extra headers and awaits a delayed promise", func(t *testing.T) {
		script := `new Promise(resolve => setTimeout(
			() => resolve(document.getElementById('echo').textContent + '/late'), 300))`
		got, err := eval.Evaluate(ctx, srv.URL, script, map[string]string{probeHeader: "probe-42"})
		if err != nil {
			t.Fatal(err)
		}
		if got != "probe-42/late" {
			t.Fatalf("got %q, want %q (header echoed, promise resolved after the delay)", got, "probe-42/late")
		}
	})

	t.Run("sends no header when none is given", func(t *testing.T) {
		got, err := eval.Evaluate(ctx, srv.URL, `document.getElementById('echo').textContent`, nil)
		if err != nil {
			t.Fatal(err)
		}
		if got != "" {
			t.Fatalf("got %q, want an empty echo: a previous tab's headers leaked", got)
		}
	})

	t.Run("null reads as the empty string", func(t *testing.T) {
		got, err := eval.Evaluate(ctx, srv.URL, `null`, nil)
		if err != nil {
			t.Fatalf("a null result must not be an error, got %v", err)
		}
		if got != "" {
			t.Fatalf("got %q, want the empty string", got)
		}
	})

	t.Run("undefined is an error", func(t *testing.T) {
		if _, err := eval.Evaluate(ctx, srv.URL, `undefined`, nil); err == nil {
			t.Fatal("expected an error for an undefined script result")
		}
	})

	t.Run("honours the caller's deadline", func(t *testing.T) {
		dctx, cancel := context.WithTimeout(ctx, 500*time.Millisecond)
		defer cancel()
		start := time.Now()
		_, err := eval.Evaluate(dctx, srv.URL, `new Promise(() => {})`, nil)
		if err == nil {
			t.Fatal("expected an error when the caller's deadline passes before the promise settles")
		}
		if elapsed := time.Since(start); elapsed > 10*time.Second {
			t.Fatalf("Evaluate returned after %v; the 500ms caller deadline was not honoured", elapsed)
		}
	})
}
