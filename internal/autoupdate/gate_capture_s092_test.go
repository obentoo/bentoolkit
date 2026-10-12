package autoupdate

// Authored for story 092 (autoupdate-apply-observable), sub-task 4.1: a build
// gate child started with a non-nil stdin is captured into the task tail and
// never handed the terminal (R5.1, R5.2); a nil-stdin child still goes through
// the attached runner, as the sudo/doas prompt needs (R5.3).
//
// Driven through the build gates' runner (recordingRunner) with a spy for the
// attached runner, so no terminal and no real `ebuild` are involved.

import (
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

type s092AttachSpy struct {
	mu    sync.Mutex
	calls int
}

func (s *s092AttachSpy) run(cmd *exec.Cmd) ([]byte, error) {
	s.mu.Lock()
	s.calls++
	s.mu.Unlock()
	return cmd.CombinedOutput()
}

func s092GateApplier(t *testing.T) (*Applier, *s092AttachSpy, *recordingReporter) {
	t.Helper()
	tmp := t.TempDir()
	spy := &s092AttachSpy{}
	rec := &recordingReporter{}
	a, err := NewApplier(filepath.Join(tmp, "overlay"), filepath.Join(tmp, "config"),
		WithApplierRunAttached(spy.run),
		WithApplierReporter(rec),
		WithApplierDistdir(filepath.Join(tmp, "distdir"), ""),
	)
	if err != nil {
		t.Fatalf("NewApplier: %v", err)
	}
	return a, spy, rec
}

func TestBuildGateNonNilStdinIsCaptured(t *testing.T) {
	a, spy, rec := s092GateApplier(t)
	run := a.recordingRunner(t.Context(), &buildAttempt{})
	cmd := exec.Command("/bin/sh", "-c", "echo GATE-OUT-092; echo GATE-ERR-092 >&2; exit 3")
	cmd.Stdin = strings.NewReader("") // the gates' ebuild: its own process group, empty stdin
	out, err := run(cmd)
	if err == nil {
		t.Errorf("the child's non-zero exit was lost")
	}
	if spy.calls != 0 {
		t.Errorf("a non-nil-stdin gate child went through the attached runner (%d call(s)): it must stay under the live region", spy.calls)
	}
	for _, want := range []string{"GATE-OUT-092", "GATE-ERR-092"} {
		if !strings.Contains(string(out), want) {
			t.Errorf("the returned output misses %q (the Output contract): %q", want, out)
		}
		seen := false
		for _, e := range rec.snapshot() {
			seen = seen || (strings.HasPrefix(e, "TaskLine:") && strings.Contains(e, want))
		}
		if !seen {
			t.Errorf("%q never reached the task tail as a TaskLine", want)
		}
	}
}

// R5.3: the converse — a nil stdin is the privileged prompt's case and keeps
// the attached runner.
func TestBuildGateNilStdinStillAttached(t *testing.T) {
	a, spy, _ := s092GateApplier(t)
	run := a.recordingRunner(t.Context(), &buildAttempt{})
	cmd := exec.Command("/bin/sh", "-c", "echo PROMPT-092")
	if _, err := run(cmd); err != nil {
		t.Fatalf("run: %v", err)
	}
	if spy.calls != 1 {
		t.Errorf("a nil-stdin child bypassed the attached runner (%d call(s)), want 1", spy.calls)
	}
}
