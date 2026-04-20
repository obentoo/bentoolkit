package autoupdate

import (
	"bytes"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/BurntSushi/toml"
	"github.com/leanovate/gopter"
	"github.com/leanovate/gopter/gen"
	"github.com/leanovate/gopter/prop"
)

// genValidURL generates valid URL strings
func genValidURL() gopter.Gen {
	return gen.RegexMatch(`^https://[a-z]{3,10}\.[a-z]{2,5}/[a-z0-9/]{1,20}$`)
}

// genValidJSONPath generates valid JSON path strings
func genValidJSONPath() gopter.Gen {
	return gen.RegexMatch(`^[a-z][a-z0-9_]{0,10}(\[[0-9]\])?(\.[a-z][a-z0-9_]{0,10})?$`)
}

// genValidRegexPattern generates valid regex patterns with capture group
func genValidRegexPattern() gopter.Gen {
	return gen.RegexMatch(`^[a-z_]+\(\\d\+\)$`)
}

// genValidLLMPrompt generates valid LLM prompt strings
func genValidLLMPrompt() gopter.Gen {
	return gen.RegexMatch(`^[A-Za-z ]{5,50}$`)
}

// genPackageConfig generates valid PackageConfig structs for JSON parser
func genPackageConfigJSON() gopter.Gen {
	return gopter.CombineGens(
		genValidURL(),
		genValidJSONPath(),
		gen.Bool(),
		gen.Bool(), // has fallback
		genValidURL(),
		genValidRegexPattern(),
		gen.Bool(), // has LLM prompt
		genValidLLMPrompt(),
	).Map(func(values []interface{}) PackageConfig {
		cfg := PackageConfig{
			URL:    values[0].(string),
			Parser: "json",
			Path:   values[1].(string),
			Binary: values[2].(bool),
		}
		if values[3].(bool) {
			cfg.FallbackURL = values[4].(string)
			cfg.FallbackParser = "regex"
			cfg.FallbackPattern = values[5].(string)
		}
		if values[6].(bool) {
			cfg.LLMPrompt = values[7].(string)
		}
		return cfg
	})
}

// genPackageConfigRegex generates valid PackageConfig structs for regex parser
func genPackageConfigRegex() gopter.Gen {
	return gopter.CombineGens(
		genValidURL(),
		genValidRegexPattern(),
		gen.Bool(),
	).Map(func(values []interface{}) PackageConfig {
		return PackageConfig{
			URL:     values[0].(string),
			Parser:  "regex",
			Pattern: values[1].(string),
			Binary:  values[2].(bool),
		}
	})
}

// genPackageName generates valid package names in category/package format
func genPackageName() gopter.Gen {
	return gen.RegexMatch(`^[a-z]{3,10}-[a-z]{3,10}/[a-z][a-z0-9-]{2,15}$`)
}

