//go:build chromedp && !playwright

// This file provides an alternative headless-browser backend for the "script"
// parser, built only when the `chromedp` build tag is set (and `playwright` is
// not, so the two backends never both register an init). Unlike the
// playwright-go backend it speaks the Chrome DevTools Protocol directly via
// github.com/chromedp/chromedp: no Node.js driver and no `playwright install`
// step — it drives whatever Chrome/Chromium is already on the system. The
// default build (no tag) still ships only the testable interface in
// script_parser.go, keeping the browser dependency opt-in.
//
//	go build -tags chromedp ./...
//	go test  -tags chromedp ./internal/autoupdate/ -run Integration
package autoupdate

import (
	"context"
	"fmt"
	"time"

	"github.com/chromedp/cdproto/network"
	"github.com/chromedp/cdproto/runtime"
	"github.com/chromedp/chromedp"
)

// init replaces the default newLiveEvaluator with a chromedp-backed one. Each
// evaluator owns a browser allocator and a long-lived browser context (started
// eagerly so launch failures surface here, not on the first Evaluate); both are
// torn down by Close.
func init() {
	newLiveEvaluator = func(opTimeout time.Duration) (liveEvaluator, error) {
		allocCtx, allocCancel := chromedp.NewExecAllocator(
			context.Background(),
			chromedp.DefaultExecAllocatorOptions[:]...,
		)
		browserCtx, browserCancel := chromedp.NewContext(allocCtx)
		// Start the browser now so a missing/broken Chrome fails fast and is
		// reported like Playwright's launch error, rather than on first use.
		if err := chromedp.Run(browserCtx); err != nil {
			browserCancel()
			allocCancel()
			return nil, fmt.Errorf("could not launch headless Chrome (chromedp): %w", err)
		}
		return &chromedpEvaluator{
			allocCancel:   allocCancel,
			browserCtx:    browserCtx,
			browserCancel: browserCancel,
			opTimeout:     opTimeout,
		}, nil
	}
}

// chromedpEvaluator renders pages and evaluates JS via the DevTools Protocol.
// browserCtx is the shared browser; each Evaluate derives a fresh tab from it.
type chromedpEvaluator struct {
	allocCancel   context.CancelFunc
	browserCtx    context.Context
	browserCancel context.CancelFunc
	opTimeout     time.Duration
}

// Evaluate opens a fresh tab, navigates to url, and evaluates script against the
// rendered DOM. WithAwaitPromise mirrors Playwright's page.Evaluate semantics so
// an `(async () => {...})()` IIFE resolves to its string result rather than
// returning an unresolved Promise. The result is unmarshalled into a string, so
// a non-string JS result (e.g. `1 + 1`) surfaces as an error.
func (e *chromedpEvaluator) Evaluate(ctx context.Context, url, script string, headers map[string]string) (string, error) {
	// Derive a per-call tab from the shared browser.
	tabCtx, cancel := chromedp.NewContext(e.browserCtx)
	defer cancel()

	// Bridge the caller's ctx: cancelling it (SIGINT or deadline) aborts the
	// in-flight navigation/evaluation by tearing the tab down.
	stop := context.AfterFunc(ctx, cancel)
	defer stop()

	if e.opTimeout > 0 {
		var tcancel context.CancelFunc
		tabCtx, tcancel = context.WithTimeout(tabCtx, e.opTimeout)
		defer tcancel()
	}

	actions := make([]chromedp.Action, 0, 4)
	if len(headers) > 0 {
		h := make(network.Headers, len(headers))
		for k, v := range headers {
			h[k] = v
		}
		actions = append(actions, network.Enable(), network.SetExtraHTTPHeaders(h))
	}

	var res string
	actions = append(actions,
		chromedp.Navigate(url),
		chromedp.Evaluate(script, &res,
			func(p *runtime.EvaluateParams) *runtime.EvaluateParams {
				return p.WithAwaitPromise(true)
			}),
	)

	if err := chromedp.Run(tabCtx, actions...); err != nil {
		return "", fmt.Errorf("chromedp evaluation of %q failed: %w", url, err)
	}
	return res, nil
}

// Close tears down the browser context and its allocator.
func (e *chromedpEvaluator) Close() error {
	if e.browserCancel != nil {
		e.browserCancel()
	}
	if e.allocCancel != nil {
		e.allocCancel()
	}
	return nil
}
