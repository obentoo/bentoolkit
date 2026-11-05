package report

import (
	"encoding/json"
	"strings"
	"testing"
)

// TestRunSections_GapBlockNamesItsCause pins the two gap blocks: a run cut
// short says "interrupted", and a run that reached every unit and left some
// without an answer must not. On 2026-11-05 overlay compare printed "the run
// was interrupted" over 19 version pairs the content check had refused, and
// nobody had interrupted anything.
func TestRunSections_GapBlockNamesItsCause(t *testing.T) {
	cases := []struct {
		interrupted bool
		title, says string
	}{
		{true, "Run Interrupted", "was interrupted"},
		{false, "Run Incomplete", "reached every unit"},
	}
	for _, c := range cases {
		gap := Run{Complete: false, NotEvaluated: 19, Interrupted: c.interrupted}.Sections(SectionOptions{})[0]
		lead := strings.Join(gap.Lead, " ")
		if gap.Title != c.title || !strings.Contains(lead, c.says) || !strings.Contains(lead, "19 planned unit(s)") {
			t.Errorf("interrupted=%v: got %q / %q, want title %q saying %q and the count",
				c.interrupted, gap.Title, lead, c.title, c.says)
		}
		if !c.interrupted && strings.Contains(lead, "interrupted") {
			t.Errorf("an uninterrupted run claims an interruption: %q", lead)
		}
	}
}

// TestRun_InterruptedIsAlwaysExported keeps the field in the document even when
// false, for the reason Complete has no omitempty.
func TestRun_InterruptedIsAlwaysExported(t *testing.T) {
	b, err := json.Marshal(Run{Schema: SchemaVersion})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), `"interrupted":false`) {
		t.Errorf("export %s lacks \"interrupted\":false", b)
	}
}
