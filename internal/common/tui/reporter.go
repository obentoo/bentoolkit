package tui

// Reporter is the single sink command code emits progress to. Both
// backends implement it: teaReporter forwards each call as a tea.Msg through
// program.Send, plainReporter writes deterministic lines to an io.Writer.
//
// A nil or Noop reporter is exactly equivalent to having no progress
// reporting: silent, fully-buffered execution. Command code holding a possibly-nil
// Reporter should normalize it through orNoop before use.
type Reporter interface {
	// BatchStart opens a run; total is an optional denominator (0 = unknown).
	BatchStart(total int)
	// TaskStart registers a task slot under a display label.
	TaskStart(id, label string)
	// TaskStage moves a task to a named stage.
	TaskStage(id, stage string)
	// TaskProgress sets a task's progress fraction; frac < 0 = indeterminate.
	TaskProgress(id string, frac float64)
	// TaskLine emits one tail update for a task. eol=false is an in-place
	// replacement of the live line (carriage-return / partial flush); eol=true
	// commits the line into the bounded tail (it ended with a newline). See
	// TaskLineMsg for the model-side semantics.
	TaskLine(id string, stream Stream, text string, eol bool)
	// TaskDone terminates a task; capturedOutput carries the full buffer for
	// the error path (the Output: %s contract at applier.go is preserved).
	TaskDone(id string, ok bool, summary, capturedOutput string)
	// Log routes a logger/output line into the UI.
	Log(level, text string)
	// BatchDone closes the run with a final summary.
	BatchDone(summary string)
}

// HintReporter is a Reporter that can show a short next step beside a finished
// task's outcome. It is optional: the plain and no-op backends do not implement
// it, so a hint reaches only the live region.
type HintReporter interface {
	Reporter
	// TaskDoneHint is TaskDone with hint shown at the end of the task's row.
	TaskDoneHint(id string, ok bool, summary, hint, capturedOutput string)
}

// TaskDoneWithHint closes a task on r, showing hint beside its outcome when r
// is a HintReporter and the hint is not empty; otherwise it is r.TaskDone.
func TaskDoneWithHint(r Reporter, id string, ok bool, summary, hint, capturedOutput string) {
	if hr, can := r.(HintReporter); can && hint != "" {
		hr.TaskDoneHint(id, ok, summary, hint, capturedOutput)
		return
	}
	r.TaskDone(id, ok, summary, capturedOutput)
}

// noopReporter discards every event. It is the default so command code that
// supplies no reporter behaves exactly as before this package existed.
type noopReporter struct{}

func (noopReporter) BatchStart(int)                        {}
func (noopReporter) TaskStart(string, string)              {}
func (noopReporter) TaskStage(string, string)              {}
func (noopReporter) TaskProgress(string, float64)          {}
func (noopReporter) TaskLine(string, Stream, string, bool) {}
func (noopReporter) TaskDone(string, bool, string, string) {}
func (noopReporter) Log(string, string)                    {}
func (noopReporter) BatchDone(string)                      {}

// Noop returns a Reporter that discards all events.
func Noop() Reporter { return noopReporter{} }

// orNoop normalizes a possibly-nil Reporter to a non-nil one, so call sites can
// emit unconditionally without nil checks.
func orNoop(r Reporter) Reporter {
	if r == nil {
		return noopReporter{}
	}
	return r
}
