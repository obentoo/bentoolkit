package report

// Authored for story 046, sub-task 3.1 — R7.4, R4.2.
//
// Written from the contract: design.md D1 — "report.Report becomes
// report.AutoupdateCheck and implements Payload. Its fields, JSON tags and
// Reconciles() are unchanged — the rename is a move into the payload position,
// not a redesign of facts that story 044 got right." Unchanged Behavior 6 of
// story.md fixes the four column names and their meanings, and sub-task 3.1's
// own validation pins the Tally's tags byte-identical to HEAD.
//
// The names assumed beyond D1: AutoupdateCheck.Sections (Payload's one method,
// declared in D1) and Run.Sections — the envelope's own, because the
// "Run Interrupted" block is built from Complete and NotEvaluated, and D1 puts
// both in the ENVELOPE and neither in the payload. Something above the payload
// therefore has to state the gap, and this is the test that says so.
//
// Red on arrival: AutoupdateCheck does not exist — the type is still Report,
// and it has no Sections method.

import (
	"reflect"
	"strconv"
	"strings"
	"testing"
)

// checkFixture is one complete check: four planned packages, one per outcome
// column, so the tally has a non-zero count in each and reconciles against a
// plan of four.
func checkFixture() AutoupdateCheck {
	return AutoupdateCheck{
		Scanned: []PackageResult{
			{Package: "app-misc/jq", Type: "source", CurrentVersion: "1.7.1", CandidateVersion: "1.8.0", HasUpdate: true},
			{Package: "dev-lang/go", Type: "source", CurrentVersion: "1.26.0", CandidateVersion: "1.26.0"},
		},
		Plan: []PlanEntry{
			{Package: "a/proved", CurrentVersion: "1", CandidateVersion: "2", Class: "minor", Depth: "compile", Reason: "minor bump earns compile"},
			{Package: "a/errored", CurrentVersion: "1", CandidateVersion: "2", Class: "patch", Depth: "configure", Reason: "patch bump earns configure"},
			{Package: "a/inconclusive", CurrentVersion: "1", CandidateVersion: "2", Class: "patch", Depth: "manifest", Reason: "patch bump earns manifest"},
			{Package: "a/policy", CurrentVersion: "1", CandidateVersion: "2", Class: "patch", Depth: "none", Reason: "depth=none in the registry", Skipped: true},
		},
		Results: []ValidationRow{
			{Package: "a/proved", CandidateVersion: "2", Outcome: Proved, Depth: "compile", Reason: "every deciding gate passed"},
			{Package: "a/errored", CandidateVersion: "2", Outcome: Errored, Depth: "configure", Reason: "configure failed"},
			{Package: "a/inconclusive", CandidateVersion: "2", Outcome: Inconclusive, Depth: "none", Reason: "no Manifest could be produced"},
			{Package: "a/policy", CandidateVersion: "2", Outcome: Skipped, Depth: "none", Reason: "depth=none in the registry", SameReasonAsPlan: true},
		},
		Tally: Tally{Proved: 1, Errored: 1, Inconclusive: 1, Skipped: 1},
	}
}

// TestTallyKeepsItsFourColumnNames pins Unchanged Behavior 6 at the wire. The
// rename moves a type; it must not move a key a consumer has already shipped a
// jq expression against, and the tags are checked by reading them rather than
// by rendering a document, so the assertion holds whatever the envelope does
// above it.
func TestTallyKeepsItsFourColumnNames(t *testing.T) {
	want := map[string]string{
		"Proved":       "proved",
		"Errored":      "errored",
		"Inconclusive": "inconclusive",
		"Skipped":      "skipped",
	}

	rt := reflect.TypeOf(Tally{})
	if rt.NumField() != len(want) {
		t.Errorf("Tally has %d fields, want exactly the %d outcome columns — a fifth column is a change to what the tally MEANS (Unchanged Behavior 6)", rt.NumField(), len(want))
	}

	for field, tag := range want {
		f, ok := rt.FieldByName(field)
		if !ok {
			t.Errorf("Tally has no %s column", field)
			continue
		}
		if got, _, _ := strings.Cut(f.Tag.Get("json"), ","); got != tag {
			t.Errorf("Tally.%s serializes as %q, want %q — renaming a key renames it under a consumer who already ships a jq expression", field, got, tag)
		}
	}
}

// TestTallyReconcilesAgainstThePlan pins S044-R5.5 as this story inherits it: the
// denominator is the plan, never the result list.
//
// The two failing cases are the point. A package counted TWICE and a package
// counted in NO column are the two ways the tally can lie, and both are
// invisible to a check that only asks whether the four numbers are non-negative.
func TestTallyReconcilesAgainstThePlan(t *testing.T) {
	cases := map[string]struct {
		mutate func(*AutoupdateCheck)
		want   bool
	}{
		"one package per column": {
			mutate: func(*AutoupdateCheck) {},
			want:   true,
		},
		"a package counted twice": {
			mutate: func(c *AutoupdateCheck) { c.Tally.Proved++ },
			want:   false,
		},
		"a package counted in no column": {
			mutate: func(c *AutoupdateCheck) { c.Tally.Skipped-- },
			want:   false,
		},
		"an interrupted run that never reached two packages": {
			mutate: func(c *AutoupdateCheck) { c.Tally = Tally{Proved: 1, Errored: 1} },
			want:   false,
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			check := checkFixture()
			tc.mutate(&check)

			if got := check.Reconciles(); got != tc.want {
				t.Errorf("Reconciles() = %v, want %v (tally totals %d over a plan of %d)",
					got, tc.want, check.Tally.Total(), len(check.Plan))
			}
		})
	}
}

