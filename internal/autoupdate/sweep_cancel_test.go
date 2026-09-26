//go:build unix

package autoupdate

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// Story 054, R2.1 / R2.2 / R2.3 (B1): a cancelled or timed-out sweep leaves the
// pkgdev manifest step within 6 s, and pkgdev's whole group — not only pkgdev —
// is gone. The fake pkgdev is a shell that spawns a grandchild holding the
// captured stdout pipe, which is what a real pkgdev's fetcher (wget/curl) does.

// pkgdevGroupSeam returns a sweeper exec seam whose "pkgdev" starts a
// pipe-holding grandchild that reports its pid through fifo, then waits on it.
func pkgdevGroupSeam(fifo string) func(ctx context.Context, name string, arg ...string) *exec.Cmd {
	return func(ctx context.Context, _ string, _ ...string) *exec.Cmd {
		return exec.CommandContext(ctx, "sh", "-c",
			`sh -c 'echo $$ > "$0"; exec sleep 60' "$0" & wait`, fifo)
	}
}

// pkgdevFifo makes the fifo the fake pkgdev's grandchild reports through.
func pkgdevFifo(t *testing.T) string {
	t.Helper()
	fifo := filepath.Join(t.TempDir(), "pid.fifo")
	if err := syscall.Mkfifo(fifo, 0o600); err != nil {
		t.Fatalf("mkfifo: %v", err)
	}
	return fifo
}

// readGrandchildPID blocks until the grandchild has started and reports its pid;
// it registers a cleanup that kills it whatever the test outcome.
func readGrandchildPID(t *testing.T, fifo string) int {
	t.Helper()
	raw, err := os.ReadFile(fifo)
	if err != nil {
		t.Fatalf("reading the grandchild pid: %v", err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(raw)))
	if err != nil {
		t.Fatalf("grandchild pid %q: %v", raw, err)
	}
	t.Cleanup(func() { _ = syscall.Kill(pid, syscall.SIGKILL) })
	return pid
}

// sweepPIDGone reports whether pid no longer runs (absent, or a zombie waiting
// for its reaper).
func sweepPIDGone(pid int) bool {
	if syscall.Kill(pid, 0) != nil {
		return true
	}
	b, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	if err != nil {
		return true
	}
	i := strings.LastIndexByte(string(b), ')')
	return i >= 0 && i+2 < len(b) && b[i+2] == 'Z'
}

// manifestAsync runs the manifest step in a goroutine. Its cleanup is registered
// before the grandchild's kill, so it runs after it: the step has returned (and
// cleaned its distdir) before t.TempDir is removed, even when the test fails.
func manifestAsync(t *testing.T, step func() error) <-chan error {
	t.Helper()
	done := make(chan error, 1)
	finished := make(chan struct{})
	go func() {
		defer close(finished)
		done <- step()
	}()
	t.Cleanup(func() {
		select {
		case <-finished:
		case <-time.After(70 * time.Second):
		}
	})
	return done
}

// assertManifestStopped waits for the manifest step's result, then checks the
// 6 s bound from `from` and that the grandchild of pkgdev's group is gone.
func assertManifestStopped(t *testing.T, done <-chan error, from time.Time, grandchild int, req string) {
	t.Helper()
	var err error
	select {
	case err = <-done:
	case <-time.After(20 * time.Second):
		t.Fatalf("manifest step still blocked 20 s after the stop: a pkgdev descendant holding the output pipe keeps it open (B1, %s)", req)
	}
	if took := time.Since(from); took > 6*time.Second {
		t.Errorf("manifest step returned %v after the stop, want within 6 s (%s)", took, req)
	}
	if err == nil {
		t.Errorf("manifest step returned nil after being stopped, want an error (%s)", req)
	}
	tick := time.NewTicker(10 * time.Millisecond)
	defer tick.Stop()
	for limit := time.Now().Add(time.Second); !sweepPIDGone(grandchild); <-tick.C {
		if time.Now().After(limit) {
			t.Errorf("pkgdev grandchild %d still running after the manifest step returned: pkgdev's group was not stopped (%s)", grandchild, req)
			return
		}
	}
}

func TestRunManifestCancelStopsPkgdevGroup(t *testing.T) {
	overlayDir := filepath.Join(t.TempDir(), "overlay")
	pkg := "test-cat/test-pkg"
	createTestEbuildFile(t, overlayDir, pkg, "2.0.0")
	distdir := t.TempDir()
	fifo := pkgdevFifo(t)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s := manifestSweeper(t, overlayDir, distdir, "",
		withSweeperContext(ctx), withSweeperExec(pkgdevGroupSeam(fifo)))

	done := manifestAsync(t, func() error { return s.runManifest(pkg, "2.0.0") })
	grandchild := readGrandchildPID(t, fifo)

	stoppedAt := time.Now()
	cancel()
	assertManifestStopped(t, done, stoppedAt, grandchild, "R2.1")
}

func TestRunStagedManifestCancelStopsPkgdevGroup(t *testing.T) {
	stagedPkg := filepath.Join(t.TempDir(), "staged", "test-cat", "test-pkg")
	if err := os.MkdirAll(stagedPkg, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(stagedPkg, "test-pkg-2.0.0.ebuild"), []byte("EAPI=8\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	supplied := t.TempDir()
	fifo := pkgdevFifo(t)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s := newSweeper(filepath.Join(t.TempDir(), "overlay"),
		withSweeperContext(ctx), withSweeperExec(pkgdevGroupSeam(fifo)))

	done := manifestAsync(t, func() error {
		_, err := s.runStagedManifestIn(supplied, stagedPkg, "test-cat/test-pkg", "2.0.0")
		return err
	})
	grandchild := readGrandchildPID(t, fifo)

	stoppedAt := time.Now()
	cancel()
	assertManifestStopped(t, done, stoppedAt, grandchild, "R2.2")
}

// The deadline half of R2.3. manifestTimeout (5 min) is a constant; the same
// derived context expires when the PARENT's deadline passes, so a 200 ms parent
// deadline exercises the identical stop path without a 5-minute wait.
func TestRunManifestDeadlineStopsPkgdevGroup(t *testing.T) {
	overlayDir := filepath.Join(t.TempDir(), "overlay")
	pkg := "test-cat/test-pkg"
	createTestEbuildFile(t, overlayDir, pkg, "2.0.0")
	distdir := t.TempDir()
	fifo := pkgdevFifo(t)

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	s := manifestSweeper(t, overlayDir, distdir, "",
		withSweeperContext(ctx), withSweeperExec(pkgdevGroupSeam(fifo)))

	start := time.Now()
	done := manifestAsync(t, func() error { return s.runManifest(pkg, "2.0.0") })
	grandchild := readGrandchildPID(t, fifo)

	assertManifestStopped(t, done, start.Add(200*time.Millisecond), grandchild, "R2.3")
}
