package main

// The live region moved to the check with PR 9b: the check is where the build
// gates run now that apply only publishes. These tests are story 092's apply
// tests (R1.5 history flush, R2.2 repair hint, R1.8 interrupt flush), removed
// with the validating apply in PR 3c, pointed at `--check --llm`.

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/obentoo/bentoolkit/internal/autoupdate"
	"github.com/obentoo/bentoolkit/internal/common/config"
	"github.com/obentoo/bentoolkit/internal/common/logging"
)

// s092CheckFixture sets up n pending bumps the scan just confirmed, every one
// failing at the manifest step (every tool is a stub exiting 1), so each is
// HUMAN, keeps its staged tree and leaves a repair bundle.
func s092CheckFixture(t *testing.T, auOpts *autoupdateOptions, n int) (pkgs []string, overlay, configDir string, checked []autoupdate.CheckResult) {
	t.Helper()
	overlay, configDir = s056AutoupdateEnv(t, auOpts)
	oLLM, oYes, oDistdir := auOpts.llm, auOpts.yes, auOpts.distdir
	t.Cleanup(func() { auOpts.llm, auOpts.yes, auOpts.distdir = oLLM, oYes, oDistdir })
	auOpts.llm, auOpts.yes = true, true
	auOpts.distdir = t.TempDir()

	if err := os.WriteFile(filepath.Join(overlay, "profiles", "repo_name"), []byte("bentoo\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	pending, err := autoupdate.NewPendingList(configDir)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < n; i++ {
		name := fmt.Sprintf("flush%02d", i)
		pkg := "dev-go/" + name
		dir := filepath.Join(overlay, "dev-go", name)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, name+"-1.0.ebuild"), []byte("EAPI=8\nSLOT=\"0\"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := pending.Add(autoupdate.PendingUpdate{Package: pkg, CurrentVersion: "1.0", NewVersion: "1.1", Status: autoupdate.StatusPending}); err != nil {
			t.Fatal(err)
		}
		pkgs = append(pkgs, pkg)
		checked = append(checked, autoupdate.CheckResult{Package: pkg, CurrentVersion: "1.0", UpstreamVersion: "1.1", HasUpdate: true, Type: "source"})
	}

	toolDir := t.TempDir()
	for _, tool := range []string{"claude", "pkgdev", "pkgcheck", "emerge", "ebuild", "egencache", "portageq"} {
		writeStub(t, toolDir, tool, "exit 1\n")
	}
	t.Setenv("PATH", toolDir+":/usr/bin:/bin")
	// The live region is drawn only when nothing opts out of it.
	for _, env := range []string{"NO_COLOR", "BENTOO_NO_TUI", "BENTOO_UI"} {
		t.Setenv(env, "")
	}
	return pkgs, overlay, configDir, checked
}

