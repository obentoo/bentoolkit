package tui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/bubbles/spinner"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// tailCap is the exact capacity of each task's bounded tail ring (R1.5). Once a
// task has committed tailCap lines, the oldest is evicted as a new one arrives,
// so the rendered tail never exceeds the last tailCap committed lines (plus the
// current live line, if any).
const tailCap = 10

// Glyphs used in history and active rows. They are fixed code points so the
// ANSI-stripped View is deterministic and matches R1.4 exactly.
const (
	glyphOK   = "✓" // U+2713
	glyphFail = "✗" // U+2717
)

// Styles for the frame. lipgloss emits color escapes only when the active
// profile supports color: under NO_COLOR / a dumb terminal the rendered text is
// the plain glyph, so the ANSI-stripped View is byte-identical either way (C3,
// NO_COLOR honored). These are package-level so View allocates no styles per
// frame.
var (
	styleHeader  = lipgloss.NewStyle().Bold(true)
	styleOK      = lipgloss.NewStyle().Foreground(lipgloss.Color("2")) // green
	styleFail    = lipgloss.NewStyle().Foreground(lipgloss.Color("1")) // red
	styleStage   = lipgloss.NewStyle().Foreground(lipgloss.Color("4")) // blue
	styleTail    = lipgloss.NewStyle().Foreground(lipgloss.Color("8")) // dim
	styleSummary = lipgloss.NewStyle().Bold(true)
)

// task is a single active unit of work. Tasks are held in an ordered slice so
// each renders in its own region and two tasks never share a rendered line
// (R1.3). The tail ring is a fixed-size buffer of committed lines; live holds
// the in-progress (eol=false) line that has not yet been committed.
type task struct {
	id       string
	label    string
	stage    string
	frac     float64 // progress fraction; < 0 (or fracNone) = indeterminate
	hasFrac  bool    // whether a determinate fraction has been set
	live     string  // current in-place line (eol=false), not yet committed
	hasLive  bool    // whether live carries a value to render
	ring     []string
	ringHead int  // index of the oldest element when the ring is full
	ringLen  int  // number of valid elements in ring (0..tailCap)
	done     bool // terminal state; a done task leaves the active set
}

// commit pushes a finished line into the bounded tail ring, evicting the oldest
// entry when the ring is already full (R1.5).
func (t *task) commit(line string) {
	if t.ring == nil {
		t.ring = make([]string, tailCap)
	}
	if t.ringLen < tailCap {
		t.ring[(t.ringHead+t.ringLen)%tailCap] = line
		t.ringLen++
		return
	}
	// Full: overwrite the oldest slot and advance the head.
	t.ring[t.ringHead] = line
	t.ringHead = (t.ringHead + 1) % tailCap
}

// tailLines returns the committed ring lines in order (oldest first), followed
// by the current live line if one is present. This is the visible tail for the
// task (R1.5): up to tailCap committed lines plus at most one live line.
func (t *task) tailLines() []string {
	out := make([]string, 0, t.ringLen+1)
	for i := 0; i < t.ringLen; i++ {
		out = append(out, t.ring[(t.ringHead+i)%tailCap])
	}
	if t.hasLive {
		out = append(out, t.live)
	}
	return out
}

// historyEntry is a terminal task rendered above the active block as a one-line
// ✓/✗ summary (R1.4).
type historyEntry struct {
	ok      bool
	label   string
	summary string
}

// model is the single tea.Model backing the live TUI. It holds an ordered set
// of active tasks, a history of completed tasks, a log scrollback, an overall
// done/total counter, and one shared spinner that animates every active task in
// lockstep. View composes these inline (no alt-screen — C3): the program owns
// the screen; the model only returns a multi-line string.
type model struct {
	header    string
	total     int
	completed int

	tasks   []*task          // ordered active tasks (R1.3)
	index   map[string]*task // id -> active task, for O(1) event routing
	history []historyEntry
	logs    []string

	spinner      spinner.Model
	batchDone    bool
	batchSummary string

	// cancel is the caller's cancellation hook (the signal/cancel chain that
	// cancels the operation context and kills child processes). It is injected
	// via withCancel so Ctrl-C reconciles with the EXISTING cancellation rather
	// than the model owning SIGINT itself (AD9). May be nil.
	cancel func()

	// confirmPending records an in-flight yes/no confirmation (AD5, R4.2). When
	// set, View renders confirmPrompt with a [y/n] hint and Update intercepts a
	// y/n keypress to answer on confirmReply. Both are cleared once answered.
	confirmPending bool
	confirmPrompt  string
	confirmReply   chan bool
}

