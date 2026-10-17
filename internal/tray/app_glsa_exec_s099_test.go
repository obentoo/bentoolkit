package tray_test

// Executor tests for the GLSA outcome paths the authored tests leave open:
// the runtime disable on a command that can no longer be executed (which
// withdraws every GLSA condition), the timeout WARN outside a failure
// streak, a run killed by a signal, blank output as an answer, and the keys
// of every new GLSA log line.

import (
	"errors"
	"io/fs"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/obentoo/bentoolkit/internal/gentoo/pending"
	"github.com/obentoo/bentoolkit/internal/tray"
)

// startGLSAWorld runs the App with both commands, so checks keep running
// after the GLSA monitor is disabled.
func (s *s099Harness) startGLSAWorld() {
	s.t.Helper()
	s.startGLSA(func(d *tray.Deps) { d.Updates = s099WorldCmd() })
}

// wantGLSALineKeys checks every glsa log line above DEBUG for the target and
// state keys and exactly one level key.
func (s *s099Harness) wantGLSALineKeys() {
	s.t.Helper()
	n := 0
	for _, l := range s.logs.lines() {
		if !strings.Contains(l, "monitor=glsa") || strings.Contains(l, "level=DEBUG") {
			continue
		}
		n++
		if !strings.Contains(l, " target=") || !strings.Contains(l, " state=") {
			s.t.Errorf("GLSA line lacks target or state: %s", l)
		}
		if k := len(s099LevelKey.FindAllString(l, -1)); k != 1 {
			s.t.Errorf("GLSA line carries %d level keys, want 1: %s", k, l)
		}
	}
	if n == 0 {
		s.t.Error("no GLSA log line to check")
	}
}

func TestS099GLSA_X_CannotExecuteDisablesAndWithdraws(t *testing.T) {
	for name, cause := range map[string]error{
		"not found":      exec.ErrNotFound,
		"path vanished":  fs.ErrNotExist,
		"not executable": fs.ErrPermission,
	} {
		t.Run(name, func(t *testing.T) {
			s := newS099(t)
			s.run.set(s099GLSAPath, func(n int) (pending.Result, error) {
				if n == 1 {
					return s099GLSAOut(s099SSL, s099Curl), nil
				}
				return pending.Result{}, s099StartErr(s099GLSAPath, cause)
			})
			s.startGLSAWorld()
			s.firstCheck(s099GLSAPath, "glsa")
			if l := glsaLines(s.viewNow().Alerts); len(l) != 2 {
				t.Fatalf("tooltip GLSA lines = %q, want 2 before the disable", l)
			}
			s.next(s099GLSAPath, 2)
			s.waitLines("the disable WARN", 1, "level=WARN", "monitor=glsa", "disabled")
			s.waitFor("the shared GLSA note withdrawn", func() bool { return s.closedFor("glsa") == 1 })
			s.next(s099EmergePath, 4)
			if n := s.run.count(s099GLSAPath); n != 2 {
				t.Errorf("glsa-check ran %d times, want 2: the monitor is disabled after it failed to start", n)
			}
			if n := s.warnsFor("glsa"); n != 1 {
				t.Errorf("GLSA WARN lines = %d, want exactly 1", n)
			}
			if n := s.warnsFor("glsa", "disabled", "command="+s099GLSAPath, "state=warning"); n != 1 {
				t.Errorf("disable WARN naming the command and the state before it = %d lines, want 1", n)
			}
			if n := s.closedFor("glsa"); n != 1 {
				t.Errorf("shared GLSA note closed %d times, want 1", n)
			}
			if l := glsaLines(s.viewNow().Alerts); len(l) != 0 {
				t.Errorf("tooltip GLSA lines = %q after the disable, want none", l)
			}
			if n := len(s.glsaSent()); n != 2 {
				t.Errorf("GLSA notifications = %d, want 2: the disable sends nothing", n)
			}
			s.wantTransitions("glsa", s099SSL.id, [2]string{"ok", "warning"}, [2]string{"warning", "ok"})
			s.wantTransitions("glsa", s099Curl.id, [2]string{"ok", "warning"}, [2]string{"warning", "ok"})
			s.wantGLSALineKeys()
		})
	}
}

