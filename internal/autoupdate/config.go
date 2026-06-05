// Package autoupdate provides configuration management for ebuild autoupdate.
package autoupdate

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/BurntSushi/toml"
)

// Error variables for configuration errors
var (
	// ErrPackagesConfigNotFound is returned when packages.toml is not found in the overlay
	ErrPackagesConfigNotFound = errors.New("packages.toml not found in overlay")
	// ErrInvalidParserType is returned when an invalid parser type is specified
	ErrInvalidParserType = errors.New("invalid parser type: must be 'json', 'regex', 'html', or 'script'")
	// ErrMissingURL is returned when a package configuration is missing the required URL field
	ErrMissingURL = errors.New("missing required field: url")
	// ErrMissingParser is returned when a package configuration is missing the required parser field
	ErrMissingParser = errors.New("missing required field: parser")
	// ErrMissingPath is returned when a JSON parser is missing the required path field
	ErrMissingPath = errors.New("missing required field: path (required for json parser)")
	// ErrMissingPattern is returned when a regex parser is missing the required pattern field
	ErrMissingPattern = errors.New("missing required field: pattern (required for regex parser)")
	// ErrMissingSelectorOrXPath is returned when an HTML parser is missing both selector and xpath fields
	ErrMissingSelectorOrXPath = errors.New("missing required field: selector or xpath (required for html parser)")
	// ErrMissingScript is returned when a script parser is missing the required script field
	ErrMissingScript = errors.New("missing required field: script (required for script parser)")
	// ErrInvalidSelect is returned when the select field has an unsupported value
	ErrInvalidSelect = errors.New("invalid select value: must be '', 'first', 'max', or 'last'")
	// ErrInvalidType is returned when the type field has an unsupported value
	ErrInvalidType = errors.New("invalid type value: must be '', 'bin', or 'source'")
)

