package tray_test

// Story 099, sub-task 5.3: the App schedules update checks off the loop and
// applies the @world outcome (R1.1, R1.3, R1.4, R1.7-R1.10, R2.3-R2.12,
// R5.1, R5.6, R5.7). Harness and contract: app_harness_s099_test.go.
//
// Hostile halves first (R2.5/R2.6 is an identity rule over package sets):
// the same count with a different package must replace (a count-only
// comparison would wrongly collapse them), and the same packages listed in
// another order, duplicated or at new versions must send nothing (an
// order- or version-sensitive comparison would wrongly split them).

import (
	"errors"
	"os"
	"os/exec"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/obentoo/bentoolkit/internal/gentoo/pending"
	"github.com/obentoo/bentoolkit/internal/sysmon"
	"github.com/obentoo/bentoolkit/internal/tray"
)

const (
	s099Qt   = "dev-qt/qtbase-6.10.1:6"
	s099Vim  = "app-editors/vim-9.1.1000"
	s099Port = "sys-apps/portage-3.0.70"
	s099Font = "media-fonts/font-adobe-100dpi-1.0.4-r1"
)

// startWorld runs the App with only the @world command present.
func (s *s099Harness) startWorld(edit func(*tray.Deps)) {
	s.t.Helper()
	s.startUpd(func(d *tray.Deps) {
		d.Updates = s099WorldCmd()
		if edit != nil {
			edit(d)
		}
	})
}

func (s *s099Harness) updatesSent() []s097Sent { return s.sentTo("updates") }

// --- hostile halves: R2.5, R2.6 ---

func TestS099World_R2_5_SameCountDifferentPackageReplaces(t *testing.T) {
	s := newS099(t)
	s.run.set(s099EmergePath, s099Seq(s099World(s099Qt, s099Vim), s099World(s099Qt, s099Port)))
	s.startWorld(nil)
	s.firstCheck(s099EmergePath, "updates")
	s.resync(s099EmergePath, "updates", 2)
	sent := s.updatesSent()
	if len(sent) != 2 {
		t.Fatalf("updates notifications = %d, want 2: two sets of 2 packages differ (vim -> portage)", len(sent))
	}
	if sent[1].Msg.ReplacesID != sent[0].ID {
		t.Errorf("second notification ReplacesID = %d, want %d (replace, not stack)", sent[1].Msg.ReplacesID, sent[0].ID)
	}
	if !strings.Contains(s099Text(sent[1].Msg), "sys-apps/portage") {
		t.Errorf("replacing notification %q does not name the new package", s099Text(sent[1].Msg))
	}
}

func TestS099World_R2_6_SamePackagesReorderedDuplicatedOrNewVersionSendNothing(t *testing.T) {
	s := newS099(t)
	s.run.set(s099EmergePath, s099Seq(
		s099World(s099Qt, s099Vim),
		s099World(s099Vim, s099Qt),
		s099World(s099Qt, s099Vim, s099Qt),
		s099World("dev-qt/qtbase-6.10.2:6", "app-editors/vim-9.1.1001"),
	))
	s.startWorld(nil)
	s.firstCheck(s099EmergePath, "updates")
	for n := 2; n <= 4; n++ {
		s.resync(s099EmergePath, "updates", n)
	}
	if sent := s.updatesSent(); len(sent) != 1 {
		t.Errorf("updates notifications = %d, want 1: the set {qtbase, vim} never changed", len(sent))
	}
	if n := s.closedFor("updates"); n != 0 {
		t.Errorf("updates notification closed %d times, want 0", n)
	}
}

// --- R2.3, R2.4, R2.12, R5.3: the notice ---

func TestS099World_R2_3_R2_4_UpdatesAreALowUrgencyInfoNotice(t *testing.T) {
	s := newS099(t)
	s.run.set(s099EmergePath, s099Fixed(s099World(s099Qt, s099Vim, s099Port, s099Font), nil))
	s.startWorld(nil)
	s.firstCheck(s099EmergePath, "updates")
	sent := s.updatesSent()
	if len(sent) != 1 {
		t.Fatalf("updates notifications = %d, want 1", len(sent))
	}
	m := sent[0].Msg
	if m.Urgency != 0 {
		t.Errorf("urgency = %d, want 0 (low)", m.Urgency)
	}
	text := s099Text(m)
	for _, want := range []string{"4", "dev-qt/qtbase", "app-editors/vim"} {
		if !strings.Contains(text, want) {
			t.Errorf("notification %q does not name %q", text, want)
		}
	}
	for _, not := range []string{"sys-apps/portage", "font-adobe", "6.10.1", "9.1.1000", "::gentoo"} {
		if strings.Contains(text, not) {
			t.Errorf("notification %q carries %q; it names the first two category/name only", text, not)
		}
	}
	v := s.viewNow()
	if len(v.Alerts) != 0 || v.AlertCritical {
		t.Errorf("view Alerts = %q critical %v; pending updates never alert the icon (R2.12)", v.Alerts, v.AlertCritical)
	}
	if len(v.Info) != 1 {
		t.Errorf("view Info = %q, want one updates tooltip line", v.Info)
	}
	s.wantTransitions("updates", "", [2]string{"ok", "notice"})
}

