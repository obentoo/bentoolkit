package distfiles

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
)

// =============================================================================
// Test harness — why some of this runs in another process
// =============================================================================
//
// R2.4's first named case is "more than one worker or run shares a distdir", and
// the second half of that is two `bentoo` RUNS. A test built on two goroutines
// cannot tell the difference between a real filesystem lock and a sync.Mutex, so
// the exclusivity assertions here re-execute the test binary and let a genuine
// second process hold the lock. The child is selected by an environment variable
// checked in TestMain BEFORE m.Run(), so it never registers as a test and no
// -run filter can accidentally turn it into one.
//
// The filesystem helpers (snapshotTree, assertSameTree, withoutDirSizes,
// dirEntryNames, seedFile) live in quarantine_test.go and cleanup_test.go and
// are shared deliberately — see the note at the top of cleanup_test.go.

const (
	// lockHelperDistdirEnv doubles as the switch: when it is set, this binary
	// is a lock-holding child rather than a test run.
	lockHelperDistdirEnv = "BENTOO_TEST_LOCK_HELPER_DISTDIR"
	// lockHelperNamesEnv carries the distfile names to lock, newline-separated.
	lockHelperNamesEnv = "BENTOO_TEST_LOCK_HELPER_NAMES"
	// lockHelperReadyEnv is the file the child writes its PID into once it
	// actually holds the locks. It is the handshake: the parent must not test
	// contention before the child is genuinely holding, and a sleep would be a
	// guess.
	lockHelperReadyEnv = "BENTOO_TEST_LOCK_HELPER_READY"
)

func TestMain(m *testing.M) {
	if distdir := os.Getenv(lockHelperDistdirEnv); distdir != "" {
		os.Exit(runLockHelperChild(distdir))
	}
	// The second child mode, for the same reason as the first: something that
	// cannot be done inside a running test binary. See probe_test.go.
	if dir := os.Getenv(denyWritesHelperDirEnv); dir != "" {
		os.Exit(runDenyWritesHelperChild(dir))
	}
	os.Exit(m.Run())
}

// runLockHelperChild takes the locks it was asked for, announces that it holds
// them, and then holds them until its stdin is closed. Closing stdin — rather
// than waiting a fixed time — is what lets the parent decide exactly when the
// release happens, so "the lock was released" and "the test got lucky" are not
// the same observation.
func runLockHelperChild(distdir string) int {
	var names []string
	for _, name := range strings.Split(os.Getenv(lockHelperNamesEnv), "\n") {
		if name != "" {
			names = append(names, name)
		}
	}

	lock, err := LockFetch(distdir, names)
	if err != nil {
		fmt.Fprintf(os.Stderr, "lock helper: LockFetch(%q, %v): %v\n", distdir, names, err)
		return 1
	}

	ready := os.Getenv(lockHelperReadyEnv)
	if err := os.WriteFile(ready, []byte(strconv.Itoa(os.Getpid())), 0o600); err != nil {
		fmt.Fprintf(os.Stderr, "lock helper: announce: %v\n", err)
		lock.Release()
		return 1
	}

	// Blocks until the parent closes the pipe.
	_, _ = io.Copy(io.Discard, os.Stdin)
	lock.Release()
	return 0
}

// lockHelper is a live child process holding real locks in a real directory.
type lockHelper struct {
	t      *testing.T
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	pid    int
	reaped bool
}

// startLockHelper spawns a child that holds names in distdir, and does not
// return until the child has confirmed it holds them.
func startLockHelper(t *testing.T, distdir string, names ...string) *lockHelper {
	t.Helper()

	ready := filepath.Join(t.TempDir(), "held-by")
	cmd := exec.Command(os.Args[0])
	cmd.Env = append(os.Environ(),
		lockHelperDistdirEnv+"="+distdir,
		lockHelperNamesEnv+"="+strings.Join(names, "\n"),
		lockHelperReadyEnv+"="+ready,
	)
	// Straight through, so a child that cannot take the lock says why in the
	// test's own output instead of dying silently.
	cmd.Stderr = os.Stderr

	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatalf("lock helper: stdin pipe: %v", err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatalf("lock helper: start: %v", err)
	}

	h := &lockHelper{t: t, cmd: cmd, stdin: stdin}
	t.Cleanup(h.stop)
	h.pid = h.waitUntilHolding(ready, names)
	return h
}

