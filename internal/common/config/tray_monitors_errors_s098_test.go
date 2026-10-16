package config

import (
	"reflect"
	"strings"
	"testing"
)

// Story 098, sub-task 1.1: tray.monitors gains four blocks, emerge, units,
// readonly and smart. Absent, each enables its monitor with its defaults
// (R6.1); `enabled: false` turns off that monitor alone (R6.2); emerge.log
// replaces /var/log/emerge.log (R6.3); units.system and units.user switch off
// one manager each (R6.4); and the new keys and blocks are validated, warned
// about and defaulted exactly like the story 097 ones (R6.5: §9.4 bad value,
// §9.5 unknown key, §9.7 block of the wrong shape, §9.9 save round-trip,
// §9.10 alias handling).

// s098DefaultEmergeLog is the documented default of tray.monitors.emerge.log.
const s098DefaultEmergeLog = "/var/log/emerge.log"

// s098Switches lists every on/off key of the four new blocks with a getter,
// relative to tray.monitors. Each defaults to true.
var s098Switches = []struct {
	key string
	get func(Monitors) bool
}{
	{"emerge.enabled", func(m Monitors) bool { return m.Emerge.Enabled }},
	{"units.enabled", func(m Monitors) bool { return m.Units.Enabled }},
	{"units.system", func(m Monitors) bool { return m.Units.System }},
	{"units.user", func(m Monitors) bool { return m.Units.User }},
	{"readonly.enabled", func(m Monitors) bool { return m.Readonly.Enabled }},
	{"smart.enabled", func(m Monitors) bool { return m.Smart.Enabled }},
}

// s098Enabled maps every monitor, old and new, to its resolved on/off switch.
func s098Enabled(m Monitors) map[string]bool {
	return map[string]bool{
		"disk": m.Disk.Enabled, "memory": m.Memory.Enabled, "cpu": m.CPU.Enabled, "network": m.Network.Enabled,
		"emerge": m.Emerge.Enabled, "units": m.Units.Enabled, "readonly": m.Readonly.Enabled, "smart": m.Smart.Enabled,
	}
}

// s098CheckDefaults reports every new key of m that is not at its default.
func s098CheckDefaults(t *testing.T, label string, m Monitors) {
	t.Helper()
	for _, s := range s098Switches {
		if !s.get(m) {
			t.Errorf("%s: tray.monitors.%s = false, want the default true (R6.1)", label, s.key)
		}
	}
	if m.Emerge.Log != s098DefaultEmergeLog {
		t.Errorf("%s: tray.monitors.emerge.log = %q, want the default %q (R6.1)", label, m.Emerge.Log, s098DefaultEmergeLog)
	}
}

// s098WarningsNaming returns the warnings, across stderr and GetMonitors,
// that name key itself.
func s098WarningsNaming(key string, stderr, warns []string) []string {
	var out []string
	for _, w := range append(append([]string{}, stderr...), warns...) {
		if s097NamesKey(w, key) {
			out = append(out, w)
		}
	}
	return out
}

// TestS098MonitorConfig_AbsentBlocksYieldDefaults is R6.1: no section, a
// section that carries only the story 097 blocks, empty new blocks and null
// new blocks all resolve every new key to its default without a warning.
func TestS098MonitorConfig_AbsentBlocksYieldDefaults(t *testing.T) {
	m, warns := TrayConfig{}.GetMonitors()
	if len(warns) != 0 {
		t.Errorf("an absent tray.monitors section warned %q", warns)
	}
	s098CheckDefaults(t, "absent section", m)

	cases := []struct{ name, body string }{
		{"only 097 blocks", "disk:\n  warn_percent: 7\nmemory: {}\n"},
		{"empty new blocks", "emerge: {}\nunits: {}\nreadonly: {}\nsmart: {}\n"},
		{"null new blocks", "emerge: ~\nunits: ~\nreadonly:\nsmart: null\n"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m, warns := monitorsFrom(t, tc.body)
			if len(warns) != 0 {
				t.Errorf("GetMonitors warned %q, want nothing", warns)
			}
			s098CheckDefaults(t, tc.name, m)
		})
	}
}

