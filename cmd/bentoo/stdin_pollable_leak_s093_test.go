package main

// Story 093: a regular-file stdin must never reach bubbletea. Its Run starts
// the renderer, queues the first frame and only then fails to register the
// file with epoll, returning without stopping the renderer, which writes that
// frame to stdout a frame interval later — into whatever fd 1 is by then.

import (
	"context"
	"os"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/obentoo/bentoolkit/internal/common/config"
)

func TestStdinPollableRegularFileLeavesNoLateWrite(t *testing.T) {
	for _, env := range []string{"NO_COLOR", "BENTOO_NO_TUI", "BENTOO_UI"} {
		t.Setenv(env, "")
	}
	in, err := os.CreateTemp(t.TempDir(), "stdin")
	if err != nil {
		t.Fatal(err)
	}
	defer in.Close()
	out, err := os.CreateTemp(t.TempDir(), "stdout")
	if err != nil {
		t.Fatal(err)
	}
	defer out.Close()

	origStdin := os.Stdin
	os.Stdin = in
	defer func() { os.Stdin = origStdin }()
	saved, err := syscall.Dup(1)
	if err != nil {
		t.Fatal(err)
	}
	if err := syscall.Dup3(int(out.Fd()), 1, 0); err != nil {
		t.Fatal(err)
	}
	func() {
		defer func() {
			if err := syscall.Dup3(saved, 1, 0); err != nil {
				t.Errorf("restoring fd 1: %v", err)
			}
			if err := syscall.Close(saved); err != nil {
				t.Errorf("closing the saved fd: %v", err)
			}
		}()
		d := defaultDeps()
		stubUIIsTerminal(d, true)
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		r, teardown := chooseManifestReporter(discardLog(), d, &config.Config{}, false, ctx, cancel)
		r.BatchStart(1)
		r.BatchDone("")
		teardown()
		// Long enough for a leaked renderer to flush its queued frame.
		time.Sleep(200 * time.Millisecond)
	}()

	data, err := os.ReadFile(out.Name())
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "Applying updates") {
		t.Errorf("a regular-file stdin still started the live region, which wrote its frame to fd 1 after the run: %q", data)
	}
}
