// Package autoupdate provides OpenAI LLM integration for version extraction and schema analysis.
package autoupdate

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"os"

	"github.com/obentoo/bentoolkit/internal/common/httputil"
)

const (
	// DefaultOpenAIEndpoint is the default OpenAI API base URL.
	DefaultOpenAIEndpoint = "https://api.openai.com/v1"
	// DefaultOpenAIModel is the default OpenAI model.
	DefaultOpenAIModel = "gpt-4o-mini"
)

// OpenAIClient implements LLMProvider for OpenAI's API.
type OpenAIClient struct {
	config     LLMConfig
	httpClient *http.Client
	apiKey     string
	baseURL    string
	// maxBodyBytes caps how many bytes are read from an API response body.
	// It defaults to httputil.MaxBodyBytes and can be overridden via
	// WithMaxBodyBytes (R11.2).
	maxBodyBytes int64
}

// openAIRequest represents the request body for OpenAI Chat Completions API
type openAIRequest struct {
	Model       string          `json:"model"`
	Messages    []openAIMessage `json:"messages"`
	MaxTokens   int             `json:"max_tokens,omitempty"`
	Temperature float64         `json:"temperature,omitempty"`
}

// openAIMessage represents a message in the OpenAI conversation
type openAIMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// openAIResponse represents the response from OpenAI Chat Completions API
type openAIResponse struct {
	ID      string         `json:"id"`
	Object  string         `json:"object"`
	Created int64          `json:"created"`
	Model   string         `json:"model"`
	Choices []openAIChoice `json:"choices"`
	Usage   openAIUsage    `json:"usage"`
}

// openAIChoice represents a choice in the OpenAI response
type openAIChoice struct {
	Index        int           `json:"index"`
	Message      openAIMessage `json:"message"`
	FinishReason string        `json:"finish_reason"`
}

// openAIUsage represents token usage information
type openAIUsage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
}

// openAIErrorResponse represents an error response from OpenAI API
type openAIErrorResponse struct {
	Error struct {
		Message string `json:"message"`
		Type    string `json:"type"`
		Code    string `json:"code"`
	} `json:"error"`
}

// NewOpenAIClient creates a new OpenAI client from configuration.
// It validates the configuration and retrieves the API key from the environment.
func NewOpenAIClient(cfg LLMConfig) (*OpenAIClient, error) {
	// Check API key environment variable name
	if cfg.APIKeyEnv == "" {
		return nil, fmt.Errorf("%w: api_key_env not specified", ErrLLMNotConfigured)
	}

	// Get API key from environment
	apiKey := os.Getenv(cfg.APIKeyEnv)
	if apiKey == "" {
		return nil, fmt.Errorf("%w: %s", ErrLLMAPIKeyMissing, cfg.APIKeyEnv)
	}

	// Set default model if not specified
	model := cfg.Model
	if model == "" {
		model = DefaultOpenAIModel
	}

	// Set default base URL
	baseURL := cfg.BaseURL
	if baseURL == "" {
		baseURL = DefaultOpenAIEndpoint
	}

	return &OpenAIClient{
		config: LLMConfig{
			Provider:  "openai",
			APIKeyEnv: cfg.APIKeyEnv,
			Model:     model,
			BaseURL:   baseURL,
		},
		httpClient: &http.Client{
			Timeout: DefaultHTTPTimeout,
		},
		apiKey:       apiKey,
		baseURL:      baseURL,
		maxBodyBytes: httputil.MaxBodyBytes,
	}, nil
}

// WithMaxBodyBytes overrides the maximum number of bytes read from an OpenAI API
// response body and returns the client for chaining. Values <= 0 are ignored so
// the default (httputil.MaxBodyBytes, 10 MiB) remains in effect. LLM responses
// may legitimately exceed the default cap, so a larger limit can be supplied
// here (R11.2).
func (c *OpenAIClient) WithMaxBodyBytes(n int64) *OpenAIClient {
	if n > 0 {
		c.maxBodyBytes = n
	}
	return c
}

// GetModel returns the model name being used by this OpenAI client.
func (c *OpenAIClient) GetModel() string {
	return c.config.Model
}