// TestS098MonitorConfig_DisableIsPerMonitor is R6.2 and its hostile half: the
// leaf `enabled` is shared by eight blocks, and switching one off must never
// switch off another — in either direction between the new blocks and the
// story 097 ones.
func TestS098MonitorConfig_DisableIsPerMonitor(t *testing.T) {
	all := []string{"disk", "memory", "cpu", "network", "emerge", "units", "readonly", "smart"}
	for _, off := range all {
		t.Run(off, func(t *testing.T) {
			m, warns := monitorsFrom(t, off+":\n  enabled: false\n")
			if len(warns) != 0 {
				t.Fatalf("%s.enabled: false warned %q", off, warns)
			}
			for name, on := range s098Enabled(m) {
				if name == off && on {
					t.Errorf("tray.monitors.%s.enabled: false left the %s monitor enabled (R6.2)", off, name)
				}
				if name != off && !on {
					t.Errorf("tray.monitors.%s.enabled: false also disabled the %s monitor", off, name)
				}
			}
		})
	}
}

// TestS098MonitorConfig_UnitsManagersAreIndependent is R6.4: units.system and
// units.user each switch off one manager, never the other, and neither turns
// off the units monitor itself nor the smart monitor, which also reads the
// system bus.
func TestS098MonitorConfig_UnitsManagersAreIndependent(t *testing.T) {
	cases := []struct {
		body         string
		system, user bool
	}{
		{"units:\n  system: false\n", false, true},
		{"units:\n  user: false\n", true, false},
		{"units:\n  system: false\n  user: false\n", false, false},
		{"units:\n  system: true\n  user: true\n", true, true},
	}
	for _, tc := range cases {
		t.Run(strings.ReplaceAll(strings.TrimSpace(tc.body), "\n", " "), func(t *testing.T) {
			m, warns := monitorsFrom(t, tc.body)
			if len(warns) != 0 {
				t.Fatalf("GetMonitors warned %q", warns)
			}
			if m.Units.System != tc.system || m.Units.User != tc.user {
				t.Errorf("Units.System, Units.User = %v, %v, want %v, %v", m.Units.System, m.Units.User, tc.system, tc.user)
			}
			if !m.Units.Enabled {
				t.Error("a manager switch disabled the whole units monitor; only units.enabled may do that")
			}
			if !m.Smart.Enabled || !m.Emerge.Enabled || !m.Readonly.Enabled {
				t.Errorf("a units manager switch disabled another monitor: smart %v, emerge %v, readonly %v",
					m.Smart.Enabled, m.Emerge.Enabled, m.Readonly.Enabled)
			}
		})
	}
}

// TestS098MonitorConfig_EmergeLogIsUsed is R6.3: a configured absolute path
// replaces the default exactly, with no warning, and leaves the emerge
// monitor enabled. An absolute path that is not in canonical form is still
// absolute and must not be refused.
func TestS098MonitorConfig_EmergeLogIsUsed(t *testing.T) {
	m, warns := monitorsFrom(t, "emerge:\n  log: /srv/portage-logs/emerge.log\n")
	if len(warns) != 0 {
		t.Errorf("an absolute emerge.log warned %q", warns)
	}
	if m.Emerge.Log != "/srv/portage-logs/emerge.log" {
		t.Errorf("Emerge.Log = %q, want the configured /srv/portage-logs/emerge.log (R6.3)", m.Emerge.Log)
	}
	if !m.Emerge.Enabled {
		t.Error("configuring emerge.log disabled the emerge monitor")
	}

	m, warns = monitorsFrom(t, "emerge:\n  log: /var/log/../log/emerge.log\n")
	if len(warns) != 0 {
		t.Errorf("the absolute path /var/log/../log/emerge.log was refused: %q", warns)
	}
	if !strings.HasPrefix(m.Emerge.Log, "/") || !strings.HasSuffix(m.Emerge.Log, "emerge.log") {
		t.Errorf("Emerge.Log = %q, want the configured absolute path", m.Emerge.Log)
	}
}

