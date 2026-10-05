package main

// Authored for story 058, sub-task 4.2 — R1.1, R1.3, R2.5, R2.8, R2.9.
//
// overlay analyze, compare and staged clean are RunE commands and keep their
// codes. The hostile rows: analyze --all under a cancelled caller context
// records every package as failed and must exit 2 (total batch failure) — a
// global "cancelled means 130" mapping, or a code collapsing into 1, both break
// it — while compare's explicit "no packages" exit 0 must not become 1.
//
// Red on arrival: the three commands are still Run commands.

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

var s058ReportCommands = []string{"overlay analyze", "overlay compare", "overlay staged clean"}

func TestS058OverlayReportCommandsReturnThroughRunE(t *testing.T) {
	if bad := s058NotOnRunE(t, s058ReportCommands...); len(bad) > 0 {
		t.Errorf("these commands still end the process themselves instead of returning their outcome (R1.1):\n  %s", strings.Join(bad, "\n  "))
	}
}

func s058ReportRows() []s058Row {
	return []s058Row{
		{name: "analyze --all whose every package fails under a cancelled caller context", args: []string{"overlay", "analyze", "--all"},
			setup: func(t *testing.T, c *testCLI) []string {
				writeExitTestEbuild(t, c.Overlay(), "app-misc/s058-one", "1.0.0")
				writeExitTestEbuild(t, c.Overlay(), "app-misc/s058-two", "1.0.0")
				return nil
			},
			ctx: func(t *testing.T) context.Context {
				ctx, cancel := context.WithCancel(context.Background())
				cancel()
				return ctx
			},
			want: 2},
		{name: "compare on an overlay with no packages", args: []string{"overlay", "compare"},
			setup: func(t *testing.T, c *testCLI) []string {
				s058LocalGentoo(t, c)
				return nil
			},
			want: 0},
		{name: "analyze --all with nothing to analyze", args: []string{"overlay", "analyze", "--all"}, want: 0},
		{name: "staged clean with nothing staged", args: []string{"overlay", "staged", "clean"}, want: 0},
		{name: "analyze with no package prints help and fails", args: []string{"overlay", "analyze"}, want: 1, usage: true},
		{name: "compare with --concurrency 0", args: []string{"overlay", "compare", "--concurrency", "0"}, want: 1, once: `msg="--concurrency must be in range [1, 100]" concurrency=0`},
		{name: "staged clean with an argument", args: []string{"overlay", "staged", "clean", "extra"}, want: 1, usage: true},
	}
}

func TestS058OverlayReportCommandsKeepTheirExitCodes(t *testing.T) {
	s058RunRows(t, s058ReportRows())
}

// s058LocalGentoo configures the ::gentoo repository as `provider: local` over
// an empty temporary tree that carries only profiles/repo_name.
//
// Without it, compare resolves "gentoo" through the api.gentoo.org registry and
// builds a GitHub provider that asks api.github.com for its rate limit, sending
// whatever token the developer's machine resolves: the row then passed or failed
// with the network and the host's API quota. A config repository wins over the
// registry in ResolveRepository, and a local provider builds no GitHub client,
// so the run stays on the disk. The block is appended through
// appendHarnessConfig, the way seedCompareFixture does it, because the overlay
// path newTestCLI already wrote is what makes the run a run.
func s058LocalGentoo(t *testing.T, c *testCLI) {
	t.Helper()
	gentoo := t.TempDir()
	profiles := filepath.Join(gentoo, "profiles")
	if err := os.MkdirAll(profiles, 0o750); err != nil {
		t.Fatalf("mkdir %s: %v", profiles, err)
	}
	if err := os.WriteFile(filepath.Join(profiles, "repo_name"), []byte("gentoo\n"), 0o600); err != nil {
		t.Fatalf("write repo_name: %v", err)
	}
	appendHarnessConfig(t, c, "repositories:\n  gentoo:\n    provider: local\n    path: "+gentoo+"\n")
}
