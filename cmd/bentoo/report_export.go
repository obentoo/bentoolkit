package main

// The one export path, shared by every command that produces a report.
//
// Story 046, sub-task 4.5 — R3.4, R3.5.
//
// --export moved to the root beside --ui and --all (D4), so "what happens when
// a report is exported" stopped being `overlay autoupdate`'s question and
// became the CLI's. This file answers it once. `overlay manifest`, `snapshot
// run` and `overlay validate --json` hand exportReport their own report.Run and
// inherit every decision below without restating one of them — which is the
// point, because a decision restated per command is a decision that ends up
// made differently per command, and the operator learns the rule from whichever
// command they happened to try first.
//
// # Why the writing itself is NOT in this file
//
// exportFormatFor, writeExport and renderExport stay in
// overlay_autoupdate_ui.go, and that is not where a reader would look for them.
// The reason is a source-text guard: TestPlainExportDoesNotInheritSkipPlan
// asserts that overlay_autoupdate_ui.go builds EXACTLY ONE render.Options
// literal — the plain export's — and that the file never names the field which
// omits the plan. Moving renderExport out empties that file of the literal the
// guard measures, and the guard fails on its own vacuity check: it would then
// be asserting the absence of a field in a file that no longer builds the
// value the field belongs to.
//
// So the mechanics stay put, and what moves here is the HANDLING — whether an
// export happens at all, how its failure is surfaced, and what it is structurally
// unable to touch. That is also what every future caller actually needs: those
// three functions are already package-level and already reachable from any file
// in cmd/bentoo, and it is the four lines below that each command would otherwise
// have had to copy.

import (
	"github.com/obentoo/bentoolkit/internal/common/logger"
	"github.com/obentoo/bentoolkit/internal/common/report"
)

// exportReport writes run to the path --export named, in the syntax that path's
// extension selects, and does nothing at all when no path was named (R3.4).
//
// # It takes a report.Run and NOTHING ELSE
//
// Not the command, not its flags, not a set of Options. R3.4 says the file
// carries the complete report whatever --all and --ui asked of the terminal, and
// the surest way to keep that true across four commands is to give this function
// no parameter through which a screen setting could arrive. What the export asks
// the report to say is exportContent's answer, and it is a constant.
//
// The consequence worth stating out loud: an export cannot be shortened. Not by
// --all, not by the terminal's width, not by a caller in a hurry. A record is
// kept precisely because the terminal is gone, and one missing exactly what the
// screen dropped answers no question later.
//
// # It reads --export rather than being handed a path
//
// The flag is the ROOT's now, so there is one path per invocation and every
// command means the same thing by it. A parameter would let two callers disagree
// about which path "the export" is, and would make each call site re-plumb a
// value neither of them owns. writeExport is still the path-taking function
// underneath, which is what keeps the writing testable without a global.
//
// # It returns nothing, and that IS R3.5's status half
//
// R3.5 says an unwritable path must leave the run exiting with the status it
// would otherwise have had. A function with no result cannot alter one: there is
// no value for a failed export to travel back through, so the caller's own status
// reaches func main untouched — by construction rather than by every caller
// remembering to ignore something. An export is an ADDITIONAL copy of an answer
// already delivered, and a display convenience that could decide whether a run
// counted as successful would be the tail wagging the dog.
//
// # It must be called AFTER the terminal render
//
// The other half of R3.5 — an unwritable path costs the operator nothing they
// already paid for — is a property of the ORDER, and the order lives at the call
// site rather than here. Exporting first would let a bad path abort a run before
// its report was drawn. presentCheckReport renders and then calls this, and
// TestUnwritableExportStillRendersToTheTerminal asserts that pairing at the one
// place both happen; a new caller owes the same ordering.
func exportReport(run report.Run) {
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
		logger.Warn("%v", err)
	}
}
