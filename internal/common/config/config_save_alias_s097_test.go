package config

import (
	"bytes"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	yaml "go.yaml.in/yaml/v3"
)

// Story 097, sub-task 12.1: SaveTo must write a file that loads back to the
// same tray.monitors values (R9.9). tray.monitors is kept as a raw yaml.Node,
// so an alias inside it can point at an anchor that lives in a typed field;
// the typed field drops the anchor on save, and a saved alias to it would
// make the next load fail. The hostile cases come first: an alias whose
// anchor would vanish (the file stops loading) and an anchor name reused so a
// resolved copy could capture a later alias (two distinct values collapse).

const s097SaveAliasBase = "overlay:\n  path: /var/db/repos/bentoo\n"

// s097SaveAliasLoad writes doc to a temporary file and loads it with LoadFrom.
func s097SaveAliasLoad(t *testing.T, doc string) *Config {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte(doc), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	cfg, err := LoadFrom(path)
	if err != nil {
		t.Fatalf("initial LoadFrom failed: %v\n%s", err, doc)
	}
	return cfg
}

// s097SaveAliasRoundTrip saves cfg with SaveTo into a temporary file, loads
// that file back and returns the reloaded config plus the saved bytes.
func s097SaveAliasRoundTrip(t *testing.T, cfg *Config) (*Config, []byte) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "saved.yaml")
	if err := cfg.SaveTo(path); err != nil {
		t.Fatalf("SaveTo: %v", err)
	}
	saved, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read saved config: %v", err)
	}
	reloaded, err := LoadFrom(path)
	if err != nil {
		t.Fatalf("the file SaveTo wrote does not load back (R9.9): %v\nsaved file:\n%s", err, saved)
	}
	return reloaded, saved
}

// s097SaveAliasMonitors resolves tray.monitors and fails on any warning: every
// fixture here is valid, so a warning means a value was lost or mangled.
func s097SaveAliasMonitors(t *testing.T, label string, cfg *Config) Monitors {
	t.Helper()
	m, warns := cfg.Tray.GetMonitors()
	if len(warns) != 0 {
		t.Fatalf("%s: GetMonitors warned %q", label, warns)
	}
	return m
}

// s097SaveAliasSame checks the reloaded monitors equal the ones loaded first.
func s097SaveAliasSame(t *testing.T, before, after Monitors, saved []byte) {
	t.Helper()
	if !reflect.DeepEqual(before, after) {
		t.Errorf("tray.monitors changed across SaveTo + LoadFrom (R9.9)\nbefore: %+v\nafter:  %+v\nsaved file:\n%s", before, after, saved)
	}
}

// s097SaveAliasFind returns the first alias node under n, or nil.
func s097SaveAliasFind(n *yaml.Node) *yaml.Node {
	if n == nil {
		return nil
	}
	if n.Kind == yaml.AliasNode {
		return n
	}
	for _, c := range n.Content {
		if a := s097SaveAliasFind(c); a != nil {
			return a
		}
	}
	return nil
}

// s097SaveAliasAnchors collects every anchor name set under n.
func s097SaveAliasAnchors(n *yaml.Node, into []string) []string {
	if n == nil || n.Kind == yaml.AliasNode {
		return into
	}
	if n.Anchor != "" {
		into = append(into, n.Anchor)
	}
	for _, c := range n.Content {
		into = s097SaveAliasAnchors(c, into)
	}
	return into
}

// R9.9 hostile half, "the file stops loading": the alias's anchor lives in
// tray.interval, a typed string field that drops the anchor on save.
func TestS097SaveAlias_AliasToScalarAnchoredInTypedFieldRoundTrips(t *testing.T) {
	cfg := s097SaveAliasLoad(t, s097SaveAliasBase+
		"tray:\n  interval: &i 2h\n  monitors:\n    reminder: *i\n    disk: {warn_percent: 7}\n")
	before := s097SaveAliasMonitors(t, "initial load", cfg)
	if before.Reminder != 2*time.Hour {
		t.Fatalf("fixture: initial reminder = %v, want 2h", before.Reminder)
	}

	reloaded, saved := s097SaveAliasRoundTrip(t, cfg)
	after := s097SaveAliasMonitors(t, "reload", reloaded)
	if after.Reminder != 2*time.Hour {
		t.Errorf("reloaded reminder = %v, want 2h (R9.9)\nsaved file:\n%s", after.Reminder, saved)
	}
	if reloaded.Tray.Interval != "2h" {
		t.Errorf("reloaded tray.interval = %q, want 2h", reloaded.Tray.Interval)
	}
	s097SaveAliasSame(t, before, after, saved)
}

