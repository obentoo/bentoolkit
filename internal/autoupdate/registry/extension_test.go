package registry

import (
	"errors"
	"testing"
)

// --- ValidatePackageConfig: new fields --------------------------------------

func TestValidatePackageConfig_Script(t *testing.T) {
	if err := ValidatePackageConfig(nil, "a/b", &PackageConfig{URL: "u", Parser: "script"}); !errors.Is(err, ErrMissingScript) {
		t.Fatalf("missing script: want ErrMissingScript, got %v", err)
	}
	if err := ValidatePackageConfig(nil, "a/b", &PackageConfig{URL: "u", Parser: "script", Script: "@lo.js"}); err != nil {
		t.Fatalf("valid script config: unexpected error %v", err)
	}
}

func TestValidatePackageConfig_Select(t *testing.T) {
	base := func(sel string) *PackageConfig {
		return &PackageConfig{URL: "u", Parser: "regex", Pattern: `(\d+)`, Select: sel}
	}
	for _, ok := range []string{"", "first", "max", "last"} {
		if err := ValidatePackageConfig(nil, "a/b", base(ok)); err != nil {
			t.Fatalf("select=%q should be valid, got %v", ok, err)
		}
	}
	if err := ValidatePackageConfig(nil, "a/b", base("highest")); !errors.Is(err, ErrInvalidSelect) {
		t.Fatalf("select=highest: want ErrInvalidSelect, got %v", err)
	}
}

func TestValidatePackageConfig_ScriptIgnoresTransformSelectWithWarn(t *testing.T) {
	lc := captureWarnLogs(t)
	cfg := &PackageConfig{
		URL: "u", Parser: "script", Script: "@lo.js",
		Transform: [][]string{{"-", "."}}, Select: "max",
	}
	if err := ValidatePackageConfig(lc.logger(), "a/b", cfg); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(lc.all()) < 2 {
		t.Fatalf("expected warnings for transform+select with script, got %v", lc.all())
	}
}
