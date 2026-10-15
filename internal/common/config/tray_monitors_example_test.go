package config

import (
	"bytes"
	"os"
	"reflect"
	"strings"
	"testing"

	yaml "go.yaml.in/yaml/v3"
)

// monitorsKeyPaths is every tray.monitors key of the design's Data Models,
// relative to tray.monitors.
var monitorsKeyPaths = []string{
	"sample_interval", "reminder",
	"disk.enabled", "disk.paths", "disk.warn_percent", "disk.warn_free", "disk.crit_percent", "disk.crit_free", "disk.sustain",
	"memory.enabled", "memory.warn_percent", "memory.crit_percent", "memory.crit_swap_percent", "memory.psi_full_crit", "memory.sustain",
	"cpu.enabled", "cpu.warn_psi", "cpu.clear_psi", "cpu.sustain",
	"network.enabled", "network.down_after", "network.down_after_startup", "network.back_after",
}

// exampleTrayBlock returns the commented `# tray:` block of
// config.example.yaml, uncommented: the `# tray:` line and every following
// line that starts with "#", each stripped of its leading "# " (or "#").
func exampleTrayBlock(t *testing.T) string {
	t.Helper()
	data, err := os.ReadFile(exampleConfigPath(t))
	if err != nil {
		t.Fatalf("read config.example.yaml: %v", err)
	}
	var out []string
	in := false
	for _, line := range strings.Split(string(data), "\n") {
		if !in {
			if strings.TrimRight(line, " ") == "# tray:" {
				in = true
				out = append(out, "tray:")
			}
			continue
		}
		if !strings.HasPrefix(line, "#") {
			break
		}
		if strings.HasPrefix(line, "# ---") {
			break
		}
		line = strings.TrimPrefix(line, "#")
		line = strings.TrimPrefix(line, " ")
		out = append(out, line)
	}
	if !in {
		t.Fatal("config.example.yaml has no commented `# tray:` block")
	}
	return strings.Join(out, "\n") + "\n"
}

// yamlChild returns the value node under key in mapping node n.
func yamlChild(n *yaml.Node, key string) *yaml.Node {
	if n == nil || n.Kind != yaml.MappingNode {
		return nil
	}
	for i := 0; i+1 < len(n.Content); i += 2 {
		if n.Content[i].Value == key {
			return n.Content[i+1]
		}
	}
	return nil
}

// TestExampleConfig_DocumentsEveryMonitorsKey is R9.6. The generic
// TestExampleConfig_DocumentsEveryKey matches a key by its leaf name anywhere
// in the file, so `enabled:` or `sustain:` written once would count for all
// four monitors. This test walks the uncommented example tray block by full
// path, so each monitor's own key must be there.
func TestExampleConfig_DocumentsEveryMonitorsKey(t *testing.T) {
	block := exampleTrayBlock(t)
	var doc yaml.Node
	if err := yaml.Unmarshal([]byte(block), &doc); err != nil {
		t.Fatalf("the uncommented example tray block is not YAML: %v\n%s", err, block)
	}
	if len(doc.Content) == 0 {
		t.Fatalf("the uncommented example tray block is empty:\n%s", block)
	}
	monitors := yamlChild(yamlChild(doc.Content[0], "tray"), "monitors")
	if monitors == nil {
		t.Fatalf("the example tray block has no monitors: section:\n%s", block)
	}
	for _, path := range monitorsKeyPaths {
		n := monitors
		for _, seg := range strings.Split(path, ".") {
			n = yamlChild(n, seg)
		}
		if n == nil {
			t.Errorf("the example tray block does not document tray.monitors.%s", path)
		}
	}
}

// TestExampleConfig_MonitorsBlockIsStrictAndShowsDefaults is R9.6: the
// uncommented block decodes strictly (no key the code does not read), draws
// no warning, and every value it shows is that key's default. Disk paths are
// the exception: their default is resolved from portage at startup, so the
// example may show sample paths.
func TestExampleConfig_MonitorsBlockIsStrictAndShowsDefaults(t *testing.T) {
	block := exampleTrayBlock(t)
	var cfg Config
	dec := yaml.NewDecoder(bytes.NewReader([]byte(block)))
	dec.KnownFields(true)
	if err := dec.Decode(&cfg); err != nil {
		t.Fatalf("the uncommented example tray block does not decode strictly: %v\n%s", err, block)
	}
	// The strict decode stops at the raw monitors node; its keys are checked here.
	if unknown := cfg.Tray.unknownMonitorKeys(); len(unknown) != 0 {
		t.Errorf("the example monitors block carries unknown keys %q", unknown)
	}
	got, warns := cfg.Tray.GetMonitors()
	if len(warns) != 0 {
		t.Errorf("the example monitors block warned %q", warns)
	}
	want, _ := TrayConfig{}.GetMonitors()
	got.Disk.Paths, want.Disk.Paths = nil, nil
	if !reflect.DeepEqual(got, want) {
		t.Errorf("the example monitors block resolves to\n  %+v\nwant the defaults\n  %+v", got, want)
	}
}

// TestTrayMonitors_ValidBlockDrawsNoUnknownKeyWarning is the converse of
// R9.5: every documented key is a known key, so loading a full block writes
// nothing to stderr.
func TestTrayMonitors_ValidBlockDrawsNoUnknownKeyWarning(t *testing.T) {
	_, stderr := trayLoad(t, trayBase+`tray:
  monitors:
    sample_interval: 30s
    reminder: 1h
    disk:
      enabled: true
      paths: [/]
      warn_percent: 10
      warn_free: 20GiB
      crit_percent: 2
      crit_free: 2GiB
      sustain: 1m
    memory:
      enabled: true
      warn_percent: 10
      crit_percent: 5
      crit_swap_percent: 10
      psi_full_crit: 10
      sustain: 2m
    cpu:
      enabled: true
      warn_psi: 80
      clear_psi: 70
      sustain: 5m
    network:
      enabled: true
      down_after: 30s
      down_after_startup: 60s
      back_after: 10s
`)
	if strings.TrimSpace(stderr) != "" {
		t.Errorf("a valid tray.monitors block wrote to stderr: %q", stderr)
	}
}

// TestTrayMonitors_UnknownKeyWarnsAndLoads is R9.5: an unknown key anywhere
// under tray.monitors is named by the existing unknown-key warning and the
// config still loads with the known values applied. cpu.warn_free is the
// hostile case: warn_free is a real key, but only under disk, so it must
// still be unknown under cpu.
func TestTrayMonitors_UnknownKeyWarnsAndLoads(t *testing.T) {
	cases := []struct {
		name, yaml, unknown string
	}{
		{"under disk", "    disk:\n      bogus: 1\n      warn_percent: 15\n", "bogus"},
		{"under monitors", "    bogus_monitor: true\n    disk:\n      warn_percent: 15\n", "bogus_monitor"},
		{"key of another monitor", "    cpu:\n      warn_free: 20GiB\n    disk:\n      warn_percent: 15\n", "warn_free"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg, stderr := trayLoad(t, trayBase+"tray:\n  monitors:\n"+tc.yaml)
			if !strings.Contains(stderr, tc.unknown) {
				t.Errorf("stderr %q does not name the unknown key %q", stderr, tc.unknown)
			}
			if strings.Contains(stderr, "field monitors not found") {
				t.Errorf("stderr %q calls the whole monitors section unknown", stderr)
			}
			m, _ := cfg.Tray.GetMonitors()
			if got := num(t, m.Disk.WarnPercent); got != 15 {
				t.Errorf("disk.warn_percent = %v next to an unknown key, want the configured 15", got)
			}
		})
	}
}
