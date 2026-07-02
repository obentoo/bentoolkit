package config

import (
	"bytes"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"gopkg.in/yaml.v3"
)

// exampleConfigPath locates config.example.yaml at the repo root relative to this
// test's source file, so the test is independent of the working directory.
func exampleConfigPath(t *testing.T) string {
	t.Helper()
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed; cannot locate repo root")
	}
	// internal/common/config/ -> repo root is three levels up.
	root := filepath.Join(filepath.Dir(thisFile), "..", "..", "..")
	return filepath.Join(root, "config.example.yaml")
}

// TestExampleConfigLoads guards the shipped config.example.yaml against schema
// drift: it must parse into Config and, critically, contain no keys that map to
// no struct field. yaml.v3 silently drops unknown keys on a normal decode, so a
// stale example (a renamed/removed field) would look fine to users while quietly
// doing nothing — a strict KnownFields decode turns that into a test failure.
func TestExampleConfigLoads(t *testing.T) {
	path := exampleConfigPath(t)

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}

	// Lenient decode: the example must be valid YAML that maps into Config.
	var cfg Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		t.Fatalf("config.example.yaml is not valid Config YAML: %v", err)
	}

	// Strict decode: every key must correspond to a real field, so the example
	// never documents a setting the code does not read.
	strict := yaml.NewDecoder(bytes.NewReader(data))
	strict.KnownFields(true)
	if err := strict.Decode(&Config{}); err != nil {
		t.Errorf("config.example.yaml has unknown/renamed keys (schema drift): %v", err)
	}

	// Sanity: the example should demonstrate the one field most commands need.
	if cfg.Overlay.Path == "" {
		t.Error("config.example.yaml should set overlay.path to a non-empty example value")
	}
}