// PackageConfig represents a single package's autoupdate configuration.
// It defines how to check upstream versions for a specific package.
type PackageConfig struct {
	// Enabled toggles whether the autoupdate checker processes this package.
	// A nil/absent value means enabled (the default), so existing entries need
	// no migration. Set enabled = false to silently skip the package — no
	// fetch, absent from progress and totals — without deleting its config
	// (e.g. an orphaned entry whose ebuild was removed from the overlay).
	// A pointer distinguishes "absent" (enabled) from an explicit false.
	Enabled *bool `toml:"enabled,omitempty"`
	// URL is the primary URL to query for version information
	URL string `toml:"url"`
	// Parser specifies the parser type: "json", "regex", or "html"
	Parser string `toml:"parser"`
	// Path is the JSON path for extracting version (used with json parser)
	Path string `toml:"path,omitempty"`
	// Pattern is the regex pattern with capture group (used with regex parser)
	Pattern string `toml:"pattern,omitempty"`
	// Binary indicates if this is a binary package (manifest-only testing)
	Binary bool `toml:"binary,omitempty"`
	// Type classifies the package as binary ("bin") or source-built
	// ("source"). Empty means auto-detect from the ebuild (RESTRICT=bindist,
	// a -bin suffix, or a binary SRC_URI). Set it only to override/correct the
	// heuristic. Used for reporting and the --only filter; it does not change
	// apply/compile behavior.
	Type string `toml:"type,omitempty"`
	// FallbackURL is an alternative URL to try if primary fails
	FallbackURL string `toml:"fallback_url,omitempty"`
	// FallbackParser is the parser type for the fallback URL
	FallbackParser string `toml:"fallback_parser,omitempty"`
	// FallbackPattern is the pattern for the fallback parser
	FallbackPattern string `toml:"fallback_pattern,omitempty"`
	// LLMPrompt is the prompt to use for LLM-based version extraction
	LLMPrompt string `toml:"llm_prompt,omitempty"`

	// New fields for HTML parser
	// Selector is the CSS selector for extracting version (used with html parser)
	Selector string `toml:"selector,omitempty"`
	// XPath is the XPath expression for extracting version (used with html parser)
	XPath string `toml:"xpath,omitempty"`

	// New fields for authentication
	// Headers contains custom HTTP headers to send with requests
	Headers map[string]string `toml:"headers,omitempty"`

	// Meta holds free-form key/value annotations for packages with special
	// acquisition requirements (e.g. a purchased serial, a platform selector,
	// a download endpoint). It is documentation only — the checker ignores it
	// when detecting versions. Never store secrets here; reference an env var
	// instead (e.g. serial_env = "FILEZILLA_PRO_KEY").
	Meta map[string]string `toml:"meta,omitempty"`

	// New fields for version history
	// VersionsPath is the JSON path for extracting version list
	VersionsPath string `toml:"versions_path,omitempty"`
	// VersionsSelector is the CSS selector for extracting version list
	VersionsSelector string `toml:"versions_selector,omitempty"`

	// Transform applies ordered regex substitutions to the extracted version,
	// e.g. [["-", "."]] turns "7.1.2-24" into "7.1.2.24". Each rule is
	// [regex, repl]; repl follows regexp.ReplaceAllString semantics ($1 etc.).
	// Rules run in order, before selection and before the Gentoo comparison.
	Transform [][]string `toml:"transform,omitempty"`
	// Select chooses which match to return when several are present.
	// "" / "first" = current behavior; "max" = highest Gentoo version;
	// "last" = last match. Requires a parser that can extract a list
	// (json/regex/html); ignored by the "script" parser.
	Select string `toml:"select,omitempty"`
	// Script is a JS expression/IIFE evaluated against the live DOM by the
	// "script" parser; its string result is the version. Inline, or "@file.js"
	// to load from .autoupdate/scripts/<file>.
	Script string `toml:"script,omitempty"`

	// Track specifies the tracking mode.
	// "" (default) = semver tag comparison.
	// "commit"     = compare commit dates on a branch; the date extracted via
	//                path/transform becomes the _pDATE suffix of the new version
	//                (base version is taken from the current ebuild by stripping
	//                the existing _p<date> suffix). CommitSHAPath must also be
	//                set so the applier can substitute the commit hash in the ebuild.
	Track string `toml:"track,omitempty"`

	// CommitSHAPath is the JSON path to extract the commit SHA from the same
	// response as the date (used with track = "commit"). The SHA is stored in
	// PendingUpdate.CommitHash and substituted into the copied ebuild at apply time.
	CommitSHAPath string `toml:"commit_sha_path,omitempty"`

	// CommitMessagePath is the JSON path, relative to each commit array element,
	// that yields the commit title/message string (used with track = "commit"
	// and commit_version_pattern). Typical values:
	//   "commit.message"  — GitHub commits list API
	//   "title"           — GitLab repository/commits API
	CommitMessagePath string `toml:"commit_message_path,omitempty"`

	// CommitVersionPattern is a regex with one capture group applied to each
	// commit title (at commit_message_path) to detect a base-version change
	// between tags (used with track = "commit"). When a commit title matches
	// and the captured version is newer than the current base, the new base
	// replaces the old one in the generated ebuild version (e.g.
	// "1.4.352_p20260515" → "1.4.353_p<today>" when the match is "1.4.353").
	CommitVersionPattern string `toml:"commit_version_pattern,omitempty"`
}

// IsEnabled reports whether the checker should process this package. An absent
// (nil) enabled field counts as enabled, so the default — and every legacy
// entry that predates the field — is processed; only an explicit
// enabled = false skips it.
func (c *PackageConfig) IsEnabled() bool {
	return c.Enabled == nil || *c.Enabled
}

// PackagesConfig represents the entire packages.toml configuration file.
// The keys in the map are package names in "category/package" format.
type PackagesConfig struct {
	Packages map[string]PackageConfig `toml:"packages"`
}

