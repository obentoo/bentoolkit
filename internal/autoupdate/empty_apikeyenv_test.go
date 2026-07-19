package autoupdate

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/obentoo/bentoolkit/internal/common/secrets"
)

// unreadableUserSecrets points the unified chain's user-scope slot at a path
// that exists but cannot be parsed, so secrets.Lookup yields ErrUnreadable.
// A directory is used rather than a chmod-000 file so the fixture behaves the
// same for root, which ignores permission bits (the mode-based tests in
// internal/common/secrets t.Skip on root for exactly that reason).
//
// Isolation comes from isolateSecretsHome, which redirects HOME and
// XDG_CONFIG_HOME together (D9).
func unreadableUserSecrets(t *testing.T) {
	t.Helper()
	home := isolateSecretsHome(t)
	if err := os.MkdirAll(filepath.Join(home, ".config", "bentoo", "secrets"), 0o755); err != nil {
		t.Fatalf("staging unreadable secrets fixture: %v", err)
	}
}

// TestConstructors_EmptyAPIKeyEnv_SkipsLookup pins that requesting NO secret is
// not the same as failing to resolve one.
//
// For a subscription run `api_key_env` is legitimately empty: the agentic
// `claude` CLI authenticates itself and bentoo injects no credential. The three
// constructors below resolve the key through the unified chain, but unlike
// NewClaudeClient (llm.go:206) and NewOpenAIClient (openai.go:86) they carried
// no APIKeyEnv == "" guard, so they called secrets.Lookup(""). That lookup
// misses the environment (os.Getenv("") is always "") and falls through to the
// user-scope file — and an unreadable one turns "no key requested" into a hard
// constructor failure. Before the unified chain, os.Getenv("") simply returned
// "" and the subscription run proceeded.
func TestConstructors_EmptyAPIKeyEnv_SkipsLookup(t *testing.T) {
	cases := []struct {
		name string
		call func(LLMConfig) error
	}{
		{"claude_code_client", func(c LLMConfig) error { _, err := NewClaudeCodeClient(c); return err }},
		{"manifest_fixer", func(c LLMConfig) error { _, err := NewClaudeCodeFixer(c); return err }},
		{"registry_fixer", func(c LLMConfig) error { _, err := NewClaudeCodeRegistryFixer(c); return err }},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			stubLookPathFound(t)
			unreadableUserSecrets(t)

			// APIKeyEnv empty: the subscription shape. No secret is being asked for.
			err := tc.call(LLMConfig{Provider: "claude", Bare: "auto"})

			if errors.Is(err, secrets.ErrUnreadable) {
				t.Fatalf("constructor failed with ErrUnreadable for an empty api_key_env; "+
					"no secret was requested, so the secrets file must not be consulted: %v", err)
			}
		})
	}
}

// TestConstructors_EmptyAPIKeyEnv_StillResolvesWhenNamed is the companion guard:
// the fix must skip the lookup only when the name is empty, never suppress a
// genuine ErrUnreadable for a caller that did name a secret.
func TestConstructors_EmptyAPIKeyEnv_StillResolvesWhenNamed(t *testing.T) {
	stubLookPathFound(t)
	unreadableUserSecrets(t)

	_, err := NewClaudeCodeClient(LLMConfig{Provider: "claude", APIKeyEnv: "BENTOO_TEST_NAMED_KEY", Bare: "auto"})

	if !errors.Is(err, secrets.ErrUnreadable) {
		t.Fatalf("a named api_key_env must still surface ErrUnreadable from an unreadable secrets file, got: %v", err)
	}
}
