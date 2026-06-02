package autoupdate

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// stubLLMProvider is a minimal LLMProvider for option testing
type stubLLMProvider struct{}

func (s *stubLLMProvider) ExtractVersion(_ []byte, _ string) (string, error) { return "", nil }
func (s *stubLLMProvider) AnalyzeContent(_ []byte, _ *EbuildMetadata, _ string) (*SchemaAnalysis, error) {
	return &SchemaAnalysis{ParserType: "json"}, nil
}
func (s *stubLLMProvider) GetModel() string { return "stub" }

// TestWithAnalyzerLLMClient tests the WithAnalyzerLLMClient option
func TestWithAnalyzerLLMClient(t *testing.T) {
	tmpDir := t.TempDir()
	stub := &stubLLMProvider{}

	analyzer, err := NewAnalyzer(tmpDir, WithAnalyzerLLMClient(stub))
	if err != nil {
		t.Fatalf("NewAnalyzer: %v", err)
	}
	if analyzer.llmClient != stub {
		t.Error("Expected llmClient to be set via WithAnalyzerLLMClient")
	}
}

// TestWithAnalyzerCache tests the WithAnalyzerCache option
func TestWithAnalyzerCache(t *testing.T) {
	tmpDir := t.TempDir()
	cache, err := NewAnalysisCache(tmpDir)
	if err != nil {
		t.Fatalf("NewAnalysisCache: %v", err)
	}

	analyzer, err := NewAnalyzer(tmpDir, WithAnalyzerCache(cache))
	if err != nil {
		t.Fatalf("NewAnalyzer: %v", err)
	}
	if analyzer.cache != cache {
		t.Error("Expected cache to be set via WithAnalyzerCache")
	}
}

// TestWithAnalyzerConfigDir tests the WithAnalyzerConfigDir option
func TestWithAnalyzerConfigDir(t *testing.T) {
	tmpDir := t.TempDir()
	customDir := filepath.Join(tmpDir, "custom-config")

	analyzer, err := NewAnalyzer(tmpDir, WithAnalyzerConfigDir(customDir))
	if err != nil {
		t.Fatalf("NewAnalyzer: %v", err)
	}
	if analyzer.configDir != customDir {
		t.Errorf("Expected configDir=%q, got %q", customDir, analyzer.configDir)
	}
}

// TestAnalyzerConfig tests the Config() accessor
func TestAnalyzerConfig(t *testing.T) {
	tmpDir := t.TempDir()
	cfg := &PackagesConfig{Packages: map[string]PackageConfig{}}

	analyzer, err := NewAnalyzer(tmpDir, WithAnalyzerPackagesConfig(cfg))
	if err != nil {
		t.Fatalf("NewAnalyzer: %v", err)
	}
	if analyzer.Config() != cfg {
		t.Error("Config() should return the configured PackagesConfig")
	}
}

// TestAnalyzerOverlayPath tests the OverlayPath() accessor
func TestAnalyzerOverlayPath(t *testing.T) {
	tmpDir := t.TempDir()

	analyzer, err := NewAnalyzer(tmpDir)
	if err != nil {
		t.Fatalf("NewAnalyzer: %v", err)
	}
	if analyzer.OverlayPath() != tmpDir {
		t.Errorf("OverlayPath()=%q, want %q", analyzer.OverlayPath(), tmpDir)
	}
}

// TestAnalyzerCache tests the Cache() accessor
func TestAnalyzerCache(t *testing.T) {
	tmpDir := t.TempDir()
	cache, err := NewAnalysisCache(tmpDir)
	if err != nil {
		t.Fatalf("NewAnalysisCache: %v", err)
	}

	analyzer, err := NewAnalyzer(tmpDir, WithAnalyzerCache(cache))
	if err != nil {
		t.Fatalf("NewAnalyzer: %v", err)
	}
	if analyzer.Cache() != cache {
		t.Error("Cache() should return the configured AnalysisCache")
	}
}

// TestSchemaFromAnalysisJSON tests schemaFromAnalysis with JSON parser
func TestSchemaFromAnalysisJSON(t *testing.T) {
	tmpDir := t.TempDir()
	analyzer, err := NewAnalyzer(tmpDir)
	if err != nil {
		t.Fatalf("NewAnalyzer: %v", err)
	}

	source := &DataSource{URL: "https://api.example.com/releases"}
	analysis := &SchemaAnalysis{
		ParserType: "json",
		Path:       "tag_name",
	}

	schema, err := analyzer.schemaFromAnalysis(analysis, source)
	if err != nil {
		t.Fatalf("schemaFromAnalysis: %v", err)
	}
	if schema.Parser != "json" {
		t.Errorf("Expected parser=json, got %q", schema.Parser)
	}
	if schema.Path != "tag_name" {
		t.Errorf("Expected path=tag_name, got %q", schema.Path)
	}
	if schema.URL != source.URL {
		t.Errorf("Expected URL=%q, got %q", source.URL, schema.URL)
	}
}

