package autoupdate

import (
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
			if got := resolveBare(tc.cfg, tc.key); got != tc.want {
				t.Fatalf("resolveBare(%+v, %q) = %v, want %v", tc.cfg, tc.key, got, tc.want)
			}
		})
	}
}

// TestChildEnv_InjectsResolvedKey pins the critical single-resolution fix: in bare
// mode childEnv injects exactly the resolved key it is handed (never os.Getenv, so
// never an empty credential), and it uses that value even when the environment
// holds a different one under api_key_env.
func TestChildEnv_InjectsResolvedKey(t *testing.T) {
	t.Setenv("MYKEY", "env-value-should-be-ignored")

	env := childEnv(true, "MYKEY", "resolved-secret")

	if got := envValue(env, "ANTHROPIC_API_KEY"); got != "resolved-secret" {
		t.Fatalf("ANTHROPIC_API_KEY = %q, want resolved-secret (the passed key, not env)", got)
	}
}

// TestChildEnv_NonBareScrubs pins that non-bare mode strips every auth source —
// the canonical ANTHROPIC_API_KEY and the configured api_key_env — so an inherited
// key cannot override the operator's `bare: false` choice.
func TestChildEnv_NonBareScrubs(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "inherited")
	t.Setenv("MYKEY", "inherited")

	env := childEnv(false, "MYKEY", "resolved-secret")

	if _, ok := lookupEnv(env, "ANTHROPIC_API_KEY"); ok {
		t.Error("ANTHROPIC_API_KEY survived non-bare scrub")
	}
	if _, ok := lookupEnv(env, "MYKEY"); ok {
		t.Error("api_key_env (MYKEY) survived non-bare scrub")
	}
}

// envValue returns the value for key in a KEY=VALUE slice, or "" if absent.
func envValue(env []string, key string) string {
	v, _ := lookupEnv(env, key)
	return v
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
