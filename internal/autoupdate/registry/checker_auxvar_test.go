package registry

import (
	"testing"
)

// TestValidate_AuxVar covers the parser-agnostic validation: both fields are
// mutually required, the pattern must compile, and a regex/html parser is
// explicitly allowed (that is the whole point of the feature).
func TestValidate_AuxVar(t *testing.T) {
	t.Run("regex ok", func(t *testing.T) {
		cfg := PackageConfig{
			URL:        "https://x",
			Parser:     "regex",
			Pattern:    `Betterbird\s*([0-9.]+)esr-bb[0-9]+`,
			AuxVar:     "MY_BUILD",
			AuxPattern: `Betterbird\s*[0-9.]+(esr-bb[0-9]+)`,
		}
		if err := ValidatePackageConfig(nil, "mail-client/betterbird-bin", &cfg); err != nil {
			t.Errorf("expected valid, got %v", err)
		}
	})
	t.Run("aux_var without aux_pattern rejected", func(t *testing.T) {
		cfg := PackageConfig{URL: "https://x", Parser: "regex", Pattern: "v(.*)", AuxVar: "MY_BUILD"}
		if err := ValidatePackageConfig(nil, "mail-client/betterbird-bin", &cfg); err == nil {
			t.Error("expected error: aux_var without aux_pattern")
		}
	})
	t.Run("aux_pattern without aux_var rejected", func(t *testing.T) {
		cfg := PackageConfig{URL: "https://x", Parser: "regex", Pattern: "v(.*)", AuxPattern: "(x)"}
		if err := ValidatePackageConfig(nil, "mail-client/betterbird-bin", &cfg); err == nil {
			t.Error("expected error: aux_pattern without aux_var")
		}
	})
	t.Run("invalid regex rejected", func(t *testing.T) {
		cfg := PackageConfig{URL: "https://x", Parser: "regex", Pattern: "v(.*)", AuxVar: "MY_BUILD", AuxPattern: "([0-9]+"}
		if err := ValidatePackageConfig(nil, "mail-client/betterbird-bin", &cfg); err == nil {
			t.Error("expected error: invalid aux_pattern regex")
		}
	})
}