// waitUntilHolding blocks until the child's announcement appears, and fails the
// test rather than continuing without it — a parent that tests contention
// against a child that is not holding anything proves nothing at all.
func (h *lockHelper) waitUntilHolding(ready string, names []string) int {
	h.t.Helper()

	deadline := time.Now().Add(20 * time.Second)
	for {
		data, err := os.ReadFile(ready)
		if err == nil {
			pid, convErr := strconv.Atoi(strings.TrimSpace(string(data)))
			if convErr != nil {
				h.t.Fatalf("lock helper announced %q, which is not a pid: %v", data, convErr)
			}
			if pid == os.Getpid() {
				h.t.Fatalf("lock helper reported OUR pid (%d): the child is not a separate process, so this test proves nothing about two runs", pid)
			}
			return pid
		}
		if time.Now().After(deadline) {
			h.t.Fatalf("lock helper never reported holding %v in the distdir (its stderr is above)", names)
		}
		time.Sleep(2 * time.Millisecond)
	}
}

// release tells the child to give up its locks and waits for it to be gone. A
// non-zero exit is a failure: the child releases and exits cleanly, so anything
// else means the release path itself broke.
func (h *lockHelper) release() {
	h.t.Helper()
	if h.reaped {
		return
	}
	_ = h.stdin.Close()
	if err := h.cmd.Wait(); err != nil {
		h.t.Fatalf("lock helper (pid %d) was asked to release and exited with %v", h.pid, err)
	}
	h.reaped = true
}

// kill stages the case the staleness rule exists for: a holder that dies without
// releasing anything. Wait is what makes it deterministic rather than a race —
// once it returns the process has been reaped, so the kernel has closed its
// descriptors and dropped its flock.
func (h *lockHelper) kill() {
	h.t.Helper()
	if h.reaped {
		return
	}
	if err := h.cmd.Process.Signal(syscall.SIGKILL); err != nil {
		h.t.Fatalf("lock helper (pid %d) could not be killed: %v", h.pid, err)
	}
	_ = h.cmd.Wait() // "signal: killed" — that is the point
	h.reaped = true
}

func (h *lockHelper) stop() {
	if h.reaped {
		return
	}
	_ = h.stdin.Close()
	_ = h.cmd.Process.Kill()
	_ = h.cmd.Wait()
	h.reaped = true
}

// shortLockWait shrinks the bounded wait for one test, the way the portageq
// tests shrink portageqTimeout, so the timeout branch is reachable without a
// slow test. Restored on cleanup; these tests never run in parallel, which is
// what makes touching a package variable safe.
func shortLockWait(t *testing.T, wait, poll time.Duration) {
	t.Helper()
	oldWait, oldPoll := lockWait, lockPoll
	lockWait, lockPoll = wait, poll
	t.Cleanup(func() { lockWait, lockPoll = oldWait, oldPoll })
}

// lockFilePath is the test's own copy of the naming rule. It is written out
// rather than calling the implementation's helper on purpose: a test that asks
// the code under test where it put its lock file agrees with any answer.
func lockFilePath(distdir, name string) string {
	return filepath.Join(distdir, "."+name+".bentoo_lockfile")
}

func assertLockFileExists(t *testing.T, distdir, name, why string) {
	t.Helper()
	path := lockFilePath(distdir, name)
	if _, err := os.Lstat(path); err != nil {
		t.Fatalf("%s: no lock file at %q (%v); the distdir holds %v", why, path, err, dirEntryNames(t, distdir))
	}
}

func assertLockFileAbsent(t *testing.T, distdir, name, why string) {
	t.Helper()
	path := lockFilePath(distdir, name)
	if _, err := os.Lstat(path); err == nil {
		t.Errorf("%s: a lock file is still at %q; the distdir holds %v", why, path, dirEntryNames(t, distdir))
	}
}

// =============================================================================
// R2.4 — one distfile, one writer
// =============================================================================

