package llm

import (
	"net/http"
	"testing"

	"github.com/obentoo/bentoolkit/internal/autoupdate/ebuilds"
)

// =============================================================================
// LLM client setters
// =============================================================================

// TestLLMClientSetBaseURL tests SetBaseURL is a no-op (does not panic)
func TestLLMClientSetBaseURL(t *testing.T) {
	t.Setenv("TEST_LLM_KEY2", "test-key")
	cfg := LLMConfig{Provider: "claude", APIKeyEnv: "TEST_LLM_KEY2", Model: "claude-3-haiku-20240307"}
	client, err := NewLLMClient(cfg)
	if err != nil {
		t.Fatalf("NewLLMClient: %v", err)
	}
	client.SetBaseURL("https://custom.example.com") // should not panic
}

// TestNewLLMClientWithHTTPClient tests NewLLMClientWithHTTPClient
func TestNewLLMClientWithHTTPClient(t *testing.T) {
	t.Setenv("TEST_LLM_KEY3", "test-key")
	cfg := LLMConfig{Provider: "claude", APIKeyEnv: "TEST_LLM_KEY3", Model: "claude-3-haiku-20240307"}
	httpClient := &http.Client{}

	client, err := NewLLMClientWithHTTPClient(cfg, httpClient)
	if err != nil {
		t.Fatalf("NewLLMClientWithHTTPClient: %v", err)
	}
	if client == nil {
		t.Error("Expected non-nil LLMClient")
	}
}

// TestOllamaSetHTTPClient tests OllamaClient.SetHTTPClient
func TestOllamaSetHTTPClient(t *testing.T) {
	client, err := NewOllamaClient(LLMConfig{Provider: "ollama", Model: "llama3"})
	if err != nil {
		t.Fatalf("NewOllamaClient: %v", err)
	}
	httpClient := &http.Client{}
	client.SetHTTPClient(httpClient)
	if client.httpClient != httpClient {
		t.Error("Expected httpClient to be set")
	}
}

// TestOpenAISetHTTPClient tests OpenAIClient.SetHTTPClient
func TestOpenAISetHTTPClient(t *testing.T) {
	t.Setenv("TEST_OPENAI_KEY", "test-key")
	client, err := NewOpenAIClient(LLMConfig{Provider: "openai", APIKeyEnv: "TEST_OPENAI_KEY", Model: "gpt-4o-mini"})
	if err != nil {
		t.Fatalf("NewOpenAIClient: %v", err)
	}
	httpClient := &http.Client{}
	client.SetHTTPClient(httpClient)
	if client.httpClient != httpClient {
		t.Error("Expected httpClient to be set")
	}
}

// TestOpenAISetBaseURL tests OpenAIClient.SetBaseURL
func TestOpenAISetBaseURL(t *testing.T) {
	t.Setenv("TEST_OPENAI_KEY2", "test-key")
	client, err := NewOpenAIClient(LLMConfig{Provider: "openai", APIKeyEnv: "TEST_OPENAI_KEY2", Model: "gpt-4o-mini"})
	if err != nil {
		t.Fatalf("NewOpenAIClient: %v", err)
	}
	client.SetBaseURL("https://custom.openai.example.com")
	if client.baseURL != "https://custom.openai.example.com" {
		t.Errorf("Expected baseURL to be set, got %q", client.baseURL)
	}
}

// =============================================================================
// LLM pure functions
// =============================================================================

// TestParseSchemaAnalysisValid tests parseSchemaAnalysis with valid JSON
func TestParseSchemaAnalysisValid(t *testing.T) {
	text := `Here is the analysis: {"parser_type":"json","path":"tag_name","confidence":0.9,"reasoning":"GitHub API"}`
	result, err := parseSchemaAnalysis(text)
	if err != nil {
		t.Fatalf("parseSchemaAnalysis: %v", err)
	}
	if result.ParserType != "json" {
		t.Errorf("Expected parser_type=json, got %q", result.ParserType)
	}
	if result.Path != "tag_name" {
		t.Errorf("Expected path=tag_name, got %q", result.Path)
	}
}

// TestParseSchemaAnalysisRegex tests parseSchemaAnalysis with regex parser
func TestParseSchemaAnalysisRegex(t *testing.T) {
	text := `{"parser_type":"regex","pattern":"v(\\d+\\.\\d+\\.\\d+)","confidence":0.8}`
	result, err := parseSchemaAnalysis(text)
	if err != nil {
		t.Fatalf("parseSchemaAnalysis: %v", err)
	}
	if result.ParserType != "regex" {
		t.Errorf("Expected parser_type=regex, got %q", result.ParserType)
	}
}

