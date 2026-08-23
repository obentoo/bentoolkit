// Package report is the view model of one `overlay autoupdate` run: the
// packages it scanned, the validation it planned, what the gates answered, and
// the counts that summarize all of it (R1.1).
//
// # It describes a run, it does not display one
//
// Nothing in this package is a rendering. It holds no ANSI escape sequence, no
// column width, no padding, no border character and no terminal dimension, and
// no field describes one (R1.2). A renderer takes every value it shows from
// these types, so a fact the model does not carry is a fact no renderer can
// invent — and shortening a long string is always a rendering decision, never a
// loss of data (R7.4).
//
// That separation is the defect this package exists to remove: the check path
// formatted each section at the moment it printed it, so nothing held "what
// this run found" as a value, and nothing could be rendered twice, exported,
// counted or tested without running the command again.
//
// # It does not depend on the producer
//
// The facts originate in internal/autoupdate and internal/autoupdate/validate,
// and this package imports neither: a package under internal/common depending
// on internal/autoupdate would invert the dependency direction. The model takes
// primitive facts — strings, bools, ints — and an adapter in cmd/bentoo
// converts the producer's types into them.
//
// # The model is built before anything is printed
//
// A run assembles the whole report first (R1.4), so a run that fails to render,
// or that is interrupted, still holds a complete description of what it found.
// Report.Complete and Report.NotEvaluated are how such a run says so.
package report

// Outcome is what a run managed to establish about one planned package.
//
// There are four values rather than three because "we could not evaluate this"
// and "you told us not to evaluate this" are different answers, and folding
// them together lets a defect in the toolkit hide behind the operator's own
// policy (R5). Which of the four a package earns is decided from a typed cause
// carried by the validation result, never by matching text in a human-readable
// reason (R5.4).
type Outcome string

const (
	// Proved means every deciding gate answered for the package and all of
	// them passed. It is the only value that stands for "this bump was
	// measured and it survived".
	Proved Outcome = "proved"
	// Errored means at least one deciding gate ran and failed. Something was
	// measured, and what it measured was wrong.
	Errored Outcome = "errored"
	// Inconclusive means no deciding gate answered because THE TOOLKIT could
	// not evaluate the package — an unsupported build system, an unpreparable
	// tree, a missing dependency (R5.2). A package whose result records no
	// cause at all lands here too, so an unclassified case stays visible
	// instead of being absorbed into policy (R5.6).
	Inconclusive Outcome = "inconclusive"
	// Skipped means no gate was run because POLICY said not to run one — a
	// configured depth of none, a package type the run excludes (R5.3). It is
	// the operator's own decision reported back, not a limitation.
	Skipped Outcome = "skipped"
)

// Tally is the run's four counts, one per Outcome (R5.1).
//
// This amends archived story 033's three-column tally (S033-R9.5) by dividing
// its third column: proved and errored are untouched and count exactly the
// packages they counted before (R5.7). The invariant that column set protected
// is preserved — each planned package lands in exactly one column, and the
// columns sum to the number of planned packages (R5.5, Report.Reconciles).
type Tally struct {
	// Proved counts the packages whose deciding gates all passed.
	Proved int
	// Errored counts the packages with at least one failing deciding gate.
	Errored int
	// Inconclusive counts the packages the toolkit could not evaluate, plus
	// the ones whose results named no cause. Both are limitations of the
	// toolkit rather than choices of the operator.
	Inconclusive int
	// Skipped counts the packages policy said not to evaluate. "Nothing was
	// said" is never folded into Proved.
	Skipped int
}

// Total is how many planned packages the tally accounts for.
//
// Every column is summed, and that is the whole contract: a Total that forgot
// one would report a number the list above it cannot be reconciled with, which
// is the failure Reconciles exists to catch.
func (t Tally) Total() int {
	return t.Proved + t.Errored + t.Inconclusive + t.Skipped
}

// Report is everything one run found.
//
// It is assembled in full before any output is produced (R1.4). A run that is
// interrupted still produces one of these, marked incomplete.
type Report struct {
	// Scanned is every package the run looked at, in the order it looked at
	// them, whether or not an update was found. It is what makes a package
	// that is up to date distinguishable from a package that was never
	// scanned.
	Scanned []PackageResult
	// Plan is the pending updates the run intended to validate, including the
	// ones policy excluded. Dropping those would produce a reassuring tally
	// about the easy packages and say nothing about the rest. It is also the
	// denominator the tally is reconciled against.
	Plan []PlanEntry
	// Results is what the gates answered, one row per planned package the run
	// reached. For an interrupted run it covers only the packages reached
	// before the interrupt, and NotEvaluated is how many of the rest there
	// were.
	Results []ValidationRow
	// Tally is the four counts taken over Plan.
	Tally Tally
	// Complete reports that the run reached the end of its plan. A run stopped
	// early — by an interrupt — sets this false, and the report still holds
	// everything it had established up to that point.
	Complete bool
	// NotEvaluated is how many planned packages the run never reached. It is
	// zero for a complete run, and for an interrupted one it is the number
	// that turns a short result list into a stated gap rather than a silent
	// one.
	NotEvaluated int
}

