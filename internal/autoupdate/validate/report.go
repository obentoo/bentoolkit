package validate

// Outcome is what a gate managed to say about one ebuild.
//
// SKIPPED is the reason this is three values and not a boolean. "The gate could
// not run" and "the gate ran and found nothing wrong" are different answers, and
// collapsing them is the defect this type exists to prevent: a clean report
// must never be readable as "we did not look".
type Outcome string

const (
	// OutcomePass means both sides were read and they agree.
	OutcomePass Outcome = "PASS"
	// OutcomeFailed means both sides were read and at least one error finding
	// came out of the comparison.
	OutcomeFailed Outcome = "FAILED"
	// OutcomeSkipped means the gate could not run. It ALWAYS carries a reason.
	OutcomeSkipped Outcome = "SKIPPED"
)

// DeclineCause says WHY a SKIPPED gate declined, in the ONE dimension a
// promotion decision turns on: was the thing that stopped the gate a fact about
// the CANDIDATE, or a fact about THIS MACHINE.
//
// It is a field and not a sentence because PromotionDecision REFUSES a bump on
// this distinction. Every skip's Reason already says which it is, in prose, but
// prose gets reworded, and reading the cause back out of it would let a wording
// change silently start (or stop) publishing unmeasured ebuilds. Nothing
// pattern-matches a Reason string.
//
// It carries unbuildableHereReason's split one level up: a list that measured
// nothing ABOUT THE CANDIDATE is refused, while one that measured nothing
// because THIS MACHINE could not answer is promoted — refusing those would make
// `overlay autoupdate --apply` inert on an ordinary workstation.
type DeclineCause string

const (
	// DeclineUnrecorded is a skip whose producer has not named its cause. It
	// keeps today's answer — it does NOT refuse — and that fail-open is
	// DELIBERATE, not a gap waiting to be closed.
	//
	// The refusal names a KNOWN vacuity. A rule that refused on silence would
	// refuse every skip no producer has been taught to tag yet, which is the
	// flat "every deciding gate SKIPPED → refuse" reading that takes
	// `overlay autoupdate` down on any host that cannot build the package.
	// Each producer that learns to name its cause TIGHTENS the rule; nobody
	// should "fix" this into fail-closed in one edit.
	DeclineUnrecorded DeclineCause = ""
	// DeclineCandidate: something about THIS EBUILD stopped the gate — no
	// Manifest could be produced for it, its tree could not be staged. Nothing
	// read the candidate, and the bentoo overlay auto-commits and pushes within
	// minutes, so promoting on this publishes an unmeasured ebuild.
	DeclineCandidate DeclineCause = "candidate"
	// DeclineHost: something about THIS MACHINE stopped it — a build dependency
	// is not installed, no privilege to isolate, no `ebuild` on PATH. The
	// machine says nothing about the bump, so the bump is promoted with the
	// depth it did not reach named.
	DeclineHost DeclineCause = "host"
)

// GateResult is what ONE gate has to say about one ebuild.
//
// The reason lives per gate, not per ebuild: a single shared Reason let the
// last gate to write it overwrite another gate's cause.
//
// Invariant: Outcome SKIPPED ALWAYS carries a non-empty Reason — a skip nobody
// can read is a pass. The converse does not hold: a PASS may carry a reason
// too, because an outcome names its own reach ("a configure pass does not cover
// compilation"). Each Finding also carries its own Gate so a renderer
// flattening every gate's findings into one list can still say which check
// spoke.
//
// Declined is `json:"-"` as a requirement: this document is pinned
// BYTE-FOR-BYTE, and the same struct is StageRecord.Gates on disk, so a new key
// would change bytes already written by every installed copy of the tool. The
// price: a gate list ROUND-TRIPPED through a StageRecord comes back
// DeclineUnrecorded, so the vacuity rule decides only on a list held in memory
// by the run that produced it, and a reload fails open.
type GateResult struct {
	Gate     string    `json:"gate"`
	Outcome  Outcome   `json:"outcome"`
	Reason   string    `json:"reason,omitempty"`
	Findings []Finding `json:"findings,omitempty"`

	// Declined is why a SKIPPED gate declined. It is meaningless on PASS and
	// FAILED, which measured something and therefore declined nothing.
	Declined DeclineCause `json:"-"`
}

// EbuildResult is everything the run has to say about one ebuild version.
//
// Gates is a list, not one field per gate: with per-gate fields, ExitCode once
// filtered on GateOptions alone, so an error from the gate that runs the build
// exited 0 while the report printed a failure. A new gate costs a constant.
//
// Depth is how far validation actually got; DepthRequested is how far it was
// asked to go; DepthReason is the "because" between them, absent when they
// agree — an outcome names its own reach, ladder included. All three are
// strings, the spelling `--depth` accepts and Depth.String prints, because
// Depth is an int whose ORDERING is its contract and must not reach the wire.
//
// The json tags are the contract of `overlay validate --json`: written out so a
// Go rename cannot silently rename a key under a consumer's jq expression. Only
// genuinely absent fields carry omitempty; Package, Version, Depth,
// DepthRequested, Gates and Sources are always present.
type EbuildResult struct {
	Package        string       `json:"package"`
	Version        string       `json:"version"`
	Depth          string       `json:"depth"`
	DepthRequested string       `json:"depth_requested"`
	DepthReason    string       `json:"depth_reason,omitempty"`
	Gates          []GateResult `json:"gates"`
	Sources        []string     `json:"sources"`
}

