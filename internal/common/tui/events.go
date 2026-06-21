// Package tui is a single Bubble Tea-based TUI foundation for bentoolkit.
//
// Command code never talks to Bubble Tea directly: it emits progress through the
// Reporter interface (see reporter.go), which is implemented by two backends —
// teaReporter (forwards to a live Program) and plainReporter (streams plain text
// to an io.Writer for non-TTY / opt-out runs). The event types below are the
// tea.Msg values teaReporter forwards via program.Send (the documented
// goroutine-safe ingress), so they double as the model's Update inputs.
package tui

// Stream identifies which subprocess stream a captured tail line came from.
type Stream int

const (
	// StreamStdout is the child process's standard output.
	StreamStdout Stream = iota
	// StreamStderr is the child process's standard error.
	StreamStderr
)

// String renders the stream name for plain-mode prefixes and log lines.
func (s Stream) String() string {
	switch s {
	case StreamStderr:
		return "stderr"
	default:
		return "stdout"
	}
}

// The event message types. Each is a tea.Msg (tea.Msg is any), forwarded by
// teaReporter through program.Send and consumed by the model's Update.
type (
	// BatchStartMsg opens a run and sets the optional denominator/header.
	BatchStartMsg struct{ Total int }

	// TaskStartMsg registers a new task slot with a display label.
	TaskStartMsg struct{ ID, Label string }

	// TaskStageMsg moves a task to a named stage ("manifest", "llm-fix", …).
	TaskStageMsg struct{ ID, Stage string }

	// TaskProgressMsg sets a task's progress fraction. Frac < 0 means
	// indeterminate (render a spinner rather than a bar).
	TaskProgressMsg struct {
		ID   string
		Frac float64
	}

	// TaskLineMsg carries one tail update for a task. Text is the full content
	// of the line currently being assembled. EOL distinguishes the two terminal
	// behaviors the StreamCapture emitter detects (AD4, R1.2):
	//   - EOL=false: an in-place update (the child emitted "\r" or the partial
	//     line was flushed). The model REPLACES the task's live line.
	//   - EOL=true: the line ended with "\n". The model COMMITS the live line
	//     into the bounded tail ring and starts a fresh live line.
	TaskLineMsg struct {
		ID     string
		Stream Stream
		Text   string
		EOL    bool
	}

	// TaskDoneMsg terminates a task. CapturedOutput carries the full buffered
	// child output for the error path (the model never renders it in full).
	TaskDoneMsg struct {
		ID             string
		OK             bool
		Summary        string
		CapturedOutput string
	}

	// LogMsg routes a logger/output line into the TUI scrollback so stray
	// writes do not corrupt a frame (AD6).
	LogMsg struct{ Level, Text string }

	// BatchDoneMsg closes the run with a final summary line.
	BatchDoneMsg struct{ Summary string }
)
