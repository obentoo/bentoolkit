// Package autoupdate provides LLM integration for version extraction and schema analysis.
package autoupdate

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/obentoo/bentoolkit/internal/common/httputil"
	"github.com/obentoo/bentoolkit/internal/common/secrets"
)

const (
	// DefaultClaudeModel is the default Claude model used when none is specified.
	DefaultClaudeModel = "claude-3-haiku-20240307"
	// DefaultClaudeEndpoint is the default Claude API endpoint.
	DefaultClaudeEndpoint = "https://api.anthropic.com/v1/messages"
	// DefaultRequestTimeout is the default timeout for LLM HTTP requests.
	DefaultRequestTimeout = 30 * time.Second
)

// Error variables for LLM errors
var (
	// ErrLLMNotConfigured is returned when LLM provider is not configured
	ErrLLMNotConfigured = errors.New("LLM provider not configured")
	// ErrLLMAPIKeyMissing is returned when the API key environment variable is not set
	ErrLLMAPIKeyMissing = errors.New("LLM API key environment variable not set")
	// ErrLLMUnsupportedProvider is returned when an unsupported LLM provider is specified
	ErrLLMUnsupportedProvider = errors.New("unsupported LLM provider")
	// ErrLLMRequestFailed is returned when the LLM API request fails
	ErrLLMRequestFailed = errors.New("LLM API request failed")
	// ErrLLMEmptyResponse is returned when the LLM returns an empty response
	ErrLLMEmptyResponse = errors.New("LLM returned empty response")
	// ErrLLMProviderNotSupported is returned when an LLM provider is not supported
	ErrLLMProviderNotSupported = errors.New("LLM provider not supported")
)

// LLMProvider defines the interface for LLM providers.
// All LLM implementations (Claude, OpenAI, Ollama) must implement this interface.
type LLMProvider interface {
	// ExtractVersion extracts a version string from content using the LLM.
	// The prompt provides additional context for the extraction.
	ExtractVersion(content []byte, prompt string) (string, error)

	// AnalyzeContent analyzes content and suggests a parser configuration.
	// It uses ebuild metadata and optional hints to generate a schema analysis.
	AnalyzeContent(content []byte, meta *EbuildMetadata, hint string) (*SchemaAnalysis, error)

	// GetModel returns the model name being used by this provider.
	GetModel() string
}

// SchemaAnalysis represents the LLM's suggested schema for version extraction.
// It contains the parser configuration and fallback options.
type SchemaAnalysis struct {
	// ParserType is the suggested parser type ("json", "regex", "html")
	ParserType string
	// Path is the JSON path for JSON parser
	Path string
	// Pattern is the regex pattern for regex parser
	Pattern string
	// Selector is the CSS selector for HTML parser
	Selector string
	// XPath is the XPath expression for HTML parser
	XPath string
	// FallbackType is the fallback parser type
	FallbackType string
	// FallbackConfig is the fallback configuration (path, pattern, or selector)
	FallbackConfig string
	// Confidence is the confidence level (0.0-1.0)
	Confidence float64
	// Reasoning is the explanation for the suggested schema
	Reasoning string
}

// LLMConfig holds LLM provider configuration.
// It defines which LLM service to use and how to authenticate.
type LLMConfig struct {
	// Provider is the LLM provider name ("claude", "openai", "ollama")
	Provider string
	// APIKeyEnv is the environment variable name containing the API key
	APIKeyEnv string
	// Model is the specific model to use (e.g., "claude-3-haiku-20240307")
	Model string
	// BaseURL is the base URL for the API (used by Ollama)
	BaseURL string
	// Bare selects the CLI bare-mode behavior: "auto" (default), "true", or "false"
	Bare string
	// MaxBudgetUSD is an optional spend cap passed to a CLI provider via --max-budget-usd
	MaxBudgetUSD float64
}

