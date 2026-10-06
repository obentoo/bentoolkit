package autoupdate

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/obentoo/bentoolkit/internal/common/secrets"
)

func TestScrubSecret(t *testing.T) {
	if got := secrets.Scrub("url?key=ABC123&x=1", "ABC123"); strings.Contains(got, "ABC123") {
		t.Fatalf("scrubSecret left the secret in: %q", got)
	}
	if got := secrets.Scrub("no secret here", ""); got != "no secret here" {
		t.Fatalf("scrubSecret with empty secret altered the string: %q", got)
	}
}

// withSecretsFile isolates HOME (and XDG_CONFIG_HOME) to a fresh tempdir so
// secrets.Lookup can never read the developer's real ~/.config/bentoo/secrets,
// then, when content != "", writes it as the user-scope secrets file so the
// lookup resolves it. Empty content leaves no file (absence == miss). Isolation
// is mandatory (D9): a bare blank-env test would otherwise read the real user
// secrets file. Both HOME and XDG_CONFIG_HOME are set because secrets.Paths
// honors XDG_CONFIG_HOME first (mirroring cmd/bentoo's overlay_autoupdate_test).
func withSecretsFile(t *testing.T, content string) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	if content == "" {
		return
	}
	p := filepath.Join(home, ".config", "bentoo", "secrets")
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		t.Fatalf("write secrets: %v", err)
	}
}
