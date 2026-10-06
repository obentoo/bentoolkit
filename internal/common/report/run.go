package report

import "fmt"

// SchemaVersion is the version number every exported document carries at its
// root. It is what a consumer reads before it reads anything else.
//
// # Why it starts at 2
//
// This is the first release that declares a version at all, and the number is
// still 2. That is not an off-by-one. The shape shipped today — the model's own
// fields at the document root, with no envelope around them — IS schema 1 in
// the field, even though nothing in it says so. Numbering the new shape 1 would
// make the version indistinguishable from the version that has no version: a
// consumer meeting `{"schema": 1, ...}` could not tell whether it held the new
// document or an old one that had grown a key by coincidence. Starting at 2
// leaves 1 meaning exactly what it already means.
//
// # Why it is a name and not a literal
//
// Every producer sets Run.Schema, and a 2 typed at each of them is the same
// defect as a column width typed into a format string: three places to find on
// the day it becomes 3, and nothing that fails when one is missed.
const SchemaVersion = 2

// Kind names what produced a report. It is the JSON discriminator, and the only
// reason a consumer can tell two exports apart without being told.
//
// A kind names the PRODUCER, never the contents: it is fixed per command and
// derived from nothing a run establishes, so `.kind == "overlay.manifest"` is a
// filter rather than a guess. A kind that varied with a run's contents would let
// a filtering consumer silently miss some of a command's documents; two commands
// sharing one string would hand it the wrong document under the right name.
//
// Values are dotted (`domain.action`) and no kind may be a prefix of another:
// `.kind | startswith("overlay.manifest")` is the natural jq for "any manifest
// run", and a later `overlay.manifest.dry` would match it. run_test.go checks
// both the equality and the prefix rule, which the compiler cannot.
type Kind string

const (
	// KindAutoupdateCheck is `overlay autoupdate check`: the run that scans an
	// overlay's packages, plans the validation the pending updates earn, and
	// reports what the gates answered. Its payload carries the four-column
	// tally.
	KindAutoupdateCheck Kind = "autoupdate.check"
	// KindOverlayManifest is `overlay manifest`: the run that regenerates
	// Manifest files. Its payload counts what succeeded and what failed —
	// two columns, not four, which is why the tally is a payload's business
	// and not the envelope's.
	KindOverlayManifest Kind = "overlay.manifest"
	// KindSnapshotRun is `snapshot run`: the run that takes and prunes
	// subvolume snapshots. Its units are subvolumes rather than packages,
	// which is the distance that proves the envelope carries no domain.
	KindSnapshotRun Kind = "snapshot.run"
	// KindOverlayValidate is `overlay validate`: the run that asks whether
	// each ebuild still matches the source it points at, gate by gate. Its
	// `--json` emits this envelope, the same document `--export` writes to a
	// `.json` path, at stdout.
	//
	// Its payload is the only one this package does not declare, because of
	// the dependency direction: the facts a validation run establishes live in
	// internal/autoupdate/validate, which boundary_test.go forbids this package
	// from importing, so the payload type sits at the adapter
	// (cmd/bentoo/overlay_validate_report.go) and stays there. Only the JSON
	// export carries its fields, reaching them through encoding/json rather
	// than through Sections. It is on run_test.go's collision sweep because it
	// is emitted, and an emitted kind is part of the contract consumers filter
	// on.
	KindOverlayValidate Kind = "overlay.validate"
	// KindOverlayCompare is `overlay compare`: the run that holds every
	// package the overlay carries against the copy a chosen repository ships,
	// and reaches a recommendation about each one. Its payload carries the
	// repository compared against, how much was scanned and shared, the
	// packages, and the four-column verdict tally.
	//
	// Its payload's lists PARTITION what the run found: four DISJOINT sets,
	// one per recommendation (remove ours, re-apply ours, keep ours, no
	// opinion), unlike the check's slices, which are stages over one set. They
	// are four fields rather than one list with a verdict column because
	// folding the rebase list into the redundant one would recommend deleting
	// an ebuild carrying changes the compared repository has no copy of.
	//
	// The value is FLAT as the command grows sub-flows: a later
	// `overlay.compare.realign` would break the prefix rule above, so every
	// sub-flow shares this kind and says which flow it was through the
	// payload. `overlay.manifest` and `overlay.validate` are SIBLINGS, not
	// prefixes, which is what the dotted form is for.
	KindOverlayCompare Kind = "overlay.compare"
)

