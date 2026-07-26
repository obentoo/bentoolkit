package snapshot

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

// TestExecRunner_PipesStdin verifies stdin is delivered on the process's standard
// input (via `cat`) rather than as an argv element.
func TestExecRunner_PipesStdin(t *testing.T) {
	out, err := execRunner{}.Run(t.Context(), "cat", nil, []byte("hello-from-stdin"))
	if err != nil {
		t.Fatalf("Run cat: %v", err)
	}
	if string(out) != "hello-from-stdin" {
		t.Errorf("stdout = %q, want piped stdin echoed back", out)
	}
}

// TestExecRunner_ContextCancelKills asserts that cancelling the parent context
// kills an in-flight child (G5, R8.1): a 5s sleep must return promptly after the
// context is cancelled, not run to completion.
func TestExecRunner_ContextCancelKills(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	go func() {
		time.Sleep(100 * time.Millisecond)
		cancel()
	}()

	start := time.Now()
	_, err := execRunner{}.Run(ctx, "sh", []string{"-c", "sleep 5"}, nil)
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("expected error from killed child, got nil")
	}
	if elapsed > 2*time.Second {
		t.Errorf("child not killed promptly: elapsed = %v", elapsed)
	}
}

// TestRunnerEnv_SetsLCAllC verifies the pure helper appends LC_ALL=C to the
// parent environment (R4.1) and that it lands last, which is what makes it win:
// os/exec keeps only the final value of a duplicated key, so a host LC_ALL is
// overridden rather than merely accompanied.
func TestRunnerEnv_SetsLCAllC(t *testing.T) {
	t.Setenv("LC_ALL", "pt_BR.UTF-8")

	env := runnerEnv()

	var found bool
	var lastLCAll string
	for _, kv := range env {
		if kv == "LC_ALL=C" {
			found = true
		}
		if strings.HasPrefix(kv, "LC_ALL=") {
			lastLCAll = kv
		}
	}
	if !found {
		t.Fatalf("runnerEnv() does not contain %q", "LC_ALL=C")
	}
	if lastLCAll != "LC_ALL=C" {
		t.Errorf("last LC_ALL entry = %q, want LC_ALL=C so it overrides the host locale", lastLCAll)
	}
}

// TestExecRunner_ForcesLCAllCInChild is the end-to-end counterpart of
// TestRunnerEnv_SetsLCAllC: a real execRunner must hand LC_ALL=C to the child
// process even when the parent exports a different locale (R4.1) — this is the
// choke point that keeps snapper/btrbk output parseable on a pt_BR host. The
// parent locale is deliberately set to a conflicting value so the test fails
// whenever Run stops assigning cmd.Env, independently of the host's own locale.
func TestExecRunner_ForcesLCAllCInChild(t *testing.T) {
	t.Setenv("LC_ALL", "pt_BR.UTF-8")

	out, err := execRunner{}.Run(t.Context(), "sh", []string{"-c", `printf %s "$LC_ALL"`}, nil)
	if err != nil {
		t.Fatalf("Run sh: %v", err)
	}
	if string(out) != "C" {
		t.Errorf("child LC_ALL = %q, want %q", out, "C")
	}
}

// TestExecRunner_StderrJoinedOnError checks a non-zero exit surfaces stderr in the
// returned error so the engine can wrap it (design §6).
func TestExecRunner_StderrJoinedOnError(t *testing.T) {
	_, err := execRunner{}.Run(t.Context(), "sh", []string{"-c", "echo boom >&2; exit 1"}, nil)
	if err == nil {
		t.Fatal("expected error from exit 1, got nil")
	}
	if !strings.Contains(err.Error(), "boom") {
		t.Errorf("error %q does not include stderr 'boom'", err)
	}
}

// TestMockRunner_RecordsCalls verifies MockRunner captures argv + stdin and
// delegates to RunFunc.
func TestMockRunner_RecordsCalls(t *testing.T) {
	mock := &MockRunner{
		RunFunc: func(_ context.Context, name string, _ []string, _ []byte) ([]byte, error) {
			if name == "fail" {
				return nil, errors.New("boom")
			}
			return []byte("ok"), nil
		},
	}

	out, err := mock.Run(t.Context(), "btrbk", []string{"run", "-c", "/tmp/btrbk.conf"}, []byte("x"))
	if err != nil || string(out) != "ok" {
		t.Fatalf("Run = %q, %v", out, err)
	}
	if _, err := mock.Run(t.Context(), "fail", nil, nil); err == nil {
		t.Fatal("expected RunFunc error for 'fail'")
	}

	if len(mock.Calls) != 2 {
		t.Fatalf("len(Calls) = %d, want 2", len(mock.Calls))
	}
	first := mock.Calls[0]
	if first.Name != "btrbk" || len(first.Args) != 3 || first.Args[0] != "run" {
		t.Errorf("Calls[0] = %+v", first)
	}
	if string(first.Stdin) != "x" {
		t.Errorf("Calls[0].Stdin = %q, want x", first.Stdin)
	}
}
