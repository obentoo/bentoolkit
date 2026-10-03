//go:build unix

package autoupdate

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
)

// Story 054, R3.3-R3.5 (B2): the privileged compile keeps sudo in the terminal's
// foreground group, asks it to stop with SIGTERM, and an interrupted compile is
// an interrupt — never ErrCompileFailed, never a job for the LLM build fixer.
//
// No real sudo runs. PATH holds a stand-in `sudo` so the privilege tool is
// deterministic on any host; the applier's exec seam turns that tool into a
// shell, and the attached-runner seam runs it with its output captured.

// compileTranscript makes the fixer reachable on a genuine failure: src_prepare
// started, so the attribution gate blames the ebuild rather than the machine.
const compileTranscript = `printf '>>> Preparing source in /var/tmp/portage/x/work ...\n>>> Source prepared.\n>>> Compiling source in /var/tmp/portage/x/work ...\nPARTIAL-TRANSCRIPT-054\n'`

// interruptibleCompile reports "$$ $!" through $0 and records SIGTERM in $1.
const interruptibleCompile = `PATH=/usr/bin:/bin
trap 'echo TERM > "$1"; kill $! 2>/dev/null; exit 143' TERM
` + compileTranscript + `
sleep 60 >/dev/null 2>&1 &
echo "$$ $!" > "$0"
wait $!`

// failingCompile is a genuine compile failure with the same transcript.
const failingCompile = compileTranscript + `; exit 1`

type countingBuildFixer struct{ calls atomic.Int32 }

func (f *countingBuildFixer) FixBuild(context.Context, BuildFixRequest) (BuildFixResult, error) {
	f.calls.Add(1)
	return BuildFixResult{}, errors.New("counting fixer: no change")
}

type compileHarness struct {
	applier  *Applier
	cand     candidatePaths
	ctx      context.Context
	cancel   context.CancelFunc
	fixer    *countingBuildFixer
	logsDir  string
	fifo     string
	termFile string
}

