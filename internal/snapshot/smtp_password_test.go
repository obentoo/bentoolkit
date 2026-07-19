package snapshot

import (
	"context"
	"fmt"
	"net/smtp"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// isolateSecrets points the secrets chain at a per-test tempdir and blanks the
// environment leg, so a populated ~/.config/bentoo/secrets on the developer's
// machine can never make these tests pass for the wrong reason (015 D9). Both
// HOME and XDG_CONFIG_HOME must be set: secrets.pathsFn honors XDG_CONFIG_HOME
// first and falls back to $HOME/.config, so setting only one leaves the other
// pointing at the real user. t.Setenv forbids t.Parallel, which is intended.
//
// It returns the user-scope secrets file path inside the tempdir; the parent
// directory is not created, so a test that wants a miss can simply not write it.
func isolateSecrets(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("BENTOO_SMTP_PASSWORD", "")
	return filepath.Join(home, ".config", "bentoo", "secrets")
}

// writeSecret creates the user-scope secrets file with one NAME=value line at
// mode 0600, so warnIfLoose stays quiet and the only warning a test observes is
// the one under test.
func writeSecret(t *testing.T, path, name, value string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := os.WriteFile(path, []byte(fmt.Sprintf("%s=%s\n", name, value)), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
}

// smtpCapture records what the SMTP transport seam was handed, so a test can
// assert on the smtp.Auth value the notifier built without opening a socket.
type smtpCapture struct {
	called bool
	auth   smtp.Auth
	addr   string
}

// captureSMTP replaces the smtpSendMail seam for the duration of the test.
func captureSMTP(t *testing.T) *smtpCapture {
	t.Helper()
	sc := &smtpCapture{}
	orig := smtpSendMail
	t.Cleanup(func() { smtpSendMail = orig })
	smtpSendMail = func(addr string, a smtp.Auth, from string, to []string, msg []byte) error {
		sc.called, sc.auth, sc.addr = true, a, addr
		return nil
	}
	return sc
}

// captureWarnings redirects the package warn seam into a buffer.
func captureWarnings(t *testing.T) *strings.Builder {
	t.Helper()
	var b strings.Builder
	orig := warnLogf
	t.Cleanup(func() { warnLogf = orig })
	warnLogf = func(format string, args ...interface{}) { fmt.Fprintf(&b, format, args...) }
	return &b
}

// smtpNotifyConfig is an email notify config using the SMTP transport. The
// password is deliberately absent: it is no longer a config field (R2.1).
func smtpNotifyConfig(user string) NotifyConfig {
	return NotifyConfig{
		Email: EmailConfig{
			To:   []string{"ops@example.com"},
			From: "bentoo@example.com",
			SMTP: SMTPConfig{Host: "smtp.example.com", Port: 587, User: user},
		},
	}
}

// plainAuthSecret replays the PLAIN handshake against the built auth value to
// recover the password it carries. PlainAuth refuses to hand credentials to an
// unencrypted connection, hence TLS: true, and it verifies the server name
// matches the host it was constructed with.
func plainAuthSecret(t *testing.T, a smtp.Auth) string {
	t.Helper()
	_, toServer, err := a.Start(&smtp.ServerInfo{Name: "smtp.example.com", TLS: true, Auth: []string{"PLAIN"}})
	if err != nil {
		t.Fatalf("PLAIN Start: %v", err)
	}
	// PLAIN is "\x00username\x00password" (RFC 4616).
	parts := strings.Split(string(toServer), "\x00")
	if len(parts) != 3 {
		t.Fatalf("PLAIN payload has %d fields, want 3", len(parts))
	}
	return parts[2]
}

// R1.1 — the password resolves from the secrets chain, not from snapshot.toml,
// and enables PLAIN auth carrying exactly the resolved value.
func TestSMTPPassword_ResolvesFromSecretsFile(t *testing.T) {
	const want = "s3cr3t-smtp-pw"
	writeSecret(t, isolateSecrets(t), "BENTOO_SMTP_PASSWORD", want)
	sent := captureSMTP(t)

	n, err := newNotifier(smtpNotifyConfig("bentoo"))
	if err != nil {
		t.Fatalf("newNotifier: %v", err)
	}
	if err := n.Notify(context.Background(), failRun()); err != nil {
		t.Fatalf("Notify: %v", err)
	}

	if !sent.called {
		t.Fatal("SMTP transport was never invoked")
	}
	if sent.auth == nil {
		t.Fatal("auth is nil with a resolvable BENTOO_SMTP_PASSWORD and a configured user (R1.1)")
	}
	if got := plainAuthSecret(t, sent.auth); got != want {
		t.Errorf("PLAIN password = %q, want the secret resolved from the chain %q (R1.1)", got, want)
	}
}

// R1.2 — a total miss is the normal "no secret" case: send unauthenticated,
// exactly as an empty password did before.
func TestSMTPPassword_TotalMissSendsUnauthenticated(t *testing.T) {
	isolateSecrets(t) // no secrets file written
	sent := captureSMTP(t)

	n, err := newNotifier(smtpNotifyConfig("bentoo"))
	if err != nil {
		t.Fatalf("newNotifier: %v", err)
	}
	if err := n.Notify(context.Background(), failRun()); err != nil {
		t.Fatalf("Notify must still deliver with no password resolvable (R1.2): %v", err)
	}

	if !sent.called {
		t.Fatal("notification was not sent when the password is absent (R1.2)")
	}
	if sent.auth != nil {
		t.Error("auth is non-nil with no resolvable password, want nil (R1.2)")
	}
}

// R1.3 — an unreadable user-scope secrets file warns and degrades to
// unauthenticated; it never aborts the notification. The file is created as a
// directory so the read fails with EISDIR, which also fails for root (a
// chmod-000 file would not).
func TestSMTPPassword_UnreadableSecretsFileWarnsAndSends(t *testing.T) {
	path := isolateSecrets(t)
	if err := os.MkdirAll(path, 0o700); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	sent := captureSMTP(t)
	warned := captureWarnings(t)

	n, err := newNotifier(smtpNotifyConfig("bentoo"))
	if err != nil {
		t.Fatalf("newNotifier must not fail over an unreadable secrets file (R1.3): %v", err)
	}
	if err := n.Notify(context.Background(), failRun()); err != nil {
		t.Fatalf("Notify must still deliver over an unreadable secrets file (R1.3): %v", err)
	}

	if !sent.called {
		t.Fatal("notification was not sent over an unreadable secrets file (R1.3)")
	}
	if sent.auth != nil {
		t.Error("auth is non-nil after an unreadable secrets file, want nil (R1.3)")
	}
	if !strings.Contains(strings.ToLower(warned.String()), "smtp") {
		t.Errorf("no warning naming the SMTP credential was emitted (R1.3), got: %q", warned.String())
	}
}

// Unchanged Behavior — an unset smtp.user means no auth regardless of a
// resolvable password, preserving the existing User != "" guard.
func TestSMTPPassword_NoUserMeansNoAuth(t *testing.T) {
	writeSecret(t, isolateSecrets(t), "BENTOO_SMTP_PASSWORD", "s3cr3t-smtp-pw")
	sent := captureSMTP(t)

	n, err := newNotifier(smtpNotifyConfig(""))
	if err != nil {
		t.Fatalf("newNotifier: %v", err)
	}
	if err := n.Notify(context.Background(), failRun()); err != nil {
		t.Fatalf("Notify: %v", err)
	}

	if !sent.called {
		t.Fatal("SMTP transport was never invoked")
	}
	if sent.auth != nil {
		t.Error("auth is non-nil with an empty smtp.user, want nil (Unchanged Behavior)")
	}
}
