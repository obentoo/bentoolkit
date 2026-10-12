package tui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/bubbles/spinner"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
)

// tailCap is the exact capacity of each task's bounded tail ring. Once a
// task has committed tailCap lines, the oldest is evicted as a new one arrives,
// so the rendered tail never exceeds the last tailCap committed lines (plus the
// current live line, if any).
const tailCap = 10

// defaultWidth and defaultHeight size the frame until the program reports the
// real terminal size through a tea.WindowSizeMsg.
const (
	defaultWidth  = 80
	defaultHeight = 24
)

// ellipsis ends a row cut to fit the terminal width.
const ellipsis = "…" // U+2026

// Glyphs used in history and active rows. They are fixed code points so the
// ANSI-stripped View is deterministic and matches the documented frame exactly.
const (
	glyphOK   = "✓" // U+2713
	glyphFail = "✗" // U+2717
)

// Styles for the frame. lipgloss emits color escapes only when the active
// profile supports color: under NO_COLOR / a dumb terminal the rendered text is
// the plain glyph, so the ANSI-stripped View is byte-identical either way
// (NO_COLOR honored). These are package-level so View allocates no styles per
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
// each renders in its own region and two tasks never share a rendered line.
// The tail ring is a fixed-size buffer of committed lines; live holds
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
// entry when the ring is already full.
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
// task: up to tailCap committed lines plus at most one live line.
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
// ✓/✗ summary, ending with its hint when it carries one.
type historyEntry struct {
	ok      bool
	label   string
	summary string
	hint    string
}

// model is the single tea.Model backing the live TUI. It holds an ordered set
// of active tasks, a history of completed tasks, a log scrollback, an overall
// done/total counter, and one shared spinner that animates every active task in
// lockstep. View composes these inline (no alt-screen): the program owns
// the screen; the model only returns a multi-line string.
type model struct {
	header    string
	total     int
	completed int

	tasks   []*task          // ordered active tasks
	index   map[string]*task // id -> active task, for O(1) event routing
	history []historyEntry
	logs    []string

	spinner      spinner.Model
	batchDone    bool
	batchSummary string

	// width and height are the terminal size from the last tea.WindowSizeMsg
	// (80x24 until one arrives). Every rendered row is cut to width-1 cells.
	width  int
	height int

	// cancel is the caller's cancellation hook (the signal/cancel chain that
	// cancels the operation context and kills child processes). It is injected
	// via withCancel so Ctrl-C reconciles with the EXISTING cancellation rather
	// than the model owning SIGINT itself. May be nil.
	cancel func()

	// confirmPending records an in-flight yes/no confirmation. When
	// set, View renders confirmPrompt with a [y/n] hint and Update intercepts a
	// y/n keypress to answer on confirmReply. Both are cleared once answered.
	confirmPending bool
	confirmPrompt  string
	confirmReply   chan bool

	// released is set once the program is asked to stop: the history rows
	// leave the frame, because Program.PrintHistory writes them to the
	// scrollback after the live region is gone.
	released bool
}

// releaseMsg asks the model to drop the history from its frame and quit. It is
// what Program.Stop sends, so the frame bubbletea leaves on screen at exit
// carries no history row PrintHistory then prints again.
type releaseMsg struct{}

// withCancel injects the caller's cancellation hook and returns the model so the
// call can be chained at construction. Ctrl-C invokes this hook.
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
		width:   defaultWidth,
		height:  defaultHeight,
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
	case tea.WindowSizeMsg:
		m.resize(msg)
		return m, nil

	case tea.KeyMsg:
		// Ctrl-C reconciles with the caller's cancellation: invoke
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
		// In-UI confirmation: while a confirm is pending, a y/n key
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

	case releaseMsg:
		m.released = true
		return m, tea.Quit

	case spinner.TickMsg:
		var cmd tea.Cmd
		m.spinner, cmd = m.spinner.Update(msg)
		return m, cmd
	}
	return m, nil
}

// resize records the terminal size so the next View fits it. A non-positive
// dimension (a terminal that cannot report its size) keeps the previous value.
func (m *model) resize(msg tea.WindowSizeMsg) {
	if msg.Width > 0 {
		m.width = msg.Width
	}
	if msg.Height > 0 {
		m.height = msg.Height
	}
}

