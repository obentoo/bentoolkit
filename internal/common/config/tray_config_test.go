package config

import (
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

// trayLoad writes content as a config file, loads it through LoadFrom and
// returns the config together with everything LoadFrom wrote to stderr (the
// unknown-key warning goes there).
func trayLoad(t *testing.T, content string) (*Config, string) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	var cfg *Config
	var loadErr error
	stderr := trayCaptureStderr(t, func() { cfg, loadErr = LoadFrom(path) })
	if loadErr != nil {
		t.Fatalf("LoadFrom: %v", loadErr)
	}
	return cfg, stderr
}

// trayCaptureStderr redirects os.Stderr while fn runs and returns what was
// written.
func trayCaptureStderr(t *testing.T, fn func()) string {
	t.Helper()
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
	defer func() { os.Stderr = orig }()
	fn()
	_ = w.Close()
	return <-done
}

const trayBase = "overlay:\n  path: /var/db/repos/bentoo\n"

// TestTrayConfig_SectionLoadsWithoutUnknownKeyWarning is R11.1: every tray key
// loads, and the strict re-decode does not call any of them unknown.
func TestTrayConfig_SectionLoadsWithoutUnknownKeyWarning(t *testing.T) {
	cfg, stderr := trayLoad(t, trayBase+
		"tray:\n"+
		"  interval: 12h\n"+
		"  feed_url: https://mirror.example.org/notices.json\n"+
		"  mute: [release]\n"+
		"  downgrade_critical: true\n"+
		"  skip_metered: false\n")

	if strings.TrimSpace(stderr) != "" {
		t.Fatalf("loading a valid tray section wrote to stderr (a false unknown-key warning?): %q", stderr)
	}
	tc := cfg.Tray
	if d, warns := tc.GetInterval(); d != 12*time.Hour || len(warns) != 0 {
		t.Errorf("GetInterval = %v, %q; want 12h and no warning", d, warns)
	}
	if got := tc.GetFeedURL(); got != "https://mirror.example.org/notices.json" {
		t.Errorf("GetFeedURL = %q, want the configured URL", got)
	}
	if types, warns := tc.GetMute(); !slices.Equal(types, []string{"release"}) || len(warns) != 0 {
		t.Errorf("GetMute = %q, %q; want [release] and no warning", types, warns)
	}
	if !tc.DowngradeCritical {
		t.Error("downgrade_critical: true did not load")
	}
	// The hostile half of the skip_metered default: an explicit false must stay
	// distinguishable from an absent key, whose default is true.
	if tc.GetSkipMetered() {
		t.Error("skip_metered: false loaded as true; an explicit false collapsed into the absent-key default")
	}
}

// TestTrayConfig_AbsentSectionYieldsDefaults is R11.2.
func TestTrayConfig_AbsentSectionYieldsDefaults(t *testing.T) {
	cfg, stderr := trayLoad(t, trayBase)
	if strings.TrimSpace(stderr) != "" {
		t.Fatalf("a config without tray: wrote to stderr: %q", stderr)
	}
	tc := cfg.Tray
	if d, warns := tc.GetInterval(); d != 6*time.Hour || len(warns) != 0 {
		t.Errorf("GetInterval = %v, %q; want the 6h default and no warning", d, warns)
	}
	if got := tc.GetFeedURL(); got != "https://obentoo.org/notices.json" {
		t.Errorf("GetFeedURL = %q, want https://obentoo.org/notices.json", got)
	}
	if types, warns := tc.GetMute(); len(types) != 0 || len(warns) != 0 {
		t.Errorf("GetMute = %q, %q; want nothing muted and no warning", types, warns)
	}
	if tc.DowngradeCritical {
		t.Error("downgrade_critical defaulted to true; want false")
	}
	if !tc.GetSkipMetered() {
		t.Error("skip_metered defaulted to false; want true")
	}
}

// TestTrayConfig_PartialSectionKeepsOtherDefaults is R11.2 per key: setting one
// key must not reset the others.
func TestTrayConfig_PartialSectionKeepsOtherDefaults(t *testing.T) {
	cfg, _ := trayLoad(t, trayBase+"tray:\n  interval: 2h\n")
	tc := cfg.Tray
	if d, _ := tc.GetInterval(); d != 2*time.Hour {
		t.Errorf("GetInterval = %v, want 2h", d)
	}
	if got := tc.GetFeedURL(); got != "https://obentoo.org/notices.json" {
		t.Errorf("GetFeedURL = %q, want the default", got)
	}
	if !tc.GetSkipMetered() {
		t.Error("skip_metered lost its true default when another tray key was set")
	}
}

