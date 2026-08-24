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
	// cause (R5.6), and so the rule has it in hand on the day it tightens.
	Cause string
}

// Classify decides which of the tally's four columns one planned package lands
// in (R5.2, R5.3, R5.4, R5.6). It is D2's four-line rule, in order:
//
//	policy said no      →  Skipped        (read FIRST, before any gate)
//	gates all declined  →  Inconclusive
//	no cause recorded   →  Inconclusive   (D3 — see below, and do not "fix" it)
//	otherwise           →  Proved / Errored, exactly as today (R5.7)
//
// Every input is a typed fact. Nothing here matches text in a human-readable
// reason string, which is R5.4 stated as code: a reworded reason must never
// move a package from one column to another.
//
// # Policy is read before the gates, and the order IS the rule
//
// A package the operator excluded is Skipped whatever its gates would have
// said — including a gate that failed. Consulting the gates first would report
// a toolkit limitation for a package the operator told the toolkit to leave
// alone: RC2 in the other direction, an operator's own instruction resurfacing
// as a defect they did not cause.
//
// # An unrecorded cause counts as Inconclusive, and that is DELIBERATE (D3)
//
// A gate that declined with Cause "" has a producer that was never taught to
// name why. That fails open here, matching validate.DeclineUnrecorded, whose
// own comment one layer down says the same thing: each producer that learns to
// name its cause TIGHTENS the rule, and nobody should convert it to
// fail-closed in one edit.
//
// Routing those to Skipped instead would restate RC2 with new labels — an
// untagged toolkit failure hiding behind the operator's policy, which is
// exactly the fold this story exists to undo. Inconclusive is the honest
// reading: "the toolkit did not establish why it could not answer" is a
// limitation of the toolkit, and that is what Inconclusive means (R5.6). It
// also keeps the case VISIBLE, which is the second half of R5.6 — an
// unclassified package is named in the output rather than absorbed into a
// column that reads as intentional.
//
// THE EXPECTED EFFECT IS A LARGE INCONCLUSIVE COLUMN IN THE FIRST RELEASES.
// Only a handful of producers tag a cause today; every other skip arrives
// untagged and lands here. That column shrinking is producers being taught, not
// this rule being repaired. Do not "fix" it here.
//
// # Cause is carried and never read
//
// "candidate" and "host" both land on Inconclusive, so the rule does not branch
// on Cause at all. Both name a limitation — one about the ebuild, one about
// this machine — and neither is the operator's instruction, which is the only
// thing Skipped means.
//
// # Proved is never returned without a deciding gate
//
// Proved claims the toolkit established that this bump survives. With no gate
// participating in the verdict, nothing established anything, so the empty gate
// list and the all-declined list both fall through to Inconclusive.
//
// The Proved condition mirrors validate.EbuildResult.WorstOutcome exactly —
// every gate in the list passed, and there was at least one — which is what
// keeps R5.7: no package becomes proved or errored that was not proved or
// errored before. A run where one gate passed and another declined is therefore
// Inconclusive, because WorstOutcome answers SKIPPED for it today; that case is
// half-measured, and half-measured is not proved.
//
// PRECONDITION FOR THE CALLER: pass only the gates that DECIDE. WorstOutcome
// drops the pkgcheck gate before counting (D8) and the adapter must drop it
// here too. A QA gate that declined on a host without pkgcheck — which is every
// package on such a host — would otherwise turn every Proved into Inconclusive
// and break R5.7 across the whole overlay.
func Classify(policySkipped bool, gates []GateFact) Outcome {
	// D2's first line. Before any gate is consulted: what the operator
	// decided outranks what the toolkit managed.
	if policySkipped {
		return Skipped
	}

	passing := 0
	for _, gate := range gates {
		if !gate.Deciding {
			// The gate declined. It said nothing about this bump, so it
			// contributes no pass — and its Cause is NOT read, deliberately.
			// An unrecorded cause must not become Skipped (D3).
			continue
		}
		if gate.Failed {
			return Errored
		}
		if gate.Passed {
			passing++
		}
		// A deciding gate that neither passed nor failed answered nothing.
		// It counts as no pass and the result falls through below: silence
		// is never read as a pass.
	}

	// EVERY gate passed, and there was one: WorstOutcome's own condition, so
	// no package becomes proved that was not proved before (R5.7). One gate
	// declining is enough to make the result half-measured, and half-measured
	// falls through to Inconclusive.
	if passing > 0 && passing == len(gates) {
		return Proved
	}
	return Inconclusive
}
