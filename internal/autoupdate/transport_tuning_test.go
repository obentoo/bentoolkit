package autoupdate

import (
	"net/http"
	"testing"

	"github.com/obentoo/bentoolkit/internal/common/httputil"
)

// httpClientConstructor names a default-client constructor under test and
// returns the *http.Client it builds. Constructors that need environment
// variables (LLM API keys) set them via t.Setenv before construction.
//
// Note: T13 splits the reflective transport-tuning tests per package because
// internal/autoupdate and internal/common/provider do not import one another,
// so no single in-package test can reach all six constructors. This file
// covers the four autoupdate constructors (RetryableHTTPClient + the three LLM
// clients); the GitHub/GitLab provider constructors are asserted by the twin
// test in internal/common/provider (design IP-3).
type httpClientConstructor struct {
	name  string
	build func(t *testing.T) *http.Client
}

// autoupdateHTTPClientConstructors is the explicit list of every autoupdate
// constructor that builds a default *http.Client. Each entry exercises the
// default-client construction site only (never an injected client).
func autoupdateHTTPClientConstructors() []httpClientConstructor {
	return []httpClientConstructor{
		{
			name: "NewRetryableHTTPClientWithConfig",
			build: func(t *testing.T) *http.Client {
				t.Helper()
				return NewRetryableHTTPClientWithConfig(DefaultRetryConfig()).client
			},
		},
		{
			name: "NewClaudeClient",
			build: func(t *testing.T) *http.Client {
				t.Helper()
				t.Setenv("BENTOO_T13_CLAUDE_KEY", "test-key")
				c, err := NewClaudeClient(LLMConfig{
					Provider:  "claude",
					APIKeyEnv: "BENTOO_T13_CLAUDE_KEY",
				})
				if err != nil {
					t.Fatalf("NewClaudeClient: %v", err)
				}
				return c.httpClient
			},
		},
		{
			name: "NewOpenAIClient",
			build: func(t *testing.T) *http.Client {
				t.Helper()
				t.Setenv("BENTOO_T13_OPENAI_KEY", "test-key")
				c, err := NewOpenAIClient(LLMConfig{
					Provider:  "openai",
					APIKeyEnv: "BENTOO_T13_OPENAI_KEY",
				})
				if err != nil {
					t.Fatalf("NewOpenAIClient: %v", err)
				}
				return c.httpClient
			},
		},
		{
			name: "NewOllamaClient",
			build: func(t *testing.T) *http.Client {
				t.Helper()
				c, err := NewOllamaClient(LLMConfig{Provider: "ollama"})
				if err != nil {
					t.Fatalf("NewOllamaClient: %v", err)
				}
				return c.httpClient
			},
		},
	}
}

// assertTunedTransport fails t unless got is a non-nil *http.Transport whose
// connection-pool and timeout fields match the want transport produced by
// httputil.BuildTransport().
func assertTunedTransport(t *testing.T, name string, got http.RoundTripper, want *http.Transport) {
	t.Helper()

	tr, ok := got.(*http.Transport)
	if !ok {
		t.Fatalf("%s: Transport is %T, want *http.Transport", name, got)
	}
	if tr == nil {
		t.Fatalf("%s: Transport is a nil *http.Transport", name)
	}

	if tr.MaxIdleConnsPerHost != want.MaxIdleConnsPerHost {
		t.Errorf("%s: MaxIdleConnsPerHost = %d, want %d", name, tr.MaxIdleConnsPerHost, want.MaxIdleConnsPerHost)
	}
	if tr.MaxConnsPerHost != want.MaxConnsPerHost {
		t.Errorf("%s: MaxConnsPerHost = %d, want %d", name, tr.MaxConnsPerHost, want.MaxConnsPerHost)
	}
	if tr.IdleConnTimeout != want.IdleConnTimeout {
		t.Errorf("%s: IdleConnTimeout = %v, want %v", name, tr.IdleConnTimeout, want.IdleConnTimeout)
	}
	if tr.TLSHandshakeTimeout != want.TLSHandshakeTimeout {
		t.Errorf("%s: TLSHandshakeTimeout = %v, want %v", name, tr.TLSHandshakeTimeout, want.TLSHandshakeTimeout)
	}
	if tr.ExpectContinueTimeout != want.ExpectContinueTimeout {
		t.Errorf("%s: ExpectContinueTimeout = %v, want %v", name, tr.ExpectContinueTimeout, want.ExpectContinueTimeout)
	}
	if tr.ForceAttemptHTTP2 != want.ForceAttemptHTTP2 {
		t.Errorf("%s: ForceAttemptHTTP2 = %v, want %v", name, tr.ForceAttemptHTTP2, want.ForceAttemptHTTP2)
	}
}

// TestAllHTTPClients_UseTunedTransport asserts that every autoupdate HTTP-client
// constructor wires the tuned transport from httputil.BuildTransport() into its
// default *http.Client (R6.1, R6.3, design IP-3).
func TestAllHTTPClients_UseTunedTransport(t *testing.T) {
	want := httputil.BuildTransport()

	for _, ctor := range autoupdateHTTPClientConstructors() {
		t.Run(ctor.name, func(t *testing.T) {
			client := ctor.build(t)
			if client == nil {
				t.Fatalf("%s: built a nil *http.Client", ctor.name)
			}
			if client.Transport == nil {
				t.Fatalf("%s: default client has a nil Transport", ctor.name)
			}
			assertTunedTransport(t, ctor.name, client.Transport, want)
		})
	}
}

// TestAllHTTPClients_HTTP2OptOut asserts that with BENTOO_DISABLE_HTTP2=1 every
// reconstructed autoupdate client gets a Transport with HTTP/2 disabled:
// ForceAttemptHTTP2 == false and an empty (non-nil) TLSNextProto map (R6.2).
func TestAllHTTPClients_HTTP2OptOut(t *testing.T) {
	t.Setenv(httputil.EnvDisableHTTP2, "1")

	for _, ctor := range autoupdateHTTPClientConstructors() {
		t.Run(ctor.name, func(t *testing.T) {
			client := ctor.build(t)
			if client == nil {
				t.Fatalf("%s: built a nil *http.Client", ctor.name)
			}
			tr, ok := client.Transport.(*http.Transport)
			if !ok {
				t.Fatalf("%s: Transport is %T, want *http.Transport", ctor.name, client.Transport)
			}
			if tr.ForceAttemptHTTP2 {
				t.Errorf("%s: ForceAttemptHTTP2 = true, want false when %s=1", ctor.name, httputil.EnvDisableHTTP2)
			}
			if tr.TLSNextProto == nil {
				t.Errorf("%s: TLSNextProto is nil, want non-nil empty map when %s=1", ctor.name, httputil.EnvDisableHTTP2)
			}
			if len(tr.TLSNextProto) != 0 {
				t.Errorf("%s: TLSNextProto has %d entries, want 0 when %s=1", ctor.name, len(tr.TLSNextProto), httputil.EnvDisableHTTP2)
			}
		})
	}
}
