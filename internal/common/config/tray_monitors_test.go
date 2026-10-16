package config

import (
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	yaml "go.yaml.in/yaml/v3"
)

const gib = 1 << 30

// num turns any numeric field into a float64, so these tests pin the values
// the design names without pinning the Go type an implementation picks for a
// percentage, a size or a duration.
func num(t *testing.T, v any) float64 {
	t.Helper()
	rv := reflect.ValueOf(v)
	switch {
	case rv.CanInt():
		return float64(rv.Int())
	case rv.CanUint():
		return float64(rv.Uint())
	case rv.CanFloat():
		return rv.Float()
	}
	t.Fatalf("value %v of type %T is not numeric", v, v)
	return 0
}

// monitorsFrom decodes body as the content of tray.monitors and resolves it.
func monitorsFrom(t *testing.T, body string) (Monitors, []string) {
	t.Helper()
	doc := "tray:\n  monitors:\n"
	for _, line := range strings.Split(strings.TrimRight(body, "\n"), "\n") {
		doc += "    " + line + "\n"
	}
	var cfg Config
	if err := yaml.Unmarshal([]byte(doc), &cfg); err != nil {
		t.Fatalf("decoding tray.monitors failed (an unparsable value must not fail the whole config, R9.4): %v\n%s", err, doc)
	}
	return cfg.Tray.GetMonitors()
}

// field is one resolved numeric value and the design default it must take.
type field struct {
	key string
	get func(Monitors) any
	def float64
}

// numericFields lists every numeric tray.monitors key with its default.
var numericFields = []field{
	{"sample_interval", func(m Monitors) any { return m.SampleInterval }, float64(30 * time.Second)},
	{"reminder", func(m Monitors) any { return m.Reminder }, float64(time.Hour)},
	{"disk.warn_percent", func(m Monitors) any { return m.Disk.WarnPercent }, 10},
	{"disk.warn_free", func(m Monitors) any { return m.Disk.WarnFree }, 20 * gib},
	{"disk.crit_percent", func(m Monitors) any { return m.Disk.CritPercent }, 2},
	{"disk.crit_free", func(m Monitors) any { return m.Disk.CritFree }, 2 * gib},
	{"disk.sustain", func(m Monitors) any { return m.Disk.Sustain }, float64(time.Minute)},
	{"memory.warn_percent", func(m Monitors) any { return m.Memory.WarnPercent }, 10},
	{"memory.crit_percent", func(m Monitors) any { return m.Memory.CritPercent }, 5},
	{"memory.crit_swap_percent", func(m Monitors) any { return m.Memory.CritSwapPercent }, 10},
	{"memory.psi_full_crit", func(m Monitors) any { return m.Memory.PSIFullCrit }, 10},
	{"memory.sustain", func(m Monitors) any { return m.Memory.Sustain }, float64(2 * time.Minute)},
	{"cpu.warn_psi", func(m Monitors) any { return m.CPU.WarnPSI }, 80},
	{"cpu.clear_psi", func(m Monitors) any { return m.CPU.ClearPSI }, 70},
	{"cpu.sustain", func(m Monitors) any { return m.CPU.Sustain }, float64(5 * time.Minute)},
	{"network.down_after", func(m Monitors) any { return m.Network.DownAfter }, float64(30 * time.Second)},
	{"network.down_after_startup", func(m Monitors) any { return m.Network.DownAfterStartup }, float64(time.Minute)},
	{"network.back_after", func(m Monitors) any { return m.Network.BackAfter }, float64(10 * time.Second)},
}

func fieldByKey(t *testing.T, key string) field {
	t.Helper()
	for _, f := range numericFields {
		if f.key == key {
			return f
		}
	}
	t.Fatalf("no numeric field %s", key)
	return field{}
}

func enabledAll(m Monitors) map[string]bool {
	return map[string]bool{"disk": m.Disk.Enabled, "memory": m.Memory.Enabled, "cpu": m.CPU.Enabled, "network": m.Network.Enabled}
}

