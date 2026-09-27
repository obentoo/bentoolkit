//go:build unix

package provider

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// Story 054: what gitclone_ctx_test.go leaves unpinned. The bound's own expiry
// and its text (S054-R5.5), group mode on the fetch and the reset as well as
// the pull (S054-R5.9), the separators on every argv the provider builds
// (S054-R8.11 for the clone's "--"), and a git child that never waits on a
// terminal prompt.

// shortenGitTimeout shortens the provider's bound for one test. A short caller
// deadline cannot stand in for it: then the caller's deadline expires, not the
// bound.
func shortenGitTimeout(t *testing.T, d time.Duration) {
	t.Helper()
	orig := gitTimeout
	t.Cleanup(func() { gitTimeout = orig })
	gitTimeout = d
}

// gitOp returns the git subcommand an argv runs, skipping a leading "-C <dir>".
func gitOp(args []string) string {
	if len(args) >= 2 && args[0] == "-C" {
		args = args[2:]
	}
	if len(args) == 0 {
		return ""
	}
	return args[0]
}

// waitGone fails the test unless process pid, described by what, is gone or a
// zombie within a second.
func waitGone(t *testing.T, pid int, what string) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for syscall.Kill(pid, 0) == nil && !isZombie(pid) {
		if time.Now().After(deadline) {
			t.Errorf("%s (pid %d) survived the bound: that git command is not run in group mode (S054-R5.9)", what, pid)
			_ = syscall.Kill(pid, syscall.SIGKILL)
			return
		}
		time.Sleep(10 * time.Millisecond) // polling kernel state, not synchronising goroutines
	}
}

// When the shared bound elapses, updateRepo stops the git command that is
// running together with the helper it started, names that command and the
// bound, wraps context.DeadlineExceeded, and starts no further command.
func TestUpdateRepoBoundStopsTheRunningCommand(t *testing.T) {
	const bound = 500 * time.Millisecond
	for _, op := range []string{"pull", "fetch", "reset"} {
		t.Run(op, func(t *testing.T) {
			shortenGitTimeout(t, bound)
			dir := t.TempDir()
			pidFile := filepath.Join(dir, "helper.pid")
			var ran []string
			swapExecCommand(t, func(ctx context.Context, _ string, arg ...string) *exec.Cmd {
				ran = append(ran, gitOp(arg))
				switch gitOp(arg) {
				case op:
					// The helper a transport would be: a kill aimed at the direct
					// child alone leaves it running.
					return exec.CommandContext(ctx, "sh", "-c", `sleep 30 & echo $! > "$0"; wait`, pidFile)
				case "pull":
					return exec.CommandContext(ctx, "false") // so the fetch and the reset run
				default:
					return exec.CommandContext(ctx, "true")
				}
			})
			prov := &GitCloneProvider{LocalPath: dir, RepoURL: "https://example.invalid/r.git", Branch: "master"}

			start := time.Now()
			err := prov.updateRepo(context.Background())
			took := time.Since(start)

			if !errors.Is(err, context.DeadlineExceeded) {
				t.Fatalf("updateRepo error = %v, want one wrapping context.DeadlineExceeded (S054-R5.5)", err)
			}
			want := fmt.Sprintf("failed to update repository %s: git %s timed out after %s", dir, op, bound)
			if !strings.Contains(err.Error(), want) {
				t.Errorf("updateRepo error = %q, want it to contain %q (S054-R5.5)", err, want)
			}
			if took > bound+6*time.Second {
				t.Errorf("updateRepo returned %v after it started, want within the %v bound + 6 s", took, bound)
			}
			if last := ran[len(ran)-1]; last != op {
				t.Errorf("git commands started: %v; want none after git %s timed out", ran, op)
			}
			raw, readErr := os.ReadFile(pidFile)
			if readErr != nil {
				t.Fatalf("the helper never reported its pid: %v", readErr)
			}
			pid, convErr := strconv.Atoi(strings.TrimSpace(string(raw)))
			if convErr != nil {
				t.Fatalf("helper pid %q: %v", raw, convErr)
			}
			waitGone(t, pid, "the helper git "+op+" started")
		})
	}
}

