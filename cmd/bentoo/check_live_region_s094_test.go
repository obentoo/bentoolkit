package main

// Story 094's live-region tests, removed with the validating apply in PR 3c,
// pointed at `--check --llm` in PR 9b: the check is where the build gates and
// the fixers run now. The screen emulator they replay on lives in
// screen_scroll_s094_test.go.

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
	"unsafe"

	"github.com/obentoo/bentoolkit/internal/autoupdate"
	"github.com/obentoo/bentoolkit/internal/common/config"
	"github.com/obentoo/bentoolkit/internal/common/logging"
)

// s094Ioctl issues one ioctl on fd; arg is converted inside the call expression
// as the unsafe.Pointer rules for syscalls require.
func s094Ioctl(fd, req uintptr, arg unsafe.Pointer) error {
	if _, _, errno := syscall.Syscall(syscall.SYS_IOCTL, fd, req, uintptr(arg)); errno != 0 {
		return errno
	}
	return nil
}

// s094OpenPTY opens a pseudo-terminal whose window is rows x cols. The master
// is read through the runtime poller (its Fd is never taken, which would make
// it blocking), so closing it ends a pending read. The test skips where no pty
// can be opened.
func s094OpenPTY(t *testing.T, rows, cols int) (master, slave *os.File) {
	t.Helper()
	m, err := os.OpenFile("/dev/ptmx", os.O_RDWR|syscall.O_NOCTTY, 0)
	if err != nil {
		t.Skipf("no pseudo-terminal here: %v", err)
	}
	raw, err := m.SyscallConn()
	if err != nil {
		t.Fatal(err)
	}
	var ptn uint32
	var ioErr error
	if err := raw.Control(func(fd uintptr) {
		var unlock int32
		if ioErr = s094Ioctl(fd, syscall.TIOCSPTLCK, unsafe.Pointer(&unlock)); ioErr == nil {
			ioErr = s094Ioctl(fd, syscall.TIOCGPTN, unsafe.Pointer(&ptn))
		}
	}); err != nil || ioErr != nil {
		m.Close()
		t.Fatalf("unlock the pty: %v %v", err, ioErr)
	}
	s, err := os.OpenFile("/dev/pts/"+strconv.FormatUint(uint64(ptn), 10), os.O_RDWR|syscall.O_NOCTTY, 0)
	if err != nil {
		m.Close()
		t.Fatalf("open the pty slave: %v", err)
	}
	ws := struct{ row, col, x, y uint16 }{uint16(rows), uint16(cols), 0, 0}
	if err := s094Ioctl(s.Fd(), syscall.TIOCSWINSZ, unsafe.Pointer(&ws)); err != nil {
		m.Close()
		s.Close()
		t.Fatalf("set the pty size: %v", err)
	}
	return m, s
}

