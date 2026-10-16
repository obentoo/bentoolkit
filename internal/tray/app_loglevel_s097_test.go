package tray_test

// App-level tests for story 097, sub-task 14.1: every condition-transition
// INFO line carries the condition under `state`, with `from` beside it, and
// exactly one `level` key, the record's own (R6.13, Q13). A `level`
// attribute is emitted next to the record's level, and a key=value parser
// keeps only one of the two, so either the severity or the condition is lost.
//
// Lines are read from the raw text the App's slog.TextHandler wrote, split
// into an ordered list of key/value pairs: a duplicate key stays two pairs,
// never one map entry, so a line with two `level` keys cannot pass.
//
// The harness (s097Harness, s097Sys, s097Notifier) lives in
// app_health_s097_test.go, the network helpers in app_netalert_s097_test.go,
// the downer* paths in app_diskowner_s097_test.go and handBOwnsWithWarning in
// app_handover_s097_test.go.

import (
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"
)

// loglvlTransitionMsg is the message of a condition-transition line.
const loglvlTransitionMsg = "system condition changed"

type loglvlPair struct{ key, val string }

// loglvlPairRe matches one key=value of slog's text format; a value with
// spaces or quotes is Go-quoted.
var loglvlPairRe = regexp.MustCompile(`(?:^|\s)([^\s="]+)=("(?:[^"\\]|\\.)*"|\S*)`)

// loglvlPairs splits a text log line into its key/value pairs, in order and
// with duplicate keys kept.
func loglvlPairs(line string) []loglvlPair {
	var out []loglvlPair
	for _, m := range loglvlPairRe.FindAllStringSubmatch(line, -1) {
		v := m[2]
		if strings.HasPrefix(v, `"`) {
			if u, err := strconv.Unquote(v); err == nil {
				v = u
			}
		}
		out = append(out, loglvlPair{m[1], v})
	}
	return out
}

// loglvlValues returns every value of key in pairs, one per occurrence.
func loglvlValues(pairs []loglvlPair, key string) []string {
	var out []string
	for _, p := range pairs {
		if p.key == key {
			out = append(out, p.val)
		}
	}
	return out
}

// loglvlIsTransition reports whether pairs are a condition-transition line.
func loglvlIsTransition(pairs []loglvlPair) bool {
	msgs := loglvlValues(pairs, "msg")
	return len(msgs) == 1 && msgs[0] == loglvlTransitionMsg
}

// loglvlTransitions returns the transition lines of monitor and target, in
// the order they were logged.
func loglvlTransitions(s *s097Harness, monitor, target string) []string {
	var out []string
	for _, l := range s.logs.lines() {
		p := loglvlPairs(l)
		if !loglvlIsTransition(p) {
			continue
		}
		m, tg := loglvlValues(p, "monitor"), loglvlValues(p, "target")
		if len(m) >= 1 && m[0] == monitor && len(tg) >= 1 && tg[0] == target {
			out = append(out, l)
		}
	}
	return out
}

// loglvlWantTransitions fails unless monitor/target logged exactly n
// transition lines, and returns them; a missing line never passes silently.
func loglvlWantTransitions(t *testing.T, s *s097Harness, when, monitor, target string, n int) []string {
	t.Helper()
	got := loglvlTransitions(s, monitor, target)
	if len(got) != n {
		t.Fatalf("%s: transition lines of monitor=%s target=%q = %d, want %d\n%s",
			when, monitor, target, len(got), n, strings.Join(s.logs.lines(), "\n"))
	}
	return got
}