// TestPackageConfigRoundTrip tests Property 1: PackageConfig Round-Trip
// **Feature: ebuild-autoupdate, Property 1: PackageConfig Round-Trip**
// **Validates: Requirements 1.1, 1.2, 1.3, 1.4, 1.5**
func TestPackageConfigRoundTrip(t *testing.T) {
	parameters := gopter.DefaultTestParameters()
	parameters.MinSuccessfulTests = 100
	properties := gopter.NewProperties(parameters)

	properties.Property("PackageConfig TOML round-trip preserves data (JSON parser)", prop.ForAll(
		func(pkgName string, cfg PackageConfig) bool {
			// Create a map to serialize
			configMap := map[string]PackageConfig{
				pkgName: cfg,
			}

			// Serialize to TOML
			var buf bytes.Buffer
			encoder := toml.NewEncoder(&buf)
			if err := encoder.Encode(configMap); err != nil {
				t.Logf("Failed to encode TOML: %v", err)
				return false
			}

			// Parse back
			var parsed map[string]PackageConfig
			if err := toml.Unmarshal(buf.Bytes(), &parsed); err != nil {
				t.Logf("Failed to decode TOML: %v", err)
				return false
			}

			// Compare
			parsedCfg, ok := parsed[pkgName]
			if !ok {
				t.Logf("Package %s not found in parsed config", pkgName)
				return false
			}

			if !reflect.DeepEqual(cfg, parsedCfg) {
				t.Logf("Config mismatch:\nOriginal: %+v\nParsed: %+v", cfg, parsedCfg)
				return false
			}

			return true
		},
		genPackageName(),
		genPackageConfigJSON(),
	))

	properties.Property("PackageConfig TOML round-trip preserves data (regex parser)", prop.ForAll(
		func(pkgName string, cfg PackageConfig) bool {
			// Create a map to serialize
			configMap := map[string]PackageConfig{
				pkgName: cfg,
			}

			// Serialize to TOML
			var buf bytes.Buffer
			encoder := toml.NewEncoder(&buf)
			if err := encoder.Encode(configMap); err != nil {
				t.Logf("Failed to encode TOML: %v", err)
				return false
			}

			// Parse back
			var parsed map[string]PackageConfig
			if err := toml.Unmarshal(buf.Bytes(), &parsed); err != nil {
				t.Logf("Failed to decode TOML: %v", err)
				return false
			}

			// Compare
			parsedCfg, ok := parsed[pkgName]
			if !ok {
				t.Logf("Package %s not found in parsed config", pkgName)
				return false
			}

			if !reflect.DeepEqual(cfg, parsedCfg) {
				t.Logf("Config mismatch:\nOriginal: %+v\nParsed: %+v", cfg, parsedCfg)
				return false
			}

			return true
		},
		genPackageName(),
		genPackageConfigRegex(),
	))

	properties.TestingRun(t)
}

// TestLoadPackagesConfigMissingFile tests that missing file returns appropriate error
// _Requirements: 1.6_
func TestLoadPackagesConfigMissingFile(t *testing.T) {
	tmpDir := t.TempDir()

	_, err := LoadPackagesConfig(tmpDir)
	if err != ErrPackagesConfigNotFound {
		t.Errorf("Expected ErrPackagesConfigNotFound, got: %v", err)
	}
}

// TestLoadPackagesConfigMalformedTOML tests that malformed TOML returns parse error
// _Requirements: 1.6_
func TestLoadPackagesConfigMalformedTOML(t *testing.T) {
	tmpDir := t.TempDir()
	configDir := filepath.Join(tmpDir, ".autoupdate")
	if err := os.MkdirAll(configDir, 0755); err != nil {
		t.Fatalf("Failed to create config dir: %v", err)
	}

	// Write malformed TOML
	malformedTOML := `[net-misc/test
url = "incomplete`
	if err := os.WriteFile(filepath.Join(configDir, "packages.toml"), []byte(malformedTOML), 0644); err != nil {
		t.Fatalf("Failed to write malformed TOML: %v", err)
	}

	_, err := LoadPackagesConfig(tmpDir)
	if err == nil {
		t.Error("Expected error for malformed TOML, got nil")
	}
}

