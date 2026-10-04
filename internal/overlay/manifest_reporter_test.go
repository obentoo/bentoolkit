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
	RegenerateManifests(t.Context(), overlay, targets, &ManifestOptions{Jobs: 1, Reporter: rec})

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
	RegenerateManifests(t.Context(), overlay, targets, &ManifestOptions{Jobs: 1, Reporter: rep})

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
	updates := RegenerateManifests(t.Context(), t.TempDir(), targets, &ManifestOptions{Reporter: rec, Jobs: 2, Keep: true}).Updates

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
	got := RegenerateManifests(t.Context(), "/nonexistent", targets, &ManifestOptions{DryRun: true, Jobs: 8}).Updates
	if len(got) != len(targets) {
		t.Fatalf("got %d updates, want %d", len(got), len(targets))
	}
	for i, u := range got {
		if u.Package != targets[i].Package {
			t.Errorf("index %d: got %q, want %q (order broken)", i, u.Package, targets[i].Package)
		}
	}
}

// summarySpy records what BatchDone was handed, which recManifestReporter
// deliberately discards: its two consumers assert the EVENT and never its
// content, which is exactly what made moving the wording out of this package
// safe. Pinning the wording needs the half that was thrown away, so it gets its
// own recorder rather than a change to that one.
type summarySpy struct {
	recManifestReporter
	mu      sync.Mutex
	closed  bool
	summary string
}

func (s *summarySpy) BatchDone(summary string) {
	s.mu.Lock()
	s.closed, s.summary = true, summary
	s.mu.Unlock()
	s.recManifestReporter.BatchDone(summary)
}

func (s *summarySpy) close() (bool, string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.closed, s.summary
}

var _ tui.Reporter = (*summarySpy)(nil)

// TestManifestSummaryIsRelayedNotComposed pins the seam sub-task 17.3 opened:
// the run closes its batch on the sentence the CALLER composed, verbatim, and
// on nothing at all when the caller composed none (S046-R5.2, design.md D5).
//
// It is the half the guard in manifest_summary_test.go cannot reach. That one
// reads the source and answers "did this package choose any wording", which a
// run that silently ignored opts.Summary would also pass — the format string
// would be gone from the package either way, and the operator would simply lose
// the last line of their live region with every test still green.
func TestManifestSummaryIsRelayedNotComposed(t *testing.T) {
	t.Run("the caller's sentence arrives verbatim", func(t *testing.T) {
		overlay, targets := manifestTargets(t, "c/a", "c/b")
		stubPkgdev(t, true, false)

		// Deliberately NOT the sentence the CLI composes. A composer whose
		// output happened to match "1 ok, 1 failed" would pass this test against
		// a package that had gone on writing that sentence itself.
		spy := &summarySpy{}
		RegenerateManifests(t.Context(), overlay, targets, &ManifestOptions{
			Jobs: 1, Keep: true, Reporter: spy,
			Summary: func(result ManifestResult) string {
				return fmt.Sprintf("the caller's words over %d/%d", result.Ok(), result.Failed())
			},
		})

		closed, summary := spy.close()
		if !closed {
			t.Fatal("the batch never closed — a bracket opened by the run and left open is what moving the BatchDone call out of it would have cost (Unchanged Behavior 3)")
		}
		if want := "the caller's words over 1/1"; summary != want {
			t.Errorf("BatchDone received %q, want %q — the run must relay the caller's sentence, not reword it or replace it (S046-R5.2)", summary, want)
		}
	})

	t.Run("no composer closes the batch with no wording", func(t *testing.T) {
		overlay, targets := manifestTargets(t, "c/a")
		stubPkgdev(t, true)

		spy := &summarySpy{}
		RegenerateManifests(t.Context(), overlay, targets, &ManifestOptions{Jobs: 1, Keep: true, Reporter: spy})

		closed, summary := spy.close()
		if !closed {
			t.Fatal("a run whose caller supplied no composer still has to close its batch; silence is the SUMMARY, never the event")
		}
		if summary != "" {
			t.Errorf("BatchDone received %q, want the empty close — a caller that chose no wording must not be given one this package invented (S046-R5.2)", summary)
		}
	})
}