// TestSchemaFromAnalysisRegex tests schemaFromAnalysis with regex parser
func TestSchemaFromAnalysisRegex(t *testing.T) {
	tmpDir := t.TempDir()
	analyzer, err := NewAnalyzer(tmpDir)
	if err != nil {
		t.Fatalf("NewAnalyzer: %v", err)
	}

	source := &DataSource{URL: "https://example.com/releases"}
	analysis := &SchemaAnalysis{
		ParserType: "regex",
		Pattern:    `v(\d+\.\d+\.\d+)`,
	}

	schema, err := analyzer.schemaFromAnalysis(analysis, source)
	if err != nil {
		t.Fatalf("schemaFromAnalysis: %v", err)
	}
	if schema.Parser != "regex" {
		t.Errorf("Expected parser=regex, got %q", schema.Parser)
	}
	if schema.Pattern != `v(\d+\.\d+\.\d+)` {
		t.Errorf("Expected pattern, got %q", schema.Pattern)
	}
}

// TestSchemaFromAnalysisHTML tests schemaFromAnalysis with html parser
func TestSchemaFromAnalysisHTML(t *testing.T) {
	tmpDir := t.TempDir()
	analyzer, err := NewAnalyzer(tmpDir)
	if err != nil {
		t.Fatalf("NewAnalyzer: %v", err)
	}

	source := &DataSource{URL: "https://example.com"}
	analysis := &SchemaAnalysis{
		ParserType: "html",
		Selector:   ".version",
		XPath:      "//span[@class='version']",
		Pattern:    `(\d+\.\d+)`,
	}

	schema, err := analyzer.schemaFromAnalysis(analysis, source)
	if err != nil {
		t.Fatalf("schemaFromAnalysis: %v", err)
	}
	if schema.Parser != "html" {
		t.Errorf("Expected parser=html, got %q", schema.Parser)
	}
	if schema.Selector != ".version" {
		t.Errorf("Expected selector=.version, got %q", schema.Selector)
	}
}

// TestSchemaFromAnalysisWithFallback tests schemaFromAnalysis sets fallback
func TestSchemaFromAnalysisWithFallback(t *testing.T) {
	tmpDir := t.TempDir()
	analyzer, err := NewAnalyzer(tmpDir)
	if err != nil {
		t.Fatalf("NewAnalyzer: %v", err)
	}

	source := &DataSource{URL: "https://example.com"}
	analysis := &SchemaAnalysis{
		ParserType:     "json",
		Path:           "version",
		FallbackType:   "regex",
		FallbackConfig: `(\d+\.\d+)`,
	}

	schema, err := analyzer.schemaFromAnalysis(analysis, source)
	if err != nil {
		t.Fatalf("schemaFromAnalysis: %v", err)
	}
	if schema.FallbackParser != "regex" {
		t.Errorf("Expected fallback parser=regex, got %q", schema.FallbackParser)
	}
}

// TestGenerateDefaultSchemaJSON tests generateDefaultSchema for JSON content
func TestGenerateDefaultSchemaJSON(t *testing.T) {
	tmpDir := t.TempDir()
	analyzer, err := NewAnalyzer(tmpDir)
	if err != nil {
		t.Fatalf("NewAnalyzer: %v", err)
	}

	content := []byte(`{"version": "1.2.3"}`)
	source := &DataSource{URL: "https://api.example.com", ContentType: ContentTypeJSON}

	schema, err := analyzer.generateDefaultSchema(content, source)
	if err != nil {
		t.Fatalf("generateDefaultSchema: %v", err)
	}
	if schema.Parser != "json" {
		t.Errorf("Expected parser=json, got %q", schema.Parser)
	}
	if schema.URL != source.URL {
		t.Errorf("Expected URL=%q, got %q", source.URL, schema.URL)
	}
}

