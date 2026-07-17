package github

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/obentoo/bentoolkit/internal/common/secrets"
)

// TestResolveToken pins the single chain-aware GitHub-token resolver: GITHUB_TOKEN
// takes precedence over GH_TOKEN, whitespace is trimmed, an empty environment with
// no secrets file yields "" and no error (anonymous is allowed), and an unreadable
// user-scope secrets file propagates secrets.ErrUnreadable so a caller can warn
// rather than silently go anonymous.
func TestResolveToken(t *testing.T) {
	t.Run("GITHUB_TOKEN precedence and trim", func(t *testing.T) {
		t.Setenv("HOME", t.TempDir())
		t.Setenv("GITHUB_TOKEN", "  ghp_primary  ")
		t.Setenv("GH_TOKEN", "gho_fallback")

		got, err := ResolveToken()
		if err != nil {
			t.Fatalf("err = %v, want nil", err)
		}
		if got != "ghp_primary" {
			t.Fatalf("ResolveToken() = %q, want ghp_primary", got)
		}
	})

	t.Run("GH_TOKEN fallback", func(t *testing.T) {
		t.Setenv("HOME", t.TempDir())
		t.Setenv("GITHUB_TOKEN", "")
		t.Setenv("GH_TOKEN", "gho_fallback")

		got, err := ResolveToken()
		if err != nil {
			t.Fatalf("err = %v, want nil", err)
		}
		if got != "gho_fallback" {
			t.Fatalf("ResolveToken() = %q, want gho_fallback", got)
		}
	})

	t.Run("none set yields empty without error", func(t *testing.T) {
		// Isolate HOME so the user-scope file resolves to an empty tempdir; the
		// system-scope /etc/bentoo/secrets is a silent EACCES miss for a non-root
		// test user (D2). Without this the test reads the developer's real token.
		t.Setenv("HOME", t.TempDir())
		t.Setenv("GITHUB_TOKEN", "")
		t.Setenv("GH_TOKEN", "")

		got, err := ResolveToken()
		if err != nil {
			t.Fatalf("err = %v, want nil", err)
		}
		if got != "" {
			t.Fatalf("ResolveToken() = %q, want empty", got)
		}
	})

	t.Run("unreadable user-scope secrets file propagates ErrUnreadable", func(t *testing.T) {
		if os.Geteuid() == 0 {
			t.Skip("root ignores permission bits; assertion is vacuous")
		}
		home := t.TempDir()
		t.Setenv("HOME", home)
		t.Setenv("GITHUB_TOKEN", "")
		t.Setenv("GH_TOKEN", "")

		secretsPath := filepath.Join(home, ".config", "bentoo", "secrets")
		if err := os.MkdirAll(filepath.Dir(secretsPath), 0o700); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		if err := os.WriteFile(secretsPath, []byte("GITHUB_TOKEN=x\n"), 0o000); err != nil {
			t.Fatalf("write: %v", err)
		}

		_, err := ResolveToken()
		if err == nil {
			t.Fatal("ResolveToken() err = nil, want wrapping secrets.ErrUnreadable")
		}
		if !errors.Is(err, secrets.ErrUnreadable) {
			t.Fatalf("ResolveToken() err = %v, want wrapping secrets.ErrUnreadable", err)
		}
	})
}