func TestS099World_R2_7_NoPackageWithdraws(t *testing.T) {
	s := newS099(t)
	s.run.set(s099EmergePath, s099Seq(s099World(s099Qt), s099World()))
	s.startWorld(nil)
	s.firstCheck(s099EmergePath, "updates")
	s.resync(s099EmergePath, "updates", 2)
	sent := s.updatesSent()
	if len(sent) != 1 {
		t.Fatalf("updates notifications = %d, want 1", len(sent))
	}
	if !slices.Contains(s.out.closedIDs(), sent[0].ID) || s.closedFor("updates") != 1 {
		t.Errorf("closed ids %v keys %v, want the updates notification %d withdrawn once", s.out.closedIDs(), s.out.closedKeys(), sent[0].ID)
	}
	if v := s.viewNow(); len(v.Info) != 0 {
		t.Errorf("view Info = %q after no package is left, want none", v.Info)
	}
	s.wantTransitions("updates", "", [2]string{"ok", "notice"}, [2]string{"notice", "ok"})
}

func TestS099World_R2_7_NothingFromTheStartSendsNothing(t *testing.T) {
	s := newS099(t)
	s.run.set(s099EmergePath, s099Fixed(s099World(), nil))
	s.startWorld(nil)
	s.firstCheck(s099EmergePath, "updates")
	if sent := s.updatesSent(); len(sent) != 0 {
		t.Errorf("updates notifications = %d with nothing to merge, want 0", len(sent))
	}
}

// --- R2.8, R2.9: "does not resolve" ---

func TestS099World_R2_8_R2_9_DoesNotResolveReplacesThenCountComesBack(t *testing.T) {
	s := newS099(t)
	broken := pending.Result{Stdout: []byte("!!! Multiple package instances within a single package slot\n"),
		Stderr: []byte("emerge: there are no ebuilds to satisfy\n"), ExitCode: 1, Duration: 20 * time.Second}
	s.run.set(s099EmergePath, s099Seq(s099World(s099Qt, s099Vim), broken, broken, s099World(s099Qt, s099Vim, s099Port), s099World()))
	s.startWorld(nil)
	s.firstCheck(s099EmergePath, "updates")
	s.resync(s099EmergePath, "updates", 2)

	sent := s.updatesSent()
	if len(sent) != 2 {
		t.Fatalf("updates notifications = %d after a non-zero exit, want 2", len(sent))
	}
	b := sent[1].Msg
	if !strings.Contains(s099Text(b), "emerge -puDN @world") {
		t.Errorf("\"does not resolve\" notification %q does not tell the user to run emerge -puDN @world", s099Text(b))
	}
	if b.ReplacesID != sent[0].ID {
		t.Errorf("\"does not resolve\" ReplacesID = %d, want %d: it replaces the updates notification", b.ReplacesID, sent[0].ID)
	}
	if b.Urgency != 0 {
		t.Errorf("\"does not resolve\" urgency = %d, want 0 (low)", b.Urgency)
	}
	if v := s.viewNow(); len(v.Alerts) != 0 || len(v.Info) != 1 {
		t.Errorf("view Alerts %q Info %q, want no alert and one info line", v.Alerts, v.Info)
	}
	brokenInfo := s.viewNow().Info

	s.resync(s099EmergePath, "updates", 3) // still broken: same text, no send
	if n := len(s.updatesSent()); n != 2 {
		t.Errorf("a second identical non-zero exit sent %d more notifications, want 0", n-2)
	}

	s.resync(s099EmergePath, "updates", 4) // resolves again with 3 packages
	sent = s.updatesSent()
	if len(sent) != 3 {
		t.Fatalf("updates notifications = %d after the tree resolves again, want 3", len(sent))
	}
	back := s099Text(sent[2].Msg)
	if strings.Contains(back, "emerge -puDN") || !strings.Contains(back, "3") || !strings.Contains(back, "dev-qt/qtbase") {
		t.Errorf("notification after exit 0 = %q, want the count of 3 and package names, not the broken text", back)
	}
	if v := s.viewNow(); slices.Equal(v.Info, brokenInfo) {
		t.Errorf("tooltip still shows the \"does not resolve\" line %q", v.Info)
	}

	s.resync(s099EmergePath, "updates", 5) // nothing left
	if s.closedFor("updates") != 1 {
		t.Errorf("updates closes = %d, want 1 after count 0", s.closedFor("updates"))
	}
	if v := s.viewNow(); len(v.Info) != 0 {
		t.Errorf("view Info = %q, want empty", v.Info)
	}
}

