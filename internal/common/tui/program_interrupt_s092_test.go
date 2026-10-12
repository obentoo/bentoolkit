package tui

// Authored for story 092 (refine v2), sub-task 6.2: an interrupted run — a
// cancelled run context or a Ctrl-C — releases the live region as a normal end
// does (R1.8). The frame left on screen carries the header and no history row,
// PrintHistory then puts each history row on screen exactly once, and a
// program that cannot quit does not hold the exit path.
//
// Driven through NewProgram with an io.Writer output replayed on a minimal
// screen emulator (s092v2Screen) and an io.Pipe stdin.

import (
	"context"
	"fmt"
	"io"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

// s092v2Out is a goroutine-safe output sink. Once block is called, every
// later write waits until unblock — a terminal that stopped draining.
type s092v2Out struct {
	mu      sync.Mutex
	buf     strings.Builder
	blocked bool
	gate    chan struct{}
	once    sync.Once
}

func newS092v2Out() *s092v2Out { return &s092v2Out{gate: make(chan struct{})} }

func (o *s092v2Out) Write(p []byte) (int, error) {
	o.mu.Lock()
	blocked := o.blocked
	o.mu.Unlock()
	if blocked {
		<-o.gate
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	o.buf.Write(p)
	return len(p), nil
}

func (o *s092v2Out) String() string {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.buf.String()
}

func (o *s092v2Out) block() {
	o.mu.Lock()
	o.blocked = true
	o.mu.Unlock()
}

func (o *s092v2Out) unblock() { o.once.Do(func() { close(o.gate) }) }

// s092v2Screen replays a captured terminal stream on a minimal screen and
// returns the rows left visible, so what the inline renderer erased or redrew
// counts as a terminal shows it. It interprets cursor up, carriage return,
// line feed, erase line and erase below, and drops every other escape.
func s092v2Screen(stream string) []string {
	var rows [][]rune
	row, col := 0, 0
	grow := func() {
		for len(rows) <= row {
			rows = append(rows, nil)
		}
	}
	grow()
	rs := []rune(stream)
	for i := 0; i < len(rs); i++ {
		switch r := rs[i]; r {
		case '\r':
			col = 0
		case '\n':
			row, col = row+1, 0
			grow()
		case 0x1b:
			i = s092v2Escape(rs, i, func(final rune, param string) {
				n, err := strconv.Atoi(param)
				switch final {
				case 'A':
					if err != nil || n < 1 {
						n = 1
					}
					row = max(row-n, 0)
				case 'K':
					if param == "2" {
						rows[row] = nil
					} else if col < len(rows[row]) {
						rows[row] = rows[row][:col]
					}
				case 'J':
					if col < len(rows[row]) {
						rows[row] = rows[row][:col]
					}
					rows = rows[:row+1]
				}
			})
		default:
			if r < 0x20 {
				continue
			}
			for len(rows[row]) < col {
				rows[row] = append(rows[row], ' ')
			}
			if col < len(rows[row]) {
				rows[row][col] = r
			} else {
				rows[row] = append(rows[row], r)
			}
			col++
		}
	}
	out := make([]string, len(rows))
	for i, r := range rows {
		out[i] = strings.TrimRight(string(r), " ")
	}
	return out
}

// s092v2Escape consumes the escape sequence at rs[i] and returns the index of
// its last rune, handing a CSI sequence's final rune and parameters to csi.
func s092v2Escape(rs []rune, i int, csi func(final rune, param string)) int {
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
			csi(rs[j], string(rs[i+2:j]))
		}
		return j
	case ']':
		j := i + 2
		for j < len(rs) && rs[j] != 0x07 && rs[j] != 0x1b {
			j++
		}
		if j+1 < len(rs) && rs[j] == 0x1b && rs[j+1] == '\\' {
			j++
		}
		return j
	default:
		return i + 1
	}
}

// s092v2Running is a started program at 40x10 whose live frame shows the
// header, three history rows and a routed log line.
type s092v2Running struct {
	p      *Program
	out    *s092v2Out
	ctx    context.Context
	cancel context.CancelFunc
	stdin  *io.PipeWriter
}

const s092v2Header = "Applying updates  3/3"

func s092v2HistoryRow(i int) string { return fmt.Sprintf("✓ pkg-%02d — ok", i) }

// s092v2Start starts the program and waits until its live frame, history
// included, is on screen, so an interrupt has a drawn region to release.
func s092v2Start(t *testing.T) *s092v2Running {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	inR, inW := io.Pipe()
	out := newS092v2Out()
	t.Cleanup(func() {
		out.unblock()
		cancel()
		_ = inW.Close()
	})
	p := NewProgram(ctx, cancel, newModel(), out, inR)
	p.Start()
	p.Send(tea.WindowSizeMsg{Width: 40, Height: 10})
	for _, msg := range append([]tea.Msg{BatchStartMsg{Total: 3}}, s092History(3)...) {
		p.Send(msg)
	}
	p.Send(LogMsg{Level: "info", Text: "routed-line"})
	deadline := time.Now().Add(5 * time.Second)
	for {
		screen := s092v2Screen(out.String())
		if s092HasRow(screen, s092v2HistoryRow(2)) && s092HasRow(screen, "info: routed-line") {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("fixture: the live frame never reached the screen:\n%s", strings.Join(screen, "\n"))
		}
		time.Sleep(10 * time.Millisecond)
	}
	return &s092v2Running{p: p, out: out, ctx: ctx, cancel: cancel, stdin: inW}
}

