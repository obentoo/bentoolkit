package filelock

// Authored for story 056, sub-task 1.2 (S056-R4.2, S056-R4.3, S056-R4.5,
// S056-R5.2). The exclusivity cases use a genuine second PROCESS, re-executed
// from this test binary: two goroutines would pass against a sync.Mutex, a
// second process only against something the kernel enforces.

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

const (
	holderPathEnv  = "BENTOO_TEST_FILELOCK_HOLDER_PATH"
	holderReadyEnv = "BENTOO_TEST_FILELOCK_HOLDER_READY"
)

// TestHelperFilelockHolder is not a test: in the child role it acquires the
// lock, announces its PID through a ready file, and holds the lock until its
// stdin is closed.
func TestHelperFilelockHolder(t *testing.T) {
	path := os.Getenv(holderPathEnv)
	if path == "" {
		t.Skip("child role only")
	}
	lock, err := Acquire(path, "test holder")
	if err != nil {
		fmt.Fprintf(os.Stderr, "holder: Acquire(%q): %v\n", path, err)
		os.Exit(3)
	}
	if err := os.WriteFile(os.Getenv(holderReadyEnv), []byte(strconv.Itoa(os.Getpid())), 0o600); err != nil {
		fmt.Fprintf(os.Stderr, "holder: announce: %v\n", err)
		os.Exit(4)
	}
	_, _ = io.Copy(io.Discard, os.Stdin)
	lock.Release()
	os.Exit(0)
}

type holder struct {
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	pid    int
	reaped bool
}

func startHolder(t *testing.T, path string) *holder {
	t.Helper()
	ready := filepath.Join(t.TempDir(), "ready")
	cmd := exec.Command(os.Args[0], "-test.run=^TestHelperFilelockHolder$")
	cmd.Env = append(os.Environ(), holderPathEnv+"="+path, holderReadyEnv+"="+ready)
	cmd.Stderr = os.Stderr
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	h := &holder{cmd: cmd, stdin: stdin}
	t.Cleanup(func() {
		if !h.reaped {
			_ = h.stdin.Close()
			_ = h.cmd.Process.Kill()
			_ = h.cmd.Wait()
		}
	})
	deadline := time.Now().Add(20 * time.Second)
	for {
		if data, err := os.ReadFile(ready); err == nil && len(data) > 0 {
			pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
			if err != nil || pid == os.Getpid() {
				t.Fatalf("holder announced %q, not a separate process's pid", data)
			}
			h.pid = pid
			return h
		}
		if time.Now().After(deadline) {
			t.Fatal("the holder child never announced it holds the lock")
		}
		time.Sleep(time.Millisecond) // polling the ready file, not a guess at timing
	}
}

func (h *holder) kill(t *testing.T) {
	t.Helper()
	if err := h.cmd.Process.Signal(syscall.SIGKILL); err != nil {
		t.Fatal(err)
	}
	_ = h.cmd.Wait()
	h.reaped = true
}

func (h *holder) release(t *testing.T) {
	t.Helper()
	_ = h.stdin.Close()
	if err := h.cmd.Wait(); err != nil {
		t.Fatalf("holder pid %d exited with %v after being asked to release", h.pid, err)
	}
	h.reaped = true
}

func shortBounds(t *testing.T, wait, poll time.Duration) {
	t.Helper()
	oldWait, oldPoll := Wait, Poll
	Wait, Poll = wait, poll
	t.Cleanup(func() { Wait, Poll = oldWait, oldPoll })
}

// TestAcquire_DefaultBounds: R4.3 and the 2-minute bound of R4.4/R5.2.
func TestAcquire_DefaultBounds(t *testing.T) {
	if Wait != 2*time.Minute {
		t.Errorf("Wait = %v, want 2m", Wait)
	}
	if Poll != 25*time.Millisecond {
		t.Errorf("Poll = %v, want 25ms", Poll)
	}
}

