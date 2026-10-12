package autoupdate

// Story 092, sub-task 4.1 (test-after): with WithApplierQuietGateOutput — the
// plain backend's setting — a build-gate child that brings its own stdin is
// still kept off the terminal and captured whole, but none of its lines reaches
// the reporter, so a non-terminal run stays as terse as before.

import (
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestBuildGateQuietOutputStaysOutOfTheTaskTail(t *testing.T) {
	tmp := t.TempDir()
	spy := &s092AttachSpy{}
	rec := &recordingReporter{}
	a, err := NewApplier(filepath.Join(tmp, "overlay"), filepath.Join(tmp, "config"),
		WithApplierRunAttached(spy.run),
		WithApplierReporter(rec),
		WithApplierDistdir(filepath.Join(tmp, "distdir"), ""),
		WithApplierQuietGateOutput(),
	)
	if err != nil {
		t.Fatalf("NewApplier: %v", err)
	}
	run := a.recordingRunner(t.Context(), &buildAttempt{pkg: "dev-go/quiet"})
	cmd := exec.Command("/bin/sh", "-c", "echo QUIET-OUT-092; echo QUIET-ERR-092 >&2; exit 3")
	cmd.Stdin = strings.NewReader("")
	out, err := run(cmd)
	if err == nil {
		t.Errorf("the child's non-zero exit was lost")
	}
	if spy.calls != 0 {
		t.Errorf("a non-nil-stdin gate child went through the attached runner (%d call(s))", spy.calls)
	}
	for _, want := range []string{"QUIET-OUT-092", "QUIET-ERR-092"} {
		if !strings.Contains(string(out), want) {
			t.Errorf("the returned output misses %q: %q", want, out)
		}
	}
	for _, e := range rec.snapshot() {
		if strings.HasPrefix(e, "TaskLine:") {
			t.Errorf("a gate line reached the reporter under the quiet option: %q", e)
		}
	}
}