// TestLoadPackagesConfigValid tests loading a valid configuration
// _Requirements: 1.1, 1.2, 1.3, 1.4, 1.5_
func TestLoadPackagesConfigValid(t *testing.T) {
	tmpDir := t.TempDir()
	configDir := filepath.Join(tmpDir, ".autoupdate")
	if err := os.MkdirAll(configDir, 0755); err != nil {
		t.Fatalf("Failed to create config dir: %v", err)
	}

	validTOML := `["net-misc/postman-bin"]
url = "https://www.postman.com/mkapi/release.json"
parser = "json"
path = "notes[0].version"
binary = true
fallback_url = "https://aur.archlinux.org/cgit/aur.git/plain/PKGBUILD?h=postman-bin"
fallback_parser = "regex"
fallback_pattern = 'pkgver=([0-9.]+)'
llm_prompt = "Extract the latest version number from this content"

["app-editors/vscode"]
url = "https://api.github.com/repos/microsoft/vscode/releases/latest"
parser = "json"
path = "tag_name"
`
	if err := os.WriteFile(filepath.Join(configDir, "packages.toml"), []byte(validTOML), 0644); err != nil {
		t.Fatalf("Failed to write valid TOML: %v", err)
	}

	config, err := LoadPackagesConfig(tmpDir)
	if err != nil {
		t.Fatalf("Expected no error, got: %v", err)
	}

	// Verify postman-bin config
	postman, ok := config.Packages["net-misc/postman-bin"]
	if !ok {
		t.Fatal("Expected net-misc/postman-bin in config")
	}
	if postman.URL != "https://www.postman.com/mkapi/release.json" {
		t.Errorf("Unexpected URL: %s", postman.URL)
	}
	if postman.Parser != "json" {
		t.Errorf("Unexpected parser: %s", postman.Parser)
	}
	if postman.Path != "notes[0].version" {
		t.Errorf("Unexpected path: %s", postman.Path)
	}
	if !postman.Binary {
		t.Error("Expected binary to be true")
	}
	if postman.FallbackURL != "https://aur.archlinux.org/cgit/aur.git/plain/PKGBUILD?h=postman-bin" {
		t.Errorf("Unexpected fallback URL: %s", postman.FallbackURL)
	}
	if postman.FallbackParser != "regex" {
		t.Errorf("Unexpected fallback parser: %s", postman.FallbackParser)
	}
	if postman.FallbackPattern != "pkgver=([0-9.]+)" {
		t.Errorf("Unexpected fallback pattern: %s", postman.FallbackPattern)
	}
	if postman.LLMPrompt != "Extract the latest version number from this content" {
		t.Errorf("Unexpected LLM prompt: %s", postman.LLMPrompt)
	}

	// Verify vscode config
	vscode, ok := config.Packages["app-editors/vscode"]
	if !ok {
		t.Fatal("Expected app-editors/vscode in config")
	}
	if vscode.URL != "https://api.github.com/repos/microsoft/vscode/releases/latest" {
		t.Errorf("Unexpected URL: %s", vscode.URL)
	}
	if vscode.Parser != "json" {
		t.Errorf("Unexpected parser: %s", vscode.Parser)
	}
	if vscode.Path != "tag_name" {
		t.Errorf("Unexpected path: %s", vscode.Path)
	}
}

// TestValidatePackageConfigMissingURL tests validation with missing URL
// _Requirements: 1.6_
func TestValidatePackageConfigMissingURL(t *testing.T) {
	cfg := &PackageConfig{
		Parser: "json",
		Path:   "version",
	}

	err := ValidatePackageConfig("test/pkg", cfg)
	if err == nil {
		t.Error("Expected error for missing URL")
	}
}

// TestValidatePackageConfigMissingParser tests validation with missing parser
// _Requirements: 1.6_
func TestValidatePackageConfigMissingParser(t *testing.T) {
	cfg := &PackageConfig{
		URL: "https://example.com/api",
	}

	err := ValidatePackageConfig("test/pkg", cfg)
	if err == nil {
		t.Error("Expected error for missing parser")
	}
}

// TestValidatePackageConfigInvalidParser tests validation with invalid parser type
// _Requirements: 1.6_
func TestValidatePackageConfigInvalidParser(t *testing.T) {
	cfg := &PackageConfig{
		URL:    "https://example.com/api",
		Parser: "invalid",
	}

	err := ValidatePackageConfig("test/pkg", cfg)
	if err == nil {
		t.Error("Expected error for invalid parser type")
	}
}

// TestValidatePackageConfigJSONMissingPath tests validation for JSON parser without path
// _Requirements: 1.6_
func TestValidatePackageConfigJSONMissingPath(t *testing.T) {
	cfg := &PackageConfig{
		URL:    "https://example.com/api",
		Parser: "json",
	}

	err := ValidatePackageConfig("test/pkg", cfg)
	if err == nil {
		t.Error("Expected error for JSON parser without path")
	}
}

// TestValidatePackageConfigRegexMissingPattern tests validation for regex parser without pattern
// _Requirements: 1.6_
func TestValidatePackageConfigRegexMissingPattern(t *testing.T) {
	cfg := &PackageConfig{
		URL:    "https://example.com/api",
		Parser: "regex",
	}

	err := ValidatePackageConfig("test/pkg", cfg)
	if err == nil {
		t.Error("Expected error for regex parser without pattern")
	}
}

