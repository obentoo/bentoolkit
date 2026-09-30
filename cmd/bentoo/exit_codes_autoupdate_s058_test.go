package main

// Authored for story 058, sub-task 5.1 — R1.1, R1.2, R1.3, R2.5, R2.8, R2.9.
//
// overlay autoupdate is a RunE command and its mode helpers' codes survive the
// trip up through runAutoupdate: the flag-validation refusals, the registry
// modes (including a repair whose write is refused), and the --check batch
// codes 0/1/2 driven through the
// whole tree (the existing TestCLI_ExitCodes calls runCheck directly and so
// cannot see a code collapse on the way up). Every row that takes the overlay
// lock also proves the lock file is gone once the command has returned (R1.2).
//
// Red on arrival: overlay autoupdate is still a Run command.

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const s058CleanRegistry = `["dev-util/claude-code"]
url = "https://registry.npmjs.org/@anthropic-ai/claude-code"
parser = "json"
path = "dist-tags.latest"
comments = """
claude-code — npm dist-tags.latest is the stable channel.
"""
# END
`

// s058WriteRegistry writes packages.toml under the overlay's .autoupdate.
func s058WriteRegistry(t *testing.T, overlay, content string) string {
	t.Helper()
	dir := filepath.Join(overlay, ".autoupdate")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "packages.toml")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestS058AutoupdateReturnsThroughRunE(t *testing.T) {
	if bad := s058NotOnRunE(t, "overlay autoupdate"); len(bad) > 0 {
		t.Errorf("overlay autoupdate still ends the process itself (R1.1): %v", bad)
	}
}

// s058LockGone asserts the overlay lock file is gone after the run returned.
func s058LockGone(t *testing.T, c *testCLI, _, _ string) {
	t.Helper()
	lock := filepath.Join(c.Overlay(), ".autoupdate.bentoo-lock")
	if _, err := os.Lstat(lock); err == nil {
		t.Errorf("%s is still on disk after the command returned: the deferred release did not run", lock)
	} else if !errors.Is(err, os.ErrNotExist) {
		t.Errorf("inspecting %s: %v", lock, err)
	}
}

func s058Registry(content string) func(*testing.T, *testCLI) []string {
	return func(t *testing.T, c *testCLI) []string {
		s058WriteRegistry(t, c.Overlay(), content)
		return nil
	}
}

const s058StrandedBannerRegistry = s058CleanRegistry + `
# ============ npm registry ============

["sys-apps/pnpm"]
url = "https://registry.npmjs.org/pnpm"
parser = "json"
path = "dist-tags.latest"
comments = """
pnpm — npm package, stable channel.
"""
# END
`

func s058AutoupdateRows() []s058Row {
	return []s058Row{
		{name: "no mode prints help and succeeds", args: []string{"overlay", "autoupdate"}, want: 0, usage: true},
		{name: "--fix without --lint", args: []string{"overlay", "autoupdate", "--fix"}, want: 1, once: "--fix repairs what --lint reports"},
		{name: "--except without --mark-auto-disabled", args: []string{"overlay", "autoupdate", "--except", "app-misc/foo"}, want: 1, once: "--except names the entries --mark-auto-disabled must not stamp"},
		{name: "--concurrency 0", args: []string{"overlay", "autoupdate", "--concurrency", "0"}, want: 1, once: "--concurrency must be in range [1, 100], got 0"},
		{name: "--only with an unknown type", args: []string{"overlay", "autoupdate", "--only", "weird"}, want: 1, once: `--only must be "bin" or "source", got "weird"`},
		{name: "--lint with findings", args: []string{"overlay", "autoupdate", "--lint"}, setup: s058Registry(s058StrandedBannerRegistry), want: 1, once: "issue(s)", after: s058LockGone},
		{name: "--lint on a clean registry", args: []string{"overlay", "autoupdate", "--lint"}, setup: s058Registry(s058CleanRegistry), want: 0, after: s058LockGone},
		{name: "--lint with no registry", args: []string{"overlay", "autoupdate", "--lint"}, want: 1, after: s058LockGone},
		{name: "--mark-auto-disabled with an --except naming no record", args: []string{"overlay", "autoupdate", "--mark-auto-disabled", "--except", "nosuch/pkg"}, setup: s058Registry(s058CleanRegistry), want: 1, once: "refusing to migrate", after: s058LockGone},
		{name: "--mark-auto-disabled with nothing to migrate", args: []string{"overlay", "autoupdate", "--mark-auto-disabled"}, setup: s058Registry(s058CleanRegistry), want: 0, after: s058LockGone},
		{name: "--list with nothing pending", args: []string{"overlay", "autoupdate", "--list"}, want: 0, after: s058LockGone},
		// Would wrongly become 0: a repair whose write is refused is a failure.
		{name: "--lint --fix --yes whose write is refused", args: []string{"overlay", "autoupdate", "--lint", "--fix", "--yes"}, setup: s058RefusedRegistryWrite, want: 1, once: "The registry was NOT repaired:", after: s058LockGone},
	}
}

// s058RefusedRegistryWrite writes a registry with one fixable finding and makes
// both the file and its directory read-only, so the repair's write is refused.
func s058RefusedRegistryWrite(t *testing.T, c *testCLI) []string {
	t.Helper()
	if os.Geteuid() == 0 {
		t.Skip("root ignores file permissions, so the refused write this row needs cannot be produced")
	}
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
	return nil
}

func TestS058AutoupdateRegistryModesKeepTheirExitCodes(t *testing.T) {
	s058RunRows(t, s058AutoupdateRows())
}

// s058CheckFixture serves {"version": "1.0.0"} and writes one record per
// package: good ones read "version" and succeed, bad ones read a missing field
// and fail at parse time (no retry, no network beyond the local server).
func s058CheckFixture(good, bad []string) func(*testing.T, *testCLI) []string {
	return func(t *testing.T, c *testCLI) []string {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]string{"version": "1.0.0"})
		}))
		t.Cleanup(server.Close)
		var b strings.Builder
		entry := func(pkg, path string) {
			b.WriteString("[\"" + pkg + "\"]\nurl = \"" + server.URL + "\"\nparser = \"json\"\npath = \"" + path + "\"\n\n")
			writeExitTestEbuild(t, c.Overlay(), pkg, "0.9.0")
		}
		for _, pkg := range good {
			entry(pkg, "version")
		}
		for _, pkg := range bad {
			entry(pkg, "nonexistent")
		}
		s058WriteRegistry(t, c.Overlay(), b.String())
		return nil
	}
}

func s058CheckBatchRows() []s058Row {
	args := []string{"overlay", "autoupdate", "--check", "--force"}
	return []s058Row{
		// Would wrongly collapse: total failure is 2, not 1.
		{name: "total failure", args: args, setup: s058CheckFixture(nil, []string{"cat-a/pkg1", "cat-b/pkg2"}), want: 2, after: s058LockGone},
		{name: "partial failure", args: args, setup: s058CheckFixture([]string{"cat-a/pkg1"}, []string{"cat-b/pkg2"}), want: 1, after: s058LockGone},
		{name: "every package succeeds", args: args, setup: s058CheckFixture([]string{"cat-a/pkg1", "cat-b/pkg2"}, nil), want: 0, after: s058LockGone},
	}
}

func TestS058AutoupdateCheckBatchKeepsItsExitCodes(t *testing.T) {
	s058RunRows(t, s058CheckBatchRows())
}
