package main

// Authored for story 056, sub-task 5.1 (S056-R4.1, S056-R4.4, S056-R4.5,
// S056-R4.7, S056-R6.1, S056-R6.2). The holder child takes the overlay lock with
// the raw mechanism the story names (0644 file, flock LOCK_EX, pid=<pid>), so
// the test does not agree with the code under test by sharing it.

import (
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

	"github.com/obentoo/bentoolkit/internal/autoupdate"
	"github.com/obentoo/bentoolkit/internal/common/filelock"
)

const (
	overlayHolderPathEnv  = "BENTOO_TEST_OVERLAY_LOCK_HOLDER_PATH"
	overlayHolderReadyEnv = "BENTOO_TEST_OVERLAY_LOCK_HOLDER_READY"
)

// TestHelperOverlayLockHolder is not a test: in the child role it holds an
// flock on the given path until its stdin closes.
func TestHelperOverlayLockHolder(t *testing.T) {
	path := os.Getenv(overlayHolderPathEnv)
	if path == "" {
		t.Skip("child role only")
	}
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o644)
	if err != nil {
		os.Exit(3)
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		os.Exit(3)
	}
	if _, err := fmt.Fprintf(f, "pid=%d\n", os.Getpid()); err != nil {
		os.Exit(3)
	}
	if err := os.WriteFile(os.Getenv(overlayHolderReadyEnv), []byte(strconv.Itoa(os.Getpid())), 0o600); err != nil {
		os.Exit(4)
	}
	_, _ = io.Copy(io.Discard, os.Stdin)
	os.Exit(0)
}

func s056StartOverlayHolder(t *testing.T, path string) int {
	t.Helper()
	ready := filepath.Join(t.TempDir(), "ready")
	cmd := exec.Command(os.Args[0], "-test.run=^TestHelperOverlayLockHolder$")
	cmd.Env = append(os.Environ(), overlayHolderPathEnv+"="+path, overlayHolderReadyEnv+"="+ready)
	cmd.Stderr = os.Stderr
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = stdin.Close(); _ = cmd.Wait() })
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
			t.Fatal("the overlay-lock holder never announced itself")
		}
		time.Sleep(time.Millisecond) // polling the ready file
	}
}

// s056DeadPID is the PID of a child that has exited and been reaped.
func s056DeadPID(t *testing.T) int {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run=^$")
	if err := cmd.Run(); err != nil {
		t.Fatalf("short-lived child: %v", err)
	}
	return cmd.ProcessState.Pid()
}

