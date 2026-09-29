package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"sync"

	"github.com/spf13/cobra"
)

// verbose, quiet and noColor carry the root's persistent flags to the run
// functions that read them (overlay_compare.go, overlay_autoupdate.go). They are
// written once per run, by the root's PersistentPreRun in root.go, and never
// bound directly to a flag — see newRootCmd for why that distinction matters.
var (
	verbose bool
	quiet   bool
	noColor bool
)

// exitCleanups holds the cleanups registered with registerExitCleanup, in
// registration order. Each carries its own id so an unregister removes exactly
// its own entry, even when two cleanups are indistinguishable.
var (
	exitCleanupsMu  sync.Mutex
	exitCleanups    []exitCleanup
	exitCleanupNext uint64
)

type exitCleanup struct {
	id uint64
	fn func()
}

// registerExitCleanup arranges for fn to run when the process ends through
// exitProcess, and returns a func that cancels the registration; calling it
// more than once is harmless. A cleanup must not call exitProcess itself.
func registerExitCleanup(fn func()) (unregister func()) {
	exitCleanupsMu.Lock()
	defer exitCleanupsMu.Unlock()
	exitCleanupNext++
	id := exitCleanupNext
	exitCleanups = append(exitCleanups, exitCleanup{id: id, fn: fn})
	return func() {
		exitCleanupsMu.Lock()
		defer exitCleanupsMu.Unlock()
		for i, c := range exitCleanups {
			if c.id == id {
				exitCleanups = append(exitCleanups[:i], exitCleanups[i+1:]...)
				return
			}
		}
	}
}

// exitProcess runs the registered cleanups, last registered first, then ends
// the process with code. The list is taken and cleared before any cleanup
// runs, so none of them can run twice.
func exitProcess(code int) {
	exitCleanupsMu.Lock()
	cleanups := exitCleanups
	exitCleanups = nil
	exitCleanupsMu.Unlock()
	for i := len(cleanups) - 1; i >= 0; i-- {
		cleanups[i].fn()
	}
	os.Exit(code)
}

// exitStatus is an error that carries the process exit code a command chose.
// The command already printed its own diagnostic, so the status is silent:
// func execute prints nothing for a bare one. It deliberately has no Unwrap —
// a silent status carries no cause.
type exitStatus struct {
	code int
}

// Error returns "exit status <code>". It is only ever printed when a caller
// wrapped the status in words of its own, and then as part of that wrapper.
func (e *exitStatus) Error() string {
	return fmt.Sprintf("exit status %d", e.code)
}

// exitWith reports a code whose diagnostic the command already printed.
// exitWith(0) returns nil, so a success path stays a nil error and no caller
// testing err != nil can mistake it for a failure.
func exitWith(code int) error {
	if code == 0 {
		return nil
	}
	return &exitStatus{code: code}
}

// exitCodeFor maps a command's returned error to the process exit code:
// nil is 0; an *exitStatus anywhere in the chain is its code, so a status
// wrapped by fmt.Errorf("...: %w") or joined by errors.Join keeps it; any
// other error is 1.
func exitCodeFor(err error) int {
	if err == nil {
		return 0
	}
	var st *exitStatus
	if errors.As(err, &st) {
		return st.code
	}
	return 1
}

// execute runs root under ctx, prints the diagnostic at most once to stderr,
// and returns the exit code. It never ends the process.
//
// It is the only printer of a returned error: it relies on root having
// SilenceErrors set (func newRootCmd does), since cobra consults the root's
// field whichever command ran, and otherwise every error would print twice.
func execute(ctx context.Context, root *cobra.Command, stderr io.Writer) int {
	err := root.ExecuteContext(ctx)
	if err == nil {
		return 0
	}
	// A bare silent status is not printed: its diagnostic is already out. The
	// question is whether the returned value itself IS the status, not whether
	// the chain contains one — a wrapper added words nobody printed yet.
	if _, bare := err.(*exitStatus); !bare { //nolint:errorlint // identity of the returned value, not the chain, decides silence
		// A failed write to stderr has nowhere else to be reported, and the
		// exit code is returned regardless, so the write's result is dropped.
		_, _ = fmt.Fprintln(stderr, err)
	}
	return exitCodeFor(err)
}

// rootCmd is the process's own command tree: one call to the constructor that
// can build any number of them.
var rootCmd = newRootCmd()

// runMain installs the process-wide signal context, executes root, releases
// the signal handler and returns the exit code. The policy travels to the
// root's PersistentPreRunE inside the context, which sets it from the selected
// command.
func runMain(root *cobra.Command) int {
	ctx, stop, policy := processContext()
	defer stop()
	return execute(withSignalPolicy(ctx, policy), root, os.Stderr)
}

// main is the process's single exit: every handler returns, its deferred calls
// run, and only then does exitProcess end the process with the mapped code.
func main() {
	exitProcess(runMain(rootCmd))
}
