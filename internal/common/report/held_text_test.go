package report

import (
	"strings"
	"testing"
)

// TestVersionCheckHeldUpdate pins how a candidate held by a record's
// min_age_days gate reads: its own state with the hold line under the row, not
// counted as a pending update (nothing was queued), and counted in a note of
// its own so a scan that only prints the lead still says one was held.
func TestVersionCheckHeldUpdate(t *testing.T) {
	const line = "held by min_age_days: 2.1.310 published 2026-11-05T12:00:00Z, eligible 2026-11-12T12:00:00Z"
	r := AutoupdateCheck{Scanned: []PackageResult{
		{Package: "dev-util/claude-code", Type: "bin", CurrentVersion: "2.1.300", CandidateVersion: "2.1.310", HasUpdate: true, Held: line},
		{Package: "dev-util/other", Type: "bin", CurrentVersion: "1.0", CandidateVersion: "1.1", HasUpdate: true},
	}}
	s := versionCheckSection(r, false)

	if want := "2 package(s) checked, 1 with a pending update."; len(s.Lead) == 0 || s.Lead[0] != want {
		t.Errorf("lead = %q, want %q", s.Lead, want)
	}
	var held *Row
	for i := range s.Rows.Rows {
		if s.Rows.Rows[i].Cells[0] == "dev-util/claude-code" {
			held = &s.Rows.Rows[i]
		}
	}
	if held == nil {
		t.Fatalf("the held package is not listed: %+v", s.Rows.Rows)
	}
	if state := held.Cells[4]; !strings.HasPrefix(state, "held") {
		t.Errorf("STATE = %q, want held", state)
	}
	if !strings.Contains(held.Detail, line) {
		t.Errorf("detail %q does not carry the hold line", held.Detail)
	}
	if !strings.Contains(strings.Join(s.Notes, "\n"), "1 package(s) have an update held by min_age_days.") {
		t.Errorf("no held note in %q", s.Notes)
	}
}
