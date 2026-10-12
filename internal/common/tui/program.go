package tui

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sync"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

// releaseGrace is how long Wait lets a released program render its final frame
// and exit before it kills it; killGrace is how long it then waits for the kill
// to land before giving up on a program whose renderer is stuck writing.
const (
	releaseGrace = 2 * time.Second
	killGrace    = time.Second
)

// errProgramStuck is what Wait returns when it gave up on a program that
// neither exited after the release nor after the kill.
var errProgramStuck = errors.New("the live region did not exit after it was released and killed")

// Program is a thin wrapper over a *tea.Program that owns the run goroutine and
// exposes a small Start/Wait/Stop lifecycle plus Send and the terminal
// release/restore passthroughs (§4.1).
//
// SIGINT is reconciled, not double-owned (AD9): the program is created with
// WithoutSignalHandler so Bubble Tea installs no competing signal handler.
// Ctrl-C reaches the model as a key message, and the model invokes the injected
// cancel — the caller's existing cancellation chain — before quitting. The
// program is NOT bound to the caller's context: bubbletea kills a program whose
// context ends, which skips the final render and leaves the last live frame on
// screen. A watcher releases the region instead, as Stop does.
type Program struct {
	prog  *tea.Program
	model *model
	// ctxDone is the run context's Done channel: its close releases the region.
	ctxDone <-chan struct{}

	startOnce sync.Once
	done      chan struct{}
	runErr    error

	stopOnce sync.Once
	stop     chan struct{}

	abandonOnce sync.Once
	abandoned   chan struct{}
}

// NewProgram builds the wrapper. It injects cancel into the model so Ctrl-C
// triggers the caller's cancellation, and keeps ctx so an external
// cancellation (the caller's signal chain) releases the live region. Output
// and input are explicit so callers can drive the program in tests or against a
// specific terminal; WithoutSignalHandler keeps SIGINT ownership with the
// caller (AD9).
func NewProgram(ctx context.Context, cancel func(), m *model, out io.Writer, in io.Reader) *Program {
	m.withCancel(cancel)
	prog := tea.NewProgram(
		m,
		tea.WithOutput(out),
		tea.WithInput(in),
		tea.WithoutSignalHandler(),
	)
	return &Program{
		prog:      prog,
		model:     m,
		ctxDone:   ctx.Done(),
		done:      make(chan struct{}),
		stop:      make(chan struct{}),
		abandoned: make(chan struct{}),
	}
}

// Start runs the program in a background goroutine, plus a watcher that
// releases the live region once the run context ends or Stop is called. It is
// guarded so repeated calls are no-ops; the run error is stored before done is
// closed so Wait reads it race-free. The watcher exits with the program.
func (p *Program) Start() {
	p.startOnce.Do(func() {
		go func() {
			_, err := p.prog.Run()
			p.runErr = err // set before signalling completion (happens-before close)
			close(p.done)
		}()
		go p.watchRelease()
	})
}

// watchRelease sends the release once the run context ends or Stop is called.
// The send itself returns once the program has exited or been killed, so a
// program that cannot take the message does not hold this goroutine forever.
func (p *Program) watchRelease() {
	select {
	case <-p.ctxDone:
	case <-p.stop:
	case <-p.done:
		return
	}
	p.prog.Send(releaseMsg{})
}

// Wait blocks until the program goroutine finishes and returns its run error.
// Reading runErr only after done is closed pairs with the write in Start, so the
// handshake is race-free. A program that exits on its own returns at once.
// Once a release was asked for (Stop or the run context ending), the wait is
// bounded: after releaseGrace the program is killed (Run then reports
// tea.ErrProgramKilled), and after killGrace more Wait gives up and returns
// errProgramStuck — a renderer blocked on a terminal write cannot be ended.
func (p *Program) Wait() error {
	select {
	case <-p.done:
		return p.runErr
	case <-p.stop:
	case <-p.ctxDone:
	}
	if p.exitedWithin(releaseGrace) {
		return p.runErr
	}
	p.prog.Kill()
	if p.exitedWithin(killGrace) {
		return p.runErr
	}
	p.abandonOnce.Do(func() { close(p.abandoned) })
	return errProgramStuck
}

// exitedWithin reports whether the program goroutine finished within d.
func (p *Program) exitedWithin(d time.Duration) bool {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-p.done:
		return true
	case <-timer.C:
		return false
	}
}

// Stop asks the program to release the live region and quit. It never blocks
// and is safe to call before Start or multiple times; pair it with Wait to
// block until the program has actually exited. The model drops its history
// rows from the frame it leaves on screen, so PrintHistory is what puts them in
// the scrollback.
func (p *Program) Stop() {
	p.stopOnce.Do(func() { close(p.stop) })
}

// PrintHistory writes every finished task's row to w, oldest first, one per
// line, unfolded and not cut to the terminal width. It waits for the program
// to exit first, so it prints below the released live region; like Wait, it
// must follow Start. When Wait gave up on a stuck program, it prints without
// waiting for that program any longer.
func (p *Program) PrintHistory(w io.Writer) error {
	select {
	case <-p.done:
	case <-p.abandoned:
	}
	for _, line := range p.model.historyLines() {
		if _, err := io.WriteString(w, line+"\n"); err != nil {
			return fmt.Errorf("print the run history: %w", err)
		}
	}
	return nil
}

// Send forwards a message to the running program. This is the documented
// goroutine-safe ingress teaReporter uses to deliver event messages.
func (p *Program) Send(msg tea.Msg) {
	p.prog.Send(msg)
}

// ReleaseTerminal relinquishes the terminal so a caller can write to it directly
// (passthrough to the underlying program).
func (p *Program) ReleaseTerminal() error {
	return p.prog.ReleaseTerminal()
}

// RestoreTerminal reclaims the terminal after a ReleaseTerminal (passthrough to
// the underlying program).
func (p *Program) RestoreTerminal() error {
	return p.prog.RestoreTerminal()
}
