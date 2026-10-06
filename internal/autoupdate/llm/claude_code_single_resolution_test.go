package llm

import (
	"strings"
	"testing"
)

// TestResolveBare_UsesPassedKey pins that bare-mode auto-selection is driven by
// the single resolved key passed in, NOT by an independent os.Getenv. auto → bare
// iff api_key_env is configured AND the resolved key is non-empty; explicit
// true/false are honoured verbatim.
func TestResolveBare_UsesPassedKey(t *testing.T) {
	cases := []struct {
		name string
		cfg  LLMConfig
		key  string
		want bool
	}{
		{"auto + env + key present → bare", LLMConfig{Bare: "auto", APIKeyEnv: "K"}, "resolved", true},
		{"auto + env + empty key → not bare", LLMConfig{Bare: "auto", APIKeyEnv: "K"}, "", false},
		{"auto + no api_key_env → not bare", LLMConfig{Bare: "auto", APIKeyEnv: ""}, "resolved", false},
		{"explicit true → bare", LLMConfig{Bare: "true", APIKeyEnv: ""}, "", true},
		{"explicit false → not bare", LLMConfig{Bare: "false", APIKeyEnv: "K"}, "resolved", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := ResolveBare(tc.cfg, tc.key); got != tc.want {
				t.Fatalf("resolveBare(%+v, %q) = %v, want %v", tc.cfg, tc.key, got, tc.want)
			}
		})
	}
}

// TestChildEnv_InjectsResolvedKey pins the critical single-resolution fix: in bare
// mode childEnv injects exactly the resolved key it is handed (never os.Getenv, so
// never an empty credential), and it uses that value even when the environment
// holds a different one under api_key_env — or under ANTHROPIC_API_KEY itself.
//
// The ambient ANTHROPIC_API_KEY is what made this test fail inside a Claude Code
// session (S051-R1.5): the old builder appended the resolved key AFTER the
// inherited one, and lookupEnv reads the first match while os/exec uses the
// last. The count below judges the slice itself, so neither reader's choice of
// duplicate can make it pass.
func TestChildEnv_InjectsResolvedKey(t *testing.T) {
	t.Setenv("MYKEY", "env-value-should-be-ignored")
	t.Setenv("ANTHROPIC_API_KEY", "ambient")

	env := ChildEnv(true, "MYKEY", "resolved-secret", AgentEnvExtra{})

	var keys []string
	for _, kv := range env {
		if v, ok := strings.CutPrefix(kv, "ANTHROPIC_API_KEY="); ok {
			keys = append(keys, v)
		}
	}
	if len(keys) != 1 || keys[0] != "resolved-secret" {
		t.Fatalf("ANTHROPIC_API_KEY entries = %q, want exactly [resolved-secret] (the passed key, not env)", keys)
	}
}

// TestChildEnv_NonBareScrubs pins that non-bare mode strips every auth source —
// the canonical ANTHROPIC_API_KEY and the configured api_key_env — so an inherited
// key cannot override the operator's `bare: false` choice.
func TestChildEnv_NonBareScrubs(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "inherited")
	t.Setenv("MYKEY", "inherited")

	env := ChildEnv(false, "MYKEY", "resolved-secret", AgentEnvExtra{})

	if _, ok := lookupEnv(env, "ANTHROPIC_API_KEY"); ok {
		t.Error("ANTHROPIC_API_KEY survived non-bare scrub")
	}
	if _, ok := lookupEnv(env, "MYKEY"); ok {
		t.Error("api_key_env (MYKEY) survived non-bare scrub")
	}
}

// lookupEnv reports the value and presence of key in a KEY=VALUE slice.
func lookupEnv(env []string, key string) (string, bool) {
	prefix := key + "="
	for _, kv := range env {
		if len(kv) >= len(prefix) && kv[:len(prefix)] == prefix {
			return kv[len(prefix):], true
		}
	}
	return "", false
}