// s092RunCheck runs the check's validation half on a stdout the run takes for
// a terminal, with fds 1 and 2 captured in order, and returns what reached
// them. Stdin is a pipe: bubbletea registers its input with epoll, which
// refuses a regular file. interrupt, when not nil, is called once the live
// frame holds two finished rows, with the run's cancel and the pipe's write end.
func s092RunCheck(t *testing.T, auOpts *autoupdateOptions, overlay, configDir string, checked []autoupdate.CheckResult,
	interrupt func(cancel context.CancelFunc, stdin *os.File)) string {
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
	ctx, cancel := context.WithCancel(logging.NewContext(context.Background(), slog.New(slog.DiscardHandler)))
	defer cancel()
	if interrupt != nil {
		go func() {
			deadline := time.Now().Add(20 * time.Second)
			for time.Now().Before(deadline) {
				data, _ := os.ReadFile(f.Name())
				if strings.Count(string(data), "✗ ") >= 2 {
					interrupt(cancel, inW)
					return
				}
				time.Sleep(20 * time.Millisecond)
			}
		}()
	}
	func() {
		defer func() {
			_ = syscall.Dup3(saved1, 1, 0)
			_ = syscall.Dup3(saved2, 2, 0)
			_ = syscall.Close(saved1)
			_ = syscall.Close(saved2)
		}()
		ar := testAutoupdateRunWith(auOpts, ttyDeps(false, true))
		ar.runPendingValidation(ctx, overlay, configDir, checked, config.LLMConfig{})
	}()
	data, err := os.ReadFile(f.Name())
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

// TestCheckHistoryFlushPrintsEveryRowOnceAfterRelease (story 092 R1.5, now on
// the check): more finished bumps than the default 24-row region holds, so the
// live frame folds its history. After the run every ✗ row is on screen exactly
// once, below the header, and the fold line is gone.
func TestCheckHistoryFlushPrintsEveryRowOnceAfterRelease(t *testing.T) {
	auOpts := testAutoupdateOptions()
	pkgs, overlay, configDir, checked := s092CheckFixture(t, auOpts, 26)
	out := s092RunCheck(t, auOpts, overlay, configDir, checked, nil)
	screen := s092Screen(out)
	shown := strings.Join(screen, "\n")
	if s092CountRowsWithPrefix(screen, "Validating updates") != 1 {
		t.Fatalf("want the live region's header once on screen:\n%s", shown)
	}
	if strings.Contains(shown, "earlier package(s) finished") {
		t.Errorf("the fold line of the live frame survived the release:\n%s", shown)
	}
	for _, pkg := range pkgs {
		if n := s092CountRowsWithPrefix(screen, "✗ "+pkg+" — "); n != 1 {
			t.Errorf("%s: %d ✗ rows on screen, want exactly 1:\n%s", pkg, n, shown)
		}
	}
}

// TestCheckRepairHintReachesTheFailureRow (story 092 R2.2, now on the check): a
// HUMAN bump that left a repair bundle names --repair on its ✗ row.
func TestCheckRepairHintReachesTheFailureRow(t *testing.T) {
	auOpts := testAutoupdateOptions()
	pkgs, overlay, configDir, checked := s092CheckFixture(t, auOpts, 2)
	out := s092RunCheck(t, auOpts, overlay, configDir, checked, nil)
	shown := strings.Join(s092Screen(out), "\n")
	for _, pkg := range pkgs {
		if !strings.Contains(shown, "(repair: bentoo overlay autoupdate --repair "+pkg+")") {
			t.Errorf("%s: its ✗ row does not name --repair:\n%s", pkg, shown)
		}
	}
}

// TestCheckInterruptFlushPrintsEveryRowOnce (story 092 R1.8, now on the
// check): an interrupted check, by its cancelled context or by Ctrl+C typed
// into the live region, releases the region as a normal end does: every
// finished row reaches the scrollback once, the bump in flight is stopped.
func TestCheckInterruptFlushPrintsEveryRowOnce(t *testing.T) {
	for _, tc := range []struct {
		name      string
		interrupt func(cancel context.CancelFunc, stdin *os.File)
	}{
		{"cancelled context", func(cancel context.CancelFunc, _ *os.File) { cancel() }},
		{"ctrl+c in the region", func(_ context.CancelFunc, stdin *os.File) { _, _ = stdin.Write([]byte{0x03}) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			auOpts := testAutoupdateOptions()
			pkgs, overlay, configDir, checked := s092CheckFixture(t, auOpts, 4)
			toolDir := strings.SplitN(os.Getenv("PATH"), ":", 2)[0]
			// The third manifest blocks until the run is interrupted.
			writeStub(t, toolDir, "pkgdev", `n=$(cat "$0.count" 2>/dev/null || echo 0)
n=$((n + 1))
echo "$n" >"$0.count"
if [ "$n" -eq 3 ]; then exec sleep 30; fi
exit 1
`)
			start := time.Now()
			out := s092RunCheck(t, auOpts, overlay, configDir, checked, tc.interrupt)
			if took := time.Since(start); took > 25*time.Second {
				t.Fatalf("the interrupt did not stop the blocked build: the run took %s", took)
			}
			// The blocker was reached, and nothing ran after the interrupt.
			if calls, _ := os.ReadFile(filepath.Join(toolDir, "pkgdev.count")); strings.TrimSpace(string(calls)) != "3" {
				t.Fatalf("pkgdev ran %q times, want 3: the third blocks until the interrupt, and no fourth may start", strings.TrimSpace(string(calls)))
			}
			screen := s092Screen(out)
			shown := strings.Join(screen, "\n")
			finished := 0
			for _, pkg := range pkgs {
				switch n := s092CountRowsWithPrefix(screen, "✗ "+pkg+" — "); n {
				case 0:
				case 1:
					finished++
				default:
					t.Errorf("%s: %d ✗ rows on screen, want at most 1:\n%s", pkg, n, shown)
				}
			}
			if finished < 2 {
				t.Errorf("only %d finished rows reached the scrollback, want the 2 drawn before the interrupt:\n%s", finished, shown)
			}
		})
	}
}
