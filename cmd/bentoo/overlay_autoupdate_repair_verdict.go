package main

import (
	"fmt"
	"strings"

	"github.com/obentoo/bentoolkit/internal/autoupdate/validate"
	"github.com/obentoo/bentoolkit/internal/common/i18n"
)

// repeatedCauseMin is the shortest cause worth folding: a short one reads
// faster repeated than as a pointer to the line above.
const repeatedCauseMin = 80

// verdictReasons renders one line per gate of a re-validation. A gate whose
// reason ends in a cause an earlier gate already printed keeps its own
// opening and points at that gate instead of repeating it: a staged tree
// that cannot be adopted, or a manifest step that failed, declines every gate
// with the same long text, and the 2026-11-26 batch printed the breakpad
// refusal and its cp three times. Only the screen folds it; the report keeps
// each gate's whole reason.
func verdictReasons(gates []validate.GateResult) []string {
	firstGate := map[string]string{}
	lines := make([]string, 0, len(gates))
	for _, g := range gates {
		line := fmt.Sprintf("    %-12s %s", g.Gate, g.Outcome)
		if g.Reason != "" && g.Outcome != validate.OutcomePass {
			head, cause, found := strings.Cut(g.Reason, ": ")
			switch earlier, seen := firstGate[cause]; {
			case found && seen:
				line += fmt.Sprintf(": %s: "+i18n.T("(the same cause as %s)"), head, earlier)
			default:
				line += ": " + g.Reason
				if found && len(cause) >= repeatedCauseMin {
					firstGate[cause] = g.Gate
				}
			}
		}
		lines = append(lines, line)
	}
	return lines
}
