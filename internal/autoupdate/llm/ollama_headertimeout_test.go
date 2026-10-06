package llm

import (
	"net/http"
	"testing"
	"time"
)

// TestOllamaHeaderTimeoutCoversInference pins R6.4: a non-streaming local
// inference sends no headers until it finishes, so the Ollama client's header
// wait equals its own 120 s client timeout — not the 30 s transport default,
// and not unbounded.
func TestOllamaHeaderTimeoutCoversInference(t *testing.T) {
	c, err := NewOllamaClient(LLMConfig{Model: "llama3"})
	if err != nil {
		t.Fatalf("NewOllamaClient: %v", err)
	}
	if got := c.httpClient.Timeout; got != 120*time.Second {
		t.Fatalf("fixture: Ollama client timeout = %v, want 120s", got)
	}
	tr, ok := c.httpClient.Transport.(*http.Transport)
	if !ok {
		t.Fatalf("Ollama transport is %T, want *http.Transport", c.httpClient.Transport)
	}
	if tr.ResponseHeaderTimeout != c.httpClient.Timeout {
		t.Errorf("Ollama ResponseHeaderTimeout = %v, want %v (its client timeout)", tr.ResponseHeaderTimeout, c.httpClient.Timeout)
	}
}
