package config

import (
	"io"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"testing"
	"time"
)

// Story 097, sub-task 8.1: a tray.monitors section, or one of its disk,
// memory, cpu or network blocks, that is not a mapping must warn once naming
// the block and its value, take that block's defaults and leave the rest of
// the configuration loaded (R9.7). Unknown keys inside a valid block keep
// being reported with their full path (R9.5).

const s097ShapeBase = "overlay:\n  path: /var/db/repos/bentoo\n"

// s097ShapeLoad writes doc to a temporary config file, loads it through
// LoadFrom and returns the config plus everything LoadFrom wrote to stderr.
func s097ShapeLoad(t *testing.T, doc string) (*Config, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte(doc), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	orig := os.Stderr
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	os.Stderr = w
	done := make(chan string, 1)
	go func() {
		b, _ := io.ReadAll(r)
		done <- string(b)
	}()
	var cfg *Config
	var loadErr error
	func() {
		defer func() { os.Stderr = orig }()
		cfg, loadErr = LoadFrom(path)
	}()
	_ = w.Close()
	stderr := <-done
	if loadErr != nil {
		t.Fatalf("LoadFrom failed; a tray.monitors block of the wrong shape must warn and fall back to defaults, not fail the whole config (R9.7): %v\n%s", loadErr, doc)
	}
	if cfg.Overlay.Path != "/var/db/repos/bentoo" {
		t.Errorf("overlay.path = %q; the rest of the configuration did not load", cfg.Overlay.Path)
	}
	return cfg, stderr
}

// s097Defaults is the fully defaulted resolution of tray.monitors.
func s097Defaults(t *testing.T) Monitors {
	t.Helper()
	m, warns := TrayConfig{}.GetMonitors()
	if len(warns) != 0 {
		t.Fatalf("GetMonitors on an absent section warned %q", warns)
	}
	return m
}

// s097BlockIs reports whether got equals the default block want. Disk paths
// are compared by length so nil and an empty list both mean "none configured".
func s097BlockIs(got, want any) bool {
	if g, ok := got.(DiskMonitor); ok {
		w := want.(DiskMonitor)
		if len(g.Paths) != 0 || len(w.Paths) != 0 {
			return len(g.Paths) == len(w.Paths) && reflect.DeepEqual(g.Paths, w.Paths)
		}
		g.Paths, w.Paths = nil, nil
		return reflect.DeepEqual(g, w)
	}
	return reflect.DeepEqual(got, want)
}

// s097NamesKey reports whether text names key itself, not a longer key it is
// a prefix of (tray.monitors.disk must not match tray.monitors.disk.sustain
// nor tray.monitors.diskx).
func s097NamesKey(text, key string) bool {
	re := regexp.MustCompile(regexp.QuoteMeta(key) + `([^.\w]|$)`)
	return re.MatchString(text)
}

var s097BlockKeys = []string{"disk", "memory", "cpu", "network"}

func s097Block(m Monitors, name string) any {
	switch name {
	case "disk":
		return m.Disk
	case "memory":
		return m.Memory
	case "cpu":
		return m.CPU
	case "network":
		return m.Network
	}
	return nil
}

