package tui

import "testing"

// Compile-time assertion: the noop backend satisfies the Reporter interface.
// teaReporter and plainReporter add their own assertions in their own files.
var (
	_ Reporter = noopReporter{}
	_ Reporter = Noop()
)

func TestStreamString(t *testing.T) {
	if got := StreamStdout.String(); got != "stdout" {
		t.Errorf("StreamStdout.String() = %q, want %q", got, "stdout")
	}
	if got := StreamStderr.String(); got != "stderr" {
		t.Errorf("StreamStderr.String() = %q, want %q", got, "stderr")
	}
}

// TestEventMessageFields pins the field set every backend and the model rely on.
func TestEventMessageFields(t *testing.T) {
	_ = BatchStartMsg{Total: 3}
	_ = TaskStartMsg{ID: "p1", Label: "cat/pkg"}
	_ = TaskStageMsg{ID: "p1", Stage: "manifest"}

	prog := TaskProgressMsg{ID: "p1", Frac: 0.5}
	if prog.Frac != 0.5 {
		t.Fatalf("TaskProgressMsg.Frac not preserved")
	}

	line := TaskLineMsg{ID: "p1", Stream: StreamStderr, Text: "10%", EOL: false}
	if line.Stream != StreamStderr || line.Text != "10%" || line.EOL {
		t.Fatalf("TaskLineMsg fields not preserved")
	}

	done := TaskDoneMsg{ID: "p1", OK: true, Summary: "ok", CapturedOutput: "full log"}
	if !done.OK || done.CapturedOutput != "full log" {
		t.Fatalf("TaskDoneMsg fields not preserved")
	}

	_ = LogMsg{Level: "info", Text: "hi"}
	_ = BatchDoneMsg{Summary: "done"}
}

// TestNoopReporterSilent exercises every method so the R3.3 "noop ≡ silence"
// contract is at least executed (no panics, no output side effects).
func TestNoopReporterSilent(t *testing.T) {
	r := Noop()
	r.BatchStart(2)
	r.TaskStart("p1", "cat/pkg")
	r.TaskStage("p1", "manifest")
	r.TaskProgress("p1", 0.3)
	r.TaskLine("p1", StreamStdout, "downloading", false)
	r.TaskLine("p1", StreamStdout, "done", true)
	r.TaskDone("p1", true, "done", "captured")
	r.Log("info", "msg")
	r.BatchDone("all done")
}