// A caller's own deadline expiring is the caller's: updateRepo reports it as
// such, never as the bound's "timed out after".
func TestUpdateRepoCallerDeadlineIsNotTheBound(t *testing.T) {
	swapExecCommand(t, func(ctx context.Context, _ string, _ ...string) *exec.Cmd {
		return exec.CommandContext(ctx, "sleep", "30")
	})
	prov := &GitCloneProvider{LocalPath: t.TempDir(), RepoURL: "https://example.invalid/r.git", Branch: "master"}

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	err := prov.updateRepo(ctx)

	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("updateRepo error = %v, want one wrapping context.DeadlineExceeded", err)
	}
	if strings.Contains(err.Error(), "timed out after") {
		t.Errorf("updateRepo error = %q: the caller's deadline expired, not the %v bound", err, gitTimeout)
	}
	if !strings.Contains(err.Error(), "git pull") {
		t.Errorf("updateRepo error = %q, want it to name git pull", err)
	}
}

// A clone that outlives its bound is stopped together with the helper it
// started, still fails with ErrCloneFailed (S054-R8.11), and its error also
// wraps context.DeadlineExceeded and names the clone and the bound
// (S054-R5.5, S054-R5.10).
func TestCloneRepoBoundNamesTheClone(t *testing.T) {
	const bound = 300 * time.Millisecond
	shortenGitTimeout(t, bound)
	dir := t.TempDir()
	pidFile := filepath.Join(dir, "helper.pid")
	swapExecCommand(t, func(ctx context.Context, _ string, _ ...string) *exec.Cmd {
		return exec.CommandContext(ctx, "sh", "-c", `sleep 30 & echo $! > "$0"; wait`, pidFile)
	})
	dest := filepath.Join(dir, "repo")
	prov := &GitCloneProvider{LocalPath: dest, RepoURL: "https://example.invalid/r.git", Branch: "master"}

	start := time.Now()
	err := prov.cloneRepo(context.Background())
	took := time.Since(start)

	if !errors.Is(err, ErrCloneFailed) || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("cloneRepo error = %v, want one wrapping ErrCloneFailed and context.DeadlineExceeded", err)
	}
	want := fmt.Sprintf("cloning into %s: git clone timed out after %s", dest, bound)
	if !strings.Contains(err.Error(), want) {
		t.Errorf("cloneRepo error = %q, want it to contain %q", err, want)
	}
	if took > bound+6*time.Second {
		t.Errorf("cloneRepo returned %v after it started, want within the %v bound + 6 s", took, bound)
	}
	raw, readErr := os.ReadFile(pidFile)
	if readErr != nil {
		t.Fatalf("the helper never reported its pid: %v", readErr)
	}
	pid, convErr := strconv.Atoi(strings.TrimSpace(string(raw)))
	if convErr != nil {
		t.Fatalf("helper pid %q: %v", raw, convErr)
	}
	waitGone(t, pid, "the helper git clone started")
}

// A failure git reports itself names the repository and the operation that
// failed (Q4): a failed fetch reports both the pull's and the fetch's error,
// with the pull's output cut to its last tui.MaxCapturedBytes (S054-R7.2), and a
// failed reset reports the reset and its ref.
func TestUpdateRepoFailureNamesTheRepository(t *testing.T) {
	for _, tc := range []struct {
		name       string
		fetchFails bool
		wantPrefix string
	}{
		{name: "fetch fails", fetchFails: true, wantPrefix: "failed to update repository %s: git pull: exit status 1; git fetch: exit status 1: [... "},
		{name: "reset fails", fetchFails: false, wantPrefix: "failed to update repository %s: git reset --hard origin/master: exit status 1"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			swapExecCommand(t, func(ctx context.Context, _ string, arg ...string) *exec.Cmd {
				switch gitOp(arg) {
				case "pull": // 200 000 bytes of output, then the last line, then a failure
					return exec.CommandContext(ctx, "sh", "-c", `head -c 200000 /dev/zero | tr '\0' a; echo; echo PULL-TAIL-7Q; exit 1`)
				case "fetch":
					if tc.fetchFails {
						return exec.CommandContext(ctx, "false")
					}
					return exec.CommandContext(ctx, "true")
				default:
					return exec.CommandContext(ctx, "false")
				}
			})
			prov := &GitCloneProvider{LocalPath: t.TempDir(), RepoURL: "https://example.invalid/r.git", Branch: "master"}
			wantPrefix := fmt.Sprintf(tc.wantPrefix, prov.LocalPath)

			err := prov.updateRepo(context.Background())

			if err == nil || !strings.HasPrefix(err.Error(), wantPrefix) {
				t.Fatalf("updateRepo error = %.200q, want it to start with %q", err, wantPrefix)
			}
			if !tc.fetchFails {
				return
			}
			if !strings.HasSuffix(err.Error(), "PULL-TAIL-7Q\n") {
				t.Errorf("updateRepo error ends %q, want the pull output's last line", err.Error()[max(0, len(err.Error())-40):])
			}
			if n := len(err.Error()); n > 65536+200 {
				t.Errorf("updateRepo error is %d bytes, want at most the last 65536 bytes of output plus the message (S054-R7.2)", n)
			}
		})
	}
}

