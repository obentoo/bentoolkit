package tray_test

// Shared harness of the story 098 App tests: the 097 harness (fake clock,
// SystemReader, Notifier with Close, sample ticks) plus helpers to pick a
// condition's notifications by key, read its transition lines and pause.
// Each 098 test file adds the fakes of its own monitors.

import (
	"regexp"
	"strings"
	"testing"

	"github.com/obentoo/bentoolkit/internal/desktop/notify"
	"github.com/obentoo/bentoolkit/internal/tray/state"
)

type s098Harness struct {
	*s097Harness
}

func newS098(t *testing.T) *s098Harness {
	return &s098Harness{s097Harness: newS097(t)}
}

// s098Keyed matches the system notifications of one notification key:
// "emerge", "units", "readonly:<mount point>" or "smart:<object path>".
func s098Keyed(key string) func(notify.Message) bool {
	return func(m notify.Message) bool { return m.System && m.Key == key }
}

// s098LevelKey finds every "level=" key of a text log line.
var s098LevelKey = regexp.MustCompile(`(^|\s)level=`)

// transitions returns the "system condition changed" lines of monitor.
func (s *s098Harness) transitions(monitor string) []string {
	var out []string
	for _, l := range s.logs.lines() {
		if strings.Contains(l, `msg="system condition changed"`) && strings.Contains(l, "monitor="+monitor+" ") {
			out = append(out, l)
		}
	}
	return out
}

// wantTransitions checks monitor's transition lines in order: each carries
// state=<to> and from=<from>, and exactly one level key (the record's own).
func (s *s098Harness) wantTransitions(monitor string, fromTo ...[2]string) {
	s.t.Helper()
	lines := s.transitions(monitor)
	if len(lines) != len(fromTo) {
		s.t.Fatalf("%s transition lines = %d, want %d:\n%s", monitor, len(lines), len(fromTo), strings.Join(lines, "\n"))
	}
	for i, ft := range fromTo {
		l := lines[i]
		if !strings.Contains(l, "level=INFO") {
			s.t.Errorf("%s transition %d is not an INFO line: %s", monitor, i, l)
		}
		if !strings.Contains(l, "from="+ft[0]) || !strings.Contains(l, "state="+ft[1]) {
			s.t.Errorf("%s transition %d = %s, want from=%s state=%s", monitor, i, l, ft[0], ft[1])
		}
		if n := len(s098LevelKey.FindAllString(l, -1)); n != 1 {
			s.t.Errorf("%s transition %d carries %d level keys, want exactly 1: %s", monitor, i, n, l)
		}
	}
}

// warns counts the WARN lines that contain every part.
func (s *s098Harness) warns(parts ...string) int {
	return s.countLines(append([]string{"level=WARN"}, parts...)...)
}

// pause clicks "Pause for 1 hour" and waits until it is saved.
func (s *s098Harness) pause() {
	s.t.Helper()
	s.menu(202)
	s.savedWhere("the pause", func(st state.State) bool { return !st.PauseUntil.IsZero() })
	s086WaitParked(s.appHarness)
}

// resume clicks "Resume" and waits until it is saved.
func (s *s098Harness) resume() {
	s.t.Helper()
	s.menu(204)
	s.savedWhere("the resume", func(st state.State) bool { return st.PauseUntil.IsZero() })
	s086WaitParked(s.appHarness)
}

// alertLines is the tooltip's current alert lines.
func (s *s098Harness) alertLines() []string {
	return s.viewNow().Alerts
}