// wait runs Wait with a bound and fails the test when it does not return.
func (r *s092v2Running) wait(t *testing.T, bound time.Duration) {
	t.Helper()
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = r.p.Wait()
	}()
	select {
	case <-done:
	case <-time.After(bound):
		t.Fatalf("Wait did not return within %v of the interrupt", bound)
	}
}

// R1.8: cancelling the run context releases the region as a normal end does:
// the frame left on screen keeps the header and the routed log line and
// carries no history row.
func TestProgramInterruptCancelRendersReleasedFrame(t *testing.T) {
	r := s092v2Start(t)
	r.cancel()
	r.wait(t, 10*time.Second)

	screen := s092v2Screen(r.out.String())
	shown := strings.Join(screen, "\n")
	for _, row := range screen {
		if strings.HasPrefix(row, "✓ pkg-") {
			t.Errorf("after a cancel the frame left on screen still holds a history row %q (no release render):\n%s", row, shown)
		}
	}
	if n := s092v2Count(screen, s092v2Header); n != 1 {
		t.Errorf("header row on screen %d times, want once:\n%s", n, shown)
	}
	if !s092HasRow(screen, "info: routed-line") {
		t.Errorf("the released frame lost the routed log line:\n%s", shown)
	}
}

// R1.8: after a cancel, PrintHistory puts every history row on screen exactly
// once — neither lost (zero) nor doubled by a live frame left above it (two).
func TestProgramInterruptCancelPrintsHistoryOnce(t *testing.T) {
	r := s092v2Start(t)
	r.cancel()
	r.wait(t, 10*time.Second)
	if err := r.p.PrintHistory(r.out); err != nil {
		t.Fatalf("PrintHistory: %v", err)
	}

	screen := s092v2Screen(r.out.String())
	shown := strings.Join(screen, "\n")
	for i := 0; i < 3; i++ {
		if n := s092v2Count(screen, s092v2HistoryRow(i)); n != 1 {
			t.Errorf("history row %q on screen %d times after a cancel, want once:\n%s", s092v2HistoryRow(i), n, shown)
		}
	}
	if n := s092v2Count(screen, s092v2Header); n != 1 {
		t.Errorf("header row on screen %d times, want once:\n%s", n, shown)
	}
}

// R1.8: a Ctrl-C typed into the program cancels the run (the injected cancel
// still runs) and releases the region the same way, so the history is on
// screen exactly once after PrintHistory.
func TestProgramInterruptCtrlCPrintsHistoryOnce(t *testing.T) {
	r := s092v2Start(t)
	if _, err := r.stdin.Write([]byte{0x03}); err != nil {
		t.Fatalf("typing Ctrl-C: %v", err)
	}
	r.wait(t, 10*time.Second)
	if r.ctx.Err() == nil {
		t.Errorf("Ctrl-C did not invoke the run's cancel")
	}
	if err := r.p.PrintHistory(r.out); err != nil {
		t.Fatalf("PrintHistory: %v", err)
	}

	screen := s092v2Screen(r.out.String())
	shown := strings.Join(screen, "\n")
	for i := 0; i < 3; i++ {
		if n := s092v2Count(screen, s092v2HistoryRow(i)); n != 1 {
			t.Errorf("history row %q on screen %d times after Ctrl-C, want once:\n%s", s092v2HistoryRow(i), n, shown)
		}
	}
	if n := s092v2Count(screen, s092v2Header); n != 1 {
		t.Errorf("header row on screen %d times, want once:\n%s", n, shown)
	}
}

// R1.8 risk: a program that cannot quit — its terminal stopped taking output —
// does not hold the exit path. After a cancel, the caller's Stop then Wait
// (the sequence the apply driver runs) return within a bound.
func TestProgramInterruptStuckProgramDoesNotBlockWait(t *testing.T) {
	r := s092v2Start(t)
	r.out.block()
	r.cancel()

	done := make(chan struct{})
	start := time.Now()
	go func() {
		defer close(done)
		r.p.Stop()
		_ = r.p.Wait()
	}()
	select {
	case <-done:
		t.Logf("Stop+Wait returned %v after the cancel", time.Since(start))
	case <-time.After(10 * time.Second):
		t.Fatalf("Stop+Wait did not return within 10s of the cancel: a program stuck writing its frame blocks the exit")
	}
}