// readCappedBody reads an HTTP response body while enforcing a maximum size.
// The body is wrapped in an http.MaxBytesReader bounded by maxBodyBytes; if the
// payload exceeds the cap the standard library yields an *http.MaxBytesError,
// which is translated into an error wrapping ErrResponseTooLarge (R11.2, R11.3).
// A non-positive maxBodyBytes falls back to httputil.MaxBodyBytes so a
// zero-valued client field can never disable the cap.
func readCappedBody(body io.ReadCloser, maxBodyBytes int64) ([]byte, error) {
	limit := maxBodyBytes
	if limit <= 0 {
		limit = httputil.MaxBodyBytes
	}
	data, err := io.ReadAll(http.MaxBytesReader(nil, body, limit))
	if err != nil {
		var maxBytesErr *http.MaxBytesError
		if errors.As(err, &maxBytesErr) {
			return nil, fmt.Errorf("%w: limit %d bytes", ErrResponseTooLarge, maxBytesErr.Limit)
		}
		return nil, err
	}
	return data, nil
}

// ClaudeClient implements LLMProvider for Anthropic's Claude API.
type ClaudeClient struct {
	config     LLMConfig
	httpClient *http.Client
	apiKey     string
	// maxBodyBytes caps how many bytes are read from an API response body.
	// It defaults to httputil.MaxBodyBytes and can be overridden via
	// WithMaxBodyBytes (R11.2).
	maxBodyBytes int64
}

// claudeRequest represents the request body for Claude Messages API
type claudeRequest struct {
	Model     string          `json:"model"`
	MaxTokens int             `json:"max_tokens"`
	Messages  []claudeMessage `json:"messages"`
}

// claudeMessage represents a message in the Claude conversation
type claudeMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// claudeResponse represents the response from Claude Messages API
type claudeResponse struct {
	ID           string         `json:"id"`
	Type         string         `json:"type"`
	Role         string         `json:"role"`
	Content      []contentBlock `json:"content"`
	Model        string         `json:"model"`
	StopReason   string         `json:"stop_reason"`
	StopSequence *string        `json:"stop_sequence"`
	Usage        claudeUsage    `json:"usage"`
}

// contentBlock represents a content block in Claude's response
type contentBlock struct {
	Type string `json:"type"`
	Text string `json:"text,omitempty"`
}

// claudeUsage represents token usage information
type claudeUsage struct {
	InputTokens  int `json:"input_tokens"`
	OutputTokens int `json:"output_tokens"`
}

// claudeErrorResponse represents an error response from Claude API
type claudeErrorResponse struct {
	Type  string `json:"type"`
	Error struct {
		Type    string `json:"type"`
		Message string `json:"message"`
	} `json:"error"`
}

// NewLLMProvider creates a new LLM provider based on the configuration.
// It returns the appropriate provider implementation (Claude, OpenAI, or Ollama).
func NewLLMProvider(cfg LLMConfig) (LLMProvider, error) {
	switch cfg.Provider {
	case "claude":
		return NewClaudeClient(cfg)
	case "openai":
		return NewOpenAIClient(cfg)
	case "ollama":
		return NewOllamaClient(cfg)
	case "claude-code":
		// NewClaudeCodeClient returns (*ClaudeCodeClient, error); since
		// *ClaudeCodeClient implements LLMProvider, the pair satisfies the
		// (LLMProvider, error) return signature directly (R1.1, R8, R8.1).
		return NewClaudeCodeClient(cfg)
	case "":
		return nil, ErrLLMNotConfigured
	default:
		return nil, fmt.Errorf("%w: %s", ErrLLMUnsupportedProvider, cfg.Provider)
	}
}

