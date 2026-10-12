package fixer

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/obentoo/bentoolkit/internal/autoupdate/llm"
	"github.com/obentoo/bentoolkit/internal/common/logging"
	"github.com/obentoo/bentoolkit/internal/common/secrets"
)

// Stage names of the three unattended agents. They prefix every progress line
// an agent reports and name its log file (<stage>-agent.jsonl), and the
// applier reports the same names as the package's stage while the agent runs.
const (
	StageManifestFix = "llm-fix"
	StageBuildFix    = "llm-build-fix"
	StageBumpReview  = "review"
)

// streamOutputArgs asks the CLI for one JSON event per line instead of a
// single envelope at exit. Under -p, stream-json is refused without --verbose.
var streamOutputArgs = []string{"--output-format", "stream-json", "--verbose"}

// progressQueue bounds how many progress lines wait for a slow callback. A
// line past it is dropped: the parser never waits on the callback, so neither
// does the agent's output pipe.
const progressQueue = 256

// progressDrainGrace bounds how long a finished run waits for the callback to
// take the lines still queued. A callback that has not caught up by then is
// abandoned: the lines it has not taken are dropped and no new call starts.
const progressDrainGrace = 500 * time.Millisecond

// AgentLogName is the file name an agent run in stage keeps its event stream in.
func AgentLogName(stage string) string {
	return stage + "-agent.jsonl"
}

// agentStreamRun is what one stream-json agent run left behind.
type agentStreamRun struct {
	// env is the envelope of the stream's final result event.
	env llm.ClaudeCodeEnvelope
	// parseErr is set when the stream carried no usable result event; it is
	// the error a missing single envelope produces.
	parseErr error
	// runErr is how the process ended.
	runErr error
	// stdout is the stream with every known secret masked, for the diagnostic
	// of a failed run: whole when it fits diagnosticsBudget, else its head and
	// tail around an elision marker.
	stdout string
	// stderr is the process's standard error, trimmed.
	stderr string
}

// agentStreamSpec says how one agent run reports and records itself.
type agentStreamSpec struct {
	// stage prefixes every progress line.
	stage string
	// progress receives one line per tool call and per refusal; nil drops them.
	progress func(string)
	// logPath is the file the stream is written to; empty writes none.
	logPath string
	// apiKey is masked in the log and the progress lines on top of every
	// secret resolved in this process.
	apiKey string
	// pkg names the package in the warnings.
	pkg string
	log *slog.Logger
}

// runAgentStream runs cmd, whose stdout is `claude -p --output-format
// stream-json --verbose`, and parses the stream while it is produced: every
// tool call and refusal reaches spec.progress as one line, and the stream is
// written to spec.logPath with every known secret masked. A log that cannot be
// written is a WARN: the agent's run is the news, not its log.
//
// spec.progress never holds up the agent (see progressForwarder), so the
// caller's timeout and cmd.WaitDelay bound the run whatever the callback does;
// the return waits at most progressDrainGrace more for the callback.
func runAgentStream(cmd *exec.Cmd, spec agentStreamSpec) agentStreamRun {
	log := logging.OrDiscard(spec.log)
	known := func() []string { return knownSecrets(spec.apiKey) }
	scrub := func(s string) string { return scrubSecrets(s, known()) }

	agentLog := openAgentLog(spec.logPath)
	diag := &diagnosticCapture{}
	scrubber := &lineScrubWriter{next: io.MultiWriter(diag, agentLog), secrets: known}
	progress := startProgressForwarder(spec.progress)

	pr, pw := io.Pipe()
	type parsed struct {
		env llm.ClaudeCodeEnvelope
		err error
	}
	done := make(chan parsed, 1)
	go func() {
		// The raw tool name and target are masked before the parser cuts them
		// to the terminal limit, and the whole line again after.
		env, err := llm.ParseStream(pr, func(ev llm.StreamEvent) {
			progress.offer(scrub(progressLine(spec.stage, ev)))
		}, llm.WithStreamLogger(log), llm.WithStreamScrub(scrub))
		// Drained to EOF whatever happened, so the process's output copy can
		// never block on a parser that stopped reading.
		if _, drainErr := io.Copy(io.Discard, pr); drainErr != nil && err == nil {
			err = drainErr
		}
		done <- parsed{env: env, err: err}
	}()

	var stderr bytes.Buffer
	cmd.Stdout = io.MultiWriter(pw, scrubber)
	cmd.Stderr = &stderr
	runErr := cmd.Run()
	// Run has returned, so every byte of stdout is already in the pipe: EOF
	// now ends the parse.
	closeErr := pw.Close()
	result := <-done
	// The parse has ended, so no line is offered any more. Lost lines leave a
	// hole in the task's output, so they are named once, at WARN: those the
	// full queue refused and those an abandoned callback was never handed.
	dropped, abandoned := progress.finish(progressDrainGrace)
	dropped += progress.stranded
	if dropped > 0 || abandoned {
		log.Warn("agent progress lines dropped: the progress callback fell behind",
			"package", spec.pkg, "stage", spec.stage, "dropped", dropped, "callback_abandoned", abandoned)
	}

	flushErr := scrubber.flush()
	if err := errors.Join(closeErr, flushErr, agentLog.close()); err != nil {
		log.Warn("agent log could not be written; the run itself is unaffected",
			"package", spec.pkg, "stage", spec.stage, "path", spec.logPath, "err", err)
	}

	return agentStreamRun{
		env:      result.env,
		parseErr: result.err,
		runErr:   runErr,
		stdout:   diag.String(),
		stderr:   strings.TrimSpace(stderr.String()),
	}
}