func TestS099World_R2_8_StderrTextAloneIsNotAFailure(t *testing.T) {
	s := newS099(t)
	res := s099World(s099Qt)
	res.Stderr = []byte("renice: failed to set priority for 1234 (process ID): Permission denied\n")
	s.run.set(s099EmergePath, s099Fixed(res, nil))
	s.startWorld(nil)
	s.firstCheck(s099EmergePath, "updates")
	sent := s.updatesSent()
	if len(sent) != 1 || strings.Contains(s099Text(sent[0].Msg), "emerge -puDN") {
		t.Errorf("exit 0 with a renice warning on stderr sent %+v, want one updates count notification", sent)
	}
}

// --- R2.10: pause ---

func TestS099World_R2_10_PauseSilencesAndKeepsTheTooltip(t *testing.T) {
	s := newS099(t)
	broken := pending.Result{ExitCode: 1, Duration: time.Second}
	s.run.set(s099EmergePath, s099Seq(s099World(s099Qt), broken))
	s.startWorld(nil)
	s.pause()
	s.firstCheck(s099EmergePath, "updates")
	if n := len(s.updatesSent()); n != 0 {
		t.Errorf("updates notifications while paused = %d, want 0", n)
	}
	if v := s.viewNow(); len(v.Info) != 1 {
		t.Errorf("view Info while paused = %q, want the updates line", v.Info)
	}
	s.resync(s099EmergePath, "updates", 2)
	if n := len(s.updatesSent()); n != 0 {
		t.Errorf("\"does not resolve\" notifications while paused = %d, want 0", n)
	}
	if v := s.viewNow(); len(v.Info) != 1 {
		t.Errorf("view Info while paused = %q, want the \"does not resolve\" line", v.Info)
	}
}

// --- R2.11, R1.8: start failure and timeout ---

func TestS099World_R2_11_StartFailureDisablesWithOneWarn(t *testing.T) {
	for name, cause := range map[string]error{"not found": exec.ErrNotFound, "permission": os.ErrPermission} {
		t.Run(name, func(t *testing.T) {
			s := newS099(t)
			s.run.set(s099EmergePath, s099Fixed(pending.Result{}, s099StartErr(s099EmergePath, cause)))
			s.startWorld(nil)
			s.untilRun(s099EmergePath, 1, 14)
			s.waitLines("the disable WARN", 1, "level=WARN", "monitor=updates")
			for range 3 {
				s.bump()
				s.em.push(sysmon.EmergeEvent{Kind: sysmon.EmergeExitOK})
				s.samples(2)
			}
			if n := s.run.count(s099EmergePath); n != 1 {
				t.Errorf("emerge run %d times, want 1: a start failure disables the updates monitor", n)
			}
			if n := s.warnsFor("updates"); n != 1 {
				t.Errorf("updates WARN lines = %d, want exactly 1", n)
			}
			if !s.logLine(func(l string) bool {
				return strings.Contains(l, "level=WARN") && strings.Contains(l, "monitor=updates") && strings.Contains(l, s099EmergePath)
			}) {
				t.Error("the WARN does not name the command")
			}
			if len(s.updatesSent()) != 0 {
				t.Error("a start failure sent an updates notification")
			}
		})
	}
}

