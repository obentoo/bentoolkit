package report

import (
	"fmt"
	"strings"
)

// SnapshotRun is everything one `snapshot run` established: the subvolumes it
// set out to operate on, the steps it ran over them, and the two counts that
// summarize the lot. It is the Payload behind KindSnapshotRun.
//
// Not one field is a package, a version, an ebuild or an atom, and Run did not
// grow to carry them: everything domain-specific — a subvolume, a pipeline
// step, a ship target — is here, so the envelope never mentions btrfs.
//
// The manifest counts REGENERATED targets and this counts SUCCEEDED steps. The
// arity matches by coincidence; a universal tally on the envelope would make
// one command lie in the other's vocabulary, which is why these two ints are
// not reused from ManifestRun.
//
// Whether the run finished is the envelope's (Run.Complete, Run.NotEvaluated);
// restating it here would be a second place for a finished run to be described
// as a partial one.
type SnapshotRun struct {
	// Subvolumes is every subvolume the run set out to operate on, in
	// configuration order, whatever became of each.
	//
	// The JSON key is singular on purpose: it names the question a consumer
	// asks — "which subvolume did this run operate on?" — while the Go name
	// names what the field holds, a list, because a run covers every subvolume
	// its configuration names.
	//
	// It is the PLAN, and Steps below is what the run reached; neither is
	// derived from the other. A failed create skips that subvolume's remaining
	// steps and a cancelled run never produces steps for the rest, yet this
	// field still names every subvolume, which is what lets a reader see WHICH
	// one is missing — the envelope's NotEvaluated says only how many.
	Subvolumes []string `json:"subvolume"`
	// Steps is every pipeline step that reached an outcome, in the order the
	// run established them.
	//
	// A nil slice reaches the JSON export as null and an empty one as [], and
	// the two say different things: the first is a producer that established
	// nothing, the second a run that ran no step at all. Neither is rewritten
	// into the other on the way out.
	//
	// The ORDER is information rather than presentation — "prune failed after
	// create succeeded" and "create failed and prune never ran" are different
	// runs, and position is the only thing that distinguishes them in a flat
	// list — so nothing here or downstream sorts it.
	Steps []SnapshotStep `json:"steps"`
	// Ok is how many steps did what they were asked, and Failed is how many did
	// not.
	//
	// They are FIELDS although the rows below could be walked to re-derive
	// them, for the reason ManifestRun.Ok gives at length: this document is
	// read by a program holding the exported file, and a program holding a file
	// cannot call a method. A count that exists only as a method is a count the
	// JSON export does not carry.
	//
	// Neither carries omitempty, and that is load-bearing: a zero dropped from
	// the document reads as "the producer never said", which conflates "no step
	// failed" with "nobody counted" — and a run in which everything succeeded
	// is the common case that would lose its zero.
	Ok     int `json:"ok"`
	Failed int `json:"failed"`
}

// SnapshotStep is what the run established about one step of its pipeline.
//
// # A step here is a STAGE, not a subprocess
//
// The two are different granularities and only one of them is legible. A stage
// is create, prune or ship: the thing the run set out to do, named by the
// manager that sequences them. A subprocess is `btrbk` or `snapper`, and one
// stage may invoke none of them, one, or several — the ssh ship moves no bytes
// itself, and a snapper subvolume takes several calls to create one snapshot.
// A report built from subprocesses would print "snapper, snapper, snapper" and
// leave the operator to guess which was the prune, which is why the step this
// type carries is the caller's semantic one.
type SnapshotStep struct {
	// Subvolume is the subvolume this step operated on.
	//
	// It may be empty, and empty is a fact rather than a gap: a step that
	// sweeps a remote holding objects from every subvolume is not attributable
	// to one, and the producer leaves it blank to say so. Nothing here invents
	// a placeholder for it — a made-up "all" would be this type answering a
	// question the run did not.
	Subvolume string `json:"subvolume"`
	// Step is what the run was doing: create, prune, ship or gfs, in the
	// producer's own words.
	Step string `json:"step"`
	// Target is the ship destination this step served, and empty for a step
	// that serves no destination.
	//
	// It is the destination's NAME — the one configured for it, or the driver
	// word when it was left unnamed — and never its address. A ship target's
	// address carries a host, a user and a remote path, and this value travels
	// into a file an operator may attach to a bug report.
	//
	// That rule covers Subvolume, Step and Target, each a name this package
	// controls the shape of. It does NOT cover Error, which is producer stderr
	// taken verbatim and says so itself; claiming the whole type address-free
	// would be a false safety claim about the field most likely to carry one.
	Target string `json:"target"`
	// Success reports that the step did what it was asked.
	//
	// It is derived once, by the adapter, from the producer's own status word,
	// and the payload's counts are taken from THAT SAME expression — so the
	// word printed on a row and the number printed above it cannot describe
	// different runs.
	Success bool `json:"success"`
	// Error is why this step failed, in the producer's own words, and empty for
	// a step that did not fail.
	//
	// It carries the failing command's own stderr, taken verbatim and never
	// reworded: a rewritten diagnostic is one the operator cannot search for or
	// reproduce.
	//
	// VERBATIM CUTS BOTH WAYS: this field is the exception to Target's
	// no-address rule. A failed `btrbk ssh` send writes `user@host:/path`, so a
	// remote address CAN reach an export through here, and nothing scrubs it.
	// An export that includes a FAILED ship step should be read before it is
	// shared; redaction, if ever wanted, belongs at the producer that knows
	// which text is an address.
	//
	// It carries no subvolume and no step name: both sit beside it in this same
	// row, and a copy of either in here would print twice on every failure.
	Error string `json:"error"`
}

