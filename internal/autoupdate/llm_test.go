package autoupdate

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/leanovate/gopter"
	"github.com/leanovate/gopter/gen"
	"github.com/leanovate/gopter/prop"
)

// =============================================================================
// Unit Tests
// =============================================================================

// TestNewLLMClientMissingProvider tests that NewLLMClient returns error when provider is empty
func TestNewLLMClientMissingProvider(t *testing.T) {
	cfg := LLMConfig{
		Provider:  "",
		APIKeyEnv: "TEST_API_KEY",
		Model:     "claude-3-haiku-20240307",
	}

	_, err := NewLLMClient(cfg)
	if err == nil {
		t.Error("Expected error for missing provider")
	}
	if err != ErrLLMNotConfigured {
		t.Errorf("Expected ErrLLMNotConfigured, got: %v", err)
	}
}

// TestNewLLMClientUnsupportedProvider tests that NewLLMClient returns error for unsupported provider
func TestNewLLMClientUnsupportedProvider(t *testing.T) {
	cfg := LLMConfig{
		Provider:  "openai",
		APIKeyEnv: "TEST_API_KEY",
		Model:     "gpt-4",
	}

	_, err := NewLLMClient(cfg)
	if err == nil {
		t.Error("Expected error for unsupported provider")
	}
}

// TestNewLLMClientMissingAPIKeyEnv tests that NewLLMClient returns error when api_key_env is empty
func TestNewLLMClientMissingAPIKeyEnv(t *testing.T) {
	cfg := LLMConfig{
		Provider:  "claude",
		APIKeyEnv: "",
		Model:     "claude-3-haiku-20240307",
	}

	_, err := NewLLMClient(cfg)
	if err == nil {
		t.Error("Expected error for missing api_key_env")
	}
}

// TestNewLLMClientMissingAPIKey tests that NewLLMClient returns error when API key env var is not set
func TestNewLLMClientMissingAPIKey(t *testing.T) {
	// Ensure the env var is not set
	os.Unsetenv("TEST_MISSING_API_KEY")

	cfg := LLMConfig{
		Provider:  "claude",
		APIKeyEnv: "TEST_MISSING_API_KEY",
		Model:     "claude-3-haiku-20240307",
	}

	_, err := NewLLMClient(cfg)
	if err == nil {
		t.Error("Expected error for missing API key")
	}
}

// TestNewLLMClientSuccess tests successful LLM client creation
func TestNewLLMClientSuccess(t *testing.T) {
	// Set up test API key
	os.Setenv("TEST_LLM_API_KEY", "test-key-12345")
	defer os.Unsetenv("TEST_LLM_API_KEY")

	cfg := LLMConfig{
		Provider:  "claude",
		APIKeyEnv: "TEST_LLM_API_KEY",
		Model:     "claude-3-haiku-20240307",
	}

	client, err := NewLLMClient(cfg)
	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}
	if client == nil {
		t.Fatal("Expected non-nil client")
	}
}

// TestNewLLMClientDefaultModel tests that default model is set when not specified
func TestNewLLMClientDefaultModel(t *testing.T) {
	os.Setenv("TEST_LLM_API_KEY", "test-key-12345")
	defer os.Unsetenv("TEST_LLM_API_KEY")

	cfg := LLMConfig{
		Provider:  "claude",
		APIKeyEnv: "TEST_LLM_API_KEY",
		Model:     "", // Empty model
	}

	// Use NewClaudeClient directly to test default model
	client, err := NewClaudeClient(cfg)
	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}
	if client.GetModel() != "claude-3-haiku-20240307" {
		t.Errorf("Expected default model 'claude-3-haiku-20240307', got %q", client.GetModel())
	}
}