// withCancel injects the caller's cancellation hook and returns the model so the
// call can be chained at construction. Ctrl-C invokes this hook (AD9, R5.1).
func (m *model) withCancel(cancel func()) *model {
	m.cancel = cancel
	return m
}

// newModel constructs a ready-to-run model. The shared spinner uses lipgloss
// styling only (no hard-coded ANSI), so NO_COLOR / non-color profiles degrade
// to plain glyphs automatically.
func newModel() *model {
	sp := spinner.New()
	sp.Spinner = spinner.Line
	return &model{
		header:  "Applying updates",
		index:   make(map[string]*task),
		spinner: sp,
	}
}

// Init implements tea.Model. It starts the shared spinner tick so active tasks
// animate; the tick is harmless when there are no active tasks.
func (m *model) Init() tea.Cmd {
	return m.spinner.Tick
}

// Update implements tea.Model. It routes every event message type to the
// corresponding state transition and advances the shared spinner on its tick.
func (m *model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		// Ctrl-C reconciles with the caller's cancellation (AD9, R5.1): invoke
		// the injected cancel synchronously so the operation context is cancelled
		// and child processes are killed, then quit. Bubble Tea installs no signal
		// handler here (WithoutSignalHandler), so this is the only path that turns
		// a Ctrl-C keypress into cancellation. Other keys keep prior behavior.
		if msg.Type == tea.KeyCtrlC {
			if m.cancel != nil {
				m.cancel()
			}
			return m, tea.Quit
		}
		// In-UI confirmation (AD5, R4.2): while a confirm is pending, a y/n key
		// answers it and clears the prompt. Ctrl-C above keeps precedence; any
		// other key while pending is ignored (the prompt stays). The reply is sent
		// non-blocking so a full or absent reply channel never stalls the UI.
		if m.confirmPending && msg.Type == tea.KeyRunes && len(msg.Runes) > 0 {
			switch msg.Runes[0] {
			case 'y', 'Y':
				m.answerConfirm(true)
			case 'n', 'N':
				m.answerConfirm(false)
			}
		}
		return m, nil

	case ConfirmMsg:
		m.confirmPending = true
		m.confirmPrompt = msg.Prompt
		m.confirmReply = msg.Reply
		return m, nil

	case BatchStartMsg:
		m.total = msg.Total
		return m, nil

	case TaskStartMsg:
		if _, ok := m.index[msg.ID]; ok {
			return m, nil
		}
		t := &task{id: msg.ID, label: msg.Label, frac: -1}
		m.tasks = append(m.tasks, t)
		m.index[msg.ID] = t
		return m, nil

	case TaskStageMsg:
		if t := m.index[msg.ID]; t != nil {
			t.stage = msg.Stage
		}
		return m, nil

	case TaskProgressMsg:
		if t := m.index[msg.ID]; t != nil {
			t.frac = msg.Frac
			t.hasFrac = msg.Frac >= 0
		}
		return m, nil

	case TaskLineMsg:
		if t := m.index[msg.ID]; t != nil {
			if msg.EOL {
				// COMMIT: push into the bounded ring, then clear the live line.
				t.commit(msg.Text)
				t.live = ""
				t.hasLive = false
			} else {
				// In-place update: REPLACE the live line; superseded value is lost.
				t.live = msg.Text
				t.hasLive = true
			}
		}
		return m, nil

	case TaskDoneMsg:
		m.finishTask(msg)
		return m, nil

	case LogMsg:
		m.logs = append(m.logs, formatLog(msg.Level, msg.Text))
		return m, nil

	case BatchDoneMsg:
		m.batchDone = true
		m.batchSummary = msg.Summary
		return m, nil

	case spinner.TickMsg:
		var cmd tea.Cmd
		m.spinner, cmd = m.spinner.Update(msg)
		return m, cmd
	}
	return m, nil
}

