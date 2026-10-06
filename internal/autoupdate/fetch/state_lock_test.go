package fetch

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
