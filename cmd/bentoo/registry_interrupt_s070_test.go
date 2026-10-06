//go:build unix

package main

// Authored for story 070, sub-tasks 2.1 and 2.2 — R2.1, R2.2, R2.3, R2.4,
// R3.1, R3.2, R4.3 (interrupted) and R6.5 (not interrupted).
//
// Written from story.md R2-R4, R6.5 and design.md "Testing Strategy", never
// from an implementation.
//
// Each row starts the real tree through the real main() in a re-exec'd child,
// inside the story 058 fixture (s058NewCancellableFixture): HOME at a temp dir
// and HTTP(S)_PROXY at a listener that accepts and never answers. Once the
// child has reached that proxy — the registry download is waiting — it gets
// one SIGINT, and must exit 1 within 5 s of THAT signal, saying it was
// interrupted while fetching the repository registry and nothing about a
// missing repository.
//
// The two halves of "interrupted is not missing":
//   - an interruption must not be reported as a missing repository (the
//     interrupted rows below forbid "not found" and the hint lines);
//   - a missing repository must not be reported as an interruption
//     (TestS070CompareUnknownRepositoryStillReportsNotFound, no signal).
//
// This file names only symbols that exist before the story, so it compiles
// on the pre-fix tree and is Red there by behaviour: every interrupted row is
// still running 5 s after its SIGINT.