// TestValidatePackageConfigValidJSON tests validation for valid JSON config
// _Requirements: 1.1, 1.2_
func TestValidatePackageConfigValidJSON(t *testing.T) {
	cfg := &PackageConfig{
		URL:    "https://example.com/api",
		Parser: "json",
		Path:   "version",
	}

	err := ValidatePackageConfig("test/pkg", cfg)
	if err != nil {
		t.Errorf("Expected no error, got: %v", err)
	}
}

// TestValidatePackageConfigValidRegex tests validation for valid regex config
// _Requirements: 1.1, 1.2_
func TestValidatePackageConfigValidRegex(t *testing.T) {
	cfg := &PackageConfig{
		URL:     "https://example.com/api",
		Parser:  "regex",
		Pattern: `version=([0-9.]+)`,
	}

	err := ValidatePackageConfig("test/pkg", cfg)
	if err != nil {
		t.Errorf("Expected no error, got: %v", err)
	}
}

// TestValidatePackageConfigFallbackRegexMissingPattern tests fallback validation
// _Requirements: 1.3_
func TestValidatePackageConfigFallbackRegexMissingPattern(t *testing.T) {
	cfg := &PackageConfig{
		URL:            "https://example.com/api",
		Parser:         "json",
		Path:           "version",
		FallbackURL:    "https://fallback.com/api",
		FallbackParser: "regex",
		// Missing FallbackPattern
	}

	err := ValidatePackageConfig("test/pkg", cfg)
	if err == nil {
		t.Error("Expected error for regex fallback without pattern")
	}
}

// TestValidateAllValid tests ValidateAll with valid configs
func TestValidateAllValid(t *testing.T) {
	config := &PackagesConfig{
		Packages: map[string]PackageConfig{
			"test/pkg1": {
				URL:    "https://example.com/api1",
				Parser: "json",
				Path:   "version",
			},
			"test/pkg2": {
				URL:     "https://example.com/api2",
				Parser:  "regex",
				Pattern: `v([0-9.]+)`,
			},
		},
	}

	err := config.ValidateAll()
	if err != nil {
		t.Errorf("Expected no error, got: %v", err)
	}
}

// TestValidateAllInvalid tests ValidateAll with invalid config
func TestValidateAllInvalid(t *testing.T) {
	config := &PackagesConfig{
		Packages: map[string]PackageConfig{
			"test/pkg1": {
				URL:    "https://example.com/api1",
				Parser: "json",
				Path:   "version",
			},
			"test/pkg2": {
				URL:    "https://example.com/api2",
				Parser: "json",
				// Missing Path
			},
		},
	}

	err := config.ValidateAll()
	if err == nil {
		t.Error("Expected error for invalid config")
	}
}

// TestValidatePackageConfigHTMLMissingSelectorAndXPath tests validation for HTML parser without selector or xpath
// _Requirements: 4.1, 4.2_
func TestValidatePackageConfigHTMLMissingSelectorAndXPath(t *testing.T) {
	cfg := &PackageConfig{
		URL:    "https://example.com/releases",
		Parser: "html",
	}

	err := ValidatePackageConfig("test/pkg", cfg)
	if err == nil {
		t.Error("Expected error for HTML parser without selector or xpath")
	}
}

// TestValidatePackageConfigValidHTMLWithSelector tests validation for valid HTML config with CSS selector
// _Requirements: 4.1_
func TestValidatePackageConfigValidHTMLWithSelector(t *testing.T) {
	cfg := &PackageConfig{
		URL:      "https://example.com/releases",
		Parser:   "html",
		Selector: ".version",
	}

	err := ValidatePackageConfig("test/pkg", cfg)
	if err != nil {
		t.Errorf("Expected no error, got: %v", err)
	}
}

// TestValidatePackageConfigValidHTMLWithXPath tests validation for valid HTML config with XPath
// _Requirements: 4.2_
func TestValidatePackageConfigValidHTMLWithXPath(t *testing.T) {
	cfg := &PackageConfig{
		URL:    "https://example.com/releases",
		Parser: "html",
		XPath:  "//div[@class='version']/text()",
	}

	err := ValidatePackageConfig("test/pkg", cfg)
	if err != nil {
		t.Errorf("Expected no error, got: %v", err)
	}
}

