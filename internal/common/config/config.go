package config

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/obentoo/bentoolkit/internal/common/secrets"
	"gopkg.in/yaml.v3"
)

var (
	ErrOverlayPathNotSet       = errors.New("overlay path is not configured")
	ErrOverlayPathNotFound     = errors.New("overlay path does not exist")
	ErrOverlayInvalidStructure = errors.New("overlay structure is invalid")
	ErrGitUserNotConfigured    = errors.New("git user is not configured: set user.name and user.email in ~/.gitconfig or bentoo config")
	// ErrOverrideMissingReason marks a per-package validation override that
	// states a depth but not why (S033-R2.5). It is a sentinel so a caller can
	// match the class of problem; the wrapped message always names the package.
	ErrOverrideMissingReason = errors.New("per-package validate override has no reason")
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

// RepoConfig holds configuration for a custom repository.
//
// A per-repository auth token is no longer a field here: it now resolves from
// BENTOO_REPO_<NAME>_TOKEN via the secrets chain (see internal/common/secrets and
// cmd/bentoo's repoTokenName). A legacy `token:` key left in config.yaml is
// surfaced by the migration diagnostic in LoadFrom and otherwise ignored.
type RepoConfig struct {
	Provider string `yaml:"provider"` // "github", "gitlab", "git", or "local"
	URL      string `yaml:"url"`      // Full URL or org/repo for GitHub/GitLab/git (remote)
	Path     string `yaml:"path"`     // On-disk tree for provider "local" (read in place, no clone)
	Branch   string `yaml:"branch"`   // Branch to use (default: master/main)
}

// AutoupdateConfig holds autoupdate-specific settings
type AutoupdateConfig struct {
	CacheTTL    int          `yaml:"cache_ttl"`    // Cache TTL in seconds (default: 3600)
	HTTPTimeout int          `yaml:"http_timeout"` // Per-request HTTP timeout in seconds (default: 30)
	LLM         LLMConfig    `yaml:"llm"`          // LLM provider configuration
	Search      SearchConfig `yaml:"search"`       // Search provider configuration
	// Distdir is the directory `pkgdev manifest` is given as --distdir on the
	// autoupdate path — the same name and the same meaning the `overlay
	// manifest` command's --distdir already has (S030-R1.3). Unset (the
	// default) means the host's own DISTDIR is used, which is what
	// `portageq distdir` reports.
	Distdir string `yaml:"distdir,omitempty"`
	// DistfilesCache is the read-only distfiles cache consulted before a
	// download, again under the name `overlay manifest` already uses
	// (S030-R1.3). Unset means the built-in default (/var/cache/distfiles).
	// The cache is only ever read from: files are symlinked into the working
	// distdir, never written back.
	DistfilesCache string `yaml:"distfiles_cache,omitempty"`
	// Validate is the staged-bump validation policy (S033-R2). It is nested
	// here rather than at the top level because everything it governs happens
	// during an autoupdate.
	Validate ValidateConfig `yaml:"validate,omitempty"`
}

// ValidateConfig is the staged-bump validation policy: how much of a bump gets
// checked before it is published, and on whose stated authority a package gets
// checked less (S033-R2).
//
// WHY IT LIVES HERE AND NOT IN THE OVERLAY'S packages.toml (S033-D10).
// packages.toml sits inside the overlay, which auto-commits and publishes, and
// a key there that runs ahead of the installed binary silently DISABLES the
// record carrying it. A policy deciding how much a bump is validated would then
// switch itself off without a word — the one failure mode it must not have. In
// this file the same typo is a line on stderr from the strict re-decode in
// LoadFrom and the run continues.
//
// Every default is answered by a getter and never by a struct literal, so an
// absent block, a half-written block and a nil pointer all resolve to the same
// documented table:
//
//	depths.revision    options     depths.series     configure
//	depths.patch       options     depths.major      configure
//	require_isolation  false       require_proof     false
//	review             false       fix_on_failure    false
//	timeout            3600 (seconds)
//
// `compile` is never a default. The ladder's expensive rung is reached through
// --depth/--compile or a per-package override that states its reason, because
// validating everything at compile depth does not become expensive, it becomes
// ignored.
type ValidateConfig struct {
	// Depths is the per-class depth table, keyed by the bump classes
	// internal/autoupdate/validate.Classify produces. A class left unset
	// answers its documented default, never the empty string.
	Depths ValidateDepths `yaml:"depths,omitempty"`
	// RequireIsolation makes a compile gate SKIP rather than run unisolated,
	// so a pass never claims a fidelity it did not have. It defaults to false
	// because `unshare --net` is denied on ordinary hosts, and a default of
	// true would turn every build gate into a SKIPPED (S033-R6.6, D11 — story
	// 031's --require-isolation default, unchanged now that it has a key).
	RequireIsolation bool `yaml:"require_isolation,omitempty"`
	// RequireProof refuses to publish a bump whose validation could not reach
	// the depth policy asked for. It defaults to false: an ordinary
	// workstation rarely holds every build dependency, so a default of true
	// would refuse most bumps for a reason that says nothing about them
	// (S033-D6, R3.12). The unreached depth is reported either way.
	RequireProof bool `yaml:"require_proof,omitempty"`
	// Review and FixOnFailure are the two LLM capabilities, and they are
	// TRI-STATE on purpose. The capabilities are enabled by --llm; this
	// configuration can only SUBTRACT from that flag, never add to it
	// (R7.1/R7.2). A plain bool cannot express "leave it to the flag" and
	// "switch this one back off" as different things, so an unset key stays
	// nil and only an explicit `review: false` disables. GetReview reports the
	// configured value alone (false unless the file says true); the flag layer
	// combines the two through ReviewDisabled.
	Review *bool `yaml:"review,omitempty"`
	// FixOnFailure is the build fixer, tri-state for the same reason as
	// Review above.
	FixOnFailure *bool `yaml:"fix_on_failure,omitempty"`
	// Timeout bounds a single agent run, in SECONDS — the same unit and shape
	// as cache_ttl and http_timeout above, because YAML has no duration
	// literal and an int of seconds is what this file already speaks. It is
	// read by the reviewer and by the build fixer: an agent with no deadline
	// hangs an unattended sweep. Unset or non-positive means the default.
	Timeout int `yaml:"timeout,omitempty"`
	// Packages holds per-package overrides, keyed by category/name. An
	// override supplies the depth to use and the reason it is used (R2.4); an
	// override with no reason is reported invalid, naming the package (R2.5),
	// because an override is the one input that can quietly reduce how much a
	// bump is checked.
	//
	// The packages nobody wants to build from source ship AS ORDINARY ENTRIES
	// in this map (see DefaultPackageOverrides), not as a hardcoded tier list,
	// so R2.4 and R2.5 reach them exactly as they reach anything an operator
	// writes — and so the report that says why a bump was not validated can
	// quote their reason like any other.
	Packages map[string]ValidatePackageOverride `yaml:"packages,omitempty"`
}

// ValidateDepths is the per-class depth table under `autoupdate.validate.depths`.
//
// Each value is a rung spelled exactly as
// internal/autoupdate/validate.ParseDepth accepts it: none, options, patches,
// configure, compile, install — shallowest first, each including every rung
// before it. This package does not validate them; ParseDepth is the validator,
// and the import runs the other way.
//
// It is a struct and not a map[string]string so that a mistyped CLASS name
// ("revison:") is caught by the strict re-decode in LoadFrom and reported on
// stderr. A map would accept the typo silently and the class it was meant for
// would quietly keep its default — which is the packages.toml failure mode this
// block exists to avoid.
type ValidateDepths struct {
	Revision string `yaml:"revision,omitempty"`
	Patch    string `yaml:"patch,omitempty"`
	Series   string `yaml:"series,omitempty"`
	Major    string `yaml:"major,omitempty"`
}

// ValidatePackageOverride is one entry of autoupdate.validate.packages: the
// depth to use for this package and the reason for using it.
//
// Both fields are reported beside the outcome, so a bump validated at a
// shallower depth than its class calls for can always say on whose authority
// (R2.4).
type ValidatePackageOverride struct {
	// Depth is a rung of the validation ladder, spelled exactly as
	// internal/autoupdate/validate.ParseDepth accepts it: none, options,
	// patches, configure, compile, install. It is stored and reported verbatim — this
	// package deliberately neither trims nor case-folds it, because ParseDepth
	// does not either, and a config layer that silently repaired " Compile "
	// would make the name in the file and the name in the report two different
	// strings.
	Depth string `yaml:"depth"`
	// Reason is why this package departs from its class depth. It is required:
	// see OverrideErrors.
	Reason string `yaml:"reason"`
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

// legacyRepo mirrors a repositories.* entry for the STRICT probe only. It
// retains the removed `token` key so the strict decode does not flag it as
// unknown; the migration diagnostic reports it with an actionable message
// instead. The other fields mirror RepoConfig so a real entry decodes cleanly.
type legacyRepo struct {
	Provider string `yaml:"provider"`
	URL      string `yaml:"url"`
	Path     string `yaml:"path"`
	Token    string `yaml:"token"`
	Branch   string `yaml:"branch"`
}

// probeConfig is the STRICT re-decode target. It deliberately does NOT inline
// Config: Config already carries a `repositories` field, and inlining it beside
// probeConfig's own legacyRepo `repositories` map makes go-yaml panic with
// "duplicated key 'repositories' in struct" (yaml.v3 re-panics that error out of
// Decode, which would crash LoadFrom). So Config's top-level keys are listed
// explicitly here, reusing Config's own sub-types so the strict decode stays
// exactly as strict as a Config decode. Two removed-but-retained keys —
// github.token and repositories.*.token — are kept so the strict decode does not
// report them as unknown; the migration diagnostic reports each with an
// actionable message instead.
type probeConfig struct {
	Overlay      OverlayConfig         `yaml:"overlay"`
	Git          GitConfig             `yaml:"git"`
	Autoupdate   AutoupdateConfig      `yaml:"autoupdate,omitempty"`
	Repositories map[string]legacyRepo `yaml:"repositories,omitempty"`
	GitHub       struct {
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

// secretDestination renders the "where this value belongs now" clause of a
// migration warning for the given env-var name.
//
// It resolves the target through secrets.UserPath rather than secrets.Paths()[0].
// Paths mirrors the resolution ORDER, so index 0 is the user-scope file only when
// one exists; with $HOME unresolvable the chain drops that entry and index 0
// becomes the root-owned /etc/bentoo/secrets, making the warning tell an
// unprivileged user to write a secret into a 0600 file they cannot open. Paths()
// is never empty, so that misdirection is silent (F-H). With no user-scope path
// the env var is named on its own instead.
func secretDestination(envName string) string {
	if path, ok := secrets.UserPath(); ok {
		return fmt.Sprintf("Move it to %s as `%s=<value>` (chmod 600)", path, envName)
	}
	return fmt.Sprintf("Set `%s=<value>` in the environment", envName)
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
			// After the save, so the file this creates stays as small as it is
			// today while the returned config still carries the shipped
			// validation overrides: a host with no config.yaml must not be the
			// one host where an unattended sweep picks a build depth for
			// chromium.
			cfg.Autoupdate.Validate.normalize()
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
	if probe.GitHub.Token != "" {
		fmt.Fprintf(os.Stderr,
			"warning: %s: `github.token` is no longer read. %s, then delete the key. Until then, GitHub requests are unauthenticated.\n",
			path, secretDestination("GITHUB_TOKEN"))
	}
	// repositories.*.token is detected on the strict probe (probe.Repositories,
	// whose legacyRepo retains the removed `token` key) rather than on cfg, which
	// no longer carries a per-repo token field.
	for name, repo := range probe.Repositories {
		if repo.Token != "" {
			fmt.Fprintf(os.Stderr,
				"warning: %s: `repositories.%s.token` is no longer read. %s, then delete the key.\n",
				path, name, secretDestination(repoTokenEnvName(name)))
		}
	}

	cfg.Autoupdate.LLM.normalize()
	cfg.Autoupdate.Validate.normalize()

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

// GetDistdir returns autoupdate.distdir with surrounding whitespace removed, or
// "" when the key is unset.
//
// Whitespace is stripped rather than preserved because this value becomes a
// DIRECTORY that distfiles are downloaded into: " /var/cache/distfiles" is a
// typo in a hand-edited YAML file, not a request for a directory whose name
// begins with a space. "" is returned unchanged and means "not configured" —
// the caller then falls through to the next rung of the precedence, never to a
// path this function invented.
func (c *AutoupdateConfig) GetDistdir() string {
	if c == nil {
		return ""
	}
	return strings.TrimSpace(c.Distdir)
}

// GetDistfilesCache returns autoupdate.distfiles_cache with surrounding
// whitespace removed, or "" when the key is unset.
//
// Unlike GetCacheTTL and GetHTTPTimeout this deliberately does NOT substitute a
// default: the flag layer owns that decision, because there the difference
// between "unset" and "set to the empty string" is observable (pflag's Changed)
// and the empty string is the documented way to DISABLE the cache. Answering
// with a default here would make a config file unable to express anything the
// flag can.
func (c *AutoupdateConfig) GetDistfilesCache() string {
	if c == nil {
		return ""
	}
	return strings.TrimSpace(c.DistfilesCache)
}

// The default depth for each bump class (S033-R2). revision and patch are the
// cheap, frequent bumps and stop at the static gate; series and major cross a
// boundary upstream chose to mark, so they earn a configure. No class defaults
// to compile — see ValidateConfig's doc comment.
//
// THEY DID NOT MOVE WHEN THE INSTALL RUNG WAS ADDED, and that is a decision
// rather than an omission (S042-R1.4). Adding a rung is a capability; promoting
// a default is a cost decision belonging to whoever pays for the hours, and an
// operator who upgrades the binary without reading the changelog must find
// yesterday's sweep costing what it cost yesterday. `install` is reached by
// --depth=install or by an explicit key here, never by default.
const (
	DefaultDepthRevision = "options"
	DefaultDepthPatch    = "options"
	DefaultDepthSeries   = "configure"
	DefaultDepthMajor    = "configure"
)

// DefaultValidateTimeout is the default per-agent budget in seconds (1 hour),
// bounding the reviewer and the build fixer.
const DefaultValidateTimeout = 3600

// GetDepthForClass returns the configured depth for a bump class, falling back
// to that class's documented default.
//
// It NEVER returns the empty string, for any input: the resolver downstream
// always has a depth to work with, and an unrecognized class is answered as the
// riskiest known one (major) rather than as "no validation", because failing to
// classify a bump is not evidence that the bump is small.
//
// The class name is matched case-insensitively and trimmed — it is a lookup key
// this package chooses, not a value it reports. The configured VALUE is
// returned verbatim, so a typo in a depth name reaches validate.ParseDepth and
// is rejected there by name instead of being silently repaired here.
func (c *ValidateConfig) GetDepthForClass(class string) string {
	depths := c.depths()

	switch strings.ToLower(strings.TrimSpace(class)) {
	case "revision":
		return depthOrDefault(depths.Revision, DefaultDepthRevision)
	case "patch":
		return depthOrDefault(depths.Patch, DefaultDepthPatch)
	case "series":
		return depthOrDefault(depths.Series, DefaultDepthSeries)
	default:
		// "major" and anything unrecognized.
		return depthOrDefault(depths.Major, DefaultDepthMajor)
	}
}

// depths reads the depth table through a possibly nil receiver.
func (c *ValidateConfig) depths() ValidateDepths {
	if c == nil {
		return ValidateDepths{}
	}
	return c.Depths
}

// depthOrDefault treats an unset key and an empty value alike: both mean "not
// configured". A depth of "none" — not "" — is how a class is switched off.
func depthOrDefault(configured, fallback string) string {
	if configured == "" {
		return fallback
	}
	return configured
}

// GetRequireIsolation reports whether a compile gate must SKIP rather than run
// unisolated. False by default, and false through a nil receiver: this is story
// 031's --require-isolation default, unchanged now that it also has a key
// (S033-R6.6).
func (c *ValidateConfig) GetRequireIsolation() bool {
	return c != nil && c.RequireIsolation
}

// GetRequireProof reports whether a bump whose validation could not reach the
// policy depth must be refused rather than published with the unreached depth
// named. False by default (S033-D6).
func (c *ValidateConfig) GetRequireProof() bool {
	return c != nil && c.RequireProof
}

// enabledExplicitly reports a tri-state key written as true. An unset key reads
// as not enabled, which is what every default in this block is.
func enabledExplicitly(v *bool) bool {
	return v != nil && *v
}

// disabledExplicitly reports a tri-state key written as false — the only way a
// config file can SUBTRACT a capability from a run started with --llm. An unset
// key is not a disabled key, which is the whole reason these two are *bool.
func disabledExplicitly(v *bool) bool {
	return v != nil && !*v
}

// GetReview reports the CONFIGURED value of the reviewer capability, which is
// false unless the file says otherwise. It is not the effective value: the
// capabilities are switched on by --llm and configuration only subtracts, so
// the flag layer combines this with ReviewDisabled.
func (c *ValidateConfig) GetReview() bool {
	return c != nil && enabledExplicitly(c.Review)
}

// ReviewDisabled reports an explicit `review: false`, which is the only way a
// config file can switch the reviewer back off for a run started with --llm.
func (c *ValidateConfig) ReviewDisabled() bool {
	return c != nil && disabledExplicitly(c.Review)
}

// GetFixOnFailure reports the CONFIGURED value of the build-fixer capability.
// See GetReview for why this is not the effective value.
func (c *ValidateConfig) GetFixOnFailure() bool {
	return c != nil && enabledExplicitly(c.FixOnFailure)
}

// FixOnFailureDisabled reports an explicit `fix_on_failure: false`. See
// ReviewDisabled.
func (c *ValidateConfig) FixOnFailureDisabled() bool {
	return c != nil && disabledExplicitly(c.FixOnFailure)
}

// GetTimeout returns the per-agent budget as a duration, defaulting to
// DefaultValidateTimeout when unset or non-positive. The stored key is an int
// of seconds; the duration is what the callers (the reviewer and the build
// fixer) actually need.
func (c *ValidateConfig) GetTimeout() time.Duration {
	if c == nil || c.Timeout <= 0 {
		return DefaultValidateTimeout * time.Second
	}
	return time.Duration(c.Timeout) * time.Second
}

// OverrideErrors reports every per-package override that is invalid, one error
// per package, in a stable (sorted) order so a diagnostic does not reshuffle
// between runs.
//
// The rule enforced here is R2.5: an override must state its reason. It is
// reported, never fatal — the same treatment an unknown key gets — so one bad
// entry cannot cost the operator the rest of the file.
//
// The depth STRING is deliberately not validated here. validate.ParseDepth owns
// that vocabulary and rejects an unknown rung by name; duplicating the ladder in
// this package would put the same list in two places, and the copy would be the
// one that goes stale.
func (c *ValidateConfig) OverrideErrors() []error {
	if c == nil || len(c.Packages) == 0 {
		return nil
	}

	names := make([]string, 0, len(c.Packages))
	for name := range c.Packages {
		names = append(names, name)
	}
	sort.Strings(names)

	var errs []error
	for _, name := range names {
		if strings.TrimSpace(c.Packages[name].Reason) == "" {
			errs = append(errs, fmt.Errorf(
				"autoupdate.validate.packages.%s: %w; an override is the only input that can quietly reduce how much a bump is checked, so state why",
				name, ErrOverrideMissingReason))
		}
	}
	return errs
}

// DefaultPackageOverrides returns the per-package overrides bentoolkit ships
// with, as a fresh map the caller may keep and edit.
//
// These are the packages whose from-source build costs hours that an unattended
// sweep would otherwise spend without being asked. They are ORDINARY overrides
// carrying ordinary reasons — not a hardcoded tier list — so R2.4 (report the
// reason beside the outcome) and R2.5 (an override needs a reason) apply to them
// exactly as to anything an operator writes, and so an operator overrides one by
// writing the same package key with a depth and a reason of their own.
func DefaultPackageOverrides() map[string]ValidatePackageOverride {
	return map[string]ValidatePackageOverride{
		"www-client/chromium": {
			Depth:  "none",
			Reason: "a from-source build costs hours of machine time; the overlay carries no patches for it, so a bump is a version and a hash",
		},
		"app-office/libreoffice": {
			Depth:  "none",
			Reason: "hours of machine time for a bump the overlay does not patch",
		},
		"net-libs/webkit-gtk": {
			Depth:  "none",
			Reason: "one of the longest builds in the tree, and every reverse dependency pays for it again",
		},
		"dev-lang/ghc": {
			Depth:  "none",
			Reason: "bootstraps its own compiler, so even a configure gate pulls in the full toolchain build",
		},
	}
}

// normalize materializes the shipped per-package overrides that the loaded file
// does not already carry.
//
// Only Packages is materialized: every scalar default is answered by its getter,
// so an absent block and a nil pointer stay indistinguishable from a written
// one. A map cannot do that — Packages is read as a field by anything that
// enumerates the policy — so the shipped entries are merged in here, per key, and
// a key the operator wrote always wins whole (their entry replaces the shipped
// one; it is never patched with the shipped reason, or a reasonless override
// would inherit an excuse it never gave).
func (c *ValidateConfig) normalize() {
	if c == nil {
		return
	}
	shipped := DefaultPackageOverrides()
	if c.Packages == nil {
		c.Packages = make(map[string]ValidatePackageOverride, len(shipped))
	}
	for name, override := range shipped {
		if _, ok := c.Packages[name]; !ok {
			c.Packages[name] = override
		}
	}
}