// TestTrayConfig_IntervalBelowOneHourIsClamped is R11.3, with both sides of the
// boundary: exactly one hour is legal and draws no warning.
func TestTrayConfig_IntervalBelowOneHourIsClamped(t *testing.T) {
	cases := []struct {
		value    string
		want     time.Duration
		wantWarn bool
	}{
		{"30m", time.Hour, true},
		{"59m59s", time.Hour, true},
		{"1s", time.Hour, true},
		{"1h", time.Hour, false},
		{"90m", 90 * time.Minute, false},
	}
	for _, tc := range cases {
		t.Run(tc.value, func(t *testing.T) {
			c := TrayConfig{Interval: tc.value}
			got, warns := c.GetInterval()
			if got != tc.want {
				t.Errorf("GetInterval(%q) = %v, want %v", tc.value, got, tc.want)
			}
			if tc.wantWarn {
				if len(warns) != 1 {
					t.Fatalf("GetInterval(%q) warnings = %q, want exactly one", tc.value, warns)
				}
				if !strings.Contains(warns[0], tc.value) {
					t.Errorf("warning %q does not name the configured value %q", warns[0], tc.value)
				}
			} else if len(warns) != 0 {
				t.Errorf("GetInterval(%q) warned %q; the value is legal", tc.value, warns)
			}
		})
	}
}

// TestTrayConfig_UnparsableIntervalWarnsAndStaysSafe: a typo must never yield
// an interval below the one-hour floor (zero would poll in a tight loop), and
// the operator must be told which value was wrong.
func TestTrayConfig_UnparsableIntervalWarnsAndStaysSafe(t *testing.T) {
	c := TrayConfig{Interval: "six hours"}
	got, warns := c.GetInterval()
	if got < time.Hour {
		t.Errorf("GetInterval(%q) = %v, below the one-hour floor", c.Interval, got)
	}
	if len(warns) == 0 || !strings.Contains(strings.Join(warns, "\n"), "six hours") {
		t.Errorf("GetInterval(%q) warnings = %q, want one naming the value", c.Interval, warns)
	}
}

// TestTrayConfig_MuteDropsUnknownTypes is R11.4: an unknown type is dropped
// with a warning naming it, and the known entries survive.
func TestTrayConfig_MuteDropsUnknownTypes(t *testing.T) {
	c := TrayConfig{Mute: []string{"release", "bogus", "announcement"}}
	types, warns := c.GetMute()
	if !slices.Equal(types, []string{"release", "announcement"}) {
		t.Errorf("GetMute types = %q, want [release announcement]", types)
	}
	if len(warns) != 1 || !strings.Contains(warns[0], "bogus") {
		t.Errorf("GetMute warnings = %q, want exactly one naming \"bogus\"", warns)
	}
}

// TestTrayConfig_MuteNeverSilencesSecurity is R6.13: security cannot be muted;
// the entry is ignored with a warning.
func TestTrayConfig_MuteNeverSilencesSecurity(t *testing.T) {
	c := TrayConfig{Mute: []string{"security", "news"}}
	types, warns := c.GetMute()
	if slices.Contains(types, "security") {
		t.Fatalf("GetMute kept security in %q; security notices can never be muted", types)
	}
	if !slices.Equal(types, []string{"news"}) {
		t.Errorf("GetMute types = %q, want [news]", types)
	}
	if len(warns) != 1 || !strings.Contains(warns[0], "security") {
		t.Errorf("GetMute warnings = %q, want exactly one naming security", warns)
	}
}

// TestTrayConfig_MuteAcceptsEveryMutableType is the converse of the two tests
// above: a filter that drops too much would pass them. Every mutable type of
// the feed contract (release, news, announcement) is kept without a warning.
func TestTrayConfig_MuteAcceptsEveryMutableType(t *testing.T) {
	c := TrayConfig{Mute: []string{"release", "news", "announcement"}}
	types, warns := c.GetMute()
	if !slices.Equal(types, []string{"release", "news", "announcement"}) {
		t.Errorf("GetMute types = %q, want all three mutable types kept", types)
	}
	if len(warns) != 0 {
		t.Errorf("GetMute warned %q for known types", warns)
	}
}

// TestTrayConfig_MuteSecurityInAnotherCaseIsNotKept: a differently-cased
// spelling must not slip security (or an unknown word) into the mute list.
func TestTrayConfig_MuteSecurityInAnotherCaseIsNotKept(t *testing.T) {
	c := TrayConfig{Mute: []string{"Security", "SECURITY"}}
	types, warns := c.GetMute()
	for _, typ := range types {
		if strings.EqualFold(typ, "security") {
			t.Fatalf("GetMute kept %q; security cannot be muted in any spelling", typ)
		}
	}
	if len(warns) != 2 {
		t.Errorf("GetMute warnings = %q, want one per dropped entry", warns)
	}
}

// TestTrayConfig_GettersDoNotPrint: warnings are returned to the caller, which
// logs them; the config package itself writes nothing.
func TestTrayConfig_GettersDoNotPrint(t *testing.T) {
	c := TrayConfig{Interval: "5m", Mute: []string{"security", "bogus"}}
	out := trayCaptureStderr(t, func() {
		_, _ = c.GetInterval()
		_, _ = c.GetMute()
		_ = c.GetFeedURL()
		_ = c.GetSkipMetered()
	})
	if out != "" {
		t.Errorf("tray getters wrote to stderr: %q", out)
	}
}
