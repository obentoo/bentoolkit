package tui

import (
	"sync"
	"testing"
)

// The test double satisfies the unexported sender sink the teaReporter forwards to.
var _ sender = (*recordingSender)(nil)

func TestTeaReporterIsReporter(t *testing.T) {
	var _ Reporter = newTeaReporter(&recordingSender{})
}

// R3.1/R7.4: the teaReporter forwards every Reporter call to the program as the
// matching tea.Msg, preserving fields.
func TestTeaReporterForwardsAllEvents(t *testing.T) {
	s := &recordingSender{}
	r := newTeaReporter(s)

	r.BatchStart(2)
	r.TaskStart("p1", "lbl")
	r.TaskStage("p1", "manifest")
	r.TaskProgress("p1", 0.5)
	r.TaskLine("p1", StreamStderr, "x", false)
	r.TaskDone("p1", true, "ok", "cap")
	r.Log("info", "hi")
	r.BatchDone("done")

	msgs := s.snapshot()
	if len(msgs) != 8 {
		t.Fatalf("got %d forwarded msgs, want 8: %#v", len(msgs), msgs)
	}
	if m, ok := msgs[0].(BatchStartMsg); !ok || m.Total != 2 {
		t.Errorf("msg[0] = %#v, want BatchStartMsg{Total:2}", msgs[0])
	}
	if m, ok := msgs[1].(TaskStartMsg); !ok || m.ID != "p1" || m.Label != "lbl" {
		t.Errorf("msg[1] = %#v, want TaskStartMsg{p1,lbl}", msgs[1])
	}
	if m, ok := msgs[2].(TaskStageMsg); !ok || m.Stage != "manifest" {
		t.Errorf("msg[2] = %#v, want TaskStageMsg{manifest}", msgs[2])
	}
	if m, ok := msgs[3].(TaskProgressMsg); !ok || m.Frac != 0.5 {
		t.Errorf("msg[3] = %#v, want TaskProgressMsg{0.5}", msgs[3])
	}
	if m, ok := msgs[4].(TaskLineMsg); !ok || m.Text != "x" || m.Stream != StreamStderr || m.EOL {
		t.Errorf("msg[4] = %#v, want TaskLineMsg{x,stderr,eol=false}", msgs[4])
	}
	if m, ok := msgs[5].(TaskDoneMsg); !ok || !m.OK || m.CapturedOutput != "cap" || m.Summary != "ok" {
		t.Errorf("msg[5] = %#v, want TaskDoneMsg{ok,cap}", msgs[5])
	}
	if m, ok := msgs[6].(LogMsg); !ok || m.Level != "info" || m.Text != "hi" {
		t.Errorf("msg[6] = %#v, want LogMsg{info,hi}", msgs[6])
	}
	if m, ok := msgs[7].(BatchDoneMsg); !ok || m.Summary != "done" {
		t.Errorf("msg[7] = %#v, want BatchDoneMsg{done}", msgs[7])
	}
}

// R7.4: the teaReporter is safe to drive from many goroutines (the parallel
// manifest workers); every event is delivered and the run is -race clean.
func TestTeaReporterConcurrentRaceClean(t *testing.T) {
	s := &recordingSender{}
	r := newTeaReporter(s)

	const goroutines, perG = 8, 100
	var wg sync.WaitGroup
	for g := 0; g < goroutines; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < perG; i++ {
				r.TaskLine("p", StreamStdout, "line", true)
			}
		}()
	}
	wg.Wait()

	if got := len(s.snapshot()); got != goroutines*perG {
		t.Fatalf("got %d delivered events, want %d", got, goroutines*perG)
	}
}
