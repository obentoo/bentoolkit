//go:build unix

package validate

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// Story 054, R3.1 / R3.2 (B2): the unprivileged `ebuild` build gate runs in its
// own process group, dies with its descendants on cancel, and never reads the
// terminal.

// realBuildRequest is buildRequestFor with a staged root that exists, because
// here the child really starts in it.
func realBuildRequest(t *testing.T) BuildRequest {
	t.Helper()
	req := buildRequestFor(t, DepthConfigure)
	if err := os.MkdirAll(req.StagedRoot, 0o755); err != nil {
		t.Fatalf("creating the staged root: %v", err)
	}
	return req
}

// fakeEbuildDeps builds deps whose "ebuild" is `sh -c script arg0`; runner nil
// keeps the package's default attached runner.
func fakeEbuildDeps(script, arg0 string, runner func(*exec.Cmd) ([]byte, error)) BuildDeps {
	return BuildDeps{
		ExecCommand: func(ctx context.Context, _ string, _ ...string) *exec.Cmd {
			return exec.CommandContext(ctx, "sh", "-c", script, arg0)
		},
		RunAttached:    runner,
		LookPath:       func(name string) (string, error) { return "/usr/bin/" + name, nil },
		IsolationProbe: func() (bool, string) { return true, "" },
	}
}

func waitFinished(finished <-chan struct{}) {
	select {
	case <-finished:
	case <-time.After(70 * time.Second):
	}
}

func buildPIDGone(pid int) bool {
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

func TestRunBuildGatesCancelStopsTheBuildGroup(t *testing.T) {
	req := realBuildRequest(t)
	fifo := filepath.Join(t.TempDir(), "pid.fifo")
	if err := syscall.Mkfifo(fifo, 0o600); err != nil {
		t.Fatal(err)
	}
	// "ebuild" starts a make/gcc stand-in that inherits the captured output and
	// reports its pid, then waits on it.
	deps := fakeEbuildDeps(`sh -c 'echo $$ > "$0"; exec sleep 60' "$0" & wait`, fifo, nil)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	type result struct {
		gates []GateResult
		err   error
	}
	done := make(chan result, 1)
	finished := make(chan struct{})
	go func() {
		defer close(finished)
		g, err := RunBuildGates(ctx, req, deps)
		done <- result{g, err}
	}()
	// Registered before the grandchild's kill, so it runs after it: the build
	// has returned (and written its log) before t.TempDir is removed.
	t.Cleanup(func() { waitFinished(finished) })

	raw, err := os.ReadFile(fifo)
	if err != nil {
		t.Fatal(err)
	}
	grandchild, err := strconv.Atoi(strings.TrimSpace(string(raw)))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = syscall.Kill(grandchild, syscall.SIGKILL) })

	stoppedAt := time.Now()
	cancel()
	var res result
	select {
	case res = <-done:
	case <-time.After(20 * time.Second):
		t.Fatal("RunBuildGates still blocked 20 s after cancel: a build descendant holding the output pipe keeps it open (B2, R3.1)")
	}
	if took := time.Since(stoppedAt); took > 6*time.Second {
		t.Errorf("RunBuildGates returned %v after cancel, want within 6 s (R3.1)", took)
	}
	if !errors.Is(res.err, context.Canceled) {
		t.Errorf("RunBuildGates error = %v, want it to wrap context.Canceled (an interrupt, not a verdict)", res.err)
	}
	if len(res.gates) != 0 {
		t.Errorf("an interrupted build produced %d gate(s): %+v", len(res.gates), res.gates)
	}
	tick := time.NewTicker(10 * time.Millisecond)
	defer tick.Stop()
	for limit := time.Now().Add(time.Second); !buildPIDGone(grandchild); <-tick.C {
		if time.Now().After(limit) {
			t.Errorf("build descendant %d still running after RunBuildGates returned: the build's group was not stopped (R3.1)", grandchild)
			break
		}
	}
}

// The runner stands in for the CLI's attached runner, following the attached-run
// convention (tui.RunAttached): a stream the caller left nil is wired to the
// terminal, a stream the caller set is kept. The "terminal" is a pipe holding
// unread bytes and never closed, so a child handed it reads those bytes, and a
// child given an empty stdin reads EOF at once.
func TestRunBuildGatesChildReadsEOF(t *testing.T) {
	req := realBuildRequest(t)
	ttyR, ttyW, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ttyR.Close(); _ = ttyW.Close() })
	if _, err := ttyW.WriteString("typed-at-the-terminal\n"); err != nil {
		t.Fatal(err)
	}
	var handedTerminal bool
	terminalRunner := func(cmd *exec.Cmd) ([]byte, error) {
		var buf bytes.Buffer
		if cmd.Stdin == nil {
			cmd.Stdin = ttyR
			handedTerminal = true
		}
		if cmd.Stdout == nil {
			cmd.Stdout = &buf
		}
		if cmd.Stderr == nil {
			cmd.Stderr = &buf
		}
		err := cmd.Run()
		return buf.Bytes(), err
	}

	seen := filepath.Join(t.TempDir(), "stdin-seen")
	deps := fakeEbuildDeps(
		`if IFS= read -r line; then printf 'READ:%s\n' "$line" > "$0"; else printf 'EOF\n' > "$0"; fi`,
		seen, terminalRunner)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_, _ = RunBuildGates(ctx, req, deps) // the verdict on this fake log is not the subject

	got, err := os.ReadFile(seen)
	if err != nil {
		t.Fatalf("the fake ebuild never recorded what its stdin held: %v", err)
	}
	if strings.TrimSpace(string(got)) != "EOF" {
		t.Errorf("the build child read %q from stdin, want immediate EOF — an unprivileged build must never be handed the terminal (R3.2)", strings.TrimSpace(string(got)))
	}
	if handedTerminal {
		t.Errorf("the build gate left the child's stdin unset, so an attached runner wires the terminal to it (R3.2: a background-group child reading the tty stops on SIGTTIN)")
	}
}
