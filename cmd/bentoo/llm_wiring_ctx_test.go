//go:build unix

package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/obentoo/bentoolkit/internal/autoupdate/llm"
	"github.com/obentoo/bentoolkit/internal/common/config"
)

// TestConfiguredClaudeProviderStopsOnCancel pins story 054 R4.3 at the one
// seam `overlay analyze`, `overlay autoupdate --check` and the revive modes all
// build their LLM provider through.
//
// Since the `claude` child runs in its own process group, a Ctrl+C typed at the
// terminal no longer reaches it: the kernel sends SIGINT to the terminal's
// foreground group only. The context handed to the provider's call is then
// the ONLY way an interrupt stops that child, so cancelling it must stop the
// whole group within 6 s. Before story 054 the client ran under
// context.Background(): a cancel changed nothing and the call waited out the
// CLI's 120 s budget. Since story 059 the provider stores no context, so the
// one that counts is the one passed to ExtractVersion.
//
// The two converses run as subtests so the sub-task's `-run` gate, which names
// this test only, cannot pass without them.
func TestConfiguredClaudeProviderStopsOnCancel(t *testing.T) {
	t.Run("a cancel stops the claude group", testClaudeProviderCancelStopsGroup)
	t.Run("a live context still gets its answer", testClaudeProviderLiveContextAnswers)
	t.Run("an absent claude is a true nil", testClaudeProviderAbsentIsATrueNil)
}

// testClaudeProviderCancelStopsGroup: the stub's grandchild inherits the stdout
// pipe, so the call cannot return while that grandchild lives — a prompt return
// proves the whole group was stopped, not only the shell the client started.
func testClaudeProviderCancelStopsGroup(t *testing.T) {
	pidFile := filepath.Join(t.TempDir(), "grandchild.pid")
	writeClaudeStubOnPath(t, "sleep 60 &\necho $! > '"+pidFile+"'\nwait\n")

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	p, err := newConfiguredLLMProvider(discardLog(), config.LLMConfig{Provider: "claude-code"})
	if err != nil {
		t.Fatalf("newConfiguredLLMProvider(discardLog(), claude-code) with a stub claude on PATH: %v", err)
	}
	if p == nil {
		t.Fatal("newConfiguredLLMProvider(discardLog(), claude-code) returned no provider and no error")
	}

	finished := make(chan struct{})
	var gotVersion string
	var gotErr error
	go func() {
		defer close(finished)
		gotVersion, gotErr = p.ExtractVersion(ctx, []byte("<html>foo-1.2.3.tar.gz</html>"), "")
	}()
	// Failure-path hygiene: if the context never reaches the client, cancel()
	// stops nothing, so the stub's group is killed here instead of outliving
	// the test, and the call is given a bounded wait to return.
	t.Cleanup(func() {
		killClaudeStubGroup(pidFile)
		select {
		case <-finished:
		case <-time.After(10 * time.Second):
		}
	})

	// Cancel only once the grandchild is known to hold the pipe: its recorded
	// PID is that event. No elapsed floor is added on top — no assertion below
	// depends on how long the call ran before the cancel.
	grandchild := readClaudeStubPID(t, pidFile, 10*time.Second)
	cancelledAt := time.Now()
	cancel()

	select {
	case <-finished:
	case <-time.After(6 * time.Second):
		t.Fatal("the provider call was still running 6 s after its context was cancelled: " +
			"the context given to ExtractVersion does not reach the claude client (R4.3)")
	}
	t.Logf("provider call returned %v after the cancel", time.Since(cancelledAt).Round(time.Millisecond))

	if gotErr == nil {
		t.Fatalf("ExtractVersion returned %q and no error after its context was cancelled", gotVersion)
	}
	// The client frames a run its parent ended as ErrLLMRequestFailed and names
	// the parent's cause. Either spelling of that cause is the cancellation.
	if !errors.Is(gotErr, context.Canceled) && !strings.Contains(gotErr.Error(), context.Canceled.Error()) {
		t.Errorf("ExtractVersion error = %v; want it to name the cancellation (%v)", gotErr, context.Canceled)
	}
	if !errors.Is(gotErr, llm.ErrLLMRequestFailed) {
		t.Errorf("ExtractVersion error = %v; want it to wrap %v", gotErr, llm.ErrLLMRequestFailed)
	}

	deadline := time.Now().Add(time.Second)
	for !claudeStubProcessGone(grandchild) {
		if time.Now().After(deadline) {
			t.Fatalf("grandchild %d of the claude stub still runs 1 s after the call returned: the group was not stopped", grandchild)
		}
		time.Sleep(20 * time.Millisecond) // polling: the stub's grandchild process has exited
	}
}