func TestS099GLSA_X_TimeoutOutsideAStreakWarnsEveryTime(t *testing.T) {
	s := newS099(t)
	garbage := pending.Result{Stdout: []byte("no advisory here\n"), ExitCode: 0, Duration: time.Second}
	s.run.set(s099GLSAPath, func(n int) (pending.Result, error) {
		switch n {
		case 1:
			return s099GLSAOut(s099SSL), nil
		case 2, 3:
			return pending.Result{Duration: 2 * time.Minute}, s099TimeoutErr(s099GLSAPath)
		default:
			return garbage, nil
		}
	})
	s.startGLSA(nil)
	s.firstCheck(s099GLSAPath, "glsa")
	s.next(s099GLSAPath, 6) // results 1-5 applied
	if n := s.warnsFor("glsa", "timed out", "timeout=2m0s", "command="+s099GLSAPath); n != 2 {
		t.Errorf("timeout WARN lines = %d, want 2: one per timeout outside a failure streak", n)
	}
	if n := s.warnsFor("glsa", "check failed"); n != 1 {
		t.Errorf("failure-streak WARN lines = %d, want 1 (calls 4-5)", n)
	}
	if n := s.closedFor("glsa"); n != 0 {
		t.Errorf("shared GLSA note closed %d times; a timeout keeps the conditions", n)
	}
	if l := glsaLines(s.viewNow().Alerts); len(l) != 1 {
		t.Errorf("tooltip GLSA lines = %q, want %s kept", l, s099SSL.id)
	}
	s.wantGLSALineKeys()
}

func TestS099GLSA_X_KilledBySignalKeepsConditions(t *testing.T) {
	s := newS099(t)
	killed := pending.Result{Stdout: []byte(s099Done), ExitCode: -1, Duration: time.Second}
	s.run.set(s099GLSAPath, s099Seq(s099GLSAOut(s099SSL), killed))
	s.startGLSA(nil)
	s.firstCheck(s099GLSAPath, "glsa")
	s.next(s099GLSAPath, 4) // results 1-3 applied
	if n := s.warnsFor("glsa", "signal"); n != 1 {
		t.Errorf("signal WARN lines over one streak = %d, want 1", n)
	}
	if n := s.closedFor("glsa"); n != 0 {
		t.Errorf("a killed GLSA run closed the shared note %d times", n)
	}
	if l := glsaLines(s.viewNow().Alerts); len(l) != 1 {
		t.Errorf("tooltip GLSA lines = %q, want %s kept", l, s099SSL.id)
	}
	s.wantGLSALineKeys()
}

func TestS099GLSA_X_BlankOutputIsAnAnswerThatClears(t *testing.T) {
	s := newS099(t)
	blank := pending.Result{Stdout: []byte("\n"), ExitCode: 0, Duration: time.Second}
	s.run.set(s099GLSAPath, s099Seq(s099GLSAOut(s099SSL), blank))
	s.startGLSA(nil)
	s.firstCheck(s099GLSAPath, "glsa")
	s.next(s099GLSAPath, 2)
	s.checked("glsa", 2)
	if n := s.closedFor("glsa"); n != 1 {
		t.Errorf("shared GLSA note closed %d times, want 1: blank output lists no advisory", n)
	}
	if n := s.warnsFor("glsa"); n != 0 {
		t.Errorf("GLSA WARN lines = %d, want 0", n)
	}
	if n := s.countLines("level=INFO", "monitor=glsa", "duration_s=", "found=0", "state=ok"); n != 1 {
		t.Errorf("finish lines with found=0 state=ok = %d, want 1", n)
	}
}

func TestS099GLSA_X_FailedSendOfARaiseKeepsTheSharedId(t *testing.T) {
	s := newS099(t)
	s.run.set(s099GLSAPath, s099Seq(s099GLSAOut(s099SSL), s099GLSAOut(s099SSL, s099Curl),
		s099GLSAOut(s099SSL, s099Curl, s099SSL2)))
	s.startGLSA(nil)
	s.firstCheck(s099GLSAPath, "glsa")
	first := s.glsaSent()
	if len(first) != 1 {
		t.Fatalf("GLSA notifications = %d, want 1", len(first))
	}
	s.out.setSendErr(errors.New("notification server gone"))
	s.next(s099GLSAPath, 2)
	s.checked("glsa", 2)
	if n := s.out.failedSends(); n != 1 {
		t.Fatalf("failed sends = %d, want 1 (the raise of %s)", n, s099Curl.id)
	}
	s.out.setSendErr(nil)
	s.next(s099GLSAPath, 3)
	s.checked("glsa", 3)
	sent := s.glsaSent()
	if len(sent) != 2 {
		t.Fatalf("GLSA notifications = %d, want 2", len(sent))
	}
	if sent[1].Msg.ReplacesID != first[0].ID {
		t.Errorf("ReplacesID after a failed send = %d, want %d: the id the server handed out", sent[1].Msg.ReplacesID, first[0].ID)
	}
	if text := s099Text(sent[1].Msg); !strings.Contains(text, s099SSL2.id) || !strings.Contains(text, "3") {
		t.Errorf("GLSA notification %q does not name %s and the count 3", text, s099SSL2.id)
	}
	if n := s.warnsFor("glsa", `msg="sending a system notification failed"`, "target="+s099Curl.id, "state=warning"); n != 1 {
		t.Errorf("failed-send WARN naming %s and state=warning = %d lines, want 1\n%s", s099Curl.id, n, strings.Join(s.logs.lines(), "\n"))
	}
	s.wantGLSALineKeys()
}