// TestExtractVersionClaudeSuccess tests successful version extraction with mocked Claude API
func TestExtractVersionClaudeSuccess(t *testing.T) {
	// Create mock server
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Verify request method
		if r.Method != "POST" {
			t.Errorf("Expected POST, got %s", r.Method)
		}

		// Verify headers
		if r.Header.Get("Content-Type") != "application/json" {
			t.Errorf("Expected Content-Type application/json, got %s", r.Header.Get("Content-Type"))
		}
		if r.Header.Get("x-api-key") != "test-key-12345" {
			t.Errorf("Expected x-api-key test-key-12345, got %s", r.Header.Get("x-api-key"))
		}
		if r.Header.Get("anthropic-version") != "2023-06-01" {
			t.Errorf("Expected anthropic-version 2023-06-01, got %s", r.Header.Get("anthropic-version"))
		}

		// Return mock response
		resp := claudeResponse{
			ID:   "msg_test123",
			Type: "message",
			Role: "assistant",
			Content: []contentBlock{
				{Type: "text", Text: "11.81.1"},
			},
			Model:      "claude-3-haiku-20240307",
			StopReason: "end_turn",
			Usage:      claudeUsage{InputTokens: 100, OutputTokens: 10},
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	// Set up test API key
	os.Setenv("TEST_LLM_API_KEY", "test-key-12345")
	defer os.Unsetenv("TEST_LLM_API_KEY")

	cfg := LLMConfig{
		Provider:  "claude",
		APIKeyEnv: "TEST_LLM_API_KEY",
		Model:     "claude-3-haiku-20240307",
	}

	client, err := NewLLMClient(cfg)
	if err != nil {
		t.Fatalf("Failed to create client: %v", err)
	}

	// Override HTTP client to use mock server
	client.SetHTTPClient(&http.Client{
		Transport: &mockTransport{server: server},
	})

	content := []byte(`{"version": "11.81.1", "notes": [{"version": "11.81.1"}]}`)
	version, err := client.ExtractVersion(content, "Extract the version number")
	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}
	if version != "11.81.1" {
		t.Errorf("Expected version '11.81.1', got %q", version)
	}
}

// mockTransport redirects requests to the test server
type mockTransport struct {
	server *httptest.Server
}

func (t *mockTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	// Redirect to test server
	req.URL.Scheme = "http"
	req.URL.Host = t.server.Listener.Addr().String()
	return http.DefaultTransport.RoundTrip(req)
}

// TestExtractVersionClaudeAPIError tests handling of API errors
func TestExtractVersionClaudeAPIError(t *testing.T) {
	// Create mock server that returns an error
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		resp := claudeErrorResponse{
			Type: "error",
			Error: struct {
				Type    string `json:"type"`
				Message string `json:"message"`
			}{
				Type:    "authentication_error",
				Message: "Invalid API key",
			},
		}
		json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	os.Setenv("TEST_LLM_API_KEY", "invalid-key")
	defer os.Unsetenv("TEST_LLM_API_KEY")

	cfg := LLMConfig{
		Provider:  "claude",
		APIKeyEnv: "TEST_LLM_API_KEY",
		Model:     "claude-3-haiku-20240307",
	}

	client, err := NewLLMClient(cfg)
	if err != nil {
		t.Fatalf("Failed to create client: %v", err)
	}

	client.SetHTTPClient(&http.Client{
		Transport: &mockTransport{server: server},
	})

	_, err = client.ExtractVersion([]byte("test content"), "Extract version")
	if err == nil {
		t.Error("Expected error for API error response")
	}
}

// TestExtractVersionClaudeEmptyResponse tests handling of empty response
func TestExtractVersionClaudeEmptyResponse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := claudeResponse{
			ID:         "msg_test123",
			Type:       "message",
			Role:       "assistant",
			Content:    []contentBlock{}, // Empty content
			Model:      "claude-3-haiku-20240307",
			StopReason: "end_turn",
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	os.Setenv("TEST_LLM_API_KEY", "test-key")
	defer os.Unsetenv("TEST_LLM_API_KEY")

	cfg := LLMConfig{
		Provider:  "claude",
		APIKeyEnv: "TEST_LLM_API_KEY",
		Model:     "claude-3-haiku-20240307",
	}

	client, err := NewLLMClient(cfg)
	if err != nil {
		t.Fatalf("Failed to create client: %v", err)
	}

	client.SetHTTPClient(&http.Client{
		Transport: &mockTransport{server: server},
	})

	_, err = client.ExtractVersion([]byte("test content"), "Extract version")
	if err != ErrLLMEmptyResponse {
		t.Errorf("Expected ErrLLMEmptyResponse, got: %v", err)
	}
}

