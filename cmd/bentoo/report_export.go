package main

// The one export path, shared by every command that produces a report.
//
// --export is a root flag beside --ui and --all, so "what happens when a report
// is exported" is the CLI's question, answered once here. `overlay manifest`,
// `snapshot run` and `overlay validate --json` hand exportReport their own
// report.Run and inherit every decision below: a decision restated per command
// ends up made differently per command.
//
// The writing itself (exportFormatFor, writeExport, renderExport) stays in
// overlay_autoupdate_ui.go because of a source-text guard:
// TestPlainExportDoesNotInheritSkipPlan asserts that file builds EXACTLY ONE
// render.Options literal, the plain export's, and fails its own vacuity check
// if the literal moves out. What lives here is the HANDLING — whether an export
// happens, how its failure surfaces, and what it cannot touch.

import (
	"log/slog"

	"github.com/obentoo/bentoolkit/internal/common/report"
)

// exportReport writes run to the path --export named, in the syntax that path's
// extension selects, and does nothing at all when no path was named.
//
// It takes a report.Run and NOTHING ELSE — no command, flags or Options — so no
// screen setting can reach the file: the export carries the complete report
// whatever --all, --ui or the terminal width asked of the screen. A record is
// kept because the terminal is gone; one missing what the screen dropped
// answers no question later.
//
// It reads the root's --export rather than taking a path, so every command
// means the same path by "the export"; writeExport underneath takes the path
// and stays testable without a global.
//
// It returns nothing, so an unwritable path cannot change the run's exit
// status: there is no value for a failure to travel back through.
//
// Callers must call it AFTER the terminal render, so a bad path never costs the
// operator the report already drawn; TestUnwritableExportStillRendersToTheTerminal
// pins that order at presentCheckReport, and a new caller owes the same.
func exportReport(log *slog.Logger, run report.Run) {
	if autoupdateExport == "" {
		return
	}

	if err := writeExport(autoupdateExport, run); err != nil {
		// Warn, never fatal, and on stderr because that is where the logger
		// writes: the report itself goes to stdout, and a run being piped into
		// a file or a diff must not find an export failure in the middle of it.
		//
		// The message is passed through rather than re-wrapped. writeExport
		// already names the path it attempted and what it was doing to it —
		// creating, writing, completing — which is the whole of what an
		// operator needs to fix a missing directory, a read-only mount or a
		// full disk. A second wrap here would print the path twice and add no
		// identifier the first one lacks.
		log.Warn("writing the report export: failed", "err", err)
	}
}
