package config

import (
	"strings"
	"testing"
	"time"

	yaml "go.yaml.in/yaml/v3"
)

// Story 099, sub-task 1.2: config.example.yaml documents the three new
// tray.monitors blocks with their defaults (R6.5). The generic example tests
// match a key by its leaf name anywhere in the file, and `enabled:` is
// already written for every story 097 block, so each new key is looked up
// here by its full path in the uncommented example tray block.

// s099ExampleKeyPaths is every key this story adds, relative to
// tray.monitors, with the default value the example must show.
var s099ExampleKeyPaths = []struct{ path, def string }{
	{"updates.enabled", "true"},
	{"glsa.enabled", "true"},
	{"sync.enabled", "true"},
	{"sync.max_age", "168h"},
}

// s099ExampleMonitors returns the tray.monitors mapping of the uncommented
// example tray block.
func s099ExampleMonitors(t *testing.T) *yaml.Node {
	t.Helper()
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
	return monitors
}

// TestS099ExampleConfig_DocumentsEveryNewKeyWithItsDefault is R6.5: each new
// key is present under its own block and shows its default. max_age is
// compared as a duration, so 168h and 7*24h written another way both count,
// while a value such as 7d (not a Go duration) does not.
func TestS099ExampleConfig_DocumentsEveryNewKeyWithItsDefault(t *testing.T) {
	monitors := s099ExampleMonitors(t)
	for _, k := range s099ExampleKeyPaths {
		n := monitors
		for _, seg := range strings.Split(k.path, ".") {
			n = yamlChild(n, seg)
		}
		if n == nil {
			t.Errorf("config.example.yaml does not document tray.monitors.%s", k.path)
			continue
		}
		if n.Kind != yaml.ScalarNode {
			t.Errorf("tray.monitors.%s in config.example.yaml is not a scalar", k.path)
			continue
		}
		if k.path == "sync.max_age" {
			got, err := time.ParseDuration(n.Value)
			if err != nil || got != 7*24*time.Hour {
				t.Errorf("tray.monitors.sync.max_age in config.example.yaml is %q, want the default 168h", n.Value)
			}
			continue
		}
		if n.Value != k.def {
			t.Errorf("tray.monitors.%s in config.example.yaml is %q, want the default %s", k.path, n.Value, k.def)
		}
	}
}

// TestS099ExampleConfig_MaxAgeLivesInSyncOnly is the hostile half of the
// lookup above: max_age documented under updates or glsa would be an unknown
// key there (and found by a leaf-name search), so the example must carry it
// under sync and nowhere else among the new blocks.
func TestS099ExampleConfig_MaxAgeLivesInSyncOnly(t *testing.T) {
	monitors := s099ExampleMonitors(t)
	for _, block := range []string{"updates", "glsa"} {
		b := yamlChild(monitors, block)
		if b == nil {
			t.Errorf("config.example.yaml has no tray.monitors.%s block", block)
			continue
		}
		if yamlChild(b, "max_age") != nil {
			t.Errorf("config.example.yaml documents max_age under tray.monitors.%s; it belongs to sync only", block)
		}
	}
	if yamlChild(yamlChild(monitors, "sync"), "max_age") == nil {
		t.Error("config.example.yaml has no tray.monitors.sync.max_age")
	}
}
