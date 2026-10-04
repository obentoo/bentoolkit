//go:build !chromedp

// This file is the default build's half of the script parser's evaluator
// selection: without the `chromedp` build tag there is no headless-browser
// backend, and defaultLiveEvaluator reports ErrScriptSupportNotBuilt. Its
// complement, script_evaluator_chromedp.go, is built only with `-tags chromedp`.

package autoupdate

import "time"

// defaultLiveEvaluator is the default build's evaluator factory. It always
// returns ErrScriptSupportNotBuilt, so a parser="script" package fails its check
// with a message that says how to build browser support in.
func defaultLiveEvaluator(time.Duration) (liveEvaluator, error) {
	return nil, ErrScriptSupportNotBuilt
}
