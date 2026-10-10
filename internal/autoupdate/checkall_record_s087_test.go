package autoupdate

// Story 087, sub-task 1.1 — R1.1, R1.2, R1.3.
//
// Written from the contract: a (nil, nil) outcome of CheckPackage becomes a
// failure whose message states the check returned no result, a panic while a
// CheckAll worker updates the run's shared state is recorded as
// "panic: <value>" and CheckAll still returns, and record and fail release the
// run's mutex on every exit, the panicking one included.
//
// # Why the run, not CheckAll
//
// CheckPackage is a concrete method and no fixture makes it return (nil, nil)
// today, so these tests drive the run's own recording methods — the shared
// state every CheckAll worker writes through — rather than CheckAll. The panic
// injectors are an error whose Is method panics (record calls errors.Is on the
// outcome while it holds the mutex) and a run whose failures map is nil (the
// only write fail performs).
//
// # Every call is bounded
//
// A deadlock must be a FAILURE, never a hung test binary: each call runs in a
// goroutine and the test fails after s087Deadline through a select.

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
)

// s087Deadline is the 5 s bound R1.2 names.
const s087Deadline = 5 * time.Second

// s087PanicValue is what s087PanickyErr panics with.
const s087PanicValue = "s087 injected panic inside errors.Is"

// s087PanickyErr panics as soon as errors.Is consults it, which happens while
// record holds the run's mutex.
type s087PanickyErr struct{}

func (s087PanickyErr) Error() string { return "s087 panicky error" }

func (s087PanickyErr) Is(error) bool { panic(s087PanicValue) }

// s087NewRun builds a run the way CheckAll does.
func s087NewRun(total int) *checkAllRun {
	return &checkAllRun{
		results:  make([]CheckResult, 0, total),
		failures: make(map[string]error),
		total:    uint64(total),
	}
}

// s087Bounded runs f in a goroutine and returns the value it panicked with (nil
// when it returned normally). It fails the test when f has not finished within
// s087Deadline.
func s087Bounded(t *testing.T, what string, f func()) (panicked any) {
	t.Helper()
	done := make(chan any, 1)
	go func() {
		var p any
		defer func() { done <- p }()
		defer func() { p = recover() }()
		f()
	}()
	timer := time.NewTimer(s087Deadline)
	defer timer.Stop()
	select {
	case p := <-done:
		return p
	case <-timer.C:
		t.Fatalf("%s did not return within %s — the run's mutex is still held (deadlock)", what, s087Deadline)
		return nil
	}
}

// s087AssertMutexFree fails when the run's mutex is still locked.
func s087AssertMutexFree(t *testing.T, run *checkAllRun, after string) {
	t.Helper()
	if !run.mu.TryLock() {
		t.Fatalf("the run's mutex is still locked after %s (R1.3): every later record or fail of this run would block forever", after)
	}
	run.mu.Unlock()
}

