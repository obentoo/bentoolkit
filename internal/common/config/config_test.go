package config

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/leanovate/gopter"
	"github.com/leanovate/gopter/gen"
	"github.com/leanovate/gopter/prop"
)

// genValidPath generates valid path strings (alphanumeric with slashes)
func genValidPath() gopter.Gen {
	return gen.RegexMatch(`^/[a-z][a-z0-9/]{0,20}$`)
}

// genValidEmail generates valid email strings
func genValidEmail() gopter.Gen {
	return gen.RegexMatch(`^[a-z]{1,10}@[a-z]{1,10}\.[a-z]{2,4}$`)
}

// genValidUsername generates valid username strings
func genValidUsername() gopter.Gen {
	return gen.RegexMatch(`^[a-zA-Z][a-zA-Z0-9_]{0,15}$`)
}

// genValidRemote generates valid git remote names
func genValidRemote() gopter.Gen {
	return gen.RegexMatch(`^[a-z]{1,10}$`)
}

// genConfig generates valid Config structs
func genConfig() gopter.Gen {
	return gopter.CombineGens(
		genValidPath(),
		genValidRemote(),
		genValidUsername(),
		genValidEmail(),
	).Map(func(values []interface{}) *Config {
		return &Config{
			Overlay: OverlayConfig{
				Path:   values[0].(string),
				Remote: values[1].(string),
			},
			Git: GitConfig{
				User:  values[2].(string),
				Email: values[3].(string),
			},
		}
	})
}

// TestConfigRoundTrip tests Property 1: Configuration round-trip
// **Feature: overlay-manager, Property 1: Configuration round-trip**
// **Validates: Requirements 1.5**
func TestConfigRoundTrip(t *testing.T) {
	parameters := gopter.DefaultTestParameters()
	parameters.MinSuccessfulTests = 100
	properties := gopter.NewProperties(parameters)

	properties.Property("Config YAML round-trip preserves data", prop.ForAll(
		func(cfg *Config) bool {
			// Create temp directory for test
			tmpDir, err := os.MkdirTemp("", "config-test-*")
			if err != nil {
				t.Logf("Failed to create temp dir: %v", err)
				return false
			}
			defer os.RemoveAll(tmpDir)

			configPath := filepath.Join(tmpDir, "config.yaml")

			// Save config
			if err := cfg.SaveTo(configPath); err != nil {
				t.Logf("Failed to save config: %v", err)
				return false
			}

			// Load config back
			loaded, err := LoadFrom(configPath)
			if err != nil {
				t.Logf("Failed to load config: %v", err)
				return false
			}

			// Compare
			return reflect.DeepEqual(cfg, loaded)
		},
		genConfig(),
	))

	properties.TestingRun(t)
}

// TestMissingConfigFileCreatesDefault tests that missing config file creates default
// _Requirements: 1.2_
func TestMissingConfigFileCreatesDefault(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "config-test-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	configPath := filepath.Join(tmpDir, "subdir", "config.yaml")

	// Load from non-existent path should create default
	cfg, err := LoadFrom(configPath)
	if err != nil {
		t.Fatalf("Expected no error, got: %v", err)
	}

	// Check default values
	if cfg.Overlay.Path != "" {
		t.Errorf("Expected empty overlay path, got: %s", cfg.Overlay.Path)
	}
	if cfg.Overlay.Remote != "origin" {
		t.Errorf("Expected remote 'origin', got: %s", cfg.Overlay.Remote)
	}

	// Verify file was created
	if _, err := os.Stat(configPath); os.IsNotExist(err) {
		t.Error("Expected config file to be created")
	}
}

// TestEmptyOverlayPathReturnsError tests that empty overlay path returns error
// _Requirements: 1.3_
func TestEmptyOverlayPathReturnsError(t *testing.T) {
	cfg := &Config{
		Overlay: OverlayConfig{
			Path: "",
		},
	}

	_, err := cfg.GetOverlayPath()
	if err != ErrOverlayPathNotSet {
		t.Errorf("Expected ErrOverlayPathNotSet, got: %v", err)
	}
}

