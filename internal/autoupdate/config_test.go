package autoupdate

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
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
		genPackageType(),
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
			Type:   values[2].(string),
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

// genPackageType generates the accepted values of PackageConfig.Type: the two
// explicit classifiers plus "" (auto-detect from the ebuild).
func genPackageType() gopter.Gen {
	return gen.OneConstOf("", "bin", "source")
}

// genPackageConfigRegex generates valid PackageConfig structs for regex parser
func genPackageConfigRegex() gopter.Gen {
	return gopter.CombineGens(
		genValidURL(),
		genValidRegexPattern(),
		genPackageType(),
	).Map(func(values []interface{}) PackageConfig {
		return PackageConfig{
			URL:     values[0].(string),
			Parser:  "regex",
			Pattern: values[1].(string),
			Type:    values[2].(string),
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

	// The record keeps the retired binary key on purpose: 23 records in the real
	// registry still carry it, and retiring the struct field behind it must not
	// stop such a record from loading. type is the classifier actually read.
	validTOML := `["net-misc/postman-bin"]
url = "https://www.postman.com/mkapi/release.json"
parser = "json"
path = "notes[0].version"
binary = true
type = "bin"
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
	if postman.Type != "bin" {
		t.Errorf("Unexpected type: %s", postman.Type)
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

// TestLoadPackagesConfigUnknownKey pins strict decoding: a key no struct field
// claims fails the load and says WHICH record carries it (R4.1).
//
// The motivating case is the first subtest. `serie` for `series` parses as
// perfectly valid TOML, so the old lenient decode dropped it silently and the
// entry lost its release-line filter — the very silent failure `series` exists
// to prevent, now reported instead of absorbed.
//
// _Requirements: R4, R4.1, R4.2_
func TestLoadPackagesConfigUnknownKey(t *testing.T) {
	t.Run("a misspelled field fails the load, naming the record", func(t *testing.T) {
		dir := writeRegistry(t, `["app-editors/zed-bin"]
url = "https://example.com"
parser = "json"
path = "tag_name"
serie = '^1\.28\.'
comments = "zed-bin — doc."
# END
`)
		_, err := LoadPackagesConfig(dir)
		if err == nil {
			t.Fatal("a misspelled field loaded silently")
		}
		// Both halves matter: the key alone would leave the maintainer grepping
		// 411 records for it.
		msg := err.Error()
		if !strings.Contains(msg, "serie") {
			t.Errorf("error does not name the key: %q", msg)
		}
		if !strings.Contains(msg, "app-editors/zed-bin") {
			t.Errorf("error does not name the record: %q", msg)
		}

		var unknown *UnknownKeysError
		if !errors.As(err, &unknown) {
			t.Fatalf("want an *UnknownKeysError, got %T", err)
		}
		want := []UnknownKey{{Package: "app-editors/zed-bin", Key: "serie"}}
		if !reflect.DeepEqual(unknown.Keys, want) {
			t.Errorf("got %v, want %v", unknown.Keys, want)
		}
	})

	t.Run("only known keys load", func(t *testing.T) {
		dir := writeRegistry(t, `["app-editors/zed-bin"]
url = "https://example.com"
parser = "json"
path = "tag_name"
series = '^1\.28\.'
select = "max"
comments = "zed-bin — doc."
# END
`)
		cfg, err := LoadPackagesConfig(dir)
		if err != nil {
			t.Fatalf("a registry of known keys was rejected: %v", err)
		}
		if got := cfg.Packages["app-editors/zed-bin"].Series; got != `^1\.28\.` {
			t.Errorf("series = %q, want the declared regex", got)
		}
	})

	t.Run("a retired key still loads", func(t *testing.T) {
		// `binary` has no field since story 022 task 1.1, but 23 records in the
		// real registry still carry it. Rejecting it would make them unreadable by
		// --lint --fix, the only thing that can migrate them — so the allowlist
		// claims the key and the linter, not the loader, reports it.
		dir := writeRegistry(t, `["net-misc/postman-bin"]
url = "https://example.com"
parser = "json"
path = "notes[0].version"
binary = true
comments = "postman-bin — doc."
# END
`)
		if _, err := LoadPackagesConfig(dir); err != nil {
			t.Fatalf("a retired key failed the load: %v", err)
		}
	})

	t.Run("every unknown key is named in one error", func(t *testing.T) {
		// Failing on the first typo would cost one edit-and-rerun cycle per typo.
		// The records are deliberately out of alphabetical order in the file, so
		// the assertion also pins that the message is sorted rather than emitted
		// in whatever order the file happens to use.
		dir := writeRegistry(t, `["dev-util/zzz"]
url = "https://example.com"
patern = 'v([0-9.]+)'
comments = "zzz — doc."
# END

["dev-libs/aaa"]
url = "https://example.com"
serie = '^1\.28\.'
binary = true
comments = "aaa — doc."
# END
`)
		_, err := LoadPackagesConfig(dir)
		if err == nil {
			t.Fatal("two misspelled fields loaded silently")
		}
		var unknown *UnknownKeysError
		if !errors.As(err, &unknown) {
			t.Fatalf("want an *UnknownKeysError, got %T", err)
		}
		want := []UnknownKey{
			{Package: "dev-libs/aaa", Key: "serie"},
			{Package: "dev-util/zzz", Key: "patern"},
		}
		if !reflect.DeepEqual(unknown.Keys, want) {
			t.Fatalf("got %v, want both keys, sorted by record: %v", unknown.Keys, want)
		}
		msg := err.Error()
		for _, s := range []string{"dev-libs/aaa", "serie", "dev-util/zzz", "patern"} {
			if !strings.Contains(msg, s) {
				t.Errorf("error omits %q: %q", s, msg)
			}
		}
		// The retired key rode along in one of those records and must not be
		// reported as unknown.
		if strings.Contains(msg, "binary") {
			t.Errorf("retired key reported as unknown: %q", msg)
		}
	})
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

// TestValidatePackageConfigType tests that the type field accepts only "",
// "bin", or "source" and rejects anything else with ErrInvalidType.
func TestValidatePackageConfigType(t *testing.T) {
	for _, valid := range []string{"", "bin", "source"} {
		cfg := &PackageConfig{
			URL:     "https://example.com/api",
			Parser:  "regex",
			Pattern: `v(\d+\.\d+)`,
			Type:    valid,
		}
		if err := ValidatePackageConfig("test/pkg", cfg); err != nil {
			t.Errorf("type %q: unexpected error: %v", valid, err)
		}
	}

	cfg := &PackageConfig{
		URL:     "https://example.com/api",
		Parser:  "regex",
		Pattern: `v(\d+\.\d+)`,
		Type:    "binary", // typo: not an accepted value
	}
	err := ValidatePackageConfig("test/pkg", cfg)
	if err == nil {
		t.Fatal("Expected error for invalid type value")
	}
	if !errors.Is(err, ErrInvalidType) {
		t.Errorf("Expected ErrInvalidType, got %v", err)
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
		genPackageType(),       // type
		gen.Bool(),             // has headers
		genHeaders(),           // headers
		gen.Bool(),             // has versions_selector
		genValidCSSSelector(),  // versions_selector
	).Map(func(values []interface{}) PackageConfig {
		cfg := PackageConfig{
			URL:    values[0].(string),
			Parser: "html",
			Type:   values[6].(string),
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
		genPackageType(),   // type
		gen.Bool(),         // has versions_path
		genValidJSONPath(), // versions_path
		gen.Bool(),         // has headers
		genHeaders(),       // headers
	).Map(func(values []interface{}) PackageConfig {
		cfg := PackageConfig{
			URL:    values[0].(string),
			Parser: "json",
			Path:   values[1].(string),
			Type:   values[2].(string),
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

// TestPackageConfigIsEnabled verifies the enabled gate: an absent (nil) field
// counts as enabled (the default and the legacy case), an explicit true is
// enabled, and only an explicit false disables the package.
func TestPackageConfigIsEnabled(t *testing.T) {
	enabled, disabled := true, false
	cases := []struct {
		name string
		ptr  *bool
		want bool
	}{
		{"absent defaults to enabled", nil, true},
		{"explicit true", &enabled, true},
		{"explicit false", &disabled, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := PackageConfig{Enabled: tc.ptr}
			if got := cfg.IsEnabled(); got != tc.want {
				t.Errorf("IsEnabled() = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestLoadPackagesConfigEnabledField verifies that enabled = false round-trips
// through LoadPackagesConfig and that an entry omitting the field loads as
// enabled (nil pointer), so legacy configs need no migration.
func TestLoadPackagesConfigEnabledField(t *testing.T) {
	tmpDir := t.TempDir()
	configDir := filepath.Join(tmpDir, ".autoupdate")
	if err := os.MkdirAll(configDir, 0755); err != nil {
		t.Fatalf("Failed to create config dir: %v", err)
	}

	cfgTOML := `["net-vpn/openvpn"]
enabled = false
url = "https://openvpn.net/community-downloads/"
parser = "regex"
pattern = 'OpenVPN-([0-9]+\.[0-9]+\.[0-9]+)'

["app-editors/vscode"]
url = "https://api.github.com/repos/microsoft/vscode/releases/latest"
parser = "json"
path = "tag_name"
`
	if err := os.WriteFile(filepath.Join(configDir, "packages.toml"), []byte(cfgTOML), 0644); err != nil {
		t.Fatalf("Failed to write TOML: %v", err)
	}

	config, err := LoadPackagesConfig(tmpDir)
	if err != nil {
		t.Fatalf("Expected no error, got: %v", err)
	}

	openvpn, ok := config.Packages["net-vpn/openvpn"]
	if !ok {
		t.Fatal("Expected net-vpn/openvpn in config")
	}
	if openvpn.Enabled == nil || *openvpn.Enabled {
		t.Errorf("Expected openvpn Enabled to be explicit false, got %v", openvpn.Enabled)
	}
	if openvpn.IsEnabled() {
		t.Error("Expected openvpn IsEnabled() to be false")
	}

	vscode, ok := config.Packages["app-editors/vscode"]
	if !ok {
		t.Fatal("Expected app-editors/vscode in config")
	}
	if vscode.Enabled != nil {
		t.Errorf("Expected vscode Enabled to be nil (absent), got %v", *vscode.Enabled)
	}
	if !vscode.IsEnabled() {
		t.Error("Expected vscode IsEnabled() to be true (default)")
	}
}

// TestValidatePackageConfigVersion covers the version pin's two validation
// rules: a non-empty version must be a well-formed Gentoo version, revision
// suffix included, and — when the entry also declares a series — must belong
// to that series. An absent version stays valid, so every pre-existing record
// loads without migration.
func TestValidatePackageConfigVersion(t *testing.T) {
	base := func() *PackageConfig {
		return &PackageConfig{
			URL:    "https://example.com/api",
			Parser: "json",
			Path:   "version",
		}
	}

	t.Run("absent version still validates", func(t *testing.T) {
		if err := ValidatePackageConfig("test/pkg", base()); err != nil {
			t.Errorf("Expected no error, got: %v", err)
		}
	})

	t.Run("valid pin passes", func(t *testing.T) {
		cfg := base()
		cfg.Version = "1.28.4"
		if err := ValidatePackageConfig("test/pkg", cfg); err != nil {
			t.Errorf("Expected no error, got: %v", err)
		}
	})

	t.Run("pin with revision suffix passes", func(t *testing.T) {
		cfg := base()
		cfg.Version = "2.52.4-r411" // the webkit-gtk shape
		if err := ValidatePackageConfig("net-libs/webkit-gtk:4.1", cfg); err != nil {
			t.Errorf("Expected no error, got: %v", err)
		}
	})

	t.Run("malformed pin fails naming the key", func(t *testing.T) {
		cfg := base()
		cfg.Version = "not-a-version"
		err := ValidatePackageConfig("test/pkg", cfg)
		if err == nil {
			t.Fatal("Expected error for malformed version")
		}
		if !errors.Is(err, ErrInvalidVersion) {
			t.Errorf("Expected ErrInvalidVersion, got %v", err)
		}
		if !strings.Contains(err.Error(), "test/pkg") {
			t.Errorf("Expected the entry key in the message, got %q", err.Error())
		}
		if !strings.Contains(err.Error(), "not-a-version") {
			t.Errorf("Expected the offending value in the message, got %q", err.Error())
		}
	})

	t.Run("pin outside its own series fails", func(t *testing.T) {
		cfg := base()
		cfg.Version = "1.29.2"
		cfg.Series = `^1\.28\.`
		err := ValidatePackageConfig("test/pkg", cfg)
		if err == nil {
			t.Fatal("Expected error for version outside series")
		}
		if !errors.Is(err, ErrVersionOutsideSeries) {
			t.Errorf("Expected ErrVersionOutsideSeries, got %v", err)
		}
		if !strings.Contains(err.Error(), "test/pkg") {
			t.Errorf("Expected the entry key in the message, got %q", err.Error())
		}
		if !strings.Contains(err.Error(), "1.29.2") {
			t.Errorf("Expected the offending value in the message, got %q", err.Error())
		}
	})

	t.Run("pin inside its series passes", func(t *testing.T) {
		cfg := base()
		cfg.Version = "1.28.4"
		cfg.Series = `^1\.28\.`
		if err := ValidatePackageConfig("test/pkg", cfg); err != nil {
			t.Errorf("Expected no error, got: %v", err)
		}
	})
}

// TestValidatePackageConfigMetaFetch covers the fetch_* sub-schema hidden inside
// the free-form [meta] map. Every key inside a map[string]string is claimed by
// the map, so the decoder's unknown-key check cannot see into it: without these
// rules a misspelled key does not fail anything, it just quietly turns the
// authenticated download off and lets pkgdev chase a distfile no mirror has.
//
// The rules must stay a mirror of parseAuthFetchSpec, never a stricter schema of
// their own — a validation the consumer does not share would reject a record
// that works today.
func TestValidatePackageConfigMetaFetch(t *testing.T) {
	base := func(meta map[string]string) *PackageConfig {
		return &PackageConfig{
			URL:     "https://filezillapro.com/filezilla-pro-version-history/",
			Parser:  "regex",
			Pattern: `Latest:\s*([0-9]+\.[0-9]+\.[0-9]+)`,
			Meta:    meta,
		}
	}
	// The net-ftp/filezilla-pro shape — the only record in the registry using
	// this sub-schema. Inlined rather than read from the maintainer's overlay so
	// the test runs on a clean machine.
	filezillaMeta := func() map[string]string {
		return map[string]string{
			"fetch_method":       "post",
			"fetch_url":          "https://filezilla-project.org/prodownload.php?beta=0",
			"fetch_serial_env":   "FILEZILLA_PRO_KEY",
			"fetch_serial_field": "key",
			"fetch_form":         "mail=&number=&platform=linux&download_program=Start download of FileZilla Pro",
			"fetch_filename":     "FileZilla_Pro_{version}_x86_64-linux-gnu.tar.xz",
		}
	}

	t.Run("the real filezilla-pro shape validates", func(t *testing.T) {
		if err := ValidatePackageConfig("net-ftp/filezilla-pro", base(filezillaMeta())); err != nil {
			t.Errorf("Expected no error, got: %v", err)
		}
	})

	t.Run("meta without any fetch_ key is untouched", func(t *testing.T) {
		for _, meta := range []map[string]string{
			nil,
			{},
			{"requires_serial": "true", "platform": "linux", "notes": "bought 2024"},
		} {
			if err := ValidatePackageConfig("test/pkg", base(meta)); err != nil {
				t.Errorf("meta %v: expected no error, got: %v", meta, err)
			}
		}
	})

	// R5.1 — the silent failure the whole rule exists for: the trigger is gone,
	// so the applier reads the block as "no authenticated fetch" and says nothing.
	t.Run("fetch_serial_env without fetch_url fails", func(t *testing.T) {
		cfg := base(map[string]string{"fetch_serial_env": "FILEZILLA_PRO_KEY"})
		err := ValidatePackageConfig("net-ftp/filezilla-pro", cfg)
		if err == nil {
			t.Fatal("Expected error for a fetch_* block without fetch_url")
		}
		if !errors.Is(err, ErrMetaFetchURLRequired) {
			t.Errorf("Expected ErrMetaFetchURLRequired, got %v", err)
		}
		if !strings.Contains(err.Error(), "net-ftp/filezilla-pro") {
			t.Errorf("Expected the entry key in the message, got %q", err.Error())
		}
		if !strings.Contains(err.Error(), "fetch_serial_env") {
			t.Errorf("Expected the offending key in the message, got %q", err.Error())
		}
	})

	// A blanked-out URL disables the download exactly as silently as a missing
	// one, because parseAuthFetchSpec trims before testing the trigger.
	t.Run("blank fetch_url fails like a missing one", func(t *testing.T) {
		meta := filezillaMeta()
		meta["fetch_url"] = "   "
		err := ValidatePackageConfig("test/pkg", base(meta))
		if err == nil {
			t.Fatal("Expected error for a blank fetch_url")
		}
		if !errors.Is(err, ErrMetaFetchURLRequired) {
			t.Errorf("Expected ErrMetaFetchURLRequired, got %v", err)
		}
	})

	// R5.2 — the parser lowercases the method before comparing, so an uppercase
	// value works at apply time and must not be rejected here.
	t.Run("fetch_method case and default", func(t *testing.T) {
		for _, valid := range []string{"post", "POST", "get", "Get", "  post  ", ""} {
			meta := filezillaMeta()
			meta["fetch_method"] = valid
			if err := ValidatePackageConfig("test/pkg", base(meta)); err != nil {
				t.Errorf("fetch_method %q: unexpected error: %v", valid, err)
			}
		}
		// Absent is legal too: the parser defaults it to "post".
		meta := filezillaMeta()
		delete(meta, "fetch_method")
		if err := ValidatePackageConfig("test/pkg", base(meta)); err != nil {
			t.Errorf("absent fetch_method: unexpected error: %v", err)
		}
	})

	t.Run("fetch_method PUT fails", func(t *testing.T) {
		meta := filezillaMeta()
		meta["fetch_method"] = "PUT"
		err := ValidatePackageConfig("test/pkg", base(meta))
		if err == nil {
			t.Fatal("Expected error for fetch_method = \"PUT\"")
		}
		if !errors.Is(err, ErrInvalidMetaFetchMethod) {
			t.Errorf("Expected ErrInvalidMetaFetchMethod, got %v", err)
		}
		if !strings.Contains(err.Error(), "PUT") {
			t.Errorf("Expected the offending value in the message, got %q", err.Error())
		}
	})

	// R5.3 — the misspelling M2 measured. The record still has a valid
	// fetch_url, so nothing else fires: only the unknown-key rule can catch it.
	t.Run("misspelled fetch_serial_env is reported as unknown", func(t *testing.T) {
		meta := filezillaMeta()
		delete(meta, "fetch_serial_env")
		meta["fetch_seral_env"] = "FILEZILLA_PRO_KEY"
		err := ValidatePackageConfig("net-ftp/filezilla-pro", base(meta))
		if err == nil {
			t.Fatal("Expected error for the misspelled fetch_seral_env")
		}
		if !errors.Is(err, ErrUnknownMetaFetchKey) {
			t.Errorf("Expected ErrUnknownMetaFetchKey, got %v", err)
		}
		if !strings.Contains(err.Error(), "fetch_seral_env") {
			t.Errorf("Expected the misspelled key in the message, got %q", err.Error())
		}
		// The message must offer the right spelling, or it names a mistake
		// without saying what the fix is.
		if !strings.Contains(err.Error(), "fetch_serial_env") {
			t.Errorf("Expected the known keys in the message, got %q", err.Error())
		}
	})

	// Every offending key at once, in a stable order: map iteration is random, so
	// an unsorted message would reshuffle between runs and be useless in a diff.
	t.Run("all unknown keys are reported deterministically", func(t *testing.T) {
		meta := filezillaMeta()
		meta["fetch_zebra"] = "1"
		meta["fetch_alpha"] = "2"
		first := ValidatePackageConfig("test/pkg", base(meta))
		if first == nil {
			t.Fatal("Expected error for the unknown fetch_* keys")
		}
		if !strings.Contains(first.Error(), "fetch_alpha, fetch_zebra") {
			t.Errorf("Expected both keys sorted in the message, got %q", first.Error())
		}
		for i := 0; i < 20; i++ {
			again := ValidatePackageConfig("test/pkg", base(meta))
			if again == nil || again.Error() != first.Error() {
				t.Fatalf("message is not stable across runs: %v vs %v", first, again)
			}
		}
	})

	// The validator must stop where parseAuthFetchSpec already fails loudly:
	// those companions are checked at apply time with a precise message, and
	// re-checking them here would let one broken record block the whole load.
	t.Run("companions the parser checks are left to the parser", func(t *testing.T) {
		for _, key := range []string{"fetch_serial_env", "fetch_serial_field", "fetch_filename"} {
			meta := filezillaMeta()
			delete(meta, key)
			if err := ValidatePackageConfig("test/pkg", base(meta)); err != nil {
				t.Errorf("missing %s: expected the load to pass and the apply to fail, got: %v", key, err)
			}
			if _, _, perr := parseAuthFetchSpec(meta); perr == nil {
				t.Errorf("missing %s: expected parseAuthFetchSpec to reject it at apply time", key)
			}
		}
	})
}

// A record without a version key gets the pin inserted immediately before its
// comments assignment — after every other field, since comments must be last —
// and a record with no comments field gets it immediately before `# END`.
func TestSetPackageVersionsInsertsBeforeComments(t *testing.T) {
	content := `["media-plugins/gst-plugins-vpx@stable"]
url = "https://example.com/releases.json"
parser = "regex"
pattern = '"name":"([0-9]+\.[0-9]+\.[0-9]+)"'
select = "max"
series = '^1\.28\.'
comments = """gst-plugins-vpx: stable line."""
# END

["a/b"]
url = "https://x/y"
parser = "json"
path = "v"
# END
`
	overlay, configPath := writePackagesTOML(t, content)

	pins := map[string]string{
		"media-plugins/gst-plugins-vpx@stable": "1.28.5",
		"a/b":                                  "2.0.0",
	}
	if err := SetPackageVersions(overlay, pins); err != nil {
		t.Fatalf("SetPackageVersions: %v", err)
	}

	got, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	want := `["media-plugins/gst-plugins-vpx@stable"]
url = "https://example.com/releases.json"
parser = "regex"
pattern = '"name":"([0-9]+\.[0-9]+\.[0-9]+)"'
select = "max"
series = '^1\.28\.'
version = "1.28.5"
comments = """gst-plugins-vpx: stable line."""
# END

["a/b"]
url = "https://x/y"
parser = "json"
path = "v"
version = "2.0.0"
# END
`
	if string(got) != want {
		t.Errorf("unexpected output:\n--- got ---\n%s\n--- want ---\n%s", got, want)
	}

	// The result must still parse and carry the pins.
	cfg, err := LoadPackagesConfig(overlay)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if v := cfg.Packages["media-plugins/gst-plugins-vpx@stable"].Version; v != "1.28.5" {
		t.Errorf("expected version 1.28.5 after edit, got %q", v)
	}
	if v := cfg.Packages["a/b"].Version; v != "2.0.0" {
		t.Errorf("expected version 2.0.0 after edit, got %q", v)
	}
}

// An existing version assignment is rewritten in place: same position, nothing
// else in the record moves.
func TestSetPackageVersionsRewritesExisting(t *testing.T) {
	content := `["a/b"]
url = "https://x/y"
parser = "json"
path = "v"
series = '^1\.28\.'
version = "1.28.4"
comments = """a/b: pinned to the stable line."""
# END
`
	overlay, configPath := writePackagesTOML(t, content)
	if err := SetPackageVersions(overlay, map[string]string{"a/b": "1.28.5"}); err != nil {
		t.Fatalf("SetPackageVersions: %v", err)
	}
	got, _ := os.ReadFile(configPath)
	want := `["a/b"]
url = "https://x/y"
parser = "json"
path = "v"
series = '^1\.28\.'
version = "1.28.5"
comments = """a/b: pinned to the stable line."""
# END
`
	if string(got) != want {
		t.Errorf("unexpected output:\n--- got ---\n%s\n--- want ---\n%s", got, want)
	}

	cfg, err := LoadPackagesConfig(overlay)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if v := cfg.Packages["a/b"].Version; v != "1.28.5" {
		t.Errorf("expected version 1.28.5 after edit, got %q", v)
	}
}

// A multi-line comments body may contain `#`-prefixed lines, `[`-prefixed lines
// that look exactly like section headers, and even `version = ...`-shaped text.
// None of it is structure: the record must survive byte-identical apart from
// the one inserted pin, placed before the comments assignment.
func TestSetPackageVersionsPreservesMultilineComments(t *testing.T) {
	content := `["a/b"]
url = "https://x/y"
parser = "json"
path = "v"
comments = """a/b: doc body with hostile lines.
# this is doc text, not a marker
[gotcha/section]
version = "9.9.9" would have been rewritten by an unguarded scanner
"""
# END

["c/d"]
url = "https://x/z"
parser = "json"
path = "v"
comments = """c/d: untouched neighbour."""
# END
`
	overlay, configPath := writePackagesTOML(t, content)
	if err := SetPackageVersions(overlay, map[string]string{"a/b": "1.2.3"}); err != nil {
		t.Fatalf("SetPackageVersions: %v", err)
	}
	got, _ := os.ReadFile(configPath)
	want := `["a/b"]
url = "https://x/y"
parser = "json"
path = "v"
version = "1.2.3"
comments = """a/b: doc body with hostile lines.
# this is doc text, not a marker
[gotcha/section]
version = "9.9.9" would have been rewritten by an unguarded scanner
"""
# END

["c/d"]
url = "https://x/z"
parser = "json"
path = "v"
comments = """c/d: untouched neighbour."""
# END
`
	if string(got) != want {
		t.Errorf("comments body corrupted:\n--- got ---\n%s\n--- want ---\n%s", got, want)
	}

	cfg, err := LoadPackagesConfig(overlay)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if v := cfg.Packages["a/b"].Version; v != "1.2.3" {
		t.Errorf("expected version 1.2.3 after edit, got %q", v)
	}
}

// A pin whose section is absent is skipped silently — no error, no write — and
// in a mixed batch the present sections are still edited.
func TestSetPackageVersionsAbsentSectionSkipped(t *testing.T) {
	content := `["a/b"]
url = "https://x/y"
parser = "json"
path = "v"
comments = """a/b: doc."""
# END
`
	overlay, configPath := writePackagesTOML(t, content)
	before, _ := os.ReadFile(configPath)

	// Absent-only batch: no error, file untouched.
	if err := SetPackageVersions(overlay, map[string]string{"c/d": "1.0.0"}); err != nil {
		t.Fatalf("absent section: %v", err)
	}
	after, _ := os.ReadFile(configPath)
	if string(before) != string(after) {
		t.Errorf("file changed for an absent section:\n%s", after)
	}

	// Mixed batch: the absent key is skipped, the present one is pinned.
	if err := SetPackageVersions(overlay, map[string]string{"c/d": "1.0.0", "a/b": "2.0.0"}); err != nil {
		t.Fatalf("mixed batch: %v", err)
	}
	cfg, err := LoadPackagesConfig(overlay)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if v := cfg.Packages["a/b"].Version; v != "2.0.0" {
		t.Errorf("expected version 2.0.0 after mixed batch, got %q", v)
	}
}

// An empty (or nil) map returns before any I/O — bytes AND mtime unchanged —
// and a batch that re-pins the value already written changes nothing either.
func TestSetPackageVersionsNoOps(t *testing.T) {
	content := `["a/b"]
url = "https://x/y"
parser = "json"
path = "v"
version = "1.2.3"
comments = """a/b: doc."""
# END
`
	overlay, configPath := writePackagesTOML(t, content)
	before, _ := os.ReadFile(configPath)
	infoBefore, err := os.Stat(configPath)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}

	if err := SetPackageVersions(overlay, nil); err != nil {
		t.Fatalf("nil map: %v", err)
	}
	if err := SetPackageVersions(overlay, map[string]string{}); err != nil {
		t.Fatalf("empty map: %v", err)
	}
	after, _ := os.ReadFile(configPath)
	infoAfter, err := os.Stat(configPath)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if string(before) != string(after) {
		t.Errorf("empty map changed the bytes:\n%s", after)
	}
	if !infoBefore.ModTime().Equal(infoAfter.ModTime()) {
		t.Errorf("empty map changed the mtime: before %v, after %v", infoBefore.ModTime(), infoAfter.ModTime())
	}

	// Re-pinning the value already in the file must not rewrite it.
	if err := SetPackageVersions(overlay, map[string]string{"a/b": "1.2.3"}); err != nil {
		t.Fatalf("same-value batch: %v", err)
	}
	after, _ = os.ReadFile(configPath)
	if string(before) != string(after) {
		t.Errorf("same-value batch changed the bytes:\n%s", after)
	}
}

// The atomic rewrite must preserve the original file mode.
func TestSetPackageVersionsPreservesFileMode(t *testing.T) {
	content := `["a/b"]
url = "https://x/y"
parser = "json"
path = "v"
comments = """a/b: doc."""
# END
`
	overlay, configPath := writePackagesTOML(t, content)
	if err := os.Chmod(configPath, 0o600); err != nil {
		t.Fatalf("chmod: %v", err)
	}

	if err := SetPackageVersions(overlay, map[string]string{"a/b": "1.2.3"}); err != nil {
		t.Fatalf("SetPackageVersions: %v", err)
	}

	info, err := os.Stat(configPath)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Errorf("file mode not preserved: got %v, want %v", got, os.FileMode(0o600))
	}
}