// TestLockIsExclusiveForTheSameDistfile is R2.4's core claim, asserted for both
// of the writers D4 names: two runs of this tool, and two workers inside one
// sweep.
//
// The cross-process half is the one that matters most, and it is why this file
// re-executes the test binary. Two goroutines would pass against a sync.Mutex; a
// second PROCESS only passes against something the filesystem enforces.
func TestLockIsExclusiveForTheSameDistfile(t *testing.T) {
	t.Run("a second bentoo run cannot take a lock another run holds", func(t *testing.T) {
		distdir := t.TempDir()
		const name = "shared-1.0.tar.xz"

		holder := startLockHelper(t, distdir, name)
		assertLockFileExists(t, distdir, name, "the child says it holds the lock")

		// Short, because the expected outcome here is the timeout.
		shortLockWait(t, 200*time.Millisecond, 5*time.Millisecond)

		lock, err := LockFetch(distdir, []string{name})
		if err == nil {
			lock.Release()
			t.Fatalf("LockFetch succeeded while pid %d holds %q: two runs would now fetch onto the same path, which is exactly what R2.4 forbids", holder.pid, name)
		}
		if !errors.Is(err, ErrDistfileLocked) {
			t.Fatalf("LockFetch error = %v, want one that wraps ErrDistfileLocked so a caller can tell contention from a broken directory", err)
		}
		if lock != nil {
			t.Errorf("LockFetch returned a non-nil claim alongside its error (%v); a caller that defers Release on it is holding nothing", err)
		}

		// The canary. Without this the test would also pass if LockFetch were
		// simply broken and always failed.
		holder.release()
		second, err := LockFetch(distdir, []string{name})
		if err != nil {
			t.Fatalf("LockFetch failed even after the holder released (%v); the failure above was not the lock", err)
		}
		second.Release()
	})

	t.Run("the workers of one sweep cannot overlap on a name", func(t *testing.T) {
		// A generous wait: here every acquisition is expected to SUCCEED, so a
		// timeout would be the bug rather than the assertion.
		shortLockWait(t, 30*time.Second, time.Millisecond)

		distdir := t.TempDir()

		// 3.1 measured that a single shared filename almost never opens the
		// window a concurrency bug lives in: every loser fails at the first
		// check and never reaches the racy part. A long shared LIST puts every
		// worker inside that window over and over.
		const (
			workers = 8
			rounds  = 40
			pool    = 12
		)
		names := make([]string, pool)
		counters := make([]*atomic.Int32, pool)
		for i := range names {
			names[i] = fmt.Sprintf("pkg-%02d-1.0.tar.xz", i)
			counters[i] = &atomic.Int32{}
		}

		var overlaps, failures atomic.Int32
		var acquisitions atomic.Int32
		var firstErr atomic.Value
		var wg sync.WaitGroup

		for w := range workers {
			wg.Add(1)
			go func(worker int) {
				defer wg.Done()
				for r := range rounds {
					idx := (worker + r) % pool
					lock, err := LockFetch(distdir, []string{names[idx]})
					if err != nil {
						failures.Add(1)
						firstErr.CompareAndSwap(nil, err)
						continue
					}
					acquisitions.Add(1)
					// Held: if the lock means anything, this counter can never
					// read above one while we are inside here.
					if counters[idx].Add(1) != 1 {
						overlaps.Add(1)
					}
					// Long enough that an overlapping holder would be caught,
					// short enough that the test stays quick.
					time.Sleep(50 * time.Microsecond)
					counters[idx].Add(-1)
					lock.Release()
				}
			}(w)
		}
		wg.Wait()

		if n := overlaps.Load(); n != 0 {
			t.Errorf("two workers held the same distfile's lock at the same time %d time(s); R2.4 says that cannot happen", n)
		}
		if n := failures.Load(); n != 0 {
			t.Errorf("%d of %d acquisitions failed (first: %v); every one of these was expected to succeed, so a pass here would be vacuous", n, workers*rounds, firstErr.Load())
		}
		if got, want := acquisitions.Load(), int32(workers*rounds); got != want {
			t.Errorf("%d acquisitions happened, want %d; the exclusivity assertion above only means something if the work actually ran", got, want)
		}
		// Every lock was released, so the directory must be empty again.
		if left := dirEntryNames(t, distdir); len(left) != 0 {
			t.Errorf("the distdir still holds %v after every worker released; lock files are litter in a directory this tool does not own", left)
		}
	})
}