// TestExtractVersionClaudeNetworkError tests handling of network errors
func TestExtractVersionClaudeNetworkError(t *testing.T) {
	os.Setenv("TEST_LLM_API_KEY", "test-key")
	defer os.Unsetenv("TEST_LLM_API_KEY")

	cfg := LLMConfig{
		Provider:  "claude",
		APIKeyEnv: "TEST_LLM_API_KEY",
		Model:     "claude-3-haiku-20240307",
	}

	client, err := NewLLMClient(cfg)
	if err != nil {
		t.Fatalf("Failed to create client: %v", err)
	}

	// Use a transport that always fails
	client.SetHTTPClient(&http.Client{
		Transport: &failingTransport{},
	})

	_, err = client.ExtractVersion([]byte("test content"), "Extract version")
	if err == nil {
		t.Error("Expected error for network failure")
	}
}

// failingTransport always returns an error
type failingTransport struct{}

func (t *failingTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	return nil, &http.MaxBytesError{}
}

// TestCleanVersionString tests version string cleanup
func TestCleanVersionString(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"11.81.1", "11.81.1"},
		{"v11.81.1", "11.81.1"},
		{"V11.81.1", "11.81.1"},
		{" 11.81.1 ", "11.81.1"},
		{"\"11.81.1\"", "11.81.1"},
		{"'11.81.1'", "11.81.1"},
		{"`11.81.1`", "11.81.1"},
		{"11.81.1.", "11.81.1"},
		{"11.81.1,", "11.81.1"},
		{"  v11.81.1.  ", "11.81.1"},
	}

	for _, tc := range tests {
		result := cleanVersionString(tc.input)
		if result != tc.expected {
			t.Errorf("cleanVersionString(%q) = %q, expected %q", tc.input, result, tc.expected)
		}
	}
}

// TestBuildVersionExtractionPrompt tests prompt building
func TestBuildVersionExtractionPrompt(t *testing.T) {
	content := []byte(`{"version": "1.2.3"}`)
	userPrompt := "Extract the latest version"

	prompt := buildVersionExtractionPrompt(content, userPrompt)

	// Check that prompt contains expected elements
	if len(prompt) == 0 {
		t.Error("Expected non-empty prompt")
	}

	// Should contain the user prompt
	if !containsString(prompt, userPrompt) {
		t.Error("Prompt should contain user prompt")
	}

	// Should contain the content
	if !containsString(prompt, `{"version": "1.2.3"}`) {
		t.Error("Prompt should contain content")
	}

	// Should contain instructions for version-only response
	if !containsString(prompt, "ONLY the version number") {
		t.Error("Prompt should contain version-only instruction")
	}
}

// TestBuildVersionExtractionPromptTruncation tests content truncation
func TestBuildVersionExtractionPromptTruncation(t *testing.T) {
	// Create content larger than maxContentLen (4000)
	largeContent := make([]byte, 5000)
	for i := range largeContent {
		largeContent[i] = 'x'
	}

	prompt := buildVersionExtractionPrompt(largeContent, "")

	// Should contain truncation indicator
	if !containsString(prompt, "truncated") {
		t.Error("Prompt should indicate truncation for large content")
	}
}

// TestBuildVersionExtractionPromptEmptyUserPrompt tests prompt without user prompt
func TestBuildVersionExtractionPromptEmptyUserPrompt(t *testing.T) {
	content := []byte(`{"version": "1.2.3"}`)

	prompt := buildVersionExtractionPrompt(content, "")

	// Should not contain "Instructions:" when user prompt is empty
	if containsString(prompt, "Instructions:") {
		t.Error("Prompt should not contain Instructions when user prompt is empty")
	}
}

// TestExtractTextFromResponse tests text extraction from Claude response
func TestExtractTextFromResponse(t *testing.T) {
	tests := []struct {
		name     string
		resp     claudeResponse
		expected string
	}{
		{
			name: "single text block",
			resp: claudeResponse{
				Content: []contentBlock{
					{Type: "text", Text: "11.81.1"},
				},
			},
			expected: "11.81.1",
		},
		{
			name: "multiple blocks, first is text",
			resp: claudeResponse{
				Content: []contentBlock{
					{Type: "text", Text: "1.2.3"},
					{Type: "tool_use", Text: ""},
				},
			},
			expected: "1.2.3",
		},
		{
			name: "empty content",
			resp: claudeResponse{
				Content: []contentBlock{},
			},
			expected: "",
		},
		{
			name: "no text blocks",
			resp: claudeResponse{
				Content: []contentBlock{
					{Type: "tool_use", Text: ""},
				},
			},
			expected: "",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			result := extractTextFromResponse(tc.resp)
			if result != tc.expected {
				t.Errorf("extractTextFromResponse() = %q, expected %q", result, tc.expected)
			}
		})
	}
}

