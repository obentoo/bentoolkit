package report

import (
	"fmt"
	"strings"
)

// ManifestRun is everything one `overlay manifest` run established: the targets
// it was given, what became of each of them, and the two counts that summarize
// the lot. It is the Payload behind KindOverlayManifest.
//
// The type and its Sections share one file, unlike AutoupdateCheck, whose fields
// in model.go predate the Payload interface: one file per payload is what makes
// "a new kind of run is a new file" true, instead of a model.go that grows one
// struct per command.
//
// Its tally has two columns because a target either had its Manifest
// regenerated or not; one universal tally on the envelope would have to lose the
// check's four or invent columns this run has no answer for. Whether the run
// finished is the envelope's (Run.Complete, Run.NotEvaluated) and is absent
// here, so a finished run cannot be described as a partial one twice over.
type ManifestRun struct {
	// Targets is every package the run was given, in the order it was given
	// them, whatever became of each.
	//
	// A nil slice reaches the JSON export as null and an empty one as [], and
	// the two say different things: the first is a producer that established
	// nothing, the second a run that was handed no target at all. Neither is
	// rewritten into the other on the way out.
	Targets []ManifestTarget `json:"targets"`
	// Ok is how many targets had their Manifest regenerated, and Failed is how
	// many did not.
	//
	// They are FIELDS although overlay.ManifestResult derives the same answers
	// with methods: that type is read by Go code, this one by a program holding
	// the exported document, and a count that exists only as a method is a
	// count the JSON export does not carry.
	//
	// They are established by the run, never summed from Targets: a dry run
	// marks every previewed target Success false, so a sum would report every
	// package failed in a run that invoked nothing. The adapter branches on the
	// preview BEFORE it counts, as Run.Complete's fact travels from the run.
	//
	// Neither carries omitempty: a dropped zero reads as "the producer never
	// said", and "no target failed" is the common case that would lose it.
	Ok     int `json:"ok"`
	Failed int `json:"failed"`
	// DryRun reports that the run was a preview: the targets below were
	// resolved and listed, and nothing was invoked for any of them.
	//
	// It is a fact about what the run DID, not about how it is displayed —
	// which is what earns it a place in a model that holds no presentation. It
	// is also the one thing that explains the shape of everything else here: on
	// a preview Ok and Failed are both zero while Targets is full, and without
	// this field a reader meeting those three numbers would have to guess
	// whether the run failed silently or never started. Sections states it in
	// words for the same reason.
	DryRun bool `json:"dry_run"`
}

// ManifestTarget is what the run established about one package.
type ManifestTarget struct {
	// Package is the atom, "<category>/<package>", as an operator types it.
	//
	// The two halves are joined by the adapter rather than carried apart,
	// because every reader of this field prints the atom: carrying them
	// separately would leave each renderer to re-join them with a separator of
	// its own, and two renderers spelling one package name differently is the
	// disagreement the model exists to make impossible.
	Package string `json:"package"`
	// Success reports that this target's Manifest was regenerated.
	//
	// It is false for a target that was merely previewed, and that reading is
	// the producer's: "did not succeed" covers "never attempted". Which of the
	// two happened is said by DryRun above, once for the run, rather than by a
	// third state per target — a distinction the run makes once cannot drift
	// between rows.
	Success bool `json:"success"`
	// Error is why this target failed, in the producer's own words, and empty
	// for a target that did not fail.
	//
	// It carries no atom: Package is right beside it, and a copy of the atom
	// in here would be printed twice on every failure line.
	Error string `json:"error"`
	// Output is what the failing command printed, verbatim — the diagnostic the
	// operator acts on, kept because by the time a report is rendered the
	// terminal that streamed it live is gone.
	//
	// It is populated only on failure, and that is the producer's decision
	// rather than this type's: the capture is unbounded, and a whole-overlay
	// run would otherwise hold one copy per target for no reader. Sections
	// states that omission rather than leaving a reader to wonder where a
	// successful target's output went.
	Output string `json:"output"`
}

// Sections is the manifest run as structure: what it says, in order, with every
// value at full length and not one decision about how it will look.
//
// It is one block because the run establishes one thing: for each target, was
// the Manifest regenerated. A second section would be a heading that says
// nothing the first does not, and padding trains a reader to skim.
//
// ShowAll decides what is LISTED and never what is counted: a target that
// succeeded is listed only when asked, the count comes from the run's own Ok so
// it cannot move with the listing, and a note states that the list is short and
// why. A FAILURE is always listed, whatever the flag, because it is why the
// operator is reading the report. A preview lists every target and never reads
// ShowAll: on a dry run the names ARE the finding.
func (r ManifestRun) Sections(opts SectionOptions) []Section {
	return []Section{manifestSection(r, opts.ShowAll)}
}