// TestCheckSectionsKeepTheirOrder pins the half of R7.4 a rename can silently
// break: the check's blocks are the same blocks, in the same order, as before
// the payload existed.
//
// The order is asserted as a SEQUENCE and not as a set. A report whose summary
// arrives before its results says the same things in an order that answers a
// different question, and no golden file in the render package would catch it —
// they are regenerated from whatever the code produces.
func TestCheckSectionsKeepTheirOrder(t *testing.T) {
	got := titlesOf(checkFixture().Sections(SectionOptions{}))
	want := []string{"Version Check Results", "Validation Plan", "Validation Results", "Validation Summary"}

	if !reflect.DeepEqual(got, want) {
		t.Errorf("the check's sections are\n  got: %q\n want: %q", got, want)
	}
}

// TestCheckWithNoPlanSaysNothingAboutOne pins the volume decision story 045
// made and this story inherits: a run that planned nothing omits the plan, the
// results and the tally rather than stating each one's emptiness.
func TestCheckWithNoPlanSaysNothingAboutOne(t *testing.T) {
	check := checkFixture()
	check.Plan = nil
	check.Results = nil
	check.Tally = Tally{}

	got := titlesOf(check.Sections(SectionOptions{}))
	want := []string{"Version Check Results"}

	if !reflect.DeepEqual(got, want) {
		t.Errorf("a run that planned nothing produced %q, want %q — twelve lines saying the thing nobody asked for did not happen is the volume story 045 removed", got, want)
	}
}

// TestInterruptedRunStatesItsGapFirst pins R1.4 where the fact lives: Complete
// and NotEvaluated are in the ENVELOPE, so the payload cannot state the gap and
// the envelope must.
//
// FIRST, not merely present. Every section below the label is short by the
// packages the run never reached, so a reader who meets the qualifier at the
// bottom has already drawn a conclusion from tables that were missing rows.
//
// The zero case is asserted for the same reason story 044 asserted it: a run
// interrupted after its last package was evaluated lost nothing, and "0" is
// what says so — suppressing the number leaves that case indistinguishable from
// a report silent about how much is missing.
func TestInterruptedRunStatesItsGapFirst(t *testing.T) {
	for _, notEvaluated := range []int{2, 0} {
		t.Run("unreached="+strconv.Itoa(notEvaluated), func(t *testing.T) {
			run := Run{
				Schema:       SchemaVersion,
				Kind:         KindAutoupdateCheck,
				Complete:     false,
				NotEvaluated: notEvaluated,
				Payload:      checkFixture(),
			}

			blocks := run.Sections(SectionOptions{})
			if len(blocks) == 0 {
				t.Fatal("an interrupted run rendered no section at all")
			}

			first := blocks[0]
			if first.Title == "Version Check Results" {
				t.Fatalf("the interrupted run leads with %q — the gap is stated below tables it already explains", first.Title)
			}

			said := strings.Join(append([]string{first.Title}, append(first.Lead, first.Notes...)...), "\n")
			if !strings.Contains(said, strconv.Itoa(notEvaluated)) {
				t.Errorf("the leading block does not state how many units were never reached (%d):\n%s", notEvaluated, said)
			}
		})
	}
}

// TestCompleteRunCarriesNoGapLabel is the converse: a run that finished must
// NOT carry the label, or the label stops meaning anything.
func TestCompleteRunCarriesNoGapLabel(t *testing.T) {
	run := Run{
		Schema:   SchemaVersion,
		Kind:     KindAutoupdateCheck,
		Complete: true,
		Payload:  checkFixture(),
	}

	got := titlesOf(run.Sections(SectionOptions{}))
	want := titlesOf(checkFixture().Sections(SectionOptions{}))

	if !reflect.DeepEqual(got, want) {
		t.Errorf("a complete run's sections differ from its payload's:\n  got: %q\n want: %q", got, want)
	}
}

func titlesOf(blocks []Section) []string {
	titles := make([]string, 0, len(blocks))
	for _, b := range blocks {
		titles = append(titles, b.Title)
	}
	return titles
}

// TestScanSection_AManualPromotionIsAnAnswerNotAnError: a package that left
// its series is listed as a manual promotion with the reason, counted in its
// own note, and never among the packages that could not be checked.
func TestScanSection_AManualPromotionIsAnAnswerNotAnError(t *testing.T) {
	s := versionCheckSection(AutoupdateCheck{Scanned: []PackageResult{{
		Package: "dev-lang/dart@stable", CurrentVersion: "3.13.7", CandidateVersion: "3.14.0",
		ManualPromotion: `upstream version "3.14.0" is outside this entry's series "^3\.13\.7$"`,
	}}}, false)

	if len(s.Rows.Rows) != 1 || s.Rows.Rows[0].Cells[4] != "manual promotion" || !strings.Contains(s.Rows.Rows[0].Detail, "outside this entry's series") {
		t.Fatalf("rows = %+v, want one manual promotion row with its reason", s.Rows.Rows)
	}
	notes := strings.Join(s.Notes, "\n")
	if !strings.Contains(notes, "1 package(s) left their series upstream and need a manual promotion.") || strings.Contains(notes, "could not be checked") {
		t.Errorf("notes do not count the manual promotion on its own:\n%s", notes)
	}
}