// ExtractVersion uses OpenAI to extract a version string from content.
func (c *OpenAIClient) ExtractVersion(content []byte, prompt string) (string, error) {
	// Build the user message with content and prompt
	userMessage := buildVersionExtractionPrompt(content, prompt)

	// Create request body
	reqBody := openAIRequest{
		Model:       c.config.Model,
		MaxTokens:   100, // Version extraction needs minimal tokens
		Temperature: 0,   // Deterministic output
		Messages: []openAIMessage{
			{
				Role:    "user",
				Content: userMessage,
			},
		},
	}

	// Marshal request body
	reqJSON, err := json.Marshal(reqBody)
	if err != nil {
		return "", fmt.Errorf("failed to marshal request: %w", err)
	}

	// Create HTTP request
	req, err := http.NewRequest("POST", c.baseURL+"/chat/completions", bytes.NewReader(reqJSON))
	if err != nil {
		return "", fmt.Errorf("failed to create request: %w", err)
	}

	// Set required headers
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.apiKey)

	// Send request
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("%w: %v", ErrLLMRequestFailed, err)
	}
	defer resp.Body.Close()

	// Read response body, capped at c.maxBodyBytes (R11.2)
	body, err := readCappedBody(resp.Body, c.maxBodyBytes)
	if err != nil {
		return "", fmt.Errorf("failed to read response: %w", err)
	}

	// Check for error response
	if resp.StatusCode != http.StatusOK {
		var errResp openAIErrorResponse
		if err := json.Unmarshal(body, &errResp); err == nil && errResp.Error.Message != "" {
			return "", fmt.Errorf("%w: %s (status %d)", ErrLLMRequestFailed, errResp.Error.Message, resp.StatusCode)
		}
		return "", fmt.Errorf("%w: status %d", ErrLLMRequestFailed, resp.StatusCode)
	}

	// Parse response
	var openAIResp openAIResponse
	if err := json.Unmarshal(body, &openAIResp); err != nil {
		return "", fmt.Errorf("failed to parse response: %w", err)
	}

	// Extract text from response
	version := extractTextFromOpenAIResponse(openAIResp)
	if version == "" {
		return "", ErrLLMEmptyResponse
	}

	// Clean up the version string
	version = cleanVersionString(version)
	if version == "" {
		return "", ErrLLMEmptyResponse
	}

	return version, nil
}

// AnalyzeContent uses OpenAI to analyze content and suggest a parser configuration.
func (c *OpenAIClient) AnalyzeContent(content []byte, meta *EbuildMetadata, hint string) (*SchemaAnalysis, error) {
	// Build the analysis prompt
	userMessage := buildSchemaAnalysisPrompt(content, meta, hint)

	// Create request body with more tokens for analysis
	reqBody := openAIRequest{
		Model:       c.config.Model,
		MaxTokens:   1000,
		Temperature: 0, // Deterministic output
		Messages: []openAIMessage{
			{
				Role:    "user",
				Content: userMessage,
			},
		},
	}

	// Marshal request body
	reqJSON, err := json.Marshal(reqBody)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal request: %w", err)
	}

	// Create HTTP request
	req, err := http.NewRequest("POST", c.baseURL+"/chat/completions", bytes.NewReader(reqJSON))
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	// Set required headers
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.apiKey)

	// Send request
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrLLMRequestFailed, err)
	}
	defer resp.Body.Close()

	// Read response body, capped at c.maxBodyBytes (R11.2)
	body, err := readCappedBody(resp.Body, c.maxBodyBytes)
	if err != nil {
		return nil, fmt.Errorf("failed to read response: %w", err)
	}

	// Check for error response
	if resp.StatusCode != http.StatusOK {
		var errResp openAIErrorResponse
		if err := json.Unmarshal(body, &errResp); err == nil && errResp.Error.Message != "" {
			return nil, fmt.Errorf("%w: %s (status %d)", ErrLLMRequestFailed, errResp.Error.Message, resp.StatusCode)
		}
		return nil, fmt.Errorf("%w: status %d", ErrLLMRequestFailed, resp.StatusCode)
	}

	// Parse response
	var openAIResp openAIResponse
	if err := json.Unmarshal(body, &openAIResp); err != nil {
		return nil, fmt.Errorf("failed to parse response: %w", err)
	}

	// Extract text from response
	text := extractTextFromOpenAIResponse(openAIResp)
	if text == "" {
		return nil, ErrLLMEmptyResponse
	}

	// Parse the schema analysis from the response
	return parseSchemaAnalysis(text)
}

// SetHTTPClient sets a custom HTTP client (useful for testing)
func (c *OpenAIClient) SetHTTPClient(client *http.Client) {
	c.httpClient = client
}

// SetBaseURL sets a custom base URL (useful for testing)
func (c *OpenAIClient) SetBaseURL(url string) {
	c.baseURL = url
}

// extractTextFromOpenAIResponse extracts the text content from OpenAI's response
func extractTextFromOpenAIResponse(resp openAIResponse) string {
	if len(resp.Choices) == 0 {
		return ""
	}
	return resp.Choices[0].Message.Content
}
