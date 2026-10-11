package main

import (
	"os"
	"os/signal"
	"syscall"
	"testing"
	"time"
)

// sendSelf sends sig to the test process and waits until sink has seen it,
// so an assertion after it is not vacuous.
func sendSelf(t *testing.T, sink <-chan os.Signal, sig syscall.Signal) {
	t.Helper()
	if err := syscall.Kill(os.Getpid(), sig); err != nil {
		t.Fatalf("sending %v to the test process: %v", sig, err)
	}
	select {
	case <-sink:
	case <-time.After(time.Second):
		t.Fatalf("%v never reached the test process; the assertions would prove nothing", sig)
	}
}

// signalSink registers a channel for sigs for the test's duration. It proves
// delivery, and keeps a signal the code under test fails to catch from
// killing the test binary.
func signalSink(t *testing.T, sigs ...os.Signal) <-chan os.Signal {
	t.Helper()
	sink := make(chan os.Signal, 1)
	signal.Notify(sink, sigs...)
	t.Cleanup(func() { signal.Stop(sink) })
	return sink
}

// TestSuspendSignalsDetachesTheHandlerUntilResume: while suspended, a SIGINT
// neither cancels a cancellable run nor ends the process, and is not reported;
// after resume the same signal cancels the run as before.
func TestSuspendSignalsDetachesTheHandlerUntilResume(t *testing.T) {
	sink := signalSink(t, os.Interrupt)
	ctx, stop, policy := processContext()
	t.Cleanup(stop)
	policy.cancellable.Store(true)

	resume, ok := policy.suspendSignals()
	if !ok {
		t.Fatal("suspendSignals refused before any signal arrived")
	}
	sendSelf(t, sink, syscall.SIGINT)
	time.Sleep(100 * time.Millisecond)
	if err := ctx.Err(); err != nil {
		t.Fatalf("a SIGINT while suspended cancelled the run: %v", err)
	}
	if got := resume(); got != nil {
		t.Fatalf("resume reported %v; SIGINT belongs to the session and is dropped", got)
	}

	sendSelf(t, sink, syscall.SIGINT)
	select {
	case <-ctx.Done():
	case <-time.After(time.Second):
		t.Fatal("a SIGINT after resume did not cancel the run: the handler was not restored")
	}
}

// TestSuspendSignalsRemembersTermAndHangup: a SIGTERM or SIGHUP while
// suspended neither cancels the run nor ends the process, and resume reports
// the first one so the caller can stop.
func TestSuspendSignalsRemembersTermAndHangup(t *testing.T) {
	for _, sig := range []syscall.Signal{syscall.SIGTERM, syscall.SIGHUP} {
		t.Run(sig.String(), func(t *testing.T) {
			sink := signalSink(t, sig)
			ctx, stop, policy := processContext()
			t.Cleanup(stop)
			policy.cancellable.Store(true)

			resume, ok := policy.suspendSignals()
			if !ok {
				t.Fatal("suspendSignals refused before any signal arrived")
			}
			sendSelf(t, sink, sig)
			time.Sleep(100 * time.Millisecond)
			if err := ctx.Err(); err != nil {
				t.Fatalf("%v while suspended cancelled the run: %v", sig, err)
			}
			if got := resume(); got != sig {
				t.Fatalf("resume reported %v, want %v", got, sig)
			}
		})
	}
}

// TestSuspendSignalsRefusesAfterTheHandlerFired: once the handler has taken
// its signal the run is being cancelled, so no suspension (and no session)
// may start.
func TestSuspendSignalsRefusesAfterTheHandlerFired(t *testing.T) {
	sink := signalSink(t, os.Interrupt)
	ctx, stop, policy := processContext()
	t.Cleanup(stop)
	policy.cancellable.Store(true)

	sendSelf(t, sink, syscall.SIGINT)
	select {
	case <-ctx.Done():
	case <-time.After(time.Second):
		t.Fatal("the handler never cancelled the run")
	}
	resume, ok := policy.suspendSignals()
	if ok {
		t.Fatal("suspendSignals accepted a suspension after the handler had fired")
	}
	if got := resume(); got != nil {
		t.Fatalf("a refused suspension's resume reported %v", got)
	}
}

// TestSuspendSignalsWithoutAPolicyStillDropsSignals: a run without func
// runMain has no handler to detach; the suspension alone keeps SIGINT from
// taking its default action.
func TestSuspendSignalsWithoutAPolicyStillDropsSignals(t *testing.T) {
	var policy *signalPolicy
	resume, ok := policy.suspendSignals()
	if !ok {
		t.Fatal("a nil policy refused the suspension")
	}
	if err := syscall.Kill(os.Getpid(), syscall.SIGINT); err != nil {
		t.Fatalf("sending SIGINT to the test process: %v", err)
	}
	time.Sleep(100 * time.Millisecond)
	if got := resume(); got != nil {
		t.Fatalf("resume reported %v for a SIGINT", got)
	}
}
