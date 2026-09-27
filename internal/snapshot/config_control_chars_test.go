package snapshot

import (
	"errors"
	"strings"
	"testing"
)

func s053CfgWithValues() *Config {
	return &Config{
		Engine: EngineConfig{
			Driver: "btrbk", Subvolumes: []string{"/home", "/var"}, SnapshotDir: "/mnt/pool/_btrbk_snap",
			Retention: Retention{Daily: 7, PreserveMin: "2d"},
		},
		Ship:     []ShipConfig{{Type: "ssh", Target: "u@h:/b"}},
		Schedule: ScheduleConfig{Backend: "systemd", OnCalendar: "daily", RandomizedDelay: "5m"},
	}
}

// TestValidate_RejectsControlCharacters pins R7.1 and its converse: only bytes
// < 0x20 and 0x7F are refused, so UTF-8 (including U+0085, whose encoding is
// 0xC2 0x85) and printable ASCII still pass.
func TestValidate_RejectsControlCharacters(t *testing.T) {
	stubLookPath(t, "btrbk", "ssh", "systemctl")
	if err := s053CfgWithValues().Validate(); err != nil {
		t.Fatalf("baseline Validate = %v, want nil", err)
	}
	keys := []struct {
		key []string
		set func(*Config, string)
	}{
		{[]string{"subvolumes", "[1]"}, func(c *Config, v string) { c.Engine.Subvolumes[1] = v }},
		{[]string{"snapshot_dir"}, func(c *Config, v string) { c.Engine.SnapshotDir = v }},
		{[]string{"preserve_min"}, func(c *Config, v string) { c.Engine.Retention.PreserveMin = v }},
		{[]string{"target", "[0]"}, func(c *Config, v string) { c.Ship[0].Target = v }},
		{[]string{"on_calendar"}, func(c *Config, v string) { c.Schedule.OnCalendar = v }},
		{[]string{"randomized_delay"}, func(c *Config, v string) { c.Schedule.RandomizedDelay = v }},
	}
	bad := map[string]string{
		"newline": "x\nsnapshot_dir /tmp", "tab": "x\ty", "cr": "x\r", "nul": "x\x00",
		"esc": "\x1b[31m", "unit separator": "x\x1f", "del": "x\x7f",
	}
	good := []string{"has space", "tilde~", "/data/ção", "x\u0085y", "x\u00a0y"}
	for _, k := range keys {
		for name, v := range bad {
			cfg := s053CfgWithValues()
			k.set(cfg, v)
			err := cfg.Validate()
			if !errors.Is(err, ErrInvalidConfigValue) {
				t.Errorf("%s with %s: Validate = %v, want ErrInvalidConfigValue", k.key[0], name, err)
				continue
			}
			for _, part := range k.key {
				if !strings.Contains(err.Error(), part) {
					t.Errorf("%s with %s: error %q must name %q", k.key[0], name, err, part)
				}
			}
		}
		for _, v := range good {
			cfg := s053CfgWithValues()
			k.set(cfg, v)
			if err := cfg.Validate(); errors.Is(err, ErrInvalidConfigValue) {
				t.Errorf("%s = %q: Validate = %v, want no control-character refusal", k.key[0], v, err)
			}
		}
	}
}
