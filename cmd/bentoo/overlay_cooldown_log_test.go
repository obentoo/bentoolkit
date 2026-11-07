package main

// Authored for bentoo story 003, sub-task 4.2 (R8.1, R3.7) before the
// subcommands existed, as the plan asks: every add and lift a writing
// `overlay cooldown` subcommand makes is one structured log record carrying
// package, version and reason, and a stale lift is a Warn.

import (
	"os"
	"path/filepath"
	"regexp"
	"testing"
	"time"
)

func TestCooldownLog_CLIWrites(t *testing.T) {
	c := newTestCLI(t)
	old := time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)
	plantCooldown(t, c,
		cooldownCmdEntry{"dev-test/cdw-confirm", "1.0", cooldownCmdRecent, "autoupdate", ""},
		cooldownCmdEntry{"dev-test/cdw-old", "1.0", old, "autoupdate", ""},
		cooldownCmdEntry{"dev-test/cdw-gone", "1.0", cooldownCmdRecent, "autoupdate", ""},
	)
	cooldownCmdEbuild(t, c, "dev-test/cdw-new", "2.0")
	if err := os.Remove(filepath.Join(c.Overlay(), "dev-test", "cdw-gone", "cdw-gone-1.0.ebuild")); err != nil {
		t.Fatal(err)
	}

	for _, args := range [][]string{
		{"add", "=dev-test/cdw-new-2.0"},
		{"confirm", "=dev-test/cdw-confirm-1.0"},
		{"sweep"},
	} {
		if _, stderr, code := c.Run(append([]string{"overlay", "cooldown"}, args...)...); code != 0 {
			t.Fatalf("cooldown %s exited %d\nstderr:\n%s", args[0], code, stderr)
		}
	}

	for _, want := range []struct{ level, pkg, reason string }{
		{"INFO", "dev-test/cdw-new", ""},
		{"INFO", "dev-test/cdw-confirm", "manual"},
		{"INFO", "dev-test/cdw-old", "elapsed"},
		{"WARN", "dev-test/cdw-gone", "stale"},
	} {
		expr := `(?i)` + want.level + `.*cooldown.*package["=:\s]+"?` + regexp.QuoteMeta(want.pkg) + `"?[\s,]`
		if want.reason != "" {
			expr += `.*reason["=:\s]+"?` + want.reason + `\b`
		}
		if got := cooldownLogLines(t, c, regexp.MustCompile(expr)); len(got) != 1 {
			t.Errorf("%d %s records for %s (reason %q), want exactly 1: %v", len(got), want.level, want.pkg, want.reason, got)
		}
	}
}