func newCompileHarness(t *testing.T, script string) *compileHarness {
	t.Helper()
	tmp := t.TempDir()
	fakeBin := filepath.Join(tmp, "bin")
	if err := os.MkdirAll(fakeBin, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(fakeBin, "sudo"), []byte("#!/bin/sh\nexit 99\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", fakeBin)

	const pkg = "media-plugins/gst-plugins-qt6"
	overlayDir := filepath.Join(tmp, "overlay")
	createTestEbuildFile(t, overlayDir, pkg, "1.28.6")
	stagedRoot := filepath.Join(tmp, "staging", "media-plugins", "gst-plugins-qt6", "1.29.2")
	pkgDir := filepath.Join(stagedRoot, "media-plugins", "gst-plugins-qt6")
	if err := os.MkdirAll(pkgDir, 0o755); err != nil {
		t.Fatal(err)
	}
	ebuild := filepath.Join(pkgDir, "gst-plugins-qt6-1.29.2.ebuild")
	if err := os.WriteFile(ebuild, []byte("EAPI=8\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	h := &compileHarness{
		fixer:    &countingBuildFixer{},
		logsDir:  filepath.Join(tmp, "logs"),
		fifo:     filepath.Join(tmp, "pid.fifo"),
		termFile: filepath.Join(tmp, "term"),
	}
	if err := syscall.Mkfifo(h.fifo, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(h.logsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	h.ctx = ctx
	h.cancel = cancel

	seam := func(ctx context.Context, name string, _ ...string) *exec.Cmd {
		if name == "sudo" {
			return exec.CommandContext(ctx, "/bin/sh", "-c", script, h.fifo, h.termFile)
		}
		return exec.CommandContext(ctx, "/bin/true")
	}
	runner := func(cmd *exec.Cmd) ([]byte, error) {
		var buf bytes.Buffer
		cmd.Stdout = &buf
		cmd.Stderr = &buf
		err := cmd.Run()
		return buf.Bytes(), err
	}
	a, err := NewApplier(overlayDir, filepath.Join(tmp, "config"),
		WithExecCommand(seam),
		WithApplierRunAttached(runner),
		WithConfirmFunc(func(string) bool { return true }),
		WithApplierIsolationProbe(func() (bool, string) { return true, "" }),
		WithApplierBuildFixer(h.fixer),
		WithLogsDir(h.logsDir),
	)
	if err != nil {
		t.Fatalf("NewApplier: %v", err)
	}
	h.applier = a
	h.cand = candidatePaths{repoRoot: stagedRoot, pkgDir: pkgDir, ebuildPath: ebuild, staged: true}
	return h
}

type compileOutcome struct {
	logPath string
	err     error
}

// runInterrupted starts the compile, waits until the child is running, lets
// during() observe it, cancels, and returns the outcome.
func (h *compileHarness) runInterrupted(t *testing.T, during func(childPID int)) compileOutcome {
	t.Helper()
	done := make(chan compileOutcome, 1)
	finished := make(chan struct{})
	go func() {
		defer close(finished)
		logPath, err := h.applier.runCompile(h.ctx, h.cand, "media-plugins/gst-plugins-qt6", "1.29.2", &ApplyResult{})
		done <- compileOutcome{logPath, err}
	}()
	t.Cleanup(func() {
		select {
		case <-finished:
		case <-time.After(70 * time.Second):
		}
	})

	raw, err := os.ReadFile(h.fifo)
	if err != nil {
		t.Fatalf("the compile child never reported its pid: %v", err)
	}
	fields := strings.Fields(string(raw))
	if len(fields) != 2 {
		t.Fatalf("compile child reported %q, want \"<pid> <sleep pid>\"", raw)
	}
	pids := make([]int, 2)
	for i, f := range fields {
		if pids[i], err = strconv.Atoi(f); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() {
		_ = syscall.Kill(pids[1], syscall.SIGKILL)
		_ = syscall.Kill(pids[0], syscall.SIGKILL)
	})
	if during != nil {
		during(pids[0])
	}

	h.cancel()
	select {
	case out := <-done:
		return out
	case <-time.After(20 * time.Second):
		t.Fatal("runCompile still blocked 20 s after cancel")
	}
	return compileOutcome{}
}

func TestCompileInterruptedIsNotAFailedBuild(t *testing.T) {
	h := newCompileHarness(t, interruptibleCompile)
	out := h.runInterrupted(t, nil)

	if out.err == nil {
		t.Fatal("an interrupted compile returned no error")
	}
	if errors.Is(out.err, ErrCompileFailed) {
		t.Errorf("interrupted compile error wraps ErrCompileFailed (%v): an interrupt is not a verdict on the ebuild (R3.5)", out.err)
	}
	if !errors.Is(out.err, context.Canceled) {
		t.Errorf("interrupted compile error = %v, want it to wrap ctx.Err() (context.Canceled) (R3.5)", out.err)
	}
	if !strings.Contains(out.err.Error(), "interrupted") {
		t.Errorf("interrupted compile error %q does not contain the word \"interrupted\" (R3.5)", out.err)
	}
}

func TestCompileInterruptedNeverInvokesTheFixer(t *testing.T) {
	// The hostile half first: the SAME transcript from a genuine failure reaches
	// the fixer, so the fixture can reach it and zero calls below means refusal.
	t.Run("a genuine failure still reaches the fixer", func(t *testing.T) {
		h := newCompileHarness(t, failingCompile)
		_, err := h.applier.runCompile(t.Context(), h.cand, "media-plugins/gst-plugins-qt6", "1.29.2", &ApplyResult{})
		if !errors.Is(err, ErrCompileFailed) {
			t.Errorf("a genuine compile failure = %v, want ErrCompileFailed", err)
		}
		if n := h.fixer.calls.Load(); n != 1 {
			t.Fatalf("a genuine compile failure invoked the fixer %d times, want 1 — the fixture cannot prove the interrupt refusal", n)
		}
	})
	t.Run("an interrupt never does", func(t *testing.T) {
		h := newCompileHarness(t, interruptibleCompile)
		h.runInterrupted(t, nil)
		if n := h.fixer.calls.Load(); n != 0 {
			t.Errorf("an interrupted compile invoked the LLM build fixer %d time(s), want 0 (R3.5)", n)
		}
	})
}

// R3.3 and R3.4 together: sudo shares the caller's (the terminal's foreground)
// process group, so its password prompt keeps working, and a cancel reaches it
// as SIGTERM it can relay — not as an unrelayable SIGKILL.
func TestCompileChildStaysInTheForegroundGroup(t *testing.T) {
	h := newCompileHarness(t, interruptibleCompile)
	h.runInterrupted(t, func(child int) {
		pgid, err := syscall.Getpgid(child)
		if err != nil {
			t.Fatalf("getpgid(%d): %v", child, err)
		}
		if pgid != syscall.Getpgrp() {
			t.Errorf("the privilege tool runs in process group %d, want the caller's %d — a child outside the foreground group cannot prompt for a password (R3.3)", pgid, syscall.Getpgrp())
		}
	})
	got, err := os.ReadFile(h.termFile)
	if err != nil || strings.TrimSpace(string(got)) != "TERM" {
		t.Errorf("the privilege tool never received SIGTERM on cancel (read %q, %v): it was killed outright, so sudo had no chance to relay the stop to ebuild (R3.4)", got, err)
	}
}

// R3.6. Green today (the log is written on any compile error); it guards the fix,
// which must not take an early return that skips the retained log.
func TestCompileInterruptedKeepsTheLog(t *testing.T) {
	h := newCompileHarness(t, interruptibleCompile)
	h.runInterrupted(t, nil)

	entries, err := os.ReadDir(h.logsDir)
	if err != nil {
		t.Fatalf("reading the logs dir: %v", err)
	}
	for _, e := range entries {
		b, rerr := os.ReadFile(filepath.Join(h.logsDir, e.Name()))
		if rerr == nil && bytes.Contains(b, []byte("PARTIAL-TRANSCRIPT-054")) {
			return
		}
	}
	t.Errorf("no retained compile log in %s holds the interrupted compile's partial transcript (%d file(s)) (R3.6)", h.logsDir, len(entries))
}