// s056AutoupdateEnv builds a HOME with a bentoo config pointing at a fresh
// overlay holding an empty registry, and selects `--check`. It returns the
// overlay and the autoupdate config dir.
func s056AutoupdateEnv(t *testing.T) (overlay, configDir string) {
	t.Helper()
	home := t.TempDir()
	cfg := filepath.Join(home, ".config", "bentoo")
	overlay = filepath.Join(home, "overlay")
	for _, d := range []string{cfg, filepath.Join(overlay, "profiles"), filepath.Join(overlay, "metadata"), filepath.Join(overlay, ".autoupdate")} {
		if err := os.MkdirAll(d, 0o750); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(overlay, ".autoupdate", "packages.toml"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	yaml := "overlay:\n  path: " + overlay + "\n  remote: origin\ngit:\n  user: Test\n  email: test@test.com\n"
	if err := os.WriteFile(filepath.Join(cfg, "config.yaml"), []byte(yaml), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))

	oLint, oFix, oConc, oTimeout, oOnly, oApply, oCheck, oList :=
		autoupdateLint, autoupdateFix, autoupdateConcurrency, autoupdateTimeout,
		autoupdateOnly, autoupdateApply, autoupdateCheck, autoupdateList
	t.Cleanup(func() {
		autoupdateLint, autoupdateFix, autoupdateConcurrency, autoupdateTimeout,
			autoupdateOnly, autoupdateApply, autoupdateCheck, autoupdateList =
			oLint, oFix, oConc, oTimeout, oOnly, oApply, oCheck, oList
	})
	autoupdateLint, autoupdateFix, autoupdateList = false, false, false
	autoupdateConcurrency = autoupdate.DefaultConcurrency
	autoupdateTimeout, autoupdateOnly, autoupdateApply = 0, "", ""
	autoupdateCheck = true
	return overlay, filepath.Join(home, ".config", "bentoo", "autoupdate")
}

// s056RunCapturingFDs runs runAutoupdate with fds 1 and 2 pointed at a file.
// The logger holds the os.Stderr it saw first, so swapping the variable would
// miss its lines; redirecting the descriptor does not.
func s056RunCapturingFDs(t *testing.T) (code int, out string) {
	t.Helper()
	f, err := os.CreateTemp(t.TempDir(), "out")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	saved1, err1 := syscall.Dup(1)
	saved2, err2 := syscall.Dup(2)
	if err1 != nil || err2 != nil {
		t.Fatalf("dup: %v %v", err1, err2)
	}
	_ = syscall.Dup3(int(f.Fd()), 1, 0)
	_ = syscall.Dup3(int(f.Fd()), 2, 0)
	func() {
		defer func() {
			_ = syscall.Dup3(saved1, 1, 0)
			_ = syscall.Dup3(saved2, 2, 0)
			_ = syscall.Close(saved1)
			_ = syscall.Close(saved2)
		}()
		code = withExitIntercept(func() { runAutoupdate(autoupdateCmd, nil) })
	}()
	data, _ := os.ReadFile(f.Name())
	return code, string(data)
}

// TestAutoupdateOverlayLock_SecondRunExitsNamingPathAndPID: R4.1 + R4.4 — the
// "second run blocked" half.
func TestAutoupdateOverlayLock_SecondRunExitsNamingPathAndPID(t *testing.T) {
	overlay, _ := s056AutoupdateEnv(t)
	oldWait, oldPoll := filelock.Wait, filelock.Poll
	filelock.Wait, filelock.Poll = 300*time.Millisecond, 10*time.Millisecond
	t.Cleanup(func() { filelock.Wait, filelock.Poll = oldWait, oldPoll })

	lockPath := filepath.Join(overlay, ".autoupdate.bentoo-lock")
	pid := s056StartOverlayHolder(t, lockPath)

	code, out := s056RunCapturingFDs(t)
	if code != 1 {
		t.Errorf("a second --check while pid %d holds the overlay lock exited %d, want 1", pid, code)
	}
	for _, want := range []string{lockPath, strconv.Itoa(pid)} {
		if !strings.Contains(out, want) {
			t.Errorf("output does not name %q:\n%s", want, out)
		}
	}
}

// TestAutoupdateOverlayLock_DeadHolderIsReapedAndReleasedOnReturn: R4.5 + R4.7
// — the hostile half: a lock file left by a dead run neither blocks this run
// (the bound is a minute) nor survives it.
func TestAutoupdateOverlayLock_DeadHolderIsReapedAndReleasedOnReturn(t *testing.T) {
	overlay, _ := s056AutoupdateEnv(t)
	oldWait, oldPoll := filelock.Wait, filelock.Poll
	filelock.Wait, filelock.Poll = time.Minute, 10*time.Millisecond
	t.Cleanup(func() { filelock.Wait, filelock.Poll = oldWait, oldPoll })

	lockPath := filepath.Join(overlay, ".autoupdate.bentoo-lock")
	if err := os.WriteFile(lockPath, []byte(fmt.Sprintf("pid=%d\n", s056DeadPID(t))), 0o644); err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	_, out := s056RunCapturingFDs(t)
	if elapsed := time.Since(start); elapsed > 30*time.Second {
		t.Errorf("the run took %v: it waited on a dead holder's lock", elapsed)
	}
	if strings.Contains(out, "did not become free") || strings.Contains(out, "locked") {
		t.Errorf("the run reported a lock held by a dead process:\n%s", out)
	}
	if _, err := os.Lstat(lockPath); err == nil {
		data, _ := os.ReadFile(lockPath)
		t.Errorf("%s is still on disk after the run returned (holds %q): the dead holder's lock was neither reaped nor released", lockPath, data)
	}
}

// TestAutoupdateOverlayLock_SweepsDeadTemps: R6.1 + R6.2 — once the lock is
// taken, dead writers' temps under the overlay (not .git) and directly in the
// config dir are removed with one WARN line each; live ones and nested config
// entries stay. The lock file is gone after the run returns (R4.7).
func TestAutoupdateOverlayLock_SweepsDeadTemps(t *testing.T) {
	overlay, configDir := s056AutoupdateEnv(t)
	dead := strconv.Itoa(s056DeadPID(t))
	self := strconv.Itoa(os.Getpid())
	remove := []string{
		filepath.Join(overlay, "app-misc", "hello", ".hello-1.1.ebuild.bentoo-"+dead+"-x1"),
		filepath.Join(overlay, ".autoupdate", ".packages.toml.bentoo-"+dead+"-x2"),
		filepath.Join(configDir, ".cache.json.bentoo-"+dead+"-x3"),
	}
	keep := []string{
		filepath.Join(overlay, "app-misc", "hello", ".hello-1.1.ebuild.bentoo-"+self+"-live"),
		filepath.Join(overlay, ".git", ".index.bentoo-"+dead+"-g"),
		filepath.Join(configDir, "nested", ".pending.json.bentoo-"+dead+"-n"),
		filepath.Join(overlay, "app-misc", "hello", "hello-1.0.ebuild"),
	}
	for _, p := range append(append([]string{}, remove...), keep...) {
		if err := os.MkdirAll(filepath.Dir(p), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	_, out := s056RunCapturingFDs(t)

	for _, p := range remove {
		if _, err := os.Lstat(p); err == nil {
			t.Errorf("dead writer's temp %s survived the run", p)
		}
		if !strings.Contains(out, p) {
			t.Errorf("no log line names the removed %s:\n%s", p, out)
		}
	}
	for _, p := range keep {
		if _, err := os.Lstat(p); err != nil {
			t.Errorf("%s must be left in place: %v", p, err)
		}
	}
	if _, err := os.Lstat(filepath.Join(overlay, ".autoupdate.bentoo-lock")); err == nil {
		t.Error("the overlay lock file is still on disk after the run returned")
	}
}
