package overlay

import (
	"fmt"
	"io"
	"strings"
	"sync"

	"github.com/fatih/color"
)

// NoopReporter implements ProgressReporter by ignoring every event.
// Useful as a default when callers don't want any progress output.
type NoopReporter struct{}

func (NoopReporter) Total(int, int)                                      {}
func (NoopReporter) Start(int, int, ManifestUpdate)                      {}
func (NoopReporter) Done(int, int, ManifestUpdate, bool, string, string) {}
func (NoopReporter) Finish()                                             {}

// LogReporter is the non-TTY fallback: it prints one line when each target
// starts and one line when each target finishes, in the order events arrive.
// Lines mix safely under concurrency thanks to an internal mutex; output
// can interleave package-by-package, but never line-by-line.
type LogReporter struct {
	w     io.Writer
	mu    sync.Mutex
	total int
	done  int
}

// NewLogReporter returns a LogReporter writing to w. Use this when stdout
// is not a TTY (CI logs, files, pipes).
func NewLogReporter(w io.Writer) *LogReporter {
	return &LogReporter{w: w}
}

func (r *LogReporter) Total(n, jobs int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.total = n
	fmt.Fprintf(r.w, ">>> Regenerating %d manifest(s) with up to %d parallel job(s)\n", n, jobs)
}

func (r *LogReporter) Start(_ int, _ int, t ManifestUpdate) {
	r.mu.Lock()
	defer r.mu.Unlock()
	fmt.Fprintf(r.w, ">>> START  %s/%s\n", t.Category, t.Package)
}

func (r *LogReporter) Done(_ int, _ int, t ManifestUpdate, ok bool, errMsg, output string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.done++
	if ok {
		fmt.Fprintf(r.w, ">>> OK     %s/%s  (%d/%d)\n", t.Category, t.Package, r.done, r.total)
		return
	}
	fmt.Fprintf(r.w, ">>> FAIL   %s/%s: %s  (%d/%d)\n", t.Category, t.Package, errMsg, r.done, r.total)
	if strings.TrimSpace(output) != "" {
		fmt.Fprintln(r.w, indent(output, "    "))
	}
}

func (r *LogReporter) Finish() {}

// TUIReporter is a minimal terminal UI: a fixed block at the bottom of the
// screen shows N "slots" — one per worker — and a global progress bar.
// Completed jobs scroll above the block as ✓/✗ history lines.
//
// Terminal control uses raw ANSI escapes (no curses), so it works on any
// VT100-ish terminal. Callers must ensure stdout is a TTY before using it;
// out-of-TTY use will produce garbage. NewTUIReporter performs no detection.
type TUIReporter struct {
	w        io.Writer
	mu       sync.Mutex
	total    int
	done     int
	failed   int
	slots    []slotState
	rendered bool // first frame drawn?
}

type slotState struct {
	active bool
	label  string // "category/package"
}

// NewTUIReporter constructs a TUIReporter writing to w (typically os.Stdout).
func NewTUIReporter(w io.Writer) *TUIReporter {
	return &TUIReporter{w: w}
}

func (r *TUIReporter) Total(n, jobs int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.total = n
	if jobs < 1 {
		jobs = 1
	}
	r.slots = make([]slotState, jobs)
	// Hide cursor for a cleaner redraw loop. Restored in Finish.
	fmt.Fprint(r.w, "\x1b[?25l")
	r.draw()
}

func (r *TUIReporter) Start(_ int, worker int, t ManifestUpdate) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if worker >= 0 && worker < len(r.slots) {
		r.slots[worker] = slotState{active: true, label: t.Category + "/" + t.Package}
	}
	r.draw()
}

func (r *TUIReporter) Done(_ int, worker int, t ManifestUpdate, ok bool, errMsg, output string) {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.done++
	if !ok {
		r.failed++
	}
	if worker >= 0 && worker < len(r.slots) {
		r.slots[worker] = slotState{}
	}

	// Rewind to the top of the live block, print the history line in place,
	// then redraw the block below it. This is what makes finished packages
	// scroll up while active slots stay anchored at the bottom.
	r.eraseBlock()
	r.printHistoryLine(t, ok, errMsg, output)
	r.draw()
}