// Sections is the snapshot run as structure: what it says, in order, with every
// value at full length and not one decision about how it will look.
//
// # One block, because the run establishes one thing
//
// A snapshot run answers one question — for each step, did it do what it was
// asked — and a second heading would sit above content the first already
// carries. A report padded with empty blocks trains a reader to skim past the
// one that matters.
//
// # ShowAll decides what is LISTED and never what is counted
//
// A successful step is listed when SectionOptions.ShowAll asks and counted
// otherwise, with the omission stated in words. The count comes from the run's
// own Ok, never from the rows, so the number cannot move when the listing does.
// A FAILED step is always listed: it is the reason an operator reads a snapshot
// report at all.
func (r SnapshotRun) Sections(opts SectionOptions) []Section {
	return []Section{snapshotSection(r, opts.ShowAll)}
}

// snapshotSection is the block: the subvolumes the run covered, the counts, the
// rows they were taken over, and the sentences saying what was left out and why.
func snapshotSection(r SnapshotRun, listEvery bool) Section {
	// Which subvolume the run operated on is stated BEFORE anything else and on
	// every path out of this function, including the two that return no rows,
	// because the answer must not depend on --all: a run in which every step
	// succeeded lists no row by default, and the subvolume would otherwise
	// vanish from the report of the run that went perfectly.
	s := Section{Title: "Snapshot Run", Lead: []string{snapshotScope(r)}}

	// A run with no step still produces a report, and says so in a sentence
	// rather than as an empty table — a report is what an operator gets INSTEAD
	// of a crash, so reading one must not be where the crash arrives.
	//
	// The guard is on the COUNTS, not on the rows. Nothing in the type makes Ok,
	// Failed and Steps agree, and the sentence below is a claim about the
	// counts: a payload carrying "2 succeeded, 1 failed" and no rows must not
	// announce that nothing was counted.
	if r.Ok+r.Failed == 0 && len(r.Steps) == 0 {
		s.Lead = append(s.Lead, "No step has an outcome to report, so nothing is counted as succeeded or as failed.")
		return s
	}

	s.Lead = append(s.Lead, snapshotTally(r))

	// Counts without the rows they were taken over. The counts are still
	// stated, because they are what the run established; what is missing is the
	// per-step detail, and a report states what it omitted rather than
	// omitting it silently.
	if len(r.Steps) == 0 {
		s.Notes = []string{"No per-step row reached this report, so the counts above are stated without the list they were taken over."}
		return s
	}

	s.Rows.Headers = []string{"SUBVOLUME", "STEP", "STATE"}
	for _, step := range r.Steps {
		if step.Success && !listEvery {
			// Counted in the note below, not listed here.
			continue
		}
		s.Rows.Rows = append(s.Rows.Rows, Row{
			Cells:  []string{step.Subvolume, snapshotStepLabel(step), snapshotState(step)},
			Detail: snapshotDetail(step),
		})
	}

	s.Notes = snapshotNotes(r, listEvery)

	return s
}

