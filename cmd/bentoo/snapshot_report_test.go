package main

// Authored for story 046, sub-task 6.2 — R1.2, R1.6, R4.4.
//
// Written from the contract: R1.2 — "WHEN `snapshot run` finishes, THE SYSTEM
// SHALL render a report of that run in the mode the run resolved" — R1.6, which
// says what that report must contain ("which subvolume it operated on and the
// outcome of each step it ran"), and R4.4's kind discriminator.
//
// design.md D6 says why this command and not an easier one: `snapshot run` is
// the DISTANCE test. It shares no vocabulary with a package run, so if the
// envelope only fits package-shaped runs, this is where that is discovered —
// and discovering it here is the whole reason snapshot is not deferred to 047.
//
// Red on arrival: the harness does not exist and `snapshot run` ends in
// output.PrintSuccess("snapshot run completed (%d stages)") — a sentence, not a
// report.
//
// # The fixture is this package's own
//
// writeSnapshotConfig, redirectStateDir, stubBinariesOnPath and
// validSnapshotTOML come from snapshot_test.go; deps.snapshotRunner is the
// subprocess seam snapshot_run_test.go already injects a MockRunner into, so no
// btrbk runs and no subvolume is touched.
//
// The config is passed as --config rather than left to the package variable
// writeSnapshotConfig sets: the harness builds a fresh command tree per run and
// pflag reassigns every flag-bound variable to its default as it registers, so
// a value written into one before the run would be overwritten by the run
// itself. Passing it as a flag is what survives that, and it is also what an
// operator would type.

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/obentoo/bentoolkit/internal/common/report"
	"github.com/obentoo/bentoolkit/internal/snapshot"
)

// snapshotRunExport runs `snapshot run` over the mocked pipeline and returns
// what the terminal saw plus the export path.
func snapshotRunExport(t *testing.T, runner snapshot.Runner) (stdout, exportPath string, code int) {
	t.Helper()

	c := newTestCLI(t)
	stubBinariesOnPath(t, "btrbk", "ssh")
	_, configPath := writeSnapshotConfig(t, validSnapshotTOML)
	redirectStateDir(t)
	c.deps.snapshotRunner = runner

	exportPath = filepath.Join(t.TempDir(), "snapshot.json")

	stdout, stderr, code := c.Run("snapshot", "--config="+configPath, "run", "--ui=plain", "--export="+exportPath)
	if strings.Contains(stderr, "unknown flag") {
		t.Fatalf("a flag was rejected: %s", stderr)
	}
	return stdout, exportPath, code
}

// snapshotDocument reads an export back as a JSON object.
func snapshotDocument(t *testing.T, path string) map[string]any {
	t.Helper()

	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("the run wrote no export at %s: %v", path, err)
	}

	var doc map[string]any
	if err := json.Unmarshal(body, &doc); err != nil {
		t.Fatalf("the export is not valid JSON: %v\n%s", err, body)
	}
	return doc
}

