package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"
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

// signalContext derives a context that is cancelled when the process receives
// SIGINT or SIGTERM, so an in-flight command aborts cleanly within ~2 s (R3.1).
//
// OQ-1: cmd.Context() is NOT signal-aware on its own — main.go uses
// rootCmd.Execute() (not ExecuteContext) and never wires signal.NotifyContext.
// Cobra's Execute path guarantees a non-nil context.Background(), but unit
// tests call the run* functions directly with a freshly built command whose
// ctx is still nil; signal.NotifyContext panics on a nil parent. The nil guard
// below falls back to context.Background() so both paths are safe.
//
// The caller MUST defer the returned stop function to release the signal
// handler. This mirrors the signal.NotifyContext pattern used in runManifest
// (AD-1).
func signalContext(parent context.Context) (context.Context, context.CancelFunc) {
	if parent == nil {
		parent = context.Background()
	}
	return signal.NotifyContext(parent, os.Interrupt, syscall.SIGTERM)
}