// snapshotScope says which subvolume the run operated on.
//
// # It counts as well as naming, and the count is len()
//
// This is the one number in the report taken over a list rather than
// established by the run, and it is safe precisely because the list IS the
// fact: Subvolumes is the plan the run was given, so its length is how many
// subvolumes were planned, by definition. That is a different thing from Ok and
// Failed, which describe what HAPPENED and could not be recovered from any
// slice this payload holds.
//
// # A run with no subvolume says so rather than printing an empty list
//
// snapshot.Config warns at load time that an empty engine.subvolumes means
// nothing will be snapshotted, and it does not refuse to run. So this arm is
// reachable, and what it must not do is print a sentence that trails off into
// nothing — a reader meeting "The run operated on 0 subvolume(s): ." would be
// looking at a bug rather than at an answer.
func snapshotScope(r SnapshotRun) string {
	if len(r.Subvolumes) == 0 {
		return "The run was given no subvolume to operate on, so no step below is attributed to one."
	}
	return fmt.Sprintf("The run operated on %d subvolume(s): %s.",
		len(r.Subvolumes), strings.Join(r.Subvolumes, ", "))
}

// snapshotTally says in one sentence how the steps came out.
//
// The total is Ok+Failed rather than len(Steps) for the reason the guard above
// gives — the counts are what the run established, and a total taken over a
// different field could disagree with the two numbers it introduces.
func snapshotTally(r SnapshotRun) string {
	return fmt.Sprintf("%d step(s) ran: %d succeeded, %d failed.", r.Ok+r.Failed, r.Ok, r.Failed)
}

// snapshotStepLabel is the word the STEP column prints: the stage, and the ship
// destination it served when it served one.
//
// # The destination is folded into the step rather than given a column
//
// A fourth column would be empty on every create and every prune — the majority
// of rows in every run — and a column that is blank most of the time teaches a
// reader to stop looking at it, which is exactly when the one populated cell
// matters. Folding keeps both values on the row and costs nothing to a machine
// reader: the exported document carries Step and Target as separate keys, so
// nothing has to parse this string back apart.
func snapshotStepLabel(step SnapshotStep) string {
	if step.Target == "" {
		return step.Step
	}
	return fmt.Sprintf("%s (%s)", step.Step, step.Target)
}

// snapshotState is the word the STATE column prints. Only the WORDING is
// decided here; which of the two applies was decided by the run, and Success is
// the one field that says it.
func snapshotState(step SnapshotStep) string {
	if step.Success {
		return "succeeded"
	}
	return "failed"
}

// snapshotDetail is why a failed step failed: the producer's sentence, whole.
//
// # A successful step has no detail, and that is not an omission
//
// It returns the empty string, which every writer already omits rather than
// printing a bare indent. A step that did what it was asked produced no
// diagnostic to keep — the runner records an error only on failure — so there
// is nothing being held back here for a note to apologise for.
//
// # The sentence is FOLDED, never shortened
//
// Every non-whitespace character survives; only the line structure is collapsed,
// and it has to be. A Row.Detail is one line by definition — the plain writer
// prints it under its row and the Markdown writer makes it a table cell, and a
// raw newline corrupts the layout of both — while the verbatim string stays on
// SnapshotStep.Error, which is what the JSON export carries. A command's stderr
// arrives here with its own newlines in it, because the runner joins stderr onto
// the error, so this is not a hypothetical case.
func snapshotDetail(step SnapshotStep) string {
	if step.Success {
		return ""
	}
	return foldToOneLine(step.Error)
}

// snapshotNotes is what the table left out, and why.
func snapshotNotes(r SnapshotRun, listEvery bool) []string {
	var notes []string

	// Conditional on there having BEEN a success: a run in which every step
	// failed listed every one of its rows, so it has nothing to account for and
	// gets no sentence at all.
	if r.Ok > 0 {
		if listEvery {
			// The count is stated even when every row is present, for the
			// reason the check's own up-to-date note is: a count never depends
			// on which rows were listed, so the sentence keeps its shape in
			// both directions and only its tail changes.
			notes = append(notes, fmt.Sprintf("%d step(s) succeeded and are listed above.", r.Ok))
		} else {
			notes = append(notes, fmt.Sprintf("%d step(s) succeeded and are not listed; pass --all to list them.", r.Ok))
		}
	}

	// The other omission, stated once for the run because it is one decision
	// rather than a property of any row. The producer times every stage and
	// persists the timings with the run result, which is where `snapshot
	// status` reads them from; carrying them here would put a value in the
	// report that differs between two runs of the same shape, and this report
	// is read to find out WHAT happened.
	return append(notes,
		"How long each step took is not carried here: the run's own result file keeps the timings, and 'snapshot status' is what reads them back.")
}