// loglvlCheck fails unless line carries exactly one `level` key, valued INFO
// (checked first: the hostile half), and the condition under exactly one
// `state` key with exactly one `from` beside it.
func loglvlCheck(t *testing.T, line, wantState, wantFrom string) {
	t.Helper()
	p := loglvlPairs(line)
	if lv := loglvlValues(p, "level"); len(lv) != 1 || lv[0] != "INFO" {
		t.Errorf("transition line carries level keys %q, want exactly one, the record's INFO (R6.13): %s", lv, line)
	}
	if st := loglvlValues(p, "state"); len(st) != 1 || st[0] != wantState {
		t.Errorf("transition line carries state keys %q, want exactly one, %q (R6.13): %s", st, wantState, line)
	}
	if fr := loglvlValues(p, "from"); len(fr) != 1 || fr[0] != wantFrom {
		t.Errorf("transition line carries from keys %q, want exactly one, %q (R6.13): %s", fr, wantFrom, line)
	}
	for _, k := range []string{"monitor", "target"} {
		if n := len(loglvlValues(p, k)); n != 1 {
			t.Errorf("transition line carries %d %s keys, want 1: %s", n, k, line)
		}
	}
}

// loglvlEveryLine fails when any line of the App carries other than one
// `level` key, or a line that is not a condition transition carries a
// `state` key (Q13).
func loglvlEveryLine(t *testing.T, s *s097Harness) {
	t.Helper()
	for _, l := range s.logs.lines() {
		if strings.TrimSpace(l) == "" {
			continue
		}
		p := loglvlPairs(l)
		if n := len(loglvlValues(p, "level")); n != 1 {
			t.Errorf("log line carries %d level keys, want exactly one (Q13): %s", n, l)
		}
		if !loglvlIsTransition(p) {
			if st := loglvlValues(p, "state"); len(st) != 0 {
				t.Errorf("a line that is no condition transition carries state keys %q: %s", st, l)
			}
		}
	}
}

// TestS097LogLevel_R6_13_MemoryTransitionsKeepOneLevelKey: becoming active,
// escalating to critical and clearing each log at INFO with one level key;
// critical is the condition's state, never the record's level.
func TestS097LogLevel_R6_13_MemoryTransitionsKeepOneLevelKey(t *testing.T) {
	s := newS097(t)
	s.sys.setMem(s097MemWarn(), nil)
	s.start(nil, nil)

	s.tickUntil("memory warning", 8, func() bool { return len(s.out.matching(s097IsMemory)) == 1 })
	s.tick()
	lines := loglvlWantTransitions(t, s, "after the memory warning", "memory", "", 1)
	loglvlCheck(t, lines[0], "warning", "ok")

	s.sys.setMem(s097MemCrit(), nil)
	s.tickUntil("memory escalation", 8, func() bool { return len(s.out.matching(s097IsMemory)) == 2 })
	s.tick()
	lines = loglvlWantTransitions(t, s, "after the escalation", "memory", "", 2)
	loglvlCheck(t, lines[1], "critical", "warning")

	s.sys.setMem(s097MemOK(), nil)
	s.tickUntil("memory withdrawn", 8, func() bool { return len(s.out.closedIDs()) == 1 })
	s.tick()
	lines = loglvlWantTransitions(t, s, "after clearing", "memory", "", 3)
	loglvlCheck(t, lines[2], "ok", "critical")

	loglvlEveryLine(t, s)
}

// TestS097LogLevel_R6_13_CriticalDiskRaiseAndClearLogAtInfo: a disk raised
// straight to critical logs state=critical at level INFO, and its clear logs
// state=ok, each with one level key.
func TestS097LogLevel_R6_13_CriticalDiskRaiseAndClearLogAtInfo(t *testing.T) {
	s := newS097(t)
	s.sys.setDisk(s097Disk("/", 1, s097DiskCrit))
	s.start([]string{"/"}, nil)

	s.tickUntil("critical disk", 8, func() bool { return len(s.out.matching(s097IsDiskOf("/"))) == 1 })
	s.tick()
	lines := loglvlWantTransitions(t, s, "after the critical disk", "disk", "/", 1)
	loglvlCheck(t, lines[0], "critical", "ok")

	s.sys.setDisk(s097Disk("/", 1, s097DiskOK))
	s.tickUntil("disk withdrawn", 8, func() bool { return len(s.out.closedIDs()) == 1 })
	s.tick()
	lines = loglvlWantTransitions(t, s, "after the disk cleared", "disk", "/", 2)
	loglvlCheck(t, lines[1], "ok", "critical")

	loglvlEveryLine(t, s)
}

