package git

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Story 054, R5.2 and R5.5 beyond the pre-authored runner_ctx_test.go: the
// error text a bound produces, and that no op hides a cancel or a timeout
// behind git's output or a sentinel.

// ctxOp is one GitRunner operation, the git subcommand its errors name, and
// whether it talks to a remote.
type ctxOp struct {
	name    string
	sub     string
	network bool
	call    func(context.Context, *GitRunner) error
}

func ctxOps() []ctxOp {
	return []ctxOp{
		{"Push", "push", true, func(c context.Context, r *GitRunner) error { return r.Push(c) }},
		{"PushDryRun", "push", true, func(c context.Context, r *GitRunner) error { _, err := r.PushDryRun(c); return err }},
		{"Fetch", "fetch", true, func(c context.Context, r *GitRunner) error { return r.Fetch(c, "origin") }},
		{"Status", "status", false, func(c context.Context, r *GitRunner) error { _, err := r.Status(c); return err }},
		{"StagedStatus", "status", false, func(c context.Context, r *GitRunner) error { _, err := r.StagedStatus(c); return err }},
		{"Add", "add", false, func(c context.Context, r *GitRunner) error { return r.Add(c, "f.txt") }},
		{"Commit", "commit", false, func(c context.Context, r *GitRunner) error { return r.Commit(c, "m", "u", "u@e.invalid") }},
		{"Merge", "merge", false, func(c context.Context, r *GitRunner) error { return r.Merge(c, "b") }},
		{"MergeFFOnly", "merge", false, func(c context.Context, r *GitRunner) error { return r.MergeFFOnly(c, "b") }},
		{"Rebase", "rebase", false, func(c context.Context, r *GitRunner) error { return r.Rebase(c, "b") }},
		{"CurrentBranch", "rev-parse", false, func(c context.Context, r *GitRunner) error { _, err := r.CurrentBranch(c); return err }},
		{"Upstream", "rev-parse", false, func(c context.Context, r *GitRunner) error { _, err := r.Upstream(c); return err }},
		{"CountRange", "rev-list", false, func(c context.Context, r *GitRunner) error { _, err := r.CountRange(c, "a", "b"); return err }},
	}
}

// stalledRunner returns a runner whose git child explains itself on both
// streams, as a conflicting merge does, and then never answers.
func stalledRunner(t *testing.T) *GitRunner {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "f.txt"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	seam := func(ctx context.Context, _ string, _ ...string) *exec.Cmd {
		return exec.CommandContext(ctx, "sh", "-c", "echo explained-on-stdout; echo explained-on-stderr >&2; exec sleep 30")
	}
	return NewGitRunner(dir, WithGitExecCommand(seam))
}

// assertStoppedByContext fails unless err is a context's report rather than
// a git failure: it wraps want, and neither ErrGitCommand nor ErrNoUpstream.
func assertStoppedByContext(t *testing.T, err, want error, took time.Duration) {
	t.Helper()
	if !errors.Is(err, want) {
		t.Errorf("error = %v, want one wrapping %v", err, want)
	}
	if errors.Is(err, ErrGitCommand) || errors.Is(err, ErrNoUpstream) {
		t.Errorf("error = %v: a context ended the run, yet it is reported as a git failure", err)
	}
	if took > 6*time.Second {
		t.Errorf("returned %v after the context ended, want within 6 s (S054-R5.2)", took)
	}
}

// S054-R5.5: past its bound an op fails with context.DeadlineExceeded and the
// text "git <sub> timed out after <bound>". The two bounds are shortened to
// different values, so the text also shows which bound each op got.
func TestRunnerBoundExpiryNamesOperationAndBound(t *testing.T) {
	const network, local = 300 * time.Millisecond, 200 * time.Millisecond
	savedNetwork, savedLocal := networkTimeout, localTimeout
	networkTimeout, localTimeout = network, local
	t.Cleanup(func() { networkTimeout, localTimeout = savedNetwork, savedLocal })

	for _, op := range ctxOps() {
		t.Run(op.name, func(t *testing.T) {
			bound := local
			if op.network {
				bound = network
			}
			start := time.Now()
			err := op.call(context.Background(), stalledRunner(t))
			assertStoppedByContext(t, err, context.DeadlineExceeded, time.Since(start)-bound)
			want := "git " + op.sub + " timed out after " + bound.String()
			if err != nil && !strings.Contains(err.Error(), want) {
				t.Errorf("error = %q, want it to contain %q", err, want)
			}
		})
	}
}

// S054-R5.2: a cancelled parent stops every op with context.Canceled. The
// frozen test covers Fetch; merge and rebase fold git's output into their
// error and Upstream maps failures to ErrNoUpstream, so each could hide it.
func TestRunnerCancelIsNotAGitFailure(t *testing.T) {
	for _, op := range ctxOps() {
		t.Run(op.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			const after = 150 * time.Millisecond
			time.AfterFunc(after, cancel)
			start := time.Now()
			err := op.call(ctx, stalledRunner(t))
			assertStoppedByContext(t, err, context.Canceled, time.Since(start)-after)
			if err != nil && strings.Contains(err.Error(), "timed out after") {
				t.Errorf("error = %q: a cancel is not a timeout", err)
			}
		})
	}
}

// The GOTCHA of task 6.1: when the caller's own deadline expires first, the
// op's bound did not, so the error wraps context.DeadlineExceeded without
// claiming "timed out after".
func TestRunnerParentDeadlineIsNotTheBound(t *testing.T) {
	const after = 200 * time.Millisecond
	ctx, cancel := context.WithTimeout(context.Background(), after)
	defer cancel()
	start := time.Now()
	_, err := stalledRunner(t).Status(ctx)
	assertStoppedByContext(t, err, context.DeadlineExceeded, time.Since(start)-after)
	if err != nil && (strings.Contains(err.Error(), "timed out after") || !strings.Contains(err.Error(), "git status")) {
		t.Errorf("error = %q, want it to name git status and not claim the op's bound", err)
	}
}

// S054-R5.5 quotes the texts "timed out after 5m0s" and "timed out after 1m0s":
// the bounds print as exactly that, and the runner applies them unshortened.
func TestRunnerBoundsAreTheStoryBounds(t *testing.T) {
	if got := NetworkTimeout.String(); got != "5m0s" {
		t.Errorf("NetworkTimeout prints %q, want 5m0s (S054-R5.3)", got)
	}
	if got := LocalTimeout.String(); got != "1m0s" {
		t.Errorf("LocalTimeout prints %q, want 1m0s (S054-R5.4)", got)
	}
	if networkTimeout != NetworkTimeout || localTimeout != LocalTimeout {
		t.Errorf("runner bounds = %v/%v, want the exported %v/%v", networkTimeout, localTimeout, NetworkTimeout, LocalTimeout)
	}
}
