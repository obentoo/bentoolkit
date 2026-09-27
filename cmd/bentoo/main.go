package main

import (
	"fmt"
	"os"
	"sync"
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

// osExit is a variable so tests can replace it to avoid process termination.
// In production it is exitProcess, so a mode that ends through osExit still
// runs the cleanups it registered — os.Exit alone skips every deferred call.
var osExit = exitProcess

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
// more than once is harmless. A cleanup must not call osExit itself.
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

// rootCmd is the process's own command tree: one call to the constructor that
// can build any number of them.
var rootCmd = newRootCmd()

func main() {
	if err := rootCmd.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		osExit(1)
	}
}
