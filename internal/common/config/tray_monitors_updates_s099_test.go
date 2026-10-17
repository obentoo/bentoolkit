package config

import (
	"reflect"
	"strings"
	"testing"
	"time"
)

// Story 099, sub-task 1.1: tray.monitors gains three blocks, updates, glsa
// and sync. Absent, each enables its monitor with its defaults (R6.1);
// `enabled: false` turns off that monitor alone (R6.2); sync.max_age, a
// duration of at least one day, replaces the 7-day default (R6.3); and the
// new keys and blocks are validated, warned about and defaulted exactly like
// the story 097 ones (R6.4: §9.4 bad value, §9.5 unknown key, §9.7 block of
// the wrong shape, §9.9 save round-trip, §9.10 alias handling).

// s099DefaultMaxAge is the documented default of tray.monitors.sync.max_age.
const s099DefaultMaxAge = 7 * 24 * time.Hour

// s099Switches lists every on/off key of the three new blocks with a getter,
// relative to tray.monitors. Each defaults to true.
var s099Switches = []struct {
	key string
	get func(Monitors) bool
}{
	{"updates.enabled", func(m Monitors) bool { return m.Updates.Enabled }},
	{"glsa.enabled", func(m Monitors) bool { return m.GLSA.Enabled }},
	{"sync.enabled", func(m Monitors) bool { return m.Sync.Enabled }},
}

// s099Enabled maps every monitor this test knows, the story 097 ones and the
// three new ones, to its resolved on/off switch.
func s099Enabled(m Monitors) map[string]bool {
	return map[string]bool{
		"disk": m.Disk.Enabled, "memory": m.Memory.Enabled, "cpu": m.CPU.Enabled, "network": m.Network.Enabled,
		"updates": m.Updates.Enabled, "glsa": m.GLSA.Enabled, "sync": m.Sync.Enabled,
	}
}

// s099CheckDefaults reports every new key of m that is not at its default.
func s099CheckDefaults(t *testing.T, label string, m Monitors) {
	t.Helper()
	for _, s := range s099Switches {
		if !s.get(m) {
			t.Errorf("%s: tray.monitors.%s = false, want the default true (R6.1)", label, s.key)
		}
	}
	if m.Sync.MaxAge != s099DefaultMaxAge {
		t.Errorf("%s: tray.monitors.sync.max_age = %v, want the default %v (R6.1)", label, m.Sync.MaxAge, s099DefaultMaxAge)
	}
}

// s099WarningsNaming returns the warnings, across stderr and GetMonitors,
// that name key itself.
func s099WarningsNaming(key string, stderr, warns []string) []string {
	var out []string
	for _, w := range append(append([]string{}, stderr...), warns...) {
		if s097NamesKey(w, key) {
			out = append(out, w)
		}
	}
	return out
}

// TestS099MonitorConfig_AbsentBlocksYieldDefaults is R6.1: no section, a
// section that carries only story 097 blocks, empty new blocks and null new
// blocks all resolve every new key to its default without a warning.
func TestS099MonitorConfig_AbsentBlocksYieldDefaults(t *testing.T) {
	m, warns := TrayConfig{}.GetMonitors()
	if len(warns) != 0 {
		t.Errorf("an absent tray.monitors section warned %q", warns)
	}
	s099CheckDefaults(t, "absent section", m)

	cases := []struct{ name, body string }{
		{"only 097 blocks", "disk:\n  warn_percent: 7\nnetwork: {}\n"},
		{"empty new blocks", "updates: {}\nglsa: {}\nsync: {}\n"},
		{"null new blocks", "updates: ~\nglsa:\nsync: null\n"},
		{"null leaves", "updates:\n  enabled: ~\nsync:\n  enabled:\n  max_age: null\n"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m, warns := monitorsFrom(t, tc.body)
			if len(warns) != 0 {
				t.Errorf("GetMonitors warned %q, want nothing", warns)
			}
			s099CheckDefaults(t, tc.name, m)
		})
	}
}

