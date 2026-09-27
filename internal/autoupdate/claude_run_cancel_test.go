//go:build unix

package autoupdate

import (
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

// Story 054, R4.1 / R4.2 (B3): a claude child whose descendant holds stdout is
// stopped with that descendant when the invocation's deadline elapses, and the
// outcome is still classified as a deadline.
func TestRunClaudeDeadlineStopsTheGroup(t *testing.T) {
	stubLookPathFound(t)
	fifo := filepath.Join(t.TempDir(), "pid.fifo")
	if err := syscall.Mkfifo(fifo, 0o600); err != nil {
		t.Fatal(err)
	}
	seam := func(ctx context.Context, _ string, _ ...string) *exec.Cmd {
		// The grandchild reports its pid and inherits stdout; the child answers
		// and then waits, as a CLI waiting on a helper would.
		return exec.CommandContext(ctx, "sh", "-c",
			`sh -c 'echo $$ > "$0"; exec sleep 60' "$0" & echo '{}'; wait`, fifo)
	}
	c, err := NewClaudeCodeClient(LLMConfig{},
		WithClaudeCodeExecCommand(seam),
		WithClaudeCodeContext(context.Background()),
		WithClaudeCodeTimeout(200*time.Millisecond),
	)
	if err != nil {
		t.Fatalf("NewClaudeCodeClient: %v", err)
	}

	done := make(chan error, 1)
	start := time.Now()
	go func() {
		_, runErr := c.run("instr", []byte("content"), "")
		done <- runErr
	}()
	raw, err := os.ReadFile(fifo)
	if err != nil {
		t.Fatal(err)
	}
	grandchild, err := strconv.Atoi(strings.TrimSpace(string(raw)))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = syscall.Kill(grandchild, syscall.SIGKILL) })

	var runErr error
	select {
	case runErr = <-done:
	case <-time.After(20 * time.Second):
		t.Fatal("run still blocked 20 s after a 200 ms deadline: a descendant holding stdout keeps it open (B3)")
	}
	if took := time.Since(start); took > 200*time.Millisecond+6*time.Second {
		t.Errorf("run returned after %v, want within 6 s of its deadline (R4.1)", took)
	}
	if !errors.Is(runErr, context.DeadlineExceeded) {
		t.Errorf("run error = %v, want it classified as a deadline (R4.2)", runErr)
	}
	if runErr != nil && strings.Contains(runErr.Error(), "exit status") {
		t.Errorf("run error %q reads as a non-zero exit; a deadline must stay a deadline (story 048)", runErr)
	}
	tick := time.NewTicker(10 * time.Millisecond)
	defer tick.Stop()
	for limit := time.Now().Add(time.Second); syscall.Kill(grandchild, 0) == nil && !procIsZombie(grandchild); <-tick.C {
		if time.Now().After(limit) {
			t.Errorf("grandchild %d survived the deadline: claude's group was not stopped (R4.1)", grandchild)
			break
		}
	}
}

func procIsZombie(pid int) bool {
	b, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	if err != nil {
		return true
	}
	i := strings.LastIndexByte(string(b), ')')
	return i >= 0 && i+2 < len(b) && b[i+2] == 'Z'
}