// TestAcquire_CreatesLockFileWithPIDAndMode: R4.2 — the file is 0644 and
// carries pid=<pid> of the holder.
func TestAcquire_CreatesLockFileWithPIDAndMode(t *testing.T) {
	old := syscall.Umask(0o022)
	t.Cleanup(func() { syscall.Umask(old) })

	path := filepath.Join(t.TempDir(), ".autoupdate.bentoo-lock")
	lock, err := Acquire(path, "autoupdate run")
	if err != nil {
		t.Fatalf("Acquire on a free path: %v", err)
	}
	defer lock.Release()

	info, err := os.Lstat(path)
	if err != nil {
		t.Fatalf("no lock file at %s while held: %v", path, err)
	}
	if info.Mode().Perm() != 0o644 || !info.Mode().IsRegular() {
		t.Errorf("lock file is %v, want a regular file at 0644", info.Mode())
	}
	data, _ := os.ReadFile(path)
	if !strings.Contains("\n"+string(data), fmt.Sprintf("\npid=%d\n", os.Getpid())) {
		t.Errorf("lock file holds %q, want a line pid=%d", data, os.Getpid())
	}
}

// TestAcquire_LiveHolderBlocksThenErrLockedNamesPathAndPID: the "second run
// blocked" half. The waiter really waits the bound (it does not fail on the
// first attempt), then reports ErrLocked naming the lock path and the other
// process's PID, and the holder's lock file is left alone.
func TestAcquire_LiveHolderBlocksThenErrLockedNamesPathAndPID(t *testing.T) {
	shortBounds(t, 300*time.Millisecond, 10*time.Millisecond)
	path := filepath.Join(t.TempDir(), ".state.bentoo-lock")
	h := startHolder(t, path)

	start := time.Now()
	lock, err := Acquire(path, "state save")
	elapsed := time.Since(start)
	if err == nil {
		lock.Release()
		t.Fatalf("Acquire succeeded while pid %d holds %s", h.pid, path)
	}
	if !errors.Is(err, ErrLocked) {
		t.Fatalf("error %q does not wrap ErrLocked", err)
	}
	for _, want := range []string{path, strconv.Itoa(h.pid)} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not name %q", err, want)
		}
	}
	if elapsed < 300*time.Millisecond {
		t.Errorf("Acquire gave up after %v, before the %v bound", elapsed, 300*time.Millisecond)
	}
	if data, _ := os.ReadFile(path); !strings.Contains(string(data), "pid="+strconv.Itoa(h.pid)) {
		t.Errorf("the live holder's lock file was disturbed: it now holds %q", data)
	}
}

// TestAcquire_WaiterGetsLockOnceReleased: R4.3 — the waiter retries and takes
// the lock as soon as the holder lets go, well inside the bound.
func TestAcquire_WaiterGetsLockOnceReleased(t *testing.T) {
	shortBounds(t, 20*time.Second, 5*time.Millisecond)
	path := filepath.Join(t.TempDir(), ".state.bentoo-lock")
	h := startHolder(t, path)

	got := make(chan error, 1)
	go func() {
		lock, err := Acquire(path, "waiter")
		if err == nil {
			lock.Release()
		}
		got <- err
	}()
	h.release(t)
	if err := <-got; err != nil {
		t.Fatalf("the waiter did not get the lock after the holder released it: %v", err)
	}
}

// TestAcquire_DeadHolderIsReapedWithoutWaiting: R4.5, the hostile half of the
// blocked case. The holder is SIGKILLed with the lock file still on disk; the
// next Acquire takes it at once, without waiting for a bound set to a minute.
func TestAcquire_DeadHolderIsReapedWithoutWaiting(t *testing.T) {
	shortBounds(t, time.Minute, 10*time.Millisecond)
	path := filepath.Join(t.TempDir(), ".autoupdate.bentoo-lock")
	h := startHolder(t, path)
	h.kill(t)
	if _, err := os.Lstat(path); err != nil {
		t.Fatalf("precondition: the killed holder's lock file should still be on disk: %v", err)
	}

	start := time.Now()
	lock, err := Acquire(path, "after a crash")
	if err != nil {
		t.Fatalf("Acquire over a dead holder's lock (pid %d): %v", h.pid, err)
	}
	defer lock.Release()
	if elapsed := time.Since(start); elapsed > 10*time.Second {
		t.Errorf("reaping a dead holder took %v; it waited instead of reaping", elapsed)
	}
	data, _ := os.ReadFile(path)
	if !strings.Contains(string(data), fmt.Sprintf("pid=%d", os.Getpid())) {
		t.Errorf("the reclaimed lock file holds %q, want this process's pid", data)
	}
}

