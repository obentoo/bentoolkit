package tui

import (
	"sync"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
)

// stripANSI removes color/cursor escape sequences so model View() output can be
// asserted on deterministically regardless of the active color profile.
func stripANSI(s string) string { return ansi.Strip(s) }

// recordingSender is a goroutine-safe test double for the teaReporter's program
// sink: it records every tea.Msg forwarded via Send. Used to assert teaReporter
// forwards all events and is -race clean under concurrent callers (R7.4).
type recordingSender struct {
	mu   sync.Mutex
	msgs []tea.Msg
}

func (s *recordingSender) Send(m tea.Msg) {
	s.mu.Lock()
	s.msgs = append(s.msgs, m)
	s.mu.Unlock()
}

func (s *recordingSender) snapshot() []tea.Msg {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]tea.Msg, len(s.msgs))
	copy(out, s.msgs)
	return out
}

// recordingReporter is a test Reporter that records every event for assertions.
// It is shared across this package's test files (events ordering, stream lines,
// stage/done sequences). It is mutex-guarded so -race tests can drive it from
// multiple goroutines.
type recordingReporter struct {
	mu     sync.Mutex
	events []recEvent
}

// recEvent is a flattened record of any Reporter call.
type recEvent struct {
	Kind     string // "BatchStart","TaskStart","TaskStage","TaskProgress","TaskLine","TaskDone","Log","BatchDone"
	ID       string
	Label    string
	Stage    string
	Frac     float64
	Stream   Stream
	Text     string
	EOL      bool
	OK       bool
	Summary  string
	Captured string
	Total    int
	Level    string
}

var _ Reporter = (*recordingReporter)(nil)

func (r *recordingReporter) add(e recEvent) {
	r.mu.Lock()
	r.events = append(r.events, e)
	r.mu.Unlock()
}

func (r *recordingReporter) BatchStart(total int) { r.add(recEvent{Kind: "BatchStart", Total: total}) }
func (r *recordingReporter) TaskStart(id, label string) {
	r.add(recEvent{Kind: "TaskStart", ID: id, Label: label})
}
func (r *recordingReporter) TaskStage(id, stage string) {
	r.add(recEvent{Kind: "TaskStage", ID: id, Stage: stage})
}
func (r *recordingReporter) TaskProgress(id string, frac float64) {
	r.add(recEvent{Kind: "TaskProgress", ID: id, Frac: frac})
}
func (r *recordingReporter) TaskLine(id string, stream Stream, text string, eol bool) {
	r.add(recEvent{Kind: "TaskLine", ID: id, Stream: stream, Text: text, EOL: eol})
}
func (r *recordingReporter) TaskDone(id string, ok bool, summary, captured string) {
	r.add(recEvent{Kind: "TaskDone", ID: id, OK: ok, Summary: summary, Captured: captured})
}
func (r *recordingReporter) Log(level, text string) {
	r.add(recEvent{Kind: "Log", Level: level, Text: text})
}
func (r *recordingReporter) BatchDone(summary string) {
	r.add(recEvent{Kind: "BatchDone", Summary: summary})
}

// snapshot returns a copy of the recorded events.
func (r *recordingReporter) snapshot() []recEvent {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]recEvent, len(r.events))
	copy(out, r.events)
	return out
}

// taskLines returns only the TaskLine events, in order.
func (r *recordingReporter) taskLines() []recEvent {
	var out []recEvent
	for _, e := range r.snapshot() {
		if e.Kind == "TaskLine" {
			out = append(out, e)
		}
	}
	return out
}

// kinds returns the ordered Kind of every recorded event.
func (r *recordingReporter) kinds() []string {
	evs := r.snapshot()
	out := make([]string, len(evs))
	for i, e := range evs {
		out[i] = e.Kind
	}
	return out
}
