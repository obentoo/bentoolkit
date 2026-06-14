package config

import (
	"os"
	"path/filepath"
	"testing"
)

// TestLoadFrom_UnknownKeyIsLenient verifies that an unknown YAML key (e.g. a
// token misplaced under `overlay:`, which has no token field) does NOT abort
// loading — the lenient unmarshal stays authoritative — while the strict probe
// that emits the stderr warning still runs. The recognized fields must parse
// correctly regardless.
func TestLoadFrom_UnknownKeyIsLenient(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	content := "" +
		"overlay:\n" +
		"  path: /var/db/repos/bentoo\n" +
		"  token: misplaced-secret\n" + // unknown: OverlayConfig has no token field
		"github:\n" +
		"  token: real-token\n"
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	cfg, err := LoadFrom(path)
	if err != nil {
		t.Fatalf("LoadFrom must not error on an unknown key: %v", err)
	}
	if cfg.Overlay.Path != "/var/db/repos/bentoo" {
		t.Errorf("Overlay.Path = %q, want /var/db/repos/bentoo", cfg.Overlay.Path)
	}
	if cfg.GitHub.Token != "real-token" {
		t.Errorf("GitHub.Token = %q, want real-token (the correctly-placed token)", cfg.GitHub.Token)
	}
}