// TestS097MonitorBlock_NonMappingBlockWarnsOnceAndUsesDefaults is the R9.7
// hostile half: each block written as a scalar or a list loads, warns once
// naming exactly that block and its value, takes that block's defaults, and
// leaves a valid sibling block (in both directions) and the monitors-level and
// tray-level keys configured.
func TestS097MonitorBlock_NonMappingBlockWarnsOnceAndUsesDefaults(t *testing.T) {
	cases := []struct {
		name    string
		bad     string // block name
		value   string // configured value as it must appear in the warning
		yaml    string // content of tray.monitors besides sample_interval
		sibling string
		check   func(Monitors) (string, bool)
	}{
		{
			name: "disk scalar beside a valid memory", bad: "disk", value: "5",
			yaml:    "    disk: 5\n    memory:\n      warn_percent: 15\n",
			sibling: "memory",
			check: func(m Monitors) (string, bool) {
				return "memory.warn_percent want 15", num(t, m.Memory.WarnPercent) == 15
			},
		},
		{
			name: "memory list beside a valid disk", bad: "memory", value: "[a, b]",
			yaml:    "    disk:\n      warn_percent: 15\n    memory: [a, b]\n",
			sibling: "disk",
			check: func(m Monitors) (string, bool) {
				return "disk.warn_percent want 15", num(t, m.Disk.WarnPercent) == 15
			},
		},
		{
			name: "cpu string beside a valid network", bad: "cpu", value: "x",
			yaml:    "    cpu: \"x\"\n    network:\n      down_after: 45s\n",
			sibling: "network",
			check: func(m Monitors) (string, bool) {
				return "network.down_after want 45s", num(t, m.Network.DownAfter) == float64(45*time.Second)
			},
		},
		{
			name: "network bool beside a valid cpu", bad: "network", value: "true",
			yaml:    "    cpu:\n      warn_psi: 90\n    network: true\n",
			sibling: "cpu",
			check: func(m Monitors) (string, bool) {
				return "cpu.warn_psi want 90", num(t, m.CPU.WarnPSI) == 90
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			def := s097Defaults(t)
			cfg, stderr := s097ShapeLoad(t, s097ShapeBase+
				"tray:\n"+
				"  interval: 12h\n"+
				"  monitors:\n"+
				"    sample_interval: 45s\n"+
				tc.yaml)
			if strings.TrimSpace(stderr) != "" {
				t.Errorf("LoadFrom wrote to stderr %q; the malformed block must be reported once, by GetMonitors", stderr)
			}
			if d, _ := cfg.Tray.GetInterval(); d != 12*time.Hour {
				t.Errorf("tray.interval = %v, want the configured 12h beside a malformed monitor block", d)
			}
			m, warns := cfg.Tray.GetMonitors()

			key := "tray.monitors." + tc.bad
			if len(warns) != 1 {
				t.Fatalf("GetMonitors warnings = %q, want exactly one naming %s", warns, key)
			}
			w := warns[0]
			if !s097NamesKey(w, key) {
				t.Errorf("warning %q does not name the block %s", w, key)
			}
			if !strings.Contains(w, tc.value) {
				t.Errorf("warning %q does not carry the configured value %q", w, tc.value)
			}
			for _, other := range s097BlockKeys {
				if other != tc.bad && strings.Contains(w, "tray.monitors."+other) {
					t.Errorf("warning %q names tray.monitors.%s; only %s is malformed", w, other, key)
				}
			}

			if got, want := s097Block(m, tc.bad), s097Block(def, tc.bad); !s097BlockIs(got, want) {
				t.Errorf("%s = %+v, want its defaults %+v", tc.bad, got, want)
			}
			if what, ok := tc.check(m); !ok {
				t.Errorf("sibling block %s lost its configured value (%s): %+v", tc.sibling, what, s097Block(m, tc.sibling))
			}
			if got := m.SampleInterval; got != 45*time.Second {
				t.Errorf("sample_interval = %v, want the configured 45s beside a malformed block", got)
			}
		})
	}
}

// TestS097MonitorBlock_NonMappingSectionYieldsEveryDefault is R9.7 for the
// section itself: `monitors: 5` loads, every monitor value is the default and
// exactly one warning names tray.monitors (not one of its blocks) and 5.
func TestS097MonitorBlock_NonMappingSectionYieldsEveryDefault(t *testing.T) {
	def := s097Defaults(t)
	cfg, stderr := s097ShapeLoad(t, s097ShapeBase+
		"tray:\n"+
		"  interval: 12h\n"+
		"  monitors: 5\n")
	if strings.TrimSpace(stderr) != "" {
		t.Errorf("LoadFrom wrote to stderr %q; the malformed section must be reported once, by GetMonitors", stderr)
	}
	if d, _ := cfg.Tray.GetInterval(); d != 12*time.Hour {
		t.Errorf("tray.interval = %v, want the configured 12h beside a malformed tray.monitors", d)
	}
	m, warns := cfg.Tray.GetMonitors()
	if len(warns) != 1 {
		t.Fatalf("GetMonitors warnings = %q, want exactly one naming tray.monitors", warns)
	}
	if !s097NamesKey(warns[0], "tray.monitors") {
		t.Errorf("warning %q does not name tray.monitors itself", warns[0])
	}
	if !strings.Contains(warns[0], "5") {
		t.Errorf("warning %q does not carry the configured value 5", warns[0])
	}
	if m.SampleInterval != def.SampleInterval || m.Reminder != def.Reminder {
		t.Errorf("sample_interval/reminder = %v/%v, want the defaults %v/%v",
			m.SampleInterval, m.Reminder, def.SampleInterval, def.Reminder)
	}
	for _, b := range s097BlockKeys {
		if got, want := s097Block(m, b), s097Block(def, b); !s097BlockIs(got, want) {
			t.Errorf("%s = %+v, want its defaults %+v", b, got, want)
		}
	}
}