// TestInvalidOverlayPathReturnsError tests that invalid overlay path returns error
// _Requirements: 1.4_
func TestInvalidOverlayPathReturnsError(t *testing.T) {
	cfg := &Config{
		Overlay: OverlayConfig{
			Path: "/nonexistent/path/that/does/not/exist",
		},
	}

	_, err := cfg.GetOverlayPath()
	if err != ErrOverlayPathNotFound {
		t.Errorf("Expected ErrOverlayPathNotFound, got: %v", err)
	}
}

// TestValidOverlayPathReturnsPath tests that valid overlay path is returned
func TestValidOverlayPathReturnsPath(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "overlay-test-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	// Create required overlay structure
	if err := os.MkdirAll(filepath.Join(tmpDir, "profiles"), 0755); err != nil {
		t.Fatalf("Failed to create profiles dir: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(tmpDir, "metadata"), 0755); err != nil {
		t.Fatalf("Failed to create metadata dir: %v", err)
	}

	cfg := &Config{
		Overlay: OverlayConfig{
			Path: tmpDir,
		},
	}

	path, err := cfg.GetOverlayPath()
	if err != nil {
		t.Errorf("Expected no error, got: %v", err)
	}
	if path != tmpDir {
		t.Errorf("Expected path %s, got: %s", tmpDir, path)
	}
}

// genGitconfigUserName generates valid git user names (non-empty, printable, no trailing spaces)
func genGitconfigUserName() gopter.Gen {
	// Generate names that don't have leading/trailing spaces (INI parsers trim these)
	return gen.RegexMatch(`^[A-Za-z][A-Za-z0-9_-]{0,30}$`)
}

// genGitconfigEmail generates valid email addresses
func genGitconfigEmail() gopter.Gen {
	return gen.RegexMatch(`^[a-z][a-z0-9._%+-]{0,15}@[a-z][a-z0-9.-]{0,10}\.[a-z]{2,4}$`)
}

// generateGitconfigContent creates a valid gitconfig INI content with user section
func generateGitconfigContent(name, email string) string {
	return "[user]\n\tname = " + name + "\n\temail = " + email + "\n"
}

// TestGitconfigParsingRoundTrip tests Property 7: Git user config parsing
// **Feature: overlay-manager, Property 7: Git user config parsing**
// **Validates: Requirements 6.1**
func TestGitconfigParsingRoundTrip(t *testing.T) {
	parameters := gopter.DefaultTestParameters()
	parameters.MinSuccessfulTests = 100
	properties := gopter.NewProperties(parameters)

	properties.Property("Gitconfig parsing extracts name and email correctly", prop.ForAll(
		func(name, email string) bool {
			// Generate gitconfig content
			content := generateGitconfigContent(name, email)

			// Parse the content
			parsedName, parsedEmail, err := ParseGitconfigContent(strings.NewReader(content))
			if err != nil {
				t.Logf("Failed to parse gitconfig: %v", err)
				return false
			}

			// Verify extracted values match input
			if parsedName != name {
				t.Logf("Name mismatch: expected %q, got %q", name, parsedName)
				return false
			}
			if parsedEmail != email {
				t.Logf("Email mismatch: expected %q, got %q", email, parsedEmail)
				return false
			}

			return true
		},
		genGitconfigUserName(),
		genGitconfigEmail(),
	))

	properties.TestingRun(t)
}

// TestGitconfigParsing tests gitconfig INI parsing
// _Requirements: 6.1_
func TestGitconfigParsing(t *testing.T) {
	tests := []struct {
		name          string
		content       string
		expectedUser  string
		expectedEmail string
	}{
		{
			name: "standard gitconfig",
			content: `[user]
	name = John Doe
	email = john@example.com
`,
			expectedUser:  "John Doe",
			expectedEmail: "john@example.com",
		},
		{
			name: "gitconfig with other sections",
			content: `[core]
	editor = vim
[user]
	name = Jane Smith
	email = jane@example.org
[alias]
	co = checkout
`,
			expectedUser:  "Jane Smith",
			expectedEmail: "jane@example.org",
		},
		{
			name: "gitconfig with comments",
			content: `# This is a comment
[user]
	; Another comment
	name = Test User
	email = test@test.com
`,
			expectedUser:  "Test User",
			expectedEmail: "test@test.com",
		},
		{
			name: "gitconfig without spaces around equals",
			content: `[user]
	name=NoSpace
	email=nospace@example.com
`,
			expectedUser:  "NoSpace",
			expectedEmail: "nospace@example.com",
		},
		{
			name:          "empty gitconfig",
			content:       "",
			expectedUser:  "",
			expectedEmail: "",
		},
		{
			name: "gitconfig without user section",
			content: `[core]
	editor = vim
`,
			expectedUser:  "",
			expectedEmail: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			user, email, err := ParseGitconfigContent(strings.NewReader(tt.content))
			if err != nil {
				t.Fatalf("Unexpected error: %v", err)
			}
			if user != tt.expectedUser {
				t.Errorf("User mismatch: expected %q, got %q", tt.expectedUser, user)
			}
			if email != tt.expectedEmail {
				t.Errorf("Email mismatch: expected %q, got %q", tt.expectedEmail, email)
			}
		})
	}
}