func TestS099World_R1_8_TimeoutKeepsStateWithOneWarn(t *testing.T) {
	s := newS099(t)
	s.run.set(s099EmergePath, func(n int) (pending.Result, error) {
		if n == 1 {
			return s099World(s099Qt, s099Vim), nil
		}
		return pending.Result{Duration: 10 * time.Minute}, s099TimeoutErr(s099EmergePath)
	})
	s.startWorld(nil)
	s.firstCheck(s099EmergePath, "updates")
	info := s.viewNow().Info
	s.bump()
	s.untilRun(s099EmergePath, 2, 4)
	s.waitLines("the timeout WARN", 1, "level=WARN", "monitor=updates")
	if n := s.warnsFor("updates"); n != 1 {
		t.Errorf("updates WARN lines after one timeout = %d, want 1", n)
	}
	if !s.logLine(func(l string) bool {
		return strings.Contains(l, "level=WARN") && strings.Contains(l, s099EmergePath) && strings.Contains(strings.ToLower(l), "time")
	}) {
		t.Error("the timeout WARN does not name the command and the timeout")
	}
	if s.closedFor("updates") != 0 || len(s.updatesSent()) != 1 {
		t.Errorf("a timeout changed the updates notification: sent %d closed %d", len(s.updatesSent()), s.closedFor("updates"))
	}
	if v := s.viewNow(); !slices.Equal(v.Info, info) {
		t.Errorf("view Info after a timeout = %q, want it kept as %q", v.Info, info)
	}
	// a timeout does not disable the monitor
	s.bump()
	s.untilRun(s099EmergePath, 3, 4)
}

// --- R5.7: one INFO per finished command ---

func TestS099World_R5_7_OneInfoLinePerCommandWithDurationAndFound(t *testing.T) {
	s := newS099(t)
	s.run.set(s099EmergePath, s099Fixed(s099World(s099Qt, s099Vim, s099Port), nil))
	s.startWorld(nil)
	s.firstCheck(s099EmergePath, "updates")
	lines := []string{}
	for _, l := range s.logs.lines() {
		if strings.Contains(l, "monitor=updates") && strings.Contains(l, "duration_s=") {
			lines = append(lines, l)
		}
	}
	if len(lines) != 1 {
		t.Fatalf("INFO lines with duration_s = %d, want 1: %q", len(lines), lines)
	}
	l := lines[0]
	if !strings.Contains(l, "level=INFO") || !strings.Contains(l, "found=3") || !strings.Contains(l, "duration_s=12") {
		t.Errorf("finish line = %s, want level=INFO, duration_s=12 and found=3", l)
	}
	if n := len(s099LevelKey.FindAllString(l, -1)); n != 1 {
		t.Errorf("finish line carries %d level keys, want 1", n)
	}
}

// --- R2.1: the command handed to the runner ---

func TestS099World_R2_1_TheConfiguredCommandReachesTheRunnerUnchanged(t *testing.T) {
	s := newS099(t)
	s.startWorld(nil)
	s.untilRun(s099EmergePath, 1, 14)
	cmds := s.run.commands()
	want := s099WorldCmd()
	got := cmds[0]
	if got.Path != want.Path || !slices.Equal(got.Args, want.Args) || got.Timeout != want.Timeout || !slices.Equal(got.Wrap, want.Wrap) {
		t.Errorf("Run got %+v, want %+v", got, *want)
	}
}

// --- R1.9, R1.7: off the loop, and quit ---

func TestS099World_R1_9_MenuHandledWithinOneSecondWhileACheckBlocks(t *testing.T) {
	s := newS099(t)
	release := s.run.hold()
	defer close(release)
	s.startWorld(nil)
	s.untilRun(s099EmergePath, 1, 14)
	s086WaitParked(s.appHarness)
	began := time.Now()
	s.menu(202)
	for {
		if st, n := s.store.last(); n > 0 && !st.PauseUntil.IsZero() {
			break
		}
		if time.Since(began) > time.Second {
			t.Fatal("the Pause click was not handled within 1 s while a check blocked")
		}
		time.Sleep(5 * time.Millisecond)
	}
	// the sample timer keeps running too, and starts no second check
	s.samples(3)
	if n := s.run.count(s099EmergePath); n != 1 {
		t.Errorf("emerge started %d times while the first check blocked, want 1 (R1.5)", n)
	}
}

func TestS099World_R1_7_QuitDuringACheckReturnsPromptlyAndCancelsIt(t *testing.T) {
	s := newS099(t)
	release := s.run.hold()
	defer close(release)
	s.startWorld(nil)
	s.untilRun(s099EmergePath, 1, 14)
	ctx := s.run.lastCtx()
	began := time.Now()
	s.menu(300) // Quit
	s086Stopped(t, s.appHarness)
	if d := time.Since(began); d > 2*time.Second {
		t.Errorf("Quit took %v while a check ran, want under 2 s", d)
	}
	select {
	case <-ctx.Done():
	case <-time.After(2 * time.Second):
		t.Error("the check's context was not cancelled by Quit: its process group would keep running")
	}
}