// progressLine renders one stream event as `<stage>: <tool> <target>`, or
// `<stage>: <tool> denied` for a refusal. A call without a target prints no
// trailing space.
func progressLine(stage string, ev llm.StreamEvent) string {
	line := stage + ": " + ev.Tool
	switch {
	case ev.Kind == llm.PermissionDenied:
		line += " denied"
	case ev.Target != "":
		line += " " + ev.Target
	}
	return line
}

// progressForwarder hands progress lines to a callback from a goroutine of
// its own, through a bounded queue, so a slow or blocked callback can never
// stall the stream parser and, through it, the agent's output pipe.
type progressForwarder struct {
	deliver func(string)
	lines   chan string
	done    chan struct{}
	// dropped counts the lines the full queue refused and offered the lines
	// it took; only the offering goroutine writes them, and finish reads them
	// after that goroutine is done.
	dropped int
	offered int

	// stranded is how many queued lines an abandoned callback was never
	// handed; finish sets it.
	stranded int

	// mu guards stopped and started, so finish can count stranded exactly.
	mu      sync.Mutex
	stopped bool
	started int
}

// startProgressForwarder starts delivering to deliver; nil delivers nothing.
func startProgressForwarder(deliver func(string)) *progressForwarder {
	f := &progressForwarder{deliver: deliver}
	if deliver == nil {
		return f
	}
	f.lines = make(chan string, progressQueue)
	f.done = make(chan struct{})
	go f.run()
	return f
}

func (f *progressForwarder) run() {
	defer close(f.done)
	for line := range f.lines {
		if !f.begin() {
			return
		}
		f.deliver(line)
	}
}

// begin counts one more line handed to the callback, unless finish has
// abandoned it.
func (f *progressForwarder) begin() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.stopped {
		return false
	}
	f.started++
	return true
}

// offer queues line, or drops it when the queue is full. It never blocks.
func (f *progressForwarder) offer(line string) {
	if f.lines == nil {
		return
	}
	select {
	case f.lines <- line:
		f.offered++
	default:
		f.dropped++
	}
}

// finish ends the queue and waits up to grace for the callback to take every
// line in it, so a callback that keeps up has seen every line before the run
// returns. Past grace the callback is abandoned: no new call starts, a call
// still running finishes on its own, and the lines never handed to it are
// counted in stranded. dropped is the lines the full queue refused. It must be
// called once, after the last offer.
func (f *progressForwarder) finish(grace time.Duration) (dropped int, abandoned bool) {
	if f.lines == nil {
		return 0, false
	}
	close(f.lines)
	timer := time.NewTimer(grace)
	defer timer.Stop()
	select {
	case <-f.done:
		return f.dropped, false
	case <-timer.C:
		f.mu.Lock()
		f.stopped = true
		f.stranded = f.offered - f.started
		f.mu.Unlock()
		return f.dropped, true
	}
}

