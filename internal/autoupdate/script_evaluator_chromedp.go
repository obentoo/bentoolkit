//go:build chromedp

// This file is the headless-browser backend for the "script" parser, built
// only when the `chromedp` build tag is set. It speaks the Chrome DevTools
// Protocol directly via github.com/chromedp/chromedp and drives whatever Chrome
// or Chromium is already installed on the system. Its complement,
// script_evaluator_stub.go (`!chromedp`), carries the default build, which
// ships only the testable interface in script_parser.go, keeping the browser
// dependency opt-in.
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

// defaultLiveEvaluator is the `-tags chromedp` build's evaluator factory: it
// returns a chromedp-backed evaluator. Each evaluator owns a browser allocator
// and a long-lived browser context (started eagerly so launch failures surface
// here, not on the first Evaluate); both are torn down by Close.
func defaultLiveEvaluator(opTimeout time.Duration) (liveEvaluator, error) {
	allocCtx, allocCancel := chromedp.NewExecAllocator(
		context.Background(), // SAFE: root parent for the browser allocator; no caller ctx exists at construction, and it is torn down by Close. Per-call cancellation arrives via Evaluate's ctx.
		chromedp.DefaultExecAllocatorOptions[:]...,
	)
	browserCtx, browserCancel := chromedp.NewContext(allocCtx)
	// Start the browser now so a missing/broken Chrome fails fast with a
	// launch error, rather than on first use.
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

// chromedpEvaluator renders pages and evaluates JS via the DevTools Protocol.
// browserCtx is the shared browser; each Evaluate derives a fresh tab from it.
type chromedpEvaluator struct {
	allocCancel   context.CancelFunc
	browserCtx    context.Context //nolint:containedctx // a chromedp browser is a context by that library's design; each tab must descend from it
	browserCancel context.CancelFunc
	opTimeout     time.Duration
}

// Evaluate opens a fresh tab, navigates to url, and evaluates script against the
// rendered DOM. WithAwaitPromise makes the evaluation wait for a returned
// Promise to settle, so an `(async () => {...})()` IIFE resolves to its string
// result rather than returning an unresolved Promise. The result is unmarshalled
// into a string, so a non-string JS result (e.g. `1 + 1`) surfaces as an error.
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

	// contextcheck flags tabCtx as not derived from ctx. It cannot be: a tab has
	// to descend from the browser context or chromedp has no browser to attach
	// it to. The caller's ctx reaches it through the AfterFunc bridge above,
	// which cancels this tab when ctx ends — the linter does not model that.
	if err := chromedp.Run(tabCtx, actions...); err != nil { //nolint:contextcheck // bridged via context.AfterFunc above
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
