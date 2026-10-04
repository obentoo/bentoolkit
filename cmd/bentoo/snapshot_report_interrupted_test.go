package main

// Authored for story 046, sub-task 16.2 — R1.4.
//
// Written from the contract: R1.4 — "IF a run stops before reaching the end of
// its work, THEN THE SYSTEM SHALL render the report established up to that
// point AND STATE HOW MANY UNITS IT NEVER REACHED" — and design.md's Error
// Handling Strategy: "A report is never skipped because it would be partial."
//
// # Why this file exists although R1.4 already has a test
//
// design.md D6 chose TWO proving consumers precisely so the envelope would be
// exercised at distance from itself, and R1.4's interrupted half was proved on
// one of the two. overlay_manifest_interrupted_test.go is `overlay manifest`'s
// half; this is `snapshot run`'s, which was absent.
//
// internal/common/report/snapshot_run_test.go declares a test whose NAME reads
// like this property — …IncompleteRunStatesItsGap — and whose body asserts the
// payload's EMPTY-COUNTS arm: a run that established nothing. R1.4 is about a
// run that started, did some work and stopped, which is a different run and a
// different sentence. Nothing here duplicates that test: it is the payload's
// no-step arm, and these are the adapter's interrupted arm.
//
// # Where the interruption is applied, and why at two levels
//
// R1.4 has two halves and they live in different places. What the report SAYS
// is decided by buildSnapshotReport, which takes the interruption as an
// argument — so the first two tests hand it a snapshot.RunResult whose stages
// stop short and read the run it returns, with no timing in the fixture at all.
// What the RUN does about it — the exit status the interruption earned — is
// decided by runSnapshotRun, so the third test interrupts a real `snapshot run`
// with SIGTERM, the shape story 043 established in
// overlay_autoupdate_signal_test.go. The run goes through func runMain, whose
// func processContext installs the handler for the process and whose root
// PersistentPreRunE marks `snapshot run` cancellable from its annotation, so
// the signal is CAUGHT — the test process is not terminated — and only the
// run's context is cancelled.
//
// The second test is the hostile half. A rule that says "state the gap when the
// run stopped short" can be satisfied by a report that says it of every run,
// and the run that would expose that is the one whose steps failed but whose
// plan was reached to the end — a failed run is not an interrupted one.
//
// # This file was NOT red on arrival, and the sentence that stood here said it
// # would be
//
// What `snapshot run` was missing is the TEST, not the behaviour:
// buildSnapshotReport already computed the gap, so materializing this file
// produced three passes. The claim that stood in this spot — "Red on arrival" —
// was written before the run that would have checked it, and a header asserting
// a failure nobody observed is the same defect, one level up, that this file
// exists to close: a name or a note that promises what its body does not hold.
//
// Its Red is therefore RECORDED BY MUTATION, the precedent 1.2, 7.3 and 15.6 set
// for a guard that passes on the day it is written (R8.3). Five mutations,
// applied one at a time and each reverted immediately, are written out in
// .draft/red-evidence.yaml under task 16.2: the gap count forced to zero; the
// established steps dropped from the payload; the exit status swallowed on an
// interrupted run; a FAILED stage read as an unreached subvolume; and the gap
// left as a struct field no sentence ever states.
//
// Every test below is killed by at least one of them, and the second — the
// hostile half — is killed by exactly ONE and by nothing else: the mutation that
// mislabels the failed-but-complete run. That is what says it carries its own
// weight rather than restating the first.

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/obentoo/bentoolkit/internal/common/report"
	"github.com/obentoo/bentoolkit/internal/snapshot"
)

// snapshotReportProse is everything the report SAYS, in one string: the titles,
// the leads, the rows and the notes.
//
// The count R1.4 asks for is a sentence, and which section carries it is a
// layout decision this test has no business pinning — what it must pin is that
// a reader of the report meets the number. Joining the whole document is what
// lets the assertion be about the statement rather than about its address.
func snapshotReportProse(sections []report.Section) string {
	var b strings.Builder
	for _, section := range sections {
		b.WriteString(section.Title + "\n")
		for _, line := range section.Lead {
			b.WriteString(line + "\n")
		}
		for _, row := range section.Rows.Rows {
			b.WriteString(strings.Join(row.Cells, " ") + " " + row.Detail + "\n")
		}
		for _, note := range section.Notes {
			b.WriteString(note + "\n")
		}
	}
	return b.String()
}