// TestGetMonitors_AbsentSectionYieldsDefaults is R9.1: no tray.monitors block
// enables every monitor with the R1-R6 defaults and warns nothing.
func TestGetMonitors_AbsentSectionYieldsDefaults(t *testing.T) {
	m, warns := TrayConfig{}.GetMonitors()
	if len(warns) != 0 {
		t.Errorf("GetMonitors() on an absent section warned %q", warns)
	}
	for name, on := range enabledAll(m) {
		if !on {
			t.Errorf("monitor %s disabled by default; R9.1 enables all four", name)
		}
	}
	if len(m.Disk.Paths) != 0 {
		t.Errorf("Disk.Paths = %q by default; want none configured (the portage defaults are resolved at startup)", m.Disk.Paths)
	}
	for _, f := range numericFields {
		if got := num(t, f.get(m)); got != f.def {
			t.Errorf("default %s = %v, want %v", f.key, got, f.def)
		}
	}
}

// TestGetMonitors_EmptySectionYieldsDefaults: an empty `monitors:` block is
// the same as an absent one.
func TestGetMonitors_EmptySectionYieldsDefaults(t *testing.T) {
	m, warns := monitorsFrom(t, "disk: {}\nmemory: {}\ncpu: {}\nnetwork: {}\n")
	if len(warns) != 0 {
		t.Errorf("empty monitor blocks warned %q", warns)
	}
	for name, on := range enabledAll(m) {
		if !on {
			t.Errorf("an empty %s block disabled the monitor", name)
		}
	}
	for _, f := range numericFields {
		if got := num(t, f.get(m)); got != f.def {
			t.Errorf("%s = %v with an empty block, want the default %v", f.key, got, f.def)
		}
	}
}

// TestGetMonitors_EveryKeyOverridable is R9.3 and R2.2: each configured value
// is used, and a configured path list is used exactly, in order.
func TestGetMonitors_EveryKeyOverridable(t *testing.T) {
	m, warns := monitorsFrom(t, `sample_interval: 45s
reminder: 2h
disk:
  enabled: true
  paths: [/var/tmp/portage, /mnt/build]
  warn_percent: 15
  warn_free: 40GiB
  crit_percent: 3
  crit_free: 512MiB
  sustain: 90s
memory:
  enabled: true
  warn_percent: 12.5
  crit_percent: 4
  crit_swap_percent: 20
  psi_full_crit: 15.5
  sustain: 3m
cpu:
  enabled: true
  warn_psi: 90
  clear_psi: 60
  sustain: 10m
network:
  enabled: true
  down_after: 45s
  down_after_startup: 2m
  back_after: 5s
`)
	if len(warns) != 0 {
		t.Fatalf("a fully valid block warned %q", warns)
	}
	want := map[string]float64{
		"sample_interval":            float64(45 * time.Second),
		"reminder":                   float64(2 * time.Hour),
		"disk.warn_percent":          15,
		"disk.warn_free":             40 * gib,
		"disk.crit_percent":          3,
		"disk.crit_free":             512 << 20,
		"disk.sustain":               float64(90 * time.Second),
		"memory.warn_percent":        12.5,
		"memory.crit_percent":        4,
		"memory.crit_swap_percent":   20,
		"memory.psi_full_crit":       15.5,
		"memory.sustain":             float64(3 * time.Minute),
		"cpu.warn_psi":               90,
		"cpu.clear_psi":              60,
		"cpu.sustain":                float64(10 * time.Minute),
		"network.down_after":         float64(45 * time.Second),
		"network.down_after_startup": float64(2 * time.Minute),
		"network.back_after":         float64(5 * time.Second),
	}
	for _, f := range numericFields {
		if got := num(t, f.get(m)); got != want[f.key] {
			t.Errorf("%s = %v, want the configured %v", f.key, got, want[f.key])
		}
	}
	if !slices.Equal(m.Disk.Paths, []string{"/var/tmp/portage", "/mnt/build"}) {
		t.Errorf("Disk.Paths = %q, want exactly [/var/tmp/portage /mnt/build]", m.Disk.Paths)
	}
}