// s094RunApplyOnPTY runs runAutoupdate with fds 1 and 2 on a rows x cols
// pseudo-terminal and returns everything written to it. bubbletea finds a
// terminal on its output and reads the window size from it, so the live
// region is drawn for that size; the pty's own output processing (\n to
// \r\n) applies as on a real terminal. Stdin stays a pipe, as in
// s092RunApply.
func s094RunCheckOnPTY(t *testing.T, auOpts *autoupdateOptions, overlay, configDir string, checked []autoupdate.CheckResult, rows, cols int) (out string) {
	t.Helper()
	master, slave := s094OpenPTY(t, rows, cols)
	defer master.Close()
	var buf bytes.Buffer
	readDone := make(chan struct{})
	go func() {
		defer close(readDone)
		// The read ends with EIO once every slave descriptor is closed.
		_, _ = io.Copy(&buf, master)
	}()

	inR, inW, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer inR.Close()
	defer inW.Close()
	origStdin := os.Stdin
	os.Stdin = inR
	defer func() { os.Stdin = origStdin }()
	saved1, err1 := syscall.Dup(1)
	saved2, err2 := syscall.Dup(2)
	if err1 != nil || err2 != nil {
		t.Fatalf("dup: %v %v", err1, err2)
	}
	func() {
		defer func() {
			_ = syscall.Dup3(saved1, 1, 0)
			_ = syscall.Dup3(saved2, 2, 0)
			_ = syscall.Close(saved1)
			_ = syscall.Close(saved2)
		}()
		if err := syscall.Dup3(int(slave.Fd()), 1, 0); err != nil {
			t.Fatal(err)
		}
		if err := syscall.Dup3(int(slave.Fd()), 2, 0); err != nil {
			t.Fatal(err)
		}
		lg := slog.New(slog.NewTextHandler(os.Stderr, nil))
		ctx := logging.NewContext(context.Background(), lg)
		ar := testAutoupdateRunWith(auOpts, ttyDeps(false, true))
		// A bare run logs nowhere; runAutoupdate gives it the command's logger.
		ar.lg = lg
		ar.runPendingValidation(ctx, overlay, configDir, checked, config.LLMConfig{Provider: "claude-code"})
	}()
	slave.Close()
	select {
	case <-readDone:
	case <-time.After(5 * time.Second):
		// A leftover child still holds the slave: close the master to end the read.
		master.Close()
		<-readDone
	}
	return buf.String()
}

// s094LiveFixture is s092CheckFixture with tools whose output moves the
// live region: pkgdev prints wget-style progress rows for a while before it
// fails (the task's tail grows, then a new task starts with an empty one), and
// with --llm and a claude-code provider each manifest failure invokes the
// fixer, which logs to the terminal and routes an info line into the frame
// while claude streams its tool calls.
func s094LiveFixture(t *testing.T, auOpts *autoupdateOptions, n int) (pkgs []string, overlay, configDir string, checked []autoupdate.CheckResult) {
	t.Helper()
	pkgs, overlay, configDir, checked = s092CheckFixture(t, auOpts, n)

	toolDir := t.TempDir()
	writeStub(t, toolDir, "pkgdev", `x=`+strings.Repeat("x", 60)+`
i=1
while [ "$i" -le 24 ]; do
  printf '%5dK .......... .......... .......... .......... .......... %3d%% 1.2M 0s %s\n' "$i" "$((i * 4))" "$x"
  i=$((i + 1))
  sleep 0.02
done
echo "!!! Fetched file: q11-1.1.tar.gz VERIFY FAILED!" >&2
exit 1
`)
	writeStub(t, toolDir, "claude", `case "$*" in *--version*) echo "2.1.296 (Claude Code)"; exit 0;; esac
cat >/dev/null 2>&1 &
for f in a.ebuild b.ebuild Manifest; do
  printf '{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Read","input":{"file_path":"/staged/%s"}}]}}\n' "$f"; sleep 0.05
done
printf '{"type":"result","subtype":"success","is_error":false,"result":"tried a fix","total_cost_usd":0.01,"permission_denials":[]}\n'
`)
	for _, tool := range []string{"pkgcheck", "emerge", "ebuild", "egencache", "portageq"} {
		writeStub(t, toolDir, tool, "exit 1\n")
	}
	writeBwrapStub(t, toolDir)
	t.Setenv("PATH", toolDir+":/usr/bin:/bin")
	return pkgs, overlay, configDir, checked
}

// s094ProgressRow matches what only the live frame draws while a task runs:
// a task row (spinner glyph, label) or a row of a task's tail.
var s094ProgressRow = regexp.MustCompile(`^(?:[|/\\-] dev-go/|\s+\d+K \.{10} )`)

