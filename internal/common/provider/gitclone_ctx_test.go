//go:build unix

package provider

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

// Story 054, R5.9 / R5.10: updateRepo runs its git commands under ONE context
// bounded by 5 minutes, in group mode; cloneRepo is bounded by 5 minutes.

func swapExecCommand(t *testing.T, fn func(ctx context.Context, name string, arg ...string) *exec.Cmd) {
	t.Helper()
	orig := execCommand
	t.Cleanup(func() { execCommand = orig })
	execCommand = fn
}

func TestUpdateRepoStopsOnCancel(t *testing.T) {
	dir := t.TempDir()
	fifo := filepath.Join(dir, "pid.fifo")
	if err := syscall.Mkfifo(fifo, 0o600); err != nil {
		t.Fatal(err)
	}
	var once sync.Once
	swapExecCommand(t, func(ctx context.Context, _ string, _ ...string) *exec.Cmd {
		script := "false"
		once.Do(func() {
			// The grandchild reports its pid, then holds the output pipe.
			script = `sh -c 'echo $$ > "$0"; exec sleep 60' "$0" & wait`
		})
		return exec.CommandContext(ctx, "sh", "-c", script, fifo)
	})
	prov := &GitCloneProvider{LocalPath: dir, RepoURL: "https://example.invalid/r.git", Branch: "master"}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- prov.updateRepo(ctx) }()

	raw, err := os.ReadFile(fifo) // blocks until the grandchild writes
	if err != nil {
		t.Fatal(err)
	}
	grandchild, err := strconv.Atoi(strings.TrimSpace(string(raw)))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = syscall.Kill(grandchild, syscall.SIGKILL) })

	cancel()
	start := time.Now()
	select {
	case err = <-done:
	case <-time.After(20 * time.Second):
		t.Fatal("updateRepo still running 20 s after the cancel (B4)")
	}
	if took := time.Since(start); took > 6*time.Second {
		t.Errorf("updateRepo returned %v after the cancel, want within 6 s (R5.2)", took)
	}
	if !errors.Is(err, context.Canceled) {
		t.Errorf("updateRepo error = %v, want one wrapping context.Canceled", err)
	}
	deadline := time.Now().Add(time.Second)
	for syscall.Kill(grandchild, 0) == nil && !isZombie(grandchild) {
		if time.Now().After(deadline) {
			t.Errorf("grandchild %d survived the cancel: updateRepo's git is not run in group mode (R5.9)", grandchild)
			break
		}
		time.Sleep(10 * time.Millisecond) // polling kernel state, not synchronising goroutines
	}
}

func isZombie(pid int) bool {
	b, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	if err != nil {
		return true
	}
	i := strings.LastIndexByte(string(b), ')')
	return i >= 0 && i+2 < len(b) && b[i+2] == 'Z'
}

// R5.9: the pull, fetch and reset share ONE 5-minute context. Hostile halves:
// no deadline at all (today), and a fresh bound per command (deadlines differ).
func TestUpdateRepoTimesOut(t *testing.T) {
	var deadlines []time.Time
	var calls int
	swapExecCommand(t, func(ctx context.Context, _ string, _ ...string) *exec.Cmd {
		calls++
		dl, ok := ctx.Deadline()
		if !ok {
			dl = time.Time{}
		}
		deadlines = append(deadlines, dl)
		if calls == 1 {
			return exec.CommandContext(ctx, "false") // pull fails: fetch + reset follow
		}
		return exec.CommandContext(ctx, "true")
	})
	prov := &GitCloneProvider{LocalPath: t.TempDir(), RepoURL: "https://example.invalid/r.git", Branch: "master"}
	_ = prov.updateRepo(context.Background())

	if len(deadlines) < 3 {
		t.Fatalf("updateRepo spawned %d git commands through execCommand, want pull, fetch and reset (3)", len(deadlines))
	}
	for i, dl := range deadlines {
		if dl.IsZero() {
			t.Errorf("git command %d ran with no deadline (R5.3)", i+1)
			continue
		}
		if b := time.Until(dl); b <= 5*time.Minute-10*time.Second || b > 5*time.Minute {
			t.Errorf("git command %d budget %v, want 5m0s (R5.9)", i+1, b)
		}
		if !dl.Equal(deadlines[0]) {
			t.Errorf("git command %d has its own deadline; the three commands must share one context (R5.9)", i+1)
		}
	}
}

// R5.10: the clone bound is 5 minutes (today 2).
func TestCloneRepoBoundIsFiveMinutes(t *testing.T) {
	var budget time.Duration = -1
	swapExecCommand(t, func(ctx context.Context, _ string, _ ...string) *exec.Cmd {
		if dl, ok := ctx.Deadline(); ok {
			budget = time.Until(dl)
		}
		return exec.CommandContext(ctx, "false")
	})
	prov := &GitCloneProvider{LocalPath: filepath.Join(t.TempDir(), "repo"), RepoURL: "https://example.invalid/r.git", Branch: "master"}
	_ = prov.cloneRepo()
	if budget <= 5*time.Minute-10*time.Second || budget > 5*time.Minute {
		t.Errorf("clone ran with a %v budget, want 5m0s (R5.10; -1 = no deadline)", budget)
	}
}