func (r *TUIReporter) Finish() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.eraseBlock()
	r.rendered = false
	// Final summary line + restore cursor.
	fmt.Fprintf(r.w, "Manifest regeneration: %d succeeded, %d failed (of %d)\n",
		r.total-r.failed, r.failed, r.total)
	fmt.Fprint(r.w, "\x1b[?25h")
}

// blockHeight is the number of lines the live block occupies: slots + bar.
func (r *TUIReporter) blockHeight() int {
	return len(r.slots) + 1
}

// draw renders (or re-renders) the live block at the current cursor position
// and leaves the cursor at the line *just below* the bar — ready for the next
// erase/redraw cycle.
func (r *TUIReporter) draw() {
	// Progress bar.
	pct := 0.0
	if r.total > 0 {
		pct = float64(r.done) / float64(r.total)
	}
	bar := renderBar(pct, 24)
	fmt.Fprintf(r.w, "\x1b[2K\rRegenerating manifests  [%d/%d]  %s  %3d%%\n",
		r.done, r.total, bar, int(pct*100))

	// Worker slots.
	for _, s := range r.slots {
		if s.active {
			fmt.Fprintf(r.w, "\x1b[2K\r  %s %s\n", color.CyanString("⟳"), s.label)
			continue
		}
		fmt.Fprintf(r.w, "\x1b[2K\r  %s idle\n", color.HiBlackString("·"))
	}
	r.rendered = true
}

// eraseBlock moves the cursor up to the top of the previously drawn block
// and clears it line-by-line. After this returns, the cursor is at column 0
// of the line where the bar was — ready for either a history line or a
// redraw.
func (r *TUIReporter) eraseBlock() {
	if !r.rendered {
		return
	}
	// The cursor is on the line just *below* the block (each draw line ended
	// with \n). Move up blockHeight() lines.
	fmt.Fprintf(r.w, "\x1b[%dA", r.blockHeight())
	// Clear each block line as we walk down.
	for i := 0; i < r.blockHeight(); i++ {
		fmt.Fprint(r.w, "\x1b[2K\r")
		if i < r.blockHeight()-1 {
			fmt.Fprint(r.w, "\n")
		}
	}
	// We're now on the last line of the (now-blank) block. Move back up to
	// the first cleared line so callers can write history there.
	fmt.Fprintf(r.w, "\x1b[%dA\r", r.blockHeight()-1)
	r.rendered = false
}

// printHistoryLine writes a single ✓/✗ line at the current cursor position
// followed by a newline. Failure output, if any, is appended (indented).
func (r *TUIReporter) printHistoryLine(t ManifestUpdate, ok bool, errMsg, output string) {
	label := t.Category + "/" + t.Package
	if ok {
		fmt.Fprintf(r.w, "%s %s\n", color.GreenString("✓"), label)
		return
	}
	fmt.Fprintf(r.w, "%s %s  %s\n", color.RedString("✗"), label, errMsg)
	if s := strings.TrimSpace(output); s != "" {
		fmt.Fprintln(r.w, indent(s, "    "))
	}
}

// renderBar produces an ASCII progress bar of the given width. pct is
// clamped to [0,1].
func renderBar(pct float64, width int) string {
	if pct < 0 {
		pct = 0
	}
	if pct > 1 {
		pct = 1
	}
	filled := int(pct * float64(width))
	if filled > width {
		filled = width
	}
	return strings.Repeat("█", filled) + strings.Repeat("░", width-filled)
}

// indent prefixes every line of s with prefix.
func indent(s, prefix string) string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	for i, ln := range lines {
		lines[i] = prefix + ln
	}
	return strings.Join(lines, "\n")
}
