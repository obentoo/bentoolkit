package main

// Gap tests the Test Advisor recorded for bentoo story 003, sub-tasks 1.3 and
// 3.3: an unreadable installed-package database is ONE Warn per run (R3.8),
// and an entry dated in the future is one Warn and is not lifted.

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"
)

func cooldownLogLines(t *testing.T, c *testCLI, re *regexp.Regexp) []string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(c.Home(), ".local", "state", "bentoo", "logs", "bentoo.log"))
	if err != nil {
		t.Fatalf("reading the invocation log: %v", err)
	}
	var out []string
	for _, line := range strings.Split(string(data), "\n") {
		if re.MatchString(line) {
			out = append(out, line)
		}
	}
	return out
}

func TestCooldownLog_AnUnreadablePkgdbIsOneWarnAndTheOtherRulesStillApply(t *testing.T) {
	c := cooldownApplyCLI(t)
	c.deps.cooldownPkgdbRoot = t.TempDir() // no var/db/pkg under it
	plantCooldown(t, c,
		cooldownCmdEntry{"dev-test/cdp-old", "1.0", cooldownLongAgo, "autoupdate", ""},
		cooldownCmdEntry{"dev-test/cdp-new", "1.0", cooldownCmdRecent, "autoupdate", ""},
	)
	seedCooldownPending(t, c)
	if _, stderr, code := c.Run("overlay", "autoupdate", "--apply", "all"); code != 0 {
		t.Fatalf("--apply all exited %d\nstderr:\n%s", code, stderr)
	}
	if got := cooldownLogLines(t, c, regexp.MustCompile(`(?i)WARN.*release by installation skipped`)); len(got) != 1 {
		t.Errorf("%d Warn records for the unreadable database, want exactly 1: %v", len(got), got)
	}
	expectAtoms(t, c, "=dev-test/cdp-new-1.0")
}

func TestCooldownLog_AFutureSinceIsOneWarnAndIsNotLifted(t *testing.T) {
	c := cooldownApplyCLI(t)
	future := time.Now().UTC().Add(48 * time.Hour).Truncate(time.Second)
	plantCooldown(t, c, cooldownCmdEntry{"dev-test/cdf-skew", "1.0", future, "autoupdate", ""})
	seedCooldownPending(t, c)
	if _, stderr, code := c.Run("overlay", "autoupdate", "--apply", "all"); code != 0 {
		t.Fatalf("--apply all exited %d\nstderr:\n%s", code, stderr)
	}
	if got := cooldownLogLines(t, c, regexp.MustCompile(`(?i)WARN.*dated in the future.*dev-test/cdf-skew`)); len(got) != 1 {
		t.Errorf("%d Warn records for the future since, want exactly 1: %v", len(got), got)
	}
	expectAtoms(t, c, "=dev-test/cdf-skew-1.0")
}