// testClaudeProviderLiveContextAnswers is the hostile converse: wiring a
// cancellable context into the client must not cost an answer. A provider
// built with a live, never-cancelled context and a stub that replies at once
// still returns that reply.
func testClaudeProviderLiveContextAnswers(t *testing.T) {
	writeClaudeStubOnPath(t, "cat >/dev/null\n"+
		`printf '%s\n' '{"type":"result","subtype":"success","is_error":false,"result":"1.2.3"}'`+"\n")

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	p, err := newConfiguredLLMProvider(discardLog(), config.LLMConfig{Provider: "claude-code"})
	if err != nil {
		t.Fatalf("newConfiguredLLMProvider(discardLog(), claude-code) with a stub claude on PATH: %v", err)
	}
	if p == nil {
		t.Fatal("newConfiguredLLMProvider(discardLog(), claude-code) returned no provider and no error")
	}

	got, err := p.ExtractVersion(ctx, []byte("<html>foo-1.2.3.tar.gz</html>"), "")
	if err != nil {
		t.Fatalf("ExtractVersion with a live context: %v", err)
	}
	if got != "1.2.3" {
		t.Errorf("ExtractVersion = %q, want %q", got, "1.2.3")
	}
}

// testClaudeProviderAbsentIsATrueNil keeps the err-first guard in runAnalyze,
// runCheck and reviveCheckerOptions honest: when the claude-code client cannot
// be built, the provider returned beside the error is nil all the way down,
// never a nil *ClaudeCodeClient boxed into a non-nil interface.
func testClaudeProviderAbsentIsATrueNil(t *testing.T) {
	t.Setenv("PATH", t.TempDir())

	p, err := newConfiguredLLMProvider(discardLog(), config.LLMConfig{Provider: "claude-code"})
	if !errors.Is(err, llm.ErrClaudeCodeUnavailable) {
		t.Fatalf("newConfiguredLLMProvider(discardLog(), claude-code) with no claude on PATH: error = %v, want %v",
			err, llm.ErrClaudeCodeUnavailable)
	}
	if !isTrueNil(p) {
		t.Errorf("provider returned beside the error is a boxed %T, want a true nil", p)
	}
}

// writeClaudeStubOnPath installs an executable `claude` shell script with the
// given body first on PATH for the rest of the test. NewClaudeCodeClient only
// checks that the binary resolves on PATH, so the stub needs no --version reply.
func writeClaudeStubOnPath(t *testing.T, body string) {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "claude"), []byte("#!/bin/sh\n"+body), 0o755); err != nil {
		t.Fatalf("writing the claude stub: %v", err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

// readClaudeStubPID waits for the stub to record its grandchild's PID.
func readClaudeStubPID(t *testing.T, pidFile string, wait time.Duration) int {
	t.Helper()
	deadline := time.Now().Add(wait)
	for {
		if raw, err := os.ReadFile(pidFile); err == nil {
			if pid, convErr := strconv.Atoi(strings.TrimSpace(string(raw))); convErr == nil && pid > 0 {
				return pid
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("the claude stub never recorded its grandchild's PID in %s within %v", pidFile, wait)
		}
		time.Sleep(10 * time.Millisecond) // polling: the stub has written its grandchild's PID file
	}
}

// killClaudeStubGroup SIGKILLs the process group of the grandchild recorded in
// pidFile, if it still runs. It never signals the test binary's own group.
func killClaudeStubGroup(pidFile string) {
	raw, err := os.ReadFile(pidFile)
	if err != nil {
		return
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(raw)))
	if err != nil || pid <= 1 {
		return
	}
	if pgid, err := syscall.Getpgid(pid); err == nil && pgid > 1 && pgid != syscall.Getpgrp() {
		_ = syscall.Kill(-pgid, syscall.SIGKILL)
		return
	}
	_ = syscall.Kill(pid, syscall.SIGKILL)
}

// claudeStubProcessGone reports whether pid no longer runs: it is either gone
// (ESRCH) or a zombie nobody has reaped yet. A zombie runs no code and holds no
// pipe; how soon it is reaped depends on which ancestor adopted it, not on
// bentoo. Where /proc cannot be read, only ESRCH counts.
func claudeStubProcessGone(pid int) bool {
	if err := syscall.Kill(pid, 0); errors.Is(err, syscall.ESRCH) {
		return true
	}
	stat, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	if err != nil {
		return false
	}
	// The state is the first field after the command name, which is wrapped in
	// parentheses and may itself contain spaces or parentheses.
	s := string(stat)
	i := strings.LastIndexByte(s, ')')
	return i >= 0 && i+2 < len(s) && (s[i+2] == 'Z' || s[i+2] == 'X')
}
