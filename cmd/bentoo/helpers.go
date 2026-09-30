package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"

	"github.com/obentoo/bentoolkit/internal/common/report/render"
	"github.com/spf13/cobra"
)

// validateGitPathArgs rejects arguments that look like git flags.
// Only file/directory paths are accepted as positional arguments.
func validateGitPathArgs(args []string) error {
	for _, arg := range args {
		if strings.HasPrefix(arg, "-") {
			return fmt.Errorf("invalid argument %q: only file paths are accepted as positional args", arg)
		}
	}
	return nil
}

// cancellableAnnotation marks a command whose handler watches its context and
// ends on its own cancelled path when the first signal arrives. A command
// without it keeps the default action of SIGINT, SIGTERM and SIGHUP.
const cancellableAnnotation = "bentoo/cancellable"

// processSignals are the signals the process's one handler listens for.
var processSignals = []os.Signal{os.Interrupt, syscall.SIGTERM, syscall.SIGHUP}

// signalPolicy records whether the running command handles cancellation. It
// starts not cancellable, so a signal that lands before cobra has selected the
// command takes its default action.
type signalPolicy struct{ cancellable atomic.Bool }

// setFrom sets the policy from the command cobra selected: cancellable when it
// carries Annotations["bentoo/cancellable"] == "true".
func (p *signalPolicy) setFrom(cmd *cobra.Command) {
	p.cancellable.Store(cmd.Annotations[cancellableAnnotation] == "true")
}

// signalPolicyKey is the unexported context key under which func runMain hands
// the policy to the tree, so the root's PersistentPreRunE reaches it without a
// package variable.
type signalPolicyKey struct{}

// withSignalPolicy returns ctx carrying policy.
func withSignalPolicy(ctx context.Context, policy *signalPolicy) context.Context {
	return context.WithValue(ctx, signalPolicyKey{}, policy)
}

// applySignalPolicy sets the policy carried by cmd's context from cmd. A tree
// executed without func runMain (a unit test calling func execute) carries no
// policy, and then it does nothing. It takes the command rather than its
// context so the root's hook passes no context along — contextcheck would
// otherwise ask newRootCmd for a context parameter it has no use for.
func applySignalPolicy(cmd *cobra.Command) {
	if policy, ok := commandContext(cmd).Value(signalPolicyKey{}).(*signalPolicy); ok {
		policy.setFrom(cmd)
	}
}

// yieldSignalsToPrompt hands the next SIGINT, SIGTERM or SIGHUP its default
// action and returns a function that restores the command's own policy. A
// prompt blocked on stdin does not watch the context, so on a cancellable
// command the first Ctrl+C would only cancel a context nobody is reading and
// the prompt would keep waiting. Before story 058 one Ctrl+C at `overlay
// commit`'s and `overlay analyze`'s prompts ended the command, and this keeps
// it so. A command running without a policy (a direct handler call in a test)
// gets a no-op.
func yieldSignalsToPrompt(cmd *cobra.Command) (restore func()) {
	policy, ok := commandContext(cmd).Value(signalPolicyKey{}).(*signalPolicy)
	if !ok {
		return func() {}
	}
	was := policy.cancellable.Swap(false)
	return func() { policy.cancellable.Store(was) }
}