// TestS099MonitorConfig_DisableIsPerMonitor is R6.2 and its hostile half: the
// leaf `enabled` is shared by every block, and switching one off must never
// switch off another, in either direction between the new blocks and the
// story 097 ones. updates and glsa share one update check, so they are the
// likeliest pair to collapse into one switch.
func TestS099MonitorConfig_DisableIsPerMonitor(t *testing.T) {
	for _, off := range []string{"updates", "glsa", "sync", "disk", "memory", "cpu", "network"} {
		t.Run(off, func(t *testing.T) {
			m, warns := monitorsFrom(t, off+":\n  enabled: false\n")
			if len(warns) != 0 {
				t.Fatalf("%s.enabled: false warned %q", off, warns)
			}
			for name, on := range s099Enabled(m) {
				if name == off && on {
					t.Errorf("tray.monitors.%s.enabled: false left the %s monitor enabled (R6.2)", off, name)
				}
				if name != off && !on {
					t.Errorf("tray.monitors.%s.enabled: false also disabled the %s monitor", off, name)
				}
			}
			if m.Sync.MaxAge != s099DefaultMaxAge {
				t.Errorf("Sync.MaxAge = %v after %s.enabled: false, want the default %v", m.Sync.MaxAge, off, s099DefaultMaxAge)
			}
		})
	}
}

// TestS099MonitorConfig_MaxAgeIsUsed is R6.3: a configured duration of at
// least one day replaces the default exactly, with no warning, and leaves the
// sync monitor enabled. One day itself is allowed, and the same day written
// in minutes is the same value.
func TestS099MonitorConfig_MaxAgeIsUsed(t *testing.T) {
	cases := []struct {
		yaml string
		want time.Duration
	}{
		{"24h", 24 * time.Hour},
		{"1440m", 24 * time.Hour},
		{"72h", 72 * time.Hour},
		{"720h", 30 * 24 * time.Hour},
		{"168h", s099DefaultMaxAge},
	}
	for _, tc := range cases {
		t.Run(tc.yaml, func(t *testing.T) {
			m, warns := monitorsFrom(t, "sync:\n  max_age: "+tc.yaml+"\n")
			if len(warns) != 0 {
				t.Errorf("sync.max_age: %s warned %q", tc.yaml, warns)
			}
			if m.Sync.MaxAge != tc.want {
				t.Errorf("Sync.MaxAge = %v, want the configured %v (R6.3)", m.Sync.MaxAge, tc.want)
			}
			if !m.Sync.Enabled {
				t.Error("configuring sync.max_age disabled the sync monitor")
			}
		})
	}
}

// TestS099MonitorConfig_BadMaxAgeWarnsAndUsesDefault is §9.4 for
// sync.max_age: a value that is not a duration of at least one day loads,
// warns once naming the full key, the value and the default, and the default
// applies. The hostile half: the bad value must not disturb its sibling
// sync.enabled, set to false beside it.
func TestS099MonitorConfig_BadMaxAgeWarnsAndUsesDefault(t *testing.T) {
	cases := []struct {
		name, yaml, shown string // shown: the value text the warning must carry
	}{
		{"just below one day", "23h59m59s", "23h59m59s"},
		{"one hour", "1h", "1h"},
		{"zero", "0", "0"},
		{"negative", "-48h", "-48h"},
		{"days are not a Go duration", "7d", "7d"},
		{"text", "weekly", "weekly"},
		{"bare number", "604800", "604800"},
		{"list", "[24h, 48h]", "24h"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m, warns := monitorsFrom(t, "sync:\n  enabled: false\n  max_age: "+tc.yaml+"\n")
			if len(warns) != 1 {
				t.Fatalf("sync.max_age %s gave %d warnings %q, want exactly one", tc.yaml, len(warns), warns)
			}
			w := warns[0]
			if !s097NamesKey(w, "tray.monitors.sync.max_age") {
				t.Errorf("warning %q does not name tray.monitors.sync.max_age", w)
			}
			if !strings.Contains(w, tc.shown) {
				t.Errorf("warning %q does not show the configured value %s", w, tc.shown)
			}
			if !strings.Contains(w, "168h") {
				t.Errorf("warning %q does not name the default 168h it falls back to", w)
			}
			if m.Sync.MaxAge != s099DefaultMaxAge {
				t.Errorf("Sync.MaxAge = %v after a bad value, want the default %v", m.Sync.MaxAge, s099DefaultMaxAge)
			}
			if m.Sync.Enabled {
				t.Error("sync.enabled: false beside a bad sync.max_age did not apply; one bad leaf must keep its siblings")
			}
		})
	}
}

