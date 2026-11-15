package main

// Authored for story 094, sub-task 1.1: a terminal emulator whose screen has a
// fixed height, so a line feed on the bottom row scrolls the top row into the
// scrollback as a real terminal does. The s092 screen grows without bound and
// so never shows a row the inline renderer left above its frame.

import (
	"os"
	"strconv"
	"strings"
	"testing"
)

// s094Screen is a rows x cols terminal screen plus the scrollback above it. It
// interprets what bubbletea's inline renderer and the run's plain output emit:
// printable runes (one cell each, with the xterm deferred wrap at the last
// column), CR, LF (scrolling at the bottom row), BS, TAB, cursor up/down/
// forward/back, cursor column and position, erase in line and erase in
// display. Every other escape sequence (SGR colors, cursor hide/show,
// bracketed paste, OSC strings) changes nothing on the screen and is dropped.
type s094Screen struct {
	rows, cols int
	grid       [][]rune
	scrollback []string
	row, col   int
	// pendingWrap is set once a rune lands in the last column: the cursor stays
	// there, and only the next printable rune wraps to the following row.
	pendingWrap bool
}

func newS094Screen(rows, cols int) *s094Screen {
	s := &s094Screen{rows: rows, cols: cols, grid: make([][]rune, rows)}
	for i := range s.grid {
		s.grid[i] = s094BlankRow(cols)
	}
	return s
}

func s094BlankRow(cols int) []rune {
	r := make([]rune, cols)
	for i := range r {
		r[i] = ' '
	}
	return r
}

// lineFeed moves the cursor one row down; on the bottom row it scrolls the
// whole screen up instead and the top row joins the scrollback.
func (s *s094Screen) lineFeed() {
	s.pendingWrap = false
	if s.row < s.rows-1 {
		s.row++
		return
	}
	s.scrollback = append(s.scrollback, strings.TrimRight(string(s.grid[0]), " "))
	copy(s.grid, s.grid[1:])
	s.grid[s.rows-1] = s094BlankRow(s.cols)
}

func (s *s094Screen) put(r rune) {
	if s.pendingWrap {
		s.col = 0
		s.lineFeed()
	}
	s.grid[s.row][s.col] = r
	if s.col == s.cols-1 {
		s.pendingWrap = true
		return
	}
	s.col++
}

func (s *s094Screen) clearRow(row, from, to int) {
	for c := max(from, 0); c < min(to, s.cols); c++ {
		s.grid[row][c] = ' '
	}
}

// Feed interprets stream as bytes written to the terminal.
func (s *s094Screen) Feed(stream string) {
	rs := []rune(stream)
	for i := 0; i < len(rs); i++ {
		switch r := rs[i]; {
		case r == '\r':
			s.col, s.pendingWrap = 0, false
		case r == '\n' || r == '\v' || r == '\f':
			s.lineFeed()
		case r == '\b':
			s.col, s.pendingWrap = max(s.col-1, 0), false
		case r == '\t':
			s.col, s.pendingWrap = min((s.col/8+1)*8, s.cols-1), false
		case r == 0x1b:
			i = s.escape(rs, i)
		case r < 0x20 || r == 0x7f:
			// Other C0 controls (BEL, SO, SI, ...) draw nothing.
		default:
			s.put(r)
		}
	}
}

// escape interprets the escape sequence starting at rs[i] and returns the
// index of its last rune.
func (s *s094Screen) escape(rs []rune, i int) int {
	if i+1 >= len(rs) {
		return i
	}
	switch rs[i+1] {
	case '[':
		j := i + 2
		for j < len(rs) && (rs[j] < 0x40 || rs[j] > 0x7e) {
			j++
		}
		if j < len(rs) {
			s.csi(rs[j], string(rs[i+2:j]))
		}
		return j
	case ']', 'P', '_', '^': // string sequences end at BEL or ST
		j := i + 2
		for j < len(rs) && rs[j] != 0x07 && (rs[j] != 0x1b || j+1 >= len(rs) || rs[j+1] != '\\') {
			j++
		}
		if j < len(rs) && rs[j] == 0x1b {
			j++
		}
		return j
	case 'D': // IND
		s.lineFeed()
	case 'E': // NEL
		s.col = 0
		s.lineFeed()
	case 'M': // RI, never above row 0 (no scroll-down needed by this stream)
		s.row, s.pendingWrap = max(s.row-1, 0), false
	case '(', ')', '*', '+', '#', '%': // charset and similar: one more rune
		return min(i+2, len(rs)-1)
	}
	return i + 1
}

// csi applies one control sequence. A private sequence (a parameter string
// starting with '?', '>' or '=') sets terminal modes and is dropped.
func (s *s094Screen) csi(final rune, param string) {
	if param != "" && strings.ContainsRune("?>=", rune(param[0])) {
		return
	}
	args := strings.Split(param, ";")
	arg := func(k, def int) int {
		if k >= len(args) {
			return def
		}
		n, err := strconv.Atoi(args[k])
		if err != nil || n < 1 {
			return def
		}
		return n
	}
	mode := 0
	if n, err := strconv.Atoi(args[0]); err == nil {
		mode = n
	}
	switch final {
	case 'A': // CUU stops at row 0 of the screen, never enters the scrollback
		s.row = max(s.row-arg(0, 1), 0)
	case 'B':
		s.row = min(s.row+arg(0, 1), s.rows-1)
	case 'C':
		s.col = min(s.col+arg(0, 1), s.cols-1)
	case 'D':
		s.col = max(s.col-arg(0, 1), 0)
	case 'G':
		s.col = min(arg(0, 1), s.cols) - 1
	case 'H', 'f':
		s.row, s.col = min(arg(0, 1), s.rows)-1, min(arg(1, 1), s.cols)-1
	case 'K':
		switch mode {
		case 0:
			s.clearRow(s.row, s.col, s.cols)
		case 1:
			s.clearRow(s.row, 0, s.col+1)
		case 2:
			s.clearRow(s.row, 0, s.cols)
		}
	case 'J':
		switch mode {
		case 0:
			s.clearRow(s.row, s.col, s.cols)
			for r := s.row + 1; r < s.rows; r++ {
				s.clearRow(r, 0, s.cols)
			}
		case 1:
			for r := 0; r < s.row; r++ {
				s.clearRow(r, 0, s.cols)
			}
			s.clearRow(s.row, 0, s.col+1)
		case 2:
			for r := 0; r < s.rows; r++ {
				s.clearRow(r, 0, s.cols)
			}
		case 3:
			s.scrollback = nil
		}
	default:
		// SGR and every other sequence leave the cursor and the cells alone.
		return
	}
	s.pendingWrap = false
}

