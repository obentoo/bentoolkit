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
// the prompt would keep waiting. One Ctrl+C at `overlay commit`'s and
// `overlay analyze`'s prompts ended the command before this handler existed,
// and this keeps it so. A command running without a policy (a direct handler call in a test)
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
// function, and the policy the root's PersistentPreRunE sets.
//
// On the first SIGINT, SIGTERM or SIGHUP the handler calls signal.Reset of the
// three (signal.Notify disables a held signal's default action, so a second
// Ctrl+C must still get the operator out). A cancellable command then gets its
// context cancelled; any other command gets the signal re-raised, taking its
// default action, or a cancel if the re-raise fails. signal.NotifyContext
// cannot express this: it always cancels and never re-raises.
//
// SIGHUP is caught because the kernel sends a terminal's Ctrl+C and hang-up to
// its FOREGROUND group only, and the children bentoo runs in their own group
// (`claude`, pkgdev, the unprivileged ebuild) receive neither: without it a
// hang-up killed bentoo and left them orphaned.
//
// stop unregisters the handler, cancels the context and waits for the
// handler's goroutine, so none outlives the caller. It is safe to call twice.
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
// column after it starts in the same place on every row. It is the other half
// of render.ColumnWidth: that one sizes the column from THIS run's values, this
// one fills it.
//
// It does not use %-*s because fmt pads to a RUNE count and a terminal aligns
// on display CELLS: "日" is one rune and two cells. The units agree on ASCII and
// part company on the first value that is not. Measuring the value with
// render.ColumnWidth([]string{value}), the function that sized the column,
// leaves one unit only. internal/common/report/render keeps its own unexported
// pad for the same reason; this is its twin for commands that print their own
// text.
//
// It pads and never cuts: a value at or over the width comes back byte for
// byte. Cutting needs a terminal-width budget and is render.Shorten's job,
// called before this, not instead of it.
func padColumn(value string, cells int) string {
	if missing := cells - render.ColumnWidth([]string{value}); missing > 0 {
		return value + strings.Repeat(" ", missing)
	}
	return value
}
