package tray_test

// Story 099, sub-task 5.4: applicable GLSAs become warning conditions
// delivered through one shared notification (R3.3-R3.6, R3.8-R3.11, R5.1,
// R5.2, R5.3, R5.6). Harness and contract: app_harness_s099_test.go.
//
// Hostile halves first (R3.8 keeps one condition per GLSA but one shared
// note): two GLSAs whose ids differ by one digit must stay two conditions
// and two tooltip lines, and one of them clearing must neither close nor
// resend the shared note; the converse, the same id listed again by a later
// check, must not count as a new GLSA.

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/obentoo/bentoolkit/internal/gentoo/pending"
	"github.com/obentoo/bentoolkit/internal/tray"
)

type s099Advisory struct{ id, title, atoms string }

var (
	s099SSL  = s099Advisory{"202610-01", "OpenSSL: Multiple Vulnerabilities (incl. CVE-2026-1234)", "dev-libs/openssl"}
	s099SSL2 = s099Advisory{"202610-10", "OpenSSH: Remote code execution", "net-misc/openssh"}
	s099Curl = s099Advisory{"202610-05", "curl: Heap overflow", "net-misc/curl net-libs/libcurl-compat"}
	// s099Done is applied ([A]): recognisable output listing nothing applicable.
	s099Done = "202509-17 [A] zlib: Buffer overflow ( sys-libs/zlib )\n"
)

// s099GLSAOut is a glsa-check --list affected answer listing advisories as [N].
func s099GLSAOut(advs ...s099Advisory) pending.Result {
	var b strings.Builder
	b.WriteString(s099Done)
	for _, a := range advs {
		fmt.Fprintf(&b, "%s [N] %s ( %s )\n", a.id, a.title, a.atoms)
	}
	return pending.Result{Stdout: []byte(b.String()), ExitCode: 0, Duration: 8 * time.Second}
}

func (s *s099Harness) startGLSA(edit func(*tray.Deps)) {
	s.t.Helper()
	s.startUpd(func(d *tray.Deps) {
		d.GLSA = s099GLSACmd()
		if edit != nil {
			edit(d)
		}
	})
}

func (s *s099Harness) glsaSent() []s097Sent { return s.sentTo("glsa") }

func glsaLines(lines []string) []string {
	var out []string
	for _, l := range lines {
		if strings.Contains(l, "202610-") {
			out = append(out, l)
		}
	}
	return out
}

func TestS099GLSA_R3_8_R3_10_NearIdenticalIdsStayApartAndOneClearingSendsNothing(t *testing.T) {
	s := newS099(t)
	s.run.set(s099GLSAPath, s099Seq(s099GLSAOut(s099SSL, s099SSL2), s099GLSAOut(s099SSL2), s099GLSAOut(s099SSL2)))
	s.startGLSA(nil)
	s.firstCheck(s099GLSAPath, "glsa")
	if l := glsaLines(s.viewNow().Alerts); len(l) != 2 {
		t.Fatalf("tooltip GLSA lines = %q, want 2: %s and %s are two advisories", l, s099SSL.id, s099SSL2.id)
	}
	sentBefore := len(s.glsaSent())
	s.next(s099GLSAPath, 3) // result 2 (SSL cleared) applied
	if n := len(s.glsaSent()); n != sentBefore {
		t.Errorf("one of two GLSAs clearing sent %d notifications, want 0 (R3.10)", n-sentBefore)
	}
	if n := s.closedFor("glsa"); n != 0 {
		t.Errorf("shared GLSA note closed %d times while %s is still active, want 0", n, s099SSL2.id)
	}
	l := glsaLines(s.viewNow().Alerts)
	if len(l) != 1 || !strings.Contains(l[0], s099SSL2.id) {
		t.Errorf("tooltip GLSA lines = %q, want only %s", l, s099SSL2.id)
	}
	s.wantTransitions("glsa", s099SSL.id, [2]string{"ok", "warning"}, [2]string{"warning", "ok"})
	s.wantTransitions("glsa", s099SSL2.id, [2]string{"ok", "warning"})
}

