package provider

import (
	"context"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
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

// R6.2/R1.1: a git clone streams a live tail plus stage/done.
func TestGitCloneStreamsTail(t *testing.T) {
	old := execCommand
	t.Cleanup(func() { execCommand = old })
	execCommand = func(ctx context.Context, name string, arg ...string) *exec.Cmd {
		return exec.CommandContext(ctx, "sh", "-c", "printf 'Cloning\\nReceiving-A\\nReceiving-B\\n'")
	}

	rec := &recReporter{}
	p := &GitCloneProvider{
		RepoURL:   "https://example.com/y.git",
		LocalPath: filepath.Join(t.TempDir(), "repo"),
		Branch:    "main",
		RepoName:  "y",
	}
	p.SetReporter(rec, "y")

	if err := p.cloneRepo(); err != nil {
		t.Fatalf("cloneRepo: %v", err)
	}
	ev := rec.snap()
	if !has(ev, "stage:y:clone") || !has(ev, "done:y:true") {
		t.Errorf("expected clone stage+done in %v", ev)
	}
	if !has(ev, "line:y:Cloning") {
		t.Errorf("expected clone tail line in %v", ev)
	}
}

// R7.1: a failing clone preserves the captured output in the error.
func TestGitCloneErrorPreservesOutput(t *testing.T) {
	old := execCommand
	t.Cleanup(func() { execCommand = old })
	execCommand = func(ctx context.Context, name string, arg ...string) *exec.Cmd {
		return exec.CommandContext(ctx, "sh", "-c", "printf 'CLONE-ERR-8A1\\n' 1>&2; exit 1")
	}

	rec := &recReporter{}
	p := &GitCloneProvider{
		RepoURL:   "https://example.com/y.git",
		LocalPath: filepath.Join(t.TempDir(), "repo"),
		Branch:    "main",
		RepoName:  "y",
	}
	p.SetReporter(rec, "y")

	err := p.cloneRepo()
	if err == nil {
		t.Fatal("expected a clone error")
	}
	if !strings.Contains(err.Error(), "CLONE-ERR-8A1") {
		t.Errorf("clone error must preserve captured output (R7.1): %v", err)
	}
	if !has(rec.snap(), "done:y:false") {
		t.Errorf("expected done:y:false on error: %v", rec.snap())
	}
}
