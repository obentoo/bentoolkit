package config

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/obentoo/bentoolkit/internal/common/secrets"
	"gopkg.in/yaml.v3"
)

var (
	ErrOverlayPathNotSet       = errors.New("overlay path is not configured")
	ErrOverlayPathNotFound     = errors.New("overlay path does not exist")
	ErrOverlayInvalidStructure = errors.New("overlay structure is invalid")
	ErrGitUserNotConfigured    = errors.New("git user is not configured: set user.name and user.email in ~/.gitconfig or bentoo config")
)

// Config represents the application configuration
type Config struct {
	Overlay      OverlayConfig          `yaml:"overlay"`
	Git          GitConfig              `yaml:"git"`
	Autoupdate   AutoupdateConfig       `yaml:"autoupdate,omitempty"`
	Repositories map[string]*RepoConfig `yaml:"repositories,omitempty"`
}

// OverlayConfig holds overlay-specific settings
type OverlayConfig struct {
	Path   string `yaml:"path"`
	Remote string `yaml:"remote"`
}

// GitConfig holds git user settings
type GitConfig struct {
	User  string `yaml:"user"`
	Email string `yaml:"email"`
}

// RepoConfig holds configuration for a custom repository
type RepoConfig struct {
	Provider string `yaml:"provider"` // "github", "gitlab", "git", or "local"
	URL      string `yaml:"url"`      // Full URL or org/repo for GitHub/GitLab/git (remote)
	Path     string `yaml:"path"`     // On-disk tree for provider "local" (read in place, no clone)
	Token    string `yaml:"token"`    // Optional auth token
	Branch   string `yaml:"branch"`   // Branch to use (default: master/main)
}

// AutoupdateConfig holds autoupdate-specific settings
type AutoupdateConfig struct {
	CacheTTL    int          `yaml:"cache_ttl"`    // Cache TTL in seconds (default: 3600)
	HTTPTimeout int          `yaml:"http_timeout"` // Per-request HTTP timeout in seconds (default: 30)
	LLM         LLMConfig    `yaml:"llm"`          // LLM provider configuration
	Search      SearchConfig `yaml:"search"`       // Search provider configuration
}

// LLMConfig holds LLM provider configuration for autoupdate
type LLMConfig struct {
	Provider     string  `yaml:"provider"`                 // LLM provider name (e.g., "claude")
	APIKeyEnv    string  `yaml:"api_key_env"`              // Environment variable name for API key
	Model        string  `yaml:"model"`                    // Model name to use
	Bare         string  `yaml:"bare,omitempty"`           // CLI bare-mode selector: "auto" (default), "true", or "false"
	MaxBudgetUSD float64 `yaml:"max_budget_usd,omitempty"` // Optional spend cap passed to the CLI provider via --max-budget-usd
}

// SearchConfig holds search provider configuration for autoupdate
type SearchConfig struct {
	Provider  string `yaml:"provider"`    // Search provider name (e.g., "perplexity")
	APIKeyEnv string `yaml:"api_key_env"` // Environment variable name for API key
}

// ConfigPaths returns all possible config file paths in priority order
// 1. ~/.config/bentoo/config.yaml (XDG standard - priority)
// 2. ~/.bentoo/config.yaml (legacy fallback)
func ConfigPaths() ([]string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, err
	}

	// Check XDG_CONFIG_HOME first, fallback to ~/.config
	xdgConfig := os.Getenv("XDG_CONFIG_HOME")
	if xdgConfig == "" {
		xdgConfig = filepath.Join(home, ".config")
	}

	return []string{
		filepath.Join(xdgConfig, "bentoo", "config.yaml"),
		filepath.Join(home, ".bentoo", "config.yaml"),
	}, nil
}

// DefaultConfigPath returns the default config file path (XDG standard)
func DefaultConfigPath() (string, error) {
	paths, err := ConfigPaths()
	if err != nil {
		return "", err
	}
	return paths[0], nil
}

// FindConfigPath returns the first existing config file path
// Returns the default path if no config file exists yet
func FindConfigPath() (string, error) {
	paths, err := ConfigPaths()
	if err != nil {
		return "", err
	}

	// Return first existing config file
	for _, path := range paths {
		if _, err := os.Stat(path); err == nil {
			return path, nil
		}
	}

	// No config exists, return default (XDG) path for creation
	return paths[0], nil
}

// Load reads configuration from the first available config file
// Priority: ~/.config/bentoo/config.yaml > ~/.bentoo/config.yaml
func Load() (*Config, error) {
	configPath, err := FindConfigPath()
	if err != nil {
		return nil, err
	}
	return LoadFrom(configPath)
}

// probeConfig mirrors Config but retains the removed legacy github.token key,
// so the strict decode does not report it as unknown; the migration diagnostic
// reports it with an actionable message instead. repositories.*.token is still
// a live RepoConfig field in this release, so it is read from cfg directly.
type probeConfig struct {
	Config `yaml:",inline"`
	GitHub struct {
		Token string `yaml:"token"`
	} `yaml:"github"`
}

