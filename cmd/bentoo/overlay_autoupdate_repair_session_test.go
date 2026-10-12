package main

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/obentoo/bentoolkit/internal/autoupdate/repair"
)

// TestRepairConfirmAnythingButYesStartsNothing covers R3.3 and R3.4.
func TestRepairConfirmAnythingButYesStartsNothing(t *testing.T) {
	for _, answer := range []string{"", "\n", "n\n", "no\n", "yess\n", "y es\n", "ok\n"} {
		t.Run(fmt.Sprintf("%q", answer), func(t *testing.T) {
			auOpts := testAutoupdateOptions()
			f := repairFixture(t, auOpts, 1)
			before := readFileOrEmpty(f.bundlePath)
			ebuild := filepath.Join(f.pkgDir, "repairdemo-"+repairCand+".ebuild")
			ebBefore := readFileOrEmpty(ebuild)
			code, out := runRepair(t, auOpts, ttyDeps(true, true), answer)
			if code != 0 {
				t.Fatalf("declined --repair exited %d, want 0:\n%s", code, out)
			}
			assertNoSession(t, f)
			if readFileOrEmpty(f.bundlePath) != before || readFileOrEmpty(ebuild) != ebBefore {
				t.Fatalf("a declined --repair changed the bundle or the staged tree")
			}
		})
	}
}

// TestRepairConfirmPrintsTheSummaryAndTheQuestion covers R3.1 and R3.2.
func TestRepairConfirmPrintsTheSummaryAndTheQuestion(t *testing.T) {
	auOpts := testAutoupdateOptions()
	f := repairFixture(t, auOpts, 1)
	_, out := runRepair(t, auOpts, ttyDeps(true, true), "n\n")
	for _, want := range []string{repairPkg, repairCurrent, repairCand, "re-check", "Bash", "WebFetch(raw.example.org)", f.staged,
		"Start an interactive claude session in " + f.pkgDir + "? [y/N]"} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}
}

// TestRepairSessionRunsClaudeInTheStagedPackageDir covers R4.1, R4.2, R4.5 and
// R5.1 (any exit status of the session still leads to re-validation).
func TestRepairSessionRunsClaudeInTheStagedPackageDir(t *testing.T) {
	for _, yes := range []string{"y\n", "yes\n"} {
		auOpts := testAutoupdateOptions()
		f := repairFixture(t, auOpts, 1)
		writeStub(t, f.toolDir, "claude", fmt.Sprintf("pwd > %q.cwd\nprintf '%%s\\0' \"$@\" > %q.argv\nexit 3\n", f.record, f.record))
		code, out := runRepair(t, auOpts, ttyDeps(true, true), yes)
		cwd := strings.TrimSpace(readFileOrEmpty(f.record + ".cwd"))
		if cwd == "" {
			t.Fatalf("answer %q: no claude session ran (exit %d):\n%s", yes, code, out)
		}
		if cwd != f.pkgDir {
			t.Fatalf("session cwd = %q, want the staged package dir %q", cwd, f.pkgDir)
		}
		argv := strings.Split(strings.TrimSuffix(readFileOrEmpty(f.record+".argv"), "\x00"), "\x00")
		if len(argv) == 0 || strings.HasPrefix(argv[0], "-") || !strings.Contains(argv[0], "404 Not Found") || !strings.Contains(argv[0], "pkgdev manifest") {
			t.Fatalf("argv[0] is not the bundle prompt: %q", argv)
		}
		if i := slices.Index(argv, "--permission-mode"); i < 0 || i+1 >= len(argv) || argv[i+1] != "default" {
			t.Fatalf("session not in permission mode default: %q", argv)
		}
		if slices.Contains(argv, "--dangerously-skip-permissions") || slices.Contains(argv, "-p") || slices.Contains(argv, "--print") {
			t.Fatalf("session argv carries a non-interactive or bypass flag: %q", argv)
		}
		if !slices.Contains(argv, "Edit(/"+f.overlay+"/**)") {
			t.Fatalf("session argv lacks the overlay edit deny rule: %q", argv)
		}
		b, err := repair.ReadBundle(f.bundlePath)
		if err != nil || !b.Created.After(f.created) {
			t.Fatalf("a session that exited 3 was not followed by a re-validation that rewrote the bundle (err %v, created %v)", err, b.Created)
		}
		if code != 1 {
			t.Fatalf("failed re-validation exited %d, want 1:\n%s", code, out)
		}
	}
}