// TestS099MonitorConfig_BadSwitchWarnsAndUsesDefault is §9.4 for each on/off
// key: a value that is not true or false loads, warns once naming exactly
// that key, and the key takes its default true. The converse of the
// max_age case: a bad sync.enabled keeps a valid sync.max_age beside it.
func TestS099MonitorConfig_BadSwitchWarnsAndUsesDefault(t *testing.T) {
	for _, s := range s099Switches {
		t.Run(s.key, func(t *testing.T) {
			block, leaf, _ := strings.Cut(s.key, ".")
			body := block + ":\n  " + leaf + ": maybe\n"
			if block == "sync" {
				body += "  max_age: 48h\n"
			}
			m, warns := monitorsFrom(t, body)
			if len(warns) != 1 {
				t.Fatalf("%s: maybe gave %d warnings %q, want exactly one", s.key, len(warns), warns)
			}
			if !s097NamesKey(warns[0], "tray.monitors."+s.key) || !strings.Contains(warns[0], "maybe") {
				t.Errorf("warning %q does not name tray.monitors.%s and its value maybe", warns[0], s.key)
			}
			for _, other := range s099Switches {
				if other.key != s.key && s097NamesKey(warns[0], "tray.monitors."+other.key) {
					t.Errorf("the warning for %s also names tray.monitors.%s: %q", s.key, other.key, warns[0])
				}
			}
			for _, other := range s099Switches {
				if !other.get(m) {
					t.Errorf("%s: maybe left tray.monitors.%s false, want the default true", s.key, other.key)
				}
			}
			if block == "sync" && m.Sync.MaxAge != 48*time.Hour {
				t.Errorf("Sync.MaxAge = %v beside a bad sync.enabled, want the configured 48h", m.Sync.MaxAge)
			}
		})
	}
}

// TestS099MonitorConfig_NonMappingBlockWarnsOnce is §9.7 for the new blocks:
// a block that is not a mapping loads, warns once naming exactly that block,
// takes its defaults and leaves a valid sibling configured. `updates: false`
// is the tempting reading of "disable": by §9.7 it is a block of the wrong
// shape, so the monitor keeps its default (enabled) and the user is warned.
func TestS099MonitorConfig_NonMappingBlockWarnsOnce(t *testing.T) {
	cases := []struct{ block, value string }{
		{"updates", "false"},
		{"glsa", "5"},
		{"sync", "[a, b]"},
		{"sync", `"168h"`},
	}
	for _, tc := range cases {
		t.Run(tc.block+"="+tc.value, func(t *testing.T) {
			// A valid sibling among the new blocks, plus a story 097 block.
			sibling, siblingYAML := "glsa", "    glsa:\n      enabled: false\n"
			if tc.block == "glsa" {
				sibling, siblingYAML = "updates", "    updates:\n      enabled: false\n"
			}
			m, stderr, warns := s097KeysWarnings(t,
				"    "+tc.block+": "+tc.value+"\n"+siblingYAML+"    disk:\n      warn_percent: 7\n")
			named := s099WarningsNaming("tray.monitors."+tc.block, stderr, warns)
			if len(named) != 1 {
				t.Errorf("%s: %s is named by %d warnings %q, want exactly one", tc.block, tc.value, len(named), named)
			}
			if total := len(stderr) + len(warns); total != 1 {
				t.Errorf("got %d warnings, want exactly one; stderr %q, GetMonitors %q", total, stderr, warns)
			}
			if !s099Enabled(m)[tc.block] {
				t.Errorf("the malformed %s block disabled its monitor; it must take its defaults", tc.block)
			}
			if s099Enabled(m)[sibling] {
				t.Errorf("the valid sibling %s.enabled: false did not apply beside a malformed %s block", sibling, tc.block)
			}
			if num(t, m.Disk.WarnPercent) != 7 {
				t.Errorf("disk.warn_percent = %v, want 7 beside a malformed %s block", m.Disk.WarnPercent, tc.block)
			}
			if m.Sync.MaxAge != s099DefaultMaxAge {
				t.Errorf("Sync.MaxAge = %v, want the default after a malformed %s block", m.Sync.MaxAge, tc.block)
			}
		})
	}
}

