package config

import (
	"strings"
	"testing"

	yaml "go.yaml.in/yaml/v3"
)

// Story 098, sub-task 1.2: config.example.yaml documents every new
// tray.monitors key with its default (R6.6). The generic example tests match
// a leaf name anywhere in the file, so `enabled:` written once for disk would
// count for all four new blocks; this walk goes by full path.

// s098ExampleDefaults is every key this story adds, relative to
// tray.monitors, with the default the example must show.
var s098ExampleDefaults = []struct{ path, want string }{
	{"emerge.enabled", "true"},
	{"emerge.log", "/var/log/emerge.log"},
	{"units.enabled", "true"},
	{"units.system", "true"},
	{"units.user", "true"},
	{"readonly.enabled", "true"},
	{"smart.enabled", "true"},
}

// TestS098ExampleConfig_DocumentsNewMonitorKeys walks the uncommented example
// tray block and requires each new key, at its full path, to show its default.
func TestS098ExampleConfig_DocumentsNewMonitorKeys(t *testing.T) {
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
	for _, k := range s098ExampleDefaults {
		n := monitors
		for _, seg := range strings.Split(k.path, ".") {
			n = yamlChild(n, seg)
		}
		switch {
		case n == nil:
			t.Errorf("the example tray block does not document tray.monitors.%s", k.path)
		case n.Kind != yaml.ScalarNode || n.Value != k.want:
			t.Errorf("the example shows tray.monitors.%s = %q, want its default %q", k.path, n.Value, k.want)
		}
	}
}
