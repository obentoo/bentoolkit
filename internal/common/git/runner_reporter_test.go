package git

import (
	"fmt"
	"os/exec"
	"strings"
	"sync"
	"testing"

	"github.com/obentoo/bentoolkit/internal/common/tui"
)

// recReporter records reporter events for assertions.
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

func hasLinePrefix(s []string, prefix string) bool {
	for _, e := range s {
		if strings.HasPrefix(e, prefix) {
			return true
		}
	}
	return false
}

// R6.2: a fast mutating op (commit) emits only stage/done events — no tail.
func TestGitRunnerCommitEmitsStageDone(t *testing.T) {
	rec := &recReporter{}
	seam := func(name string, arg ...string) *exec.Cmd { return exec.Command("true") }
	g := NewGitRunner(t.TempDir(), WithGitReporter(rec, "repo1"), WithGitExecCommand(seam))

	if err := g.Commit("msg", "user", "user@example.com"); err != nil {
		t.Fatalf("commit: %v", err)
	}
	ev := rec.snap()
	if !has(ev, "stage:repo1:commit") {
		t.Errorf("expected stage:repo1:commit in %v", ev)
	}
	if !has(ev, "done:repo1:true") {
		t.Errorf("expected done:repo1:true in %v", ev)
	}
	if hasLinePrefix(ev, "line:") {
		t.Errorf("a commit must not stream tail lines: %v", ev)
	}
}

// R6.2/R1.1: a streaming download (fetch) emits a live tail plus stage/done.
func TestGitRunnerFetchStreamsTail(t *testing.T) {
	rec := &recReporter{}
	seam := func(name string, arg ...string) *exec.Cmd {
		return exec.Command("sh", "-c", "printf 'recv-A\\nrecv-B\\n'")
	}
	g := NewGitRunner(t.TempDir(), WithGitReporter(rec, "repo1"), WithGitExecCommand(seam))

	if err := g.Fetch("origin"); err != nil {
		t.Fatalf("fetch: %v", err)
	}
	ev := rec.snap()
	if !has(ev, "stage:repo1:fetch") || !has(ev, "done:repo1:true") {
		t.Errorf("expected fetch stage+done in %v", ev)
	}
	if !has(ev, "line:repo1:recv-A") || !has(ev, "line:repo1:recv-B") {
		t.Errorf("expected fetch tail lines in %v", ev)
	}
}

// R7.1: a failing op preserves the captured output in the returned error and
// reports done:false.
func TestGitRunnerErrorPreservesOutput(t *testing.T) {
	rec := &recReporter{}
	seam := func(name string, arg ...string) *exec.Cmd {
		return exec.Command("sh", "-c", "printf 'FETCH-ERR-OUT-3D9\\n' 1>&2; exit 1")
	}
	g := NewGitRunner(t.TempDir(), WithGitReporter(rec, "repo1"), WithGitExecCommand(seam))

	err := g.Fetch("origin")
	if err == nil {
		t.Fatal("expected a fetch error")
	}
	if !strings.Contains(err.Error(), "FETCH-ERR-OUT-3D9") {
		t.Errorf("fetch error must preserve captured output (R7.1): %v", err)
	}
	if !has(rec.snap(), "done:repo1:false") {
		t.Errorf("expected done:repo1:false on error: %v", rec.snap())
	}
}

// Backward compatibility: NewGitRunner with no options still works (Noop reporter,
// default exec) — existing callers are unaffected (R3.3).
func TestGitRunnerNoReporterUnchanged(t *testing.T) {
	g := NewGitRunner(t.TempDir())
	if g == nil {
		t.Fatal("NewGitRunner returned nil")
	}
	if g.WorkDir() == "" {
		t.Error("expected a work dir")
	}
}