// TestS098MonitorConfig_BadEmergeLogWarnsAndUsesDefault is §9.4 for
// emerge.log: a value that is not an absolute path loads, warns once naming
// the full key, the value and the default, and the default applies. The
// warning names emerge.log alone and no other key is disturbed.
func TestS098MonitorConfig_BadEmergeLogWarnsAndUsesDefault(t *testing.T) {
	cases := []struct {
		name, yaml, shown string // shown: the value text the warning must carry ("" = not checked)
	}{
		{"bare name", "emerge.log", "emerge.log"},
		{"dot relative", "./emerge.log", "./emerge.log"},
		{"relative dir", "var/log/emerge.log", "var/log/emerge.log"},
		{"empty string", `""`, ""},
		{"number", "42", "42"},
		{"list", "[/a.log, /b.log]", "/a.log"},
		{"mapping", "{path: /a.log}", "/a.log"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m, warns := monitorsFrom(t, "emerge:\n  log: "+tc.yaml+"\nunits:\n  user: false\n")
			if len(warns) != 1 {
				t.Fatalf("emerge.log %s gave %d warnings %q, want exactly one", tc.yaml, len(warns), warns)
			}
			w := warns[0]
			if !s097NamesKey(w, "tray.monitors.emerge.log") {
				t.Errorf("warning %q does not name tray.monitors.emerge.log", w)
			}
			if tc.shown != "" && !strings.Contains(w, tc.shown) {
				t.Errorf("warning %q does not show the configured value %s", w, tc.shown)
			}
			if !strings.Contains(w, s098DefaultEmergeLog) {
				t.Errorf("warning %q does not name the default %s it falls back to", w, s098DefaultEmergeLog)
			}
			if m.Emerge.Log != s098DefaultEmergeLog {
				t.Errorf("Emerge.Log = %q after a bad value, want the default %q", m.Emerge.Log, s098DefaultEmergeLog)
			}
			if !m.Emerge.Enabled {
				t.Error("a bad emerge.log disabled the emerge monitor")
			}
			if m.Units.User {
				t.Error("units.user: false beside a bad emerge.log did not apply")
			}
		})
	}
}

// TestS098MonitorConfig_BadSwitchWarnsAndUsesDefault is §9.4 for each on/off
// key: a value that is not true or false loads, warns once naming exactly
// that key, and the key takes its default true. The hostile half: a bad
// units.system must never be reported as, or change, units.user, and the
// converse.
func TestS098MonitorConfig_BadSwitchWarnsAndUsesDefault(t *testing.T) {
	for _, s := range s098Switches {
		t.Run(s.key, func(t *testing.T) {
			block, leaf, _ := strings.Cut(s.key, ".")
			m, warns := monitorsFrom(t, block+":\n  "+leaf+": maybe\n")
			if len(warns) != 1 {
				t.Fatalf("%s: maybe gave %d warnings %q, want exactly one", s.key, len(warns), warns)
			}
			if !s097NamesKey(warns[0], "tray.monitors."+s.key) || !strings.Contains(warns[0], "maybe") {
				t.Errorf("warning %q does not name tray.monitors.%s and its value maybe", warns[0], s.key)
			}
			for _, other := range s098Switches {
				if other.key != s.key && s097NamesKey(warns[0], "tray.monitors."+other.key) {
					t.Errorf("the warning for %s also names tray.monitors.%s: %q", s.key, other.key, warns[0])
				}
			}
			s098CheckDefaults(t, s.key+": maybe", m)
		})
	}
}