// Lines returns the scrollback followed by the screen rows, right-trimmed,
// without the empty rows below the last written one.
func (s *s094Screen) Lines() []string {
	out := append([]string(nil), s.scrollback...)
	for _, r := range s.grid {
		out = append(out, strings.TrimRight(string(r), " "))
	}
	for len(out) > 0 && out[len(out)-1] == "" {
		out = out[:len(out)-1]
	}
	return out
}

func s094CountRowsContaining(lines []string, sub string) int {
	n := 0
	for _, l := range lines {
		if strings.Contains(l, sub) {
			n++
		}
	}
	return n
}

// s094ONLCR applies a tty's output translation (\n to \r\n), which a replay
// through `cat` into a terminal pane goes through.
func s094ONLCR(b []byte) string {
	return strings.ReplaceAll(string(b), "\n", "\r\n")
}

func TestScrollScreenLineFeedOnLastRowScrollsIntoScrollback(t *testing.T) {
	s := newS094Screen(3, 10)
	s.Feed("one\r\ntwo\r\nthree\r\nfour")
	if want := []string{"one"}; strings.Join(s.scrollback, "|") != strings.Join(want, "|") {
		t.Fatalf("scrollback = %q, want %q", s.scrollback, want)
	}
	if got, want := strings.Join(s.Lines(), "|"), "one|two|three|four"; got != want {
		t.Fatalf("lines = %q, want %q", got, want)
	}
	if s.row != 2 {
		t.Fatalf("cursor row after the scroll = %d, want the bottom row 2", s.row)
	}
	// A bare LF keeps the column; the next rune lands below the previous one.
	s.Feed("\nX")
	if got := s.Lines(); got[len(got)-1] != "    X" || len(s.scrollback) != 2 {
		t.Fatalf("after a bare LF: lines = %q, scrollback = %q", got, s.scrollback)
	}
}

func TestScrollScreenCursorUpStopsAtRowZero(t *testing.T) {
	s := newS094Screen(3, 10)
	s.Feed("a\r\nb\r\nc\r\nd\x1b[9A\rx")
	if s.row != 0 {
		t.Fatalf("cursor row after CUU 9 from the bottom = %d, want 0", s.row)
	}
	if got, want := strings.Join(s.Lines(), "|"), "a|x|c|d"; got != want {
		t.Fatalf("lines = %q, want %q: CUU must not reach the scrollback row", got, want)
	}
}

func TestScrollScreenDeferredWrapAndErase(t *testing.T) {
	s := newS094Screen(3, 4)
	// Four runes fill the row without wrapping; the CR then returns to its start.
	s.Feed("abcd\rX")
	if got := s.Lines(); len(got) != 1 || got[0] != "Xbcd" {
		t.Fatalf("pending wrap: lines = %q, want [Xbcd]", got)
	}
	s.Feed("\x1b[1m\x1b[?25l\x1b[?2004h\x1b[0m\x1b[K\r\nzz\r\nyy\x1b[1A\r\x1b[J")
	if got, want := strings.Join(s.Lines(), "|"), "X"; got != want {
		t.Fatalf("erase line/below: lines = %q, want %q", got, want)
	}
	s.Feed("\r\x1b[2Aabcde")
	if got, want := strings.Join(s.Lines(), "|"), "abcd|e"; got != want {
		t.Fatalf("wrap on the fifth rune: lines = %q, want %q", got, want)
	}
}

// s094RecordingPath is a raw capture of `bentoo overlay autoupdate --apply all
// --llm` in a 120x40 tmux pane (story 092's manual check Q11). Replayed into a
// fresh 120x40 tmux pane with `cat`, it leaves 9 "Applying updates" rows in the
// scrollback plus the screen; the first byte prefix that leaves a second one
// ends between offsets 537851 and 537985.
const s094RecordingPath = "/tmp/s092q11/bentoo-out.bin"

func TestScrollScreenReplayMatchesTmuxResidue(t *testing.T) {
	data, err := os.ReadFile(s094RecordingPath)
	if os.IsNotExist(err) {
		t.Skipf("%s is absent: the tmux recording exists only on the machine that took it", s094RecordingPath)
	}
	if err != nil {
		t.Fatal(err)
	}
	count := func(n int) int {
		s := newS094Screen(40, 120)
		s.Feed(s094ONLCR(data[:n]))
		return s094CountRowsContaining(s.Lines(), "Applying updates")
	}
	if got := count(len(data)); got != 9 {
		t.Errorf("full replay: %d \"Applying updates\" rows, tmux shows 9", got)
	}
	if got := count(537851); got != 1 {
		t.Errorf("replay up to byte 537851: %d header rows, tmux shows 1", got)
	}
	if got := count(537985); got < 2 {
		t.Errorf("replay up to byte 537985: %d header rows, tmux shows the first residue (2 or more)", got)
	}
}
