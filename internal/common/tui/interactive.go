package tui

import (
	"fmt"
	"os"
	"os/exec"
)

// terminalController is the subset of *Program that RunAttached needs: the
// release/restore passthroughs that hand the real terminal to an interactive
// child and reclaim it afterwards. Narrowing to this interface keeps RunAttached
// testable with a fake (no live program required).
type terminalController interface {
	ReleaseTerminal() error
	RestoreTerminal() error
}

// *Program satisfies terminalController via its passthrough methods.
var _ terminalController = (*Program)(nil)

// RunAttached runs an interactive child command with the real terminal handed
// back to it, so a sudo/doas password prompt is read on the actual TTY rather
// than swallowed by the running program (R4.1, AD5).
//
// The terminal is released before the child starts and restored afterwards.
// Restore is best-effort: it runs via defer so the terminal is reclaimed even
// when the child fails. If the release itself fails the child is NOT run and no
// restore is attempted, because the program still owns the terminal.
//
// Each of cmd.Stdin/Stdout/Stderr is wired to the process's real streams only
// when the caller left it nil, so the child owns the TTY for the prompt while a
// caller that pre-set a stream (e.g. to capture output) keeps its choice.
func RunAttached(tc terminalController, cmd *exec.Cmd) error {
	if err := tc.ReleaseTerminal(); err != nil {
		return fmt.Errorf("release terminal: %w", err)
	}
	// Released successfully: always reclaim, even if the child fails. The restore
	// error is intentionally ignored — there is no better recovery than to return
	// the child's result, and swallowing keeps the success path's error unmasked.
	defer func() { _ = tc.RestoreTerminal() }()

	if cmd.Stdin == nil {
		cmd.Stdin = os.Stdin
	}
	if cmd.Stdout == nil {
		cmd.Stdout = os.Stdout
	}
	if cmd.Stderr == nil {
		cmd.Stderr = os.Stderr
	}
	return cmd.Run()
}

// ConfirmMsg asks the model to render an in-UI yes/no prompt and deliver the
// answer on Reply. It is the in-band confirmation path (AD5): the y/N decision
// is a key prompt rendered by the model rather than a read of os.Stdin behind
// the running program. Reply should be a buffered (cap >= 1) channel so the
// model's non-blocking send always lands.
type ConfirmMsg struct {
	Prompt string
	Reply  chan bool
}

// Confirm asks the running program for a yes/no decision and blocks until the
// user answers (R4.2). It sends a ConfirmMsg carrying a buffered reply channel
// so the model can answer without blocking, then waits for that answer.
func (p *Program) Confirm(prompt string) bool {
	reply := make(chan bool, 1)
	p.Send(ConfirmMsg{Prompt: prompt, Reply: reply})
	return <-reply
}
