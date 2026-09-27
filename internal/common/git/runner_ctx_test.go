package git

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// Story 054, R5 / R7.2: every GitRunner op takes a context, is bounded,
// stops on cancel, and never lets a user-supplied name become an option.

func hermeticGit(t *testing.T) {
	t.Helper()
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	for _, k := range []string{"GIT_AUTHOR_NAME", "GIT_COMMITTER_NAME"} {
		t.Setenv(k, "tester")
	}
	for _, k := range []string{"GIT_AUTHOR_EMAIL", "GIT_COMMITTER_EMAIL"} {
		t.Setenv(k, "tester@example.invalid")
	}
}

func gitOut(t *testing.T, dir string, args ...string) string {
	t.Helper()
	out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func newRepo(t *testing.T) string {
	t.Helper()
	hermeticGit(t)
	dir := t.TempDir()
	gitOut(t, dir, "init", "-q", "-b", "main")
	gitOut(t, dir, "commit", "-q", "--allow-empty", "-m", "c1")
	return dir
}

// R5.6: `--` precedes paths. Hostile halves: a file literally named "--all"
// must stage ONLY itself (read as an option it stages everything), and an
// ordinary name still stages.
func TestAddStagesADashPath(t *testing.T) {
	for _, name := range []string{"-x", "--all", "plain.txt"} {
		t.Run(name, func(t *testing.T) {
			dir := newRepo(t)
			for _, f := range []string{name, "bystander.txt"} {
				if err := os.WriteFile(filepath.Join(dir, f), []byte(f), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			if err := NewGitRunner(dir).Add(context.Background(), name); err != nil {
				t.Fatalf("Add(%q): %v", name, err)
			}
			staged := gitOut(t, dir, "diff", "--cached", "--name-only")
			if staged != name {
				t.Errorf("staged = %q, want exactly %q (bystander.txt must stay unstaged)", staged, name)
			}
		})
	}
}

// R5.7: `--end-of-options` precedes the branch, so a branch named "-x" is a
// revision. Converse: an ordinary branch name keeps working.
func TestMergeAndRebaseUseEndOfOptions(t *testing.T) {
	ops := map[string]func(*GitRunner, context.Context, string) error{
		"Merge":       (*GitRunner).Merge,
		"MergeFFOnly": (*GitRunner).MergeFFOnly,
		"Rebase":      (*GitRunner).Rebase,
	}
	for opName, op := range ops {
		for _, branch := range []string{"-x", "feature"} {
			t.Run(opName+"/"+branch, func(t *testing.T) {
				dir := newRepo(t)
				gitOut(t, dir, "checkout", "-q", "-b", "tmp")
				gitOut(t, dir, "commit", "-q", "--allow-empty", "-m", "c2")
				target := gitOut(t, dir, "rev-parse", "HEAD")
				gitOut(t, dir, "update-ref", "refs/heads/"+branch, target)
				gitOut(t, dir, "checkout", "-q", "main")

				if err := op(NewGitRunner(dir), context.Background(), branch); err != nil {
					t.Fatalf("%s(%q): %v", opName, branch, err)
				}
				if head := gitOut(t, dir, "rev-parse", "HEAD"); head != target {
					t.Errorf("HEAD = %s after %s(%q), want %s: the branch was not read as a revision", head, opName, branch, target)
				}
			})
		}
	}
}

// R5.2: a cancelled context stops the git child and the error wraps ctx.Err().
func TestRunnerCancelStopsGit(t *testing.T) {
	var mu sync.Mutex
	var spawned *exec.Cmd
	seam := func(ctx context.Context, name string, arg ...string) *exec.Cmd {
		c := exec.CommandContext(ctx, "sh", "-c", "exec sleep 60")
		mu.Lock()
		spawned = c
		mu.Unlock()
		return c
	}
	r := NewGitRunner(t.TempDir(), WithGitExecCommand(seam))
	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(200*time.Millisecond, cancel)

	done := make(chan error, 1)
	start := time.Now()
	go func() { done <- r.Fetch(ctx, "origin") }()
	var err error
	select {
	case err = <-done:
	case <-time.After(20 * time.Second):
		t.Fatal("Fetch still running 20 s after the cancel (B4)")
	}
	if took := time.Since(start); took > 200*time.Millisecond+6*time.Second {
		t.Errorf("Fetch returned %v after start, want within 6 s of the cancel (R5.2)", took)
	}
	if !errors.Is(err, context.Canceled) {
		t.Errorf("Fetch error = %v, want one wrapping context.Canceled", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if spawned == nil || spawned.ProcessState == nil {
		t.Error("the git child was never started and reaped")
	}
}

// R5.3 / R5.4: network ops get a 5-minute bound, local ops a 1-minute one.
// Both directions are hostile: a local op must not get 5 min, nor a network
// op 1 min. The bound is read from the context each git child receives.
func TestRunnerTimeoutNamesTheBound(t *testing.T) {
	const network, local = 5 * time.Minute, time.Minute
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "f.txt"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	ops := []struct {
		name  string
		bound time.Duration
		call  func(context.Context, *GitRunner) error
	}{
		{"Push", network, func(c context.Context, r *GitRunner) error { return r.Push(c) }},
		{"PushDryRun", network, func(c context.Context, r *GitRunner) error { _, err := r.PushDryRun(c); return err }},
		{"Fetch", network, func(c context.Context, r *GitRunner) error { return r.Fetch(c, "origin") }},
		{"Status", local, func(c context.Context, r *GitRunner) error { _, err := r.Status(c); return err }},
		{"StagedStatus", local, func(c context.Context, r *GitRunner) error { _, err := r.StagedStatus(c); return err }},
		{"Add", local, func(c context.Context, r *GitRunner) error { return r.Add(c, "f.txt") }},
		{"Commit", local, func(c context.Context, r *GitRunner) error { return r.Commit(c, "m", "u", "u@e.invalid") }},
		{"Merge", local, func(c context.Context, r *GitRunner) error { return r.Merge(c, "b") }},
		{"MergeFFOnly", local, func(c context.Context, r *GitRunner) error { return r.MergeFFOnly(c, "b") }},
		{"Rebase", local, func(c context.Context, r *GitRunner) error { return r.Rebase(c, "b") }},
		{"CurrentBranch", local, func(c context.Context, r *GitRunner) error { _, err := r.CurrentBranch(c); return err }},
		{"Upstream", local, func(c context.Context, r *GitRunner) error { _, err := r.Upstream(c); return err }},
		{"CountRange", local, func(c context.Context, r *GitRunner) error { _, err := r.CountRange(c, "a", "b"); return err }},
	}
	for _, op := range ops {
		t.Run(op.name, func(t *testing.T) {
			var budgets []time.Duration
			seam := func(ctx context.Context, name string, arg ...string) *exec.Cmd {
				dl, ok := ctx.Deadline()
				if !ok {
					budgets = append(budgets, -1)
				} else {
					budgets = append(budgets, time.Until(dl))
				}
				return exec.CommandContext(ctx, "true")
			}
			_ = op.call(context.Background(), NewGitRunner(dir, WithGitExecCommand(seam)))
			if len(budgets) == 0 {
				t.Fatalf("%s spawned no git child", op.name)
			}
			for _, b := range budgets {
				if b <= op.bound-10*time.Second || b > op.bound {
					t.Errorf("%s ran git with a %v budget, want %v (-1 = no deadline at all)", op.name, b, op.bound)
				}
			}
		})
	}
}

// R7.2: a failing git command puts at most the last 65536 bytes of its output
// in the error — and they are the LAST bytes (the end marker survives).
func TestRunnerErrorKeepsTheTail(t *testing.T) {
	seam := func(ctx context.Context, name string, arg ...string) *exec.Cmd {
		return exec.CommandContext(ctx, "sh", "-c",
			`head -c 300000 /dev/zero | tr '\0' a >&2; printf 'END-MARK' >&2; exit 1`)
	}
	err := NewGitRunner(t.TempDir(), WithGitExecCommand(seam)).Push(context.Background())
	if err == nil {
		t.Fatal("Push succeeded; the seam exits 1")
	}
	msg := err.Error()
	if len(msg) > 65536+1024 {
		t.Errorf("error text is %d bytes; want at most the last 65536 bytes of output plus framing (R7.2)", len(msg))
	}
	if !strings.Contains(msg, "END-MARK") {
		t.Error("error text lost the LAST bytes of the output; the tail, not the head, must be kept")
	}
}