// =============================================================================
// R2.4 — but only for the same distfile
// =============================================================================

// TestLockAllowsDifferentDistfilesConcurrently pins the other side of R2.4: the
// lock is per distfile NAME, not per distdir. A lock over the whole directory
// would satisfy every exclusivity assertion above and quietly serialise a sweep
// that was written to run its packages in parallel (sweep.go:620).
//
// The wait is deliberately short. A directory-wide lock would not fail this test
// loudly — it would simply block — so the bound is what turns "slower" into
// "failed".
func TestLockAllowsDifferentDistfilesConcurrently(t *testing.T) {
	t.Run("one run fetches while another holds a different distfile", func(t *testing.T) {
		distdir := t.TempDir()
		const held = "held-1.0.tar.xz"
		const wanted = "wanted-2.0.tar.xz"

		holder := startLockHelper(t, distdir, held)
		shortLockWait(t, 200*time.Millisecond, 5*time.Millisecond)

		lock, err := LockFetch(distdir, []string{wanted})
		if err != nil {
			t.Fatalf("LockFetch(%q) failed while pid %d holds a DIFFERENT distfile (%q): %v — the lock is guarding the directory, not the file", wanted, holder.pid, held, err)
		}
		defer lock.Release()

		// Both claims are live at the same time, which is the actual assertion.
		assertLockFileExists(t, distdir, held, "the child still holds its own distfile")
		assertLockFileExists(t, distdir, wanted, "this run holds the distfile it asked for")
	})

	t.Run("a package with several distfiles is not blocked by an unrelated one", func(t *testing.T) {
		distdir := t.TempDir()
		holder := startLockHelper(t, distdir, "theirs-1.0.tar.xz")
		shortLockWait(t, 200*time.Millisecond, 5*time.Millisecond)

		wanted := []string{"ours-b-1.0.tar.xz", "ours-a-1.0.tar.xz", "ours-c-1.0.tar.xz"}
		lock, err := LockFetch(distdir, wanted)
		if err != nil {
			t.Fatalf("LockFetch(%v) failed while pid %d holds only %q: %v", wanted, holder.pid, "theirs-1.0.tar.xz", err)
		}
		defer lock.Release()

		for _, name := range wanted {
			assertLockFileExists(t, distdir, name, "every distfile the package expects is claimed")
		}
	})

	t.Run("two packages that share two distfiles do not wait on each other", func(t *testing.T) {
		// Deadlock-freedom, which is why LockFetch sorts. Two workers that need
		// the same two distfiles and take them in the order they were HANDED
		// them can each end up holding what the other is waiting for; the only
		// way out of that is the timeout, so a short bound turns a deadlock
		// into a failure instead of into a slow pass.
		shortLockWait(t, 500*time.Millisecond, time.Millisecond)
		distdir := t.TempDir()

		a, b := "shared-a-1.0.tar.xz", "shared-b-1.0.tar.xz"
		orders := [][]string{{a, b}, {b, a}}

		var wg sync.WaitGroup
		var failures atomic.Int32
		var acquisitions atomic.Int32
		var firstErr atomic.Value

		const (
			workers = 8
			rounds  = 15
		)
		for w := range workers {
			wg.Add(1)
			go func(worker int) {
				defer wg.Done()
				names := orders[worker%len(orders)]
				for range rounds {
					lock, err := LockFetch(distdir, names)
					if err != nil {
						failures.Add(1)
						firstErr.CompareAndSwap(nil, err)
						continue
					}
					acquisitions.Add(1)
					time.Sleep(20 * time.Microsecond)
					lock.Release()
				}
			}(w)
		}
		wg.Wait()

		if n := failures.Load(); n != 0 {
			t.Errorf("%d of %d acquisitions timed out (first: %v); workers handed the same two distfiles in opposite orders are waiting on each other", n, workers*rounds, firstErr.Load())
		}
		if got, want := acquisitions.Load(), int32(workers*rounds); got != want {
			t.Errorf("%d acquisitions succeeded, want %d", got, want)
		}
	})
}

// =============================================================================
// The released-on-return contract, and what happens when nobody returns
// =============================================================================