// statesTheGap reports whether prose says, in one sentence, how many planned
// units the run never reached.
//
// The wording is the envelope's to choose; what the requirement fixes is that
// the NUMBER is stated and stated AS a gap, so both must appear in the same
// sentence. A document carrying the digit in one place and the phrase in
// another would satisfy two separate Contains checks and tell the reader
// nothing.
func statesTheGap(prose string, notEvaluated int) bool {
	for _, sentence := range strings.Split(prose, "\n") {
		lower := strings.ToLower(sentence)
		named := strings.Contains(lower, "not evaluated") ||
			strings.Contains(lower, "not reached") ||
			strings.Contains(lower, "never reached")
		if named && strings.Contains(sentence, fmt.Sprintf("%d", notEvaluated)) {
			return true
		}
	}
	return false
}

// TestSnapshotReportInterruptedCarriesItsStepsAndStatesTheGap is R1.4 on the
// second proving consumer: a run cut short after its first subvolume still
// reports what it established, and says how much of its plan it never reached.
func TestSnapshotReportInterruptedCarriesItsStepsAndStatesTheGap(t *testing.T) {
	planned := []string{"/home", "/var", "/srv"}

	// Everything the run established before it was cut short: one subvolume
	// created and pruned, and nothing at all about the other two.
	result := snapshot.RunResult{
		Stages: []snapshot.StageResult{
			{Subvolume: "/home", Stage: snapshot.StageCreate, Status: snapshot.StatusOK},
			{Subvolume: "/home", Stage: snapshot.StagePrune, Status: snapshot.StatusOK},
		},
		Err: "context canceled",
	}

	run := buildSnapshotReport(&result, planned, true)

	if run.Complete {
		t.Error("an interrupted run reported Complete — a report that does not admit it is partial is worse than one that will not fit (R1.4)")
	}
	if got, want := run.NotEvaluated, 2; got != want {
		t.Errorf("the run states %d planned unit(s) never reached, want %d — it stopped after the first of three subvolumes (R1.4)", got, want)
	}

	payload, ok := run.Payload.(report.SnapshotRun)
	if !ok {
		t.Fatalf("the interrupted run carries no snapshot payload: %T — the report established up to that point was thrown away (R1.4)", run.Payload)
	}
	if len(payload.Steps) != len(result.Stages) {
		t.Errorf("the report carries %d step(s) over a run that established %d — an interrupted run reports what it reached (R1.4)",
			len(payload.Steps), len(result.Stages))
	}
	if payload.Ok != 2 {
		t.Errorf("the report counts %d succeeded step(s), want 2 — the work done before the interrupt is still the run's answer (R1.4, R1.6)", payload.Ok)
	}

	prose := snapshotReportProse(run.Sections(report.SectionOptions{ShowAll: true}))
	if !strings.Contains(prose, "/home") {
		t.Errorf("the rendered report never names the subvolume the run did reach (R1.4, R1.6):\n%s", prose)
	}
	if !statesTheGap(prose, run.NotEvaluated) {
		t.Errorf("no sentence in the report states how many planned units the run never reached (R1.4):\n%s", prose)
	}
}

// TestSnapshotReportInterruptedDoesNotFireOnARunThatReachedItsWholePlan is the
// hostile half of the same rule.
//
// A run whose create failed skipped that subvolume's remaining steps and is
// still COMPLETE: it reached the end of its plan and reported an outcome for
// every subvolume in it. Labelling that run "interrupted" would print a
// sentence that is not true of it, and a suite that only ever checks the label
// APPEARS cannot tell the two runs apart.
func TestSnapshotReportInterruptedDoesNotFireOnARunThatReachedItsWholePlan(t *testing.T) {
	planned := []string{"/home", "/var"}

	result := snapshot.RunResult{
		Stages: []snapshot.StageResult{
			{Subvolume: "/home", Stage: snapshot.StageCreate, Status: snapshot.StatusFailed, Err: "btrbk: no such subvolume"},
			{Subvolume: "/var", Stage: snapshot.StageCreate, Status: snapshot.StatusOK},
			{Subvolume: "/var", Stage: snapshot.StagePrune, Status: snapshot.StatusOK},
		},
		Err: "create /home failed",
	}

	run := buildSnapshotReport(&result, planned, false)

	if !run.Complete {
		t.Error("a run that reached every planned subvolume was reported as incomplete — a failed run is not an interrupted one (R1.4)")
	}
	if run.NotEvaluated != 0 {
		t.Errorf("the run states %d planned unit(s) never reached over a plan it reached to the end (R1.4)", run.NotEvaluated)
	}

	prose := snapshotReportProse(run.Sections(report.SectionOptions{ShowAll: true}))
	if strings.Contains(strings.ToLower(prose), "interrupted") {
		t.Errorf("the report of a run nobody interrupted says it was interrupted (R1.4):\n%s", prose)
	}
}

