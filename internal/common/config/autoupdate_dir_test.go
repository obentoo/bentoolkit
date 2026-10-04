package config

import (
	"path/filepath"
	"testing"
)

// TestAutoupdateDirFollowsXDGConfigHome pins that the autoupdate state lives
// beside config.yaml: a temporary XDG_CONFIG_HOME must move both, or a scratch
// run writes its pending entries into the real state.
func TestAutoupdateDirFollowsXDGConfigHome(t *testing.T) {
	home, xdg := t.TempDir(), t.TempDir()
	t.Setenv("HOME", home)

	t.Setenv("XDG_CONFIG_HOME", xdg)
	if got, _ := AutoupdateDir(); got != filepath.Join(xdg, "bentoo", "autoupdate") {
		t.Errorf("with XDG_CONFIG_HOME: %s", got)
	}

	t.Setenv("XDG_CONFIG_HOME", "")
	if got, _ := AutoupdateDir(); got != filepath.Join(home, ".config", "bentoo", "autoupdate") {
		t.Errorf("without XDG_CONFIG_HOME: %s", got)
	}
}
