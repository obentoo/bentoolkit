package snapshot

// Authored for story 062, sub-task 3.4 (R5.3).
//
// Validate() is kept as ValidateWith(nil), and a component handed no logger
// discards: so Validate's empty-subvolumes warning must reach nobody. The name
// starts with TestValidateWith so 3.4's `-run '^TestValidateWith'` selects it.
//
// This file uses no symbol the story adds: it compiles against today's tree,
// where the warning is on stderr, so its Red is an assertion failure.

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

// s062IsolateSnapshot points HOME and the XDG dirs at a temp root.
func s062IsolateSnapshot(t *testing.T) {
	t.Helper()
	root := t.TempDir()
	t.Setenv("HOME", filepath.Join(root, "home"))
	t.Setenv("XDG_STATE_HOME", filepath.Join(root, "state"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(root, "config"))
}

// s062SnapshotCaptureFD2 runs fn with fd 2 pointed at a pipe. The os.Stderr
// variable is deliberately left alone: the pre-story logger keeps the os.Stderr
// it saw first, so only the descriptor reliably catches it.
func s062SnapshotCaptureFD2(t *testing.T, fn func()) string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	saved, err := syscall.Dup(2)
	if err != nil {
		t.Fatalf("dup fd 2: %v", err)
	}
	if err := syscall.Dup2(int(w.Fd()), 2); err != nil {
		t.Fatalf("redirect fd 2: %v", err)
	}
	done := make(chan string, 1)
	go func() {
		var b bytes.Buffer
		_, _ = io.Copy(&b, r)
		done <- b.String()
	}()
	func() {
		defer func() {
			if err := syscall.Dup2(saved, 2); err != nil {
				t.Errorf("restore fd 2: %v", err)
			}
			_ = syscall.Close(saved)
			_ = w.Close()
		}()
		fn()
	}()
	out := <-done
	_ = r.Close()
	return out
}

// TestValidateWithoutALoggerWritesNothingToStderr: Validate() — no logger —
// discards the empty-subvolumes warning.
func TestValidateWithoutALoggerWritesNothingToStderr(t *testing.T) {
	s062IsolateSnapshot(t)
	cfg := &Config{Engine: EngineConfig{Driver: "btrbk"}}

	stderr := s062SnapshotCaptureFD2(t, func() { _ = cfg.Validate() })
	if strings.TrimSpace(stderr) != "" {
		t.Errorf("Validate() without a logger wrote to stderr (R5.3: it must discard):\n%s", stderr)
	}
}
