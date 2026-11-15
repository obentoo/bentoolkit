package report

import "testing"

func TestStateOf(t *testing.T) {
	declined := func(cause string) GateFact { return GateFact{Cause: cause} }
	passed := GateFact{Deciding: true, Passed: true}

	cases := []struct {
		name    string
		outcome Outcome
		gates   []GateFact
		want    State
	}{
		{"proved", Proved, []GateFact{passed}, Ready},
		{"policy skip", Skipped, nil, Excluded},
		{"a gate failed", Errored, []GateFact{{Deciding: true, Failed: true}}, Human},
		{"host declined only", Inconclusive, []GateFact{passed, declined("host"), declined("host")}, Wait},
		{"candidate beats host", Inconclusive, []GateFact{declined("host"), declined("candidate")}, Human},
		{"no cause recorded", Inconclusive, []GateFact{declined("")}, Human},
		{"host beside an unrecorded cause", Inconclusive, []GateFact{declined("host"), declined("")}, Human},
		{"nothing at all", Inconclusive, nil, Human},
		{"selected gate waits on the host, options had no cause", Inconclusive,
			[]GateFact{declined(""), {Cause: "host", ProvesSelectedDepth: true}}, Wait},
		{"selected gate blames the candidate", Inconclusive,
			[]GateFact{declined("host"), {Cause: "candidate", ProvesSelectedDepth: true}}, Human},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := StateOf(tc.outcome, tc.gates); got != tc.want {
				t.Errorf("StateOf = %s, want %s", got, tc.want)
			}
		})
	}
}

func TestStateTally_AddsEachStateOnce(t *testing.T) {
	var tally StateTally
	for _, s := range []State{Ready, Wait, Human, Excluded, Wait} {
		tally.Add(s)
	}
	if tally != (StateTally{Ready: 1, Wait: 2, Human: 1, Excluded: 1}) {
		t.Errorf("tally = %+v", tally)
	}
}

// TestCheckSections_StateTheNextStep: the results table shows each bump's
// State, and the summary says how many an apply could publish.
func TestCheckSections_StateTheNextStep(t *testing.T) {
	check := AutoupdateCheck{
		Plan: []PlanEntry{{Package: "app-misc/a"}, {Package: "app-misc/b"}},
		Results: []ValidationRow{
			{Package: "app-misc/a", CandidateVersion: "2", Depth: "configure", Outcome: Proved, State: Ready},
			{Package: "app-misc/b", CandidateVersion: "3", Depth: "none", Outcome: Inconclusive, State: Wait},
		},
		Tally:  Tally{Proved: 1, Inconclusive: 1},
		States: StateTally{Ready: 1, Wait: 1},
	}

	var results, summary Section
	for _, s := range check.Sections(SectionOptions{}) {
		switch s.Title {
		case "Validation Results":
			results = s
		case "Validation Summary":
			summary = s
		}
	}

	if got := results.Rows.Headers[len(results.Rows.Headers)-1]; got != "STATE" {
		t.Errorf("last results column is %q, want STATE", got)
	}
	if got := results.Rows.Rows[1].Cells[3]; got != string(Wait) {
		t.Errorf("row b shows %q, want %s", got, Wait)
	}
	want := "Next: 1 ready to apply, 1 waiting, 0 for the maintainer, 0 excluded."
	found := false
	for _, line := range summary.Lead {
		found = found || line == want
	}
	if !found {
		t.Errorf("summary lead %q lacks %q", summary.Lead, want)
	}
}
