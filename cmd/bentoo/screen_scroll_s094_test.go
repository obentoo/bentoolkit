package main

// Authored for story 094, sub-task 1.1: a terminal emulator whose screen has a
// fixed height, so a line feed on the bottom row scrolls the top row into the
// scrollback as a real terminal does. The s092 screen grows without bound and
// so never shows a row the inline renderer left above its frame.

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
	"unsafe"

	"github.com/obentoo/bentoolkit/internal/common/logging"
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

// s094Ioctl issues one ioctl on fd; arg is converted inside the call expression
// as the unsafe.Pointer rules for syscalls require.
func s094Ioctl(fd, req uintptr, arg unsafe.Pointer) error {
	if _, _, errno := syscall.Syscall(syscall.SYS_IOCTL, fd, req, uintptr(arg)); errno != 0 {
		return errno
	}
	return nil
}

// s094OpenPTY opens a pseudo-terminal whose window is rows x cols. The master
// is read through the runtime poller (its Fd is never taken, which would make
// it blocking), so closing it ends a pending read. The test skips where no pty
// can be opened.
func s094OpenPTY(t *testing.T, rows, cols int) (master, slave *os.File) {
	t.Helper()
	m, err := os.OpenFile("/dev/ptmx", os.O_RDWR|syscall.O_NOCTTY, 0)
	if err != nil {
		t.Skipf("no pseudo-terminal here: %v", err)
	}
	raw, err := m.SyscallConn()
	if err != nil {
		t.Fatal(err)
	}
	var ptn uint32
	var ioErr error
	if err := raw.Control(func(fd uintptr) {
		var unlock int32
		if ioErr = s094Ioctl(fd, syscall.TIOCSPTLCK, unsafe.Pointer(&unlock)); ioErr == nil {
			ioErr = s094Ioctl(fd, syscall.TIOCGPTN, unsafe.Pointer(&ptn))
		}
	}); err != nil || ioErr != nil {
		m.Close()
		t.Fatalf("unlock the pty: %v %v", err, ioErr)
	}
	s, err := os.OpenFile("/dev/pts/"+strconv.FormatUint(uint64(ptn), 10), os.O_RDWR|syscall.O_NOCTTY, 0)
	if err != nil {
		m.Close()
		t.Fatalf("open the pty slave: %v", err)
	}
	ws := struct{ row, col, x, y uint16 }{uint16(rows), uint16(cols), 0, 0}
	if err := s094Ioctl(s.Fd(), syscall.TIOCSWINSZ, unsafe.Pointer(&ws)); err != nil {
		m.Close()
		s.Close()
		t.Fatalf("set the pty size: %v", err)
	}
	return m, s
}

// s094RunApplyOnPTY runs runAutoupdate with fds 1 and 2 on a rows x cols
// pseudo-terminal and returns everything written to it. bubbletea finds a
// terminal on its output and reads the window size from it, so the live
// region is drawn for that size; the pty's own output processing (\n to
// \r\n) applies as on a real terminal. Stdin stays a pipe, as in
// s092RunApply.
func s094RunApplyOnPTY(t *testing.T, auOpts *autoupdateOptions, rows, cols int) (code int, out string) {
	t.Helper()
	master, slave := s094OpenPTY(t, rows, cols)
	defer master.Close()
	var buf bytes.Buffer
	readDone := make(chan struct{})
	go func() {
		defer close(readDone)
		// The read ends with EIO once every slave descriptor is closed.
		_, _ = io.Copy(&buf, master)
	}()

	inR, inW, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer inR.Close()
	defer inW.Close()
	origStdin := os.Stdin
	os.Stdin = inR
	defer func() { os.Stdin = origStdin }()
	saved1, err1 := syscall.Dup(1)
	saved2, err2 := syscall.Dup(2)
	if err1 != nil || err2 != nil {
		t.Fatalf("dup: %v %v", err1, err2)
	}
	func() {
		defer func() {
			_ = syscall.Dup3(saved1, 1, 0)
			_ = syscall.Dup3(saved2, 2, 0)
			_ = syscall.Close(saved1)
			_ = syscall.Close(saved2)
		}()
		if err := syscall.Dup3(int(slave.Fd()), 1, 0); err != nil {
			t.Fatal(err)
		}
		if err := syscall.Dup3(int(slave.Fd()), 2, 0); err != nil {
			t.Fatal(err)
		}
		auCmd := testAutoupdateCmd()
		auCmd.SetContext(logging.NewContext(context.Background(), slog.New(slog.NewTextHandler(os.Stderr, nil))))
		code = exitCodeFor(runAutoupdate(auCmd, nil, auOpts, ttyDeps(false, true)))
	}()
	slave.Close()
	select {
	case <-readDone:
	case <-time.After(5 * time.Second):
		// A leftover child still holds the slave: close the master to end the read.
		master.Close()
		<-readDone
	}
	return code, buf.String()
}

