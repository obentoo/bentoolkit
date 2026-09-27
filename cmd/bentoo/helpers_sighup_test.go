//go:build unix

package main

import (
	"context"
	"errors"
	"os"
	"os/signal"
	"syscall"
	"testing"
	"time"
)

// TestSignalContextCancelsOnSIGHUP pins story 054 R4.4: a terminal hang-up
// cancels a signalContext context exactly as SIGINT and SIGTERM do.
//
// The kernel delivers SIGHUP to the terminal's foreground group only. The
// children bentoo runs in their own process group (claude, pkgdev, the
// unprivileged ebuild) no longer receive it, so when an SSH session drops,
// bentoo's own cancel is what stops them — without it bentoo dies of the
// hang-up and leaves them running.
func TestSignalContextCancelsOnSIGHUP(t *testing.T) {
	// The sink makes a regression FAIL this test instead of killing the test
	// binary. With no handler registered for SIGHUP its default action ends the
	// process, and the package's run would die with "signal: hangup" naming no
	// test. A channel of our own registered BEFORE the signal is sent keeps the
	// process alive whatever signalContext listens for, so the assertion on its
	// context — not the default action — decides the outcome. The sink also
	// proves the signal was actually delivered.
	sink := make(chan os.Signal, 1)
	signal.Notify(sink, syscall.SIGHUP)
	t.Cleanup(func() { signal.Stop(sink) })

	ctx, stop := signalContext(context.Background())
	t.Cleanup(stop)

	if err := ctx.Err(); err != nil {
		t.Fatalf("signalContext returned a context that was already done before any signal: %v", err)
	}

	if err := syscall.Kill(os.Getpid(), syscall.SIGHUP); err != nil {
		t.Fatalf("sending SIGHUP to the test process: %v", err)
	}

	select {
	case <-sink:
	case <-time.After(time.Second):
		t.Fatal("the SIGHUP never reached the test process; the assertion below would prove nothing")
	}

	select {
	case <-ctx.Done():
	case <-time.After(time.Second):
		t.Fatal("signalContext's context was not cancelled within 1 s of SIGHUP (R4.4)")
	}
	if !errors.Is(ctx.Err(), context.Canceled) {
		t.Errorf("ctx.Err() = %v after SIGHUP, want %v", ctx.Err(), context.Canceled)
	}
}
