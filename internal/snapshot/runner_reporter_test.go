package snapshot

import (
	"context"
	"fmt"
	"os/exec"
	"sync"
	"testing"

	"github.com/obentoo/bentoolkit/internal/common/tui"
)

type recReporter struct {
	mu sync.Mutex
	ev []string
}

func (r *recReporter) add(s string)                 { r.mu.Lock(); r.ev = append(r.ev, s); r.mu.Unlock() }
func (r *recReporter) BatchStart(int)               {}
func (r *recReporter) TaskStart(id, label string)   { r.add("start:" + id) }
func (r *recReporter) TaskStage(id, stage string)   { r.add("stage:" + id + ":" + stage) }
func (r *recReporter) TaskProgress(string, float64) {}
func (r *recReporter) TaskLine(id string, s tui.Stream, text string, eol bool) {
	r.add("line:" + id + ":" + text)
}
func (r *recReporter) TaskDone(id string, ok bool, summary, captured string) {
	r.add(fmt.Sprintf("done:%s:%t", id, ok))
}
func (r *recReporter) Log(string, string) {}
func (r *recReporter) BatchDone(string)   {}
func (r *recReporter) snap() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.ev...)
}

var _ tui.Reporter = (*recReporter)(nil)

func has(s []string, x string) bool {
	for _, e := range s {
		if e == x {
			return true
		}
	}
	return false
}

// R6.2/R3.3: a reporting runner emits stage/done around a snapshot command.
func TestSnapshotRunnerEmitsStageDone(t *testing.T) {
	old := execCommand
	t.Cleanup(func() { execCommand = old })
	execCommand = func(ctx context.Context, name string, arg ...string) *exec.Cmd {
		return exec.CommandContext(ctx, "true")
	}

	rec := &recReporter{}
	r := NewReportingRunner(rec, "snap1")
	if _, err := r.Run(context.Background(), "snapper", []string{"create"}, nil); err != nil {
		t.Fatalf("Run: %v", err)
	}

	ev := rec.snap()
	if !has(ev, "stage:snap1:snapper") {
		t.Errorf("expected stage:snap1:snapper in %v", ev)
	}
	if !has(ev, "done:snap1:true") {
		t.Errorf("expected done:snap1:true in %v", ev)
	}
}

// On failure the runner reports done:false (and still returns the error).
func TestSnapshotRunnerReportsFailure(t *testing.T) {
	old := execCommand
	t.Cleanup(func() { execCommand = old })
	execCommand = func(ctx context.Context, name string, arg ...string) *exec.Cmd {
		return exec.CommandContext(ctx, "false")
	}

	rec := &recReporter{}
	r := NewReportingRunner(rec, "snap1")
	if _, err := r.Run(context.Background(), "snapper", []string{"create"}, nil); err == nil {
		t.Fatal("expected an error from a failing command")
	}
	if !has(rec.snap(), "done:snap1:false") {
		t.Errorf("expected done:snap1:false in %v", rec.snap())
	}
}

// R3.3: the default runner (Noop reporter) behaves exactly as before — no panic,
// no events required.
func TestSnapshotRunnerNoopDefaultUnchanged(t *testing.T) {
	old := execCommand
	t.Cleanup(func() { execCommand = old })
	execCommand = func(ctx context.Context, name string, arg ...string) *exec.Cmd {
		return exec.CommandContext(ctx, "true")
	}

	r := defaultRunner()
	if _, err := r.Run(context.Background(), "snapper", []string{"list"}, nil); err != nil {
		t.Fatalf("default runner Run: %v", err)
	}
}