// R9.9 hostile half: a whole monitor block is an alias to a mapping anchored
// in a typed field (notice:), and a second block merges the same mapping.
func TestS097SaveAlias_AliasToMappingAnchoredInTypedFieldReachesWholeBlock(t *testing.T) {
	cfg := s097SaveAliasLoad(t, s097SaveAliasBase+
		"notice: &blk {down_after: 2m, back_after: 20s}\n"+
		"tray:\n  monitors:\n    network: *blk\n    cpu:\n      <<: {sustain: 9m}\n      warn_psi: 60\n")
	before := s097SaveAliasMonitors(t, "initial load", cfg)
	if before.Network.DownAfter != 2*time.Minute || before.Network.BackAfter != 20*time.Second {
		t.Fatalf("fixture: initial network = %+v, want down_after 2m, back_after 20s", before.Network)
	}

	reloaded, saved := s097SaveAliasRoundTrip(t, cfg)
	after := s097SaveAliasMonitors(t, "reload", reloaded)
	if after.Network.DownAfter != 2*time.Minute || after.Network.BackAfter != 20*time.Second {
		t.Errorf("reloaded network = %+v, want down_after 2m, back_after 20s (R9.9)\nsaved file:\n%s", after.Network, saved)
	}
	s097SaveAliasSame(t, before, after, saved)
}

// R9.9 hostile half for the merge key: `<<: *blk` inside a block, where blk is
// anchored in a typed field. A resolved merge must keep the merged values and
// let the block's own key win.
func TestS097SaveAlias_MergeOfMappingAnchoredInTypedFieldRoundTrips(t *testing.T) {
	cfg := s097SaveAliasLoad(t, s097SaveAliasBase+
		"notice: &blk {warn_percent: 7, sustain: 3m}\n"+
		"tray:\n  monitors:\n    disk:\n      <<: *blk\n      sustain: 4m\n")
	before := s097SaveAliasMonitors(t, "initial load", cfg)
	if before.Disk.WarnPercent != 7 || before.Disk.Sustain != 4*time.Minute {
		t.Fatalf("fixture: initial disk = %+v, want warn_percent 7, sustain 4m", before.Disk)
	}

	reloaded, saved := s097SaveAliasRoundTrip(t, cfg)
	after := s097SaveAliasMonitors(t, "reload", reloaded)
	if after.Disk.WarnPercent != 7 || after.Disk.Sustain != 4*time.Minute {
		t.Errorf("reloaded disk = %+v, want warn_percent 7, sustain 4m (R9.9)\nsaved file:\n%s", after.Disk, saved)
	}
	s097SaveAliasSame(t, before, after, saved)
}

// R9.9 hostile half, "two values collapse": the anchor name d is defined in a
// typed field (inside the mapping blk) and defined again inside monitors. In
// the source, cpu.sustain's *d means the monitors one (45s) and network's
// down_after is 2m. A saved file that re-emits blk's inner anchor after the
// monitors one would let *d capture 2m.
func TestS097SaveAlias_ReusedAnchorNameKeepsDistinctValues(t *testing.T) {
	cfg := s097SaveAliasLoad(t, s097SaveAliasBase+
		"notice: &blk {down_after: &d 2m, back_after: 20s}\n"+
		"tray:\n  monitors:\n    sample_interval: &d 45s\n    network: *blk\n    cpu: {sustain: *d}\n")
	before := s097SaveAliasMonitors(t, "initial load", cfg)
	if before.SampleInterval != 45*time.Second || before.CPU.Sustain != 45*time.Second ||
		before.Network.DownAfter != 2*time.Minute {
		t.Fatalf("fixture: initial sample_interval=%v cpu.sustain=%v network.down_after=%v, want 45s 45s 2m",
			before.SampleInterval, before.CPU.Sustain, before.Network.DownAfter)
	}

	reloaded, saved := s097SaveAliasRoundTrip(t, cfg)
	after := s097SaveAliasMonitors(t, "reload", reloaded)
	if after.CPU.Sustain != 45*time.Second {
		t.Errorf("reloaded cpu.sustain = %v, want 45s: the alias captured another anchor of the same name (R9.9)\nsaved file:\n%s",
			after.CPU.Sustain, saved)
	}
	if after.Network.DownAfter != 2*time.Minute {
		t.Errorf("reloaded network.down_after = %v, want 2m (R9.9)\nsaved file:\n%s", after.Network.DownAfter, saved)
	}
	s097SaveAliasSame(t, before, after, saved)
}

// Benign: anchor and alias both inside monitors.
func TestS097SaveAlias_AnchorAndAliasInsideMonitorsRoundTrip(t *testing.T) {
	cfg := s097SaveAliasLoad(t, s097SaveAliasBase+
		"tray:\n  monitors:\n    sample_interval: &s 45s\n    disk: &dk {warn_percent: 7, sustain: 3m}\n"+
		"    network: {down_after: *s}\n    memory: {sustain: *s}\n")
	before := s097SaveAliasMonitors(t, "initial load", cfg)
	if before.Network.DownAfter != 45*time.Second || before.Memory.Sustain != 45*time.Second {
		t.Fatalf("fixture: initial network.down_after=%v memory.sustain=%v, want 45s 45s",
			before.Network.DownAfter, before.Memory.Sustain)
	}

	reloaded, saved := s097SaveAliasRoundTrip(t, cfg)
	after := s097SaveAliasMonitors(t, "reload", reloaded)
	s097SaveAliasSame(t, before, after, saved)
}

