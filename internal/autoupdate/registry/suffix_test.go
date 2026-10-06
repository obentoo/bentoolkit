package registry

import (
	"errors"
	"testing"
)

// TestValidatePackageConfigSuffix covers the field validation: a typo here would
// be written straight into an ebuild filename.
func TestValidatePackageConfigSuffix(t *testing.T) {
	base := func() *PackageConfig {
		return &PackageConfig{URL: "https://example.com/x", Parser: "json", Path: "version"}
	}

	t.Run("valid suffixes accepted", func(t *testing.T) {
		for _, s := range []string{"_alpha", "_beta2", "_pre", "_rc1", "_p", "_p20260731"} {
			cfg := base()
			cfg.Suffix = s
			if err := ValidatePackageConfig(nil, "app-office/x", cfg); err != nil {
				t.Fatalf("suffix %q rejected: %v", s, err)
			}
		}
	})

	t.Run("invalid suffixes rejected", func(t *testing.T) {
		for _, s := range []string{"pre", "_dev", "_PRE", "_pre_p1", "-r1", "_"} {
			cfg := base()
			cfg.Suffix = s
			err := ValidatePackageConfig(nil, "app-office/x", cfg)
			if !errors.Is(err, ErrInvalidSuffix) {
				t.Fatalf("suffix %q: got %v, want ErrInvalidSuffix", s, err)
			}
		}
	})

	t.Run("suffix_when without suffix rejected", func(t *testing.T) {
		cfg := base()
		cfg.SuffixWhen = `^26\.8\.`
		if err := ValidatePackageConfig(nil, "app-office/x", cfg); !errors.Is(err, ErrSuffixWhenWithoutSuffix) {
			t.Fatalf("got %v, want ErrSuffixWhenWithoutSuffix", err)
		}
	})

	t.Run("uncompilable suffix_when rejected", func(t *testing.T) {
		cfg := base()
		cfg.Suffix = "_pre"
		cfg.SuffixWhen = `^(26\.8`
		if err := ValidatePackageConfig(nil, "app-office/x", cfg); err == nil {
			t.Fatal("uncompilable suffix_when accepted")
		}
	})

	t.Run("suffix with track=commit rejected", func(t *testing.T) {
		cfg := base()
		cfg.Suffix = "_pre"
		cfg.Track = "commit"
		cfg.CommitSHAPath = "[0].sha"
		if err := ValidatePackageConfig(nil, "sci-ml/x", cfg); err == nil {
			t.Fatal("suffix combined with track=\"commit\" accepted")
		}
	})
}
