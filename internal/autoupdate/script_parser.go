// Package autoupdate provides a headless-browser ("script") version parser for
// cases that need a rendered DOM, multi-step navigation, or arbitrary JS logic.
package autoupdate

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// ErrScriptSupportNotBuilt is returned when a parser="script" package is checked
// by a binary compiled WITHOUT the headless-browser backend. The real evaluator
// lives behind the `playwright` build tag (and needs the Playwright browsers
// installed); the default build ships only the testable interface so the heavy
// dependency stays opt-in.
var ErrScriptSupportNotBuilt = errors.New(
	"script parser support not built: rebuild with -tags playwright and run " +
		"`playwright install chromium`")

// liveEvaluator renders a URL in a headless browser and evaluates a JS
// expression against the live (post-JS) DOM, returning the expression's string
// result. It is an interface so tests can inject a fake without a real browser,
// and so the heavy browser backend can be swapped in behind a build tag.
type liveEvaluator interface {
	Evaluate(ctx context.Context, url, script string, headers map[string]string) (string, error)
}

// newLiveEvaluator builds the evaluator used by the script parser. opTimeout
// bounds each navigation/evaluation. The default implementation reports
// ErrScriptSupportNotBuilt; the `playwright` build tag replaces this (via init)
// with a real headless-browser evaluator.
var newLiveEvaluator = func(opTimeout time.Duration) (liveEvaluator, error) {
	return nil, ErrScriptSupportNotBuilt
}

// ScriptParser extracts a version by evaluating JS against a live page. Unlike
// the Parser implementations it navigates itself (it needs the rendered DOM),
// so it is driven by Checker.parseLive rather than the NewParserFromConfig path
// and the Parser interface (there is no pre-fetched []byte to hand it).
type ScriptParser struct {
	URL     string
	Script  string
	Headers map[string]string
	eval    liveEvaluator
}

// ParseLive renders URL and evaluates Script, returning the trimmed result. The
// script is responsible for producing a Gentoo-formatted version string (it may
// do its own transform/selection in JS) — transform/select from the TOML do not
// apply on this path.
func (p *ScriptParser) ParseLive(ctx context.Context) (string, error) {
	if p.eval == nil {
		return "", ErrScriptSupportNotBuilt
	}
	out, err := p.eval.Evaluate(ctx, p.URL, p.Script, p.Headers)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(out), nil
}

// resolveScript loads the script body. A value of the form "@file.js" is read
// from scriptsDir/file.js (the overlay's .autoupdate/scripts/); any other value
// is treated as an inline script. The "@file" form is restricted to a bare file
// name (no path separators, no "..") so a packages.toml cannot read arbitrary
// files outside the scripts directory.
func resolveScript(script, scriptsDir string) (string, error) {
	if !strings.HasPrefix(script, "@") {
		return script, nil
	}
	name := strings.TrimPrefix(script, "@")
	if name == "" || strings.ContainsRune(name, '/') ||
		strings.ContainsRune(name, os.PathSeparator) || strings.Contains(name, "..") {
		return "", fmt.Errorf("invalid script file reference %q (must be a bare file name)", script)
	}
	path := filepath.Join(scriptsDir, name)
	data, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("failed to read script file %q: %w", path, err)
	}
	return string(data), nil
}