// TestLockReleaseAllowsReacquisition covers the two ways a lock stops being
// held: the holder gives it back, and the holder is killed.
//
// The second is the staleness rule, and it is the reason this lock carries an
// flock as well as an O_EXCL file. The kernel drops an flock when its holder
// dies, so a killed run's lock is detectably abandoned on the very next attempt
// — no timeout to age out, no PID to guess from, and no lock file that has to be
// removed by hand before the tool works again.
func TestLockReleaseAllowsReacquisition(t *testing.T) {
	t.Run("a released lock is gone from the directory and can be taken again", func(t *testing.T) {
		distdir := t.TempDir()
		const name = "cycle-1.0.tar.xz"
		seedFile(t, distdir, "unrelated-9.9.tar.xz", "not ours", 0o644)
		before := snapshotTree(t, distdir)

		first, err := LockFetch(distdir, []string{name})
		if err != nil {
			t.Fatalf("LockFetch: %v", err)
		}
		assertLockFileExists(t, distdir, name, "a held lock is a real file")

		first.Release()
		assertLockFileAbsent(t, distdir, name, "a released lock leaves nothing behind")

		// Nothing else in the directory moved. Directory sizes are relaxed
		// because on tmpfs adding and removing a name changes them; modes and
		// owners are not, because those are what R2.5 is about.
		assertSameTree(t, withoutDirSizes(before), withoutDirSizes(snapshotTree(t, distdir)),
			distdir, "taking and releasing a lock must not touch anything else in a directory this tool does not own")

		second, err := LockFetch(distdir, []string{name})
		if err != nil {
			t.Fatalf("LockFetch after Release: %v — a released lock is still blocking", err)
		}
		second.Release()
	})

	t.Run("Release is safe twice and on a claim that was never taken", func(t *testing.T) {
		distdir := t.TempDir()
		lock, err := LockFetch(distdir, []string{"twice-1.0.tar.xz"})
		if err != nil {
			t.Fatalf("LockFetch: %v", err)
		}
		lock.Release()
		lock.Release() // must not panic, and must not remove somebody else's lock

		var never *FetchLock
		never.Release() // the shape `defer lock.Release()` produces on the error path

		empty, err := LockFetch(distdir, nil)
		if err != nil {
			t.Fatalf("LockFetch with no names: %v", err)
		}
		empty.Release()
	})

	t.Run("a SIGKILLed holder does not deadlock the next run", func(t *testing.T) {
		distdir := t.TempDir()
		const name = "orphaned-1.0.tar.xz"

		holder := startLockHelper(t, distdir, name)
		assertLockFileExists(t, distdir, name, "the child holds the lock before it is killed")
		holder.kill()

		// The lock FILE is deliberately still there: a killed process unlinks
		// nothing. If the next run needed that file to be gone, this is where
		// it would deadlock.
		assertLockFileExists(t, distdir, name, "a killed holder leaves its lock file behind, which is the whole hazard")

		// Short on purpose: the abandoned lock must be recognised, not waited
		// out. A rule that aged the lock out on a timer would fail here.
		shortLockWait(t, 500*time.Millisecond, 5*time.Millisecond)

		start := time.Now()
		lock, err := LockFetch(distdir, []string{name})
		if err != nil {
			t.Fatalf("LockFetch could not take a lock whose holder (pid %d) was SIGKILLed: %v — every later run is now blocked on a file nobody owns", holder.pid, err)
		}
		defer lock.Release()

		if elapsed := time.Since(start); elapsed > 200*time.Millisecond {
			t.Errorf("reclaiming an abandoned lock took %s; it was recognised as abandoned, not detected immediately", elapsed)
		}

		// The lock is now genuinely OURS: the dead holder's PID has been
		// replaced. Without this, a no-op that simply reused the stale file
		// would pass.
		pid, ok := holderPID(lockFilePath(distdir, name))
		if !ok {
			t.Fatalf("the reclaimed lock file records no pid; a stale lock would no longer be diagnosable")
		}
		if pid == holder.pid {
			t.Errorf("the reclaimed lock still names the dead holder (pid %d); it was never really retaken", pid)
		}
		if pid != os.Getpid() {
			t.Errorf("the reclaimed lock names pid %d, want this process (%d)", pid, os.Getpid())
		}
	})
}