// TestS097MonitorBlock_NullBlockIsAbsent: `disk:` with no value (or an
// explicit null) is an absent block, not a malformed one; it warns nothing.
func TestS097MonitorBlock_NullBlockIsAbsent(t *testing.T) {
	for _, null := range []string{"    disk:\n", "    disk: null\n", "    disk: ~\n"} {
		t.Run(strings.TrimSpace(null), func(t *testing.T) {
			def := s097Defaults(t)
			cfg, stderr := s097ShapeLoad(t, s097ShapeBase+
				"tray:\n  monitors:\n"+null+
				"    memory:\n      warn_percent: 15\n")
			if strings.TrimSpace(stderr) != "" {
				t.Errorf("a null disk block wrote to stderr: %q", stderr)
			}
			m, warns := cfg.Tray.GetMonitors()
			if len(warns) != 0 {
				t.Errorf("a null disk block warned %q; null means absent", warns)
			}
			if !s097BlockIs(m.Disk, def.Disk) {
				t.Errorf("disk = %+v, want its defaults %+v", m.Disk, def.Disk)
			}
			if got := num(t, m.Memory.WarnPercent); got != 15 {
				t.Errorf("memory.warn_percent = %v, want the configured 15", got)
			}
		})
	}
}

// s097LinesNaming counts the lines of text that mention word.
func s097LinesNaming(text, word string) int {
	n := 0
	for _, line := range strings.Split(text, "\n") {
		if strings.Contains(line, word) {
			n++
		}
	}
	return n
}

// TestS097MonitorBlock_UnknownKeyInValidBlockWarnsByFullPath is R9.5 once the
// blocks are raw: an unknown key is still reported, exactly once, with the
// full path of the block it sits in, and the block's known keys still apply.
// The warning may come from LoadFrom (stderr) or from GetMonitors. warn_free
// under cpu is the hostile case: it is a real key, but only under disk, so it
// must be named tray.monitors.cpu.warn_free and never as a disk key.
func TestS097MonitorBlock_UnknownKeyInValidBlockWarnsByFullPath(t *testing.T) {
	cases := []struct {
		name, yaml, path, word, wrong string
	}{
		{"under disk", "    disk:\n      bogus: 1\n      warn_percent: 15\n",
			"tray.monitors.disk.bogus", "bogus", "tray.monitors.cpu.bogus"},
		{"key of another monitor under cpu", "    cpu:\n      warn_free: 20GiB\n    disk:\n      warn_percent: 15\n",
			"tray.monitors.cpu.warn_free", "warn_free", "tray.monitors.disk.warn_free"},
		{"under monitors", "    bogus_monitor: true\n    disk:\n      warn_percent: 15\n",
			"tray.monitors.bogus_monitor", "bogus_monitor", "tray.monitors.disk.bogus_monitor"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg, stderr := s097ShapeLoad(t, s097ShapeBase+"tray:\n  monitors:\n"+tc.yaml)
			m, warns := cfg.Tray.GetMonitors()
			all := stderr + "\n" + strings.Join(warns, "\n")
			if !s097NamesKey(all, tc.path) {
				t.Errorf("no warning names the unknown key by its full path %s; stderr %q, GetMonitors %q", tc.path, stderr, warns)
			}
			if strings.Contains(all, tc.wrong) {
				t.Errorf("the unknown key is reported under the wrong block (%s): %q", tc.wrong, all)
			}
			if n := s097LinesNaming(all, tc.word); n != 1 {
				t.Errorf("the unknown key %q is reported on %d lines, want exactly one: stderr %q, GetMonitors %q", tc.word, n, stderr, warns)
			}
			if got := num(t, m.Disk.WarnPercent); got != 15 {
				t.Errorf("disk.warn_percent = %v next to an unknown key, want the configured 15", got)
			}
		})
	}
}

