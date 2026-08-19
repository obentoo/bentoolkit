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

// DeclineCause says WHY a SKIPPED gate declined, in the ONE dimension a
// promotion decision turns on: was the thing that stopped the gate a fact about
// the CANDIDATE, or a fact about THIS MACHINE (S039-R2.1).
//
// # Why this is a field and not a sentence
//
// Every skip already carries a Reason, and the reason already says which it is —
// in prose. Prose is free to be reworded, and PromotionDecision now REFUSES a
// bump on this distinction, so reading it back out of the sentence would make a
// wording change silently start (or stop) publishing unmeasured ebuilds. The
// cause therefore travels as data from the producer that knows it to the rule
// that needs it, and nothing anywhere pattern-matches a Reason string.
//
// # The same split D1(c) already makes, one level up
//
// unbuildableHereReason exists because "this host lacks the dependency" and
// "this ebuild is broken" must not reach the same verdict. DeclineCause is that
// distinction carried past the gate and into the promotion rule: R2.1 refuses a
// list that measured nothing ABOUT THE CANDIDATE; S033-R3.12 promotes a list
// that measured nothing because THIS MACHINE could not answer, because refusing
// those makes `overlay autoupdate --apply` inert on an ordinary workstation.
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
	// minutes, so promoting on this publishes an unmeasured ebuild (R2.1).
	DeclineCandidate DeclineCause = "candidate"
	// DeclineHost: something about THIS MACHINE stopped it — a build dependency
	// is not installed, no privilege to isolate, no `ebuild` on PATH. The
	// machine says nothing about the bump, so the bump is promoted with the
	// depth it did not reach named (S033-R3.12).
	DeclineHost DeclineCause = "host"
)

// GateResult is what ONE gate has to say about one ebuild.
//
// # Why the reason lives here and not on the result
//
// EbuildResult shipped with a single Reason shared by every gate, and attachQA
// overwrote it: an option gate that skipped for a missing distfile and a QA gate
// that skipped for a missing pkgcheck came out of the run as one cause, and
// whichever wrote last won. A reason is a property of the gate that could not
// run. With five gates that is no longer a nicety — four of them would be
// explaining themselves through one field.
//
// # The invariant
//
// Outcome SKIPPED ALWAYS carries a non-empty Reason. That is the whole story in
// one sentence — a skip nobody can read is a pass — and putting Reason on the
// gate is what makes it statable once per gate instead of once per ebuild. The
// converse does NOT hold: a PASS may carry a reason too, because an outcome
// names its own reach and "a configure pass does not cover compilation" is
// exactly the kind of qualification R4 asks for.
//
// Findings are the ones THIS gate produced. Each also carries its own Gate
// field, which looks redundant here and is not: a renderer flattening every
// gate's findings into one list must still be able to say which check spoke.
//
// # Declined is deliberately NOT serialized, and that costs something
//
// `json:"-"` is a requirement, not a default. This document is the one S039-R1.6
// and story 031's R11.3 pin BYTE-FOR-BYTE, and the same struct is also
// StageRecord.Gates on disk (record.go). A new key in either changes bytes this
// story promised not to change — including the records already written by every
// installed copy of the tool.
//
// The price is real and is stated here so nobody discovers it as a surprise: a
// gate list ROUND-TRIPPED THROUGH A StageRecord comes back DeclineUnrecorded,
// whatever it was when it was written. So the vacuity rule below decides on a
// list held in memory by the run that produced it, and a reload sees only
// "SKIPPED, cause unrecorded" — which fails open, by DeclineUnrecorded's rule.
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
// # A list of gates, not one field per gate
//
// This shipped with two hardcoded outcome fields, Options and QA, and one Reason
// for both. Five gates do not fit that shape, and the failure is not cosmetic:
// ExitCode filtered its findings on GateOptions, so an error from the gate that
// actually runs the build was invisible to it and the command exited 0 while the
// report printed a failure. Gates is a list so that a sixth gate costs a
// constant and no surgery (D12).
//
// # Depth beside DepthRequested
//
// Depth is how far validation actually got; DepthRequested is how far it was
// asked to go. Both are needed to say "compile was asked for and configure is as
// far as it got, because …", which is the R4 rule — an outcome names its own
// reach — applied to the ladder itself. DepthReason is that "because", and it is
// the one of the three that is genuinely absent when the two agree.
//
// All three are strings rather than the Depth type on purpose: Depth is an int
// whose ORDERING is its contract, so it would reach the wire as a number and
// force every consumer to carry the ladder's ordering to read it. The string is
// the same spelling `--depth` accepts and Depth.String prints.
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
// Depth, DepthRequested, Gates and Sources are always present, so a consumer
// never has to tell "missing" from "empty" for them.
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
// this whole story exists to remove.
//
// # pkgcheck is excluded, exactly as it is from ExitCode
//
// The QA gate skips whenever pkgcheck is not installed, which on such a host is
// every ebuild in the tree. Folding that into the headline would turn a clean
// whole-overlay run into "0 passed, 500 skipped" and put the summary line at
// odds with the exit code beside it — the same D8 argument that keeps QA
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
// still rendering something (R5.7).
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
// # Every gate counts, except the two that never decide
//
// This filtered on GateOptions, which is why it existed to be rewritten: the
// configure gate is the one that actually runs the build, and an error from it
// answered 0. The rule is now uniform — an error finding fails the run whatever
// produced it — and the two exceptions are structural rather than listed:
//
//   - pkgcheck findings ride in the same report and are excluded here (D8). The
//     overlay carries pre-existing QA findings that have nothing to do with a
//     bump; letting them set the exit status would make `overlay validate` fail
//     across the whole tree and reduce it to noise — a metadata.xml DOCTYPE typo
//     outranking the real signal.
//   - The reviewer is not excluded and does not need to be: it emits at info or
//     warning and never at error (R7.6), so a model's opinion cannot fail a bump
//     even when the rule counting it is uniform. That is a severity discipline
//     enforced where the findings are produced, not a special case here.
//
// The exclusion selects on the GATE THAT RAN, not on the finding's own Gate
// label. The gate that produced a finding owns whether it decides; the label is
// for a renderer flattening several gates into one list.
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