// NewClaudeClient creates a new Claude client from configuration.
// It validates the configuration and retrieves the API key from the environment.
// The API endpoint can be overridden via the CLAUDE_API_ENDPOINT environment variable.
func NewClaudeClient(cfg LLMConfig) (*ClaudeClient, error) {
	// Check API key environment variable name
	if cfg.APIKeyEnv == "" {
		return nil, fmt.Errorf("%w: api_key_env not specified", ErrLLMNotConfigured)
	}

	// Resolve the API key through the unified secrets chain (env → user file →
	// system file) rather than env-only. A present-but-unreadable secrets file
	// propagates as secrets.ErrUnreadable instead of silently degrading to
	// "anonymous"; a total miss names the env var and the searched paths.
	apiKey, found, err := secrets.Lookup(cfg.APIKeyEnv)
	if err != nil {
		return nil, err
	}
	if !found {
		return nil, fmt.Errorf("%w: %s (export %s=... or add it to one of: %s)",
			ErrLLMAPIKeyMissing, cfg.APIKeyEnv, cfg.APIKeyEnv, strings.Join(secrets.Paths(), ", "))
	}

	// Set default model if not specified
	model := cfg.Model
	if model == "" {
		model = DefaultClaudeModel
	}

	// Allow endpoint override via environment variable
	endpoint := os.Getenv("CLAUDE_API_ENDPOINT")
	if endpoint == "" {
		endpoint = DefaultClaudeEndpoint
	}

	return &ClaudeClient{
		config: LLMConfig{
			Provider:  "claude",
			APIKeyEnv: cfg.APIKeyEnv,
			Model:     model,
			BaseURL:   endpoint,
		},
		httpClient: &http.Client{
			Timeout:   DefaultRequestTimeout,
			Transport: httputil.BuildTransport(),
		},
		apiKey:       apiKey,
		maxBodyBytes: httputil.MaxBodyBytes,
	}, nil
}

// WithMaxBodyBytes overrides the maximum number of bytes read from a Claude API
// response body and returns the client for chaining. Values <= 0 are ignored so
// the default (httputil.MaxBodyBytes, 10 MiB) remains in effect. LLM responses
// may legitimately exceed the default cap, so a larger limit can be supplied
// here (R11.2).
func (c *ClaudeClient) WithMaxBodyBytes(n int64) *ClaudeClient {
	if n > 0 {
		c.maxBodyBytes = n
	}
	return c
}

// GetModel returns the model name being used by this Claude client.
func (c *ClaudeClient) GetModel() string {
	return c.config.Model
}