// Reconciles reports whether the tally accounts for every planned package
// exactly once (R5.5, preserving S033-R9.5).
//
// The denominator is the plan, not the result list: a package that produced no
// row still had to be counted somewhere, and comparing against the rows would
// make the check pass precisely when packages went missing.
//
// An interrupted run does not reconcile, and that is the honest answer rather
// than a bug — the packages it never reached are counted in no column, and
// Complete and NotEvaluated are what say so.
func (r Report) Reconciles() bool {
	return r.Tally.Total() == len(r.Plan)
}

// PackageResult is what the run learned about one scanned package, before any
// validation is planned.
//
// A scan reports ONE condition per package — orphaned, failed, not comparable,
// or an update found — so those four are mutually exclusive by construction
// even though nothing in the type says so, and all four false means the package
// was read and is already up to date. FromCache is orthogonal to them: it
// qualifies where the upstream answer came from, not what the answer was.
type PackageResult struct {
	// Package is the full atom, category/package.
	Package string
	// Type is the tier the package resolved to, "bin" or "source". It is empty
	// when the current ebuild could not be read, and empty is meaningful:
	// nobody resolved it, so nothing may be assumed from it.
	Type string
	// CurrentVersion is the version the overlay holds today.
	CurrentVersion string
	// CandidateVersion is the version found upstream. It is present even when
	// it could not be ordered against CurrentVersion, because the unusable
	// value is exactly what a reader needs to fix the parser configuration.
	CandidateVersion string
	// HasUpdate reports that CandidateVersion is newer than CurrentVersion. It
	// is false whenever the two could not be compared.
	HasUpdate bool
	// NotComparable reports that the upstream value could not be ordered
	// against the current one. It is carried apart from HasUpdate so that a
	// broken parser is never read as "up to date".
	NotComparable bool
	// Orphaned reports that the package no longer has any ebuild in the
	// overlay. When set, the version fields say nothing.
	Orphaned bool
	// FromCache reports that the upstream version was answered from cache
	// rather than fetched, which is why the run may not reflect a bump made in
	// the last few minutes.
	FromCache bool
	// Error is why the scan could not answer for this package, in full and
	// exactly as it was reported. It is empty when the scan succeeded, and a
	// string rather than an error because the model carries facts and has no
	// behaviour.
	Error string
}

// PlanEntry is one pending update the run intends to validate: the bump, the
// depth it earned, and the case for that depth.
//
// The plan is what the operator approves before any gate spends time or
// bandwidth, so an entry has to carry the case for its own cost. Reason is
// never expected to be empty: an entry whose reason went missing leaves a
// number nobody can check.
type PlanEntry struct {
	// Package is the full atom, category/package.
	Package string
	// CurrentVersion and CandidateVersion are the two ends of the bump. Both
	// are carried because the depth follows the distance between them, and a
	// reader checking the plan against the policy needs the two values the
	// classifier saw.
	CurrentVersion   string
	CandidateVersion string
	// Class is how far the bump moved — how the version distance was
	// classified.
	Class string
	// Depth is the resolved depth, spelled the way the --depth flag and the
	// config key spell it, so a plan line can be typed back as a flag.
	Depth string
	// Reason names the input that decided the depth, in full, and quotes an
	// override's stated justification where one exists. It is never shortened
	// here; a renderer that lacks the room shortens its own copy (R7.4).
	Reason string
	// Skipped marks an entry no gate will run for, because policy said so — a
	// depth of none, or a package type the run excludes. It travels with the
	// entry rather than being derived when the entry is shown, because the
	// reason has to travel with it, and because it is what separates Skipped
	// from Inconclusive in the tally (R5.3).
	Skipped bool
}

// ValidationRow is what the gates had to say about one planned package.
//
// One row per package the run reached, and each row lands in exactly one column
// of the Tally.
type ValidationRow struct {
	// Package is the full atom, category/package.
	Package string
	// CandidateVersion is the version that was evaluated.
	CandidateVersion string
	// Outcome is the one verdict that stands for the whole package, across
	// every gate that spoke for it.
	Outcome Outcome
	// Depth is how far validation actually got, which is not always how far it
	// was asked to go — PlanEntry.Depth is the request. Both are needed to say
	// "compile was asked for, configure is as far as it got", because an
	// outcome has to name its own reach.
	Depth string
	// Reason is why this package earned this outcome, IN FULL: the gate's own
	// reason where there is one, otherwise the reason the run reported for the
	// depth or the plan. It is never shortened, never truncated and never
	// summarized in the model — that is a rendering decision, and doing it
	// here would make it a loss of data (R7.4).
	//
	// A row that produced no verdict always carries a reason: a package
	// reported without one reads as a result.
	Reason string
	// SameReasonAsPlan reports that Reason repeats the plan entry's reason
	// word for word. The run sets it; a renderer uses it to print a reason
	// once instead of twice (R7.2), and a differing reason is new information
	// that gets printed (R7.3). It is computed where the two strings are both
	// in hand rather than by a renderer that would have to go looking for the
	// plan entry.
	SameReasonAsPlan bool
}
