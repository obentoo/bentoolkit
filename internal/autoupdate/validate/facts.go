package validate

import "github.com/obentoo/bentoolkit/internal/common/report"

// ReportGateFacts reduces one result's gates to the facts report.Classify and
// report.StateOf read, dropping the QA gate, and marks the gate that answers
// selectedDepth. The report and the stage record both derive a bump's state
// from it, so the two cannot disagree on the rule.
func ReportGateFacts(gates []GateResult, selectedDepth string) []report.GateFact {
	facts := make([]report.GateFact, 0, len(gates))

	// The depth->gate mapping is resolved HERE and travels as a bool, because
	// internal/common/report must not import internal/autoupdate. This is
	// the same crossing Cause makes as a plain string.
	//
	// A depth that does not parse, or one no gate stands for, marks nothing:
	// selectedGate stays empty and no fact matches it, so the result falls
	// through to Inconclusive instead of guessing a gate whose pass would read
	// as proof of a rung nobody climbed.
	selectedGate := ""
	if depth, err := ParseDepth(selectedDepth); err == nil {
		if gate, ok := GateForDepth(depth); ok {
			selectedGate = gate
		}
	}

	for _, gate := range gates {
		if gate.Gate == GateQA {
			continue
		}

		facts = append(facts, report.GateFact{
			Deciding:            gate.Outcome != OutcomeSkipped,
			Passed:              gate.Outcome == OutcomePass,
			Failed:              gate.Outcome == OutcomeFailed,
			Cause:               string(gate.Declined),
			ProvesSelectedDepth: selectedGate != "" && gate.Gate == selectedGate,
		})
	}

	return facts
}