// TestS098MonitorConfig_NonMappingBlockWarnsOnce is §9.7 for the new blocks:
// a block that is not a mapping loads, warns once naming exactly that block,
// takes its defaults and leaves a valid sibling configured. `units: false`
// is the tempting reading of "disable": by §9.7 it is a block of the wrong
// shape, so the monitor keeps its default (enabled) and the user is warned.
func TestS098MonitorConfig_NonMappingBlockWarnsOnce(t *testing.T) {
	cases := []struct{ block, value string }{
		{"emerge", "5"},
		{"units", "false"},
		{"readonly", "[a, b]"},
		{"smart", `"off"`},
	}
	for _, tc := range cases {
		t.Run(tc.block, func(t *testing.T) {
			// A valid sibling in both directions: a 097 block and a new block.
			sibling, siblingYAML := "smart", "    smart:\n      enabled: false\n"
			if tc.block == "smart" {
				sibling, siblingYAML = "readonly", "    readonly:\n      enabled: false\n"
			}
			m, stderr, warns := s097KeysWarnings(t,
				"    "+tc.block+": "+tc.value+"\n"+siblingYAML+"    disk:\n      warn_percent: 7\n")
			named := s098WarningsNaming("tray.monitors."+tc.block, stderr, warns)
			if len(named) != 1 {
				t.Errorf("%s: %s is named by %d warnings %q, want exactly one", tc.block, tc.value, len(named), named)
			}
			if total := len(stderr) + len(warns); total != 1 {
				t.Errorf("got %d warnings, want exactly one; stderr %q, GetMonitors %q", total, stderr, warns)
			}
			if !s098Enabled(m)[tc.block] {
				t.Errorf("the malformed %s block disabled its monitor; it must take its defaults", tc.block)
			}
			if s098Enabled(m)[sibling] {
				t.Errorf("the valid sibling %s.enabled: false did not apply beside a malformed %s block", sibling, tc.block)
			}
			if num(t, m.Disk.WarnPercent) != 7 {
				t.Errorf("disk.warn_percent = %v, want 7 beside a malformed %s block", m.Disk.WarnPercent, tc.block)
			}
			if tc.block == "emerge" && m.Emerge.Log != s098DefaultEmergeLog {
				t.Errorf("Emerge.Log = %q, want the default after a malformed emerge block", m.Emerge.Log)
			}
			if tc.block == "units" && (!m.Units.System || !m.Units.User) {
				t.Errorf("Units = %+v, want both managers on after a malformed units block", m.Units)
			}
		})
	}
}

// TestS098MonitorConfig_UnknownKeysByFullPath is §9.5 for the new blocks and
// its hostile half: a key that is valid in one new block is unknown in
// another, is reported by its full path there, and never applies to the block
// it belongs to. A plain unknown key is reported once.
func TestS098MonitorConfig_UnknownKeysByFullPath(t *testing.T) {
	cases := []struct {
		name, yaml, key string
		check           func(Monitors) (string, bool)
	}{
		{"plain unknown key", "    emerge:\n      bogus: 1\n", "tray.monitors.emerge.bogus",
			func(m Monitors) (string, bool) { return "emerge stays enabled", m.Emerge.Enabled }},
		{"emerge's log in readonly", "    readonly:\n      log: /srv/x.log\n", "tray.monitors.readonly.log",
			func(m Monitors) (string, bool) {
				return "Emerge.Log stays default", m.Emerge.Log == s098DefaultEmergeLog
			}},
		{"units' system in smart", "    smart:\n      system: false\n", "tray.monitors.smart.system",
			func(m Monitors) (string, bool) { return "Units.System stays true", m.Units.System && m.Smart.Enabled }},
		{"units' user in emerge", "    emerge:\n      user: false\n", "tray.monitors.emerge.user",
			func(m Monitors) (string, bool) { return "Units.User stays true", m.Units.User }},
		{"emerge's log in units", "    units:\n      log: /srv/x.log\n", "tray.monitors.units.log",
			func(m Monitors) (string, bool) {
				return "Emerge.Log stays default", m.Emerge.Log == s098DefaultEmergeLog
			}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m, stderr, warns := s097KeysWarnings(t, tc.yaml)
			if n := len(s098WarningsNaming(tc.key, stderr, warns)); n != 1 {
				t.Errorf("%s is reported on %d lines, want 1; stderr %q, GetMonitors %q", tc.key, n, stderr, warns)
			}
			if total := len(stderr) + len(warns); total != 1 {
				t.Errorf("got %d warnings, want exactly one; stderr %q, GetMonitors %q", total, stderr, warns)
			}
			if what, ok := tc.check(m); !ok {
				t.Errorf("a misplaced key leaked into another block: want %s; got %+v %+v", what, m.Emerge, m.Units)
			}
		})
	}
}

