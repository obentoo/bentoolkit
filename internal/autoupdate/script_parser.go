package autoupdate

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// ErrScriptSupportNotBuilt is returned when a parser="script" package is checked
// by a binary compiled WITHOUT the chromedp headless-browser backend. The real
// evaluator lives behind the `chromedp` build tag and drives a Chrome or
// Chromium already installed on the system; the default build ships only the
// testable interface so the browser dependency stays opt-in.
var ErrScriptSupportNotBuilt = errors.New(
	"script parser support not built: rebuild with -tags chromedp; " +
		"a Chrome or Chromium executable is required at run time")

// liveEvaluator renders a URL in a headless browser and evaluates a JS
// expression against the live (post-JS) DOM, returning the expression's string
// result. It is an interface so tests can inject a fake without a real browser;
// the only real implementation is the chromedp backend, built with
// `-tags chromedp`.
type liveEvaluator interface {
	Evaluate(ctx context.Context, url, script string, headers map[string]string) (string, error)
}

// newLiveEvaluator builds the evaluator used by the script parser. opTimeout
// bounds each navigation/evaluation. It is initialised from
// defaultLiveEvaluator, which build constraints select: the stub in
// script_evaluator_stub.go (`!chromedp`) reports ErrScriptSupportNotBuilt, and
// the chromedp backend in script_evaluator_chromedp.go (`chromedp`) launches a
// headless Chrome. It stays a var so in-package tests can swap it, restoring
// the original through t.Cleanup.
var newLiveEvaluator = defaultLiveEvaluator

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
	data, err := os.ReadFile(path) //nolint:gosec // G304: path joins scriptsDir and a script name refused above unless it is a bare file name (no separator, no "..")
	if err != nil {
		return "", fmt.Errorf("failed to read script file %q: %w", path, err)
	}
	return string(data), nil
}