// TestGenerateDefaultSchemaHTML tests generateDefaultSchema for HTML content
func TestGenerateDefaultSchemaHTML(t *testing.T) {
	tmpDir := t.TempDir()
	analyzer, err := NewAnalyzer(tmpDir)
	if err != nil {
		t.Fatalf("NewAnalyzer: %v", err)
	}

	content := []byte(`<html><body><span class="version">1.0.0</span></body></html>`)
	source := &DataSource{URL: "https://example.com", ContentType: ContentTypeHTML}

	schema, err := analyzer.generateDefaultSchema(content, source)
	if err != nil {
		t.Fatalf("generateDefaultSchema: %v", err)
	}
	if schema.Parser != "html" {
		t.Errorf("Expected parser=html, got %q", schema.Parser)
	}
}

// TestGenerateDefaultSchemaDefault tests generateDefaultSchema for unknown content type
func TestGenerateDefaultSchemaDefault(t *testing.T) {
	tmpDir := t.TempDir()
	analyzer, err := NewAnalyzer(tmpDir)
	if err != nil {
		t.Fatalf("NewAnalyzer: %v", err)
	}

	content := []byte(`Version: 1.2.3`)
	source := &DataSource{URL: "https://example.com", ContentType: "text/plain"}

	schema, err := analyzer.generateDefaultSchema(content, source)
	if err != nil {
		t.Fatalf("generateDefaultSchema: %v", err)
	}
	if schema.Parser != "regex" {
		t.Errorf("Expected parser=regex, got %q", schema.Parser)
	}
}

// TestLoadAndMergeSchema tests LoadAndMergeSchema merges and saves
func TestLoadAndMergeSchema(t *testing.T) {
	tmpDir := t.TempDir()

	// Create .autoupdate dir
	if err := os.MkdirAll(filepath.Join(tmpDir, ".autoupdate"), 0755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}

	analyzer, err := NewAnalyzer(tmpDir)
	if err != nil {
		t.Fatalf("NewAnalyzer: %v", err)
	}

	schema := &PackageConfig{URL: "https://example.com", Parser: "json", Path: "version"}
	if err := analyzer.LoadAndMergeSchema("app-misc/hello", schema); err != nil {
		t.Fatalf("LoadAndMergeSchema: %v", err)
	}

	// Verify it's in config
	if _, exists := analyzer.config.Packages["app-misc/hello"]; !exists {
		t.Error("Expected package to be in config after LoadAndMergeSchema")
	}
}

// TestLoadAndMergeSchemaMergesExisting tests that existing entries are preserved
func TestLoadAndMergeSchemaMergesExisting(t *testing.T) {
	tmpDir := t.TempDir()

	// Pre-populate with existing schema
	existing := &PackagesConfig{
		Packages: map[string]PackageConfig{
			"app-misc/existing": {URL: "https://existing.com", Parser: "json", Path: "version"},
		},
	}
	analyzer, err := NewAnalyzer(tmpDir, WithAnalyzerPackagesConfig(existing))
	if err != nil {
		t.Fatalf("NewAnalyzer: %v", err)
	}
	// Save existing to disk first
	if err := analyzer.savePackagesConfig(); err != nil {
		t.Fatalf("savePackagesConfig: %v", err)
	}

	// Now create a fresh analyzer and merge a new schema
	analyzer2, err := NewAnalyzer(tmpDir)
	if err != nil {
		t.Fatalf("NewAnalyzer2: %v", err)
	}

	newSchema := &PackageConfig{URL: "https://new.com", Parser: "regex", Pattern: `v(\d+)`}
	if err := analyzer2.LoadAndMergeSchema("app-misc/new", newSchema); err != nil {
		t.Fatalf("LoadAndMergeSchema: %v", err)
	}

	// Both should exist
	if _, exists := analyzer2.config.Packages["app-misc/existing"]; !exists {
		t.Error("Expected existing package to be preserved after merge")
	}
	if _, exists := analyzer2.config.Packages["app-misc/new"]; !exists {
		t.Error("Expected new package to be added after merge")
	}
}

// TestCacheSave tests Cache.Save persists current state
func TestCacheSave(t *testing.T) {
	tmpDir := t.TempDir()
	cache, err := NewCache(tmpDir)
	if err != nil {
		t.Fatalf("NewCache: %v", err)
	}

	// Manually add entry without auto-save
	cache.Entries["app-misc/test"] = CacheEntry{
		Version:   "1.0.0",
		Timestamp: time.Now(),
		Source:    "https://example.com",
	}

	if err := cache.Save(); err != nil {
		t.Fatalf("Save: %v", err)
	}

	// Reload and verify
	cache2, err := NewCache(tmpDir)
	if err != nil {
		t.Fatalf("NewCache reload: %v", err)
	}
	if _, found := cache2.Get("app-misc/test"); !found {
		t.Error("Expected entry to persist after Save")
	}
}