// Run is what every report carries, whatever produced it: the envelope.
//
// It is assembled in full before any part of it is rendered, so a run that is
// interrupted, or that fails to write its output anywhere, still holds a
// complete description of what it established.
//
// The envelope knows nothing about the domain: every field is true of ANY
// batch, and the facts of one kind of run sit behind Payload, reachable only
// through Sections, so a new kind of run is a new payload rather than a
// renderer edit. Complete and NotEvaluated are here because any batch can
// answer them; a tally is not, because each payload counts in its own columns
// (four outcomes for autoupdate, ok and failed for manifest).
//
// It marshals and does not unmarshal, on purpose: decoding into the Payload
// interface needs a hand-kept registry of every kind, and export is write-only
// today. The reader belongs with the first use case that needs one, which
// should say whether it needs a round trip, one field or only the kind.
type Run struct {
	// Schema is the document's version, always SchemaVersion. It is written
	// even though every document this release produces carries the same
	// number, because a version a consumer has to infer from the keys present
	// is the situation schema 1 is already in.
	Schema int `json:"schema"`
	// Kind names the command that produced this document. It is what a
	// consumer branches on, and the only key that says what the payload
	// below is.
	Kind Kind `json:"kind"`
	// Title is the run's heading in the reader's own words — "Autoupdate
	// check", "42 targets". It is a label, not a discriminator: two runs may
	// share one, and a consumer that matched on it would be matching on prose.
	Title string `json:"title"`
	// Complete reports that the run reached the end of its plan. A run stopped
	// early — by an interrupt, or by a failure it could not continue past —
	// sets this false, and the report still holds everything established up to
	// that point.
	//
	// It has no omitempty, and that is load-bearing: a `false` dropped from the
	// document reads as "the producer never said", which is exactly the
	// conflation this field exists to remove.
	Complete bool `json:"complete"`
	// NotEvaluated is how many planned units the run never reached. It is zero
	// for a complete run, and for an interrupted one it is the number that
	// turns a short list into a stated gap rather than a silent one.
	//
	// It carries no omitempty for the same reason Complete does not.
	NotEvaluated int `json:"not_evaluated"`
	// Payload is the domain half, and it marshals NESTED under its own key.
	// The nesting is the whole point of the envelope: `.tally.proved` becomes
	// `.payload.tally.proved`, and every kind added after this one adds a
	// value to Kind rather than a key at the root. A payload flattened up
	// here would be schema 1 again, with Kind saying nothing about the shape
	// beside it.
	Payload Payload `json:"payload"`
}

// Sections is the whole run as ordered blocks: the gap it left, if it left one,
// and then everything its payload has to say.
//
// The envelope states the gap because only the envelope holds Complete and
// NotEvaluated, and one sentence here serves packages, subvolumes and ebuilds
// alike. It goes FIRST: every block below is short by the units never reached,
// a reader must meet the qualifier before the tables, and the fullscreen
// viewport cuts from the bottom. It is prepended to a NEW slice, because
// appending into the payload's own would let displaying a report edit the
// blocks the export then writes.
//
// A nil Payload (a producer that returned early) contributes no block rather
// than panicking: a report is what an operator gets INSTEAD of a crash, and an
// incomplete run still states its gap.
func (r Run) Sections(opts SectionOptions) []Section {
	var blocks []Section
	if r.Payload != nil {
		blocks = r.Payload.Sections(opts)
	}

	if r.Complete {
		return blocks
	}
	return append([]Section{interruptedSection(r.NotEvaluated)}, blocks...)
}

// interruptedSection is the label for a run that stopped before the end of its
// plan: it says so, and says how much of that plan it never reached.
//
// It reads one number and nothing about the terminal, so the same interrupt
// reads the same way on screen, in a pull request comment and in a log file;
// the JSON export already carries Complete and NotEvaluated. It says "unit",
// not "package", because the envelope does not know what was counted — the
// sections below say it in the payload's own vocabulary. The count is stated
// even when it is zero: "0" is what says an interrupt after the last unit lost
// nothing.
func interruptedSection(notEvaluated int) Section {
	return Section{
		Title: "Run Interrupted",
		Lead: []string{
			fmt.Sprintf("This report is incomplete: the run was interrupted, and %d planned unit(s) were not evaluated.",
				notEvaluated),
		},
		Notes: []string{
			"The counts below cover only what the run reached; the rest are counted in no column.",
		},
	}
}

// Payload is the domain half: the facts one kind of run establishes, and how
// that kind says them as ordered blocks.
//
// One method, and it is the entire contract between a producer and every
// renderer. A payload turns itself into []Section — titled blocks of structure,
// with no width, no colour and no escape sequence in them — and each renderer
// consumes sections and nothing else. That is the seam: a new kind of run
// implements this interface, and no renderer changes.
//
// Sections is the only way anything in this package reaches a payload. The
// payload does also MARSHAL — encoding/json reaches its exported fields
// directly, which is what puts the domain facts under "payload" in the exported
// document — but no code here and none in render may branch on its concrete
// type. A type switch over payloads is the edit-per-kind this interface exists
// to prevent.
type Payload interface {
	// Sections returns the payload's blocks in the order they are meant to be
	// read. opts says what the report should SAY; nothing in it says how the
	// report should look.
	Sections(opts SectionOptions) []Section
}

// SectionOptions is what a report should SAY. render.Options is what the device
// ALLOWS. Keeping those two apart is the split this package exists to hold.
//
// Listing every package a run found up to date is a decision about CONTENT, and
// it is made once, here, where the sections are built. Deciding that a line may
// be 100 cells wide is a decision about the DEVICE, and it is made in a
// renderer, from that device's own measurement. A renderer that read ShowAll
// would be deciding content, and the same run would then say different things
// in plain and in fullscreen with nothing in the model able to explain why.
//
// It lives beside Payload rather than beside Section because it is what a
// payload is ASKED, not part of what a payload answers.
type SectionOptions struct {
	// ShowAll lists the units a run found nothing to report about — the
	// packages already up to date — instead of only counting them. It is the
	// --all flag.
	//
	// False prints the count alone, and that is the default because those
	// units are the bulk of a large run: listing them unasked is most of why a
	// check that found four updates printed 348 lines.
	ShowAll bool
	// SkipPlan omits the section describing what the run intended to do. It
	// exists for exactly one caller: the on-screen report of a run whose plan
	// was already printed before the confirmation prompt, so the operator is
	// not shown the same list twice.
	//
	// An export must never set it. A record missing the plan answers no
	// question later, and the reader of the file is not the operator
	// who saw the plan go by on screen.
	SkipPlan bool
}