// packagesConfigFile is the internal representation matching the TOML structure
// where each [category/package] section is a top-level key
type packagesConfigFile map[string]PackageConfig

// LoadPackagesConfig loads and parses packages.toml from the overlay.
// The configuration file is expected at overlay/.autoupdate/packages.toml
func LoadPackagesConfig(overlayPath string) (*PackagesConfig, error) {
	configPath := filepath.Join(overlayPath, ".autoupdate", "packages.toml")

	// Check if file exists
	if _, err := os.Stat(configPath); os.IsNotExist(err) {
		return nil, ErrPackagesConfigNotFound
	}

	// Read and parse the TOML file
	data, err := os.ReadFile(configPath)
	if err != nil {
		return nil, fmt.Errorf("failed to read packages.toml: %w", err)
	}

	// Parse TOML into the internal structure
	var fileConfig packagesConfigFile
	if err := toml.Unmarshal(data, &fileConfig); err != nil {
		return nil, fmt.Errorf("failed to parse packages.toml: %w", err)
	}

	// Convert to PackagesConfig
	config := &PackagesConfig{
		Packages: make(map[string]PackageConfig),
	}
	for pkg, cfg := range fileConfig {
		config.Packages[pkg] = cfg
	}

	return config, nil
}

// tomlTableName returns the table name of a TOML section header line and true
// when the line is a standard `[name]` header. It tolerates surrounding
// whitespace and a trailing inline comment, strips one layer of basic (") or
// literal (') quotes from the name, and reports false for array-of-table
// headers (`[[name]]`), comments, array continuation lines (`["-", "."],`), and
// anything else. The strictness — requiring only whitespace or a comment after
// the closing bracket — keeps multi-line array values from being mistaken for
// section headers during the surgical edit in DisablePackagesInConfig.
func tomlTableName(line string) (string, bool) {
	t := strings.TrimSpace(line)
	if !strings.HasPrefix(t, "[") || strings.HasPrefix(t, "[[") {
		return "", false
	}
	end := strings.IndexByte(t, ']')
	if end < 0 {
		return "", false
	}
	// Whatever follows the closing bracket must be empty or a comment, else
	// this is not a header (e.g. an array element line `["-", "."],`).
	if rest := strings.TrimSpace(t[end+1:]); rest != "" && !strings.HasPrefix(rest, "#") {
		return "", false
	}
	inner := strings.TrimSpace(t[1:end])
	if len(inner) >= 2 {
		if (inner[0] == '"' && inner[len(inner)-1] == '"') ||
			(inner[0] == '\'' && inner[len(inner)-1] == '\'') {
			inner = inner[1 : len(inner)-1]
		}
	}
	if inner == "" {
		return "", false
	}
	return inner, true
}

