package autoupdate

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
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

	version, err := client.ExtractVersion([]byte("some content"), "")
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

	_, err = client.ExtractVersion([]byte("some content"), "")
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

	_, err = client.ExtractVersion([]byte("some content"), "")
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
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(2 * time.Second)
		json.NewEncoder(w).Encode(ollamaResponse{Response: "1.2.3", Done: true})
	}))
	defer server.Close()

	client, err := NewOllamaClient(LLMConfig{Model: "llama3"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	client.SetBaseURL(server.URL)
	client.SetHTTPClient(&http.Client{Timeout: 50 * time.Millisecond})

	_, err = client.ExtractVersion([]byte("some content"), "")
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

	_, err = client.ExtractVersion([]byte("some content"), "")
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !errors.Is(err, ErrLLMEmptyResponse) {
		t.Errorf("expected ErrLLMEmptyResponse, got: %v", err)
	}
}