// =============================================================================
// The bounded wait, and the diagnostic it produces
// =============================================================================

// TestLockWaitTimesOutAndReportsHolderPID pins both halves of D4's sentence
// about a worker that cannot take the lock: the wait is BOUNDED, and what comes
// back names the process to go and look at.
//
// The PID is the only reason the lock file has any content at all, so a test
// that only checked the error type would leave the payload unasserted.
func TestLockWaitTimesOutAndReportsHolderPID(t *testing.T) {
	distdir := t.TempDir()
	const name = "contended-3.1.4.tar.xz"

	holder := startLockHelper(t, distdir, name)

	const wait = 300 * time.Millisecond
	shortLockWait(t, wait, 10*time.Millisecond)

	start := time.Now()
	lock, err := LockFetch(distdir, []string{name})
	elapsed := time.Since(start)

	if err == nil {
		lock.Release()
		t.Fatalf("LockFetch succeeded while pid %d still holds %q", holder.pid, name)
	}
	if !errors.Is(err, ErrDistfileLocked) {
		t.Fatalf("LockFetch error = %v, want one wrapping ErrDistfileLocked", err)
	}

	// Bounded above: it gave up rather than waiting on a holder that never
	// lets go.
	if elapsed > 20*wait {
		t.Errorf("LockFetch waited %s for a bound of %s; the wait is not bounded", elapsed, wait)
	}
	// And bounded below: a call that returns instantly is not waiting at all,
	// and would fail a package the moment a sibling worker started a download.
	if elapsed < wait {
		t.Errorf("LockFetch gave up after %s, before its own bound of %s; a contended lock has to be waited for", elapsed, wait)
	}

	msg := err.Error()
	if !strings.Contains(msg, strconv.Itoa(holder.pid)) {
		t.Errorf("the timeout error does not name the holder's pid (%d), so a stuck lock cannot be traced to a process: %s", holder.pid, msg)
	}
	if !strings.Contains(msg, name) {
		t.Errorf("the timeout error does not name the distfile (%q): %s", name, msg)
	}
	if !strings.Contains(msg, distdir) {
		t.Errorf("the timeout error does not name the directory (%q): %s", distdir, msg)
	}

	// Canary: the failure above was contention, not a broken LockFetch.
	holder.release()
	after, err := LockFetch(distdir, []string{name})
	if err != nil {
		t.Fatalf("LockFetch failed after the holder released: %v", err)
	}
	after.Release()
}

// =============================================================================
// Untrusted names
// =============================================================================

