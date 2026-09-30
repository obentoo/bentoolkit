package autoupdate

// Authored for story 056, sub-task 2.2 (S056-R5.1, S056-R5.2, S056-R5.6).
//
// The holder child takes the lock with the raw mechanism the story names —
// a 0644 file, flock(LOCK_EX), pid=<pid> — rather than through filelock, so the
// test does not agree with the code under test merely by sharing it.

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/obentoo/bentoolkit/internal/common/filelock"
)

const (
	stateHolderPathEnv  = "BENTOO_TEST_STATE_LOCK_HOLDER_PATH"
	stateHolderReadyEnv = "BENTOO_TEST_STATE_LOCK_HOLDER_READY"
)

// TestHelperStateLockHolder is not a test: in the child role it holds an flock
// on the given path until its stdin closes.
func TestHelperStateLockHolder(t *testing.T) {
	path := os.Getenv(stateHolderPathEnv)
	if path == "" {
		t.Skip("child role only")
	}
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o644)
	if err != nil {
		fmt.Fprintf(os.Stderr, "state holder: open: %v\n", err)
		os.Exit(3)
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		fmt.Fprintf(os.Stderr, "state holder: flock: %v\n", err)
		os.Exit(3)
	}
	if _, err := fmt.Fprintf(f, "pid=%d\n", os.Getpid()); err != nil {
		os.Exit(3)
	}
	if err := os.WriteFile(os.Getenv(stateHolderReadyEnv), []byte(strconv.Itoa(os.Getpid())), 0o600); err != nil {
		os.Exit(4)
	}
	_, _ = io.Copy(io.Discard, os.Stdin)
	os.Exit(0)
}

func s056StartStateHolder(t *testing.T, path string) int {
	t.Helper()
	ready := filepath.Join(t.TempDir(), "ready")
	cmd := exec.Command(os.Args[0], "-test.run=^TestHelperStateLockHolder$")
	cmd.Env = append(os.Environ(), stateHolderPathEnv+"="+path, stateHolderReadyEnv+"="+ready)
	cmd.Stderr = os.Stderr
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = stdin.Close()
		_ = cmd.Wait()
	})
	deadline := time.Now().Add(20 * time.Second)
	for {
		if data, err := os.ReadFile(ready); err == nil && len(data) > 0 {
			pid, err := strconv.Atoi(string(data))
			if err != nil || pid == os.Getpid() {
				t.Fatalf("holder announced %q", data)
			}
			return pid
		}
		if time.Now().After(deadline) {
			t.Fatal("the state-lock holder never announced itself")
		}
		time.Sleep(time.Millisecond) // polling: the ready file exists
	}
}

func s056Bounds(t *testing.T, wait, poll time.Duration) {
	t.Helper()
	oldWait, oldPoll := filelock.Wait, filelock.Poll
	filelock.Wait, filelock.Poll = wait, poll
	t.Cleanup(func() { filelock.Wait, filelock.Poll = oldWait, oldPoll })
}

// TestStateSave_LockTimeoutNamesPathAndPID: R5.1 + R5.2. While another process
// holds <configDir>/.state.bentoo-lock, a save waits out the bound, fails with
// ErrLocked naming the lock path and the holder's PID, and cache.json is left
// byte-identical.
func TestStateSave_LockTimeoutNamesPathAndPID(t *testing.T) {
	s056Bounds(t, 300*time.Millisecond, 10*time.Millisecond)
	dir := t.TempDir()
	seed, err := NewCache(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := seed.Set("x/a", "1", "u"); err != nil {
		t.Fatalf("seeding with the lock free: %v", err)
	}
	before, err := os.ReadFile(filepath.Join(dir, "cache.json"))
	if err != nil {
		t.Fatal(err)
	}

	lockPath := filepath.Join(dir, ".state.bentoo-lock")
	pid := s056StartStateHolder(t, lockPath)

	c, _ := NewCache(dir)
	err = c.Set("x/b", "2", "u")
	if err == nil {
		t.Fatalf("Set succeeded while pid %d holds %s", pid, lockPath)
	}
	if !errors.Is(err, filelock.ErrLocked) {
		t.Errorf("error %q does not wrap filelock.ErrLocked", err)
	}
	for _, want := range []string{lockPath, strconv.Itoa(pid)} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not name %q", err, want)
		}
	}
	after, _ := os.ReadFile(filepath.Join(dir, "cache.json"))
	if !bytes.Equal(before, after) {
		t.Errorf("cache.json changed although the save could not take the lock:\nbefore %s\nafter  %s", before, after)
	}
}

// TestStateSave_InProcessSavesSerialize: R5.6. Four Cache and four PendingList
// instances over ONE config dir save at once. They share one lock file, so a
// save that polled it would sleep a full Poll; Poll is set to 30s, so only an
// in-process queue in front of the file lock finishes inside the bound. Every
// key must also survive, which is R5.4 under real concurrency.
func TestStateSave_InProcessSavesSerialize(t *testing.T) {
	s056Bounds(t, 2*time.Minute, 30*time.Second)
	dir := t.TempDir()

	var caches []*Cache
	var pendings []*PendingList
	for range 4 {
		c, err := NewCache(dir)
		if err != nil {
			t.Fatal(err)
		}
		p, err := NewPendingList(dir)
		if err != nil {
			t.Fatal(err)
		}
		caches, pendings = append(caches, c), append(pendings, p)
	}

	errs := make(chan error, 8)
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := range 4 {
		wg.Add(2)
		go func(i int) {
			defer wg.Done()
			<-start
			errs <- caches[i].Set(fmt.Sprintf("x/c%d", i), "1", "u")
		}(i)
		go func(i int) {
			defer wg.Done()
			<-start
			errs <- pendings[i].Add(PendingUpdate{Package: fmt.Sprintf("x/p%d", i), CurrentVersion: "1", NewVersion: "2", Status: StatusPending})
		}(i)
	}
	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	close(start)

	select {
	case <-done:
	case <-time.After(20 * time.Second):
		t.Fatal("eight concurrent saves did not finish in 20s with Poll=30s: in-process saves are polling the file lock instead of queueing")
	}
	close(errs)
	for err := range errs {
		if err != nil {
			t.Errorf("concurrent save failed: %v", err)
		}
	}

	c, _ := NewCache(dir)
	p, _ := NewPendingList(dir)
	for i := range 4 {
		if _, ok := c.GetEntry(fmt.Sprintf("x/c%d", i)); !ok {
			t.Errorf("cache.json lost x/c%d", i)
		}
		if !p.Has(fmt.Sprintf("x/p%d", i)) {
			t.Errorf("pending.json lost x/p%d", i)
		}
	}
}
