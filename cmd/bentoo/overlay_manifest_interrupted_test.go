package main

// Authored for story 046, sub-task 5.3 — R1.4.
//
// Written from the contract: R1.4 — "IF a run stops before reaching the end of
// its work, THEN THE SYSTEM SHALL render the report established up to that
// point AND STATE HOW MANY UNITS IT NEVER REACHED" — and design.md's Error
// Handling Strategy: "A report is never skipped because it would be partial."
//
// # Why this is a separate file from overlay_manifest_report_test.go
//
// 5.2 and 5.3 name the same file in their Tests fields. One file per sub-task
// keeps a failure readable — 5.2 fails when a finished run does not report,
// 5.3 when an interrupted one does not say what it missed — and the validation
// commands already separate them (`-run TestManifestReport`,
// `-run TestManifestInterrupted`).
//
// # How the run is interrupted
//
// In process, by signalling this test's own PID, which is the shape story 043
// already established in overlay_autoupdate_signal_test.go. The harness runs
// the tree through func runMain, whose process-wide handler (func
// processContext) is installed for the whole run; overlay manifest is annotated
// cancellable, so the SIGTERM is CAUGHT — the test process is not terminated —
// and only the run context runManifest took from func commandContext is
// cancelled.
//
// The pkgdev stub is what makes the interrupt land during real work rather than
// before or after it: it announces itself by creating a marker file and then
// sleeps, so the signal is sent when the run is provably in flight.
//
// Red on arrival: the harness does not exist and `overlay manifest` produces no
// report to be partial.

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"testing"
	"time"
)

// stubSlowPkgdev puts a pkgdev on PATH that reports it started and then blocks,
// and returns the marker path it creates.
func stubSlowPkgdev(t *testing.T) (marker string) {
	t.Helper()

	dir := t.TempDir()
	marker = filepath.Join(dir, "started")

	script := "#!/bin/sh\ntouch " + marker + "\nsleep 30\n"
	if err := os.WriteFile(filepath.Join(dir, "pkgdev"), []byte(script), 0o755); err != nil {
		t.Fatalf("writing the pkgdev stub: %v", err)
	}

	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return marker
}

// TestManifestInterruptedStillReports pins R1.4 end to end: the operator who
// pressed ctrl+c still gets the report of what the run had established, and the
// report says how much of the plan it never reached.
func TestManifestInterruptedStillReports(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("SIGTERM and a /bin/sh stub have no portable semantics on Windows")
	}

	c := newTestCLI(t)
	targets := []string{"app-misc/jq", "dev-lang/go", "app-shells/fish"}
	for _, pkg := range targets {
		writeExitTestEbuild(t, c.Overlay(), pkg, "1.0.0")
	}

	marker := stubSlowPkgdev(t)
	export := filepath.Join(t.TempDir(), "interrupted.json")

	type outcome struct {
		stdout string
		code   int
	}
	done := make(chan outcome, 1)
	go func() {
		stdout, _, code := c.Run("overlay", "manifest", "--ui=plain", "--export="+export)
		done <- outcome{stdout, code}
	}()

	// Wait until pkgdev is genuinely running: an interrupt delivered before the
	// work starts would test the empty case, and one delivered after it
	// finishes would test nothing at all.
	deadline := time.After(15 * time.Second)
	for {
		if _, err := os.Stat(marker); err == nil {
			break
		}
		select {
		case <-deadline:
			t.Fatal("pkgdev never started; the run cannot be interrupted mid-work")
		case result := <-done:
			t.Fatalf("the run finished before it could be interrupted (exit %d):\n%s", result.code, result.stdout)
		case <-time.After(25 * time.Millisecond):
		}
	}

	proc, err := os.FindProcess(os.Getpid())
	if err != nil {
		t.Fatalf("FindProcess(self): %v", err)
	}
	if err := proc.Signal(syscall.SIGTERM); err != nil {
		t.Fatalf("signalling self: %v", err)
	}

	var result outcome
	select {
	case result = <-done:
	case <-time.After(20 * time.Second):
		t.Fatal("the interrupted run never returned — the report is assembled before it is rendered, so an interrupt must not be able to hang it (R1.3, R1.4)")
	}

	if strings.TrimSpace(result.stdout) == "" {
		t.Fatal("an interrupted run printed nothing — the report established up to that point was thrown away (R1.4)")
	}

	body, err := os.ReadFile(export)
	if err != nil {
		t.Fatalf("the interrupted run wrote no export at %s: %v — a report is never skipped because it would be partial", export, err)
	}

	var doc map[string]any
	if err := json.Unmarshal(body, &doc); err != nil {
		t.Fatalf("the export is not valid JSON: %v\n%s", err, body)
	}

	if complete, _ := doc["complete"].(bool); complete {
		t.Error(`the interrupted run reported "complete": true — a report that does not admit it is partial is worse than one that will not fit (R1.4)`)
	}

	unreached, ok := doc["not_evaluated"].(float64)
	if !ok {
		t.Fatalf(`the export has no "not_evaluated" count: %v`, doc)
	}
	if int(unreached) < 1 {
		t.Errorf(`"not_evaluated" is %v after an interrupt that landed while the first target was still running — the gap is stated even when it is zero, and zero here means the count is not being computed (R1.4)`, unreached)
	}
	if int(unreached) > len(targets) {
		t.Errorf(`"not_evaluated" is %v over %d planned targets — more units were missed than existed`, unreached, len(targets))
	}

	// The terminal render must say it too. A machine-readable gap and a
	// terminal that reads as a finished run is the same silence, one surface
	// along (R2.3).
	if !strings.Contains(result.stdout, "nterrupt") && !strings.Contains(strings.ToLower(result.stdout), "not reached") {
		t.Errorf("the terminal render does not state that the run stopped early (R1.4, R2.3):\n%s", result.stdout)
	}
}
