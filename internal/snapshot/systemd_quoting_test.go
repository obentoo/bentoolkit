package snapshot

import (
	"strings"
	"testing"
)

func s053ExecStart(unit string) string {
	for _, l := range strings.Split(unit, "\n") {
		if strings.HasPrefix(l, "ExecStart=") {
			return l
		}
	}
	return ""
}

// TestRenderServiceUnit_QuotesExecStartArgs pins R7.2/R7.3; the "already"
// cases are the third-element collisions: an input that already LOOKS escaped
// must be escaped again, never taken as-is.
func TestRenderServiceUnit_QuotesExecStartArgs(t *testing.T) {
	const pre = "ExecStart=bentoo snapshot run --config "
	cases := []struct{ name, exec, conf, want string }{
		{"ordinary stays unquoted", "bentoo", "/etc/bentoo/snapshot.toml", pre + "/etc/bentoo/snapshot.toml"},
		{"space", "bentoo", "/etc/bentoo/my snap.toml", pre + `"/etc/bentoo/my snap.toml"`},
		{"percent", "bentoo", "/etc/bentoo/100%.toml", pre + "/etc/bentoo/100%%.toml"},
		{"dollar", "bentoo", "/etc/$HOME.toml", pre + "/etc/$$HOME.toml"},
		{"space and dollar", "bentoo", "/etc/my $x.toml", pre + `"/etc/my $$x.toml"`},
		{"space and percent", "bentoo", "/etc/my 100%.toml", pre + `"/etc/my 100%%.toml"`},
		{"double quote", "bentoo", `/etc/a"b.toml`, pre + `"/etc/a\"b.toml"`},
		{"single quote", "bentoo", `/etc/it's.toml`, pre + `"/etc/it's.toml"`},
		{"backslash", "bentoo", `/etc/a\b.toml`, pre + `"/etc/a\\b.toml"`},
		{"lone semicolon", "bentoo", ";", pre + `";"`},
		{"empty", "bentoo", "", pre + `""`},
		{"already doubled percent", "bentoo", "/etc/a%%b.toml", pre + "/etc/a%%%%b.toml"},
		{"already escaped quote", "bentoo", `/etc/a\"b.toml`, pre + `"/etc/a\\\"b.toml"`},
		{"exec path with space", "/opt/my tools/bentoo", "/etc/bentoo/snapshot.toml",
			`ExecStart="/opt/my tools/bentoo" snapshot run --config /etc/bentoo/snapshot.toml`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			unit, err := renderServiceUnit(tc.exec, tc.conf)
			if err != nil {
				t.Fatalf("renderServiceUnit: %v", err)
			}
			if got := s053ExecStart(unit); got != tc.want {
				t.Errorf("got  %s\nwant %s", got, tc.want)
			}
		})
	}
}

// TestRenderServiceUnit_RejectsControlCharacters pins R7.4 (and that UTF-8 is
// not a control character).
func TestRenderServiceUnit_RejectsControlCharacters(t *testing.T) {
	for name, p := range map[string][2]string{
		"newline in config": {"bentoo", "/etc/a\nExecStartPre=/bin/evil"},
		"tab in config":     {"bentoo", "/etc/a\tb.toml"},
		"nul in config":     {"bentoo", "/etc/a\x00.toml"},
		"DEL in exec":       {"bentoo\x7f", "/etc/bentoo/snapshot.toml"},
		"newline in exec":   {"bentoo\n", "/etc/bentoo/snapshot.toml"},
	} {
		if _, err := renderServiceUnit(p[0], p[1]); err == nil {
			t.Errorf("%s: renderServiceUnit = nil error, want a refusal", name)
		}
	}
	unit, err := renderServiceUnit("bentoo", "/etc/bentoo/ção.toml")
	if err != nil || !strings.Contains(unit, "/etc/bentoo/ção.toml") {
		t.Errorf("UTF-8 path: (%q, %v), want accepted verbatim", s053ExecStart(unit), err)
	}
	if _, err := renderTimerUnit(ScheduleConfig{OnCalendar: "daily"}); err != nil {
		t.Errorf("renderTimerUnit(daily) = %v, want nil", err)
	}
}