// TestAcquire_UnflockedPlantedFileIsReaped: a lock file nobody holds a flock on
// (a crashed run, or a file planted with a live-looking PID) is reclaimed: the
// decision is the kernel's flock, never the PID written in the file.
func TestAcquire_UnflockedPlantedFileIsReaped(t *testing.T) {
	shortBounds(t, time.Minute, 10*time.Millisecond)
	path := filepath.Join(t.TempDir(), ".state.bentoo-lock")
	if err := os.WriteFile(path, []byte(fmt.Sprintf("pid=%d\n", os.Getppid())), 0o644); err != nil {
		t.Fatal(err)
	}
	lock, err := Acquire(path, "planted")
	if err != nil {
		t.Fatalf("Acquire over an unflocked file: %v", err)
	}
	lock.Release()
}

// TestAcquire_SecondAcquireInSameProcessContends: the lock is per open file
// description, so a second Acquire in the SAME process is refused like a
// second process would be. POSIX record locks would hand it the lock.
func TestAcquire_SecondAcquireInSameProcessContends(t *testing.T) {
	shortBounds(t, 150*time.Millisecond, 10*time.Millisecond)
	path := filepath.Join(t.TempDir(), ".state.bentoo-lock")
	first, err := Acquire(path, "first")
	if err != nil {
		t.Fatal(err)
	}
	defer first.Release()

	second, err := Acquire(path, "second")
	if err == nil {
		second.Release()
		t.Fatal("a second Acquire in the same process got a lock that is already held")
	}
	if !errors.Is(err, ErrLocked) {
		t.Errorf("error %q does not wrap ErrLocked", err)
	}
}

// TestAcquire_CreateFailureNamesPathAndIsNotErrLocked: Q4 — a lock that cannot
// be created is an environment fault, reported with its path and cause, never
// dressed up as contention.
func TestAcquire_CreateFailureNamesPathAndIsNotErrLocked(t *testing.T) {
	path := filepath.Join(t.TempDir(), "missing-dir", ".state.bentoo-lock")
	_, err := Acquire(path, "nowhere")
	if err == nil {
		t.Fatal("Acquire succeeded in a directory that does not exist")
	}
	if errors.Is(err, ErrLocked) {
		t.Errorf("a create failure was reported as ErrLocked: %v", err)
	}
	if !strings.Contains(err.Error(), path) {
		t.Errorf("error %q does not name %s", err, path)
	}
}

// TestRelease_RemovesLockFileAndAllowsReacquisition: R4.7's mechanism — a
// released lock leaves no file behind and can be taken again at once.
func TestRelease_RemovesLockFileAndAllowsReacquisition(t *testing.T) {
	shortBounds(t, 150*time.Millisecond, 10*time.Millisecond)
	dir := t.TempDir()
	path := filepath.Join(dir, ".autoupdate.bentoo-lock")
	lock, err := Acquire(path, "first")
	if err != nil {
		t.Fatal(err)
	}
	lock.Release()
	if _, err := os.Lstat(path); err == nil {
		t.Errorf("the lock file is still at %s after Release", path)
	}
	again, err := Acquire(path, "second")
	if err != nil {
		t.Fatalf("re-acquiring after Release: %v", err)
	}
	again.Release()
	if entries, _ := os.ReadDir(dir); len(entries) != 0 {
		t.Errorf("the directory still holds %d entries after the last Release", len(entries))
	}
}
