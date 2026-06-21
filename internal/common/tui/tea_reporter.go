package tui

import (
	tea "github.com/charmbracelet/bubbletea"
)

// sender is the goroutine-safe ingress the teaReporter forwards events through.
// It is the minimal slice of *Program (its Send method) the reporter depends on,
// so the reporter is testable without standing up a real Bubble Tea program.
type sender interface{ Send(tea.Msg) }

// Compile-time guarantees: *Program is a usable sink, and teaReporter is a
// Reporter.
var (
	_ sender   = (*Program)(nil)
	_ Reporter = (*teaReporter)(nil)
)

// teaReporter implements Reporter by translating each call into the matching
// tea.Msg and forwarding it through the program's goroutine-safe Send (design
// §5, AD3). It holds no mutable state, so it is trivially safe to call from the
// parallel manifest workers (R7.4): every method is a pure construct-and-forward.
type teaReporter struct {
	s sender
}

// newTeaReporter builds a teaReporter forwarding to s (in production a *Program;
// in tests a recording sink).
func newTeaReporter(s sender) *teaReporter { return &teaReporter{s: s} }

func (r *teaReporter) BatchStart(total int) {
	r.s.Send(BatchStartMsg{Total: total})
}

func (r *teaReporter) TaskStart(id, label string) {
	r.s.Send(TaskStartMsg{ID: id, Label: label})
}

func (r *teaReporter) TaskStage(id, stage string) {
	r.s.Send(TaskStageMsg{ID: id, Stage: stage})
}

func (r *teaReporter) TaskProgress(id string, frac float64) {
	r.s.Send(TaskProgressMsg{ID: id, Frac: frac})
}

func (r *teaReporter) TaskLine(id string, stream Stream, text string, eol bool) {
	r.s.Send(TaskLineMsg{ID: id, Stream: stream, Text: text, EOL: eol})
}

func (r *teaReporter) TaskDone(id string, ok bool, summary, capturedOutput string) {
	r.s.Send(TaskDoneMsg{ID: id, OK: ok, Summary: summary, CapturedOutput: capturedOutput})
}

func (r *teaReporter) Log(level, text string) {
	r.s.Send(LogMsg{Level: level, Text: text})
}

func (r *teaReporter) BatchDone(summary string) {
	r.s.Send(BatchDoneMsg{Summary: summary})
}
