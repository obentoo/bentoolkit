package github

import "testing"

// TestTokenFromEnv covers the shared env-token resolution: GITHUB_TOKEN takes
// precedence, GH_TOKEN is the fallback, surrounding whitespace is trimmed, and
// an empty environment yields "".
func TestTokenFromEnv(t *testing.T) {
	t.Run("none set", func(t *testing.T) {
		t.Setenv("GITHUB_TOKEN", "")
		t.Setenv("GH_TOKEN", "")
		if got := TokenFromEnv(); got != "" {
			t.Errorf("TokenFromEnv() = %q, want empty", got)
		}
	})

	t.Run("GH_TOKEN fallback", func(t *testing.T) {
		t.Setenv("GITHUB_TOKEN", "")
		t.Setenv("GH_TOKEN", "gho_fallback")
		if got := TokenFromEnv(); got != "gho_fallback" {
			t.Errorf("TokenFromEnv() = %q, want gho_fallback", got)
		}
	})

	t.Run("GITHUB_TOKEN precedence + trim", func(t *testing.T) {
		t.Setenv("GITHUB_TOKEN", "  ghp_primary  ")
		t.Setenv("GH_TOKEN", "gho_fallback")
		if got := TokenFromEnv(); got != "ghp_primary" {
			t.Errorf("TokenFromEnv() = %q, want ghp_primary", got)
		}
	})
}
