package overlay

import (
	"bytes"
	"os"
	"os/exec"
	"strings"
	"sync"
	"testing"
)

// captureReporter records every event for assertion in tests. It is the
// reporter passed to RegenerateManifests when we want to verify lifecycle
// ordering and concurrency without running real pkgdev or printing UI.
type captureReporter struct {
	mu        sync.Mutex
	totalN    int
	totalJobs int
	starts    []captureEvent
	dones     []captureEvent
	finished  bool
}

type captureEvent struct {
	idx    int
	worker int
	target ManifestUpdate
	ok     bool
	errMsg string
}

func (r *captureReporter) Total(n, jobs int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.totalN = n
	r.totalJobs = jobs
}

func (r *captureReporter) Start(i, worker int, t ManifestUpdate) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.starts = append(r.starts, captureEvent{idx: i, worker: worker, target: t})
}

func (r *captureReporter) Done(i, worker int, t ManifestUpdate, ok bool, errMsg, _ string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.dones = append(r.dones, captureEvent{idx: i, worker: worker, target: t, ok: ok, errMsg: errMsg})
}

func (r *captureReporter) Finish() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.finished = true
}

func TestRegenerateManifests_ReporterNotInvokedWhenPkgdevMissing(t *testing.T) {
	if _, err := exec.LookPath("pkgdev"); err == nil {
		t.Skip("pkgdev installed; this test asserts the not-found path")
	}

	overlayPath := setupRenameTestOverlay(t)
	defer os.RemoveAll(overlayPath)

	createRenameTestEbuild(t, overlayPath, "app-misc", "alpha", "1.0.0")
	createRenameTestEbuild(t, overlayPath, "app-misc", "beta", "1.0.0")
	createRenameTestEbuild(t, overlayPath, "app-misc", "gamma", "1.0.0")

	// pkgdev missing causes RegenerateManifests to short-circuit before
	// the worker pool spins up — every target is marked failed but the
	// reporter is never touched (no Total/Start/Done/Finish fired).
	rep := &captureReporter{}
	targets, err := ResolveManifestTargets(overlayPath, ManifestScope{})
	if err != nil {
		t.Fatalf("ResolveManifestTargets: %v", err)
	}
	updates := RegenerateManifests(overlayPath, targets, &ManifestOptions{
		Reporter: rep,
		Jobs:     2,
		Keep:     true,
	})
	if len(updates) != 3 {
		t.Fatalf("got %d updates, want 3", len(updates))
	}
	for _, u := range updates {
		if u.Success {
			t.Errorf("%s/%s: expected failure when pkgdev missing", u.Category, u.Package)
		}
	}
	if rep.totalN != 0 || len(rep.starts) != 0 || len(rep.dones) != 0 || rep.finished {
		t.Errorf("reporter should not be invoked when pkgdev is missing, got %+v", rep)
	}
}

func TestWorkerPool_PreservesInputOrder(t *testing.T) {
	// Even when workers complete out of order, RegenerateManifests must
	// return updates in the same order as the input. We exercise this with
	// a dry-run (no pkgdev needed) and a large enough N to make ordering
	// non-trivial under concurrency.
	targets := make([]ManifestUpdate, 50)
	for i := range targets {
		targets[i] = ManifestUpdate{
			Category: "cat",
			Package:  "pkg-" + sprintInt(i),
		}
	}

	got := RegenerateManifests("/nonexistent", targets, &ManifestOptions{
		DryRun: true,
		Jobs:   8,
	})
	if len(got) != len(targets) {
		t.Fatalf("got %d updates, want %d", len(got), len(targets))
	}
	for i, u := range got {
		if u.Package != targets[i].Package {
			t.Errorf("index %d: got %q, want %q (order broken)", i, u.Package, targets[i].Package)
		}
	}
}

