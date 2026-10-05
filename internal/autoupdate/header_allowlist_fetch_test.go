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

// Story 068, sub-task 3.1 — R3.1: a BENTOO_FETCH_ variable is an
// authenticated-fetch secret, never a header credential. Without the
// reservation a record could send the maintainer's exported serial to its own
// url host through ${BENTOO_FETCH_*} in an allow-listed header.
func TestHeaderExpansion_BentooFetchStaysLiteral(t *testing.T) {
	const secret = "s068-fetch-secret-must-not-reach-a-header"
	reserved := []string{"BENTOO_FETCH_X", "BENTOO_FETCH_FILEZILLA_PRO_KEY", "BENTOO_FETCH_A"}

	// Hostile first: SET, referenced from allow-listed headers, still literal
	// with exactly one denied-variable Warn naming it.
	for _, name := range reserved {
		for _, header := range []string{"Authorization", "X-Api-Key"} {
			t.Run(name+" in "+header+" stays literal", func(t *testing.T) {
				lc := captureWarnLogs(t)
				t.Setenv(name, secret)

				ref := "${" + name + "}"
				got := SubstituteEnvVars(lc.logger(), "Bearer "+ref, header)
				if got != "Bearer "+ref {
					t.Errorf("SubstituteEnvVars expanded %s in %s: got %q, want %q", name, header, got, "Bearer "+ref)
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
	}

	t.Run("isAllowedEnvVar refuses the BENTOO_FETCH_ prefix", func(t *testing.T) {
		for _, name := range append([]string{"BENTOO_FETCH_"}, reserved...) {
			if isAllowedEnvVar(name) {
				t.Errorf("isAllowedEnvVar(%q) = true, want false", name)
			}
		}
	})

	// A literal reference sends nothing, so it is not a credential to bind: not
	// refused, even toward a host outside the record's own (052's R1.8 rule).
	t.Run("a literal reference is not refused", func(t *testing.T) {
		scope := credentialScope{ownHosts: []string{"api.example.com"}}
		headers := map[string]string{"X-Api-Key": "${BENTOO_FETCH_X}"}
		if err := checkCredentialBinding("https://elsewhere.example/v", headers, scope); err != nil {
			t.Errorf("checkCredentialBinding refused a reserved (literal) reference: %v", err)
		}
	})

	t.Run("the upstream receives the literal reference", func(t *testing.T) {
		t.Setenv("BENTOO_FETCH_X", secret)
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
			map[string]string{"X-Api-Key": "${BENTOO_FETCH_X}"})
		if err != nil {
			t.Fatalf("request with a reserved (literal) reference was refused or failed: %v", err)
		}
		_ = resp.Body.Close()

		mu.Lock()
		defer mu.Unlock()
		if len(received) != 1 || received[0] != "${BENTOO_FETCH_X}" {
			t.Errorf("server received X-Api-Key %q, want exactly one request carrying the literal ${BENTOO_FETCH_X}", received)
		}
	})

	// Converse: near-misses are ordinary BENTOO_* variables and keep expanding.
	for _, name := range []string{"BENTOO_FETCHX", "BENTOO_MY_FETCH_X", "BENTOO_FETCH", "BENTOO_FETCHER_X"} {
		t.Run(name+" still expands", func(t *testing.T) {
			lc := captureWarnLogs(t)
			t.Setenv(name, "ordinary-value")
			if got := SubstituteEnvVars(lc.logger(), "Bearer ${"+name+"}", "Authorization"); got != "Bearer ordinary-value" {
				t.Errorf("got %q, want %q — %s is not a BENTOO_FETCH_ variable", got, "Bearer ordinary-value", name)
			}
			if c := lc.count(); c != 0 {
				t.Errorf("expected no Warn for an allowed expansion, got %d: %v", c, lc.all())
			}
		})
	}
}