// s094LiveFixture is s092ApplyAllFixture with tools whose output moves the
// live region: pkgdev prints wget-style progress rows for a while before it
// fails (the task's tail grows, then a new task starts with an empty one), and
// with --llm and a claude-code provider each manifest failure invokes the
// fixer, which logs to the terminal and routes an info line into the frame
// while claude streams its tool calls.
func s094LiveFixture(t *testing.T, auOpts *autoupdateOptions, n int) []string {
	t.Helper()
	pkgs := s092ApplyAllFixture(t, auOpts, n)
	oLLM := auOpts.llm
	t.Cleanup(func() { auOpts.llm = oLLM })
	auOpts.llm = true

	cfg := filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "bentoo", "config.yaml")
	yaml, err := os.ReadFile(cfg)
	if err != nil {
		t.Fatal(err)
	}
	yaml = append(yaml, "autoupdate:\n  llm:\n    provider: claude-code\n"...)
	if err := os.WriteFile(cfg, yaml, 0o600); err != nil {
		t.Fatal(err)
	}

	toolDir := t.TempDir()
	writeStub(t, toolDir, "pkgdev", `x=`+strings.Repeat("x", 60)+`
i=1
while [ "$i" -le 24 ]; do
  printf '%5dK .......... .......... .......... .......... .......... %3d%% 1.2M 0s %s\n' "$i" "$((i * 4))" "$x"
  i=$((i + 1))
  sleep 0.02
done
echo "!!! Fetched file: q11-1.1.tar.gz VERIFY FAILED!" >&2
exit 1
`)
	writeStub(t, toolDir, "claude", `case "$*" in *--version*) echo "2.1.296 (Claude Code)"; exit 0;; esac
cat >/dev/null 2>&1 &
for f in a.ebuild b.ebuild Manifest; do
  printf '{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Read","input":{"file_path":"/staged/%s"}}]}}\n' "$f"; sleep 0.05
done
printf '{"type":"result","subtype":"success","is_error":false,"result":"tried a fix","total_cost_usd":0.01,"permission_denials":[]}\n'
`)
	for _, tool := range []string{"pkgcheck", "emerge", "ebuild", "egencache", "portageq"} {
		writeStub(t, toolDir, tool, "exit 1\n")
	}
	t.Setenv("PATH", toolDir+":/usr/bin:/bin")
	return pkgs
}

// s094ProgressRow matches what only the live frame draws while a task runs:
// a task row (spinner glyph, label) or a row of a task's tail.
var s094ProgressRow = regexp.MustCompile(`^(?:[|/\\-] dev-go/|\s+\d+K \.{10} )`)

// The live region redraws in place as its height changes: tails grow and
// shrink, packages finish and a routed log line joins the frame, while the
// fixer's own log lines reach the terminal. On a terminal with a fixed height,
// whatever the region leaves behind is in the scrollback or on the screen
// after the run: it must hold the header and each ✗ row exactly once, and no
// row of a task in progress.
func TestLiveRegionNoResidueOnHeightChange(t *testing.T) {
	for _, size := range []struct{ rows, cols int }{{40, 120}, {12, 120}} {
		t.Run(fmt.Sprintf("%dx%d", size.cols, size.rows), func(t *testing.T) {
			auOpts := testAutoupdateOptions()
			pkgs := s094LiveFixture(t, auOpts, 3)
			code, out := s094RunApplyOnPTY(t, auOpts, size.rows, size.cols)
			screen := newS094Screen(size.rows, size.cols)
			screen.Feed(out)
			lines := screen.Lines()
			shown := strings.Join(lines, "\n")
			// Guards: a run that never drew the live region at this width, or
			// never reached the fixer, proves nothing about residue.
			if code != 1 {
				t.Fatalf("a batch of failing bumps exited %d, want 1:\n%s", code, shown)
			}
			if !strings.Contains(out, "invoking LLM fixer") {
				t.Fatalf("the fixture never reached the LLM fixer:\n%s", shown)
			}
			if !strings.Contains(out, "\x1b[?25l") || !strings.Contains(out, "…") {
				t.Fatalf("the live region was not drawn at %d columns:\n%s", size.cols, shown)
			}

			var residue []string
			if n := s094CountRowsContaining(lines, "Applying updates"); n != 1 {
				residue = append(residue, fmt.Sprintf("%d \"Applying updates\" header rows, want 1", n))
			}
			for _, pkg := range pkgs {
				if n := s092CountRowsWithPrefix(lines, "✗ "+pkg+" — "); n != 1 {
					residue = append(residue, fmt.Sprintf("%d ✗ rows for %s, want 1", n, pkg))
				}
			}
			progress := 0
			for _, l := range lines {
				if s094ProgressRow.MatchString(l) {
					progress++
				}
			}
			if progress != 0 {
				residue = append(residue, fmt.Sprintf("%d rows of a task in progress, want 0", progress))
			}
			if len(residue) > 0 {
				var marked []string
				for i, l := range lines {
					if strings.Contains(l, "Applying updates") || strings.HasPrefix(l, "✗ ") || s094ProgressRow.MatchString(l) {
						marked = append(marked, fmt.Sprintf("%4d  %.100s", i, l))
					}
				}
				t.Errorf("live region residue at %dx%d (scrollback + screen, %d rows): %s\nheader, ✗ and progress rows:\n%s",
					size.cols, size.rows, len(lines), strings.Join(residue, "; "), strings.Join(marked, "\n"))
			}
		})
	}
}
