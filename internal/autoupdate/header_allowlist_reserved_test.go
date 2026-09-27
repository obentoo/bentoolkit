package autoupdate

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"
)

// Story 052, sub-task 5.1 — S052-R1.9, S052-R3.2: bentoolkit's own secrets are
// never expanded into a header. R1.4 sends a BENTOO_* variable to the host of
// the record's own url, and the record's author picks that url; without a
// reservation a packages.toml PR could name ${BENTOO_NTFY_TOKEN} and receive
// the maintainer's notification token at a host of its choosing.

// reservedBentooSecretNames are the names S052-R1.9 reserves. The repository
// token is given in the exact shape its owner derives it for a repository
// named "my-overlay": BENTOO_REPO_<NAME>_TOKEN with NAME upper-cased and every
// rune outside [A-Z0-9] replaced by '_' (internal/common/config.repoTokenEnvName
// and cmd/bentoo.repoTokenName — both unexported, so the shape is pinned here
// and the owners' source is pinned below).
var reservedBentooSecretNames = []string{
	"BENTOO_NTFY_TOKEN",
	"BENTOO_SMTP_PASSWORD",
	"BENTOO_REPO_MY_OVERLAY_TOKEN",
}

// TestSubstituteEnvVars_ReservedBentooSecretsStayLiteral asserts the hostile
// case first: each reserved name is SET, referenced from an allow-listed header,
// and must still come out literally with exactly one denied-variable Warn
// naming it. The converse — ordinary BENTOO_* names, including near-misses of
// the reserved shapes, still expand — follows, so an implementation that
// stopped expanding BENTOO_* altogether would not pass.
func TestSubstituteEnvVars_ReservedBentooSecretsStayLiteral(t *testing.T) {
	const secret = "bentoolkit-own-secret-must-not-leave"

	for _, name := range reservedBentooSecretNames {
		t.Run(name+" set in the environment passes through literally", func(t *testing.T) {
			lc := captureWarnLogs(t)
			t.Setenv(name, secret)

			ref := "${" + name + "}"
			got := SubstituteEnvVars("Bearer "+ref, "Authorization")

			if got != "Bearer "+ref {
				t.Errorf("SubstituteEnvVars expanded %s: got %q, want the literal %q", name, got, "Bearer "+ref)
			}
			if strings.Contains(got, secret) {
				t.Errorf("the %s value reached the header: %q", name, got)
			}
			lines := lc.all()
			if len(lines) != 1 {
				t.Fatalf("expected exactly 1 Warn line, got %d: %v", len(lines), lines)
			}
			if !strings.Contains(lines[0], "is not allow-listed for header") || !strings.Contains(lines[0], name) {
				t.Errorf("Warn line %q should be the denied-variable Warn naming %s", lines[0], name)
			}
		})
	}

	// A reserved reference expands to nothing, so no credential leaves the
	// process and there is nothing to bind: like a non-allow-listed variable
	// (S052-R1.8) it is not refused, even toward a host outside the record's own.
	for _, name := range reservedBentooSecretNames {
		t.Run(name+" is not a credential reference, so it is not refused", func(t *testing.T) {
			scope := credentialScope{ownHosts: []string{"api.example.com"}}
			headers := map[string]string{"X-Api-Key": "${" + name + "}"}
			if err := checkCredentialBinding("https://elsewhere.example/v", headers, scope); err != nil {
				t.Errorf("checkCredentialBinding refused a reserved (literal) reference to %s: %v", name, err)
			}
		})
	}

	// Hostile converses: names that look like the reserved ones but are not.
	// Each is an ordinary BENTOO_* variable and must keep expanding.
	for _, name := range []string{
		"BENTOO_REPO_TOKEN",    // no repository name between the prefix and the suffix
		"BENTOO_REPO_X_TOKENS", // suffix is not _TOKEN
		"BENTOO_REPO_X",        // no _TOKEN suffix at all
		"BENTOO_MY_TOKEN",      // the README's own example name
		"BENTOO_NTFY_TOKENS",   // near-miss of BENTOO_NTFY_TOKEN
		"BENTOO_SMTP_PASSWORDS",
	} {
		t.Run(name+" still expands", func(t *testing.T) {
			lc := captureWarnLogs(t)
			t.Setenv(name, "ordinary-value")

			got := SubstituteEnvVars("Bearer ${"+name+"}", "Authorization")
			if got != "Bearer ordinary-value" {
				t.Errorf("got %q, want %q — %s is not one of bentoolkit's own secrets", got, "Bearer ordinary-value", name)
			}
			if c := lc.count(); c != 0 {
				t.Errorf("expected no Warn for an allowed expansion, got %d: %v", c, lc.all())
			}
		})
	}

	// Round trip: what the upstream actually RECEIVES. The request is sent to
	// its own host (unscoped, S052-R1.5 — so R1.4 alone would let it through)
	// and must carry the unexpanded text, never the secret.
	for _, name := range reservedBentooSecretNames {
		t.Run(name+": the upstream receives the literal reference", func(t *testing.T) {
			t.Setenv(name, secret)

			var mu sync.Mutex
			var received []string
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				mu.Lock()
				received = append(received, r.Header.Get("X-Api-Key"))
				mu.Unlock()
				_, _ = w.Write([]byte("1.0.0"))
			}))
			defer srv.Close()

			client := NewRetryableHTTPClient()
			client.SetDelayFunc(func(time.Duration) {})
			resp, err := client.GetWithHeadersContext(context.Background(), srv.URL,
				map[string]string{"X-Api-Key": "${" + name + "}"})
			if err != nil {
				t.Fatalf("request with a reserved (literal) reference was refused or failed: %v", err)
			}
			_ = resp.Body.Close()

			mu.Lock()
			defer mu.Unlock()
			if len(received) != 1 {
				t.Fatalf("server received %d requests, want 1", len(received))
			}
			if received[0] != "${"+name+"}" {
				t.Errorf("server received X-Api-Key %q, want the literal %q", received[0], "${"+name+"}")
			}
		})
	}
}

