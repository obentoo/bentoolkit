//go:build playwright

// The playwright build tag selected a playwright-go evaluator for the "script"
// parser. That backend was removed: chromedp drives the system Chrome/Chromium
// instead and needs no separately provisioned browser. A build that still asks
// for the removed tag must not quietly produce a binary without browser
// support, so this file, compiled only under that tag, fails type-checking on
// purpose. The compiler echoes the constant expression below, which makes the
// string the diagnostic the user reads.

package autoupdate

const _ = "the playwright backend was removed; build with -tags chromedp" + 1