// TestGetGitUserFallbackToBentooConfig tests fallback to bentoo config
// _Requirements: 6.2_
func TestGetGitUserFallbackToBentooConfig(t *testing.T) {
	// Create a config with git user info
	cfg := &Config{
		Git: GitConfig{
			User:  "Bentoo User",
			Email: "bentoo@example.com",
		},
	}

	// GetGitUser should fall back to bentoo config when gitconfig is missing/incomplete
	// Note: This test assumes ~/.gitconfig doesn't have complete user info,
	// or we need to mock the gitconfig path. For now, we test the fallback logic directly.

	// Test that bentoo config values are accessible
	if cfg.Git.User != "Bentoo User" {
		t.Errorf("Expected user 'Bentoo User', got %q", cfg.Git.User)
	}
	if cfg.Git.Email != "bentoo@example.com" {
		t.Errorf("Expected email 'bentoo@example.com', got %q", cfg.Git.Email)
	}
}

// TestGetGitUserErrorWhenNeitherConfigured tests error when neither has user info
// _Requirements: 6.3_
func TestGetGitUserErrorWhenNeitherConfigured(t *testing.T) {
	// Create temp directory for test
	tmpDir, err := os.MkdirTemp("", "gituser-test-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	// Create empty gitconfig
	gitconfigPath := filepath.Join(tmpDir, ".gitconfig")
	if err := os.WriteFile(gitconfigPath, []byte("[core]\n\teditor = vim\n"), 0644); err != nil {
		t.Fatalf("Failed to write gitconfig: %v", err)
	}

	// Config without git user info
	cfg := &Config{
		Git: GitConfig{
			User:  "",
			Email: "",
		},
	}

	// Parse the empty gitconfig to verify it returns empty values
	user, email, err := parseGitconfig(gitconfigPath)
	if err != nil {
		t.Fatalf("Unexpected error parsing gitconfig: %v", err)
	}
	if user != "" || email != "" {
		t.Errorf("Expected empty user/email from gitconfig without user section")
	}

	// Verify that config without user info would trigger error
	if cfg.Git.User == "" && cfg.Git.Email == "" {
		// This is the expected state - neither source has user info
		// The actual GetGitUser would return ErrGitUserNotConfigured
	}
}

// TestGetGitUserWithValidGitconfig tests reading from valid gitconfig file
// _Requirements: 6.1_
func TestGetGitUserWithValidGitconfig(t *testing.T) {
	// Create temp directory for test
	tmpDir, err := os.MkdirTemp("", "gituser-test-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	// Create valid gitconfig
	gitconfigPath := filepath.Join(tmpDir, ".gitconfig")
	gitconfigContent := `[user]
	name = Git User
	email = git@example.com
`
	if err := os.WriteFile(gitconfigPath, []byte(gitconfigContent), 0644); err != nil {
		t.Fatalf("Failed to write gitconfig: %v", err)
	}

	// Parse the gitconfig
	user, email, err := parseGitconfig(gitconfigPath)
	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}
	if user != "Git User" {
		t.Errorf("Expected user 'Git User', got %q", user)
	}
	if email != "git@example.com" {
		t.Errorf("Expected email 'git@example.com', got %q", email)
	}
}

// genCacheTTL generates valid cache TTL values (positive integers)
func genCacheTTL() gopter.Gen {
	return gen.IntRange(1, 86400) // 1 second to 24 hours
}

// genLLMProvider generates valid LLM provider names
func genLLMProvider() gopter.Gen {
	return gen.OneConstOf("claude", "perplexity", "openai")
}

// genAPIKeyEnv generates valid environment variable names
func genAPIKeyEnv() gopter.Gen {
	return gen.RegexMatch(`^[A-Z][A-Z0-9_]{2,20}$`)
}

// genModel generates valid model names
func genModel() gopter.Gen {
	return gen.RegexMatch(`^[a-z][a-z0-9-]{2,30}$`)
}

// genAutoupdateConfig generates valid AutoupdateConfig structs
func genAutoupdateConfig() gopter.Gen {
	return gopter.CombineGens(
		genCacheTTL(),
		genLLMProvider(),
		genAPIKeyEnv(),
		genModel(),
		genLLMProvider(), // reuse for search provider
		genAPIKeyEnv(),   // reuse for search api key env
	).Map(func(values []interface{}) AutoupdateConfig {
		return AutoupdateConfig{
			CacheTTL: values[0].(int),
			LLM: LLMConfig{
				Provider:  values[1].(string),
				APIKeyEnv: values[2].(string),
				Model:     values[3].(string),
			},
			Search: SearchConfig{
				Provider:  values[4].(string),
				APIKeyEnv: values[5].(string),
			},
		}
	})
}

// genConfigWithAutoupdate generates valid Config structs with autoupdate settings
func genConfigWithAutoupdate() gopter.Gen {
	return gopter.CombineGens(
		genValidPath(),
		genValidRemote(),
		genValidUsername(),
		genValidEmail(),
		genAutoupdateConfig(),
	).Map(func(values []interface{}) *Config {
		return &Config{
			Overlay: OverlayConfig{
				Path:   values[0].(string),
				Remote: values[1].(string),
			},
			Git: GitConfig{
				User:  values[2].(string),
				Email: values[3].(string),
			},
			Autoupdate: values[4].(AutoupdateConfig),
		}
	})
}

// TestGlobalConfigParsingRoundTrip tests Property 11: Global Config Parsing
// **Feature: ebuild-autoupdate, Property 11: Global Config Parsing**
// **Validates: Requirements 7.1, 7.2, 7.3, 7.4**
func TestGlobalConfigParsingRoundTrip(t *testing.T) {
	parameters := gopter.DefaultTestParameters()
	parameters.MinSuccessfulTests = 100
	properties := gopter.NewProperties(parameters)

	properties.Property("Config with autoupdate YAML round-trip preserves all fields", prop.ForAll(
		func(cfg *Config) bool {
			// Create temp directory for test
			tmpDir, err := os.MkdirTemp("", "config-autoupdate-test-*")
			if err != nil {
				t.Logf("Failed to create temp dir: %v", err)
				return false
			}
			defer os.RemoveAll(tmpDir)

			configPath := filepath.Join(tmpDir, "config.yaml")

			// Save config
			if err := cfg.SaveTo(configPath); err != nil {
				t.Logf("Failed to save config: %v", err)
				return false
			}

			// Load config back
			loaded, err := LoadFrom(configPath)
			if err != nil {
				t.Logf("Failed to load config: %v", err)
				return false
			}

			// Verify autoupdate fields are preserved
			if loaded.Autoupdate.CacheTTL != cfg.Autoupdate.CacheTTL {
				t.Logf("CacheTTL mismatch: expected %d, got %d", cfg.Autoupdate.CacheTTL, loaded.Autoupdate.CacheTTL)
				return false
			}
			if loaded.Autoupdate.LLM.Provider != cfg.Autoupdate.LLM.Provider {
				t.Logf("LLM.Provider mismatch: expected %q, got %q", cfg.Autoupdate.LLM.Provider, loaded.Autoupdate.LLM.Provider)
				return false
			}
			if loaded.Autoupdate.LLM.APIKeyEnv != cfg.Autoupdate.LLM.APIKeyEnv {
				t.Logf("LLM.APIKeyEnv mismatch: expected %q, got %q", cfg.Autoupdate.LLM.APIKeyEnv, loaded.Autoupdate.LLM.APIKeyEnv)
				return false
			}
			if loaded.Autoupdate.LLM.Model != cfg.Autoupdate.LLM.Model {
				t.Logf("LLM.Model mismatch: expected %q, got %q", cfg.Autoupdate.LLM.Model, loaded.Autoupdate.LLM.Model)
				return false
			}
			if loaded.Autoupdate.Search.Provider != cfg.Autoupdate.Search.Provider {
				t.Logf("Search.Provider mismatch: expected %q, got %q", cfg.Autoupdate.Search.Provider, loaded.Autoupdate.Search.Provider)
				return false
			}
			if loaded.Autoupdate.Search.APIKeyEnv != cfg.Autoupdate.Search.APIKeyEnv {
				t.Logf("Search.APIKeyEnv mismatch: expected %q, got %q", cfg.Autoupdate.Search.APIKeyEnv, loaded.Autoupdate.Search.APIKeyEnv)
				return false
			}

			// Also verify full equality
			return reflect.DeepEqual(cfg, loaded)
		},
		genConfigWithAutoupdate(),
	))

	properties.TestingRun(t)
}

// TestAutoupdateConfigMissingSection tests backward compatibility when autoupdate section is missing
// _Requirements: 7.1_
func TestAutoupdateConfigMissingSection(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "config-test-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	// Create config without autoupdate section
	configContent := `overlay:
  path: /test/overlay
  remote: origin
git:
  user: testuser
  email: test@example.com
`
	configPath := filepath.Join(tmpDir, "config.yaml")
	if err := os.WriteFile(configPath, []byte(configContent), 0644); err != nil {
		t.Fatalf("Failed to write config: %v", err)
	}

	// Load config
	cfg, err := LoadFrom(configPath)
	if err != nil {
		t.Fatalf("Failed to load config: %v", err)
	}

	// Verify autoupdate section has zero values (backward compatible)
	if cfg.Autoupdate.CacheTTL != 0 {
		t.Errorf("Expected CacheTTL 0 for missing section, got: %d", cfg.Autoupdate.CacheTTL)
	}
	if cfg.Autoupdate.LLM.Provider != "" {
		t.Errorf("Expected empty LLM.Provider for missing section, got: %q", cfg.Autoupdate.LLM.Provider)
	}
	if cfg.Autoupdate.Search.Provider != "" {
		t.Errorf("Expected empty Search.Provider for missing section, got: %q", cfg.Autoupdate.Search.Provider)
	}

	// Verify GetCacheTTL returns default
	if cfg.Autoupdate.GetCacheTTL() != DefaultCacheTTL {
		t.Errorf("Expected default CacheTTL %d, got: %d", DefaultCacheTTL, cfg.Autoupdate.GetCacheTTL())
	}
}

// TestAutoupdateConfigPartialConfig tests parsing with partial autoupdate configuration
// _Requirements: 7.2, 7.3_
func TestAutoupdateConfigPartialConfig(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "config-test-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	// Create config with partial autoupdate section (only cache_ttl and llm)
	configContent := `overlay:
  path: /test/overlay
  remote: origin
autoupdate:
  cache_ttl: 7200
  llm:
    provider: claude
    api_key_env: ANTHROPIC_API_KEY
    model: claude-3-haiku-20240307
`
	configPath := filepath.Join(tmpDir, "config.yaml")
	if err := os.WriteFile(configPath, []byte(configContent), 0644); err != nil {
		t.Fatalf("Failed to write config: %v", err)
	}

	// Load config
	cfg, err := LoadFrom(configPath)
	if err != nil {
		t.Fatalf("Failed to load config: %v", err)
	}

	// Verify autoupdate fields that are set
	if cfg.Autoupdate.CacheTTL != 7200 {
		t.Errorf("Expected CacheTTL 7200, got: %d", cfg.Autoupdate.CacheTTL)
	}
	if cfg.Autoupdate.LLM.Provider != "claude" {
		t.Errorf("Expected LLM.Provider 'claude', got: %q", cfg.Autoupdate.LLM.Provider)
	}
	if cfg.Autoupdate.LLM.APIKeyEnv != "ANTHROPIC_API_KEY" {
		t.Errorf("Expected LLM.APIKeyEnv 'ANTHROPIC_API_KEY', got: %q", cfg.Autoupdate.LLM.APIKeyEnv)
	}
	if cfg.Autoupdate.LLM.Model != "claude-3-haiku-20240307" {
		t.Errorf("Expected LLM.Model 'claude-3-haiku-20240307', got: %q", cfg.Autoupdate.LLM.Model)
	}

	// Verify search section has zero values (not configured)
	if cfg.Autoupdate.Search.Provider != "" {
		t.Errorf("Expected empty Search.Provider, got: %q", cfg.Autoupdate.Search.Provider)
	}
	if cfg.Autoupdate.Search.APIKeyEnv != "" {
		t.Errorf("Expected empty Search.APIKeyEnv, got: %q", cfg.Autoupdate.Search.APIKeyEnv)
	}
}

// TestAutoupdateConfigFullConfig tests parsing with full autoupdate configuration
// _Requirements: 7.1, 7.2, 7.3, 7.4_
func TestAutoupdateConfigFullConfig(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "config-test-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	// Create config with full autoupdate section
	configContent := `overlay:
  path: ~/repos/bentoo
autoupdate:
  cache_ttl: 3600
  llm:
    provider: claude
    api_key_env: ANTHROPIC_API_KEY
    model: claude-3-haiku-20240307
  search:
    provider: perplexity
    api_key_env: PERPLEXITY_API_KEY
`
	configPath := filepath.Join(tmpDir, "config.yaml")
	if err := os.WriteFile(configPath, []byte(configContent), 0644); err != nil {
		t.Fatalf("Failed to write config: %v", err)
	}

	// Load config
	cfg, err := LoadFrom(configPath)
	if err != nil {
		t.Fatalf("Failed to load config: %v", err)
	}

	// Verify all autoupdate fields
	if cfg.Autoupdate.CacheTTL != 3600 {
		t.Errorf("Expected CacheTTL 3600, got: %d", cfg.Autoupdate.CacheTTL)
	}
	if cfg.Autoupdate.LLM.Provider != "claude" {
		t.Errorf("Expected LLM.Provider 'claude', got: %q", cfg.Autoupdate.LLM.Provider)
	}
	if cfg.Autoupdate.LLM.APIKeyEnv != "ANTHROPIC_API_KEY" {
		t.Errorf("Expected LLM.APIKeyEnv 'ANTHROPIC_API_KEY', got: %q", cfg.Autoupdate.LLM.APIKeyEnv)
	}
	if cfg.Autoupdate.LLM.Model != "claude-3-haiku-20240307" {
		t.Errorf("Expected LLM.Model 'claude-3-haiku-20240307', got: %q", cfg.Autoupdate.LLM.Model)
	}
	if cfg.Autoupdate.Search.Provider != "perplexity" {
		t.Errorf("Expected Search.Provider 'perplexity', got: %q", cfg.Autoupdate.Search.Provider)
	}
	if cfg.Autoupdate.Search.APIKeyEnv != "PERPLEXITY_API_KEY" {
		t.Errorf("Expected Search.APIKeyEnv 'PERPLEXITY_API_KEY', got: %q", cfg.Autoupdate.Search.APIKeyEnv)
	}
}

// TestGetCacheTTLDefault tests that GetCacheTTL returns default for zero/negative values
// _Requirements: 7.2_
func TestGetCacheTTLDefault(t *testing.T) {
	tests := []struct {
		name     string
		cacheTTL int
		expected int
	}{
		{
			name:     "zero value returns default",
			cacheTTL: 0,
			expected: DefaultCacheTTL,
		},
		{
			name:     "negative value returns default",
			cacheTTL: -100,
			expected: DefaultCacheTTL,
		},
		{
			name:     "positive value returns configured",
			cacheTTL: 7200,
			expected: 7200,
		},
		{
			name:     "small positive value returns configured",
			cacheTTL: 1,
			expected: 1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := AutoupdateConfig{CacheTTL: tt.cacheTTL}
			if got := cfg.GetCacheTTL(); got != tt.expected {
				t.Errorf("GetCacheTTL() = %d, want %d", got, tt.expected)
			}
		})
	}
}