// DisablePackagesInConfig sets `enabled = false` for each named package in the
// overlay's packages.toml, editing the raw text so comments, ordering, and
// formatting survive — unlike a full re-encode (toml.Encoder), which would drop
// every comment in the hand-maintained file. For each package it locates the
// [section] whose table name equals the package and either rewrites an existing
// `enabled = ...` assignment or inserts `enabled = false` immediately after the
// header. Packages whose section is absent are skipped silently. The write is
// atomic (temp file + rename) and preserves the original file mode; an empty
// package list, or a run that changes nothing, leaves the file untouched.
func DisablePackagesInConfig(overlayPath string, pkgs []string) error {
	if len(pkgs) == 0 {
		return nil
	}

	configPath := filepath.Join(overlayPath, ".autoupdate", "packages.toml")
	data, err := os.ReadFile(configPath)
	if err != nil {
		return fmt.Errorf("failed to read packages.toml: %w", err)
	}
	info, err := os.Stat(configPath)
	if err != nil {
		return fmt.Errorf("failed to stat packages.toml: %w", err)
	}

	targets := make(map[string]bool, len(pkgs))
	for _, p := range pkgs {
		targets[p] = true
	}

	// Split on "\n" (not bufio.Scanner) so a file without a trailing newline is
	// reproduced byte-for-byte by the strings.Join below.
	lines := strings.Split(string(data), "\n")
	enabledRe := regexp.MustCompile(`^(\s*)enabled\s*=`)

	changed := false
	out := make([]string, 0, len(lines)+len(pkgs))
	for i := 0; i < len(lines); {
		name, isHeader := tomlTableName(lines[i])
		if !isHeader || !targets[name] {
			out = append(out, lines[i])
			i++
			continue
		}

		// At the header of a target section: emit the header, then walk its body
		// (up to the next header or EOF), rewriting an existing `enabled` key or
		// inserting one right after the header when absent.
		out = append(out, lines[i])
		i++
		bodyStart := len(out)
		found := false
		for i < len(lines) {
			if _, nextHeader := tomlTableName(lines[i]); nextHeader {
				break
			}
			if m := enabledRe.FindStringSubmatch(lines[i]); m != nil {
				out = append(out, m[1]+"enabled = false")
				found = true
			} else {
				out = append(out, lines[i])
			}
			i++
		}
		if !found {
			inserted := make([]string, 0, len(out)+1)
			inserted = append(inserted, out[:bodyStart]...)
			inserted = append(inserted, "enabled = false")
			inserted = append(inserted, out[bodyStart:]...)
			out = inserted
		}
		changed = true
	}

	if !changed {
		return nil
	}

	tmpPath := configPath + ".tmp"
	if err := os.WriteFile(tmpPath, []byte(strings.Join(out, "\n")), info.Mode().Perm()); err != nil {
		return fmt.Errorf("failed to write temp config: %w", err)
	}
	if err := os.Rename(tmpPath, configPath); err != nil {
		os.Remove(tmpPath) //nolint:errcheck
		return fmt.Errorf("failed to replace packages.toml: %w", err)
	}

	return nil
}