// --- triggers: R1.1, R1.3, R1.4, R1.10 ---

func TestS099World_R1_1_RepoRevisionsChangeTriggersACheck(t *testing.T) {
	s := newS099(t)
	s.startWorld(nil)
	s.firstCheck(s099EmergePath, "updates")
	s.samples(6)
	if n := s.run.count(s099EmergePath); n != 1 {
		t.Fatalf("emerge ran %d times with no trigger after the start check, want 1", n)
	}
	s.bump()
	s.untilRun(s099EmergePath, 2, 3)
	s.checked("updates", 2)
	s.samples(4)
	if n := s.run.count(s099EmergePath); n != 2 {
		t.Errorf("one repo_revisions change ran emerge %d more times, want 1", n-1)
	}
}

func TestS099World_R1_1_FirstSightOfRepoRevisionsIsNoTrigger(t *testing.T) {
	s := newS099(t)
	s.startWorld(nil)
	s.samples(4) // well before the 5-minute start delay
	if n := s.run.count(s099EmergePath); n != 0 {
		t.Errorf("emerge ran %d times before the start delay; first sight of repo_revisions is not a sync", n)
	}
}

func TestS099World_R1_3_SuccessfulEmergeTriggersACheck(t *testing.T) {
	s := newS099(t)
	s.startWorld(nil)
	s.firstCheck(s099EmergePath, "updates")
	s.em.push(sysmon.EmergeEvent{Kind: sysmon.EmergeExitFailed, Status: 1})
	s.samples(3)
	if n := s.run.count(s099EmergePath); n != 1 {
		t.Fatalf("a failed emerge run triggered a check (%d runs), want none", n)
	}
	s.em.push(sysmon.EmergeEvent{Kind: sysmon.EmergeExitOK})
	s.untilRun(s099EmergePath, 2, 3)
}

func TestS099World_R1_4_RunningEmergeHoldsTheCheck(t *testing.T) {
	t.Run("through the SystemReader", func(t *testing.T) {
		s := newS099(t)
		s.sys.setCPU(5, true)
		s.startWorld(nil)
		s.samples(14)
		if n := s.run.count(s099EmergePath); n != 0 {
			t.Fatalf("emerge --pretend ran %d times while an emerge was running", n)
		}
		s.sys.setCPU(5, false)
		s.untilRun(s099EmergePath, 1, 2)
	})
	t.Run("through Deps.EmergeRunning with no SystemReader", func(t *testing.T) {
		s := newS099(t)
		var running atomic.Bool
		running.Store(true)
		s.startWorld(func(d *tray.Deps) {
			d.Sys = nil
			d.EmergeRunning = func() (bool, error) { return running.Load(), nil }
		})
		s.samples(14)
		if n := s.run.count(s099EmergePath); n != 0 {
			t.Fatalf("emerge --pretend ran %d times while an emerge was running", n)
		}
		running.Store(false)
		s.untilRun(s099EmergePath, 1, 2)
	})
}

func TestS099World_R1_4_EmergeScanErrorCountsAsNotRunning(t *testing.T) {
	s := newS099(t)
	s.startWorld(func(d *tray.Deps) {
		d.Sys = nil
		d.EmergeRunning = func() (bool, error) { return false, errors.New("reading /proc: permission denied") }
	})
	s.untilRun(s099EmergePath, 1, 14)
}

func TestS099World_R1_10_BothMonitorsDisabledRunNothing(t *testing.T) {
	s := newS099(t)
	s.cfg = s097Config(t, "monitors:\n  updates:\n    enabled: false\n  glsa:\n    enabled: false\n")
	s.startUpd(func(d *tray.Deps) {
		d.Updates = s099WorldCmd()
		d.GLSA = s099GLSACmd()
	})
	s.samples(12)
	s.bump()
	s.em.push(sysmon.EmergeEvent{Kind: sysmon.EmergeExitOK})
	s.samples(4)
	if n := s.run.total(); n != 0 {
		t.Errorf("Run called %d times with updates and glsa disabled, want 0", n)
	}
}

func TestS099World_R1_10_AbsentCommandsRunNothing(t *testing.T) {
	s := newS099(t)
	s.startUpd(nil) // Updates and GLSA nil: cmd found neither command
	s.samples(12)
	if n := s.run.total(); n != 0 {
		t.Errorf("Run called %d times with no command configured, want 0", n)
	}
}