// TestS097LogLevel_R6_13_CPUTransitionsKeepOneLevelKey: CPU pressure raised
// and cleared logs state=warning then state=ok, each with one level key.
func TestS097LogLevel_R6_13_CPUTransitionsKeepOneLevelKey(t *testing.T) {
	s := newS097(t)
	s.sys.setCPU(95, false)
	s.start(nil, nil)

	s.tickUntil("a CPU notification", 14, func() bool { return len(s.out.matching(s097IsCPU)) == 1 })
	s.tick()
	lines := loglvlWantTransitions(t, s, "after the CPU warning", "cpu", "", 1)
	loglvlCheck(t, lines[0], "warning", "ok")

	s.sys.setCPU(0, false)
	s.tickUntil("CPU withdrawn", 14, func() bool { return len(s.out.closedIDs()) == 1 })
	s.tick()
	lines = loglvlWantTransitions(t, s, "after the CPU cleared", "cpu", "", 2)
	loglvlCheck(t, lines[1], "ok", "warning")

	loglvlEveryLine(t, s)
}

// TestS097LogLevel_R6_13_NetworkDownAndBackKeepOneLevelKey: "network down"
// logs state=warning and "network back" state=ok, each with one level key.
func TestS097LogLevel_R6_13_NetworkDownAndBackKeepOneLevelKey(t *testing.T) {
	s := newS097Net(t)
	s.startNet()

	s.setOnline(false)
	s.advance(30 * time.Second)
	s.waitFor("network down", func() bool { return len(s.out.matching(s097IsNetDown)) == 1 })
	s086WaitParked(s.appHarness)
	lines := loglvlWantTransitions(t, s, "after network down", "network", "", 1)
	loglvlCheck(t, lines[0], "warning", "ok")

	s.setOnline(true)
	s.advance(10 * time.Second)
	s.waitFor("network back", func() bool { return len(s.out.matching(s097IsNetBack)) == 1 })
	s086WaitParked(s.appHarness)
	lines = loglvlWantTransitions(t, s, "after network back", "network", "", 2)
	loglvlCheck(t, lines[1], "ok", "warning")

	loglvlEveryLine(t, s)
}

// TestS097LogLevel_R6_13_YieldedDiskKeepsOneLevelKey: a disk condition
// withdrawn because its filesystem is now evaluated under an earlier path
// logs state=ok with one level key, whether the path still reads or fails.
func TestS097LogLevel_R6_13_YieldedDiskKeepsOneLevelKey(t *testing.T) {
	t.Run("a reading path deduplicated under an earlier one", func(t *testing.T) {
		const tmp = "/var/tmp/portage"
		s := newS097(t)
		s.sys.setDisk(s097Disk("/", 1, s097DiskOK))
		s.sys.setDisk(s097Disk(tmp, 2, s097DiskCrit))
		s.start([]string{"/", tmp}, nil)
		s.tickUntil("critical disk", 8, func() bool { return len(s.out.matching(s097IsDiskOf(tmp))) == 1 })

		// The tmpfs is unmounted: the path now lives on /'s filesystem.
		s.sys.setDisk(s097Disk(tmp, 1, s097DiskOK))
		s.ticks(2)
		lines := loglvlWantTransitions(t, s, "after the path joined /", "disk", tmp, 2)
		loglvlCheck(t, lines[0], "critical", "ok")
		loglvlCheck(t, lines[1], "ok", "critical")
		loglvlEveryLine(t, s)
	})

	t.Run("a failing path yields to an earlier owner", func(t *testing.T) {
		s := newS097(t)
		handBOwnsWithWarning(t, s, []string{downerA, downerB})

		s.sys.setDiskErr(downerB, errDownerIO)
		s.sys.setDisk(s097Disk(downerA, 1, s097DiskOK))
		s.ticks(3)
		lines := loglvlWantTransitions(t, s, "after "+downerB+" yielded", "disk", downerB, 2)
		loglvlCheck(t, lines[0], "warning", "ok")
		loglvlCheck(t, lines[1], "ok", "warning")
		loglvlEveryLine(t, s)
	})
}
