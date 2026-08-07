package validate

// Outcome is what a gate managed to say about one ebuild.
//
// SKIPPED is the reason this is three values and not a boolean. "The gate could
// not run" and "the gate ran and found nothing wrong" are different answers, and
// collapsing them is the defect this whole story exists to remove: a clean
// report must never be readable as "we did not look".
type Outcome string

const (
	// OutcomePass means both sides were read and they agree (R3.5).
	OutcomePass Outcome = "PASS"
	// OutcomeFailed means both sides were read and at least one error finding
	// came out of the comparison (R3.4).
	OutcomeFailed Outcome = "FAILED"
	// OutcomeSkipped means the gate could not run. It ALWAYS carries a reason.
	OutcomeSkipped Outcome = "SKIPPED"
)

// EbuildResult is everything the run has to say about one ebuild version.
//
// Reason is non-empty whenever either Outcome is SKIPPED. That is the invariant
// the whole story rests on — a skip nobody can read is a pass — so it is
// asserted in tests and enforced by the constructors below rather than left to
// whoever writes the next call site.
//
// # The json tags are the contract
//
// `overlay validate --json` is the first JSON surface in this CLI, so there is
// no precedent to inherit and these names are established here. They are
// written out explicitly rather than left to Go's field names for one reason: a
// rename on the Go side must not silently rename the wire key under a consumer
// who has already shipped a jq expression against it.
//
// Only the fields that are genuinely absent carry omitempty. Package, Version,
// Options, QA and Findings are always present, so a consumer never has to tell
// "missing" from "empty" for them.
type EbuildResult struct {
	Package  string    `json:"package"`
	Version  string    `json:"version"`
	Options  Outcome   `json:"options"`
	QA       Outcome   `json:"qa"`
	Reason   string    `json:"reason,omitempty"`
	Sources  []string  `json:"sources"`
	Findings []Finding `json:"findings"`
}

// Report is one whole run.
//
// UnmatchedSelector carries the selector when it named a category or package
// the overlay does not hold. It is a field rather than an error because the run
// still produced a report — an empty one — and the command has to exit 2 while
// still rendering something (R5.7).
type Report struct {
	Overlay           string         `json:"overlay"`
	Results           []EbuildResult `json:"results"`
	UnmatchedSelector string         `json:"unmatched_selector,omitempty"`
}

// Normalized returns a copy whose nil slices are empty ones, so the JSON
// document carries `[]` rather than `null`. A consumer piping this into
// `jq '.results[].findings[]'` should not have to special-case the difference
// between "no findings" and "the key was nil in Go".
func (r Report) Normalized() Report {
	out := r
	out.Results = make([]EbuildResult, len(r.Results))
	for i, res := range r.Results {
		if res.Sources == nil {
			res.Sources = []string{}
		}
		if res.Findings == nil {
			res.Findings = []Finding{}
		}
		out.Results[i] = res
	}
	return out
}

// ExitCode returns the process exit code for the run.
//
//	0 — every option-gate outcome was PASS or SKIPPED
//	1 — at least one option-gate finding of severity error
//	2 — the selector named something the overlay does not hold
//
// # This is NOT BatchResult.ExitCode, and the two must not be unified
//
// internal/autoupdate.BatchResult defines its own exit codes for the batch
// commands: 0 no failures, 2 TOTAL failure, 1 PARTIAL failure. Both use 1 and 2
// and neither means what the other does.
//
// They are not reconcilable and should not be made to look it. BatchResult
// counts ITEMS IT COULD NOT PRODUCE. The option gate has no such mode: R4 turns
// every would-be failure — missing distfile, foreign build system, undetermined
// build system — into a produced outcome that names why it was SKIPPED. A gate
// whose entire purpose is to never fail silently cannot borrow an exit code
// shaped around failing to produce a result.
//
// Unify them and `2` acquires two meanings inside one binary, which is a bug
// that surfaces in someone's CI script and nowhere else.
//
// # Only option-gate findings count
//
// pkgcheck findings ride in the same report and are excluded here (D8). The
// overlay carries pre-existing QA findings that have nothing to do with a bump;
// letting them set the exit status would make `overlay validate` fail across
// the whole tree and reduce it to noise — a metadata.xml DOCTYPE typo
// outranking the real signal.
func (r Report) ExitCode() int {
	if r.UnmatchedSelector != "" {
		return 2
	}
	for _, res := range r.Results {
		for _, f := range res.Findings {
			if f.Gate == GateOptions && f.Severity == SeverityError {
				return 1
			}
		}
	}
	return 0
}

// HasErrors reports whether any option-gate error finding was recorded. It is
// what the text renderer keys its summary line on, so the summary and the exit
// code cannot disagree.
func (r Report) HasErrors() bool { return r.ExitCode() == 1 }

// skippedResult builds the outcome for an ebuild whose option gate could not
// run. The reason is a required argument rather than a settable field, which is
// how the "SKIPPED always carries a reason" invariant is enforced instead of
// merely documented.
func skippedResult(pkg, version, reason string) EbuildResult {
	return EbuildResult{
		Package: pkg,
		Version: version,
		Options: OutcomeSkipped,
		Reason:  reason,
	}
}

// comparedResult builds the outcome for an ebuild whose two sides were both
// read. It is the ONLY way to produce a PASS, which is how R3.5 — "PASS only
// after reading both sides" — is made structural: a caller that has only one
// side has no function to call that would give it a pass.
func comparedResult(pkg, version string, d Declared, p Passed) EbuildResult {
	findings := Compare(d, p, pkg, version)

	outcome := OutcomePass
	for _, f := range findings {
		if f.Severity == SeverityError {
			outcome = OutcomeFailed
			break
		}
	}

	return EbuildResult{
		Package:  pkg,
		Version:  version,
		Options:  outcome,
		Sources:  d.Sources,
		Findings: findings,
	}
}