// TestS099MonitorConfig_UnknownKeysByFullPath is §9.5 for the new blocks and
// its hostile half: max_age is valid in sync only, so in updates or glsa it is
// an unknown key reported by its full path there, and it never applies to
// sync. A plain unknown key is reported once.
func TestS099MonitorConfig_UnknownKeysByFullPath(t *testing.T) {
	cases := []struct{ name, yaml, key string }{
		{"plain unknown key in sync", "    sync:\n      bogus: 1\n", "tray.monitors.sync.bogus"},
		{"sync's max_age in updates", "    updates:\n      max_age: 48h\n", "tray.monitors.updates.max_age"},
		{"sync's max_age in glsa", "    glsa:\n      max_age: 48h\n", "tray.monitors.glsa.max_age"},
		{"098's log key in sync", "    sync:\n      log: /srv/x.log\n", "tray.monitors.sync.log"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m, stderr, warns := s097KeysWarnings(t, tc.yaml)
			if n := len(s099WarningsNaming(tc.key, stderr, warns)); n != 1 {
				t.Errorf("%s is reported on %d lines, want 1; stderr %q, GetMonitors %q", tc.key, n, stderr, warns)
			}
			if total := len(stderr) + len(warns); total != 1 {
				t.Errorf("got %d warnings, want exactly one; stderr %q, GetMonitors %q", total, stderr, warns)
			}
			if m.Sync.MaxAge != s099DefaultMaxAge {
				t.Errorf("a misplaced max_age leaked into sync: Sync.MaxAge = %v, want the default", m.Sync.MaxAge)
			}
			for _, s := range s099Switches {
				if !s.get(m) {
					t.Errorf("an unknown key disabled tray.monitors.%s", s.key)
				}
			}
		})
	}
}

// s099FullSection sets every new key to a non-default value.
const s099FullSection = "" +
	"    updates:\n      enabled: false\n" +
	"    glsa:\n      enabled: false\n" +
	"    sync:\n      enabled: false\n      max_age: 72h\n"

// TestS099MonitorConfig_FullValidSectionWarnsNothing is the converse of the
// unknown-key rule: every new key is a known key, so a section that sets all
// of them writes nothing to stderr, warns nothing, and every value applies.
func TestS099MonitorConfig_FullValidSectionWarnsNothing(t *testing.T) {
	m, stderr, warns := s097KeysWarnings(t, s099FullSection)
	if len(stderr) != 0 || len(warns) != 0 {
		t.Errorf("a valid section warned; stderr %q, GetMonitors %q", stderr, warns)
	}
	for _, s := range s099Switches {
		if s.get(m) {
			t.Errorf("tray.monitors.%s: false did not apply", s.key)
		}
	}
	if m.Sync.MaxAge != 72*time.Hour {
		t.Errorf("Sync.MaxAge = %v, want 72h", m.Sync.MaxAge)
	}
	if !m.Disk.Enabled || !m.Memory.Enabled || !m.CPU.Enabled || !m.Network.Enabled {
		t.Errorf("disabling the new monitors disabled a story 097 one: %+v", s099Enabled(m))
	}
}

// TestS099MonitorConfig_AliasValueApplies is §9.10 for the new blocks: a value
// written as an alias is read as its target in whichever block uses it, and
// never in a block that does not.
func TestS099MonitorConfig_AliasValueApplies(t *testing.T) {
	m, stderr, warns := s097KeysWarnings(t,
		"    updates:\n      enabled: &off false\n"+
			"    glsa:\n      enabled: *off\n"+
			"    sync:\n      max_age: &age 96h\n")
	if len(stderr) != 0 || len(warns) != 0 {
		t.Errorf("alias values warned; stderr %q, GetMonitors %q", stderr, warns)
	}
	if m.Updates.Enabled || m.GLSA.Enabled {
		t.Errorf("updates %v, glsa %v; want both false through the alias", m.Updates.Enabled, m.GLSA.Enabled)
	}
	if !m.Sync.Enabled {
		t.Error("sync.enabled is false; an alias leaked into a key that never used it")
	}
	if m.Sync.MaxAge != 96*time.Hour {
		t.Errorf("Sync.MaxAge = %v, want the anchored 96h", m.Sync.MaxAge)
	}
}

// TestS099MonitorConfig_SaveRoundTrips is §9.9 for the new keys: a config
// whose new blocks carry non-default values saves and loads back to the same
// resolved monitors.
func TestS099MonitorConfig_SaveRoundTrips(t *testing.T) {
	cfg := s097SaveAliasLoad(t, s097SaveAliasBase+"tray:\n  monitors:\n"+s099FullSection)
	before := s097SaveAliasMonitors(t, "before save", cfg)
	if before.Sync.MaxAge != 72*time.Hour || before.Updates.Enabled || before.GLSA.Enabled || before.Sync.Enabled {
		t.Fatalf("fixture did not resolve to its non-default values: %+v %+v %+v", before.Updates, before.GLSA, before.Sync)
	}
	reloaded, saved := s097SaveAliasRoundTrip(t, cfg)
	after := s097SaveAliasMonitors(t, "after reload", reloaded)
	if !reflect.DeepEqual(before, after) {
		t.Errorf("tray.monitors changed across SaveTo + LoadFrom\nbefore: %+v\nafter:  %+v\nsaved file:\n%s", before, after, saved)
	}
}