// TestValidatePackageConfigValidHTMLWithHeaders tests validation for HTML config with custom headers
// _Requirements: 8.1_
func TestValidatePackageConfigValidHTMLWithHeaders(t *testing.T) {
	cfg := &PackageConfig{
		URL:      "https://example.com/releases",
		Parser:   "html",
		Selector: ".version",
		Headers: map[string]string{
			"Authorization": "Bearer ${API_TOKEN}",
			"User-Agent":    "bentoolkit/1.0",
		},
	}

	err := ValidatePackageConfig("test/pkg", cfg)
	if err != nil {
		t.Errorf("Expected no error, got: %v", err)
	}
}

// TestValidatePackageConfigValidJSONWithVersionsPath tests validation for JSON config with versions_path
// _Requirements: 9.2_
func TestValidatePackageConfigValidJSONWithVersionsPath(t *testing.T) {
	cfg := &PackageConfig{
		URL:          "https://api.github.com/repos/test/test/releases",
		Parser:       "json",
		Path:         "[0].tag_name",
		VersionsPath: "[*].tag_name",
	}

	err := ValidatePackageConfig("test/pkg", cfg)
	if err != nil {
		t.Errorf("Expected no error, got: %v", err)
	}
}

// TestValidatePackageConfigValidHTMLWithVersionsSelector tests validation for HTML config with versions_selector
// _Requirements: 9.2_
func TestValidatePackageConfigValidHTMLWithVersionsSelector(t *testing.T) {
	cfg := &PackageConfig{
		URL:              "https://example.com/releases",
		Parser:           "html",
		Selector:         ".version:first-child",
		VersionsSelector: ".version",
	}

	err := ValidatePackageConfig("test/pkg", cfg)
	if err != nil {
		t.Errorf("Expected no error, got: %v", err)
	}
}

// genValidCSSSelector generates valid CSS selector strings
func genValidCSSSelector() gopter.Gen {
	return gen.RegexMatch(`^\.[a-z][a-z0-9-]{0,10}$`)
}

// genValidXPath generates valid XPath expression strings
func genValidXPath() gopter.Gen {
	return gen.RegexMatch(`^//[a-z]+\[@[a-z]+='[a-z]+'\]/text\(\)$`)
}

// genValidHeaderKey generates valid HTTP header key strings
func genValidHeaderKey() gopter.Gen {
	return gen.RegexMatch(`^[A-Z][a-z]{2,10}(-[A-Z][a-z]{2,10})?$`)
}

// genValidHeaderValue generates valid HTTP header value strings
func genValidHeaderValue() gopter.Gen {
	return gen.RegexMatch(`^[A-Za-z0-9 /_-]{1,30}$`)
}

// genHeaders generates a map of HTTP headers (non-empty)
func genHeaders() gopter.Gen {
	// Generate a simple single-header map to avoid filter issues
	return gopter.CombineGens(
		genValidHeaderKey(),
		genValidHeaderValue(),
	).Map(func(values []interface{}) map[string]string {
		return map[string]string{
			values[0].(string): values[1].(string),
		}
	})
}

// genPackageConfigHTML generates valid PackageConfig structs for HTML parser
func genPackageConfigHTML() gopter.Gen {
	return gopter.CombineGens(
		genValidURL(),
		gen.Bool(),             // use selector (true) or xpath (false)
		genValidCSSSelector(),  // selector
		genValidXPath(),        // xpath
		gen.Bool(),             // has pattern
		genValidRegexPattern(), // pattern
		gen.Bool(),             // binary
		gen.Bool(),             // has headers
		genHeaders(),           // headers
		gen.Bool(),             // has versions_selector
		genValidCSSSelector(),  // versions_selector
	).Map(func(values []interface{}) PackageConfig {
		cfg := PackageConfig{
			URL:    values[0].(string),
			Parser: "html",
			Binary: values[6].(bool),
		}
		if values[1].(bool) {
			cfg.Selector = values[2].(string)
		} else {
			cfg.XPath = values[3].(string)
		}
		if values[4].(bool) {
			cfg.Pattern = values[5].(string)
		}
		if values[7].(bool) {
			headers := values[8].(map[string]string)
			if len(headers) > 0 {
				cfg.Headers = headers
			}
		}
		if values[9].(bool) {
			cfg.VersionsSelector = values[10].(string)
		}
		return cfg
	})
}

