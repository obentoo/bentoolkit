package tui

// Authored for story 092, sub-task 1.3 (R2.2): the repair hint on the ✗ row.
// Kept in its own file so its new TaskDoneMsg field does not mask the Red
// reasons of model_s092_test.go.

import (
	"strings"
	"testing"
)

// R2.2: a repair hint carried on TaskDoneMsg is appended to the ✗ row; with no
// hint the row names no repair (the hint never fires on its own).
func TestModelFailureLineRepairHint(t *testing.T) {
	hint := "(repair: bentoo overlay autoupdate --repair dev-go/govulncheck)"
	rows := s092Frame(120, 24,
		BatchStartMsg{Total: 2},
		TaskStartMsg{ID: "p1", Label: "dev-go/govulncheck"},
		TaskDoneMsg{ID: "p1", OK: false, Summary: "manifest failed", Hint: hint},
		TaskStartMsg{ID: "p2", Label: "dev-go/other"},
		TaskDoneMsg{ID: "p2", OK: false, Summary: "manifest failed"},
	)
	frame := s092Rows(rows)
	var withHint, without string
	for _, r := range rows {
		if strings.HasPrefix(r, "✗ dev-go/govulncheck") {
			withHint = r
		}
		if strings.HasPrefix(r, "✗ dev-go/other") {
			without = r
		}
	}
	if !strings.HasSuffix(withHint, hint) || !strings.Contains(withHint, "manifest failed") {
		t.Errorf("the ✗ row does not end with the repair hint: %q\n%s", withHint, frame)
	}
	if strings.Contains(without, "repair") {
		t.Errorf("a failure without a bundle names a repair: %q", without)
	}
	if strings.Count(frame, "--repair") != 1 {
		t.Errorf("the repair hint appears %d times, want once:\n%s", strings.Count(frame, "--repair"), frame)
	}
}
