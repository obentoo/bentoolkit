package snapshot

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func s053NeedTools(t *testing.T, tools ...string) {
	t.Helper()
	for _, b := range tools {
		if _, err := exec.LookPath(b); err != nil {
			t.Skipf("%s not on PATH", b)
		}
	}
}

// TestExecRunner_PipeStreamsBoundedMemory pins R5.1/R5.2.
func TestExecRunner_PipeStreamsBoundedMemory(t *testing.T) {
	s053NeedTools(t, "sh", "head", "gzip")
	dir := t.TempDir()
	in, out := filepath.Join(dir, "in"), filepath.Join(dir, "out")
	if err := exec.Command("sh", "-c", `head -c 67108864 /dev/urandom > "$1"`, "sh", in).Run(); err != nil {
		t.Fatalf("generating 64 MiB input: %v", err)
	}
	stages := []pipeStage{
		{name: "sh", args: []string{"-c", `cat "$1"`, "sh", in}},
		{name: "gzip", args: []string{"-n", "-c"}},
		{name: "sh", args: []string{"-c", `cat > "$1"`, "sh", out}},
	}
	runtime.GC()
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	_, err := runPipe(t.Context(), execRunner{}, stages)
	runtime.ReadMemStats(&after)
	if err != nil {
		t.Fatalf("runPipe: %v", err)
	}
	if delta := after.TotalAlloc - before.TotalAlloc; delta >= 8<<20 {
		t.Errorf("TotalAlloc grew %d bytes (%.1f MiB) for a 64 MiB stream, want < 8 MiB", delta, float64(delta)/(1<<20))
	}
	fi, err := os.Stat(out)
	if err != nil || fi.Size() < 60<<20 {
		t.Errorf("final stage output = (%v, %v), want ~64 MiB of gzip written", fi, err)
	}
}

// TestExecRunner_PipeStageFailureNamesStage pins R5.4: a failing middle stage
// cancels the others (the never-ending first stage is killed, not left to finish), and the
// error names the stage, wraps its exit error and carries its stderr.
func TestExecRunner_PipeStageFailureNamesStage(t *testing.T) {
	s053NeedTools(t, "sleep", "gzip", "cat")
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	stages := []pipeStage{
		{name: "sleep", args: []string{"20"}},
		{name: "gzip", args: []string{"--definitely-not-a-flag"}},
		{name: "cat"},
	}
	start := time.Now()
	_, err := runPipe(ctx, execRunner{}, stages)
	elapsed := time.Since(start)
	if err == nil {
		t.Fatalf("runPipe = nil, want the gzip stage's failure")
	}
	if ctx.Err() != nil || elapsed > 3*time.Second {
		t.Errorf("runPipe took %v (ctx err %v): the other stages were not cancelled on failure", elapsed, ctx.Err())
	}
	var ee *exec.ExitError
	if !errors.As(err, &ee) {
		t.Errorf("err %v does not wrap the stage's *exec.ExitError", err)
	}
	if !strings.Contains(err.Error(), "gzip") || !strings.Contains(err.Error(), "definitely-not-a-flag") {
		t.Errorf("err %q must name the gzip stage and carry its stderr", err)
	}
}

// TestExecRunner_PipeCtxCancelKills pins R5.6.
func TestExecRunner_PipeCtxCancelKills(t *testing.T) {
	s053NeedTools(t, "sleep", "cat")
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	stages := []pipeStage{{name: "sleep", args: []string{"5"}}, {name: "cat"}, {name: "cat"}}
	timer := time.AfterFunc(200*time.Millisecond, cancel)
	defer timer.Stop()
	start := time.Now()
	_, err := runPipe(ctx, execRunner{}, stages)
	if !errors.Is(err, context.Canceled) {
		t.Errorf("err = %v, want errors.Is(err, context.Canceled)", err)
	}
	if elapsed := time.Since(start); elapsed >= 2*time.Second {
		t.Errorf("cancelled pipe returned after %v, want < 2s", elapsed)
	}
}
