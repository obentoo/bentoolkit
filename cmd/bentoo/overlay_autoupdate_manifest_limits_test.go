package main

import (
	"strings"
	"testing"
)

// TestManifestConcurrency_ZeroIsRefusedByName: a run with no manifest slot
// could never download anything, so --manifest-concurrency=0 stops before any
// work instead of hanging every manifest run.
func TestManifestConcurrency_ZeroIsRefusedByName(t *testing.T) {
	c := newTestCLI(t)
	if _, stderr, code := c.Run("overlay", "autoupdate", "--check", "--manifest-concurrency=0"); code == 0 || !strings.Contains(stderr, "at least one manifest run must be allowed") {
		t.Errorf("--manifest-concurrency=0 exited %d:\n%s", code, stderr)
	}
}