// repoTokenEnvName derives the environment variable that now supplies a custom
// repository's auth token: BENTOO_REPO_<NAME>_TOKEN, with NAME upper-cased and
// every rune outside [A-Z0-9] replaced by '_'. This mirrors the resolver's
// convention (duplicated with cmd/bentoo's repoTokenName in a different
// package, which is acceptable for this small normalization).
func repoTokenEnvName(name string) string {
	var b strings.Builder
	for _, r := range strings.ToUpper(name) {
		if (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
		} else {
			b.WriteRune('_')
		}
	}
	return "BENTOO_REPO_" + b.String() + "_TOKEN"
}

// LoadFrom reads configuration from a specific file path
func LoadFrom(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			// Create default config
			cfg := &Config{
				Overlay: OverlayConfig{
					Path:   "",
					Remote: "origin",
				},
				Git: GitConfig{},
			}
			if saveErr := cfg.SaveTo(path); saveErr != nil {
				return nil, saveErr
			}
			return cfg, nil
		}
		return nil, err
	}

	var cfg Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, err
	}

	// yaml.v3 silently drops keys that map to no struct field — e.g. a token
	// placed under `overlay:` (which has no `token` field) vanishes without a
	// trace. A strict re-decode surfaces such genuine typos as a warning to
	// stderr; it never blocks loading, so the lenient cfg above stays
	// authoritative. The decode target is probeConfig (not Config) so the
	// removed legacy `github.token` key is NOT reported here as unknown — the
	// migration diagnostic below reports it with an actionable message instead.
	var probe probeConfig
	pd := yaml.NewDecoder(bytes.NewReader(data))
	pd.KnownFields(true)
	if perr := pd.Decode(&probe); perr != nil {
		fmt.Fprintf(os.Stderr, "warning: %s: %v\n", path, perr)
	}

	// Migration diagnostics (R4): a config still carrying a secret that this
	// release no longer reads gets exactly one actionable warning per key,
	// emitted here in LoadFrom so it always precedes any later SaveTo that would
	// silently drop the key. The secret VALUE is never printed.
	secretsPath := secrets.Paths()[0]
	if probe.GitHub.Token != "" {
		fmt.Fprintf(os.Stderr,
			"warning: %s: `github.token` is no longer read. Move it to %s as `GITHUB_TOKEN=<value>` (chmod 600), then delete the key. Until then, GitHub requests are unauthenticated.\n",
			path, secretsPath)
	}
	// NOTE: repositories.*.token is still a live RepoConfig field this release,
	// so it is read from cfg directly. When Task 6.1 deletes RepoConfig.Token,
	// this detection must move to a retained field on probeConfig (mirroring
	// probeConfig.GitHub) or it will stop compiling.
	for name, repo := range cfg.Repositories {
		if repo != nil && repo.Token != "" {
			fmt.Fprintf(os.Stderr,
				"warning: %s: `repositories.%s.token` is no longer read. Move it to %s as `%s=<value>` (chmod 600), then delete the key.\n",
				path, name, secretsPath, repoTokenEnvName(name))
		}
	}

	cfg.Autoupdate.LLM.normalize()

	return &cfg, nil
}

// normalize fills in defaults and coerces invalid values for the LLM config.
//
// The `bare` field accepts only "auto", "true", or "false". An unset value
// resolves to "auto". Any other value is leniently coerced to "auto" rather
// than rejected: this project treats config normalization as best-effort and
// never hard-errors on a stray value here, keeping a typo from aborting a
// whole autoupdate run (a downstream feature, the bare-mode flag, can still
// surface a clearer signal if needed).
func (c *LLMConfig) normalize() {
	switch c.Bare {
	case "auto", "true", "false":
		// Valid value — preserve verbatim.
	default:
		// Empty or unrecognized — default/coerce to "auto" (lenient).
		c.Bare = "auto"
	}
}

// Save writes configuration to the default config file
func (c *Config) Save() error {
	configPath, err := DefaultConfigPath()
	if err != nil {
		return err
	}
	return c.SaveTo(configPath)
}

// SaveTo writes configuration to a specific file path
func (c *Config) SaveTo(path string) error {
	// Ensure directory exists
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return err
	}

	data, err := yaml.Marshal(c)
	if err != nil {
		return err
	}

	return os.WriteFile(path, data, 0600)
}

// GetOverlayPath returns the validated overlay path
func (c *Config) GetOverlayPath() (string, error) {
	return c.getOverlayPathWithValidation(true)
}

// GetOverlayPathNoValidation returns the overlay path without structure validation.
// Use this when you need the path but don't require a valid overlay structure
// (e.g., for overlay init command).
func (c *Config) GetOverlayPathNoValidation() (string, error) {
	return c.getOverlayPathWithValidation(false)
}