// s098FullSection sets every new key to a non-default value.
const s098FullSection = "" +
	"    emerge:\n      enabled: false\n      log: /srv/logs/emerge.log\n" +
	"    units:\n      enabled: false\n      system: false\n      user: false\n" +
	"    readonly:\n      enabled: false\n" +
	"    smart:\n      enabled: false\n"

// TestS098MonitorConfig_FullValidSectionWarnsNothing is the converse of the
// unknown-key rule: every new key is a known key, so a section that sets all
// of them writes nothing to stderr, warns nothing, and every value applies.
func TestS098MonitorConfig_FullValidSectionWarnsNothing(t *testing.T) {
	m, stderr, warns := s097KeysWarnings(t, s098FullSection)
	if len(stderr) != 0 || len(warns) != 0 {
		t.Errorf("a valid section warned; stderr %q, GetMonitors %q", stderr, warns)
	}
	for _, s := range s098Switches {
		if s.get(m) {
			t.Errorf("tray.monitors.%s: false did not apply", s.key)
		}
	}
	if m.Emerge.Log != "/srv/logs/emerge.log" {
		t.Errorf("Emerge.Log = %q, want /srv/logs/emerge.log", m.Emerge.Log)
	}
}

// TestS098MonitorConfig_AliasValueApplies is §9.10 for the new blocks: a value
// written as an alias is read as its target in whichever block uses it.
func TestS098MonitorConfig_AliasValueApplies(t *testing.T) {
	m, stderr, warns := s097KeysWarnings(t,
		"    readonly:\n      enabled: &off false\n"+
			"    smart:\n      enabled: *off\n"+
			"    units:\n      user: *off\n")
	if len(stderr) != 0 || len(warns) != 0 {
		t.Errorf("alias values warned; stderr %q, GetMonitors %q", stderr, warns)
	}
	if m.Readonly.Enabled || m.Smart.Enabled || m.Units.User {
		t.Errorf("readonly %v, smart %v, units.user %v; want all false through the alias",
			m.Readonly.Enabled, m.Smart.Enabled, m.Units.User)
	}
	if !m.Units.System || !m.Emerge.Enabled {
		t.Errorf("units.system %v, emerge %v; an alias leaked into keys that never used it", m.Units.System, m.Emerge.Enabled)
	}
}

// TestS098MonitorConfig_SaveRoundTrips is §9.9 for the new keys: a config
// whose new blocks carry non-default values saves and loads back to the same
// resolved monitors.
func TestS098MonitorConfig_SaveRoundTrips(t *testing.T) {
	cfg := s097SaveAliasLoad(t, s097SaveAliasBase+"tray:\n  monitors:\n"+s098FullSection)
	before := s097SaveAliasMonitors(t, "before save", cfg)
	if before.Emerge.Log != "/srv/logs/emerge.log" || before.Units.User {
		t.Fatalf("fixture did not resolve to its non-default values: %+v %+v", before.Emerge, before.Units)
	}
	reloaded, saved := s097SaveAliasRoundTrip(t, cfg)
	after := s097SaveAliasMonitors(t, "after reload", reloaded)
	if !reflect.DeepEqual(before, after) {
		t.Errorf("tray.monitors changed across SaveTo + LoadFrom\nbefore: %+v\nafter:  %+v\nsaved file:\n%s", before, after, saved)
	}
}
