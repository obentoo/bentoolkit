package snapshot

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/obentoo/bentoolkit/internal/common/secrets"
)

// captureStderr redirects os.Stderr for the duration of fn and returns everything
// written to it. LoadFrom emits the migration diagnostic via fmt.Fprintf(os.Stderr,
// ...), so this captures the actual user-visible warning rather than a stub — the
// shape story 015 task 5.2 used for the config.yaml diagnostic.
func captureStderr(t *testing.T, fn func()) string {
	t.Helper()
	orig := os.Stderr
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	os.Stderr = w
	done := make(chan string, 1)
	go func() {
		b, _ := io.ReadAll(r)
		done <- string(b)
	}()
	fn()
	_ = w.Close()
	os.Stderr = orig
	return <-done
}

// writeSnapshotTOML writes content to a snapshot.toml in a fresh tempdir and
// returns its path.
func writeSnapshotTOML(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "snapshot.toml")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write snapshot.toml: %v", err)
	}
	return path
}

const legacySMTPPassword = "legacy-smtp-secret"

// legacyConfigTOML carries the removed smtp.password key alongside the fields
// that legitimately remain (R2.2), so the test also proves the diagnostic does
// not fire on host/port/user.
const legacyConfigTOML = `
[engine]
driver = "btrbk"
subvolumes = ["/home"]

[notify.email]
to = ["ops@example.com"]
from = "bentoo@example.com"

[notify.email.smtp]
host = "smtp.example.com"
port = 587
user = "bentoo"
password = "` + legacySMTPPassword + `"
`

const cleanConfigTOML = `
[engine]
driver = "btrbk"
subvolumes = ["/home"]

[notify.email.smtp]
host = "smtp.example.com"
port = 587
user = "bentoo"
`

// R3.1, R3.2 — a snapshot.toml still carrying smtp.password loads successfully
// and emits exactly one warning naming the key, the target secrets path, and
// BENTOO_SMTP_PASSWORD. The password VALUE never reaches the output.
func TestLoadFrom_MigrationDiagnostic_SMTPPassword(t *testing.T) {
	isolateSecrets(t) // deterministic user-scope path in the message
	path := writeSnapshotTOML(t, legacyConfigTOML)

	var cfg *Config
	var loadErr error
	out := captureStderr(t, func() {
		cfg, loadErr = LoadFrom(path)
	})

	if loadErr != nil {
		t.Fatalf("LoadFrom must still succeed with a legacy key (R3.2), got err = %v", loadErr)
	}
	if cfg == nil {
		t.Fatal("LoadFrom returned a nil config (R3.2)")
	}

	// Exactly one — a diagnostic that repeats is as bad as one that is silent.
	if got := strings.Count(out, "smtp.password"); got != 1 {
		t.Fatalf("want exactly one warning naming smtp.password (R3.1), got %d in:\n%s", got, out)
	}
	if !strings.Contains(out, "BENTOO_SMTP_PASSWORD") {
		t.Errorf("warning does not name the BENTOO_SMTP_PASSWORD env var (R3.1):\n%s", out)
	}
	// Assert the EXACT user-scope path. A bare "secrets" substring check would
	// pass even when the message points at a path the user cannot write.
	want, ok := secrets.UserPath()
	if !ok {
		t.Fatal("secrets.UserPath() reports no user-scope path with HOME set to a tempdir")
	}
	if !strings.Contains(out, want) {
		t.Errorf("warning does not name the target secrets path %q (R3.1):\n%s", want, out)
	}
	if strings.Contains(out, legacySMTPPassword) {
		t.Errorf("warning leaked the secret value (008 R1.3):\n%s", out)
	}

	// The config still parses; the surviving SMTP fields are configuration, not
	// secrets, and must be unaffected (R2.2).
	if cfg.Notify.Email.SMTP.Host != "smtp.example.com" || cfg.Notify.Email.SMTP.User != "bentoo" {
		t.Errorf("legacy key handling disturbed the retained SMTP fields (R2.2): %+v", cfg.Notify.Email.SMTP)
	}
}

// R3.4 — a config with no legacy key produces no warning at all.
func TestLoadFrom_NoMigrationWarningWhenClean(t *testing.T) {
	isolateSecrets(t)
	path := writeSnapshotTOML(t, cleanConfigTOML)

	var loadErr error
	out := captureStderr(t, func() {
		_, loadErr = LoadFrom(path)
	})

	if loadErr != nil {
		t.Fatalf("LoadFrom err = %v", loadErr)
	}
	if strings.TrimSpace(out) != "" {
		t.Errorf("clean config produced output, want none (R3.4):\n%s", out)
	}
}

// Out of scope guard — this is a targeted diagnostic for ONE legacy key, not a
// general strict-decode pass. An unrelated unknown key must stay silent, or the
// change acquires the compatibility risk story.md explicitly rejected.
func TestLoadFrom_UnrelatedUnknownKeyIsSilent(t *testing.T) {
	isolateSecrets(t)
	path := writeSnapshotTOML(t, cleanConfigTOML+"\n[unrelated]\nfuture_option = true\n")

	var loadErr error
	out := captureStderr(t, func() {
		_, loadErr = LoadFrom(path)
	})

	if loadErr != nil {
		t.Fatalf("an unknown key must not fail the load, got err = %v", loadErr)
	}
	if strings.TrimSpace(out) != "" {
		t.Errorf("an unrelated unknown key produced a warning, want none:\n%s", out)
	}
}

// F-H (carried from story 015's second validation pass) — when $HOME is
// unresolvable there IS no user-scope path, and secrets.Paths()[0] silently
// becomes the root-owned /etc/bentoo/secrets. Telling an unprivileged user to
// write their password into a 0600 root file they cannot open is worse than
// saying nothing about a path at all. This loader runs as root under the systemd
// timer, which is exactly where $HOME goes missing, so the fallback must name the
// env var alone.
func TestLoadFrom_MigrationDiagnostic_NoUserScopePathNamesEnvVarOnly(t *testing.T) {
	t.Setenv("HOME", "")
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("BENTOO_SMTP_PASSWORD", "")

	if _, ok := secrets.UserPath(); ok {
		t.Skip("this platform still resolves a home directory with HOME blank; the fallback is unreachable here")
	}

	path := writeSnapshotTOML(t, legacyConfigTOML)

	var loadErr error
	out := captureStderr(t, func() {
		_, loadErr = LoadFrom(path)
	})

	if loadErr != nil {
		t.Fatalf("LoadFrom must still succeed (R3.2), got err = %v", loadErr)
	}
	if got := strings.Count(out, "smtp.password"); got != 1 {
		t.Fatalf("want exactly one warning naming smtp.password (R3.1), got %d in:\n%s", got, out)
	}
	if !strings.Contains(out, "BENTOO_SMTP_PASSWORD") {
		t.Errorf("warning does not name the BENTOO_SMTP_PASSWORD env var (R3.1):\n%s", out)
	}
	if strings.Contains(out, "/etc/bentoo/secrets") {
		t.Errorf("warning directs the user at the root-owned system secrets file when no user-scope path exists (F-H):\n%s", out)
	}
	if strings.Contains(out, legacySMTPPassword) {
		t.Errorf("warning leaked the secret value (008 R1.3):\n%s", out)
	}
}