import (
	"bytes"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

// s070InterruptedLine is the interruption text of R2.2 and R3.2. It is spelled
// here, not read from the tree, so the file compiles before the fix.
const s070InterruptedLine = "interrupted while fetching the repository registry"

// s070SignalBound is R2.1/R2.4/R3.1/R4.3's bound, measured from the SIGINT.
const s070SignalBound = 5 * time.Second

// s070Child is one started child of the real main() inside a 058 fixture.
type s070Child struct {
	f      *s058CancellableFixture
	cmd    *exec.Cmd
	done   chan struct{}
	output func() string
}

func s070StartChild(t *testing.T, f *s058CancellableFixture, args []string) *s070Child {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run=^TestS058CancellableHelperChild$", "-test.count=1")
	env := append([]string(nil), f.env...)
	env = append(env, s058CancellableArgsEnv+"="+strings.Join(args, "\x1f"))
	cmd.Env = env
	cmd.WaitDelay = 2 * time.Second
	cmd.Dir = f.overlay
	var out bytes.Buffer
	var mu sync.Mutex
	w := writerFunc(func(p []byte) (int, error) { mu.Lock(); defer mu.Unlock(); return out.Write(p) })
	cmd.Stdout, cmd.Stderr = w, w
	if err := cmd.Start(); err != nil {
		t.Fatalf("starting the child: %v", err)
	}
	c := &s070Child{f: f, cmd: cmd, done: make(chan struct{}),
		output: func() string { mu.Lock(); defer mu.Unlock(); return out.String() }}
	go func() { _ = cmd.Wait(); close(c.done) }()
	t.Cleanup(func() {
		select {
		case <-c.done:
		default:
			_ = cmd.Process.Kill()
			<-c.done
		}
	})
	return c
}

// exit reports how an ended child ended: its code, or -1 and true when a
// signal killed it.
func (c *s070Child) exit() (int, bool) {
	if ws, ok := c.cmd.ProcessState.Sys().(syscall.WaitStatus); ok && ws.Signaled() {
		return -1, true
	}
	return c.cmd.ProcessState.ExitCode(), false
}

func s070SkipWithoutSIGINT(t *testing.T) {
	t.Helper()
	if signal.Ignored(syscall.SIGINT) {
		t.Skip("SIGINT is ignored in this process (nohup?); children would inherit it — run under env --default-signal")
	}
}

// TestS070RegistryInterruptReportsTheInterruption is R2.1-R2.4, R3.1, R3.2
// and R4.3: one SIGINT while the registry download waits.
func TestS070RegistryInterruptReportsTheInterruption(t *testing.T) {
	s070SkipWithoutSIGINT(t)
	t.Parallel()
	rows := []struct {
		name string
		args []string
		// once: the interruption line must appear exactly once (R2.2, R2.4,
		// R3.2 say "one error line"); otherwise at least once.
		once bool
	}{
		{"compare", []string{"overlay", "compare"}, true},                                     // R2.1, R2.2, R2.3
		{"compare_sync", []string{"overlay", "compare", "--sync"}, true},                      // R2.4
		{"prune", []string{"overlay", "prune"}, true},                                         // R3.1, R3.2
		{"autoupdate_revive_list", []string{"overlay", "autoupdate", "--revive-list"}, false}, // R4.3
	}
	forbidden := []string{
		"not found",                      // R2.2, R3.2, R4.3
		"Registry repositories:",         // R2.2 hint line; its absence also proves R2.3
		"Registry unavailable",           // R2.2 hint line; its absence also proves R2.3
		"Failed to sync repository list", // R2.4
	}
	for _, row := range rows {
		t.Run(row.name, func(t *testing.T) {
			t.Parallel()
			f := s058NewCancellableFixture(t)
			c := s070StartChild(t, f, row.args)
			cmdline := "bentoo " + strings.Join(row.args, " ")

			var ready string
			deadline := time.After(20 * time.Second)
		poll:
			for {
				if b, err := os.ReadFile(filepath.Join(f.marks, "ready")); err == nil {
					ready = strings.TrimSpace(string(b))
					break poll
				}
				select {
				case <-c.done:
					code, _ := c.exit()
					t.Fatalf("%s ended (exit %d) before it reached the registry proxy, so it could not be interrupted mid-download:\n%s",
						cmdline, code, c.output())
				case <-deadline:
					t.Fatalf("%s never reached the registry proxy within 20s:\n%s", cmdline, c.output())
				case <-time.After(20 * time.Millisecond):
				}
			}
			if ready != "proxy" {
				t.Fatalf("%s first waited on %q, not on the registry proxy; the SIGINT would not land during the registry download", cmdline, ready)
			}

			sent := time.Now()
			if err := c.cmd.Process.Signal(syscall.SIGINT); err != nil {
				t.Fatalf("signalling the child: %v", err)
			}
			select {
			case <-c.done:
			case <-time.After(s070SignalBound):
				t.Fatalf("%s was still running %s after its first SIGINT (registry download waiting); output so far:\n%s",
					cmdline, s070SignalBound, c.output())
			}
			took := time.Since(sent)
			out := c.output()

			code, killed := c.exit()
			if killed {
				t.Fatalf("%s was killed by its SIGINT instead of returning exit 1:\n%s", cmdline, out)
			}
			if code != 1 {
				t.Errorf("%s exited %d after its SIGINT (%s), want 1:\n%s", cmdline, code, took, out)
			}
			n := strings.Count(out, s070InterruptedLine)
			switch {
			case n == 0:
				t.Errorf("%s output does not say %q:\n%s", cmdline, s070InterruptedLine, out)
			case row.once && n != 1:
				t.Errorf("%s output says %q %d times, want one line:\n%s", cmdline, s070InterruptedLine, n, out)
			}
			for _, bad := range forbidden {
				if strings.Contains(out, bad) {
					t.Errorf("%s output contains %q after an interrupted registry download:\n%s", cmdline, bad, out)
				}
			}
		})
	}
}

// s070RegistryXML is a registry listing gentoo only. Its shape is the one
// repositories.xml has (internal/common/provider/registry_test.go).
const s070RegistryXML = `<?xml version="1.0" encoding="UTF-8"?>
<repositories version="1.0">
  <repo quality="core" status="official">
    <name>gentoo</name>
    <source type="git">https://github.com/gentoo-mirror/gentoo.git</source>
  </repo>
</repositories>
`

// TestS070CompareUnknownRepositoryStillReportsNotFound is R6.5, the converse
// of the interrupted rows: a name the registry does not list, with no signal
// and a fresh registry cache, is still a missing repository — never an
// interruption — and the hint lines still print.
func TestS070CompareUnknownRepositoryStillReportsNotFound(t *testing.T) {
	f := s058NewCancellableFixture(t)
	cacheDir := filepath.Join(f.home, ".cache", "bentoo")
	if err := os.MkdirAll(cacheDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cacheDir, "repositories.xml"), []byte(s070RegistryXML), 0o644); err != nil {
		t.Fatal(err)
	}
	args := []string{"overlay", "compare", "nosuchrepo"}
	c := s070StartChild(t, f, args)
	select {
	case <-c.done:
	case <-time.After(20 * time.Second):
		t.Fatalf("bentoo %s was still running after 20s with a fresh registry cache:\n%s", strings.Join(args, " "), c.output())
	}
	out := c.output()
	code, killed := c.exit()
	if killed || code != 1 {
		t.Errorf("bentoo %s exited %d (killed=%v), want 1:\n%s", strings.Join(args, " "), code, killed, out)
	}
	if want := `msg="Repository not found." repository=nosuchrepo`; !strings.Contains(out, want) {
		t.Errorf("output does not say %q:\n%s", want, out)
	}
	if want := "Registry repositories:"; !strings.Contains(out, want) {
		t.Errorf("output lost the registry hint line %q:\n%s", want, out)
	}
	if strings.Contains(out, s070InterruptedLine) {
		t.Errorf("an uninterrupted missing repository is reported as an interruption:\n%s", out)
	}
	if b, err := os.ReadFile(filepath.Join(f.marks, "ready")); err == nil {
		t.Errorf("a fresh registry cache still made the command reach the network (%s):\n%s", strings.TrimSpace(string(b)), out)
	}
}
