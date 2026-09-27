package overlay

import (
	"context"
	"errors"
	"os/exec"
	"testing"
	"time"

	"github.com/obentoo/bentoolkit/internal/common/git"
)

// Story 054, R5.1 / R5.2 through the overlay API: the context a caller passes
// reaches the git child — a stalled remote ends on cancel instead of never.

func stalledGit(t *testing.T) *git.GitRunner {
	t.Helper()
	seam := func(ctx context.Context, _ string, _ ...string) *exec.Cmd {
		return exec.CommandContext(ctx, "sh", "-c", "exec sleep 60") // a remote that never answers
	}
	return git.NewGitRunner(t.TempDir(), git.WithGitExecCommand(seam))
}

func assertStopsOnCancel(t *testing.T, what string, call func(context.Context) error) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	time.AfterFunc(200*time.Millisecond, cancel)
	done := make(chan error, 1)
	start := time.Now()
	go func() { done <- call(ctx) }()
	select {
	case err := <-done:
		if took := time.Since(start); took > 200*time.Millisecond+6*time.Second {
			t.Errorf("%s returned %v after start, want within 6 s of the cancel", what, took)
		}
		if !errors.Is(err, context.Canceled) {
			t.Errorf("%s error = %v, want one wrapping context.Canceled", what, err)
		}
	case <-time.After(20 * time.Second):
		t.Fatalf("%s still blocked 20 s after the cancel (B4: overlay push never returns)", what)
	}
}

func TestPushWithExecutorStopsOnCancel(t *testing.T) {
	r := stalledGit(t)
	assertStopsOnCancel(t, "PushWithExecutor", func(ctx context.Context) error {
		_, err := PushWithExecutor(ctx, r)
		return err
	})
}

func TestPullWithRunnerStopsOnCancel(t *testing.T) {
	r := stalledGit(t)
	assertStopsOnCancel(t, "PullWithRunner", func(ctx context.Context) error {
		_, err := PullWithRunner(ctx, r, "origin", PullFFOnly, false)
		return err
	})
}