// TestSnapshotReportInterruptedKeepsTheExitStatusItEarned is the half no
// hand-built RunResult can reach: what the COMMAND does when the interrupt
// lands. The report is rendered, the export is written, and the run still exits
// with the status the interruption earned — a report is what an operator gets
// instead of a crash, not instead of an exit status.
func TestSnapshotReportInterruptedKeepsTheExitStatusItEarned(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("SIGTERM has no portable semantics on Windows")
	}

	c := newTestCLI(t)
	stubBinariesOnPath(t, "btrbk", "ssh")
	_, configPath := writeSnapshotConfig(t, `
[engine]
driver = "btrbk"
subvolumes = ["/home", "/var"]
snapshot_dir = "/.snapshots"

[[ship]]
type = "ssh"
target = "user@host:/backup"
`)
	redirectStateDir(t)

	// The interrupt is delivered from inside the run's first subprocess call,
	// and that call does not return until the run's OWN context has observed
	// it: the run is provably in flight when it is cut short and provably
	// cancelled when it continues. No sleep decides the outcome.
	var once sync.Once
	var observed bool
	c.deps.snapshotRunner = &snapshot.MockRunner{
		RunFunc: func(ctx context.Context, _ string, _ []string, _ []byte) ([]byte, error) {
			once.Do(func() {
				proc, err := os.FindProcess(os.Getpid())
				if err != nil {
					t.Errorf("FindProcess(self): %v", err)
					return
				}
				if err := proc.Signal(syscall.SIGTERM); err != nil {
					t.Errorf("signalling self: %v", err)
					return
				}
				select {
				case <-ctx.Done():
					observed = true
				case <-time.After(15 * time.Second):
				}
			})
			return nil, nil
		},
	}

	exportPath := filepath.Join(t.TempDir(), "interrupted.json")
	stdout, stderr, code := c.Run("snapshot", "--config="+configPath, "run", "--ui=plain", "--export="+exportPath)
	if strings.Contains(stderr, "unknown flag") {
		t.Fatalf("a flag was rejected: %s", stderr)
	}
	if !observed {
		t.Fatal("the run's context never observed the SIGTERM, so nothing was interrupted and this test would prove nothing")
	}

	if code == 0 {
		t.Errorf("the interrupted run exited 0 — a report does not buy back the status the interruption earned:\n%s", stdout)
	}
	if strings.TrimSpace(stdout) == "" {
		t.Fatal("an interrupted run printed nothing — the report established up to that point was thrown away (R1.4)")
	}
	if !strings.Contains(strings.ToLower(stdout), "nterrupt") && !strings.Contains(strings.ToLower(stdout), "not evaluated") {
		t.Errorf("the terminal render does not state that the run stopped early (R1.4, R2.3):\n%s", stdout)
	}

	body, err := os.ReadFile(exportPath)
	if err != nil {
		t.Fatalf("the interrupted run wrote no export at %s: %v — a report is never skipped because it would be partial (R1.4)", exportPath, err)
	}
	var doc map[string]any
	if err := json.Unmarshal(body, &doc); err != nil {
		t.Fatalf("the export is not valid JSON: %v\n%s", err, body)
	}
	if complete, _ := doc["complete"].(bool); complete {
		t.Error(`the interrupted run exported "complete": true (R1.4)`)
	}
	unreached, ok := doc["not_evaluated"].(float64)
	if !ok {
		t.Fatalf(`the export has no "not_evaluated" count: %v`, doc)
	}
	if int(unreached) != 1 {
		t.Errorf(`"not_evaluated" is %v after an interrupt that landed during the first of two subvolumes, want 1`, unreached)
	}

	payload, ok := doc["payload"].(map[string]any)
	if !ok {
		t.Fatalf("the interrupted run exported no payload: %v", doc)
	}
	steps, _ := payload["steps"].([]any)
	if len(steps) == 0 {
		t.Errorf("the interrupted run exported no step at all, although it ran the first subvolume's stages before it was cut short (R1.4, R1.6):\n%s", body)
	}
}