// TestExtractVersionRequestFormat tests that the request is properly formatted
func TestExtractVersionRequestFormat(t *testing.T) {
	var capturedRequest claudeRequest

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Capture the request body
		json.NewDecoder(r.Body).Decode(&capturedRequest)

		// Return success response
		resp := claudeResponse{
			ID:   "msg_test",
			Type: "message",
			Role: "assistant",
			Content: []contentBlock{
				{Type: "text", Text: "1.0.0"},
			},
			StopReason: "end_turn",
		}
		json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	os.Setenv("TEST_LLM_API_KEY", "test-key")
	defer os.Unsetenv("TEST_LLM_API_KEY")

	cfg := LLMConfig{
		Provider:  "claude",
		APIKeyEnv: "TEST_LLM_API_KEY",
		Model:     "claude-3-haiku-20240307",
	}

	client, err := NewLLMClient(cfg)
	if err != nil {
		t.Fatalf("Failed to create client: %v", err)
	}

	client.SetHTTPClient(&http.Client{
		Transport: &mockTransport{server: server},
	})

	client.ExtractVersion([]byte("test content"), "Extract version")

	// Verify request format
	if capturedRequest.Model != "claude-3-haiku-20240307" {
		t.Errorf("Expected model 'claude-3-haiku-20240307', got %q", capturedRequest.Model)
	}
	if capturedRequest.MaxTokens != 100 {
		t.Errorf("Expected max_tokens 100, got %d", capturedRequest.MaxTokens)
	}
	if len(capturedRequest.Messages) != 1 {
		t.Errorf("Expected 1 message, got %d", len(capturedRequest.Messages))
	}
	if capturedRequest.Messages[0].Role != "user" {
		t.Errorf("Expected role 'user', got %q", capturedRequest.Messages[0].Role)
	}
}

// TestExtractVersionWithVersionPrefix tests that version prefixes are cleaned
func TestExtractVersionWithVersionPrefix(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := claudeResponse{
			ID:   "msg_test",
			Type: "message",
			Role: "assistant",
			Content: []contentBlock{
				{Type: "text", Text: "v1.2.3"}, // With 'v' prefix
			},
			StopReason: "end_turn",
		}
		json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	os.Setenv("TEST_LLM_API_KEY", "test-key")
	defer os.Unsetenv("TEST_LLM_API_KEY")

	cfg := LLMConfig{
		Provider:  "claude",
		APIKeyEnv: "TEST_LLM_API_KEY",
		Model:     "claude-3-haiku-20240307",
	}

	client, err := NewLLMClient(cfg)
	if err != nil {
		t.Fatalf("Failed to create client: %v", err)
	}

	client.SetHTTPClient(&http.Client{
		Transport: &mockTransport{server: server},
	})

	version, err := client.ExtractVersion([]byte("test"), "")
	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}
	if version != "1.2.3" {
		t.Errorf("Expected version '1.2.3' (without prefix), got %q", version)
	}
}

// containsString checks if a string contains a substring
func containsString(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(substr) == 0 ||
		(len(s) > 0 && len(substr) > 0 && findSubstring(s, substr)))
}

func findSubstring(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}

// =============================================================================
// Property-Based Tests
// =============================================================================