func TestS099GLSA_R3_3_SameIdListedAgainIsNotANewGLSA(t *testing.T) {
	s := newS099(t)
	again := s099SSL
	again.atoms = "dev-libs/openssl dev-libs/openssl-compat" // a later listing may differ in detail
	s.run.set(s099GLSAPath, s099Seq(s099GLSAOut(s099SSL), s099GLSAOut(s099SSL), s099GLSAOut(again), s099GLSAOut(again)))
	s.startGLSA(nil)
	s.firstCheck(s099GLSAPath, "glsa")
	s.next(s099GLSAPath, 4)
	if n := len(s.glsaSent()); n != 1 {
		t.Errorf("GLSA notifications = %d, want 1: %s stayed applicable", n, s099SSL.id)
	}
	if l := glsaLines(s.viewNow().Alerts); len(l) != 1 {
		t.Errorf("tooltip GLSA lines = %q, want 1", l)
	}
}

func TestS099GLSA_R3_4_R3_9_SharedNoteNamesIdTitleAtomsAndCount(t *testing.T) {
	s := newS099(t)
	s.run.set(s099GLSAPath, s099Seq(s099GLSAOut(s099SSL), s099GLSAOut(s099SSL, s099Curl), s099GLSAOut(s099SSL, s099Curl)))
	s.startGLSA(nil)
	s.firstCheck(s099GLSAPath, "glsa")
	sent := s.glsaSent()
	if len(sent) != 1 {
		t.Fatalf("GLSA notifications = %d, want 1", len(sent))
	}
	first := s099Text(sent[0].Msg)
	for _, want := range []string{s099SSL.id, "OpenSSL", "dev-libs/openssl", "1"} {
		if !strings.Contains(first, want) {
			t.Errorf("GLSA notification %q does not carry %q", first, want)
		}
	}
	if u := sent[0].Msg.Urgency; u != 1 {
		t.Errorf("GLSA urgency = %d, want 1 (normal: a warning)", u)
	}
	s.next(s099GLSAPath, 3)
	sent = s.glsaSent()
	if len(sent) != 2 {
		t.Fatalf("GLSA notifications = %d after a second GLSA, want 2", len(sent))
	}
	second := sent[1]
	if second.Msg.ReplacesID != sent[0].ID {
		t.Errorf("second GLSA notification ReplacesID = %d, want %d: one shared note", second.Msg.ReplacesID, sent[0].ID)
	}
	text := s099Text(second.Msg)
	for _, want := range []string{s099Curl.id, "curl", "net-misc/curl", "net-libs/libcurl-compat", "2"} {
		if !strings.Contains(text, want) {
			t.Errorf("second GLSA notification %q does not carry %q", text, want)
		}
	}
}

func TestS099GLSA_R3_11_LastClearingWithdrawsTheSharedNote(t *testing.T) {
	s := newS099(t)
	s.run.set(s099GLSAPath, s099Seq(s099GLSAOut(s099SSL, s099Curl), s099GLSAOut(s099Curl), s099GLSAOut(), s099GLSAOut()))
	s.startGLSA(nil)
	s.firstCheck(s099GLSAPath, "glsa")
	s.next(s099GLSAPath, 4)
	sent := s.glsaSent()
	if len(sent) == 0 {
		t.Fatal("no GLSA notification was sent")
	}
	if n := s.closedFor("glsa"); n != 1 {
		t.Errorf("shared GLSA note closed %d times, want once when the last GLSA cleared", n)
	}
	last := sent[len(sent)-1].ID
	found := false
	for _, id := range s.out.closedIDs() {
		found = found || id == last
	}
	if !found {
		t.Errorf("closed ids %v do not include the shared note's id %d", s.out.closedIDs(), last)
	}
	if l := glsaLines(s.viewNow().Alerts); len(l) != 0 {
		t.Errorf("tooltip GLSA lines = %q, want none", l)
	}
}