// Every argv the provider builds keeps names apart from options: the ref
// reaches fetch and reset after --end-of-options, and the clone's URL and
// path come after "--" (S054-R8.11). The names here begin with "-", which
// NewGitCloneProvider would refuse, but a struct literal can still carry them.
func TestGitArgvSeparatesNamesFromOptions(t *testing.T) {
	var argvs [][]string
	swapExecCommand(t, func(ctx context.Context, name string, arg ...string) *exec.Cmd {
		argvs = append(argvs, append([]string{name}, arg...))
		if gitOp(arg) == "pull" {
			return exec.CommandContext(ctx, "false") // so the fetch and the reset run
		}
		return exec.CommandContext(ctx, "true")
	})
	dir := t.TempDir()
	dest := filepath.Join(dir, "clone")
	update := &GitCloneProvider{LocalPath: dir, Branch: "-x"}
	if err := update.updateRepo(context.Background()); err != nil {
		t.Fatalf("updateRepo: %v", err)
	}
	clone := &GitCloneProvider{LocalPath: dest, RepoURL: "-u", Branch: "-x"}
	if err := clone.cloneRepo(context.Background()); err != nil {
		t.Fatalf("cloneRepo: %v", err)
	}

	want := [][]string{
		{"git", "-C", dir, "pull", "--ff-only"},
		{"git", "-C", dir, "fetch", "--end-of-options", "origin", "-x"},
		{"git", "-C", dir, "reset", "--hard", "--end-of-options", "origin/-x"},
		{"git", "clone", "--depth", "1", "--single-branch", "--branch", "-x", "--", "-u", dest},
	}
	if !reflect.DeepEqual(argvs, want) {
		t.Errorf("git argvs:\n got %q\nwant %q", argvs, want)
	}
}

// Every git child runs with GIT_TERMINAL_PROMPT=0, even when the operator's
// environment says otherwise: a credential prompt fails at once instead of
// stopping a child outside the terminal's foreground group until the bound.
func TestGitChildrenNeverPrompt(t *testing.T) {
	t.Setenv("GIT_TERMINAL_PROMPT", "1")
	dir := t.TempDir()
	logFile := filepath.Join(dir, "prompt.log")
	swapExecCommand(t, func(ctx context.Context, _ string, arg ...string) *exec.Cmd {
		status := "0"
		if gitOp(arg) == "pull" {
			status = "1" // so the fetch and the reset run
		}
		return exec.CommandContext(ctx, "sh", "-c",
			`echo "$1 GIT_TERMINAL_PROMPT=$GIT_TERMINAL_PROMPT" >> "$0"; exit "$2"`, logFile, gitOp(arg), status)
	})
	prov := &GitCloneProvider{LocalPath: filepath.Join(dir, "repo"), RepoURL: "https://example.invalid/r.git", Branch: "master"}
	if err := prov.updateRepo(context.Background()); err != nil {
		t.Fatalf("updateRepo: %v", err)
	}
	if err := prov.cloneRepo(context.Background()); err != nil {
		t.Fatalf("cloneRepo: %v", err)
	}

	raw, err := os.ReadFile(logFile)
	if err != nil {
		t.Fatal(err)
	}
	want := "pull GIT_TERMINAL_PROMPT=0\nfetch GIT_TERMINAL_PROMPT=0\nreset GIT_TERMINAL_PROMPT=0\nclone GIT_TERMINAL_PROMPT=0\n"
	if string(raw) != want {
		t.Errorf("environment the git children saw:\n%s\nwant:\n%s", raw, want)
	}
}