// TestAPIKeyValidation tests Property 13: API Key Validation
// **Feature: autoupdate-analyzer, Property 13: API Key Validation**
// **Validates: Requirements 5.4**
//
// For any LLM provider (Claude, OpenAI) without the configured API key environment
// variable set, NewLLMClient SHALL return ErrLLMAPIKeyMissing. Ollama SHALL NOT
// require an API key.
func TestAPIKeyValidation(t *testing.T) {
	parameters := gopter.DefaultTestParameters()
	parameters.MinSuccessfulTests = 100
	properties := gopter.NewProperties(parameters)

	// Property: Claude without API key returns ErrLLMAPIKeyMissing
	properties.Property("Claude without API key returns ErrLLMAPIKeyMissing", prop.ForAll(
		func(envVarName string) bool {
			// Ensure the env var is not set
			os.Unsetenv(envVarName)

			cfg := LLMConfig{
				Provider:  "claude",
				APIKeyEnv: envVarName,
				Model:     "claude-3-haiku-20240307",
			}

			_, err := NewClaudeClient(cfg)
			if err == nil {
				return false
			}
			return strings.Contains(err.Error(), ErrLLMAPIKeyMissing.Error())
		},
		gen.OneConstOf(
			"TEST_CLAUDE_KEY_1",
			"TEST_CLAUDE_KEY_2",
			"TEST_CLAUDE_KEY_3",
			"ANTHROPIC_API_KEY_TEST",
		),
	))

	// Property: OpenAI without API key returns ErrLLMAPIKeyMissing
	properties.Property("OpenAI without API key returns ErrLLMAPIKeyMissing", prop.ForAll(
		func(envVarName string) bool {
			// Ensure the env var is not set
			os.Unsetenv(envVarName)

			cfg := LLMConfig{
				Provider:  "openai",
				APIKeyEnv: envVarName,
				Model:     "gpt-4o-mini",
			}

			_, err := NewOpenAIClient(cfg)
			if err == nil {
				return false
			}
			return strings.Contains(err.Error(), ErrLLMAPIKeyMissing.Error())
		},
		gen.OneConstOf(
			"TEST_OPENAI_KEY_1",
			"TEST_OPENAI_KEY_2",
			"TEST_OPENAI_KEY_3",
			"OPENAI_API_KEY_TEST",
		),
	))

	// Property: Ollama does NOT require an API key
	properties.Property("Ollama does NOT require an API key", prop.ForAll(
		func(model string) bool {
			cfg := LLMConfig{
				Provider: "ollama",
				Model:    model,
				BaseURL:  "http://localhost:11434",
			}

			client, err := NewOllamaClient(cfg)
			if err != nil {
				return false
			}
			return client != nil
		},
		gen.OneConstOf(
			"llama3",
			"llama2",
			"mistral",
			"codellama",
		),
	))

	// Property: Claude with valid API key succeeds
	properties.Property("Claude with valid API key succeeds", prop.ForAll(
		func(apiKey string) bool {
			envVarName := "TEST_CLAUDE_VALID_KEY"
			os.Setenv(envVarName, apiKey)
			defer os.Unsetenv(envVarName)

			cfg := LLMConfig{
				Provider:  "claude",
				APIKeyEnv: envVarName,
				Model:     "claude-3-haiku-20240307",
			}

			client, err := NewClaudeClient(cfg)
			if err != nil {
				return false
			}
			return client != nil
		},
		gen.OneConstOf(
			"sk-ant-api03-test-key-1",
			"sk-ant-api03-test-key-2",
			"test-api-key-12345",
		),
	))

	// Property: OpenAI with valid API key succeeds
	properties.Property("OpenAI with valid API key succeeds", prop.ForAll(
		func(apiKey string) bool {
			envVarName := "TEST_OPENAI_VALID_KEY"
			os.Setenv(envVarName, apiKey)
			defer os.Unsetenv(envVarName)

			cfg := LLMConfig{
				Provider:  "openai",
				APIKeyEnv: envVarName,
				Model:     "gpt-4o-mini",
			}

			client, err := NewOpenAIClient(cfg)
			if err != nil {
				return false
			}
			return client != nil
		},
		gen.OneConstOf(
			"sk-test-key-1",
			"sk-test-key-2",
			"test-api-key-12345",
		),
	))

	// Property: NewLLMProvider routes to correct provider
	properties.Property("NewLLMProvider routes to correct provider", prop.ForAll(
		func(provider string) bool {
			envVarName := "TEST_PROVIDER_KEY"
			os.Setenv(envVarName, "test-key")
			defer os.Unsetenv(envVarName)

			cfg := LLMConfig{
				Provider:  provider,
				APIKeyEnv: envVarName,
				Model:     "test-model",
				BaseURL:   "http://localhost:11434",
			}

			llmProvider, err := NewLLMProvider(cfg)
			if err != nil {
				return false
			}

			switch provider {
			case "claude":
				_, ok := llmProvider.(*ClaudeClient)
				return ok
			case "openai":
				_, ok := llmProvider.(*OpenAIClient)
				return ok
			case "ollama":
				_, ok := llmProvider.(*OllamaClient)
				return ok
			}
			return false
		},
		gen.OneConstOf("claude", "openai", "ollama"),
	))

	properties.TestingRun(t)
}