// TestLockRefusesNamesThatAreNotFilenames pins that the lock file's name goes
// through the same reduction every other write in this package uses.
//
// 3.2 measured which input actually separates distfileName from a bare
// filepath.Base on Linux: not ".." — every one of those joins to a directory
// that exists, and a second check catches it — but a name carrying a BACKSLASH,
// which is a perfectly legal filename here. That is the case used below, because
// it is the one whose outcome differs.
func TestLockRefusesNamesThatAreNotFilenames(t *testing.T) {
	t.Run("a name that is not a filename produces no lock file", func(t *testing.T) {
		// Short on purpose. Two refused names that stopped being refused would
		// reduce to the SAME lock file, and the second call would then contend
		// with the first rather than assert anything — measured, that is a
		// two-minute hang on the production bound instead of a failure.
		shortLockWait(t, 200*time.Millisecond, 5*time.Millisecond)

		distdir := t.TempDir()
		before := snapshotTree(t, distdir)

		// The positive control comes first: if a good name did not produce a
		// lock file either, the assertion below would hold for the wrong
		// reason.
		good, err := LockFetch(distdir, []string{"real-1.0.tar.xz"})
		if err != nil {
			t.Fatalf("LockFetch on an ordinary name: %v", err)
		}
		assertLockFileExists(t, distdir, "real-1.0.tar.xz", "an ordinary name is locked, so this test can tell the two apart")
		good.Release()

		refused := []string{
			`weird\name.tar.xz`, // the input that separates distfileName from filepath.Base
			"..",
			".",
			"/",
			"",
		}
		// The claims are kept until after the assertions. Release removes the
		// lock file it created, so releasing inside this loop would tidy away
		// the exact evidence the test is looking for — measured: with the
		// reduction replaced by a bare filepath.Base every one of these names
		// DOES produce a lock file, and a release-as-you-go version of this
		// test passed anyway.
		var claims []*FetchLock
		for _, name := range refused {
			lock, err := LockFetch(distdir, []string{name})
			if err != nil {
				t.Fatalf("LockFetch(%q) = %v; an unusable name is skipped, not a failure", name, err)
			}
			claims = append(claims, lock)
		}

		if left := dirEntryNames(t, distdir); len(left) != 0 {
			t.Errorf("names that are not filenames left %v in the distdir; the reduction is not being applied to the lock file's name", left)
		}
		assertSameTree(t, withoutDirSizes(before), withoutDirSizes(snapshotTree(t, distdir)),
			distdir, "a refused name must not touch the distdir at all")

		for _, lock := range claims {
			lock.Release()
		}
	})

	t.Run("a traversing name is neutralised into the distdir, not followed out of it", func(t *testing.T) {
		// "../../etc/passwd" is NOT refused, and that is the correct answer,
		// not a hole. distfileName's job is to neutralise traversal rather than
		// to reject it — filepath.Base reduces this to "passwd", which is an
		// ordinary filename, and Quarantine and RecordFetchScope treat the very
		// same input the very same way. What has to be true is that the lock
		// lands INSIDE the distdir under the reduced name and that nothing at
		// all appears outside it.
		parent := t.TempDir()
		distdir := filepath.Join(parent, "distfiles")
		if err := os.Mkdir(distdir, 0o755); err != nil {
			t.Fatalf("mkdir %q: %v", distdir, err)
		}
		beforeParent := snapshotTree(t, parent)

		lock, err := LockFetch(distdir, []string{"../../etc/passwd"})
		if err != nil {
			t.Fatalf("LockFetch on a traversing name: %v", err)
		}

		assertLockFileExists(t, distdir, "passwd", "the traversal is reduced to its base name and locked inside the distdir")
		if siblings := dirEntryNames(t, parent); len(siblings) != 1 || siblings[0] != "distfiles" {
			t.Errorf("the parent of the distdir holds %v, want only [distfiles]; the lock escaped the directory it was meant to stay in", siblings)
		}
		assertSameTree(t,
			withoutDirSizes(withoutSubtree(beforeParent, "distfiles")),
			withoutDirSizes(withoutSubtree(snapshotTree(t, parent), "distfiles")),
			parent, "nothing outside the distdir may be touched by a traversing name")

		lock.Release()
		assertLockFileAbsent(t, distdir, "passwd", "the neutralised lock is released like any other")
	})

	t.Run("the same distfile named twice is locked once", func(t *testing.T) {
		distdir := t.TempDir()
		// Reducing to the same filename twice must collapse: taking one lock
		// twice would deadlock this call against itself.
		shortLockWait(t, time.Second, 5*time.Millisecond)

		done := make(chan error, 1)
		go func() {
			lock, err := LockFetch(distdir, []string{"dup-1.0.tar.xz", "dup-1.0.tar.xz", "sub/dir/dup-1.0.tar.xz"})
			if lock != nil {
				lock.Release()
			}
			done <- err
		}()

		select {
		case err := <-done:
			if err != nil {
				t.Fatalf("LockFetch with a repeated name: %v", err)
			}
		case <-time.After(10 * time.Second):
			t.Fatalf("LockFetch never returned for a repeated name; it is waiting on a lock it already holds")
		}
	})

	t.Run("an empty distdir is refused rather than locked somewhere else", func(t *testing.T) {
		// filepath.Join("", name) resolves against the WORKING directory, so a
		// lock taken there would separate nobody from anybody — and would drop
		// a file into the source tree.
		lock, err := LockFetch("", []string{"anything-1.0.tar.xz"})
		if err == nil {
			lock.Release()
			t.Fatalf("LockFetch with no distdir succeeded")
		}
		if errors.Is(err, ErrDistfileLocked) {
			t.Errorf("an unresolved distdir was reported as contention (%v); it is a caller bug, not a busy lock", err)
		}
	})
}
