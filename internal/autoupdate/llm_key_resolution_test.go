package autoupdate

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/obentoo/bentoolkit/internal/common/secrets"
)

// writeUserSecrets writes a user-scope secrets file under an isolated HOME and
// returns nothing; the caller has already set HOME via t.Setenv. Isolation is
// mandatory: a test asserting a key is missing would otherwise read the
// developer's real ~/.config/bentoo/secrets (D9, commit a77de4b).
func writeUserSecrets(t *testing.T, home, content string) {
	t.Helper()
	p := filepath.Join(home, ".config", "bentoo", "secrets")
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		t.Fatalf("write secrets: %v", err)
	}
}

// TestNewLLMClient_ResolvesViaChain pins that the Claude and OpenAI clients
// resolve their API key through secrets.Lookup, not os.Getenv:
//   - an empty api_key_env is ErrLLMNotConfigured;
//   - a key present only in the secrets file (env unset) still constructs the
//     client (the chain, not env-only, is consulted);
//   - a total miss is ErrLLMAPIKeyMissing and the message names the secrets path.
func TestNewLLMClient_ResolvesViaChain(t *testing.T) {
	constructors := map[string]func(LLMConfig) error{
		"claude": func(cfg LLMConfig) error { _, err := NewClaudeClient(cfg); return err },
		"openai": func(cfg LLMConfig) error { _, err := NewOpenAIClient(cfg); return err },
	}

	for provider, construct := range constructors {
		t.Run(provider+"/api_key_env unset is ErrLLMNotConfigured", func(t *testing.T) {
			t.Setenv("HOME", t.TempDir())
			err := construct(LLMConfig{Provider: provider, APIKeyEnv: ""})
			if !errors.Is(err, ErrLLMNotConfigured) {
				t.Fatalf("err = %v, want ErrLLMNotConfigured", err)
			}
		})

		t.Run(provider+"/key in secrets file constructs client", func(t *testing.T) {
			home := t.TempDir()
			t.Setenv("HOME", home)
			t.Setenv("BENTOO_TEST_LLM_KEY", "") // env miss; only the file has it
			writeUserSecrets(t, home, "BENTOO_TEST_LLM_KEY=file-key\n")

			err := construct(LLMConfig{Provider: provider, APIKeyEnv: "BENTOO_TEST_LLM_KEY"})
			if err != nil {
				t.Fatalf("construct with file-resolved key: err = %v, want nil", err)
			}
		})

		t.Run(provider+"/total miss names the secrets path", func(t *testing.T) {
			t.Setenv("HOME", t.TempDir())
			t.Setenv("BENTOO_TEST_LLM_KEY", "")

			err := construct(LLMConfig{Provider: provider, APIKeyEnv: "BENTOO_TEST_LLM_KEY"})
			if !errors.Is(err, ErrLLMAPIKeyMissing) {
				t.Fatalf("err = %v, want ErrLLMAPIKeyMissing", err)
			}
			var named bool
			for _, p := range secrets.Paths() {
				if strings.Contains(err.Error(), p) {
					named = true
					break
				}
			}
			if !named {
				t.Fatalf("error %q names no secrets path from %v", err.Error(), secrets.Paths())
			}
		})
	}
}