// TestPendingListSave tests PendingList.Save persists current state
func TestPendingListSave(t *testing.T) {
	tmpDir := t.TempDir()
	pending, err := NewPendingList(tmpDir)
	if err != nil {
		t.Fatalf("NewPendingList: %v", err)
	}

	// Manually add entry without auto-save
	pending.Updates["app-misc/test"] = PendingUpdate{
		Package:        "app-misc/test",
		CurrentVersion: "1.0.0",
		NewVersion:     "1.1.0",
		Status:         StatusPending,
		DetectedAt:     time.Now(),
	}

	if err := pending.Save(); err != nil {
		t.Fatalf("Save: %v", err)
	}

	// Reload and verify
	pending2, err := NewPendingList(tmpDir)
	if err != nil {
		t.Fatalf("NewPendingList reload: %v", err)
	}
	if !pending2.Has("app-misc/test") {
		t.Error("Expected entry to persist after Save")
	}
}

// TestWithClock tests the WithClock option for RateLimiter
func TestWithClock(t *testing.T) {
	mockClock := &mockClock{now: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}
	rl := NewRateLimiter(WithClock(mockClock))

	if rl.clock != mockClock {
		t.Error("Expected clock to be set via WithClock")
	}
}

// mockClock implements Clock for testing
type mockClock struct {
	now time.Time
}

func (m *mockClock) Now() time.Time        { return m.now }
func (m *mockClock) Sleep(d time.Duration) { m.now = m.now.Add(d) }

// =============================================================================
// Checker options
// =============================================================================

// TestCheckerWithLLMClient tests the WithLLMClient checker option. After the
// AD2 refactor WithLLMClient takes an LLMProvider and checker.llmClient is that
// interface; the legacy *LLMClient still satisfies it (it now delegates
// AnalyzeContent/GetModel to its embedded provider), so passing one here both
// compiles and is stored. This is the backward-compatibility guard for R8.1 —
// an existing *LLMClient caller keeps working.
func TestCheckerWithLLMClient(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("TEST_LLM_KEY", "test-key")
	llmCfg := LLMConfig{Provider: "claude", APIKeyEnv: "TEST_LLM_KEY", Model: "claude-3-haiku-20240307"}
	llmClient, err := NewLLMClient(llmCfg)
	if err != nil {
		t.Fatalf("NewLLMClient: %v", err)
	}

	cfg := &PackagesConfig{Packages: map[string]PackageConfig{}}
	checker, err := NewChecker(tmpDir, WithPackagesConfig(cfg), WithLLMClient(llmClient))
	if err != nil {
		t.Fatalf("NewChecker: %v", err)
	}
	// The interface-typed field holds the concrete *LLMClient we passed.
	if checker.llmClient != llmClient {
		t.Error("Expected llmClient to be set via WithLLMClient")
	}
}

// TestCheckerWithHTTPClient tests the WithHTTPClient checker option
func TestCheckerWithHTTPClient(t *testing.T) {
	tmpDir := t.TempDir()
	httpClient := NewRetryableHTTPClient()

	cfg := &PackagesConfig{Packages: map[string]PackageConfig{}}
	checker, err := NewChecker(tmpDir, WithPackagesConfig(cfg), WithHTTPClient(httpClient))
	if err != nil {
		t.Fatalf("NewChecker: %v", err)
	}
	if checker.httpClient != httpClient {
		t.Error("Expected httpClient to be set via WithHTTPClient")
	}
}

// =============================================================================
// httpclient.isTimeoutError
// =============================================================================

// TestIsTimeoutError tests the isTimeoutError helper
func TestIsTimeoutError(t *testing.T) {
	if isTimeoutError(nil) {
		t.Error("nil error should not be timeout")
	}
	if !isTimeoutError(context.DeadlineExceeded) {
		t.Error("DeadlineExceeded should be timeout")
	}
}

// =============================================================================
// realClock Now/Sleep
// =============================================================================

// TestRealClockNow tests realClock.Now returns a non-zero time
func TestRealClockNow(t *testing.T) {
	c := realClock{}
	if c.Now().IsZero() {
		t.Error("realClock.Now() should not return zero time")
	}
}

// TestRealClockSleep tests realClock.Sleep does not panic
func TestRealClockSleep(t *testing.T) {
	c := realClock{}
	c.Sleep(0) // zero duration — should not block
}

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
	meta := &EbuildMetadata{
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