// TestGetMonitors_SizeUnits: sizes take KiB, MiB, GiB and TiB, binary units.
func TestGetMonitors_SizeUnits(t *testing.T) {
	cases := map[string]float64{
		"1KiB":   1 << 10,
		"300MiB": 300 << 20,
		"5GiB":   5 * gib,
		"1TiB":   1 << 40,
	}
	for in, want := range cases {
		t.Run(in, func(t *testing.T) {
			m, warns := monitorsFrom(t, "disk:\n  warn_free: "+in+"\n")
			if len(warns) != 0 {
				t.Errorf("warn_free %s warned %q", in, warns)
			}
			if got := num(t, m.Disk.WarnFree); got != want {
				t.Errorf("warn_free %s = %v bytes, want %v", in, got, want)
			}
		})
	}
}

// TestGetMonitors_DisableIsPerMonitor is R9.2, in both converse directions:
// an explicit false disables exactly that monitor (never collapsing into the
// absent-key true), and disabling one never disables another.
func TestGetMonitors_DisableIsPerMonitor(t *testing.T) {
	for _, name := range []string{"disk", "memory", "cpu", "network"} {
		t.Run(name, func(t *testing.T) {
			m, warns := monitorsFrom(t, name+":\n  enabled: false\n")
			if len(warns) != 0 {
				t.Errorf("enabled: false warned %q", warns)
			}
			for other, on := range enabledAll(m) {
				if other == name && on {
					t.Errorf("%s.enabled: false resolved to enabled", name)
				}
				if other != name && !on {
					t.Errorf("%s.enabled: false also disabled %s", name, other)
				}
			}
		})
	}
}

// TestGetMonitors_ExplicitZeroIsNotAbsent: 0 is inside the 0-100 range, so a
// configured 0 is used; it must not collapse into the absent-key default.
func TestGetMonitors_ExplicitZeroIsNotAbsent(t *testing.T) {
	cases := []struct{ yaml, key string }{
		{"disk:\n  warn_percent: 0\n", "disk.warn_percent"},
		{"disk:\n  crit_percent: 0\n", "disk.crit_percent"},
		{"memory:\n  warn_percent: 0\n", "memory.warn_percent"},
		{"memory:\n  crit_swap_percent: 0\n", "memory.crit_swap_percent"},
	}
	for _, tc := range cases {
		t.Run(tc.key, func(t *testing.T) {
			m, warns := monitorsFrom(t, tc.yaml)
			if len(warns) != 0 {
				t.Errorf("%s: 0 warned %q; 0 is in range", tc.key, warns)
			}
			if got := num(t, fieldByKey(t, tc.key).get(m)); got != 0 {
				t.Errorf("%s: 0 resolved to %v; the explicit zero collapsed into the default", tc.key, got)
			}
		})
	}
}

// TestGetMonitors_RangeBoundariesAccepted: the edges of each legal range are
// legal (percent 0 and 100, sample_interval 10s, reminder 15m).
func TestGetMonitors_RangeBoundariesAccepted(t *testing.T) {
	cases := []struct {
		yaml, key string
		want      float64
	}{
		{"disk:\n  warn_percent: 100\n", "disk.warn_percent", 100},
		{"memory:\n  crit_swap_percent: 100\n", "memory.crit_swap_percent", 100},
		{"sample_interval: 10s\n", "sample_interval", float64(10 * time.Second)},
		{"reminder: 15m\n", "reminder", float64(15 * time.Minute)},
	}
	for _, tc := range cases {
		t.Run(tc.key, func(t *testing.T) {
			m, warns := monitorsFrom(t, tc.yaml)
			if len(warns) != 0 {
				t.Errorf("%s at its boundary warned %q", tc.key, warns)
			}
			if got := num(t, fieldByKey(t, tc.key).get(m)); got != tc.want {
				t.Errorf("%s = %v, want %v", tc.key, got, tc.want)
			}
		})
	}
}

