//go:build playwright

// This file provides the real headless-browser backend for the "script" parser,
// built only when the `playwright` build tag is set. It depends on
// playwright-go and requires the Playwright browsers to be installed
// (`playwright install chromium`, or playwright.Install() at startup). The
// default build (without this tag) ships only the testable interface in
// script_parser.go, keeping the heavy dependency opt-in.

package autoupdate

import (
	"context"
	"fmt"
	"time"

	"github.com/playwright-community/playwright-go"
)

// init replaces the default newLiveEvaluator with a Playwright-backed one. Each
// evaluator owns a driver process (playwright.Run) and a headless Chromium
// instance, torn down by Close.
func init() {
	newLiveEvaluator = func(opTimeout time.Duration) (liveEvaluator, error) {
		pw, err := playwright.Run()
		if err != nil {
			return nil, fmt.Errorf("could not start Playwright (run `playwright install chromium`): %w", err)
		}
		browser, err := pw.Chromium.Launch(playwright.BrowserTypeLaunchOptions{
			Headless: playwright.Bool(true),
		})
		if err != nil {
			_ = pw.Stop()
			return nil, fmt.Errorf("could not launch headless Chromium: %w", err)
		}
		return &playwrightEvaluator{pw: pw, browser: browser, opTimeout: opTimeout}, nil
	}
}

// playwrightEvaluator renders pages and evaluates JS via playwright-go.
type playwrightEvaluator struct {
	pw        *playwright.Playwright
	browser   playwright.Browser
	opTimeout time.Duration
}

// Evaluate opens a fresh page, navigates to url, and evaluates script against
// the rendered DOM. page.Evaluate auto-awaits a Promise, so an
// `(async () => {...})()` IIFE resolves to its string result.
func (e *playwrightEvaluator) Evaluate(ctx context.Context, url, script string, headers map[string]string) (string, error) {
	page, err := e.browser.NewPage()
	if err != nil {
		return "", fmt.Errorf("could not open page: %w", err)
	}
	defer page.Close()

	if e.opTimeout > 0 {
		page.SetDefaultTimeout(float64(e.opTimeout.Milliseconds()))
	}
	if len(headers) > 0 {
		if hErr := page.SetExtraHTTPHeaders(headers); hErr != nil {
			return "", fmt.Errorf("could not set extra HTTP headers: %w", hErr)
		}
	}

	// playwright-go is not context-aware on Goto/Evaluate; close the page when
	// ctx is cancelled so an in-flight call aborts on SIGINT or deadline. The
	// done channel stops this watcher once Evaluate returns normally.
	done := make(chan struct{})
	defer close(done)
	go func() {
		select {
		case <-ctx.Done():
			_ = page.Close()
		case <-done:
		}
	}()

	if _, err := page.Goto(url); err != nil {
		return "", fmt.Errorf("navigation to %q failed: %w", url, err)
	}
	res, err := page.Evaluate(script)
	if err != nil {
		return "", fmt.Errorf("script evaluation failed: %w", err)
	}
	if res == nil {
		return "", fmt.Errorf("script returned null/undefined")
	}
	s, ok := res.(string)
	if !ok {
		return "", fmt.Errorf("script result is not a string: got %T (%v)", res, res)
	}
	return s, nil
}

// Close tears down the browser and the driver process.
func (e *playwrightEvaluator) Close() error {
	var firstErr error
	if e.browser != nil {
		if err := e.browser.Close(); err != nil {
			firstErr = err
		}
	}
	if e.pw != nil {
		if err := e.pw.Stop(); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}
