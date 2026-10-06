package autoupdate

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/obentoo/bentoolkit/internal/autoupdate/fetch"
	"github.com/obentoo/bentoolkit/internal/common/httpx"
)

func TestOllamaExtractVersionSuccess(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(ollamaResponse{Response: "1.2.3", Done: true})
	}))
	defer server.Close()

	client, err := NewOllamaClient(LLMConfig{Model: "llama3"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	client.SetBaseURL(server.URL)

	version, err := client.ExtractVersion(t.Context(), []byte("some content"), "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if version != "1.2.3" {
		t.Errorf("expected version %q, got %q", "1.2.3", version)
	}
}

func TestOllamaExtractVersionHTTP500(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		json.NewEncoder(w).Encode(ollamaErrorResponse{Error: "internal error"})
	}))
	defer server.Close()

	client, err := NewOllamaClient(LLMConfig{Model: "llama3"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	client.SetBaseURL(server.URL)

	_, err = client.ExtractVersion(t.Context(), []byte("some content"), "")
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !errors.Is(err, ErrLLMRequestFailed) {
		t.Errorf("expected ErrLLMRequestFailed, got: %v", err)
	}
}

func TestOllamaExtractVersionMalformedJSON(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("not-json{{{"))
	}))
	defer server.Close()

	client, err := NewOllamaClient(LLMConfig{Model: "llama3"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	client.SetBaseURL(server.URL)

	_, err = client.ExtractVersion(t.Context(), []byte("some content"), "")
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if err.Error() == "" {
		t.Error("expected non-empty error message")
	}
	// Should contain "failed to parse response"
	found := false
	msg := err.Error()
	for i := 0; i <= len(msg)-len("failed to parse response"); i++ {
		if msg[i:i+len("failed to parse response")] == "failed to parse response" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected error to contain 'failed to parse response', got: %v", err)
	}
}

func TestOllamaExtractVersionContextCancellation(t *testing.T) {
	release := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select { // held until the client gives up, or the test releases it
		case <-r.Context().Done():
		case <-release:
		}
		json.NewEncoder(w).Encode(ollamaResponse{Response: "1.2.3", Done: true})
	}))
	// Cleanups run last-in first-out: release the handler, then close the server.
	t.Cleanup(server.Close)
	t.Cleanup(func() { close(release) })

	client, err := NewOllamaClient(LLMConfig{Model: "llama3"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	client.SetBaseURL(server.URL)
	client.SetHTTPClient(&http.Client{Timeout: 50 * time.Millisecond})

	_, err = client.ExtractVersion(t.Context(), []byte("some content"), "")
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !errors.Is(err, ErrOllamaConnectionFailed) {
		t.Errorf("expected ErrOllamaConnectionFailed, got: %v", err)
	}
}

func TestOllamaExtractVersionEmptyResponse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(ollamaResponse{Response: "", Done: true})
	}))
	defer server.Close()

	client, err := NewOllamaClient(LLMConfig{Model: "llama3"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	client.SetBaseURL(server.URL)

	_, err = client.ExtractVersion(t.Context(), []byte("some content"), "")
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !errors.Is(err, ErrLLMEmptyResponse) {
		t.Errorf("expected ErrLLMEmptyResponse, got: %v", err)
	}
}

// TestOllamaClient_WithCustomMaxBody verifies that WithMaxBodyBytes lowers the
// Ollama response-body cap and that exceeding it surfaces ErrResponseTooLarge.
// It also asserts the default (no option) equals httpx.MaxBodyBytes (R11.2).
func TestOllamaClient_WithCustomMaxBody(t *testing.T) {
	// Default cap (no option) must equal httpx.MaxBodyBytes.
	defaultClient, err := NewOllamaClient(LLMConfig{Model: "llama3"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if defaultClient.maxBodyBytes != httpx.MaxBodyBytes {
		t.Errorf("default maxBodyBytes = %d, want %d (httpx.MaxBodyBytes)",
			defaultClient.maxBodyBytes, httpx.MaxBodyBytes)
	}

	const limit = 1024 // 1 KiB cap for the test
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		writeOversizedBody(w, limit*4) // 4 KiB > 1 KiB cap
	}))
	defer server.Close()

	client, err := NewOllamaClient(LLMConfig{Model: "llama3"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	client.SetBaseURL(server.URL)
	client.WithMaxBodyBytes(limit)
	if client.maxBodyBytes != limit {
		t.Fatalf("WithMaxBodyBytes(%d) did not apply: maxBodyBytes = %d", limit, client.maxBodyBytes)
	}

	_, err = client.ExtractVersion(t.Context(), []byte("some content"), "")
	if err == nil {
		t.Fatal("expected an error for an oversized response body, got nil")
	}
	if !errors.Is(err, fetch.ErrResponseTooLarge) {
		t.Errorf("expected ErrResponseTooLarge, got: %v", err)
	}
}
