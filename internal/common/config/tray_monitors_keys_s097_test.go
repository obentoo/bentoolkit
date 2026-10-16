package config

import (
	"strings"
	"testing"
)

// Story 097, sub-task 12.2: an unknown-key warning under tray.monitors names
// the key the YAML decoder reads. A key written as an alias names its target,
// never the anchor; a key that is not text is left to the R9.7 block warning
// alone (R9.10). A plain unknown key is still reported once by its full path
// (R9.5).

// s097KeysWarnings loads doc (the content of tray.monitors) and returns every
// warning it produced: the unknown-key lines LoadFrom wrote to stderr and the
// warnings GetMonitors returned, plus the resolved monitors.
func s097KeysWarnings(t *testing.T, monitors string) (Monitors, []string, []string) {
	t.Helper()
	cfg, stderr := s097ShapeLoad(t, s097ShapeBase+"tray:\n  monitors:\n"+monitors)
	var lines []string
	for _, l := range strings.Split(stderr, "\n") {
		if strings.TrimSpace(l) != "" {
			lines = append(lines, l)
		}
	}
	m, warns := cfg.Tray.GetMonitors()
	return m, lines, warns
}

// s097KeysCount counts the warnings, across stderr and GetMonitors, that name
// key itself (not a longer key it is a prefix of).
func s097KeysCount(key string, stderr, warns []string) int {
	n := 0
	for _, w := range append(append([]string{}, stderr...), warns...) {
		if s097NamesKey(w, key) {
			n++
		}
	}
	return n
}

// TestS097MonitorKeys_AliasKeyToKnownKeyIsRead is the hostile half that would
// wrongly split: an alias key whose target is warn_percent is the key
// warn_percent, spelled differently. It must draw no unknown-key warning and
// its value must apply, even when the anchor itself is named like an unknown
// key.
func TestS097MonitorKeys_AliasKeyToKnownKeyIsRead(t *testing.T) {
	for _, anchor := range []string{"anch", "bogus"} {
		t.Run("anchor_"+anchor, func(t *testing.T) {
			m, stderr, warns := s097KeysWarnings(t,
				"    disk:\n"+
					"      &"+anchor+" warn_percent: 7\n"+
					"    memory:\n"+
					"      *"+anchor+" : 8\n")
			if len(stderr) != 0 || len(warns) != 0 {
				t.Errorf("an alias key whose target is warn_percent warned; stderr %q, GetMonitors %q", stderr, warns)
			}
			if m.Disk.WarnPercent != 7 {
				t.Errorf("Disk.WarnPercent = %v, want 7 (the anchored key itself)", m.Disk.WarnPercent)
			}
			if m.Memory.WarnPercent != 8 {
				t.Errorf("Memory.WarnPercent = %v, want 8: the value under the alias key *%s (target warn_percent) did not apply", m.Memory.WarnPercent, anchor)
			}
		})
	}
}

// TestS097MonitorKeys_AliasKeyToUnknownKeyNamesTarget is the hostile half
// that would wrongly collapse: an alias key whose target is bogus is reported
// as tray.monitors.memory.bogus — never by its anchor name, and never hidden
// because the anchor happens to be named like a known key. The anchored key
// itself (disk.bogus) is a third, like-named entry; each block's report must
// stay distinct and appear exactly once.
func TestS097MonitorKeys_AliasKeyToUnknownKeyNamesTarget(t *testing.T) {
	for _, anchor := range []string{"anch", "warn_percent"} {
		t.Run("anchor_"+anchor, func(t *testing.T) {
			_, stderr, warns := s097KeysWarnings(t,
				"    disk:\n"+
					"      &"+anchor+" bogus: 1\n"+
					"    memory:\n"+
					"      *"+anchor+" : 2\n")
			if n := s097KeysCount("tray.monitors.memory.bogus", stderr, warns); n != 1 {
				t.Errorf("the alias key *%s (target bogus) is reported as tray.monitors.memory.bogus on %d lines, want 1; stderr %q, GetMonitors %q", anchor, n, stderr, warns)
			}
			if n := s097KeysCount("tray.monitors.disk.bogus", stderr, warns); n != 1 {
				t.Errorf("the anchored key disk.bogus is reported on %d lines, want 1; stderr %q, GetMonitors %q", n, stderr, warns)
			}
			if n := s097KeysCount("tray.monitors.memory."+anchor, stderr, warns); n != 0 {
				t.Errorf("a warning names the anchor tray.monitors.memory.%s instead of the key the decoder reads; stderr %q, GetMonitors %q", anchor, stderr, warns)
			}
			if total := len(stderr) + len(warns); total != 2 {
				t.Errorf("got %d warnings, want exactly 2 (disk.bogus, memory.bogus); stderr %q, GetMonitors %q", total, stderr, warns)
			}
		})
	}
}

// TestS097MonitorKeys_NonTextKeyLeftToBlockWarning: a key that is not text
// (a sequence, or an alias whose target is a sequence) makes the block
// undecodable. It yields exactly one warning in total — the R9.7 block
// warning naming tray.monitors.disk — and no unknown-key line, empty or not.
func TestS097MonitorKeys_NonTextKeyLeftToBlockWarning(t *testing.T) {
	cases := []struct{ name, yaml string }{
		{"sequence_key", "    disk:\n      [a, b]: 1\n"},
		{"alias_to_sequence_key", "    disk:\n      paths: &seq [/, /home]\n      *seq : 1\n"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m, stderr, warns := s097KeysWarnings(t, tc.yaml)
			for _, l := range stderr {
				if strings.Contains(l, "unknown key") {
					t.Errorf("a non-text key drew an unknown-key line %q; it belongs to the block warning alone", l)
				}
			}
			if total := len(stderr) + len(warns); total != 1 {
				t.Errorf("got %d warnings, want exactly one (the block warning); stderr %q, GetMonitors %q", total, stderr, warns)
			}
			if len(warns) != 1 || !s097NamesKey(warns[0], "tray.monitors.disk") {
				t.Errorf("GetMonitors warnings = %q, want one block warning naming tray.monitors.disk", warns)
			}
			if !s097BlockIs(m.Disk, s097Defaults(t).Disk) {
				t.Errorf("Disk = %+v, want the disk defaults after the block warning", m.Disk)
			}
		})
	}
}

// TestS097MonitorKeys_PlainUnknownKeyStillReportedOnce is R9.5 unchanged: a
// plain unknown key is reported once by its full path, next to a known key
// that still applies, and a merge key is still accepted silently.
func TestS097MonitorKeys_PlainUnknownKeyStillReportedOnce(t *testing.T) {
	m, stderr, warns := s097KeysWarnings(t,
		"    cpu: &c {warn_psi: 20}\n"+
			"    memory:\n"+
			"      warn_percent: 8\n"+
			"      bogus: 1\n"+
			"    disk:\n"+
			"      <<: *c\n"+
			"      warn_percent: 7\n")
	if n := s097KeysCount("tray.monitors.memory.bogus", stderr, warns); n != 1 {
		t.Errorf("tray.monitors.memory.bogus reported on %d lines, want 1; stderr %q, GetMonitors %q", n, stderr, warns)
	}
	if total := len(stderr) + len(warns); total != 1 {
		t.Errorf("got %d warnings, want exactly one; stderr %q, GetMonitors %q", total, stderr, warns)
	}
	if m.Memory.WarnPercent != 8 || m.Disk.WarnPercent != 7 {
		t.Errorf("Memory.WarnPercent = %v, Disk.WarnPercent = %v; want 8 and 7", m.Memory.WarnPercent, m.Disk.WarnPercent)
	}
}
