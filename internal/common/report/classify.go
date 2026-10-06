package report

// GateFact is one gate's contribution to a package's verdict, reduced to the
// facts the rule reads.
//
// # Deciding is "participated", not "was planned"
//
// Deciding answers ONE question: did this gate participate in the verdict. A
// gate that declined was planned, was asked, and said nothing, so it arrives
// with Deciding false. That distinction is the whole reason a run that answered
// cleanly can be told apart from a run that never answered — collapsing the two
// is the defect this rule exists to remove.
//
// # Why primitive facts and not validate.GateResult
//
// The facts originate in internal/autoupdate/validate, and this package must
// not import it: a package under internal/common depending on
// internal/autoupdate inverts the dependency direction, and boundary_test.go
// fails the build the moment that import appears. An adapter in cmd/bentoo
// converts each GateResult into one of these — PASS/FAILED/SKIPPED into the
// bools, DeclineCause into the plain string Cause.
type GateFact struct {
	// Deciding reports that this gate participated in the verdict. A gate
	// that declined did not, and arrives false.
	Deciding bool
	// Passed reports that this deciding gate measured the bump and found
	// nothing wrong. It says nothing on a gate that did not decide.
	Passed bool
	// Failed reports that this deciding gate measured the bump and produced
	// an error finding.
	Failed bool
	// Cause is why a gate that did not decide declined: "candidate", "host",
	// or "" when its producer never said — validate.DeclineCause's value,
	// carried as a plain string so the boundary above stays uncrossed.
	//
	// Classify does NOT branch on it. It travels so the output can NAME the
	// cause, and so the rule has it in hand on the day it tightens.
	Cause string
	// ProvesSelectedDepth marks the ONE gate whose pass answers the depth the
	// policy selected for this bump. At most one fact in a slice carries it.
	//
	// It is a primitive fact for the same reason Cause is a plain string: the
	// depth->gate mapping lives in internal/autoupdate/validate (GateForDepth),
	// which a package under internal/common must not import. The adapter
	// resolves it and marks the fact; this package only reads the mark.
	//
	// Nothing carries it when the plan's depth cannot be parsed, or when no
	// gate stands for that depth — depth none, today. Both cases fall through
	// to Inconclusive rather than guessing a gate.
	ProvesSelectedDepth bool
}

// Classify decides which of the tally's four columns one planned package lands
// in. The order IS the rule:
//
//	policy said no        →  Skipped        (FIRST: the operator outranks any gate)
//	any deciding failed   →  Errored
//	every gate passed     →  Proved
//	selected depth passed →  Proved         (only after the lines above)
//	gates all declined    →  Inconclusive
//	no cause recorded     →  Inconclusive   (deliberate: do not "fix" it)
//
// Inputs are typed facts, never reason text, so rewording a reason moves no
// package. A decline with no Cause fails open to Inconclusive, like
// validate.DeclineUnrecorded: Skipped would hide a toolkit failure behind the
// operator's policy, so a large Inconclusive column is expected until producers
// tag a cause. Proved needs a deciding gate; no package WorstOutcome called
// errored becomes proved. Callers pass only deciding gates, dropping pkgcheck as
// WorstOutcome does, or a host without pkgcheck turns every Proved Inconclusive.
func Classify(policySkipped bool, gates []GateFact) Outcome {
	// The rule's first line. Before any gate is consulted: what the operator
	// decided outranks what the toolkit managed.
	if policySkipped {
		return Skipped
	}

	passing, selectedDepthProved := 0, false
	for _, gate := range gates {
		if !gate.Deciding {
			// The gate declined. It said nothing about this bump, so it
			// contributes no pass — and its Cause is NOT read, deliberately.
			// An unrecorded cause must not become Skipped.
			continue
		}
		if gate.Failed {
			return Errored
		}
		if gate.Passed {
			passing++
			if gate.ProvesSelectedDepth {
				selectedDepthProved = true
			}
		}
		// A deciding gate that neither passed nor failed answered nothing.
		// It counts as no pass and the result falls through below: silence
		// is never read as a pass.
	}

	// EVERY gate passed, and there was one: WorstOutcome's own condition, so
	// no package becomes proved that was not proved before.
	if passing > 0 && passing == len(gates) {
		return Proved
	}

	// THE GATE THE POLICY ASKED FOR PASSED, and none failed. Read only after
	// the two checks above: an excluded package stays Skipped and a failure
	// outranks any pass, so only the half-measured case reaches here.
	//
	// A declining gate no longer collapses this result because the ladder is
	// ordered: a deeper gate's pass IMPLIES the rungs below it. Over this host's
	// 137 staged records the declining gates were `review` (no rung at all) and
	// `options` (always BELOW the depth that passed); none was deeper than the
	// selected depth, so nothing here reports a rung the run did not climb. No
	// package WorstOutcome called errored becomes proved — only one it called
	// SKIPPED whose selected depth was in fact measured and passed.
	if selectedDepthProved {
		return Proved
	}

	return Inconclusive
}