// genPackageConfigJSONWithVersionsPath generates valid PackageConfig structs for JSON parser with versions_path
func genPackageConfigJSONWithVersionsPath() gopter.Gen {
	return gopter.CombineGens(
		genValidURL(),
		genValidJSONPath(),
		gen.Bool(),         // binary
		gen.Bool(),         // has versions_path
		genValidJSONPath(), // versions_path
		gen.Bool(),         // has headers
		genHeaders(),       // headers
	).Map(func(values []interface{}) PackageConfig {
		cfg := PackageConfig{
			URL:    values[0].(string),
			Parser: "json",
			Path:   values[1].(string),
			Binary: values[2].(bool),
		}
		if values[3].(bool) {
			cfg.VersionsPath = values[4].(string)
		}
		if values[5].(bool) {
			headers := values[6].(map[string]string)
			if len(headers) > 0 {
				cfg.Headers = headers
			}
		}
		return cfg
	})
}

// TestExtendedPackageConfigRoundTrip tests Property 30: Extended Format Support
// **Feature: autoupdate-analyzer, Property 30: Extended Format Support**
// **Validates: Requirements 13.3**
// For any PackageConfig with HTML parser fields (selector, xpath), serialization
// and deserialization SHALL preserve all fields.
func TestExtendedPackageConfigRoundTrip(t *testing.T) {
	parameters := gopter.DefaultTestParameters()
	parameters.MinSuccessfulTests = 100
	properties := gopter.NewProperties(parameters)

	properties.Property("PackageConfig TOML round-trip preserves HTML parser fields", prop.ForAll(
		func(pkgName string, cfg PackageConfig) bool {
			// Create a map to serialize
			configMap := map[string]PackageConfig{
				pkgName: cfg,
			}

			// Serialize to TOML
			var buf bytes.Buffer
			encoder := toml.NewEncoder(&buf)
			if err := encoder.Encode(configMap); err != nil {
				t.Logf("Failed to encode TOML: %v", err)
				return false
			}

			// Parse back
			var parsed map[string]PackageConfig
			if err := toml.Unmarshal(buf.Bytes(), &parsed); err != nil {
				t.Logf("Failed to decode TOML: %v", err)
				return false
			}

			// Compare
			parsedCfg, ok := parsed[pkgName]
			if !ok {
				t.Logf("Package %s not found in parsed config", pkgName)
				return false
			}

			if !reflect.DeepEqual(cfg, parsedCfg) {
				t.Logf("Config mismatch:\nOriginal: %+v\nParsed: %+v", cfg, parsedCfg)
				return false
			}

			return true
		},
		genPackageName(),
		genPackageConfigHTML(),
	))

	properties.Property("PackageConfig TOML round-trip preserves JSON parser with versions_path and headers", prop.ForAll(
		func(pkgName string, cfg PackageConfig) bool {
			// Create a map to serialize
			configMap := map[string]PackageConfig{
				pkgName: cfg,
			}

			// Serialize to TOML
			var buf bytes.Buffer
			encoder := toml.NewEncoder(&buf)
			if err := encoder.Encode(configMap); err != nil {
				t.Logf("Failed to encode TOML: %v", err)
				return false
			}

			// Parse back
			var parsed map[string]PackageConfig
			if err := toml.Unmarshal(buf.Bytes(), &parsed); err != nil {
				t.Logf("Failed to decode TOML: %v", err)
				return false
			}

			// Compare
			parsedCfg, ok := parsed[pkgName]
			if !ok {
				t.Logf("Package %s not found in parsed config", pkgName)
				return false
			}

			if !reflect.DeepEqual(cfg, parsedCfg) {
				t.Logf("Config mismatch:\nOriginal: %+v\nParsed: %+v", cfg, parsedCfg)
				return false
			}

			return true
		},
		genPackageName(),
		genPackageConfigJSONWithVersionsPath(),
	))

	properties.TestingRun(t)
}
