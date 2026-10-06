package autoupdate

import (
	"path/filepath"
	"testing"
)

// writeUserSecrets writes a user-scope secrets file under an isolated HOME and
// returns nothing; the caller has already set HOME via t.Setenv. Isolation is
// mandatory: a test asserting a key is missing would otherwise read the
// developer's real ~/.config/bentoo/secrets (D9, commit a77de4b).
// isolateSecretsHome redirects the unified chain's user-scope slot at a fresh
// tempdir and returns it. Both HOME and XDG_CONFIG_HOME must be set (D9):
// secrets.pathsFn honors XDG_CONFIG_HOME BEFORE $HOME/.config, so redirecting
// HOME alone lets the resolver walk past the tempdir into the developer's real
// ~/.config/bentoo/secrets. Setting only HOME made these tests pass on a host
// with XDG_CONFIG_HOME unset and fail wherever it is exported.
func isolateSecretsHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	return home
}
