package main

// Authored for story 092, sub-task 6.2: an interrupted --apply (a cancelled run
// context, or Ctrl-C typed into the live region) releases the region as a
// normal end does, so every history row reaches the scrollback once (R1.8).

import (
	"context"
	"log/slog"
	"os"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/obentoo/bentoolkit/internal/common/logging"
)

// s092InterruptFixture is s092ApplyAllFixture with four packages applied one
// at a time, whose pkgdev stub fails at once on its first two calls and blocks
// on the third: the run stays live with two ✗ rows drawn until interrupted.
// The count, not a package name, picks the blocker, as the pending order is
// not the order the packages were added in.
func s092InterruptFixture(t *testing.T, auOpts *autoupdateOptions) []string {
	t.Helper()
	pkgs := s092ApplyAllFixture(t, auOpts, 4)
	oConc := auOpts.concurrency
	t.Cleanup(func() { auOpts.concurrency = oConc })
	auOpts.concurrency = 1
	toolDir := strings.SplitN(os.Getenv("PATH"), ":", 2)[0]
	writeStub(t, toolDir, "pkgdev", `n=$(cat "$0.count" 2>/dev/null || echo 0)
n=$((n + 1))
echo "$n" >"$0.count"
if [ "$n" -eq 3 ]; then exec sleep 30; fi
exit 1
`)
	return pkgs
}

// s092RunInterruptedApply runs runAutoupdate like s092RunApply, and calls
// interrupt once the live frame shows two ✗ rows and the blocked package's
// active row. It returns the packages whose ✗ row the live frame held then.
// The run context is cancellable so interrupt can end it from outside. The
// logger discards: a log line written straight to stderr would tear the frame
// the screen emulator replays.
func s092RunInterruptedApply(t *testing.T, auOpts *autoupdateOptions, pkgs []string, interrupt func(cancel context.CancelFunc, stdin *os.File)) (code int, out string, drawn []string) {
	t.Helper()
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

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	watched := make(chan []string, 1)
	go func() {
		deadline := time.Now().Add(20 * time.Second)
		for time.Now().Before(deadline) {
			if data, err := os.ReadFile(f.Name()); err == nil {
				if live := s092LiveFailures(s092Screen(string(data)), pkgs); len(live) == 2 {
					interrupt(cancel, inW)
					watched <- live
					return
				}
			}
			time.Sleep(20 * time.Millisecond)
		}
		// Never drawn: end the run anyway so the test reports instead of hanging.
		cancel()
		watched <- nil
	}()

	saved1, err1 := syscall.Dup(1)
	saved2, err2 := syscall.Dup(2)
	if err1 != nil || err2 != nil {
		t.Fatalf("dup: %v %v", err1, err2)
	}
	if err := syscall.Dup3(int(f.Fd()), 1, 0); err != nil {
		t.Fatal(err)
	}
	if err := syscall.Dup3(int(f.Fd()), 2, 0); err != nil {
		t.Fatal(err)
	}
	func() {
		defer func() {
			_ = syscall.Dup3(saved1, 1, 0)
			_ = syscall.Dup3(saved2, 2, 0)
			_ = syscall.Close(saved1)
			_ = syscall.Close(saved2)
		}()
		auCmd := testAutoupdateCmd() //nolint:contextcheck // the run reads ctx from auCmd, set below
		auCmd.SetContext(logging.NewContext(ctx, slog.New(slog.DiscardHandler)))
		code = exitCodeFor(runAutoupdate(auCmd, nil, auOpts, ttyDeps(false, true))) //nolint:contextcheck // ctx reaches the run through auCmd
	}()
	drawn = <-watched
	data, err := os.ReadFile(f.Name())
	if err != nil {
		t.Fatal(err)
	}
	return code, string(data), drawn
}

// s092LiveFailures returns the packages whose ✗ row is on screen, once one
// more package is active (its spinner row ends in its stage); nil before.
func s092LiveFailures(screen []string, pkgs []string) []string {
	var failed []string
	active := false
	for _, pkg := range pkgs {
		if s092CountRowsWithPrefix(screen, "✗ "+pkg+" —") > 0 {
			failed = append(failed, pkg)
		}
		for _, r := range screen {
			if strings.Contains(r, " "+pkg+"  [manifest]") {
				active = true
			}
		}
	}
	if !active {
		return nil
	}
	return failed
}

// s092CheckInterruptedScreen asserts the screen an interrupted run left: the
// header once; the ✗ row of each package in drawn — rows the live frame held
// at the interrupt, which an unreleased last frame would leave on screen above
// PrintHistory's copy — exactly once; and no package's ✗ row twice.
func s092CheckInterruptedScreen(t *testing.T, code int, out string, drawn, pkgs []string) {
	t.Helper()
	screen := s092Screen(out)
	shown := strings.Join(screen, "\n")
	if len(drawn) != 2 {
		t.Fatalf("fixture: the live frame never showed two ✗ rows beside the blocked package:\n%s", shown)
	}
	if code == 0 {
		t.Errorf("an interrupted batch of failing bumps exited 0:\n%s", shown)
	}
	if n := s092CountRowsWithPrefix(screen, "Applying updates"); n != 1 {
		t.Errorf("the live region's header is on screen %d times, want once:\n%s", n, shown)
	}
	for _, pkg := range drawn {
		if n := s092CountRowsWithPrefix(screen, "✗ "+pkg+" —"); n != 1 {
			t.Errorf("%s: %d ✗ rows on screen after the interrupt, want exactly 1:\n%s", pkg, n, shown)
		}
	}
	for _, pkg := range pkgs {
		if n := s092CountRowsWithPrefix(screen, "✗ "+pkg+" —"); n > 1 {
			t.Errorf("%s: %d ✗ rows on screen after the interrupt, want at most 1:\n%s", pkg, n, shown)
		}
	}
}

// R1.8: cancelling the run context mid-batch releases the live region, so the
// history is printed below it once and not left in the last live frame too.
func TestApplyInterruptFlushCancelPrintsEveryRowOnce(t *testing.T) {
	auOpts := testAutoupdateOptions()
	pkgs := s092InterruptFixture(t, auOpts)
	code, out, drawn := s092RunInterruptedApply(t, auOpts, pkgs, func(cancel context.CancelFunc, _ *os.File) {
		cancel()
	})
	s092CheckInterruptedScreen(t, code, out, drawn, pkgs)
}

// R1.8: Ctrl-C typed into the live region cancels the run and releases the
// region the same way.
func TestApplyInterruptFlushCtrlCPrintsEveryRowOnce(t *testing.T) {
	auOpts := testAutoupdateOptions()
	pkgs := s092InterruptFixture(t, auOpts)
	code, out, drawn := s092RunInterruptedApply(t, auOpts, pkgs, func(_ context.CancelFunc, stdin *os.File) {
		_, _ = stdin.Write([]byte{0x03})
	})
	s092CheckInterruptedScreen(t, code, out, drawn, pkgs)
}
