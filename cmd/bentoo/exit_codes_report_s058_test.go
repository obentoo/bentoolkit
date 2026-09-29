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
		{name: "compare on an overlay with no packages", args: []string{"overlay", "compare"}, want: 0},
		{name: "analyze --all with nothing to analyze", args: []string{"overlay", "analyze", "--all"}, want: 0},
		{name: "staged clean with nothing staged", args: []string{"overlay", "staged", "clean"}, want: 0},
		{name: "analyze with no package prints help and fails", args: []string{"overlay", "analyze"}, want: 1, usage: true},
		{name: "compare with --concurrency 0", args: []string{"overlay", "compare", "--concurrency", "0"}, want: 1, once: "--concurrency must be in range [1, 100], got 0"},
		{name: "staged clean with an argument", args: []string{"overlay", "staged", "clean", "extra"}, want: 1, usage: true},
	}
}

func TestS058OverlayReportCommandsKeepTheirExitCodes(t *testing.T) {
	s058RunRows(t, s058ReportRows())
}