func snapshotDocumentKeys(doc map[string]any) []string {
	keys := make([]string, 0, len(doc))
	for key := range doc {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

// TestSnapshotReportNamesTheSubvolumeAndItsSteps pins R1.6. A snapshot run says
// nothing at all today; what it must say is which subvolume it worked on and
// how each step came out — because "1 stage" answers neither question.
func TestSnapshotReportNamesTheSubvolumeAndItsSteps(t *testing.T) {
	stdout, _, code := snapshotRunExport(t, &snapshot.MockRunner{})

	if code != 0 {
		t.Errorf("`snapshot run` exited %d over a mocked pipeline", code)
	}
	if strings.TrimSpace(stdout) == "" {
		t.Fatal("the run printed nothing at all (R1.2)")
	}
	if !strings.Contains(stdout, "/home") {
		t.Errorf("the report does not name the subvolume it operated on (R1.6):\n%s", stdout)
	}
	if strings.ContainsRune(stdout, 0x1b) {
		t.Errorf("--ui=plain produced escape sequences (R2.2):\n%q", stdout)
	}
}

// TestSnapshotReportStatesAFailingStep is the half a happy-path fixture cannot
// reach. A report that names every step and calls them all fine is the same
// report whatever happened, and a failing step is exactly the run whose report
// the operator needs.
func TestSnapshotReportStatesAFailingStep(t *testing.T) {
	failing := &snapshot.MockRunner{
		RunFunc: func(_ context.Context, _ string, _ []string, _ []byte) ([]byte, error) {
			return nil, os.ErrPermission
		},
	}

	stdout, exportPath, code := snapshotRunExport(t, failing)

	if code == 0 {
		t.Error("`snapshot run` exited 0 although every step failed — the exit status still reports the run (Unchanged Behavior)")
	}
	if strings.TrimSpace(stdout) == "" {
		t.Fatal("a failed run printed no report — the run that most needs one produced none (R1.2, R1.6)")
	}

	doc := snapshotDocument(t, exportPath)
	payload, ok := doc["payload"].(map[string]any)
	if !ok {
		t.Fatalf("the export has no payload object: %v", snapshotDocumentKeys(doc))
	}
	if _, present := payload["subvolume"]; !present {
		t.Errorf("the exported payload does not name the subvolume (R1.6) — keys: %v", snapshotDocumentKeys(payload))
	}

	// The outcome must be legible from the document, not only from the exit
	// status: a script reading the export is the reader R4.2 exists for.
	body, _ := json.Marshal(doc)
	if !strings.Contains(strings.ToLower(string(body)), "fail") {
		t.Errorf("nothing in the exported document says a step failed (R1.6):\n%s", body)
	}
}

// TestSnapshotExportIsToldApartByItsKindAlone is R4.4 over the second new
// producer, and it carries both halves for the same reason 5.2's does: the
// kinds must differ, and nothing ELSE about the two documents may differ, or
// the discriminator is not what is doing the work.
func TestSnapshotExportIsToldApartByItsKindAlone(t *testing.T) {
	_, snapshotPath, _ := snapshotRunExport(t, &snapshot.MockRunner{})
	snapshotDoc := snapshotDocument(t, snapshotPath)

	if got, _ := snapshotDoc["kind"].(string); got != string(report.KindSnapshotRun) {
		t.Errorf(`the export's kind is %q, want %q`, got, report.KindSnapshotRun)
	}
	if got, ok := snapshotDoc["schema"].(float64); !ok || int(got) != 2 {
		t.Errorf(`the export's schema is %v, want 2`, snapshotDoc["schema"])
	}

	manifestCLI := newTestCLI(t)
	writeExitTestEbuild(t, manifestCLI.Overlay(), "app-misc/jq", "1.0.0")
	manifestPath := filepath.Join(t.TempDir(), "manifest.json")
	if _, stderr, _ := manifestCLI.Run("overlay", "manifest", "--dry-run", "--ui=plain", "--export="+manifestPath); strings.Contains(stderr, "unknown flag") {
		t.Fatalf("a flag was rejected on the manifest path: %s", stderr)
	}
	manifestDoc := snapshotDocument(t, manifestPath)

	snapshotKind, _ := snapshotDoc["kind"].(string)
	manifestKind, _ := manifestDoc["kind"].(string)

	if snapshotKind == manifestKind {
		t.Fatalf("a subvolume run and a package run both exported kind %q — the field that tells two documents apart says the same about both (R4.4)", snapshotKind)
	}
	if got, want := strings.Join(snapshotDocumentKeys(snapshotDoc), ","), strings.Join(snapshotDocumentKeys(manifestDoc), ","); got != want {
		t.Errorf("the two documents do not share a shape — a consumer could tell them apart without reading the kind, so the discriminator is untested (R4.4)\n  snapshot: %s\n  manifest: %s", got, want)
	}
}
