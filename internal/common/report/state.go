package report

// State is what the operator does next with a planned bump. It sits on top of
// Outcome: Outcome says what the gates measured, State says who acts.
type State string

const (
	// Ready means the bump holds a proof at the depth the policy asked for;
	// an apply may publish it.
	Ready State = "READY"
	// Wait means only the environment stood in the way (a missing or blocking
	// package, the network, the disk); running the check again later, or on
	// another backend, may prove it without touching the ebuild.
	Wait State = "WAIT"
	// Human means the maintainer must act: a gate failed, the candidate itself
	// could not be read, or nothing recorded whose fault it was.
	Human State = "HUMAN"
	// Excluded means the policy asked for no gate, so nothing was proven and
	// nothing will be published until the policy asks for a depth.
	Excluded State = "EXCLUDED"
)

// StateOf derives a bump's State from its Outcome and the same gate facts
// Classify read.
//
// For an Inconclusive bump the question is who stood between it and the proof
// the policy asked for, so the gate marked ProvesSelectedDepth answers alone
// when it declined: an option gate that merely had nothing to compare must not
// turn a bump that only waits on a dependency into the maintainer's problem.
// With no such gate, every declined gate is read. A "candidate" cause outranks
// "host", and a gate with no recorded cause is Human, because retrying cannot
// settle what nobody diagnosed.
func StateOf(outcome Outcome, gates []GateFact) State {
	switch outcome {
	case Proved:
		return Ready
	case Skipped:
		return Excluded
	case Errored:
		return Human
	case Inconclusive:
		// Who stood in the way decides; read below.
	}

	for _, gate := range gates {
		if gate.ProvesSelectedDepth && !gate.Deciding {
			return stateOfCauses([]GateFact{gate})
		}
	}
	return stateOfCauses(gates)
}

// stateOfCauses reads the causes of the declined gates among gates.
func stateOfCauses(gates []GateFact) State {
	host := false
	for _, gate := range gates {
		if gate.Deciding {
			continue
		}
		if gate.Cause != "host" {
			return Human
		}
		host = true
	}
	if host {
		return Wait
	}
	return Human
}

// StateTally counts planned bumps by State.
type StateTally struct {
	Ready    int `json:"ready"`
	Wait     int `json:"wait"`
	Human    int `json:"human"`
	Excluded int `json:"excluded"`
}

// Add counts one bump in its State's column.
func (t *StateTally) Add(s State) {
	switch s {
	case Ready:
		t.Ready++
	case Wait:
		t.Wait++
	case Excluded:
		t.Excluded++
	default:
		t.Human++
	}
}
