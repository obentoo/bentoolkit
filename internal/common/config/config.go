package config

import (
	"bufio"
	"errors"
	"os"
	"path/filepath"
	"strings"

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
	GitHub       GitHubConfig           `yaml:"github"`
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

// GitHubConfig holds GitHub API settings
type GitHubConfig struct {
	Token string `yaml:"token"` // Personal access token for higher rate limits
}

// RepoConfig holds configuration for a custom repository
type RepoConfig struct {
	Provider string `yaml:"provider"` // "github", "gitlab", or "git"
	URL      string `yaml:"url"`      // Full URL or org/repo for GitHub/GitLab
	Token    string `yaml:"token"`    // Optional auth token
	Branch   string `yaml:"branch"`   // Branch to use (default: master/main)
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

	return &cfg, nil
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
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}

	data, err := yaml.Marshal(c)
	if err != nil {
		return err
	}

	return os.WriteFile(path, data, 0644)
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
	return parseGitconfigReader(path)
}

// parseGitconfigReader parses gitconfig from a file path
func parseGitconfigReader(path string) (user, email string, err error) {
	file, err := os.Open(path)
	if err != nil {
		return "", "", err
	}
	defer file.Close()

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
