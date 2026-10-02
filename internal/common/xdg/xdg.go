// Package xdg resolves XDG Base Directory locations.
package xdg

import "path/filepath"

// StateHome returns the base directory for user state files: XDG_STATE_HOME
// when it is set to an absolute path, else home/.local/state. A relative or
// empty value is ignored, as the XDG Base Directory specification requires.
// getenv is injected so callers and tests never touch the process environment.
func StateHome(getenv func(string) string, home string) string {
	if dir := getenv("XDG_STATE_HOME"); filepath.IsAbs(dir) {
		return dir
	}
	return filepath.Join(home, ".local", "state")
}
