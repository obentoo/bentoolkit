package report

import "testing"

// The helpers below fix what each field of GateFact means, because the rule
// reads them and a misreading changes the answer:
//
//   Deciding — did this gate PARTICIPATE IN THE VERDICT. A gate that declined
//   did not, so a declined gate is Deciding: false. "Deciding" is not "was
//   planned"; a gate can be planned and still decline.
//
//   Cause    — why it declined: "candidate", "host", or "" when the producer
//   did not say. Note that Cause does NOT change the answer today: all three
//   land on Inconclusive (D2 says both named causes are limitations, D3 says
//   the unrecorded one fails open to the same column). It is carried because
//   R5.6 requires the output to NAME the cause, and because D3 expects the rule
//   to tighten as producers learn to tag. Do not branch on it here.

func passingGate() GateFact { return GateFact{Deciding: true, Passed: true} }
func failingGate() GateFact { return GateFact{Deciding: true, Failed: true} }
func declinedGate(cause string) GateFact {
	return GateFact{Deciding: false, Cause: cause}
}

// TestClassifyTable walks D2's four-line rule.
//
// One case is deliberately ABSENT: a run where one deciding gate passed and
// another declined. The design does not fix that answer, and R5.7 measures it
// where it is measurable — sub-task 6.2 asserts the Proved and Errored counts
// are byte-identical to the current implementation over a fixture set. Pinning
// a guess here would freeze whichever answer the author happened to assume.
func TestClassifyTable(t *testing.T) {
	cases := []struct {
		name          string
		policySkipped bool
		gates         []GateFact
		want          Outcome
	}{
		{
			name:          "policy said no, and the gates would have passed",
			policySkipped: true,
			gates:         []GateFact{passingGate(), passingGate()},
			want:          Skipped,
		},
		{
			name:          "policy said no, and no gate was planned at all",
			policySkipped: true,
			gates:         nil,
			want:          Skipped,
		},
		{
			name:  "every gate declined because of the host",
			gates: []GateFact{declinedGate("host"), declinedGate("host")},
			want:  Inconclusive,
		},
		{
			name:  "every gate declined because of the candidate",
			gates: []GateFact{declinedGate("candidate")},
			want:  Inconclusive,
		},
		{
			name:  "a gate declined and recorded no cause",
			gates: []GateFact{declinedGate("")},
			want:  Inconclusive,
		},
		{
			name:  "no gates at all",
			gates: nil,
			want:  Inconclusive,
		},
		{
			name:  "one deciding gate failed",
			gates: []GateFact{passingGate(), failingGate()},
			want:  Errored,
		},
		{
			name:  "a deciding gate failed while another declined",
			gates: []GateFact{failingGate(), declinedGate("host")},
			want:  Errored,
		},
		{
			name:  "every deciding gate passed",
			gates: []GateFact{passingGate(), passingGate()},
			want:  Proved,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := Classify(tc.policySkipped, tc.gates); got != tc.want {
				t.Fatalf("Classify(%v, %+v) = %q, want %q", tc.policySkipped, tc.gates, got, tc.want)
			}
		})
	}
}

// TestClassifyPolicyWinsBeforeTheGates pins D2's first line: policy is read
// BEFORE a gate is consulted. Consulting the gates first would report a
// toolkit failure for a package the operator told the toolkit to leave alone —
// RC2 in the other direction.
func TestClassifyPolicyWinsBeforeTheGates(t *testing.T) {
	got := Classify(true, []GateFact{failingGate()})

	if got != Skipped {
		t.Fatalf("Classify(policySkipped=true, [a failing gate]) = %q, want %q: "+
			"policy is consulted before the gates, so a failing gate on a package "+
			"the operator excluded is still Skipped", got, Skipped)
	}
}

