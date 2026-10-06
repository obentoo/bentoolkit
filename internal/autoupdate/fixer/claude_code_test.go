package fixer

import (
	"testing"
)

// stubLookPathFound forces claudeAvailable() to succeed regardless of the host
// PATH, so ClaudeCodeClient construction is deterministic. Restored via
// t.Cleanup. Every test that constructs a client (other than the explicit
// unavailable test) calls this first.
func stubLookPathFound(t *testing.T) {
	t.Helper()
	orig := lookPath
	lookPath = func(string) (string, error) { return "/usr/bin/claude", nil }
	t.Cleanup(func() { lookPath = orig })
}

// capturedExec records the argv handed to the exec seam for the most recent
// call, so tests can assert what landed in argv (and what did NOT — e.g. page
// content or the API key).
type capturedExec struct {
	name string
	args []string
}

// argsContain reports whether any element of args equals target.
func argsContain(args []string, target string) bool {
	for _, a := range args {
		if a == target {
			return true
		}
	}
	return false
}

// flagValue returns the element immediately following the first occurrence of
// flag in args, plus whether the flag was found with a following value.
func flagValue(args []string, flag string) (string, bool) {
	for i, a := range args {
		if a == flag && i+1 < len(args) {
			return args[i+1], true
		}
	}
	return "", false
}