// TestGetMonitors_BadValueWarnsAndUsesDefault is R9.4: an unparsable or
// out-of-range value draws exactly one warning naming the full key and the
// value, and that key falls back to its default.
func TestGetMonitors_BadValueWarnsAndUsesDefault(t *testing.T) {
	cases := []struct {
		key, yaml, value string
	}{
		{"disk.warn_percent", "disk:\n  warn_percent: 150\n", "150"},
		{"disk.warn_percent", "disk:\n  warn_percent: -1\n", "-1"},
		{"disk.crit_percent", "disk:\n  crit_percent: 100.5\n", "100.5"},
		{"memory.warn_percent", "memory:\n  warn_percent: 101\n", "101"},
		{"memory.crit_percent", "memory:\n  crit_percent: -0.5\n", "-0.5"},
		{"memory.crit_swap_percent", "memory:\n  crit_swap_percent: 200\n", "200"},
		{"memory.psi_full_crit", "memory:\n  psi_full_crit: -5\n", "-5"},
		{"cpu.warn_psi", "cpu:\n  warn_psi: 120\n", "120"},
		{"sample_interval", "sample_interval: 9s\n", "9s"},
		{"sample_interval", "sample_interval: fast\n", "fast"},
		{"sample_interval", "sample_interval: -30s\n", "-30s"},
		{"reminder", "reminder: 14m59s\n", "14m59s"},
		{"reminder", "reminder: hourly\n", "hourly"},
		{"disk.warn_free", "disk:\n  warn_free: -1GiB\n", "-1GiB"},
		{"disk.warn_free", "disk:\n  warn_free: 20GB\n", "20GB"},
		{"disk.warn_free", "disk:\n  warn_free: \"20\"\n", "20"},
		{"disk.warn_free", "disk:\n  warn_free: 1.5GiB\n", "1.5GiB"},
		{"disk.crit_free", "disk:\n  crit_free: lots\n", "lots"},
		{"disk.sustain", "disk:\n  sustain: -1m\n", "-1m"},
		{"memory.sustain", "memory:\n  sustain: two minutes\n", "two minutes"},
		{"cpu.sustain", "cpu:\n  sustain: -5m\n", "-5m"},
		{"network.down_after", "network:\n  down_after: -30s\n", "-30s"},
		{"network.down_after_startup", "network:\n  down_after_startup: soon\n", "soon"},
		{"network.back_after", "network:\n  back_after: -1s\n", "-1s"},
	}
	for _, tc := range cases {
		t.Run(tc.key+"="+tc.value, func(t *testing.T) {
			m, warns := monitorsFrom(t, tc.yaml)
			if len(warns) != 1 {
				t.Fatalf("%s: %s warnings = %q, want exactly one", tc.key, tc.value, warns)
			}
			if full := "tray.monitors." + tc.key; !strings.Contains(warns[0], full) {
				t.Errorf("warning %q does not name the key %s", warns[0], full)
			}
			if !strings.Contains(warns[0], tc.value) {
				t.Errorf("warning %q does not name the value %q", warns[0], tc.value)
			}
			f := fieldByKey(t, tc.key)
			if got := num(t, f.get(m)); got != f.def {
				t.Errorf("%s = %v after a bad value, want the default %v", tc.key, got, f.def)
			}
		})
	}
}

// TestGetMonitors_SameLeafKeysNeverCross is the hostile half of "that key's
// default": warn_percent, crit_percent and sustain exist under several
// monitors. A bad value under one monitor must reset and name only that
// monitor's key, and a good value for the same leaf under another monitor
// must survive.
func TestGetMonitors_SameLeafKeysNeverCross(t *testing.T) {
	m, warns := monitorsFrom(t, `disk:
  warn_percent: 150
  sustain: 4m
memory:
  warn_percent: 12
  sustain: bogus
`)
	if len(warns) != 2 {
		t.Fatalf("warnings = %q, want exactly two (disk.warn_percent, memory.sustain)", warns)
	}
	joined := strings.Join(warns, "\n")
	for _, want := range []string{"tray.monitors.disk.warn_percent", "tray.monitors.memory.sustain"} {
		if !strings.Contains(joined, want) {
			t.Errorf("warnings %q do not name %s", warns, want)
		}
	}
	for _, wrong := range []string{"tray.monitors.memory.warn_percent", "tray.monitors.disk.sustain"} {
		if strings.Contains(joined, wrong) {
			t.Errorf("warnings %q blame %s, whose value is valid", warns, wrong)
		}
	}
	if got := num(t, m.Disk.WarnPercent); got != 10 {
		t.Errorf("disk.warn_percent = %v, want the default 10", got)
	}
	if got := num(t, m.Memory.WarnPercent); got != 12 {
		t.Errorf("memory.warn_percent = %v, want the configured 12 (reset by the disk key's error)", got)
	}
	if got := num(t, m.Disk.Sustain); got != float64(4*time.Minute) {
		t.Errorf("disk.sustain = %v, want the configured 4m (reset by the memory key's error)", time.Duration(got))
	}
	if got := num(t, m.Memory.Sustain); got != float64(2*time.Minute) {
		t.Errorf("memory.sustain = %v, want the default 2m", time.Duration(got))
	}
}