const repairSignalChildEnv = "BENTOO_TEST_REPAIR_SIGNAL_CHILD"

// TestRepairSessionSignalChild is the re-exec'd child of
// TestRepairSessionSignalReachesClaudeAndRevalidates; it runs --repair with
// the real exit path and does nothing in a normal run.
func TestRepairSessionSignalChild(t *testing.T) {
	if os.Getenv(repairSignalChildEnv) != "1" {
		t.Skip("re-exec child only")
	}
	auOpts := testAutoupdateOptions()
	auOpts.repair = repairPkg
	auOpts.distdir = os.Getenv("BENTOO_TEST_REPAIR_DISTDIR")
	in, err := os.Open(os.Getenv("BENTOO_TEST_REPAIR_ANSWER"))
	if err != nil {
		os.Exit(97)
	}
	os.Stdin = in
	rerr := runAutoupdate(testAutoupdateCmd(), nil, auOpts, ttyDeps(true, true))
	exitProcess(exitCodeFor(rerr))
}

// TestRepairSessionSignalReachesClaudeAndRevalidates covers R4.6 and Q6: the
// operator's Ctrl+C, sent to the whole foreground process group, reaches the
// session and bentoo survives it to re-validate. Must not run under nohup.
func TestRepairSessionSignalReachesClaudeAndRevalidates(t *testing.T) {
	if os.Getenv(repairSignalChildEnv) == "1" {
		t.Skip("in the child")
	}
	auOpts := testAutoupdateOptions()
	f := repairFixture(t, auOpts, 1)
	writeStub(t, f.toolDir, "claude", fmt.Sprintf(
		"trap 'echo got-int > %q.int; exit 130' INT\necho $$ > %q.pid\nwhile :; do sleep 0.05; done\n", f.record, f.record))
	answer := filepath.Join(t.TempDir(), "answer")
	if err := os.WriteFile(answer, []byte("y\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(os.Args[0], "-test.run=^TestRepairSessionSignalChild$", "-test.count=1")
	cmd.Env = append(os.Environ(), repairSignalChildEnv+"=1", "BENTOO_TEST_REPAIR_ANSWER="+answer, "BENTOO_TEST_REPAIR_DISTDIR="+auOpts.distdir)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	var out strings.Builder
	cmd.Stdout, cmd.Stderr = &out, &out
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	pgid := cmd.Process.Pid
	deadline := time.Now().Add(20 * time.Second)
	for readFileOrEmpty(f.record+".pid") == "" {
		if time.Now().After(deadline) {
			_ = syscall.Kill(-pgid, syscall.SIGKILL)
			_ = cmd.Wait()
			t.Fatalf("the session never started:\n%s", out.String())
		}
		time.Sleep(20 * time.Millisecond)
	}
	if err := syscall.Kill(-pgid, syscall.SIGINT); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	var werr error
	select {
	case werr = <-done:
	case <-time.After(60 * time.Second):
		_ = syscall.Kill(-pgid, syscall.SIGKILL)
		t.Fatalf("bentoo did not finish after the session was interrupted:\n%s", out.String())
	}
	if strings.TrimSpace(readFileOrEmpty(f.record+".int")) != "got-int" {
		t.Fatalf("the session never received SIGINT:\n%s", out.String())
	}
	var ee *exec.ExitError
	if errors.As(werr, &ee) {
		if ws, ok := ee.Sys().(syscall.WaitStatus); ok && ws.Signaled() {
			t.Fatalf("bentoo was killed by %v instead of leaving the signal to the session:\n%s", ws.Signal(), out.String())
		}
	}
	b, err := repair.ReadBundle(f.bundlePath)
	if err != nil || !b.Created.After(f.created) {
		t.Fatalf("no re-validation after the interrupted session (bundle err %v, created %v):\n%s", err, b.Created, out.String())
	}
	if code := cmd.ProcessState.ExitCode(); code != 1 {
		t.Fatalf("child exit %d, want 1 (re-validation fails with failing stub tools):\n%s", code, out.String())
	}
}
