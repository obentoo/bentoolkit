package autoupdate

// Authored for story 062, sub-task 3.1 (R5.3).
//
// A component built without a logger discards its diagnostics: the same two
// warnings logger_injection_test.go observes must reach nobody — in particular
// not stderr, where the pre-story package-level logger wrote them.
//
// This file uses no symbol the story adds, so it compiles against today's tree
// and its Red is an assertion failure: the warnings are on stderr now.
//
// Stderr is captured at the file descriptor ONLY; the os.Stderr variable is left
// alone on purpose. The pre-story logger keeps whatever os.Stderr was the first
// time it ran: had a capture swapped the variable, that logger would keep
// writing into the first capture's pipe — closed by then — and every later
// capture would read empty, a false green (measured while authoring this file).
// Every writer, however it holds stderr, ends on fd 2.

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"github.com/obentoo/bentoolkit/internal/autoupdate/registry"
)

// s062IsolateAutoupdate points HOME and the XDG dirs at a temp root and clears
// the token variables, so no test reads the developer's config or secrets and
// nothing can be written into their state directory.
func s062IsolateAutoupdate(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	t.Setenv("HOME", filepath.Join(root, "home"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(root, "xdg-config"))
	t.Setenv("XDG_STATE_HOME", filepath.Join(root, "xdg-state"))
	t.Setenv("GITHUB_TOKEN", "")
	t.Setenv("GH_TOKEN", "")
	return root
}

// s062CaptureStderr runs fn with fd 2 pointed at a pipe and returns everything
// written to it.
func s062CaptureStderr(t *testing.T, fn func()) string {
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

// TestCheckerWithoutALoggerWritesNothingToStderr: the llm_prompt warning of a
// checker built with no logger goes nowhere.
func TestCheckerWithoutALoggerWritesNothingToStderr(t *testing.T) {
	root := s062IsolateAutoupdate(t)
	cfg := &registry.PackagesConfig{Packages: map[string]registry.PackageConfig{
		"cat-a/s062-one": {URL: "https://example.invalid/a", Parser: "json", Path: "v", LLMPrompt: "extract version"},
	}}

	stderr := s062CaptureStderr(t, func() {
		if _, err := NewChecker(filepath.Join(root, "overlay"),
			WithConfigDir(filepath.Join(root, "config")),
			WithPackagesConfig(cfg),
		); err != nil {
			t.Fatalf("NewChecker: %v", err)
		}
	})
	if strings.TrimSpace(stderr) != "" {
		t.Errorf("a checker built without a logger wrote to stderr (R5.3: it must discard):\n%s", stderr)
	}
}