// TestModelConfiguration tests Property 14: Model Configuration
// **Feature: autoupdate-analyzer, Property 14: Model Configuration**
// **Validates: Requirements 5.6**
//
// For any LLM provider with a configured model name, all API requests SHALL use
// that model name.
func TestModelConfiguration(t *testing.T) {
	parameters := gopter.DefaultTestParameters()
	parameters.MinSuccessfulTests = 100
	properties := gopter.NewProperties(parameters)

	// Property: Claude client uses configured model name
	properties.Property("Claude client uses configured model name", prop.ForAll(
		func(modelName string) bool {
			envVarName := "TEST_CLAUDE_MODEL_KEY"
			os.Setenv(envVarName, "test-api-key")
			defer os.Unsetenv(envVarName)

			cfg := LLMConfig{
				Provider:  "claude",
				APIKeyEnv: envVarName,
				Model:     modelName,
			}

			client, err := NewClaudeClient(cfg)
			if err != nil {
				return false
			}

			return client.GetModel() == modelName
		},
		gen.OneConstOf(
			"claude-3-haiku-20240307",
			"claude-3-sonnet-20240229",
			"claude-3-opus-20240229",
			"claude-3-5-sonnet-20241022",
		),
	))

	// Property: OpenAI client uses configured model name
	properties.Property("OpenAI client uses configured model name", prop.ForAll(
		func(modelName string) bool {
			envVarName := "TEST_OPENAI_MODEL_KEY"
			os.Setenv(envVarName, "test-api-key")
			defer os.Unsetenv(envVarName)

			cfg := LLMConfig{
				Provider:  "openai",
				APIKeyEnv: envVarName,
				Model:     modelName,
			}

			client, err := NewOpenAIClient(cfg)
			if err != nil {
				return false
			}

			return client.GetModel() == modelName
		},
		gen.OneConstOf(
			"gpt-4o-mini",
			"gpt-4o",
			"gpt-4-turbo",
			"gpt-3.5-turbo",
		),
	))

	// Property: Ollama client uses configured model name
	properties.Property("Ollama client uses configured model name", prop.ForAll(
		func(modelName string) bool {
			cfg := LLMConfig{
				Provider: "ollama",
				Model:    modelName,
				BaseURL:  "http://localhost:11434",
			}

			client, err := NewOllamaClient(cfg)
			if err != nil {
				return false
			}

			return client.GetModel() == modelName
		},
		gen.OneConstOf(
			"llama3",
			"llama2",
			"mistral",
			"codellama",
			"phi",
		),
	))

	// Property: Claude uses default model when not specified
	properties.Property("Claude uses default model when not specified", prop.ForAll(
		func(apiKey string) bool {
			envVarName := "TEST_CLAUDE_DEFAULT_MODEL"
			os.Setenv(envVarName, apiKey)
			defer os.Unsetenv(envVarName)

			cfg := LLMConfig{
				Provider:  "claude",
				APIKeyEnv: envVarName,
				Model:     "", // Empty model
			}

			client, err := NewClaudeClient(cfg)
			if err != nil {
				return false
			}

			// Default model should be claude-3-haiku-20240307
			return client.GetModel() == "claude-3-haiku-20240307"
		},
		gen.OneConstOf(
			"test-key-1",
			"test-key-2",
			"sk-ant-api03-test",
		),
	))

	// Property: OpenAI uses default model when not specified
	properties.Property("OpenAI uses default model when not specified", prop.ForAll(
		func(apiKey string) bool {
			envVarName := "TEST_OPENAI_DEFAULT_MODEL"
			os.Setenv(envVarName, apiKey)
			defer os.Unsetenv(envVarName)

			cfg := LLMConfig{
				Provider:  "openai",
				APIKeyEnv: envVarName,
				Model:     "", // Empty model
			}

			client, err := NewOpenAIClient(cfg)
			if err != nil {
				return false
			}

			// Default model should be gpt-4o-mini
			return client.GetModel() == "gpt-4o-mini"
		},
		gen.OneConstOf(
			"test-key-1",
			"test-key-2",
			"sk-test-key",
		),
	))

	// Property: Ollama uses default model when not specified
	properties.Property("Ollama uses default model when not specified", prop.ForAll(
		func(baseURL string) bool {
			cfg := LLMConfig{
				Provider: "ollama",
				Model:    "", // Empty model
				BaseURL:  baseURL,
			}

			client, err := NewOllamaClient(cfg)
			if err != nil {
				return false
			}

			// Default model should be llama3
			return client.GetModel() == "llama3"
		},
		gen.OneConstOf(
			"http://localhost:11434",
			"http://127.0.0.1:11434",
			"http://ollama:11434",
		),
	))

	// Property: Claude API request uses configured model
	properties.Property("Claude API request uses configured model", prop.ForAll(
		func(modelName string) bool {
			var capturedModel string

			// Create mock server to capture the request
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var req claudeRequest
				json.NewDecoder(r.Body).Decode(&req)
				capturedModel = req.Model

				// Return success response
				resp := claudeResponse{
					ID:   "msg_test",
					Type: "message",
					Role: "assistant",
					Content: []contentBlock{
						{Type: "text", Text: "1.0.0"},
					},
					StopReason: "end_turn",
				}
				json.NewEncoder(w).Encode(resp)
			}))
			defer server.Close()

			envVarName := "TEST_CLAUDE_REQUEST_MODEL"
			os.Setenv(envVarName, "test-api-key")
			defer os.Unsetenv(envVarName)

			cfg := LLMConfig{
				Provider:  "claude",
				APIKeyEnv: envVarName,
				Model:     modelName,
			}

			client, err := NewClaudeClient(cfg)
			if err != nil {
				return false
			}

			client.SetHTTPClient(&http.Client{
				Transport: &mockTransport{server: server},
			})

			_, _ = client.ExtractVersion([]byte("test content"), "")

			return capturedModel == modelName
		},
		gen.OneConstOf(
			"claude-3-haiku-20240307",
			"claude-3-sonnet-20240229",
			"claude-3-opus-20240229",
		),
	))

	// Property: OpenAI API request uses configured model
	properties.Property("OpenAI API request uses configured model", prop.ForAll(
		func(modelName string) bool {
			var capturedModel string

			// Create mock server to capture the request
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var req openAIRequest
				json.NewDecoder(r.Body).Decode(&req)
				capturedModel = req.Model

				// Return success response
				resp := openAIResponse{
					ID:      "chatcmpl-test",
					Object:  "chat.completion",
					Created: 1234567890,
					Model:   modelName,
					Choices: []openAIChoice{
						{
							Index: 0,
							Message: openAIMessage{
								Role:    "assistant",
								Content: "1.0.0",
							},
							FinishReason: "stop",
						},
					},
				}
				json.NewEncoder(w).Encode(resp)
			}))
			defer server.Close()

			envVarName := "TEST_OPENAI_REQUEST_MODEL"
			os.Setenv(envVarName, "test-api-key")
			defer os.Unsetenv(envVarName)

			cfg := LLMConfig{
				Provider:  "openai",
				APIKeyEnv: envVarName,
				Model:     modelName,
				BaseURL:   server.URL,
			}

			client, err := NewOpenAIClient(cfg)
			if err != nil {
				return false
			}

			_, _ = client.ExtractVersion([]byte("test content"), "")

			return capturedModel == modelName
		},
		gen.OneConstOf(
			"gpt-4o-mini",
			"gpt-4o",
			"gpt-4-turbo",
		),
	))

	// Property: Ollama API request uses configured model
	properties.Property("Ollama API request uses configured model", prop.ForAll(
		func(modelName string) bool {
			var capturedModel string

			// Create mock server to capture the request
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var req ollamaRequest
				json.NewDecoder(r.Body).Decode(&req)
				capturedModel = req.Model

				// Return success response
				resp := ollamaResponse{
					Model:     modelName,
					CreatedAt: "2024-01-01T00:00:00Z",
					Response:  "1.0.0",
					Done:      true,
				}
				json.NewEncoder(w).Encode(resp)
			}))
			defer server.Close()

			cfg := LLMConfig{
				Provider: "ollama",
				Model:    modelName,
				BaseURL:  server.URL,
			}

			client, err := NewOllamaClient(cfg)
			if err != nil {
				return false
			}

			client.SetBaseURL(server.URL)

			_, _ = client.ExtractVersion([]byte("test content"), "")

			return capturedModel == modelName
		},
		gen.OneConstOf(
			"llama3",
			"llama2",
			"mistral",
			"codellama",
		),
	))

	properties.TestingRun(t)
}
