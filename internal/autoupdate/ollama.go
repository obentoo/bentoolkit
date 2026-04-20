// Package autoupdate provides Ollama LLM integration for version extraction and schema analysis.
package autoupdate

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

// OllamaClient implements LLMProvider for local Ollama API.
// Ollama does not require an API key as it runs locally.
type OllamaClient struct {
	config     LLMConfig
	httpClient *http.Client
	baseURL    string
}

// ollamaRequest represents the request body for Ollama Generate API
type ollamaRequest struct {
	Model   string         `json:"model"`
	Prompt  string         `json:"prompt"`
	Stream  bool           `json:"stream"`
	Options *ollamaOptions `json:"options,omitempty"`
}

// ollamaOptions represents optional parameters for Ollama
type ollamaOptions struct {
	Temperature float64 `json:"temperature,omitempty"`
	NumPredict  int     `json:"num_predict,omitempty"`
}

// ollamaResponse represents the response from Ollama Generate API
type ollamaResponse struct {
	Model              string `json:"model"`
	CreatedAt          string `json:"created_at"`
	Response           string `json:"response"`
	Done               bool   `json:"done"`
	Context            []int  `json:"context,omitempty"`
	TotalDuration      int64  `json:"total_duration,omitempty"`
	LoadDuration       int64  `json:"load_duration,omitempty"`
	PromptEvalCount    int    `json:"prompt_eval_count,omitempty"`
	PromptEvalDuration int64  `json:"prompt_eval_duration,omitempty"`
	EvalCount          int    `json:"eval_count,omitempty"`
	EvalDuration       int64  `json:"eval_duration,omitempty"`
}

// ollamaErrorResponse represents an error response from Ollama API
type ollamaErrorResponse struct {
	Error string `json:"error"`
}

const (
	// DefaultOllamaEndpoint is the default Ollama API base URL.
	DefaultOllamaEndpoint = "http://localhost:11434"
	// DefaultOllamaModel is the default Ollama model.
	DefaultOllamaModel = "llama3"
)

// ErrOllamaConnectionFailed is returned when connection to Ollama server fails
var ErrOllamaConnectionFailed = fmt.Errorf("failed to connect to Ollama server")

// NewOllamaClient creates a new Ollama client from configuration.
// Ollama does not require an API key as it runs locally.
func NewOllamaClient(cfg LLMConfig) (*OllamaClient, error) {
	// Set default model if not specified
	model := cfg.Model
	if model == "" {
		model = DefaultOllamaModel
	}

	// Set default base URL if not specified
	baseURL := cfg.BaseURL
	if baseURL == "" {
		baseURL = DefaultOllamaEndpoint
	}

	return &OllamaClient{
		config: LLMConfig{
			Provider: "ollama",
			Model:    model,
			BaseURL:  baseURL,
		},
		httpClient: &http.Client{
			Timeout: 120 * time.Second, // Longer timeout for local inference
		},
		baseURL: baseURL,
	}, nil
}

// GetModel returns the model name being used by this Ollama client.
func (c *OllamaClient) GetModel() string {
	return c.config.Model
}

// ExtractVersion uses Ollama to extract a version string from content.
func (c *OllamaClient) ExtractVersion(content []byte, prompt string) (string, error) {
	// Build the user message with content and prompt
	userMessage := buildVersionExtractionPrompt(content, prompt)

	// Create request body
	reqBody := ollamaRequest{
		Model:  c.config.Model,
		Prompt: userMessage,
		Stream: false,
		Options: &ollamaOptions{
			Temperature: 0,   // Deterministic output
			NumPredict:  100, // Version extraction needs minimal tokens
		},
	}

	// Marshal request body
	reqJSON, err := json.Marshal(reqBody)
	if err != nil {
		return "", fmt.Errorf("failed to marshal request: %w", err)
	}

	// Create HTTP request
	req, err := http.NewRequest("POST", c.baseURL+"/api/generate", bytes.NewReader(reqJSON))
	if err != nil {
		return "", fmt.Errorf("failed to create request: %w", err)
	}

	// Set required headers
	req.Header.Set("Content-Type", "application/json")

	// Send request
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("%w: %v", ErrOllamaConnectionFailed, err)
	}
	defer resp.Body.Close()

	// Read response body
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("failed to read response: %w", err)
	}

	// Check for error response
	if resp.StatusCode != http.StatusOK {
		var errResp ollamaErrorResponse
		if err := json.Unmarshal(body, &errResp); err == nil && errResp.Error != "" {
			return "", fmt.Errorf("%w: %s (status %d)", ErrLLMRequestFailed, errResp.Error, resp.StatusCode)
		}
		return "", fmt.Errorf("%w: status %d", ErrLLMRequestFailed, resp.StatusCode)
	}

	// Parse response
	var ollamaResp ollamaResponse
	if err := json.Unmarshal(body, &ollamaResp); err != nil {
		return "", fmt.Errorf("failed to parse response: %w", err)
	}

	// Extract text from response
	version := ollamaResp.Response
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

// AnalyzeContent uses Ollama to analyze content and suggest a parser configuration.
func (c *OllamaClient) AnalyzeContent(content []byte, meta *EbuildMetadata, hint string) (*SchemaAnalysis, error) {
	// Build the analysis prompt
	userMessage := buildSchemaAnalysisPrompt(content, meta, hint)

	// Create request body with more tokens for analysis
	reqBody := ollamaRequest{
		Model:  c.config.Model,
		Prompt: userMessage,
		Stream: false,
		Options: &ollamaOptions{
			Temperature: 0,    // Deterministic output
			NumPredict:  1000, // More tokens for analysis
		},
	}

	// Marshal request body
	reqJSON, err := json.Marshal(reqBody)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal request: %w", err)
	}

	// Create HTTP request
	req, err := http.NewRequest("POST", c.baseURL+"/api/generate", bytes.NewReader(reqJSON))
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	// Set required headers
	req.Header.Set("Content-Type", "application/json")

	// Send request
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrOllamaConnectionFailed, err)
	}
	defer resp.Body.Close()

	// Read response body
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response: %w", err)
	}

	// Check for error response
	if resp.StatusCode != http.StatusOK {
		var errResp ollamaErrorResponse
		if err := json.Unmarshal(body, &errResp); err == nil && errResp.Error != "" {
			return nil, fmt.Errorf("%w: %s (status %d)", ErrLLMRequestFailed, errResp.Error, resp.StatusCode)
		}
		return nil, fmt.Errorf("%w: status %d", ErrLLMRequestFailed, resp.StatusCode)
	}

	// Parse response
	var ollamaResp ollamaResponse
	if err := json.Unmarshal(body, &ollamaResp); err != nil {
		return nil, fmt.Errorf("failed to parse response: %w", err)
	}

	// Extract text from response
	text := ollamaResp.Response
	if text == "" {
		return nil, ErrLLMEmptyResponse
	}

	// Parse the schema analysis from the response
	return parseSchemaAnalysis(text)
}

// SetHTTPClient sets a custom HTTP client (useful for testing)
func (c *OllamaClient) SetHTTPClient(client *http.Client) {
	c.httpClient = client
}

// SetBaseURL sets a custom base URL (useful for testing)
func (c *OllamaClient) SetBaseURL(url string) {
	c.baseURL = url
}