func TestNoopReporter_AllMethodsSafe(t *testing.T) {
	var r ProgressReporter = NoopReporter{}
	r.Total(0, 0)
	r.Start(0, 0, ManifestUpdate{})
	r.Done(0, 0, ManifestUpdate{}, true, "", "")
	r.Finish()
}

func TestLogReporter_LinesContainPackageNames(t *testing.T) {
	var buf bytes.Buffer
	r := NewLogReporter(&buf)

	r.Total(2, 4)
	r.Start(0, 0, ManifestUpdate{Category: "app-misc", Package: "alpha"})
	r.Done(0, 0, ManifestUpdate{Category: "app-misc", Package: "alpha"}, true, "", "")
	r.Start(1, 1, ManifestUpdate{Category: "dev-libs", Package: "beta"})
	r.Done(1, 1, ManifestUpdate{Category: "dev-libs", Package: "beta"}, false, "boom", "stderr line")
	r.Finish()

	out := buf.String()
	for _, want := range []string{
		"Regenerating 2 manifest(s)",
		"START  app-misc/alpha",
		"OK     app-misc/alpha",
		"START  dev-libs/beta",
		"FAIL   dev-libs/beta: boom",
		"stderr line",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("LogReporter output missing %q\n--- got ---\n%s", want, out)
		}
	}
}

func TestTUIReporter_DrawsAndClearsCleanly(t *testing.T) {
	// We don't try to validate the TUI visually — we just ensure the
	// reporter writes *something* on each lifecycle stage and the cursor
	// hide/show escapes appear in the output.
	var buf bytes.Buffer
	r := NewTUIReporter(&buf)

	r.Total(2, 2)
	r.Start(0, 0, ManifestUpdate{Category: "a", Package: "x"})
	r.Done(0, 0, ManifestUpdate{Category: "a", Package: "x"}, true, "", "")
	r.Start(1, 1, ManifestUpdate{Category: "b", Package: "y"})
	r.Done(1, 1, ManifestUpdate{Category: "b", Package: "y"}, false, "boom", "")
	r.Finish()

	out := buf.String()
	if !strings.Contains(out, "\x1b[?25l") {
		t.Errorf("TUIReporter did not emit cursor-hide escape")
	}
	if !strings.Contains(out, "\x1b[?25h") {
		t.Errorf("TUIReporter did not emit cursor-show escape on Finish")
	}
	if !strings.Contains(out, "Regenerating manifests") {
		t.Errorf("TUIReporter did not render the bar header")
	}
	if !strings.Contains(out, "succeeded") || !strings.Contains(out, "failed") {
		t.Errorf("TUIReporter did not render summary on Finish: %q", out)
	}
}

func TestRenderBar_ClampsAndScales(t *testing.T) {
	tests := []struct {
		pct      float64
		width    int
		filledOK func(string) bool
	}{
		{0, 10, func(s string) bool { return strings.Count(s, "█") == 0 && strings.Count(s, "░") == 10 }},
		{1, 10, func(s string) bool { return strings.Count(s, "█") == 10 && strings.Count(s, "░") == 0 }},
		{0.5, 10, func(s string) bool { return strings.Count(s, "█") == 5 && strings.Count(s, "░") == 5 }},
		{2, 8, func(s string) bool { return strings.Count(s, "█") == 8 }},  // clamps
		{-1, 8, func(s string) bool { return strings.Count(s, "█") == 0 }}, // clamps
	}
	for _, tt := range tests {
		got := renderBar(tt.pct, tt.width)
		if !tt.filledOK(got) {
			t.Errorf("renderBar(%v, %d) = %q — failed shape check", tt.pct, tt.width, got)
		}
	}
}

// --- small helpers used only by these tests ---

func sprintInt(i int) string {
	// avoid pulling fmt just for tests
	if i == 0 {
		return "0"
	}
	const digits = "0123456789"
	var b []byte
	for i > 0 {
		b = append([]byte{digits[i%10]}, b...)
		i /= 10
	}
	return string(b)
}
