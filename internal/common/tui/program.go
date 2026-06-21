package tui

import (
	"context"
	"io"
	"sync"

	tea "github.com/charmbracelet/bubbletea"
)

// Program is a thin wrapper over a *tea.Program that owns the run goroutine and
// exposes a small Start/Wait/Stop lifecycle plus Send and the terminal
// release/restore passthroughs (§4.1).
//
// SIGINT is reconciled, not double-owned (AD9): the program is bound to the
// caller's context via tea.WithContext and is created with WithoutSignalHandler
// so Bubble Tea installs no competing signal handler. Ctrl-C therefore reaches
// the model as a key message, and the model invokes the injected cancel — the
// caller's existing cancellation chain — before quitting.
type Program struct {
	prog  *tea.Program
	model *model

	startOnce sync.Once
	done      chan struct{}
	runErr    error
}

// NewProgram builds the wrapper. It injects cancel into the model so Ctrl-C
// triggers the caller's cancellation, and binds the program to ctx so an
// external cancellation (the caller's signal chain) tears the UI down. Output
// and input are explicit so callers can drive the program in tests or against a
// specific terminal; WithoutSignalHandler keeps SIGINT ownership with the
// caller (AD9).
func NewProgram(ctx context.Context, cancel func(), m *model, out io.Writer, in io.Reader) *Program {
	m.withCancel(cancel)
	prog := tea.NewProgram(
		m,
		tea.WithContext(ctx),
		tea.WithOutput(out),
		tea.WithInput(in),
		tea.WithoutSignalHandler(),
	)
	return &Program{
		prog:  prog,
		model: m,
		done:  make(chan struct{}),
	}
}

// Start runs the program in a background goroutine. It is guarded so repeated
// calls are no-ops; the run error is stored before done is closed so Wait reads
// it race-free.
func (p *Program) Start() {
	p.startOnce.Do(func() {
		go func() {
			_, err := p.prog.Run()
			p.runErr = err // set before signalling completion (happens-before close)
			close(p.done)
		}()
	})
}

// Wait blocks until the program goroutine finishes and returns its run error.
// Reading runErr only after done is closed pairs with the write in Start, so the
// handshake is race-free. Calling Wait without a preceding Start would block
// forever; callers always Start first.
func (p *Program) Wait() error {
	<-p.done
	return p.runErr
}

// Stop signals the program to quit gracefully. It is non-blocking and safe to
// call before Start or multiple times; pair it with Wait to block until the
// program has actually exited.
func (p *Program) Stop() {
	p.prog.Quit()
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