// knownSecrets is every secret value resolved in this process plus apiKey,
// each also as the CLI's JSON stream spells it inside a string, longest first,
// blanks dropped: masking a shorter value first would leave fragments of a
// longer one that contains it.
func knownSecrets(apiKey string) []string {
	all := secrets.Resolved()
	if strings.TrimSpace(apiKey) != "" {
		all = append(all, apiKey)
	}
	out := make([]string, 0, 2*len(all))
	for _, v := range all {
		if strings.TrimSpace(v) == "" {
			continue
		}
		for _, form := range []string{v, nodeJSONEscape(v)} {
			if !slices.Contains(out, form) {
				out = append(out, form)
			}
		}
	}
	slices.SortStableFunc(out, func(a, b string) int { return len(b) - len(a) })
	return out
}

// nodeJSONEscape spells s the way Node's JSON.stringify writes it inside a
// string, which is how the claude CLI prints a tool result that echoes it:
// the quote and the backslash are escaped, the controls with a short form use
// it, other controls become a lowercase \u00xx, and everything else, non-ASCII
// and U+2028/U+2029 included, is left as it is. encoding/json differs on <, >,
// & and U+2028/U+2029, so it cannot be used here.
func nodeJSONEscape(s string) string {
	const hex = "0123456789abcdef"
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		switch c := s[i]; c {
		case '"':
			b.WriteString(`\"`)
		case '\\':
			b.WriteString(`\\`)
		case '\b':
			b.WriteString(`\b`)
		case '\f':
			b.WriteString(`\f`)
		case '\n':
			b.WriteString(`\n`)
		case '\r':
			b.WriteString(`\r`)
		case '\t':
			b.WriteString(`\t`)
		default:
			if c < 0x20 {
				b.WriteString(`\u00`)
				b.WriteByte(hex[c>>4])
				b.WriteByte(hex[c&0xf])
				continue
			}
			b.WriteByte(c)
		}
	}
	return b.String()
}

// scrubSecrets masks each value of known in s, in the given order.
func scrubSecrets(s string, known []string) string {
	for _, v := range known {
		s = secrets.Scrub(s, v)
	}
	return s
}

// scrubFlushLimit bounds how much of a line without a newline is held back
// before part of it is written anyway.
const scrubFlushLimit = llm.StreamLineLimit

// lineScrubWriter masks secrets a line at a time, so a value split across two
// writes is still masked whole. A line longer than scrubFlushLimit is written
// in parts; each part keeps back, unmasked, as many bytes as the longest secret
// could still need, and never ends inside a value, so no value is cut between
// two parts and none is masked before the longer value holding it is whole.
type lineScrubWriter struct {
	next    io.Writer
	secrets func() []string
	pending []byte
}

func (w *lineScrubWriter) Write(p []byte) (int, error) {
	w.pending = append(w.pending, p...)
	if i := bytes.LastIndexByte(w.pending, '\n'); i >= 0 {
		if err := w.emit(i + 1); err != nil {
			return 0, err
		}
	}
	if len(w.pending) >= scrubFlushLimit {
		if err := w.emitHoldingBack(); err != nil {
			return 0, err
		}
	}
	return len(p), nil
}

// emit writes pending[:n] scrubbed and keeps the rest.
func (w *lineScrubWriter) emit(n int) error {
	out := scrubSecrets(string(w.pending[:n]), w.secrets())
	w.pending = append(w.pending[:0], w.pending[n:]...)
	_, err := io.WriteString(w.next, out)
	return err
}

// emitHoldingBack writes the pending part-line, masked, but for its last
// len(longest secret)-1 bytes, which may start a value the next write ends.
// The cut moves back to the start of any value it would split, so the written
// part is masked as a whole of its own and the rest waits for the next part.
func (w *lineScrubWriter) emitHoldingBack() error {
	known := w.secrets()
	keep := 0
	if len(known) > 0 {
		keep = min(len(known[0])-1, len(w.pending))
	}
	return w.emit(holdCut(w.pending, len(w.pending)-keep, known))
}

// holdCut returns a cut no occurrence of a known value spans: cut itself, or
// the start of the value spanning it, repeated. Values that overlap all the way
// back to the start would hold the whole line, so then the cut moves forward
// past them instead, which keeps the held part bounded.
func holdCut(b []byte, cut int, known []string) int {
	for c := cut; c > 0; {
		start, _, ok := spanningValue(b, c, known)
		if !ok {
			return c
		}
		c = start
	}
	for {
		_, end, ok := spanningValue(b, cut, known)
		if !ok {
			return cut
		}
		cut = end
	}
}