// ExtractVersion uses Claude to extract a version string from content.
func (c *ClaudeClient) ExtractVersion(content []byte, prompt string) (string, error) {
	// Build the user message with content and prompt
	userMessage := buildVersionExtractionPrompt(content, prompt)

	// Create request body
	reqBody := claudeRequest{
		Model:     c.config.Model,
		MaxTokens: 100, // Version extraction needs minimal tokens
		Messages: []claudeMessage{
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
	req, err := http.NewRequest("POST", c.config.BaseURL, bytes.NewReader(reqJSON))
	if err != nil {
		return "", fmt.Errorf("failed to create request: %w", err)
	}

	// Set required headers
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-api-key", c.apiKey)
	req.Header.Set("anthropic-version", "2023-06-01")

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
		var errResp claudeErrorResponse
		if err := json.Unmarshal(body, &errResp); err == nil && errResp.Error.Message != "" {
			return "", fmt.Errorf("%w: %s (status %d)", ErrLLMRequestFailed, errResp.Error.Message, resp.StatusCode)
		}
		return "", fmt.Errorf("%w: status %d", ErrLLMRequestFailed, resp.StatusCode)
	}

	// Parse response
	var claudeResp claudeResponse
	if err := json.Unmarshal(body, &claudeResp); err != nil {
		return "", fmt.Errorf("failed to parse response: %w", err)
	}

	// Extract text from response
	version := extractTextFromResponse(claudeResp)
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

// AnalyzeContent uses Claude to analyze content and suggest a parser configuration.
func (c *ClaudeClient) AnalyzeContent(content []byte, meta *EbuildMetadata, hint string) (*SchemaAnalysis, error) {
	// Build the analysis prompt
	userMessage := buildSchemaAnalysisPrompt(content, meta, hint)

	// Create request body with more tokens for analysis
	reqBody := claudeRequest{
		Model:     c.config.Model,
		MaxTokens: 1000,
		Messages: []claudeMessage{
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
	req, err := http.NewRequest("POST", c.config.BaseURL, bytes.NewReader(reqJSON))
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	// Set required headers
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-api-key", c.apiKey)
	req.Header.Set("anthropic-version", "2023-06-01")

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
		var errResp claudeErrorResponse
		if err := json.Unmarshal(body, &errResp); err == nil && errResp.Error.Message != "" {
			return nil, fmt.Errorf("%w: %s (status %d)", ErrLLMRequestFailed, errResp.Error.Message, resp.StatusCode)
		}
		return nil, fmt.Errorf("%w: status %d", ErrLLMRequestFailed, resp.StatusCode)
	}

	// Parse response
	var claudeResp claudeResponse
	if err := json.Unmarshal(body, &claudeResp); err != nil {
		return nil, fmt.Errorf("failed to parse response: %w", err)
	}

	// Extract text from response
	text := extractTextFromResponse(claudeResp)
	if text == "" {
		return nil, ErrLLMEmptyResponse
	}

	// Parse the schema analysis from the response
	return parseSchemaAnalysis(text)
}

// SetHTTPClient sets a custom HTTP client (useful for testing)
func (c *ClaudeClient) SetHTTPClient(client *http.Client) {
	c.httpClient = client
}

// buildVersionExtractionPrompt creates the prompt for version extraction
func buildVersionExtractionPrompt(content []byte, userPrompt string) string {
	// Truncate content if too long (to avoid token limits)
	contentStr := string(content)
	const maxContentLen = 4000
	if len(contentStr) > maxContentLen {
		contentStr = contentStr[:maxContentLen] + "\n... (truncated)"
	}

	// Build the prompt
	var sb strings.Builder
	sb.WriteString("Extract the version number from the following content.\n\n")

	if userPrompt != "" {
		sb.WriteString("Instructions: ")
		sb.WriteString(userPrompt)
		sb.WriteString("\n\n")
	}

	sb.WriteString("Content:\n```\n")
	sb.WriteString(contentStr)
	sb.WriteString("\n```\n\n")
	sb.WriteString("Respond with ONLY the version number (e.g., \"1.2.3\" or \"11.81.1\"). ")
	sb.WriteString("Do not include any other text, explanation, or formatting.")

	return sb.String()
}

// buildSchemaAnalysisPrompt creates the prompt for schema analysis
func buildSchemaAnalysisPrompt(content []byte, meta *EbuildMetadata, hint string) string {
	// Truncate content if too long
	contentStr := string(content)
	const maxContentLen = 4000
	if len(contentStr) > maxContentLen {
		contentStr = contentStr[:maxContentLen] + "\n... (truncated)"
	}

	var sb strings.Builder
	sb.WriteString("Analyze the following content and suggest the best way to extract version information.\n\n")

	// Add metadata context if available
	if meta != nil {
		sb.WriteString("Package Information:\n")
		if meta.Package != "" {
			fmt.Fprintf(&sb, "- Package: %s\n", meta.Package)
		}
		if meta.Version != "" {
			fmt.Fprintf(&sb, "- Current Version: %s\n", meta.Version)
		}
		if meta.Homepage != "" {
			fmt.Fprintf(&sb, "- Homepage: %s\n", meta.Homepage)
		}
		sb.WriteString("\n")
	}

	if hint != "" {
		sb.WriteString("User Hint: ")
		sb.WriteString(hint)
		sb.WriteString("\n\n")
	}

	sb.WriteString("Content:\n```\n")
	sb.WriteString(contentStr)
	sb.WriteString("\n```\n\n")

	sb.WriteString("Respond in JSON format with the following structure:\n")
	sb.WriteString("{\n")
	sb.WriteString("  \"parser_type\": \"json\" | \"regex\" | \"html\",\n")
	sb.WriteString("  \"path\": \"JSON path if parser_type is json\",\n")
	sb.WriteString("  \"pattern\": \"regex pattern if parser_type is regex\",\n")
	sb.WriteString("  \"selector\": \"CSS selector if parser_type is html\",\n")
	sb.WriteString("  \"xpath\": \"XPath expression if parser_type is html (alternative to selector)\",\n")
	sb.WriteString("  \"fallback_type\": \"fallback parser type\",\n")
	sb.WriteString("  \"fallback_config\": \"fallback configuration\",\n")
	sb.WriteString("  \"confidence\": 0.0-1.0,\n")
	sb.WriteString("  \"reasoning\": \"explanation of the choice\"\n")
	sb.WriteString("}\n")

	return sb.String()
}

// flexString is a JSON-decoding helper for fields that the schema expects as a
// string but an LLM may emit as a different shape. Strings and scalar values
// (number/bool) are preserved as text; null becomes ""; and an object or array
// — which some models return for e.g. fallback_config — is tolerated by dropping
// it to "" rather than failing the entire parse. This keeps a single malformed
// secondary field from discarding an otherwise-valid primary schema.
type flexString string

func (s *flexString) UnmarshalJSON(data []byte) error {
	data = bytes.TrimSpace(data)
	if len(data) == 0 || string(data) == "null" {
		*s = ""
		return nil
	}
	switch data[0] {
	case '"':
		var str string
		if err := json.Unmarshal(data, &str); err != nil {
			return err
		}
		*s = flexString(str)
	case '{', '[':
		// Object/array where a string was expected: drop it.
		*s = ""
	default:
		// Scalars (number/bool): keep their literal text.
		*s = flexString(string(data))
	}
	return nil
}

// flexFloat is a JSON-decoding helper for a numeric field (confidence) that an
// LLM may emit either as a JSON number or as a numeric string (e.g. "0.95").
// null and empty decode to 0.
type flexFloat float64

func (f *flexFloat) UnmarshalJSON(data []byte) error {
	data = bytes.TrimSpace(data)
	if len(data) == 0 || string(data) == "null" {
		*f = 0
		return nil
	}
	if data[0] == '"' {
		var str string
		if err := json.Unmarshal(data, &str); err != nil {
			return err
		}
		str = strings.TrimSpace(str)
		if str == "" {
			*f = 0
			return nil
		}
		v, err := strconv.ParseFloat(str, 64)
		if err != nil {
			return err
		}
		*f = flexFloat(v)
		return nil
	}
	var v float64
	if err := json.Unmarshal(data, &v); err != nil {
		return err
	}
	*f = flexFloat(v)
	return nil
}

// parseSchemaAnalysis parses the LLM response into a SchemaAnalysis struct.
// String fields use flexString and confidence uses flexFloat so that benign
// shape variations in the model's JSON (an object where a string was expected,
// a quoted number) do not fail an otherwise-valid analysis.
func parseSchemaAnalysis(text string) (*SchemaAnalysis, error) {
	// Try to find JSON in the response
	start := strings.Index(text, "{")
	end := strings.LastIndex(text, "}")
	if start == -1 || end == -1 || end <= start {
		return nil, fmt.Errorf("no valid JSON found in response")
	}

	jsonStr := text[start : end+1]

	// Parse the JSON
	var raw struct {
		ParserType     flexString `json:"parser_type"`
		Path           flexString `json:"path"`
		Pattern        flexString `json:"pattern"`
		Selector       flexString `json:"selector"`
		XPath          flexString `json:"xpath"`
		FallbackType   flexString `json:"fallback_type"`
		FallbackConfig flexString `json:"fallback_config"`
		Confidence     flexFloat  `json:"confidence"`
		Reasoning      flexString `json:"reasoning"`
	}

	if err := json.Unmarshal([]byte(jsonStr), &raw); err != nil {
		return nil, fmt.Errorf("failed to parse schema analysis: %w", err)
	}

	return &SchemaAnalysis{
		ParserType:     string(raw.ParserType),
		Path:           string(raw.Path),
		Pattern:        string(raw.Pattern),
		Selector:       string(raw.Selector),
		XPath:          string(raw.XPath),
		FallbackType:   string(raw.FallbackType),
		FallbackConfig: string(raw.FallbackConfig),
		Confidence:     float64(raw.Confidence),
		Reasoning:      string(raw.Reasoning),
	}, nil
}

// extractTextFromResponse extracts the text content from Claude's response
func extractTextFromResponse(resp claudeResponse) string {
	for _, block := range resp.Content {
		if block.Type == "text" && block.Text != "" {
			return block.Text
		}
	}
	return ""
}

// cleanVersionString cleans up the version string from LLM response
func cleanVersionString(version string) string {
	// Trim whitespace
	version = strings.TrimSpace(version)

	// Remove common prefixes
	version = strings.TrimPrefix(version, "v")
	version = strings.TrimPrefix(version, "V")

	// Remove quotes if present
	version = strings.Trim(version, "\"'`")

	// Remove any trailing punctuation
	version = strings.TrimRight(version, ".,;:")

	// Trim whitespace again
	version = strings.TrimSpace(version)

	return version
}

// =============================================================================
// Legacy API compatibility - LLMClient wraps the new provider interface
// =============================================================================

// LLMClient handles LLM API interactions for version extraction.
// This is maintained for backward compatibility with existing code.
type LLMClient struct {
	provider LLMProvider
	config   LLMConfig
}

// NewLLMClient creates a new LLM client from configuration.
// It validates the configuration and retrieves the API key from the environment.
// Returns an error if the provider is not configured or the API key is missing.
func NewLLMClient(cfg LLMConfig) (*LLMClient, error) {
	// Check if provider is configured
	if cfg.Provider == "" {
		return nil, ErrLLMNotConfigured
	}

	// For backward compatibility, only support claude in the legacy API
	if cfg.Provider != "claude" {
		return nil, fmt.Errorf("%w: %s", ErrLLMUnsupportedProvider, cfg.Provider)
	}

	provider, err := NewClaudeClient(cfg)
	if err != nil {
		return nil, err
	}

	return &LLMClient{
		provider: provider,
		config:   cfg,
	}, nil
}

// NewLLMClientWithHTTPClient creates a new LLM client with a custom HTTP client.
// This is useful for testing with mock servers.
func NewLLMClientWithHTTPClient(cfg LLMConfig, httpClient *http.Client) (*LLMClient, error) {
	client, err := NewLLMClient(cfg)
	if err != nil {
		return nil, err
	}
	if claude, ok := client.provider.(*ClaudeClient); ok {
		claude.SetHTTPClient(httpClient)
	}
	return client, nil
}

// Compile-time assertion that *LLMClient satisfies LLMProvider. The legacy
// wrapper must remain a valid LLMProvider so it stays accepted by
// WithLLMClient after the Checker was reprogrammed to the interface (AD2): it
// delegates every interface method to its embedded provider below.
var _ LLMProvider = (*LLMClient)(nil)

// ExtractVersion uses the LLM to extract a version string from content.
func (c *LLMClient) ExtractVersion(content []byte, prompt string) (string, error) {
	return c.provider.ExtractVersion(content, prompt)
}

// AnalyzeContent delegates schema analysis to the embedded provider so
// *LLMClient satisfies the full LLMProvider interface (AD2). The legacy API
// historically exposed only ExtractVersion; this method exists purely to keep
// *LLMClient a valid WithLLMClient argument now that the option takes an
// LLMProvider.
func (c *LLMClient) AnalyzeContent(content []byte, meta *EbuildMetadata, hint string) (*SchemaAnalysis, error) {
	return c.provider.AnalyzeContent(content, meta, hint)
}

// GetModel delegates to the embedded provider so *LLMClient satisfies
// LLMProvider (AD2).
func (c *LLMClient) GetModel() string {
	return c.provider.GetModel()
}

// SetHTTPClient sets a custom HTTP client (useful for testing)
func (c *LLMClient) SetHTTPClient(client *http.Client) {
	if claude, ok := c.provider.(*ClaudeClient); ok {
		claude.SetHTTPClient(client)
	}
}

// SetBaseURL allows overriding the API endpoint URL (useful for testing).
func (c *LLMClient) SetBaseURL(url string) {
	if claude, ok := c.provider.(*ClaudeClient); ok {
		claude.config.BaseURL = url
	}
}