// processContext registers the process's one signal handler. It returns a
// context the first signal cancels while a cancellable command runs, a stop
// function that unregisters the handler, and the policy the root's
// PersistentPreRunE sets from the selected command.
//
// On the first SIGINT, SIGTERM or SIGHUP the handler first calls signal.Reset
// of the three: while signal.Notify holds a signal its default action is
// disabled, so without the reset a command that never looks at its context
// could not be stopped at all, and a second Ctrl+C could not get the operator
// out. Then, when the policy says the running command is cancellable, it
// cancels the context and the command ends on its own cancelled path.
// Otherwise it re-raises the same signal to the process, which now takes its
// default action — exactly what a command that registered no handler got. If
// the re-raise fails, it falls back to cancelling the context rather than
// dropping the signal. signal.NotifyContext cannot express this policy: it
// always cancels and never re-raises.
//
// SIGHUP is here because of process groups (story 054, R4.4). The kernel sends
// a terminal's Ctrl+C and its hang-up — an SSH session dropping, a terminal
// window closed — to the terminal's FOREGROUND group only, and the children
// bentoo runs in their own group (the `claude` CLI, pkgdev, the unprivileged
// ebuild) are outside it. They receive neither signal, so this context is what
// stops them: without SIGHUP in the list a hang-up killed bentoo by its default
// action and left those children running, orphaned. Catching it turns the
// hang-up into the same clean cancel as a Ctrl+C.
//
// stop unregisters the handler, cancels the context and waits for the
// handler's goroutine to return, so no goroutine outlives the caller. Calling
// it more than once is harmless.
func processContext() (context.Context, context.CancelFunc, *signalPolicy) {
	policy := &signalPolicy{}
	ctx, cancel := context.WithCancel(context.Background())

	ch := make(chan os.Signal, 1)
	signal.Notify(ch, processSignals...)

	done := make(chan struct{})
	finished := make(chan struct{})
	go func() {
		defer close(finished)
		var sig os.Signal
		select {
		case sig = <-ch:
		case <-done:
			return
		}
		// Restore the default action first: a second signal, or the
		// re-raised one below, must be able to end the process.
		signal.Reset(processSignals...)
		if policy.cancellable.Load() || !reraise(sig) {
			cancel()
			return
		}
		// The re-raised signal is delivered asynchronously; wait for it (or
		// for stop) rather than race the process's own exit path.
		<-done
	}()

	var once sync.Once
	stop := func() {
		once.Do(func() {
			signal.Stop(ch)
			cancel()
			close(done)
			<-finished
		})
	}
	return ctx, stop, policy
}

// reraise sends sig to this process again, reporting whether it was sent.
func reraise(sig os.Signal) bool {
	s, ok := sig.(syscall.Signal)
	if !ok {
		return false
	}
	return syscall.Kill(os.Getpid(), s) == nil
}

// commandContext is cmd.Context(), or context.Background() when the command
// was never executed: cobra returns nil then (unit tests that call a handler
// directly), and signal.NotifyContext panics on a nil parent.
func commandContext(cmd *cobra.Command) context.Context {
	if ctx := cmd.Context(); ctx != nil {
		return ctx
	}
	return context.Background()
}

// padColumn lays value into a column that is cells display columns wide, so the
// column after it starts in the same place on every row.
//
// It is the other half of render.ColumnWidth, and the two are always used
// together: the first says how wide a column has to be to hold the values THIS
// run produced, this one puts one value into that column. A caller that
// measured a width and then padded by some other rule would have measured
// nothing.
//
// # Why not %-*s, which fmt already offers
//
// Because the two would be counting different things. fmt pads to a RUNE count
// (fmt/format.go pads with utf8.RuneCount), and a column here is measured in
// display CELLS — the only unit a terminal aligns on. "→" is three bytes, one
// rune and one cell; "日" is three bytes, one rune and TWO cells. The two units
// agree on every ASCII value and part company on the first one that is not,
// which is a misalignment that appears in someone's package list and in no test
// that was written before it. Measuring the value with the same function that
// measured the column removes the second unit entirely (R6.1).
//
// A column holding a single value is exactly as wide as that value, so
// render.ColumnWidth([]string{value}) IS its cell width — computed by the code
// that sized the column it is being laid into, so the two cannot disagree.
// internal/common/report/render keeps its own unexported pad for this reason
// and states it the same way; this is that function on the command side of the
// boundary, where the report's renderers cannot be called because these
// commands print their own text.
//
// # It pads and never cuts
//
// A value at or over the width comes back byte for byte. Cutting is
// render.Shorten's job and a separate decision: it needs a budget to cut
// AGAINST — the width of the terminal — and a column measured from its own
// values has no opinion about that. A caller that acquires such a budget calls
// Shorten before this, not instead of it.
func padColumn(value string, cells int) string {
	if missing := cells - render.ColumnWidth([]string{value}); missing > 0 {
		return value + strings.Repeat(" ", missing)
	}
	return value
}
