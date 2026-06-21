package overlay

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/obentoo/bentoolkit/internal/common/tui"
)

// recManifestReporter records tui.Reporter events for parity assertions on the
// migrated manifest reporting (slots = TaskStart per target, ✓/✗ history =
// TaskDone ok, summary = BatchDone, live tail = TaskLine).
type recManifestReporter struct {
	mu sync.Mutex
	ev []string
}

func (r *recManifestReporter) add(s string)                 { r.mu.Lock(); r.ev = append(r.ev, s); r.mu.Unlock() }
func (r *recManifestReporter) BatchStart(n int)             { r.add(fmt.Sprintf("batchstart:%d", n)) }
func (r *recManifestReporter) TaskStart(id, label string)   { r.add("start:" + id) }
func (r *recManifestReporter) TaskStage(id, stage string)   { r.add("stage:" + id + ":" + stage) }
func (r *recManifestReporter) TaskProgress(string, float64) {}
func (r *recManifestReporter) TaskLine(id string, s tui.Stream, text string, eol bool) {
	r.add("line:" + id + ":" + text)
}
func (r *recManifestReporter) TaskDone(id string, ok bool, summary, captured string) {
	r.add(fmt.Sprintf("done:%s:%t", id, ok))
}
func (r *recManifestReporter) Log(string, string) {}
func (r *recManifestReporter) BatchDone(string)   { r.add("batchdone") }
func (r *recManifestReporter) snap() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.ev...)
}

var _ tui.Reporter = (*recManifestReporter)(nil)

func mfHas(s []string, x string) bool {
	for _, e := range s {
		if e == x {
			return true
		}
	}
	return false
}

// R1.3/R3.2/R6.2: the migrated manifest emits the lifecycle that the tui model
// renders as per-target slots, ✓/✗ history, a live tail, and a summary line.
func TestManifestEmitsParityEvents(t *testing.T) {
	overlay := t.TempDir()
	for _, p := range [][2]string{{"c", "a"}, {"c", "b"}} {
		if err := os.MkdirAll(filepath.Join(overlay, p[0], p[1]), 0o755); err != nil {
			t.Fatal(err)
		}
	}

	oldLook := lookPath
	t.Cleanup(func() { lookPath = oldLook })
	lookPath = func(string) (string, error) { return "/usr/bin/pkgdev", nil }

	oldExec := execCommand
	t.Cleanup(func() { execCommand = oldExec })
	calls := 0
	execCommand = func(ctx context.Context, name string, arg ...string) *exec.Cmd {
		calls++
		if calls == 2 {
			return exec.CommandContext(ctx, "sh", "-c", "printf 'FAIL-OUT\\n' 1>&2; exit 1")
		}
		return exec.CommandContext(ctx, "sh", "-c", "printf 'ok-line\\n'")
	}

	rec := &recManifestReporter{}
	targets := []ManifestUpdate{{Category: "c", Package: "a"}, {Category: "c", Package: "b"}}
	RegenerateManifests(overlay, targets, &ManifestOptions{Jobs: 1, Reporter: rec})

	ev := rec.snap()
	if !mfHas(ev, "batchstart:2") {
		t.Errorf("expected batchstart:2 in %v", ev)
	}
	if !mfHas(ev, "start:c/a") || !mfHas(ev, "done:c/a:true") {
		t.Errorf("expected slot + ✓ history for c/a in %v", ev)
	}
	if !mfHas(ev, "start:c/b") || !mfHas(ev, "done:c/b:false") {
		t.Errorf("expected slot + ✗ history for c/b in %v", ev)
	}
	if !mfHas(ev, "line:c/a:ok-line") {
		t.Errorf("expected a live tail line for c/a in %v", ev)
	}
	if !mfHas(ev, "batchdone") {
		t.Errorf("expected a summary (batchdone) in %v", ev)
	}
}

// R2.2: a non-TTY plain reporter emits deterministic lines with NO ANSI.
func TestManifestPlainNoANSI(t *testing.T) {
	overlay := t.TempDir()
	if err := os.MkdirAll(filepath.Join(overlay, "c", "a"), 0o755); err != nil {
		t.Fatal(err)
	}

	oldLook := lookPath
	t.Cleanup(func() { lookPath = oldLook })
	lookPath = func(string) (string, error) { return "/usr/bin/pkgdev", nil }

	oldExec := execCommand
	t.Cleanup(func() { execCommand = oldExec })
	execCommand = func(ctx context.Context, name string, arg ...string) *exec.Cmd {
		return exec.CommandContext(ctx, "sh", "-c", "printf 'fetched\\n'")
	}

	var buf bytes.Buffer
	rep := tui.NewPlainReporter(&buf, 0)
	targets := []ManifestUpdate{{Category: "c", Package: "a"}}
	RegenerateManifests(overlay, targets, &ManifestOptions{Jobs: 1, Reporter: rep})

	out := buf.String()
	if strings.ContainsRune(out, 0x1b) {
		t.Errorf("plain manifest output must contain no ANSI ESC:\n%q", out)
	}
	if !strings.Contains(out, "c/a") {
		t.Errorf("plain output should name the package:\n%s", out)
	}
}

// When pkgdev is absent the run short-circuits and the reporter is never touched
// (no events) — every target is marked failed.
func TestManifestReporterNotInvokedWhenPkgdevMissing(t *testing.T) {
	oldLook := lookPath
	t.Cleanup(func() { lookPath = oldLook })
	lookPath = func(string) (string, error) { return "", exec.ErrNotFound }

	rec := &recManifestReporter{}
	targets := []ManifestUpdate{{Category: "c", Package: "a"}, {Category: "c", Package: "b"}}
	updates := RegenerateManifests(t.TempDir(), targets, &ManifestOptions{Reporter: rec, Jobs: 2, Keep: true})

	if len(updates) != 2 {
		t.Fatalf("got %d updates, want 2", len(updates))
	}
	for _, u := range updates {
		if u.Success {
			t.Errorf("%s/%s: expected failure when pkgdev missing", u.Category, u.Package)
		}
	}
	if got := rec.snap(); len(got) != 0 {
		t.Errorf("reporter must not be invoked when pkgdev is missing, got %v", got)
	}
}

// RegenerateManifests must return updates in input order even under concurrency.
func TestWorkerPool_PreservesInputOrder(t *testing.T) {
	targets := make([]ManifestUpdate, 50)
	for i := range targets {
		targets[i] = ManifestUpdate{Category: "cat", Package: fmt.Sprintf("pkg-%d", i)}
	}
	got := RegenerateManifests("/nonexistent", targets, &ManifestOptions{DryRun: true, Jobs: 8})
	if len(got) != len(targets) {
		t.Fatalf("got %d updates, want %d", len(got), len(targets))
	}
	for i, u := range got {
		if u.Package != targets[i].Package {
			t.Errorf("index %d: got %q, want %q (order broken)", i, u.Package, targets[i].Package)
		}
	}
}