// TestClassifyUnrecordedCauseIsInconclusive pins D3's fail-open. An empty Cause
// means the producer did not say why it declined — a toolkit limitation, not an
// operator instruction. Routing it to Skipped restates RC2 with new labels:
// an untagged toolkit failure hiding behind the operator's policy.
//
// The inconclusive column WILL be large in the first releases. That is the
// honest reading, not a defect.
func TestClassifyUnrecordedCauseIsInconclusive(t *testing.T) {
	got := Classify(false, []GateFact{declinedGate("")})

	if got != Inconclusive {
		t.Fatalf("Classify(policySkipped=false, [a gate that declined with no cause]) = %q, want %q: "+
			"nobody said this was policy, so it is a limitation (D3)", got, Inconclusive)
	}
}

// TestClassifyNeverProvesWithoutADecidingGate is the invariant R5.7 leans on.
// "Proved" claims the toolkit established the package builds; with no gate
// participating in the verdict, nothing established anything.
func TestClassifyNeverProvesWithoutADecidingGate(t *testing.T) {
	withoutADecidingGate := [][]GateFact{
		nil,
		{},
		{declinedGate("host")},
		{declinedGate("candidate")},
		{declinedGate("")},
		{declinedGate("host"), declinedGate(""), declinedGate("candidate")},
	}

	for _, gates := range withoutADecidingGate {
		for _, policySkipped := range []bool{false, true} {
			if got := Classify(policySkipped, gates); got == Proved {
				t.Errorf("Classify(%v, %+v) = %q: no gate participated in the verdict, so nothing was proved",
					policySkipped, gates, got)
			}
		}
	}
}

// provingGate is the gate that stands for the depth the policy selected.
func provingGate() GateFact {
	return GateFact{Deciding: true, Passed: true, ProvesSelectedDepth: true}
}

// TestClassifySelectedDepthProves covers S043-R4: a half-measured package whose
// SELECTED depth was measured and passed is proved at that depth, rather than
// falling to Inconclusive because some other gate declined.
//
// The case this removes, from a real run: `dev-libs/icu-compat` passed the
// `configure` gate — the depth the policy asked for — and passed `patches` too,
// and the summary printed `not validated (no distfile named by the Manifest is
// present …)`. The operator was told about a missing distfile and never told the
// package had configured.
func TestClassifySelectedDepthProves(t *testing.T) {
	cases := []struct {
		name          string
		policySkipped bool
		gates         []GateFact
		want          Outcome
	}{
		{
			name:  "selected depth passed, another gate declined",
			gates: []GateFact{declinedGate("candidate"), passingGate(), provingGate()},
			want:  Proved,
		},
		{
			name:  "selected depth passed and it is the only gate that did",
			gates: []GateFact{declinedGate("host"), declinedGate("candidate"), provingGate()},
			want:  Proved,
		},

		// The ordering guards. Each of these would have been broken by the
		// original R4, which decided before consulting policy.
		{
			name:          "POLICY OUTRANKS IT: excluded package stays skipped",
			policySkipped: true,
			gates:         []GateFact{declinedGate("candidate"), provingGate()},
			want:          Skipped,
		},
		{
			name:  "FAILURE OUTRANKS IT: a failing gate still errors",
			gates: []GateFact{provingGate(), failingGate()},
			want:  Errored,
		},

		// No mark: the depth did not parse, or no gate stands for it.
		{
			name:  "nothing marked, and a gate declined",
			gates: []GateFact{declinedGate("candidate"), passingGate()},
			want:  Inconclusive,
		},
		{
			name:  "the marked gate did not pass",
			gates: []GateFact{declinedGate("candidate"), {Deciding: true, ProvesSelectedDepth: true}},
			want:  Inconclusive,
		},
		{
			name:  "the marked gate declined",
			gates: []GateFact{passingGate(), {Deciding: false, ProvesSelectedDepth: true}},
			want:  Inconclusive,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := Classify(tc.policySkipped, tc.gates); got != tc.want {
				t.Errorf("Classify() = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestClassifyEveryGatePassedStillProvesWithoutTheMark keeps the older Proved
// condition reachable on its own. A run where every gate passed is proved
// whether or not anything was marked — the mark widens the rule, it does not
// become a precondition of it.
func TestClassifyEveryGatePassedStillProvesWithoutTheMark(t *testing.T) {
	if got := Classify(false, []GateFact{passingGate(), passingGate()}); got != Proved {
		t.Errorf("Classify() = %v, want %v", got, Proved)
	}
}