// TestIsAllowedEnvVar_ReservedBentooSecrets pins the allow-list decision for
// the reserved names (S052-R1.9) and the rest of the BENTOO_ prefix (S052-R3.2).
func TestIsAllowedEnvVar_ReservedBentooSecrets(t *testing.T) {
	testCases := []struct {
		name  string
		input string
		want  bool
	}{
		// Hostile first: bentoolkit's own secrets carry the BENTOO_ prefix and
		// must nonetheless be refused.
		{"notification token", "BENTOO_NTFY_TOKEN", false},
		{"smtp password", "BENTOO_SMTP_PASSWORD", false},
		{"repo token for my-overlay (repoTokenEnvName shape)", "BENTOO_REPO_MY_OVERLAY_TOKEN", false},
		{"repo token for a one-letter repo", "BENTOO_REPO_X_TOKEN", false},
		// A repository named "api" owns BENTOO_REPO_API_TOKEN; a record that
		// picks the same spelling for "its own" variable is asking for that
		// repository's token, so the shape alone decides.
		{"repo token whose name looks like a record's own variable", "BENTOO_REPO_API_TOKEN", false},
		// A repository named "token" derives BENTOO_REPO_TOKEN_TOKEN.
		{"repo token for a repo named token", "BENTOO_REPO_TOKEN_TOKEN", false},
		// A repository whose name is all punctuation derives underscores only.
		{"repo token for a repo named '-'", "BENTOO_REPO___TOKEN", false},

		// Converse: near-misses of the reserved shapes stay ordinary BENTOO_*.
		{"no repository name", "BENTOO_REPO_TOKEN", true},
		{"plural suffix", "BENTOO_REPO_X_TOKENS", true},
		{"no suffix", "BENTOO_REPO_X", true},
		{"repo in the middle, not the prefix", "BENTOO_MY_REPO_X_TOKEN", true},
		{"ordinary token", "BENTOO_MY_TOKEN", true},
		{"ntfy near-miss", "BENTOO_NTFY_TOKENS", true},
		{"smtp near-miss", "BENTOO_SMTP_PASSWORDS", true},
		{"migration name", "BENTOO_OPENAI_API_KEY", true},
		{"vendor token github", "GITHUB_TOKEN", true},
		{"vendor token gitlab", "GITLAB_TOKEN", true},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			if got := isAllowedEnvVar(tc.input); got != tc.want {
				t.Errorf("isAllowedEnvVar(%q) = %v, want %v", tc.input, got, tc.want)
			}
		})
	}

	// The reserved names are constants in this package (importing config or
	// snapshot would create a cycle), so pin them to their owners' source: if an
	// owner renames its secret, the reservation above no longer covers it.
	t.Run("the reserved names match their owners", func(t *testing.T) {
		owners := []struct {
			file string
			want *regexp.Regexp
		}{
			{filepath.Join("..", "snapshot", "notify.go"), regexp.MustCompile(`"BENTOO_NTFY_TOKEN"`)},
			{filepath.Join("..", "snapshot", "notify.go"), regexp.MustCompile(`"BENTOO_SMTP_PASSWORD"`)},
			{filepath.Join("..", "common", "config", "config.go"), regexp.MustCompile(`"BENTOO_REPO_"\s*\+[^\n]*\+\s*"_TOKEN"`)},
			{filepath.Join("..", "..", "cmd", "bentoo", "overlay_compare.go"), regexp.MustCompile(`"BENTOO_REPO_"\s*\+[^\n]*\+\s*"_TOKEN"`)},
		}
		for _, o := range owners {
			src, err := os.ReadFile(o.file) //nolint:gosec // fixed, test-local source path
			if err != nil {
				t.Fatalf("reading owner %s: %v", o.file, err)
			}
			if !o.want.Match(src) {
				t.Errorf("owner %s no longer contains %s — update the reserved names (S052-R1.9)", o.file, o.want)
			}
		}
	})
}
