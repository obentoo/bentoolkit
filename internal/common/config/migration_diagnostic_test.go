package config

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// captureStderr redirects os.Stderr for the duration of fn and returns everything
// written to it. LoadFrom emits the migration diagnostic via fmt.Fprintf(os.Stderr,
// ...), so this captures the actual user-visible warning.
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

func writeConfig(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	return path
}

// TestLoadFrom_MigrationDiagnostic_GitHubToken pins R4: a config.yaml still
// carrying github.token loads successfully and emits exactly one warning that
// names the key, the target secrets path, and the GITHUB_TOKEN env-var name.
func TestLoadFrom_MigrationDiagnostic_GitHubToken(t *testing.T) {
	t.Setenv("HOME", t.TempDir()) // deterministic secrets path in the message
	path := writeConfig(t, "github:\n  token: legacy-secret\n")

	var cfg interface{}
	var loadErr error
	out := captureStderr(t, func() {
		c, err := LoadFrom(path)
		cfg, loadErr = c, err
	})

	if loadErr != nil {
		t.Fatalf("LoadFrom must still succeed, got err = %v", loadErr)
	}
	if cfg == nil {
		t.Fatal("LoadFrom returned nil config")
	}
	if got := strings.Count(out, "github.token"); got != 1 {
		t.Fatalf("want exactly one warning naming github.token, got %d in:\n%s", got, out)
	}
	if !strings.Contains(out, "GITHUB_TOKEN") {
		t.Errorf("warning does not name the GITHUB_TOKEN env var:\n%s", out)
	}
	if !strings.Contains(out, "secrets") {
		t.Errorf("warning does not name the target secrets path:\n%s", out)
	}
	if strings.Contains(out, "legacy-secret") {
		t.Errorf("warning leaked the secret value:\n%s", out)
	}
}

// TestLoadFrom_MigrationDiagnostic_RepoToken pins R4 for a per-repository legacy
// token: the warning names the repo and the BENTOO_REPO_<NAME>_TOKEN env var.
func TestLoadFrom_MigrationDiagnostic_RepoToken(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	path := writeConfig(t, "repositories:\n  myfork:\n    provider: github\n    token: legacy-secret\n")

	var loadErr error
	out := captureStderr(t, func() {
		_, loadErr = LoadFrom(path)
	})

	if loadErr != nil {
		t.Fatalf("LoadFrom must still succeed, got err = %v", loadErr)
	}
	if !strings.Contains(out, "token") {
		t.Fatalf("want a migration warning naming the repository token, got:\n%s", out)
	}
	if !strings.Contains(out, "BENTOO_REPO_MYFORK_TOKEN") {
		t.Errorf("warning does not name BENTOO_REPO_MYFORK_TOKEN:\n%s", out)
	}
	if strings.Contains(out, "legacy-secret") {
		t.Errorf("warning leaked the secret value:\n%s", out)
	}
}

// TestLoadFrom_NoMigrationWarningWhenClean pins that a config with no legacy
// secret key produces no migration warning.
func TestLoadFrom_NoMigrationWarningWhenClean(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	path := writeConfig(t, "overlay:\n  path: /var/db/repos/bentoo\n  remote: origin\n")

	var loadErr error
	out := captureStderr(t, func() {
		_, loadErr = LoadFrom(path)
	})

	if loadErr != nil {
		t.Fatalf("LoadFrom err = %v", loadErr)
	}
	if strings.Contains(out, "no longer read") || strings.Contains(out, "GITHUB_TOKEN") {
		t.Errorf("clean config produced a migration warning:\n%s", out)
	}
}