// manifestSection is the block: the counts, the rows they were taken over, and
// the sentences saying what was left out of the rows and why.
func manifestSection(r ManifestRun, listEvery bool) Section {
	s := Section{Title: "Manifest Regeneration"}

	// A run with no row still produces a report, and says so in a sentence
	// rather than as an empty table: a report is what an operator gets INSTEAD
	// of a crash, so reading one must not be where the crash arrives.
	//
	// Two runs arrive here: one handed no target, and one interrupted before any
	// target reached an outcome (the producer hands back only the targets it
	// answered for). The sentence claims no cause — "no target was selected"
	// would be false after ctrl+c — and Run.Sections adds `Run Interrupted`
	// above this block when the second is the case.
	//
	// The guard is on the COUNTS, not the rows: Ok, Failed and Targets are
	// separate fields nothing forces to agree, and the sentence is a claim about
	// the counts. Judged from the rows, a payload carrying "3 regenerated, 1
	// failed" and no rows would be told it did nothing.
	if r.Ok+r.Failed == 0 && len(r.Targets) == 0 {
		s.Lead = []string{"No target has an outcome to report, so nothing is counted as regenerated or as failed."}
		return s
	}

	// Counts without the rows they were taken over. The counts are still stated,
	// because they are what the run established; what is missing is the
	// per-target detail, and a report states what it omitted rather than
	// omitting it silently.
	if len(r.Targets) == 0 {
		s.Lead = []string{fmt.Sprintf("%d target(s) processed: %d regenerated, %d failed.",
			r.Ok+r.Failed, r.Ok, r.Failed)}
		s.Notes = []string{"No per-target row reached this report, so the counts above are stated without the list they were taken over."}
		return s
	}

	if r.DryRun {
		return manifestPreview(s, r)
	}

	s.Lead = []string{fmt.Sprintf("%d target(s) processed: %d regenerated, %d failed.",
		r.Ok+r.Failed, r.Ok, r.Failed)}

	s.Rows.Headers = []string{"PACKAGE", "STATE"}
	for _, target := range r.Targets {
		if target.Success && !listEvery {
			// Counted in the note below, not listed here.
			continue
		}
		s.Rows.Rows = append(s.Rows.Rows, Row{
			Cells:  []string{target.Package, manifestState(target)},
			Detail: manifestDetail(target),
		})
	}

	s.Notes = manifestNotes(r, listEvery)

	return s
}

// manifestPreview is the block a --dry-run produces: the targets, and the plain
// statement that nothing happened to any of them.
//
// # The count in the lead is len(Targets), and NOT Ok+Failed
//
// Those two are zero here, deliberately and correctly — no target succeeded and
// none failed, because none was attempted — so a lead that read them would
// announce "0 target(s)" over a table of names. What the operator asked for is
// how many the run WOULD process, and that is the length of the list under the
// sentence.
//
// # The note is the whole point of this arm
//
// Without it a reader meets a full table of packages beside two zero counts and
// has to guess which of the two is lying. Saying "nothing ran" once, in words,
// is what makes both readings correct at the same time, and it states the
// omission: this run deliberately established nothing about any
// of the rows it is showing.
func manifestPreview(s Section, r ManifestRun) Section {
	s.Lead = []string{fmt.Sprintf("Dry run: %d target(s) would have their Manifest regenerated.", len(r.Targets))}

	s.Rows.Headers = []string{"PACKAGE"}
	for _, target := range r.Targets {
		s.Rows.Rows = append(s.Rows.Rows, Row{Cells: []string{target.Package}})
	}

	s.Notes = []string{
		"Nothing ran: no command was invoked and no Manifest was written, so no target above is counted as regenerated or as failed.",
	}

	return s
}

// manifestNotes is what the table left out, and why.
//
// Both sentences are conditional on there having been a success, because both
// are about the successful targets: a run in which everything failed listed
// every one of its rows and kept every one of their diagnostics, so it has
// nothing to apologise for and gets no note at all.
func manifestNotes(r ManifestRun, listEvery bool) []string {
	if r.Ok == 0 {
		return nil
	}

	var notes []string
	if listEvery {
		// The count is stated even when the rows are all present, for the
		// reason the check's own up-to-date note is: a count never depends on
		// which rows were listed, so the sentence is the same shape in
		// both directions and only its tail changes.
		notes = append(notes, fmt.Sprintf("%d target(s) were regenerated and are listed above.", r.Ok))
	} else {
		notes = append(notes, fmt.Sprintf("%d target(s) were regenerated and are not listed; pass --all to list them.", r.Ok))
	}

	return append(notes,
		"What a target that succeeded printed is not kept: it was shown while the run was live, and holding a verbatim copy of every worker's output would grow with the overlay. A target that failed carries its own, in full.")
}

// manifestState is the word the STATE column prints. Only the wording is decided
// here; which of the two applies was decided by the run, and Success is the one
// field that says it.
func manifestState(target ManifestTarget) string {
	if target.Success {
		return "regenerated"
	}
	return "failed"
}

// manifestDetail is why a failed target failed: the producer's sentence, and
// then everything its command printed.
//
// A successful target returns the empty string, which every writer omits; its
// output was never kept (see ManifestTarget.Output), and manifestNotes says so
// once for the run rather than once per row.
//
// The captured output is JOINED, not shortened: every non-whitespace character
// survives and only the line structure is folded, because a Row.Detail is one
// line (a raw newline corrupts both the plain and the Markdown layout). The
// verbatim bytes stay on ManifestTarget.Output for the JSON export, so nothing
// is lost. This matches the check's failureDetail; cutting a long sentence to a
// line budget is a renderer's decision, marked where it cuts.
func manifestDetail(target ManifestTarget) string {
	if target.Success {
		return ""
	}

	var parts []string
	if target.Error != "" {
		parts = append(parts, target.Error)
	}
	// Labelled, because the two halves are different kinds of statement: the
	// first is the toolkit's account of the failure and the second is the
	// child's own. A reader meeting them run together would have no way to tell
	// where one ended.
	if printed := foldToOneLine(target.Output); printed != "" {
		parts = append(parts, "output: "+printed)
	}

	return strings.Join(parts, "; ")
}

// foldToOneLine collapses every run of whitespace in s to a single space.
//
// strings.Fields splits on any whitespace and discards none of the text between,
// so the result holds every visible character the original did, in order — a
// fold of the layout rather than a loss of content. Leading and trailing
// whitespace disappears with it, which is what keeps a command whose output ends
// in a newline from contributing a detail that is nothing but a space.
func foldToOneLine(s string) string {
	return strings.Join(strings.Fields(s), " ")
}
