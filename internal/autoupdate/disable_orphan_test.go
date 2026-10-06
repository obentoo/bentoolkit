package autoupdate

import (
	"os"
	"path/filepath"
	"testing"
)

// writePackagesTOML writes content to overlay/.autoupdate/packages.toml under a
// temp dir and returns the overlay path and the config file path.
func writePackagesTOML(t *testing.T, content string) (overlayPath, configPath string) {
	t.Helper()
	overlayPath = t.TempDir()
	dir := filepath.Join(overlayPath, ".autoupdate")
	if err := os.MkdirAll(dir, 0o750); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	configPath = filepath.Join(dir, "packages.toml")
	if err := os.WriteFile(configPath, []byte(content), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	return overlayPath, configPath
}