// SaveTo must not mutate the in-memory config: the alias stays an alias, the
// anchors inside monitors stay set, and a second save writes the same bytes.
func TestS097SaveAlias_InMemoryConfigNotMutated(t *testing.T) {
	cfg := s097SaveAliasLoad(t, s097SaveAliasBase+
		"tray:\n  interval: &i 2h\n  monitors:\n    reminder: *i\n    sample_interval: &s 45s\n    cpu: {sustain: *s}\n")
	alias := s097SaveAliasFind(&cfg.Tray.Monitors)
	if alias == nil {
		t.Fatal("fixture: no alias node in tray.monitors after load")
	}
	anchorsBefore := s097SaveAliasAnchors(&cfg.Tray.Monitors, nil)
	monBefore := s097SaveAliasMonitors(t, "initial load", cfg)

	dir := t.TempDir()
	first, second := filepath.Join(dir, "a.yaml"), filepath.Join(dir, "b.yaml")
	if err := cfg.SaveTo(first); err != nil {
		t.Fatalf("SaveTo: %v", err)
	}
	if err := cfg.SaveTo(second); err != nil {
		t.Fatalf("second SaveTo: %v", err)
	}

	if got := s097SaveAliasFind(&cfg.Tray.Monitors); got == nil {
		t.Error("after SaveTo the in-memory tray.monitors holds no alias: SaveTo mutated the config")
	} else if got != alias || got.Alias == nil || got.Alias.Value != "2h" {
		t.Errorf("after SaveTo the alias node changed: %+v", got)
	}
	if anchorsAfter := s097SaveAliasAnchors(&cfg.Tray.Monitors, nil); !reflect.DeepEqual(anchorsBefore, anchorsAfter) {
		t.Errorf("anchors in tray.monitors changed across SaveTo: before %q, after %q", anchorsBefore, anchorsAfter)
	}
	if monAfter := s097SaveAliasMonitors(t, "after save", cfg); !reflect.DeepEqual(monBefore, monAfter) {
		t.Errorf("in-memory monitors changed across SaveTo\nbefore: %+v\nafter:  %+v", monBefore, monAfter)
	}
	a, errA := os.ReadFile(first)
	b, errB := os.ReadFile(second)
	if errA != nil || errB != nil {
		t.Fatalf("read saved files: %v, %v", errA, errB)
	}
	if !bytes.Equal(a, b) {
		t.Errorf("two saves of the same config differ\nfirst:\n%s\nsecond:\n%s", a, b)
	}
}

// Regression guard: a config with no tray.monitors section saves exactly the
// bytes yaml.Marshal produced before this change, anchors elsewhere included.
func TestS097SaveAlias_NoMonitorsSectionSavesByteIdentical(t *testing.T) {
	docs := map[string]string{
		"plain": s097SaveAliasBase +
			"tray:\n  interval: 3h\n  mute: [news]\n",
		"anchor outside monitors": s097SaveAliasBase +
			"notice: &n {site_path: /srv/site}\ntray:\n  interval: &i 2h\n  feed_url: https://example.invalid/n.json\n",
		"no tray at all": s097SaveAliasBase + "git:\n  user: someone\n",
	}
	for name, doc := range docs {
		t.Run(name, func(t *testing.T) {
			cfg := s097SaveAliasLoad(t, doc)
			want, err := yaml.Marshal(cfg)
			if err != nil {
				t.Fatalf("yaml.Marshal: %v", err)
			}
			path := filepath.Join(t.TempDir(), "saved.yaml")
			if err := cfg.SaveTo(path); err != nil {
				t.Fatalf("SaveTo: %v", err)
			}
			got, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("read saved config: %v", err)
			}
			if !bytes.Equal(got, want) {
				t.Errorf("SaveTo output differs from yaml.Marshal for a config without tray.monitors\ngot:\n%s\nwant:\n%s", got, want)
			}
		})
	}
	empty := &Config{}
	want, err := yaml.Marshal(empty)
	if err != nil {
		t.Fatalf("yaml.Marshal(empty): %v", err)
	}
	path := filepath.Join(t.TempDir(), "empty.yaml")
	if err := empty.SaveTo(path); err != nil {
		t.Fatalf("SaveTo(empty): %v", err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read saved config: %v", err)
	}
	if !bytes.Equal(got, want) {
		t.Errorf("SaveTo output differs from yaml.Marshal for an empty Config\ngot:\n%s\nwant:\n%s", got, want)
	}
}