// Story 094, now on the check (PR 9b): the live region redraws in place as its height changes: tails grow and
// shrink, packages finish and a routed log line joins the frame, while the
// fixer's own log lines reach the terminal. On a terminal with a fixed height,
// whatever the region leaves behind is in the scrollback or on the screen
// after the run: it must hold the header and each ✗ row exactly once, and no
// row of a task in progress.
func TestCheckLiveRegionNoResidueOnHeightChange(t *testing.T) {
	for _, size := range []struct{ rows, cols int }{{40, 120}, {12, 120}} {
		t.Run(fmt.Sprintf("%dx%d", size.cols, size.rows), func(t *testing.T) {
			auOpts := testAutoupdateOptions()
			pkgs, overlay, configDir, checked := s094LiveFixture(t, auOpts, 3)
			out := s094RunCheckOnPTY(t, auOpts, overlay, configDir, checked, size.rows, size.cols)
			screen := newS094Screen(size.rows, size.cols)
			screen.Feed(out)
			lines := screen.Lines()
			shown := strings.Join(lines, "\n")
			// Guards: a run that never drew the live region at this width, or
			// never reached the fixer, proves nothing about residue.
			if !strings.Contains(out, "invoking LLM fixer") {
				t.Fatalf("the fixture never reached the LLM fixer:\n%s", shown)
			}
			if !strings.Contains(out, "\x1b[?25l") || !strings.Contains(out, "…") {
				t.Fatalf("the live region was not drawn at %d columns:\n%s", size.cols, shown)
			}

			var residue []string
			if n := s094CountRowsContaining(lines, "Validating updates"); n != 1 {
				residue = append(residue, fmt.Sprintf("%d \"Validating updates\" header rows, want 1", n))
			}
			for _, pkg := range pkgs {
				if n := s092CountRowsWithPrefix(lines, "✗ "+pkg+" — "); n != 1 {
					residue = append(residue, fmt.Sprintf("%d ✗ rows for %s, want 1", n, pkg))
				}
			}
			progress := 0
			for _, l := range lines {
				if s094ProgressRow.MatchString(l) {
					progress++
				}
			}
			if progress != 0 {
				residue = append(residue, fmt.Sprintf("%d rows of a task in progress, want 0", progress))
			}
			if len(residue) > 0 {
				var marked []string
				for i, l := range lines {
					if strings.Contains(l, "Validating updates") || strings.HasPrefix(l, "✗ ") || s094ProgressRow.MatchString(l) {
						marked = append(marked, fmt.Sprintf("%4d  %.100s", i, l))
					}
				}
				t.Errorf("live region residue at %dx%d (scrollback + screen, %d rows): %s\nheader, ✗ and progress rows:\n%s",
					size.cols, size.rows, len(lines), strings.Join(residue, "; "), strings.Join(marked, "\n"))
			}
		})
	}
}

// TestCheckLiveRegionHeldRecordsPrintBelowTheRegion (story 094, now on the
// check): end to end on a pseudo-terminal, every record the fixer path logs
// while the region is drawn reaches the terminal exactly once, after
// bubbletea shows the cursor again on exit, with its level and fields.
func TestCheckLiveRegionHeldRecordsPrintBelowTheRegion(t *testing.T) {
	auOpts := testAutoupdateOptions()
	pkgs, overlay, configDir, checked := s094LiveFixture(t, auOpts, 3)
	out := s094RunCheckOnPTY(t, auOpts, overlay, configDir, checked, 40, 120)
	hide, show := strings.Index(out, "\x1b[?25l"), strings.LastIndex(out, "\x1b[?25h")
	if hide < 0 || show < hide {
		t.Fatalf("guard: no live region drawn (hide cursor at %d, show at %d):\n%q", hide, show, out)
	}
	if under := out[hide:show]; strings.Contains(under, "level=") {
		t.Errorf("a slog record was written under the live region:\n%q", under)
	}
	below := out[show:]
	for _, pkg := range pkgs {
		rec := "level=INFO msg=\"manifest failed; invoking LLM fixer to repair the ebuild\" package=" + pkg + " "
		if n := strings.Count(below, rec); n != 1 {
			t.Errorf("%d fixer records for %s below the region, want 1:\n%s", n, pkg, below)
		}
	}
}