func TestS099GLSA_R3_6_FailuresKeepConditionsWithOneWarnPerStreak(t *testing.T) {
	s := newS099(t)
	garbage := pending.Result{Stdout: []byte("Traceback (most recent call last):\n  oops\n"), ExitCode: 0, Duration: time.Second}
	exit1 := pending.Result{Stdout: []byte(s099Done), ExitCode: 1, Duration: time.Second}
	s.run.set(s099GLSAPath, func(n int) (pending.Result, error) {
		switch n {
		case 1, 5:
			return s099GLSAOut(s099SSL), nil
		case 2, 6, 7:
			return garbage, nil
		case 3:
			return exit1, nil
		default: // 4
			return pending.Result{Duration: 2 * time.Minute}, s099TimeoutErr(s099GLSAPath)
		}
	})
	s.startGLSA(nil)
	s.firstCheck(s099GLSAPath, "glsa")
	for n := 2; n <= 7; n++ {
		s.next(s099GLSAPath, n)
	}
	if n := s.warnsFor("glsa"); n != 2 {
		t.Errorf("GLSA WARN lines = %d, want 2: one per failure streak (calls 2-4, then 6-7)", n)
	}
	if !s.logLine(func(l string) bool {
		return strings.Contains(l, "level=WARN") && strings.Contains(l, "monitor=glsa") && strings.Contains(l, s099GLSAPath)
	}) {
		t.Error("the GLSA WARN does not name the command")
	}
	if n := s.closedFor("glsa"); n != 0 {
		t.Errorf("a failed GLSA check closed the shared note %d times; conditions must be kept", n)
	}
	if n := len(s.glsaSent()); n != 1 {
		t.Errorf("GLSA notifications = %d, want 1: failures change nothing", n)
	}
	if l := glsaLines(s.viewNow().Alerts); len(l) != 1 {
		t.Errorf("tooltip GLSA lines = %q, want %s kept", l, s099SSL.id)
	}
}

func TestS099GLSA_R5_3_GLSAMakesTheIconAskForAttention(t *testing.T) {
	s := newS099(t)
	s.run.set(s099GLSAPath, s099Fixed(s099GLSAOut(s099SSL), nil))
	s.startGLSA(nil)
	s.firstCheck(s099GLSAPath, "glsa")
	v := s.viewNow()
	if len(glsaLines(v.Alerts)) != 1 {
		t.Errorf("view Alerts = %q, want the GLSA line among the alerts (NeedsAttention)", v.Alerts)
	}
	if v.AlertCritical {
		t.Error("a GLSA made the tooltip critical; it is a warning")
	}
	if len(glsaLines(v.Info)) != 0 {
		t.Errorf("view Info = %q carries the GLSA; it belongs to Alerts", v.Info)
	}
}

func TestS099GLSA_R5_1_PauseSilencesAWarningAndKeepsTheLine(t *testing.T) {
	s := newS099(t)
	s.run.set(s099GLSAPath, s099Fixed(s099GLSAOut(s099SSL), nil))
	s.startGLSA(nil)
	s.pause()
	s.firstCheck(s099GLSAPath, "glsa")
	if n := len(s.glsaSent()); n != 0 {
		t.Errorf("GLSA notifications while paused = %d, want 0", n)
	}
	if l := glsaLines(s.viewNow().Alerts); len(l) != 1 {
		t.Errorf("tooltip GLSA lines while paused = %q, want 1", l)
	}
}

func TestS099GLSA_R5_7_InfoLineWithFound(t *testing.T) {
	s := newS099(t)
	s.run.set(s099GLSAPath, s099Fixed(s099GLSAOut(s099SSL, s099Curl), nil))
	s.startGLSA(nil)
	s.firstCheck(s099GLSAPath, "glsa")
	if n := s.countLines("level=INFO", "monitor=glsa", "duration_s=8", "found=2"); n != 1 {
		t.Errorf("GLSA finish lines with duration_s=8 found=2 = %d, want 1", n)
	}
}

func TestS099GLSA_GLSARunsBeforeWorld(t *testing.T) {
	s := newS099(t)
	s.startUpd(func(d *tray.Deps) {
		d.GLSA = s099GLSACmd()
		d.Updates = s099WorldCmd()
	})
	s.untilRun(s099EmergePath, 1, 14)
	cmds := s.run.commands()
	if len(cmds) < 2 || cmds[0].Path != s099GLSAPath || cmds[1].Path != s099EmergePath {
		var paths []string
		for _, c := range cmds {
			paths = append(paths, c.Path)
		}
		t.Errorf("command order = %q, want glsa-check then emerge", paths)
	}
}

func TestS099GLSA_R3_7_DisabledByConfigRunsNothing(t *testing.T) {
	s := newS099(t)
	s.cfg = s097Config(t, "monitors:\n  glsa:\n    enabled: false\n")
	s.startUpd(func(d *tray.Deps) {
		d.GLSA = s099GLSACmd()
		d.Updates = s099WorldCmd()
	})
	s.untilRun(s099EmergePath, 1, 14)
	s.next(s099EmergePath, 2)
	if n := s.run.count(s099GLSAPath); n != 0 {
		t.Errorf("glsa-check ran %d times with the GLSA monitor disabled", n)
	}
}