// TestGetMonitors_ClearPSIBelowWarnPSI: CPU hysteresis needs clear_psi below
// warn_psi. A configured pair that breaks it warns, and in every case,
// including a configured warn_psi that falls below the default clear_psi,
// the resolved pair keeps clear below warn.
func TestGetMonitors_ClearPSIBelowWarnPSI(t *testing.T) {
	cases := []struct {
		name, yaml string
		wantWarn   bool
	}{
		{"clear above warn", "cpu:\n  warn_psi: 60\n  clear_psi: 70\n", true},
		{"clear equal to warn", "cpu:\n  warn_psi: 70\n  clear_psi: 70\n", true},
		{"only warn below default clear", "cpu:\n  warn_psi: 60\n", false},
		{"only clear above default warn", "cpu:\n  clear_psi: 85\n", true},
		{"legal pair", "cpu:\n  warn_psi: 95\n  clear_psi: 50\n", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m, warns := monitorsFrom(t, tc.yaml)
			if w, c := num(t, m.CPU.WarnPSI), num(t, m.CPU.ClearPSI); c >= w {
				t.Errorf("resolved clear_psi %v >= warn_psi %v; a CPU condition could never clear", c, w)
			}
			if tc.wantWarn {
				if len(warns) == 0 || !strings.Contains(strings.Join(warns, "\n"), "clear_psi") {
					t.Errorf("warnings = %q, want one naming clear_psi", warns)
				}
			}
			if tc.name == "legal pair" && len(warns) != 0 {
				t.Errorf("a legal pair warned %q", warns)
			}
		})
	}
}

// TestGetMonitors_UnparsableScalarsLoadThroughLoadFrom is R9.4 end to end: a
// value of the wrong YAML type ("10%", "maybe") must not fail the config
// load; it warns by key and value and the key takes its default, while the
// rest of the file still loads.
func TestGetMonitors_UnparsableScalarsLoadThroughLoadFrom(t *testing.T) {
	cfg, _ := trayLoad(t, trayBase+
		"tray:\n"+
		"  interval: 12h\n"+
		"  monitors:\n"+
		"    disk:\n"+
		"      warn_percent: 10%\n"+
		"    memory:\n"+
		"      crit_percent: five\n"+
		"    cpu:\n"+
		"      enabled: maybe\n"+
		"      sustain: 7m\n")
	if d, _ := cfg.Tray.GetInterval(); d != 12*time.Hour {
		t.Errorf("tray.interval = %v; the rest of the tray section did not load", d)
	}
	m, warns := cfg.Tray.GetMonitors()
	joined := strings.Join(warns, "\n")
	for _, kv := range [][2]string{
		{"tray.monitors.disk.warn_percent", "10%"},
		{"tray.monitors.memory.crit_percent", "five"},
		{"tray.monitors.cpu.enabled", "maybe"},
	} {
		if !strings.Contains(joined, kv[0]) || !strings.Contains(joined, kv[1]) {
			t.Errorf("warnings %q do not name %s and its value %q", warns, kv[0], kv[1])
		}
	}
	if len(warns) != 3 {
		t.Errorf("warnings = %q, want exactly three", warns)
	}
	if got := num(t, m.Disk.WarnPercent); got != 10 {
		t.Errorf("disk.warn_percent = %v, want the default 10", got)
	}
	if got := num(t, m.Memory.CritPercent); got != 5 {
		t.Errorf("memory.crit_percent = %v, want the default 5", got)
	}
	if !m.CPU.Enabled {
		t.Error("cpu.enabled: maybe disabled the CPU monitor; the default is enabled")
	}
	if got := num(t, m.CPU.Sustain); got != float64(7*time.Minute) {
		t.Errorf("cpu.sustain = %v, want the configured 7m next to the bad enabled", time.Duration(got))
	}
}