// answerConfirm delivers the user's yes/no decision on the pending reply channel
// and clears the pending confirm (AD5, R4.2). The send is non-blocking so a full
// or already-drained channel never stalls the UI; with the buffered reply channel
// Confirm uses, the value always lands.
func (m *model) answerConfirm(v bool) {
	if m.confirmReply != nil {
		select {
		case m.confirmReply <- v:
		default:
		}
	}
	m.confirmPending = false
	m.confirmPrompt = ""
	m.confirmReply = nil
}

// finishTask moves a task out of the ordered active set into history and bumps
// the completed counter (R1.4). CapturedOutput is intentionally not rendered.
func (m *model) finishTask(msg TaskDoneMsg) {
	t := m.index[msg.ID]
	if t == nil {
		// Done for an unknown task: still count it and record the summary so the
		// overall progress and history stay consistent.
		m.history = append(m.history, historyEntry{ok: msg.OK, label: msg.ID, summary: msg.Summary})
		m.completed++
		return
	}
	t.done = true
	delete(m.index, msg.ID)
	// Remove from the ordered active slice, preserving order.
	for i, at := range m.tasks {
		if at == t {
			m.tasks = append(m.tasks[:i], m.tasks[i+1:]...)
			break
		}
	}
	m.history = append(m.history, historyEntry{ok: msg.OK, label: t.label, summary: msg.Summary})
	m.completed++
}

// View implements tea.Model. It composes the frame inline (no alt-screen, C3):
//
//	<header>  <done>/<total>
//	<history lines, ✓/✗ above>
//	<active task blocks: spinner+label[+stage], indented tail beneath each>
//	<batch summary, when the run is done>
//
// When no active task remains the frame is fully static (no spinner glyph), so a
// driven-to-done model renders deterministically for the golden test.
func (m *model) View() string {
	var b strings.Builder

	// Header with overall done/total (R1.4) when a denominator is known.
	if m.total > 0 {
		b.WriteString(styleHeader.Render(fmt.Sprintf("%s  %d/%d", m.header, m.completed, m.total)))
	} else {
		b.WriteString(styleHeader.Render(m.header))
	}

	// History block (✓/✗), above the active tasks (R1.4).
	for _, h := range m.history {
		glyph, gs := glyphOK, styleOK
		if !h.ok {
			glyph, gs = glyphFail, styleFail
		}
		line := glyph + " " + h.label
		if h.summary != "" {
			line += " — " + h.summary
		}
		b.WriteByte('\n')
		b.WriteString(gs.Render(line))
	}

	// Active task blocks. Each task is its own region: a header row carrying the
	// spinner glyph + label (+ optional [stage]), then its bounded tail indented
	// beneath it, so two tasks never share a rendered line (R1.3).
	spin := m.spinner.View()
	for _, t := range m.tasks {
		b.WriteByte('\n')
		b.WriteString(spin)
		b.WriteByte(' ')
		b.WriteString(t.label)
		if t.stage != "" {
			b.WriteString("  ")
			b.WriteString(styleStage.Render("[" + t.stage + "]"))
		}
		if t.hasFrac {
			fmt.Fprintf(&b, "  %d%%", int(t.frac*100+0.5))
		}
		for _, line := range t.tailLines() {
			b.WriteString("\n    ")
			b.WriteString(styleTail.Render(line))
		}
	}

	// Log scrollback, if any stray writes were routed here (AD6).
	for _, l := range m.logs {
		b.WriteByte('\n')
		b.WriteString(l)
	}

	// Final batch summary line when the run is closed.
	if m.batchDone && m.batchSummary != "" {
		b.WriteByte('\n')
		b.WriteString(styleSummary.Render(m.batchSummary))
	}

	// Pending in-UI confirmation (AD5, R4.2): render the prompt with a [y/n] hint
	// so the decision is made inside the frame instead of reading os.Stdin behind
	// the program. The line is removed once answered (answerConfirm clears it).
	if m.confirmPending {
		b.WriteByte('\n')
		b.WriteString(styleSummary.Render(m.confirmPrompt + " [y/n]"))
	}

	return b.String()
}

// formatLog renders a single scrollback line; an empty level degrades to the
// bare text so plain log forwarding stays uncluttered.
func formatLog(level, text string) string {
	if level == "" {
		return text
	}
	return level + ": " + text
}
