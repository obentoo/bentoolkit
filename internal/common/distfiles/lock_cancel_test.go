package distfiles

import (
	"context"
	"errors"
	"io/fs"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Story 054, R2.4 / R2.5: LockFetch(ctx, distdir, names) stops waiting when ctx
// is done. The holder is a SEPARATE process (re-exec helper), as in production.

func TestLockFetchAbortsOnCancel(t *testing.T) {
	distdir := t.TempDir()
	const held = "a.tar.gz"
	// "0-first" sorts and is listed before the held name: whatever order the
	// call takes locks in, it owns this one while it waits on the held one.
	const takenFirst = "0-first.tar.gz"

	holder := startLockHelper(t, distdir, held)
	defer holder.release()
	assertLockFileExists(t, distdir, held, "the helper process holds the lock")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	type result struct {
		lock *FetchLock
		err  error
		at   time.Time
	}
	done := make(chan result, 1)
	go func() {
		l, err := LockFetch(ctx, distdir, []string{takenFirst, held})
		done <- result{l, err, time.Now()}
	}()

	// Cancel while the call is waiting; lockWait (2 min) is left untouched,
	// so only the cancel can end the wait inside this test's bound.
	cancelAt := make(chan time.Time, 1)
	time.AfterFunc(300*time.Millisecond, func() { cancelAt <- time.Now(); cancel() })

	var r result
	select {
	case r = <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("LockFetch still waiting 10 s after the cancel: the wait ignores ctx (B6)")
	}
	if r.err == nil {
		r.lock.Release()
		t.Fatalf("LockFetch succeeded while pid %d holds %q", holder.pid, held)
	}
	if took := r.at.Sub(<-cancelAt); took > 100*time.Millisecond+time.Second {
		t.Errorf("LockFetch returned %v after the cancel, want within 100 ms (+1 s slack) (R2.4)", took)
	}
	if !errors.Is(r.err, context.Canceled) {
		t.Errorf("error %v does not wrap context.Canceled", r.err)
	}
	if msg := r.err.Error(); !strings.Contains(msg, held) || !strings.Contains(msg, distdir) {
		t.Errorf("error %q must name the distfile %q and the distdir %q", msg, held, distdir)
	}
	if r.lock != nil {
		t.Error("a non-nil lock came back with the error")
	}

	// Resulting state: the lock this call had already taken is free again.
	probe, cancelProbe := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancelProbe()
	again, err := LockFetch(probe, distdir, []string{takenFirst})
	if err != nil {
		t.Fatalf("%q is still locked after the cancelled call returned: it leaked a lock it had taken (R2.4): %v", takenFirst, err)
	}
	again.Release()
}

func TestLockFetchPreCancelledCreatesNothing(t *testing.T) {
	distdir := t.TempDir()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	lock, err := LockFetch(ctx, distdir, []string{"a.tar.gz", "b.tar.gz"})
	if err == nil {
		lock.Release()
		t.Fatal("LockFetch succeeded on an already-cancelled context")
	}
	if !errors.Is(err, context.Canceled) {
		t.Errorf("error %v does not wrap context.Canceled (R2.5)", err)
	}
	var created []string
	_ = filepath.WalkDir(distdir, func(p string, d fs.DirEntry, err error) error {
		if err == nil && p != distdir {
			created = append(created, p)
		}
		return nil
	})
	if len(created) != 0 {
		t.Errorf("a pre-cancelled LockFetch created %v; it must create no lock file (R2.5)", created)
	}
}