// TestParseSchemaAnalysisNoJSON tests parseSchemaAnalysis with no JSON
func TestParseSchemaAnalysisNoJSON(t *testing.T) {
	_, err := parseSchemaAnalysis("no json here at all")
	if err == nil {
		t.Error("Expected error for text with no JSON")
	}
}

// TestParseSchemaAnalysisInvalidJSON tests parseSchemaAnalysis with invalid JSON
func TestParseSchemaAnalysisInvalidJSON(t *testing.T) {
	_, err := parseSchemaAnalysis("{invalid json}")
	if err == nil {
		t.Error("Expected error for invalid JSON")
	}
}

// TestParseSchemaAnalysisFallbackConfigObject verifies that a fallback_config
// emitted as an object (observed from real LLM responses) no longer fails the
// whole parse: the primary schema is preserved and the offending field drops to "".
func TestParseSchemaAnalysisFallbackConfigObject(t *testing.T) {
	text := `{"parser_type":"json","path":"tag_name","fallback_config":{"path":"name"},"confidence":0.9}`
	result, err := parseSchemaAnalysis(text)
	if err != nil {
		t.Fatalf("parseSchemaAnalysis: %v", err)
	}
	if result.ParserType != "json" || result.Path != "tag_name" {
		t.Errorf("Expected primary schema preserved, got parser=%q path=%q", result.ParserType, result.Path)
	}
	if result.FallbackConfig != "" {
		t.Errorf("Expected fallback_config dropped to empty, got %q", result.FallbackConfig)
	}
}

// TestParseSchemaAnalysisConfidenceString verifies confidence emitted as a
// numeric string (e.g. "0.95") is coerced to float rather than failing.
func TestParseSchemaAnalysisConfidenceString(t *testing.T) {
	text := `{"parser_type":"json","path":"version","confidence":"0.95"}`
	result, err := parseSchemaAnalysis(text)
	if err != nil {
		t.Fatalf("parseSchemaAnalysis: %v", err)
	}
	if result.Confidence != 0.95 {
		t.Errorf("Expected confidence=0.95, got %v", result.Confidence)
	}
}

// TestParseSchemaAnalysisNullFields verifies null-valued optional string fields
// decode to "" instead of failing.
func TestParseSchemaAnalysisNullFields(t *testing.T) {
	text := `{"parser_type":"regex","pattern":"v([0-9.]+)","selector":null,"xpath":null,"confidence":0.8}`
	result, err := parseSchemaAnalysis(text)
	if err != nil {
		t.Fatalf("parseSchemaAnalysis: %v", err)
	}
	if result.ParserType != "regex" || result.Selector != "" || result.XPath != "" {
		t.Errorf("Expected null fields empty, got selector=%q xpath=%q", result.Selector, result.XPath)
	}
}

// TestBuildSchemaAnalysisPromptBasic tests buildSchemaAnalysisPrompt returns non-empty string
func TestBuildSchemaAnalysisPromptBasic(t *testing.T) {
	content := []byte(`{"version": "1.2.3"}`)
	prompt := buildSchemaAnalysisPrompt(content, nil, "")
	if len(prompt) == 0 {
		t.Error("Expected non-empty prompt")
	}
	if !containsStr(prompt, "parser_type") {
		t.Error("Expected prompt to contain 'parser_type'")
	}
}

// TestBuildSchemaAnalysisPromptWithMeta tests buildSchemaAnalysisPrompt with metadata
func TestBuildSchemaAnalysisPromptWithMeta(t *testing.T) {
	content := []byte(`{"tag_name": "v1.0.0"}`)
	meta := &ebuilds.EbuildMetadata{
		Package:  "app-misc/hello",
		Version:  "1.0.0",
		Homepage: "https://example.com",
	}
	prompt := buildSchemaAnalysisPrompt(content, meta, "look for tag_name")
	if !containsStr(prompt, "app-misc/hello") {
		t.Error("Expected prompt to contain package name")
	}
	if !containsStr(prompt, "look for tag_name") {
		t.Error("Expected prompt to contain hint")
	}
}

// TestBuildSchemaAnalysisPromptTruncates tests that long content is truncated
func TestBuildSchemaAnalysisPromptTruncates(t *testing.T) {
	// Create content longer than 4000 chars
	content := make([]byte, 5000)
	for i := range content {
		content[i] = 'x'
	}
	prompt := buildSchemaAnalysisPrompt(content, nil, "")
	if !containsStr(prompt, "truncated") {
		t.Error("Expected prompt to indicate truncation for long content")
	}
}

// containsStr is a helper to check substring
func containsStr(s, sub string) bool {
	return len(s) >= len(sub) && (s == sub || len(sub) == 0 ||
		func() bool {
			for i := 0; i <= len(s)-len(sub); i++ {
				if s[i:i+len(sub)] == sub {
					return true
				}
			}
			return false
		}())
}
