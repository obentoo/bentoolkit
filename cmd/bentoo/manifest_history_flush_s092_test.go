package main

// Story 092 (test-after): the live region now leaves no history row on screen
// when it is released, so `overlay manifest` must print its history itself,
// exactly as --apply does. Without that, every ✓/✗ row of a manifest run
// would vanish from the scrollback.

import (
	"context"
	"os"
	"strings"
	"syscall"
	"testing"
)

func TestManifestHistoryFlushKeepsEveryRowAfterRelease(t *testing.T) {
	for _, env := range []string{"NO_COLOR", "BENTOO_NO_TUI", "BENTOO_UI"} {
		t.Setenv(env, "")
	}
	inR, inW, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer inR.Close()
	defer inW.Close()
	f, err := os.CreateTemp(t.TempDir(), "out")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	origStdin := os.Stdin
	os.Stdin = inR
	defer func() { os.Stdin = origStdin }()
	saved1, err := syscall.Dup(1)
	if err != nil {
		t.Fatal(err)
	}
	if err := syscall.Dup3(int(f.Fd()), 1, 0); err != nil {
		t.Fatal(err)
	}
	func() {
		defer func() {
			_ = syscall.Dup3(saved1, 1, 0)
			_ = syscall.Close(saved1)
		}()
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		r, teardown := chooseManifestReporter(nil, ttyDeps(false, true), nil, false, ctx, cancel)
		r.BatchStart(2)
		r.TaskStart("dev-go/alpha", "dev-go/alpha")
		r.TaskDone("dev-go/alpha", true, "manifest ok", "")
		r.TaskStart("dev-go/beta", "dev-go/beta")
		r.TaskDone("dev-go/beta", false, "manifest failed: 404", "")
		r.BatchDone("")
		teardown()
	}()
	data, err := os.ReadFile(f.Name())
	if err != nil {
		t.Fatal(err)
	}
	screen := s092Screen(string(data))
	for _, want := range []string{"✓ dev-go/alpha", "✗ dev-go/beta"} {
		if n := s092CountRowsWithPrefix(screen, want); n != 1 {
			t.Errorf("%q rows left on screen = %d, want 1\nscreen:\n%s", want, n, strings.Join(screen, "\n"))
		}
	}
}
