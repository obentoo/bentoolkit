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
	"errors"
	"fmt"
	"time"

	"github.com/chromedp/cdproto/cdp"
	"github.com/chromedp/cdproto/network"
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
	if err := chromedp.Do(browserCtx); err != nil {
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
// rendered DOM. EvalAwaitPromise makes the evaluation wait for a returned
// Promise to settle, so an `(async () => {...})()` IIFE resolves to its string
// result rather than returning an unresolved Promise. The result is decoded
// into a string, so a non-string JS result (e.g. `1 + 1`) surfaces as an error,
// `undefined` surfaces as an error, and `null` reads as the empty string.
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

	steps := make([]chromedp.Action[chromedp.Void], 0, 2)
	if len(headers) > 0 {
		steps = append(steps, sendExtraHeaders(headers))
	}
	steps = append(steps, chromedp.Navigate(url))
	evaluate := chromedp.Evaluate[string](script, chromedp.EvalAwaitPromise)

	// contextcheck flags tabCtx as not derived from ctx. It cannot be: a tab has
	// to descend from the browser context or chromedp has no browser to attach
	// it to. The caller's ctx reaches it through the AfterFunc bridge above,
	// which cancels this tab when ctx ends — the linter does not model that.
	res, err := chromedp.Run(tabCtx, func(actCtx context.Context, t *chromedp.Target) (string, error) { //nolint:contextcheck // bridged via context.AfterFunc above
		if _, err := chromedp.Steps(steps...)(actCtx, t); err != nil {
			return "", err
		}
		return evaluate(actCtx, t)
	})
	// chromedp reports a JS null as ErrJSNull for a string result. The
	// evaluator has always read it as the empty string, so a script that
	// yields null hands its caller an empty result, not an evaluation failure.
	if errors.Is(err, chromedp.ErrJSNull) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("chromedp evaluation of %q failed: %w", url, err)
	}
	return res, nil
}

// sendExtraHeaders is the step that makes the tab attach headers to every
// request it issues: the navigation and the script's own fetches alike.
func sendExtraHeaders(headers map[string]string) chromedp.Action[chromedp.Void] {
	h := make(network.Headers, len(headers))
	for k, v := range headers {
		h[k] = v
	}
	return chromedp.Func(func(ctx context.Context, t *chromedp.Target) error {
		if _, err := cdp.Call(ctx, t, network.Enable, network.EnableParams{}); err != nil {
			return fmt.Errorf("enabling the network domain: %w", err)
		}
		if _, err := cdp.Call(ctx, t, network.SetExtraHTTPHeaders, network.SetExtraHTTPHeadersParams{Headers: h}); err != nil {
			return fmt.Errorf("setting extra HTTP headers: %w", err)
		}
		return nil
	})
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