// answerConfirm delivers the user's yes/no decision on the pending reply channel
// and clears the pending confirm. The send is non-blocking so a full
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
// the completed counter. CapturedOutput is intentionally not rendered, and only
// the summary's first line is kept: an error that embeds a child's output would
// otherwise turn one history row into a screenful.
func (m *model) finishTask(msg TaskDoneMsg) {
	summary, _, _ := strings.Cut(msg.Summary, "\n")
	entry := historyEntry{ok: msg.OK, label: msg.ID, summary: summary, hint: msg.Hint}
	t := m.index[msg.ID]
	if t == nil {
		// Done for an unknown task: still count it and record the summary so the
		// overall progress and history stay consistent.
		m.history = append(m.history, entry)
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
	entry.label = t.label
	m.history = append(m.history, entry)
	m.completed++
}

// View implements tea.Model. It composes the frame inline (no alt-screen):
//
//	<header>  <done>/<total>
//	<history lines, ✓/✗ above>
//	<active task blocks: spinner+label[+stage], indented tail beneath each>
//	<batch summary, when the run is done>
//
// Every row is cut to the terminal width minus one column, and the frame is
// folded to at most height-1 rows (see frame.fold), so the terminal never wraps
// or scrolls the live region and the inline redraw erases exactly the rows it
// drew. When no active task remains the frame is fully static (no spinner
// glyph), so a driven-to-done model renders deterministically for the golden
// test.
//
// Once released, the frame drops the history and ends with an empty row:
// bubbletea erases the row under the cursor (the last one) as it exits, and the
// history is printed below the frame by Program.PrintHistory instead.
func (m *model) View() string {
	f := frame{
		head:    splitRows(m.headerRow()),
		tasks:   m.taskBlocks(),
		trailer: splitRows(m.trailerRows()...),
	}
	if m.released {
		return m.fitRows(append(f.fold(m.height-1), ""))
	}
	for _, h := range m.history {
		f.history = append(f.history, splitRows(historyRow(h, m.width)))
	}
	return m.fitRows(f.fold(m.height - 1))
}

// historyLines renders every history row, oldest first, neither folded nor cut
// to the terminal width: the scrollback they are printed to wraps a long row
// instead of losing its end.
func (m *model) historyLines() []string {
	lines := make([]string, 0, len(m.history))
	for _, h := range m.history {
		lines = append(lines, historyRow(h, 0))
	}
	return lines
}

// trailerRows renders what follows the task blocks: routed log lines, the final
// batch summary once the run is closed, and a pending in-UI confirmation.
func (m *model) trailerRows() []string {
	rows := append([]string(nil), m.logs...)
	if m.batchDone && m.batchSummary != "" {
		rows = append(rows, styleSummary.Render(m.batchSummary))
	}
	// The confirmation is answered inside the frame instead of reading
	// os.Stdin behind the program; answerConfirm removes the line.
	if m.confirmPending {
		rows = append(rows, styleSummary.Render(m.confirmPrompt+" [y/n]"))
	}
	return rows
}

// headerRow renders the header with overall done/total when a denominator is
// known.
func (m *model) headerRow() string {
	if m.total > 0 {
		return styleHeader.Render(fmt.Sprintf("%s  %d/%d", m.header, m.completed, m.total))
	}
	return styleHeader.Render(m.header)
}

// historyRow renders a finished task's ✓/✗ row. With a hint and a width > 0 the
// summary is the part cut to fit, so the hint at the end of the row stays
// readable; a row whose label and hint alone overflow is left to fitWidth.
func historyRow(h historyEntry, width int) string {
	glyph, gs := glyphOK, styleOK
	if !h.ok {
		glyph, gs = glyphFail, styleFail
	}
	head := glyph + " " + h.label
	body := ""
	if h.summary != "" {
		body = " — " + h.summary
	}
	tail := ""
	if h.hint != "" {
		tail = " " + h.hint
	}
	if room := width - 1 - ansi.StringWidth(head+tail); tail != "" && width > 0 && room > 1 {
		body = ansi.Truncate(body, room, ellipsis)
	}
	return gs.Render(head + body + tail)
}

// taskBlocks renders the active task blocks. Each task is its own region: a row
// carrying the spinner glyph + label (+ optional [stage] and percentage), then
// its bounded tail indented beneath it, so two tasks never share a row.
func (m *model) taskBlocks() []taskBlock {
	blocks := make([]taskBlock, 0, len(m.tasks))
	spin := m.spinner.View()
	for _, t := range m.tasks {
		var b strings.Builder
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
		blk := taskBlock{row: splitRows(b.String())}
		for _, line := range t.tailLines() {
			blk.tail = append(blk.tail, splitRows("    "+styleTail.Render(line))...)
		}
		blocks = append(blocks, blk)
	}
	return blocks
}

// frame is a rendered View split into sections of physical rows, so the height
// fold can drop history and tail rows while the header and the task rows stay.
type frame struct {
	head    []string
	history [][]string // the rows of each finished task, oldest first
	tasks   []taskBlock
	trailer []string
}

// taskBlock is one active task: its own row (never dropped) and its tail rows,
// which the height fold trims from the top.
type taskBlock struct {
	row  []string
	tail []string
}

// fold returns the frame as at most budget rows. History entries go first,
// oldest first, replaced by one line counting them; then tail rows go from the
// top of the longest tail, so every task keeps its newest lines the longest.
// The header, the task rows and the trailer are never dropped, so a frame made
// of those alone can still exceed the budget. The model keeps every history
// entry; only this rendering drops them.
func (f frame) fold(budget int) []string {
	n := len(f.head) + len(f.trailer)
	for _, h := range f.history {
		n += len(h)
	}
	for _, t := range f.tasks {
		n += len(t.row) + len(t.tail)
	}

	dropped := 0
	for n > budget && dropped < len(f.history) {
		if dropped == 0 {
			n++ // the fold line takes the place of the dropped entries
		}
		n -= len(f.history[dropped])
		dropped++
	}

	tails := make([]int, len(f.tasks)) // tail rows trimmed per task
	for n > budget {
		i := longestTail(f.tasks, tails)
		if i < 0 {
			break
		}
		tails[i]++
		n--
	}

	rows := append(make([]string, 0, n), f.head...)
	if dropped > 0 {
		rows = append(rows, styleTail.Render(fmt.Sprintf("%s %d earlier package(s) finished", ellipsis, dropped)))
	}
	for _, h := range f.history[dropped:] {
		rows = append(rows, h...)
	}
	for i, t := range f.tasks {
		rows = append(rows, t.row...)
		rows = append(rows, t.tail[tails[i]:]...)
	}
	return append(rows, f.trailer...)
}

// longestTail returns the index of the task with the most tail rows left after
// trimmed[i] rows were taken from task i, or -1 when no tail row is left. Ties
// go to the earliest task.
func longestTail(tasks []taskBlock, trimmed []int) int {
	best, most := -1, 0
	for i, t := range tasks {
		if left := len(t.tail) - trimmed[i]; left > most {
			best, most = i, left
		}
	}
	return best
}

// splitRows splits rows on embedded newlines into the physical rows the
// terminal draws, so the height fold counts each of them.
func splitRows(rows ...string) []string {
	out := make([]string, 0, len(rows))
	for _, row := range rows {
		out = append(out, strings.Split(row, "\n")...)
	}
	return out
}

// fitRows joins physical rows into the frame, cutting each one with fitWidth.
func (m *model) fitRows(rows []string) string {
	for i, row := range rows {
		rows[i] = fitWidth(row, m.width)
	}
	return strings.Join(rows, "\n")
}

// fitWidth cuts line to at most width-1 display cells, ending a cut line with
// the ellipsis. Width is measured in terminal cells, not bytes or runes: escape
// sequences count as zero cells and wide runes as two, and the escape sequences
// past the cut are kept so styling is still closed. The last column stays free
// so a full row never triggers the terminal's auto-wrap.
func fitWidth(line string, width int) string {
	limit := width - 1
	if limit < 1 {
		return line
	}
	return ansi.Truncate(line, limit, ellipsis)
}

// formatLog renders a single scrollback line; an empty level degrades to the
// bare text so plain log forwarding stays uncluttered.
func formatLog(level, text string) string {
	if level == "" {
		return text
	}
	return level + ": " + text
}