// WorstOutcome is the one outcome that stands for the whole ebuild, for a
// renderer that has one column and five gates to fit in it.
//
// FAILED beats SKIPPED beats PASS, and PASS is only reached when EVERY deciding
// gate passed. A result carrying no deciding gate at all answers SKIPPED: it has
// said nothing about this ebuild, and "nothing" read as a pass is the defect
// this outcome exists to prevent.
//
// # pkgcheck is excluded, exactly as it is from ExitCode
//
// The QA gate skips whenever pkgcheck is not installed, which on such a host is
// every ebuild in the tree. Folding that into the headline would turn a clean
// whole-overlay run into "0 passed, 500 skipped" and put the summary line at
// odds with the exit code beside it — the same argument that keeps QA
// findings out of ExitCode, applied to the outcome instead of the finding. The
// QA gate is still rendered, still named, and still carries its own reason.
func (r EbuildResult) WorstOutcome() Outcome {
	deciding, passes := 0, 0
	for _, gate := range r.Gates {
		if gate.Gate == GateQA {
			continue
		}
		deciding++
		switch gate.Outcome {
		case OutcomeFailed:
			return OutcomeFailed
		case OutcomePass:
			passes++
		}
	}
	if deciding > 0 && passes == deciding {
		return OutcomePass
	}
	return OutcomeSkipped
}

// Report is one whole run.
//
// UnmatchedSelector carries the selector when it named a category or package
// the overlay does not hold. It is a field rather than an error because the run
// still produced a report — an empty one — and the command has to exit 2 while
// still rendering something.
type Report struct {
	Overlay           string         `json:"overlay"`
	Results           []EbuildResult `json:"results"`
	UnmatchedSelector string         `json:"unmatched_selector,omitempty"`
}

// Normalized returns a copy whose nil slices are empty ones, so the JSON
// document carries `[]` rather than `null`. A consumer piping this into
// `jq '.results[].gates[]'` should not have to special-case the difference
// between "no gates" and "the key was nil in Go".
//
// A gate's own Findings are left alone, because they carry omitempty: nil and
// empty are both written as an absent key there, so neither can surface as a
// `null` for jq to trip over.
func (r Report) Normalized() Report {
	out := r
	out.Results = make([]EbuildResult, len(r.Results))
	for i, res := range r.Results {
		if res.Sources == nil {
			res.Sources = []string{}
		}
		if res.Gates == nil {
			res.Gates = []GateResult{}
		}
		out.Results[i] = res
	}
	return out
}

// ExitCode returns the process exit code for the run.
//
//	0 — every gate outcome was PASS or SKIPPED
//	1 — at least one finding of severity error, from any gate but pkgcheck's
//	2 — the selector named something the overlay does not hold
//
// This is NOT internal/autoupdate.BatchResult.ExitCode (0 none, 2 TOTAL, 1
// PARTIAL failure), and the two must not be unified. BatchResult counts ITEMS IT
// COULD NOT PRODUCE; this gate turns every would-be failure into a produced
// outcome that names why it was SKIPPED. Unified, `2` would carry two meanings
// in one binary — a bug that surfaces only in someone's CI script.
//
// An error finding fails the run whatever gate produced it, with two
// structural exceptions. pkgcheck findings are excluded: the overlay carries
// pre-existing QA findings unrelated to any bump, and a metadata.xml typo must
// not fail `overlay validate` across the whole tree. The reviewer needs no
// exclusion: it never emits at error, so a model's opinion cannot fail a bump.
// The exclusion selects on the GATE THAT RAN, not on a finding's Gate label.
func (r Report) ExitCode() int {
	if r.UnmatchedSelector != "" {
		return 2
	}
	for _, res := range r.Results {
		for _, gate := range res.Gates {
			if gate.Gate == GateQA {
				continue
			}
			for _, f := range gate.Findings {
				if f.Severity == SeverityError {
					return 1
				}
			}
		}
	}
	return 0
}

// HasErrors reports whether any deciding gate recorded an error finding. It is
// what the text renderer keys its summary line on, so the summary and the exit
// code cannot disagree.
func (r Report) HasErrors() bool { return r.ExitCode() == 1 }

// skippedResult builds the outcome for an ebuild whose option gate could not
// run. The reason is a required argument rather than a settable field, which is
// how the "SKIPPED always carries a reason" invariant is enforced instead of
// merely documented.
//
// It produces the option gate alone. Every later gate is appended by whoever
// runs it, and one that never ran contributes no GateResult rather than a
// hollow one — an absent gate is honest, and a PASS nobody measured is not.
func skippedResult(pkg, version, reason string) EbuildResult {
	return EbuildResult{
		Package: pkg,
		Version: version,
		Gates: []GateResult{{
			Gate:    GateOptions,
			Outcome: OutcomeSkipped,
			Reason:  reason,
		}},
	}
}

// comparedResult builds the outcome for an ebuild whose two sides were both
// read. It is the ONLY way to produce a PASS, which is how "PASS only after
// reading both sides" is made structural: a caller that has only one
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
		Package: pkg,
		Version: version,
		Sources: d.Sources,
		Gates: []GateResult{{
			Gate:     GateOptions,
			Outcome:  outcome,
			Findings: findings,
		}},
	}
}