// TestS087_1_1_RecordNilResultIsAFailure is R1.1: a (nil, nil) outcome is a
// failure that says the check returned no result, and the run keeps recording
// the packages after it.
func TestS087_1_1_RecordNilResultIsAFailure(t *testing.T) {
	const nilPkg = "cat-nil/pkg-nil"
	run := s087NewRun(3)

	if p := s087Bounded(t, "record of a (nil, nil) outcome", func() { run.record(nilPkg, nil, nil) }); p != nil {
		t.Fatalf("record panicked on a (nil, nil) outcome: %v", p)
	}
	s087AssertMutexFree(t, run, "recording a (nil, nil) outcome")

	err, ok := run.failures[nilPkg]
	if !ok || err == nil {
		t.Fatalf("a (nil, nil) outcome recorded no failure for %q; failures: %v, results: %d", nilPkg, run.failures, len(run.results))
	}
	if !strings.Contains(strings.ToLower(err.Error()), "no result") {
		t.Errorf("the failure for %q is %q; want a message stating the check returned no result", nilPkg, err)
	}
	if len(run.results) != 0 || len(run.orphaned) != 0 {
		t.Errorf("a (nil, nil) outcome also landed as a result (%d) or an orphan (%v)", len(run.results), run.orphaned)
	}

	// The remaining packages are still recorded, each on its own branch.
	const okPkg, badPkg = "cat-ok/pkg-ok", "cat-bad/pkg-bad"
	ordinary := errors.New("s087 ordinary failure")
	if p := s087Bounded(t, "record after a (nil, nil) outcome", func() {
		run.record(okPkg, &CheckResult{Package: okPkg, CurrentVersion: "1.0"}, nil)
		run.record(badPkg, nil, ordinary)
	}); p != nil {
		t.Fatalf("record panicked after a (nil, nil) outcome: %v", p)
	}
	if len(run.results) != 1 || run.results[0].Package != okPkg {
		t.Errorf("results after the (nil, nil) outcome = %+v; want exactly %q", run.results, okPkg)
	}
	if !errors.Is(run.failures[badPkg], ordinary) {
		t.Errorf("failure for %q = %v; want the ordinary error", badPkg, run.failures[badPkg])
	}
	if len(run.failures) != 2 {
		t.Errorf("failures = %v; want exactly %q and %q", run.failures, nilPkg, badPkg)
	}
}

// TestS087_1_1_RecordPanicReleasesTheMutex is R1.3 for record: a panic raised
// while record holds the mutex must not leave it locked.
func TestS087_1_1_RecordPanicReleasesTheMutex(t *testing.T) {
	run := s087NewRun(1)

	p := s087Bounded(t, "record of a panicking error", func() { run.record("cat-p/pkg-p", nil, s087PanickyErr{}) })
	if p == nil {
		// A record that no longer calls into the error is fine; the mutex
		// must be free either way.
		t.Log("record did not panic on the injected error")
	}
	s087AssertMutexFree(t, run, "a panic inside record")
}

// TestS087_1_1_FailPanicReleasesTheMutex is R1.3 for fail: a panic raised
// while fail holds the mutex (here, a write to a nil failures map) must not
// leave it locked.
func TestS087_1_1_FailPanicReleasesTheMutex(t *testing.T) {
	run := &checkAllRun{total: 1} // failures deliberately nil: the write panics

	if p := s087Bounded(t, "fail on a nil failures map", func() { run.fail("cat-f/pkg-f", errors.New("s087")) }); p == nil {
		t.Log("fail did not panic on a nil failures map")
	}
	s087AssertMutexFree(t, run, "a panic inside fail")
}

// TestS087_1_1_WorkerRecoveryAfterRecordPanicReturns is R1.2: the recovery a
// CheckAll worker performs — record the panic value through fail — must
// complete, within the deadline, after a panic inside record, and the package's
// failure must read "panic: <value>".
func TestS087_1_1_WorkerRecoveryAfterRecordPanicReturns(t *testing.T) {
	const pkg = "cat-w/pkg-w"
	run := s087NewRun(1)

	p := s087Bounded(t, "a worker's recovery after a panic inside record", func() {
		defer func() {
			if r := recover(); r != nil {
				run.fail(pkg, fmt.Errorf("panic: %v", r))
			}
		}()
		run.record(pkg, nil, s087PanickyErr{})
	})
	if p != nil {
		t.Fatalf("the recovery itself panicked: %v", p)
	}
	s087AssertMutexFree(t, run, "the worker's recovery")

	err := run.failures[pkg]
	if err == nil {
		t.Fatalf("no failure recorded for %q after a panic inside record; failures: %v", pkg, run.failures)
	}
	if want := "panic: " + s087PanicValue; !strings.Contains(err.Error(), want) {
		t.Errorf("failure for %q = %q; want it to carry %q", pkg, err, want)
	}
	if len(run.orphaned) != 0 || len(run.results) != 0 {
		t.Errorf("a panic was also filed as an orphan or a result: orphaned %v, results %d", run.orphaned, len(run.results))
	}
}
