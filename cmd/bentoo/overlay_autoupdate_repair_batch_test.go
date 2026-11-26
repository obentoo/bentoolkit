package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/obentoo/bentoolkit/internal/autoupdate"
	"github.com/obentoo/bentoolkit/internal/autoupdate/repair"
	"github.com/obentoo/bentoolkit/internal/autoupdate/validate"
	"github.com/obentoo/bentoolkit/internal/common/config"
	"github.com/obentoo/bentoolkit/internal/common/report"
)

const batchPkg2 = "dev-go/otherdemo"

// addSecondRepair adds batchPkg2 to f, staged and bundled like repairPkg, and
// swaps the claude stub for one that also counts its runs in f.record+".runs".
// It returns the second bundle's path.
func addSecondRepair(t *testing.T, f *repairFix) string {
	t.Helper()
	pub := filepath.Join(f.overlay, "dev-go", "otherdemo")
	if err := os.MkdirAll(pub, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(pub, "otherdemo-"+repairCurrent+".ebuild"), []byte("EAPI=8\nSLOT=\"0\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	pending, err := autoupdate.NewPendingList(f.configDir)
	if err != nil {
		t.Fatal(err)
	}
	if err := pending.Add(autoupdate.PendingUpdate{Package: batchPkg2, CurrentVersion: repairCurrent, NewVersion: repairCand, Status: autoupdate.StatusFailed}); err != nil {
		t.Fatal(err)
	}
	staged, err := validate.Stage(validate.StageRequest{
		Overlay: f.overlay, StagingRoot: f.staging, Key: batchPkg2, Version: repairCand,
		EbuildBytes: []byte("EAPI=8\nSLOT=\"0\"\n# repaired\n"),
	})
	if err != nil {
		t.Fatalf("staging the second candidate: %v", err)
	}
	b := repair.Bundle{
		Schema: repair.SchemaVersion, Package: batchPkg2, CurrentVersion: repairCurrent, Candidate: repairCand,
		Stage: "re-check", Failure: "SECOND-FAILURE", StagedRoot: staged, FixerSession: "fixer-of-otherdemo",
		EbuildPath: filepath.Join(staged, "dev-go", "otherdemo", "otherdemo-"+repairCand+".ebuild"),
		Created:    time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC),
	}
	if err := repair.WriteBundle(b); err != nil {
		t.Fatal(err)
	}
	writeStub(t, f.toolDir, "claude", fmt.Sprintf("echo run >> %q.runs\npwd > %q.cwd\nprintf '%%s\\0' \"$@\" > %q.argv\nexit 0\n", f.record, f.record, f.record))
	return filepath.Join(staged, repair.BundleFile)
}

// twoHumanRun is a finished check whose two bumps are both HUMAN.
func twoHumanRun() report.Run {
	return report.Run{Payload: report.AutoupdateCheck{Results: []report.ValidationRow{
		{Package: repairPkg, CandidateVersion: repairCand, State: report.Human},
		{Package: batchPkg2, CandidateVersion: repairCand, State: report.Human},
	}}}
}

// sessionRuns is how many times the counting claude stub ran.
func sessionRuns(f *repairFix) int {
	return strings.Count(readFileOrEmpty(f.record+".runs"), "run")
}

// TestOfferRepairs_SeveralHumanBumpsShareOneSession: two HUMAN bumps are asked
// about once and repaired in one session, run in the staging root with both
// trees added; both are then proven, so both bundles go.
func TestOfferRepairs_SeveralHumanBumpsShareOneSession(t *testing.T) {
	f, ar, asked := offerFixture(t, true)
	second := addSecondRepair(t, f)

	ar.offerRepairs(context.Background(), f.overlay, f.configDir, config.AutoupdateConfig{}, twoHumanRun())

	if *asked != 1 {
		t.Fatalf("the session question was asked %d times, want once", *asked)
	}
	if n := sessionRuns(f); n != 1 {
		t.Fatalf("claude ran %d times, want once for both bumps", n)
	}
	if cwd := strings.TrimSpace(readFileOrEmpty(f.record + ".cwd")); cwd != f.staging {
		t.Errorf("the session ran in %q, want the staging root %q", cwd, f.staging)
	}
	argv := readFileOrEmpty(f.record + ".argv")
	for _, want := range []string{f.staged, filepath.Dir(second), "SECOND-FAILURE", "manifest command failed"} {
		if !strings.Contains(argv, want) {
			t.Errorf("the session argv lacks %q", want)
		}
	}
	if strings.Contains(argv, "--resume") {
		t.Error("the batch session resumed a fixer session")
	}
	for _, p := range []string{f.bundlePath, second} {
		if _, err := os.Stat(p); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("the proven repair kept its bundle %s (stat %v)", p, err)
		}
	}
}

// TestOfferRepairs_BatchNoChangesNothing: "n" to the one question starts no
// session and leaves both bundles as they were.
func TestOfferRepairs_BatchNoChangesNothing(t *testing.T) {
	f, ar, asked := offerFixture(t, false)
	second := addSecondRepair(t, f)
	before1, before2 := readFileOrEmpty(f.bundlePath), readFileOrEmpty(second)

	ar.offerRepairs(context.Background(), f.overlay, f.configDir, config.AutoupdateConfig{}, twoHumanRun())

	if *asked != 1 {
		t.Fatalf("asked %d times, want once", *asked)
	}
	if n := sessionRuns(f); n != 0 {
		t.Errorf("a declined batch started %d session(s)", n)
	}
	if readFileOrEmpty(f.bundlePath) != before1 || readFileOrEmpty(second) != before2 {
		t.Error("a declined batch changed a bundle")
	}
}

// TestRepairCommaListRunsOneSession: --repair a,b is the batch flow, exit 0
// when both trees are proven.
func TestRepairCommaListRunsOneSession(t *testing.T) {
	auOpts := testAutoupdateOptions()
	f := repairFixture(t, auOpts, 0)
	second := addSecondRepair(t, f)
	auOpts.repair = repairPkg + "," + batchPkg2

	code, out := runRepair(t, auOpts, ttyDeps(true, true), "y\n")

	if code != 0 {
		t.Fatalf("exit %d, want 0; output:\n%s", code, out)
	}
	if n := sessionRuns(f); n != 1 {
		t.Fatalf("claude ran %d times, want once", n)
	}
	for _, p := range []string{f.bundlePath, second} {
		if _, err := os.Stat(p); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("the proven repair kept its bundle %s (stat %v)", p, err)
		}
	}
}

// TestRepairCommaListFailingTreesAreMarkedAndExitOne: trees the re-validation
// does not prove keep their bundle, marked repaired, and the run is exit 1.
func TestRepairCommaListFailingTreesAreMarkedAndExitOne(t *testing.T) {
	auOpts := testAutoupdateOptions()
	f := repairFixture(t, auOpts, 1)
	second := addSecondRepair(t, f)
	auOpts.repair = repairPkg + "," + batchPkg2

	code, out := runRepair(t, auOpts, ttyDeps(true, true), "y\n")

	if code != 1 {
		t.Fatalf("exit %d, want 1; output:\n%s", code, out)
	}
	for _, p := range []string{f.bundlePath, second} {
		if !strings.Contains(readFileOrEmpty(p), "repaired_at") {
			t.Errorf("the unproven bundle %s lacks the repaired_at mark", p)
		}
	}
	if !strings.Contains(out, "--repair "+repairPkg+","+batchPkg2) {
		t.Errorf("the summary does not name the batch to rerun; output:\n%s", out)
	}
}