// TestS097MonitorBlock_ValidFullSectionWarnsNothing is the benign half: every
// block a mapping with valid values loads them all and warns nowhere.
func TestS097MonitorBlock_ValidFullSectionWarnsNothing(t *testing.T) {
	cfg, stderr := s097ShapeLoad(t, s097ShapeBase+
		"tray:\n"+
		"  monitors:\n"+
		"    sample_interval: 45s\n"+
		"    reminder: 2h\n"+
		"    disk:\n"+
		"      enabled: true\n"+
		"      paths: [/, /var/tmp/portage]\n"+
		"      warn_percent: 15\n"+
		"      warn_free: 30GiB\n"+
		"      crit_percent: 3\n"+
		"      crit_free: 3GiB\n"+
		"      sustain: 2m\n"+
		"    memory:\n"+
		"      enabled: true\n"+
		"      warn_percent: 12\n"+
		"      crit_percent: 6\n"+
		"      crit_swap_percent: 11\n"+
		"      psi_full_crit: 12\n"+
		"      sustain: 3m\n"+
		"    cpu:\n"+
		"      enabled: false\n"+
		"      warn_psi: 90\n"+
		"      clear_psi: 75\n"+
		"      sustain: 6m\n"+
		"    network:\n"+
		"      enabled: true\n"+
		"      down_after: 40s\n"+
		"      down_after_startup: 90s\n"+
		"      back_after: 15s\n")
	if strings.TrimSpace(stderr) != "" {
		t.Errorf("a valid tray.monitors section wrote to stderr: %q", stderr)
	}
	m, warns := cfg.Tray.GetMonitors()
	if len(warns) != 0 {
		t.Errorf("a valid tray.monitors section warned %q", warns)
	}
	checks := []struct {
		key       string
		got, want float64
	}{
		{"sample_interval", num(t, m.SampleInterval), float64(45 * time.Second)},
		{"reminder", num(t, m.Reminder), float64(2 * time.Hour)},
		{"disk.warn_percent", num(t, m.Disk.WarnPercent), 15},
		{"disk.warn_free", num(t, m.Disk.WarnFree), 30 * gib},
		{"disk.crit_percent", num(t, m.Disk.CritPercent), 3},
		{"disk.crit_free", num(t, m.Disk.CritFree), 3 * gib},
		{"disk.sustain", num(t, m.Disk.Sustain), float64(2 * time.Minute)},
		{"memory.warn_percent", num(t, m.Memory.WarnPercent), 12},
		{"memory.crit_percent", num(t, m.Memory.CritPercent), 6},
		{"memory.crit_swap_percent", num(t, m.Memory.CritSwapPercent), 11},
		{"memory.psi_full_crit", num(t, m.Memory.PSIFullCrit), 12},
		{"memory.sustain", num(t, m.Memory.Sustain), float64(3 * time.Minute)},
		{"cpu.warn_psi", num(t, m.CPU.WarnPSI), 90},
		{"cpu.clear_psi", num(t, m.CPU.ClearPSI), 75},
		{"cpu.sustain", num(t, m.CPU.Sustain), float64(6 * time.Minute)},
		{"network.down_after", num(t, m.Network.DownAfter), float64(40 * time.Second)},
		{"network.down_after_startup", num(t, m.Network.DownAfterStartup), float64(90 * time.Second)},
		{"network.back_after", num(t, m.Network.BackAfter), float64(15 * time.Second)},
	}
	for _, c := range checks {
		if c.got != c.want {
			t.Errorf("%s = %v, want the configured %v", c.key, c.got, c.want)
		}
	}
	if !reflect.DeepEqual(m.Disk.Paths, []string{"/", "/var/tmp/portage"}) {
		t.Errorf("disk.paths = %q, want [/ /var/tmp/portage]", m.Disk.Paths)
	}
	if m.CPU.Enabled {
		t.Error("cpu.enabled: false left the CPU monitor enabled")
	}
	if !m.Disk.Enabled || !m.Memory.Enabled || !m.Network.Enabled {
		t.Errorf("enabled disk/memory/network = %v/%v/%v, want all true", m.Disk.Enabled, m.Memory.Enabled, m.Network.Enabled)
	}
}