// spanningValue finds an occurrence of a known value that starts before c and
// ends after it, within b.
func spanningValue(b []byte, c int, known []string) (start, end int, ok bool) {
	for _, v := range known {
		lo, hi := max(0, c-len(v)+1), min(len(b), c+len(v)-1)
		if lo >= hi {
			continue
		}
		if i := bytes.Index(b[lo:hi], []byte(v)); i >= 0 {
			return lo + i, lo + i + len(v), true
		}
	}
	return 0, 0, false
}

// flush writes whatever is left once the stream has ended.
func (w *lineScrubWriter) flush() error {
	if len(w.pending) == 0 {
		return nil
	}
	return w.emit(len(w.pending))
}

// agentLogFile is the agent's stream log. It never fails a write: the first
// error is kept, later writes are dropped, and close reports it, so a full
// disk cannot fail the agent run it records.
type agentLogFile struct {
	f   *os.File
	err error
}

// openAgentLog creates path 0600, with its directory, and truncates an
// earlier run's log. An empty path keeps nothing. The directory gets the mode
// the applier's child-run logs give it, so either may create it first. A
// symlink at path is refused (ELOOP), never followed to the file it names.
func openAgentLog(path string) *agentLogFile {
	if path == "" {
		return &agentLogFile{}
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil { //nolint:gosec // G301: the operator's own logs directory, read with ordinary tools; the files in it are 0600
		return &agentLogFile{err: fmt.Errorf("creating %s: %w", filepath.Dir(path), err)}
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC|syscall.O_NOFOLLOW, 0o600) //nolint:gosec // G304: the applier's own per-package log path, built from its logs directory
	if err != nil {
		return &agentLogFile{err: fmt.Errorf("opening %s: %w", path, err)}
	}
	// An earlier run's file keeps its mode through O_TRUNC.
	if err := f.Chmod(0o600); err != nil {
		return &agentLogFile{f: f, err: fmt.Errorf("restricting %s to 0600: %w", path, err)}
	}
	return &agentLogFile{f: f}
}

func (l *agentLogFile) Write(p []byte) (int, error) {
	if l.f != nil && l.err == nil {
		if _, err := l.f.Write(p); err != nil {
			l.err = fmt.Errorf("writing %s: %w", l.f.Name(), err)
		}
	}
	return len(p), nil
}

// close closes the file and returns the first error the log met.
func (l *agentLogFile) close() error {
	if l.f == nil {
		return l.err
	}
	if err := l.f.Close(); err != nil && l.err == nil {
		l.err = fmt.Errorf("closing %s: %w", l.f.Name(), err)
	}
	return l.err
}

// diagnosticCapture keeps the part of the masked stream a failure's diagnostic
// can quote, not the whole stream: its first and last bytes and its length.
type diagnosticCapture struct {
	head  []byte
	tail  []byte
	total int
}

const (
	diagnosticHead = diagnosticsBudget / 2
	diagnosticTail = diagnosticsBudget - diagnosticHead
)

func (c *diagnosticCapture) Write(p []byte) (int, error) {
	n := len(p)
	c.total += n
	if room := diagnosticHead - len(c.head); room > 0 {
		k := min(room, len(p))
		c.head = append(c.head, p[:k]...)
		p = p[k:]
	}
	if len(p) >= diagnosticTail {
		c.tail = append(c.tail[:0], p[len(p)-diagnosticTail:]...)
		return n, nil
	}
	c.tail = append(c.tail, p...)
	if len(c.tail) > 2*diagnosticTail {
		c.tail = append(c.tail[:0], c.tail[len(c.tail)-diagnosticTail:]...)
	}
	return n, nil
}

// String is the whole stream when it fits diagnosticsBudget. Otherwise it is
// the stream's head and tail around truncateDiagnostic's elision marker, cut so
// the whole fits the budget and truncateDiagnostic passes it unchanged.
func (c *diagnosticCapture) String() string {
	if c.total <= diagnosticsBudget {
		return string(c.head) + string(c.tail)
	}
	tail := c.tail[len(c.tail)-diagnosticTail:]
	// The marker is never longer than with the whole length as its count.
	keep := (diagnosticsBudget - len(elisionMarker("diagnostic", c.total))) / 2
	return strings.ToValidUTF8(string(c.head[:keep]), "") +
		elisionMarker("diagnostic", c.total-2*keep) +
		strings.ToValidUTF8(string(tail[len(tail)-keep:]), "")
}
