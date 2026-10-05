package autoupdate

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// Story 052, sub-task 1.1 — S052-R3.1, S052-R3.2: the two LLM keys are no
// longer expandable in a header. A packages.toml record comes from the overlay
// repository, so a contributor could otherwise pair an upstream URL with
// ${ANTHROPIC_API_KEY} and receive the maintainer's key.

// TestSubstituteEnvVars_LLMKeysDenied asserts the hostile case first: each LLM
// key is SET, referenced from an allow-listed header, and must still come out
// literally with the denied-variable Warn. The converse — the migration name
// BENTOO_* still expands — follows, so an implementation that stopped
// expanding everything would not pass.
func TestSubstituteEnvVars_LLMKeysDenied(t *testing.T) {
	for _, name := range []string{"OPENAI_API_KEY", "ANTHROPIC_API_KEY"} {
		t.Run(name+" set in the environment passes through literally", func(t *testing.T) {
			lc := captureWarnLogs(t)
			t.Setenv(name, "sk-must-not-leave-the-process")

			ref := "${" + name + "}"
			got := SubstituteEnvVars(lc.logger(), "Bearer "+ref, "Authorization")

			if got != "Bearer "+ref {
				t.Errorf("SubstituteEnvVars expanded %s: got %q, want the literal %q", name, got, "Bearer "+ref)
			}
			if strings.Contains(got, "sk-must-not-leave-the-process") {
				t.Errorf("the %s value reached the header: %q", name, got)
			}
			lines := lc.all()
			if len(lines) != 1 {
				t.Fatalf("expected exactly 1 Warn line, got %d: %v", len(lines), lines)
			}
			if !strings.Contains(lines[0], "is not allow-listed for header") || !strings.Contains(lines[0], name) {
				t.Errorf("Warn line %q should be the denied-variable Warn naming %s", lines[0], name)
			}
			if isAllowedEnvVar(name) {
				t.Errorf("isAllowedEnvVar(%q) = true, want false", name)
			}
		})
	}

	t.Run("the allow-list is exactly GITHUB_TOKEN and GITLAB_TOKEN by name", func(t *testing.T) {
		if len(allowedHeaderEnvAllowList) != 2 {
			t.Errorf("allowedHeaderEnvAllowList has %d entries, want exactly 2: %v",
				len(allowedHeaderEnvAllowList), allowedHeaderEnvAllowList)
		}
		for _, name := range []string{"GITHUB_TOKEN", "GITLAB_TOKEN"} {
			if !isAllowedEnvVar(name) {
				t.Errorf("isAllowedEnvVar(%q) = false, want true", name)
			}
		}
		if allowedHeaderEnvPrefix != "BENTOO_" {
			t.Errorf("allowedHeaderEnvPrefix = %q, want %q", allowedHeaderEnvPrefix, "BENTOO_")
		}
	})

	t.Run("the migration name BENTOO_OPENAI_API_KEY still expands", func(t *testing.T) {
		lc := captureWarnLogs(t)
		t.Setenv("BENTOO_OPENAI_API_KEY", "renamed-value")

		got := SubstituteEnvVars(lc.logger(), "Bearer ${BENTOO_OPENAI_API_KEY}", "Authorization")
		if got != "Bearer renamed-value" {
			t.Errorf("got %q, want %q — the rename to BENTOO_* is the documented migration", got, "Bearer renamed-value")
		}
		if c := lc.count(); c != 0 {
			t.Errorf("expected no Warn for an allowed expansion, got %d: %v", c, lc.all())
		}
	})

	// Round trip: what the upstream actually RECEIVES. The request is sent (a
	// literal reference is not a credential, so nothing is refused) and carries
	// the unexpanded text.
	t.Run("the upstream receives the literal reference and the request is not refused", func(t *testing.T) {
		t.Setenv("ANTHROPIC_API_KEY", "sk-ant-secret")

		var mu sync.Mutex
		var received []string
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			mu.Lock()
			received = append(received, r.Header.Get("Authorization"))
			mu.Unlock()
			_, _ = w.Write([]byte("1.0.0"))
		}))
		defer srv.Close()

		client := NewRetryableHTTPClient()
		client.SetDelayFunc(func(time.Duration) {})
		resp, err := client.GetWithHeadersContext(context.Background(), srv.URL,
			map[string]string{"Authorization": "Bearer ${ANTHROPIC_API_KEY}"})
		if err != nil {
			t.Fatalf("request with a literal LLM-key reference was refused or failed: %v", err)
		}
		_ = resp.Body.Close()

		mu.Lock()
		defer mu.Unlock()
		if len(received) != 1 {
			t.Fatalf("server received %d requests, want 1", len(received))
		}
		if received[0] != "Bearer ${ANTHROPIC_API_KEY}" {
			t.Errorf("server received Authorization %q, want the literal %q", received[0], "Bearer ${ANTHROPIC_API_KEY}")
		}
	})
}
