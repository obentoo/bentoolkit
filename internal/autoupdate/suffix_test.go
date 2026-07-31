package autoupdate

import (
	"errors"
	"testing"
)

// TestApplySuffix covers the pre-release marker a record declares for a version
// upstream numbers as if it were final.
func TestApplySuffix(t *testing.T) {
	tests := []struct {
		name string
		in   string
		cfg  *PackageConfig
		want string
	}{
		{"no suffix configured is identity", "26.8.0.1", &PackageConfig{}, "26.8.0.1"},
		{"nil config is identity", "26.8.0.1", nil, "26.8.0.1"},
		{"empty version stays empty", "", &PackageConfig{Suffix: "_pre"}, ""},
		{
			"unconditional suffix applies",
			"26.8.0.1",
			&PackageConfig{Suffix: "_pre"},
			"26.8.0.1_pre",
		},
		{
			// LibreOffice: one archive index lists the stable 26.2 line and the
			// testing 26.8 one, so only the latter is marked.
			"suffix_when marks the matching release line",
			"26.8.0.1",
			&PackageConfig{Suffix: "_pre", SuffixWhen: `^26\.8\.`},
			"26.8.0.1_pre",
		},
		{
			"suffix_when leaves the other release line bare",
			"26.2.5.2",
			&PackageConfig{Suffix: "_pre", SuffixWhen: `^26\.8\.`},
			"26.2.5.2",
		},
		{
			// The pipeline applies the suffix per candidate AND once on the final
			// version; the second pass must not stack.
			"already suffixed version is untouched",
			"26.8.0.1_pre",
			&PackageConfig{Suffix: "_pre"},
			"26.8.0.1_pre",
		},
		{
			"upstream's own marker is not overwritten",
			"2.0.0_rc1",
			&PackageConfig{Suffix: "_pre"},
			"2.0.0_rc1",
		},
		{
			"suffixed version with a revision is untouched",
			"2.52.5_rc1-r600",
			&PackageConfig{Suffix: "_pre"},
			"2.52.5_rc1-r600",
		},
		{
			"numbered suffix is appended verbatim",
			"4.8",
			&PackageConfig{Suffix: "_alpha2"},
			"4.8_alpha2",
		},
		{
			// ValidatePackageConfig rejects this up front; a check that reached
			// here must degrade to the bare version rather than die.
			"uncompilable suffix_when is ignored",
			"26.8.0.1",
			&PackageConfig{Suffix: "_pre", SuffixWhen: `^(26\.8`},
			"26.8.0.1",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := applySuffix(tt.in, tt.cfg); got != tt.want {
				t.Fatalf("applySuffix(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

// TestSelectVersionAppliesSuffix pins that selection orders the values that will
// actually become the PV: a candidate list spanning two release lines must be
// compared with the development line already marked.
func TestSelectVersionAppliesSuffix(t *testing.T) {
	cands := []string{"26.2.5.2", "26.8.0.1", "26.2.4.1"}
	cfg := &PackageConfig{Select: "max", Suffix: "_pre", SuffixWhen: `^26\.8\.`}

	if got, want := selectVersion(cands, cfg), "26.8.0.1_pre"; got != want {
		t.Fatalf("selectVersion = %q, want %q", got, want)
	}
}

// TestSelectVersionSuffixOrdersBelowRelease pins the ordering the suffix buys:
// once upstream promotes the line, the bare release outranks the _pre snapshot
// that shares its version, so the bump fires.
func TestSelectVersionSuffixOrdersBelowRelease(t *testing.T) {
	// Both candidates land in the same series; only the first is marked _pre.
	cfg := &PackageConfig{Select: "max", Suffix: "_pre", SuffixWhen: `^26\.8\.0\.1$`}

	got := selectVersion([]string{"26.8.0.1", "26.8.0.4"}, cfg)
	if got != "26.8.0.4" {
		t.Fatalf("selectVersion = %q, want %q (bare release must outrank the _pre one)", got, "26.8.0.4")
	}
}

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
			if err := ValidatePackageConfig("app-office/x", cfg); err != nil {
				t.Fatalf("suffix %q rejected: %v", s, err)
			}
		}
	})

	t.Run("invalid suffixes rejected", func(t *testing.T) {
		for _, s := range []string{"pre", "_dev", "_PRE", "_pre_p1", "-r1", "_"} {
			cfg := base()
			cfg.Suffix = s
			err := ValidatePackageConfig("app-office/x", cfg)
			if !errors.Is(err, ErrInvalidSuffix) {
				t.Fatalf("suffix %q: got %v, want ErrInvalidSuffix", s, err)
			}
		}
	})

	t.Run("suffix_when without suffix rejected", func(t *testing.T) {
		cfg := base()
		cfg.SuffixWhen = `^26\.8\.`
		if err := ValidatePackageConfig("app-office/x", cfg); !errors.Is(err, ErrSuffixWhenWithoutSuffix) {
			t.Fatalf("got %v, want ErrSuffixWhenWithoutSuffix", err)
		}
	})

	t.Run("uncompilable suffix_when rejected", func(t *testing.T) {
		cfg := base()
		cfg.Suffix = "_pre"
		cfg.SuffixWhen = `^(26\.8`
		if err := ValidatePackageConfig("app-office/x", cfg); err == nil {
			t.Fatal("uncompilable suffix_when accepted")
		}
	})

	t.Run("suffix with track=commit rejected", func(t *testing.T) {
		cfg := base()
		cfg.Suffix = "_pre"
		cfg.Track = "commit"
		cfg.CommitSHAPath = "[0].sha"
		if err := ValidatePackageConfig("sci-ml/x", cfg); err == nil {
			t.Fatal("suffix combined with track=\"commit\" accepted")
		}
	})
}
