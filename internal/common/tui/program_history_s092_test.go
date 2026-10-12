package tui

// Authored for story 092, sub-task 1.3: the history leaves the frame on
// release and is printed once, whole, after the program exits (R1.5).

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

// A released model renders no history row and no fold line, keeps the header
// and the trailer, and ends with an empty row for bubbletea's exit erase. The
// release also quits the program.
func TestModelReleasedFrameDropsHistory(t *testing.T) {
	msgs := append([]tea.Msg{BatchStartMsg{Total: 30}}, s092History(30)...)
	msgs = append(msgs, LogMsg{Level: "info", Text: "a routed log"})
	var m tea.Model = newModel()
	m, _ = m.Update(tea.WindowSizeMsg{Width: 40, Height: 10})
	for _, msg := range msgs {
		m, _ = m.Update(msg)
	}
	m, cmd := m.Update(releaseMsg{})
	if cmd == nil || cmd() != (tea.QuitMsg{}) {
		t.Fatalf("a release must quit the program")
	}
	got := strings.Split(stripANSI(m.View()), "\n")
	want := []string{"Applying updates  30/30", "info: a routed log", ""}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("released frame:\n--- got ---\n%q\n--- want ---\n%q", got, want)
	}
}

// PrintHistory writes every row once, oldest first, although the live frame
// folded most of them away; a row is neither cut to the width nor split.
func TestProgramPrintHistoryWritesEveryRowUnfolded(t *testing.T) {
	inR, inW := io.Pipe()
	t.Cleanup(func() { _ = inW.Close() })
	p := NewProgram(context.Background(), func() {}, newModel(), io.Discard, inR)
	p.Start()
	p.Send(tea.WindowSizeMsg{Width: 40, Height: 10})
	long := "manifest failed: " + strings.Repeat("z", 100)
	for _, msg := range append([]tea.Msg{BatchStartMsg{Total: 31}}, s092History(30)...) {
		p.Send(msg)
	}
	p.Send(TaskStartMsg{ID: "bad", Label: "dev-go/bad"})
	p.Send(TaskDoneMsg{ID: "bad", OK: false, Summary: long + "\nOutput: child noise", Hint: "(repair: x)"})
	p.Stop()

	done := make(chan error, 1)
	go func() { done <- p.Wait() }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatalf("program did not stop within 5s")
	}
	var buf bytes.Buffer
	if err := p.PrintHistory(&buf); err != nil {
		t.Fatalf("PrintHistory: %v", err)
	}
	var want []string
	for i := 0; i < 30; i++ {
		want = append(want, fmt.Sprintf("✓ pkg-%02d — ok", i))
	}
	want = append(want, "✗ dev-go/bad — "+long+" (repair: x)")
	if got := stripANSI(buf.String()); got != strings.Join(want, "\n")+"\n" {
		t.Errorf("printed history:\n--- got ---\n%s--- want ---\n%s\n", got, strings.Join(want, "\n"))
	}
}

// A hint is shown only through a HintReporter and only when not empty; any
// other reporter gets the plain TaskDone it always had.
func TestTaskDoneWithHintFallsBackToTaskDone(t *testing.T) {
	var sink recordingSink
	TaskDoneWithHint(newTeaReporter(&sink), "p1", false, "failed", "(repair: x)", "")
	TaskDoneWithHint(newTeaReporter(&sink), "p2", false, "failed", "", "")
	if len(sink.msgs) != 2 {
		t.Fatalf("want 2 messages, got %d", len(sink.msgs))
	}
	if got := sink.msgs[0].(TaskDoneMsg).Hint; got != "(repair: x)" {
		t.Errorf("hint = %q, want it forwarded", got)
	}
	if got := sink.msgs[1].(TaskDoneMsg).Hint; got != "" {
		t.Errorf("an empty hint arrived as %q", got)
	}
	rec := &recordingReporter{}
	TaskDoneWithHint(rec, "p3", false, "failed", "(repair: x)", "")
	if ev := rec.snapshot(); len(ev) != 1 || ev[0].Kind != "TaskDone" || ev[0].Summary != "failed" {
		t.Errorf("a reporter without hints got %+v, want one plain TaskDone", ev)
	}
}

// recordingSink is a sender that keeps every message it is handed.
type recordingSink struct{ msgs []tea.Msg }

func (s *recordingSink) Send(msg tea.Msg) { s.msgs = append(s.msgs, msg) }