// getOverlayPathWithValidation returns the overlay path with optional structure validation
func (c *Config) getOverlayPathWithValidation(validate bool) (string, error) {
	if c.Overlay.Path == "" {
		return "", ErrOverlayPathNotSet
	}

	// Expand home directory if needed
	path := c.Overlay.Path
	if len(path) > 0 && path[0] == '~' {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		path = filepath.Join(home, path[1:])
	}

	// Check if path exists
	info, err := os.Stat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return "", ErrOverlayPathNotFound
		}
		return "", err
	}

	if !info.IsDir() {
		return "", ErrOverlayPathNotFound
	}

	// Validate overlay structure if requested
	if validate {
		result := ValidateOverlayStructure(path)
		if !result.Valid {
			return "", &OverlayValidationError{
				Path:   path,
				Errors: result.Errors,
			}
		}
	}

	return path, nil
}

// GetGitUser returns the git user name and email.
// It first tries to read from ~/.gitconfig, then falls back to bentoo config.
func (c *Config) GetGitUser() (user, email string, err error) {
	// Try to read from ~/.gitconfig first
	gitconfigPath, err := defaultGitconfigPath()
	if err == nil {
		user, email, err = parseGitconfig(gitconfigPath)
		if err == nil && user != "" && email != "" {
			return user, email, nil
		}
	}

	// Fall back to bentoo config
	if c.Git.User != "" && c.Git.Email != "" {
		return c.Git.User, c.Git.Email, nil
	}

	// Neither source has complete user info
	return "", "", ErrGitUserNotConfigured
}

// defaultGitconfigPath returns the default gitconfig file path
func defaultGitconfigPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".gitconfig"), nil
}

// parseGitconfig reads user.name and user.email from a gitconfig file.
// The gitconfig file uses INI format.
func parseGitconfig(path string) (user, email string, err error) {
	file, err := os.Open(path)
	if err != nil {
		return "", "", err
	}
	defer file.Close() //nolint:errcheck

	return ParseGitconfigContent(file)
}

// ParseGitconfigContent parses gitconfig content from an io.Reader.
// Exported for testing purposes.
func ParseGitconfigContent(r interface{ Read([]byte) (int, error) }) (user, email string, err error) {
	scanner := bufio.NewScanner(r)
	inUserSection := false

	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())

		// Skip empty lines and comments
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, ";") {
			continue
		}

		// Check for section header
		if strings.HasPrefix(line, "[") && strings.HasSuffix(line, "]") {
			section := strings.ToLower(strings.Trim(line, "[]"))
			inUserSection = section == "user"
			continue
		}

		// Parse key-value pairs in user section
		if inUserSection {
			parts := strings.SplitN(line, "=", 2)
			if len(parts) != 2 {
				continue
			}
			key := strings.TrimSpace(strings.ToLower(parts[0]))
			value := strings.TrimSpace(parts[1])

			switch key {
			case "name":
				user = value
			case "email":
				email = value
			}
		}
	}

	if err := scanner.Err(); err != nil {
		return "", "", err
	}

	return user, email, nil
}

// OverlayValidationResult contains overlay validation results
type OverlayValidationResult struct {
	Valid    bool     // True if overlay structure is valid
	Errors   []string // Critical issues that prevent operation
	Warnings []string // Non-critical issues
}

// OverlayValidationError represents an overlay validation failure
type OverlayValidationError struct {
	Path   string
	Errors []string
}

func (e *OverlayValidationError) Error() string {
	msg := "overlay validation failed for " + e.Path + ":"
	for _, err := range e.Errors {
		msg += "\n  - " + err
	}
	msg += "\n\nSuggestion: run 'bentoo overlay init' or check the overlay path configuration"
	return msg
}

// ValidateOverlayStructure checks if a path is a valid Gentoo overlay.
// A valid overlay must have:
// - profiles/ directory
// - metadata/ directory
func ValidateOverlayStructure(path string) *OverlayValidationResult {
	result := &OverlayValidationResult{
		Valid:    true,
		Errors:   []string{},
		Warnings: []string{},
	}

	// Check for profiles/ directory
	profilesPath := filepath.Join(path, "profiles")
	if _, err := os.Stat(profilesPath); os.IsNotExist(err) {
		result.Valid = false
		result.Errors = append(result.Errors, "missing profiles/ directory")
	}

	// Check for metadata/ directory
	metadataPath := filepath.Join(path, "metadata")
	if _, err := os.Stat(metadataPath); os.IsNotExist(err) {
		result.Valid = false
		result.Errors = append(result.Errors, "missing metadata/ directory")
	}

	return result
}

// DefaultCacheTTL is the default cache TTL in seconds (1 hour)
const DefaultCacheTTL = 3600

// GetCacheTTL returns the cache TTL in seconds, using the default if not configured.
func (c *AutoupdateConfig) GetCacheTTL() int {
	if c.CacheTTL <= 0 {
		return DefaultCacheTTL
	}
	return c.CacheTTL
}

// DefaultHTTPTimeout is the default per-request HTTP timeout in seconds.
const DefaultHTTPTimeout = 30

// GetHTTPTimeout returns the per-request HTTP timeout in seconds, using the
// default when unset or non-positive. This is the cap on a single outbound
// attempt; the autoupdate checker derives the overall per-operation budget from
// it so the retry attempts fit within the deadline.
func (c *AutoupdateConfig) GetHTTPTimeout() int {
	if c.HTTPTimeout <= 0 {
		return DefaultHTTPTimeout
	}
	return c.HTTPTimeout
}
