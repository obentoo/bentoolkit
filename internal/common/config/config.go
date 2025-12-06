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
	ErrOverlayPathNotSet   = errors.New("overlay path is not configured")
	ErrOverlayPathNotFound = errors.New("overlay path does not exist")
	ErrGitUserNotConfigured = errors.New("git user is not configured: set user.name and user.email in ~/.gitconfig or bentoo config")
)

// Config represents the application configuration
type Config struct {
	Overlay OverlayConfig `yaml:"overlay"`
	Git     GitConfig     `yaml:"git"`
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

// DefaultConfigPath returns the default config file path
func DefaultConfigPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".config", "bentoo", "config.yaml"), nil
}

// Load reads configuration from the default config file
func Load() (*Config, error) {
	configPath, err := DefaultConfigPath()
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
