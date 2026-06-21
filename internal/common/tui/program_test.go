package tui

import (
	"context"
	"io"
	"sync/atomic"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

// R5.1: a Ctrl-C key message invokes the injected cancel func (so the in-flight
// operation's context is cancelled and child processes are terminated) and
// returns tea.Quit so the UI exits. The cancel must run synchronously and the
// returned command must be exactly tea.Quit (not batched).
func TestModelCtrlCCancelsAndQuits(t *testing.T) {
	var cancelled atomic.Bool
	var m tea.Model = newModel().withCancel(func() { cancelled.Store(true) })

	_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyCtrlC})

	if !cancelled.Load() {
		t.Fatalf("Ctrl-C should invoke the injected cancel func")
	}
	if cmd == nil {
		t.Fatalf("Ctrl-C should return a quit command")
	}
	if msg := cmd(); msg != (tea.QuitMsg{}) {
		t.Fatalf("Ctrl-C command should be tea.Quit, got %T", msg)
	}
}

// A nil cancel must not panic on Ctrl-C (it still quits).
func TestModelCtrlCNilCancelSafe(t *testing.T) {
	var m tea.Model = newModel()
	_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyCtrlC})
	if cmd == nil || cmd() != (tea.QuitMsg{}) {
		t.Fatalf("Ctrl-C with no cancel should still quit")
	}
}

// R5.1: cancelling the bound context quits the program (tea.WithContext wiring),
// so the TUI tears down when the caller's signal chain cancels.
func TestProgramContextCancellationQuits(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	inR, inW := io.Pipe()
	t.Cleanup(func() { _ = inW.Close() })

	p := NewProgram(ctx, cancel, newModel(), io.Discard, inR)
	p.Start()

	cancel() // external cancellation must stop the program

	done := make(chan error, 1)
	go func() { done <- p.Wait() }()
	select {
	case <-done:
		// quit as expected
	case <-time.After(5 * time.Second):
		p.Stop()
		t.Fatalf("program did not quit within 5s of context cancellation")
	}
}

// Lifecycle: Start, Send events, Stop — Wait returns without hanging.
func TestProgramSendAndStop(t *testing.T) {
	inR, inW := io.Pipe()
	t.Cleanup(func() { _ = inW.Close() })

	p := NewProgram(context.Background(), func() {}, newModel(), io.Discard, inR)
	p.Start()
	p.Send(BatchStartMsg{Total: 1})
	p.Send(TaskStartMsg{ID: "p1", Label: "cat/pkg"})
	p.Send(TaskDoneMsg{ID: "p1", OK: true, Summary: "ok"})

	done := make(chan error, 1)
	go func() { done <- p.Wait() }()
	p.Stop()
	select {
	case <-done:
		// stopped as expected
	case <-time.After(5 * time.Second):
		t.Fatalf("program did not stop within 5s")
	}
}