// ValidatePackageConfig validates a single package configuration.
// It checks for required fields and valid parser types.
func ValidatePackageConfig(pkg string, cfg *PackageConfig) error {
	// Check required fields
	if cfg.URL == "" {
		return fmt.Errorf("package %s: %w", pkg, ErrMissingURL)
	}
	if cfg.Parser == "" {
		return fmt.Errorf("package %s: %w", pkg, ErrMissingParser)
	}

	// Validate parser type and required fields
	switch cfg.Parser {
	case "json":
		if cfg.Path == "" {
			return fmt.Errorf("package %s: %w", pkg, ErrMissingPath)
		}
	case "regex":
		if cfg.Pattern == "" {
			return fmt.Errorf("package %s: %w", pkg, ErrMissingPattern)
		}
	case "html":
		if cfg.Selector == "" && cfg.XPath == "" {
			return fmt.Errorf("package %s: %w", pkg, ErrMissingSelectorOrXPath)
		}
	case "script":
		if cfg.Script == "" {
			return fmt.Errorf("package %s: %w", pkg, ErrMissingScript)
		}
	default:
		return fmt.Errorf("package %s: %w: got %q", pkg, ErrInvalidParserType, cfg.Parser)
	}

	// Validate the select field. An unrecognized value is almost certainly a
	// typo in packages.toml, so fail hard rather than silently fall back.
	switch cfg.Select {
	case "", "first", "max", "last":
		// valid
	default:
		return fmt.Errorf("package %s: %w: got %q", pkg, ErrInvalidSelect, cfg.Select)
	}

	// Validate the type field. Like select, an unrecognized value is almost
	// certainly a typo in packages.toml, so fail hard rather than silently
	// auto-detecting and masking the mistake.
	switch cfg.Type {
	case "", "bin", "source":
		// valid
	default:
		return fmt.Errorf("package %s: %w: got %q", pkg, ErrInvalidType, cfg.Type)
	}

	// Validate transform rules. A malformed rule (wrong arity or uncompilable
	// regex) is warned and ignored at apply time (applyTransforms does the same),
	// so we warn here rather than fail — a bad rule must not block the whole run.
	for i, r := range cfg.Transform {
		if len(r) != 2 {
			warnLogf("package %s: transform rule #%d has %d elements, want 2 ([regex, repl]); it will be ignored", pkg, i, len(r))
			continue
		}
		if _, err := regexp.Compile(r[0]); err != nil {
			warnLogf("package %s: transform rule #%d has bad regex %q (%v); it will be ignored", pkg, i, r[0], err)
		}
	}

	// transform/select do not apply to the script parser: that branch bypasses
	// fetchAndParse and the JS is responsible for all normalization. Warn so the
	// config author is not misled into thinking they take effect.
	if cfg.Parser == "script" {
		if len(cfg.Transform) > 0 {
			warnLogf("package %s: transform is ignored for parser=\"script\" (the script must normalize the version itself)", pkg)
		}
		if cfg.Select != "" && cfg.Select != "first" {
			warnLogf("package %s: select=%q is ignored for parser=\"script\" (the script must select the version itself)", pkg, cfg.Select)
		}
	}

	// Validate track field and its dependencies.
	switch cfg.Track {
	case "", "commit":
		// valid
	default:
		return fmt.Errorf("package %s: invalid track value: must be '' or 'commit', got %q", pkg, cfg.Track)
	}
	if cfg.Track == "commit" {
		if cfg.Parser != "json" {
			return fmt.Errorf("package %s: track=\"commit\" requires parser=\"json\"", pkg)
		}
		if cfg.CommitSHAPath == "" {
			return fmt.Errorf("package %s: track=\"commit\" requires commit_sha_path", pkg)
		}
	}
	if cfg.CommitSHAPath != "" && cfg.Track != "commit" {
		warnLogf("package %s: commit_sha_path is set but track!=\"commit\"; it will be ignored", pkg)
	}
	if cfg.CommitVersionPattern != "" {
		if cfg.Track != "commit" {
			warnLogf("package %s: commit_version_pattern is set but track!=\"commit\"; it will be ignored", pkg)
		} else if cfg.CommitMessagePath == "" {
			return fmt.Errorf("package %s: commit_version_pattern requires commit_message_path", pkg)
		} else if _, err := regexp.Compile(cfg.CommitVersionPattern); err != nil {
			return fmt.Errorf("package %s: invalid commit_version_pattern %q: %w", pkg, cfg.CommitVersionPattern, err)
		}
	}
	if cfg.CommitMessagePath != "" && cfg.Track != "commit" {
		warnLogf("package %s: commit_message_path is set but track!=\"commit\"; it will be ignored", pkg)
	}

	// Validate fallback configuration if present
	if cfg.FallbackURL != "" && cfg.FallbackParser != "" {
		switch cfg.FallbackParser {
		case "json":
			// JSON fallback doesn't require pattern, uses Path from main config or FallbackPattern
		case "regex":
			if cfg.FallbackPattern == "" {
				return fmt.Errorf("package %s: fallback_pattern required for regex fallback parser", pkg)
			}
		case "html":
			// HTML fallback uses Selector or XPath from main config
		default:
			return fmt.Errorf("package %s: invalid fallback_parser type: %q", pkg, cfg.FallbackParser)
		}
	}

	return nil
}

// ValidateAll validates all package configurations in the PackagesConfig.
// Returns the first validation error encountered, or nil if all are valid.
func (c *PackagesConfig) ValidateAll() error {
	for pkg, cfg := range c.Packages {
		cfgCopy := cfg // Create a copy to get a pointer
		if err := ValidatePackageConfig(pkg, &cfgCopy); err != nil {
			return err
		}
	}
	return nil
}