// runGit runs the host's git for a test fixture and fails the test on error.
func runGit(t *testing.T, args ...string) {
	t.Helper()
	if out, err := exec.CommandContext(t.Context(), "git", args...).CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

// newClonedRepo returns a clone of a local bare repository whose branch
// "main" holds one empty change, in a git environment that reads no user or
// system configuration.
func newClonedRepo(t *testing.T) string {
	t.Helper()
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	for _, k := range []string{"GIT_AUTHOR_NAME", "GIT_COMMITTER_NAME"} {
		t.Setenv(k, "tester")
	}
	for _, k := range []string{"GIT_AUTHOR_EMAIL", "GIT_COMMITTER_EMAIL"} {
		t.Setenv(k, "tester@example.invalid")
	}
	root := t.TempDir()
	src := filepath.Join(root, "src")
	remote := filepath.Join(root, "remote.git")
	local := filepath.Join(root, "local")
	runGit(t, "init", "-q", "-b", "main", src)
	runGit(t, "-C", src, "commit", "-q", "--allow-empty", "-m", "c1")
	runGit(t, "clone", "-q", "--bare", src, remote)
	runGit(t, "clone", "-q", remote, local)
	return local
}

// Against the host's real git: after --end-of-options a branch "-x" is a ref
// git cannot find, where without the separator git refuses "-x" as an unknown
// switch. The hostile converse: an ordinary branch still fetches and resets.
func TestUpdateRepoFetchReadsADashBranchAsARef(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skipf("no git on PATH: %v", err)
	}
	local := newClonedRepo(t)

	for _, tc := range []struct {
		branch     string
		wantErr    bool
		wantStderr string
	}{
		{branch: "-x", wantErr: true, wantStderr: "couldn't find remote ref -x"},
		{branch: "main", wantErr: false},
	} {
		t.Run(tc.branch, func(t *testing.T) {
			var fetchStderr bytes.Buffer
			swapExecCommand(t, func(ctx context.Context, name string, arg ...string) *exec.Cmd {
				if gitOp(arg) == "pull" {
					return exec.CommandContext(ctx, "false") // so the real fetch and reset run
				}
				cmd := exec.CommandContext(ctx, name, arg...)
				if gitOp(arg) == "fetch" {
					cmd.Stderr = &fetchStderr
				}
				return cmd
			})
			prov := &GitCloneProvider{LocalPath: local, Branch: tc.branch}

			err := prov.updateRepo(context.Background())

			if (err != nil) != tc.wantErr {
				t.Fatalf("updateRepo error = %v, want an error: %t (fetch stderr: %s)", err, tc.wantErr, fetchStderr.String())
			}
			stderr := fetchStderr.String()
			if strings.Contains(stderr, "unknown switch") {
				t.Errorf("git fetch read the branch as an option:\n%s", stderr)
			}
			if !strings.Contains(stderr, tc.wantStderr) {
				t.Errorf("git fetch stderr = %q, want it to contain %q", stderr, tc.wantStderr)
			}
		})
	}
}

// A pull that exits 0 while a helper it left behind still holds its output (an
// ssh ControlPersist master inherits git's stderr) succeeds once group mode's
// WaitDelay stops waiting for the pipe: procgroup.Result reads that as the
// success it is, so no fetch or reset follows (S054-R1.5). It takes
// procgroup.GracePeriod, which only procgroup's own tests can shorten.
func TestUpdateRepoPullWithALingeringHelperSucceeds(t *testing.T) {
	pidFile := filepath.Join(t.TempDir(), "helper.pid")
	t.Cleanup(func() {
		if raw, err := os.ReadFile(pidFile); err == nil {
			if pid, convErr := strconv.Atoi(strings.TrimSpace(string(raw))); convErr == nil {
				_ = syscall.Kill(pid, syscall.SIGKILL)
			}
		}
	})
	var ran []string
	swapExecCommand(t, func(ctx context.Context, _ string, arg ...string) *exec.Cmd {
		ran = append(ran, gitOp(arg))
		if gitOp(arg) == "pull" {
			return exec.CommandContext(ctx, "sh", "-c", `sleep 30 & echo $! > "$0"; exit 0`, pidFile)
		}
		return exec.CommandContext(ctx, "false")
	})
	prov := &GitCloneProvider{LocalPath: t.TempDir(), RepoURL: "https://example.invalid/r.git", Branch: "master"}

	if err := prov.updateRepo(context.Background()); err != nil {
		t.Fatalf("updateRepo error = %v, want nil: the pull succeeded", err)
	}
	if !reflect.DeepEqual(ran, []string{"pull"}) {
		t.Errorf("git commands started: %v, want only the pull", ran)
	}
}

// The bound is five minutes and prints as the "5m0s" S054-R5.5 names; the
// tests above shorten only the variable, and restore it.
func TestGitBoundIsTheStoryBound(t *testing.T) {
	if DefaultGitCloneTimeout.String() != "5m0s" || gitTimeout != DefaultGitCloneTimeout {
		t.Errorf("DefaultGitCloneTimeout = %v, gitTimeout = %v; want both 5m0s (S054-R5.10)", DefaultGitCloneTimeout, gitTimeout)
	}
}
