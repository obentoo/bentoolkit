package main

// Authored for story 058, sub-task 6.1 — R3.3, R3.4.
//
// Each test drives one run through the in-process harness and asserts both
// halves of the design.md stream classification in that run: the failure
// diagnostic (and its companion hint) moves to stderr, while the report it
// interrupts — header, rows, findings — stays on stdout. Moving too much is as
// wrong as moving too little.
//
// Uses s058Env (1.1) and s058WriteRegistry / s058CleanRegistry (5.1).
//
// Red on arrival: the four diagnostic sites still print to stdout.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/obentoo/bentoolkit/internal/autoupdate"
)

// s058OnStderrOnly asserts line is on stderr exactly once and absent from stdout.
func s058OnStderrOnly(t *testing.T, stdout, stderr, line string) {
	t.Helper()
	if n := strings.Count(stderr, line); n != 1 {
		t.Errorf("stderr carries %q %d time(s), want once\n--- stderr ---\n%s", line, n, stderr)
	}
	if strings.Contains(stdout, line) {
		t.Errorf("stdout still carries the diagnostic %q\n--- stdout ---\n%s", line, stdout)
	}
}

// s058OnStdoutOnly asserts line is on stdout and absent from stderr.
func s058OnStdoutOnly(t *testing.T, stdout, stderr, line string) {
	t.Helper()
	if !strings.Contains(stdout, line) {
		t.Errorf("stdout lost the report line %q\n--- stdout ---\n%s", line, stdout)
	}
	if strings.Contains(stderr, line) {
		t.Errorf("the report line %q was moved to stderr\n--- stderr ---\n%s", line, stderr)
	}
}

// Rows 10 and its companion hint (design.md): `overlay prune nosuch-cat
// 2>/dev/null` prints the report header and no failure line.
func TestS058PruneRestrictionFailureGoesToStderr(t *testing.T) {
	c := s058Env(t)
	stdout, stderr, code := c.Run("overlay", "prune", "nosuch-cat")
	if code != 1 {
		t.Errorf("exit %d, want 1", code)
	}
	s058OnStderrOnly(t, stdout, stderr, `"nosuch-cat" matches no package in the overlay`)
	s058OnStderrOnly(t, stdout, stderr, "Give a category (app-editors), a category/package (app-editors/zed), or no argument at all.")
	s058OnStdoutOnly(t, stdout, stderr, "Overlay Prune")
}

// Rows 2, 3 and 4: the refused migration's three lines move; the report header
// stays; the logger's refusal is printed once.
func TestS058MarkAutoDisabledRefusalGoesToStderr(t *testing.T) {
	c := s058Env(t)
	s058WriteRegistry(t, c.Overlay(), s058CleanRegistry)
	stdout, stderr, code := c.Run("overlay", "autoupdate", "--mark-auto-disabled", "--except", "nosuch/pkg")
	if code != 1 {
		t.Errorf("exit %d, want 1", code)
	}
	s058OnStderrOnly(t, stdout, stderr, "--except names entries that are not in packages.toml, so they protect nothing:")
	s058OnStderrOnly(t, stdout, stderr, "    nosuch/pkg")
	s058OnStderrOnly(t, stdout, stderr, "packages.toml was NOT modified.")
	s058OnStderrOnly(t, stdout, stderr, "refusing to migrate")
	s058OnStdoutOnly(t, stdout, stderr, "Registry Migration")
}

// Row 19 moves; the --depth diagnostic written through validate's diag writer
// stays on stdout in text mode (story 046's decision, out of scope here).
func TestS058ValidateUnmatchedSelectorGoesToStderr(t *testing.T) {
	c := s058Env(t)
	stdout, stderr, code := c.Run("overlay", "validate", "nosuch-cat")
	if code != 2 {
		t.Errorf("exit %d, want 2", code)
	}
	s058OnStderrOnly(t, stdout, stderr, `nothing in the overlay matches "nosuch-cat"`)

	stdout, stderr, code = c.Run("overlay", "validate", "--depth=bogus")
	if code != 1 {
		t.Errorf("--depth=bogus: exit %d, want 1", code)
	}
	s058OnStdoutOnly(t, stdout, stderr, "--depth: unknown validation depth")
}

// Row 32 stays: a --lint --fix --yes run whose write is refused prints its
// findings as report rows on stdout, and only the refusal (row 22) moves.
func TestS058LintFindingsStayOnStdout(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores file permissions, so the refused write this run needs cannot be produced")
	}
	c := s058Env(t)
	dir := s058WriteRegistry(t, c.Overlay(), `["net-misc/foo"]
url = "https://example.com/foo"
parser = "json"
path = "version"
enabled = true
comments = """
foo — carries a redundant enabled = true.
"""
# END
`)
	registry := filepath.Join(dir, "packages.toml")
	if err := os.Chmod(registry, 0o444); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = os.Chmod(dir, 0o755)
		_ = os.Chmod(registry, 0o644)
	})

	stdout, stderr, code := c.Run("overlay", "autoupdate", "--lint", "--fix", "--yes")
	if code != 1 {
		t.Errorf("exit %d, want 1", code)
	}
	s058OnStdoutOnly(t, stdout, stderr, "[net-misc/foo] "+autoupdate.LintRedundantEnabled)
	s058OnStderrOnly(t, stdout, stderr, "The registry was NOT repaired:")
}
