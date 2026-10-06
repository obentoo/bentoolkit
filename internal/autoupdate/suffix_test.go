package autoupdate

import (
	"testing"

	"github.com/obentoo/bentoolkit/internal/autoupdate/registry"
)

// TestApplySuffix covers the pre-release marker a record declares for a version
// upstream numbers as if it were final.
func TestApplySuffix(t *testing.T) {
	tests := []struct {
		name string
		in   string
		cfg  *registry.PackageConfig
		want string
	}{
		{"no suffix configured is identity", "26.8.0.1", &registry.PackageConfig{}, "26.8.0.1"},
		{"nil config is identity", "26.8.0.1", nil, "26.8.0.1"},
		{"empty version stays empty", "", &registry.PackageConfig{Suffix: "_pre"}, ""},
		{
			"unconditional suffix applies",
			"26.8.0.1",
			&registry.PackageConfig{Suffix: "_pre"},
			"26.8.0.1_pre",
		},
		{
			// LibreOffice: one archive index lists the stable 26.2 line and the
			// testing 26.8 one, so only the latter is marked.
			"suffix_when marks the matching release line",
			"26.8.0.1",
			&registry.PackageConfig{Suffix: "_pre", SuffixWhen: `^26\.8\.`},
			"26.8.0.1_pre",
		},
		{
			"suffix_when leaves the other release line bare",
			"26.2.5.2",
			&registry.PackageConfig{Suffix: "_pre", SuffixWhen: `^26\.8\.`},
			"26.2.5.2",
		},
		{
			// The pipeline applies the suffix per candidate AND once on the final
			// version; the second pass must not stack.
			"already suffixed version is untouched",
			"26.8.0.1_pre",
			&registry.PackageConfig{Suffix: "_pre"},
			"26.8.0.1_pre",
		},
		{
			"upstream's own marker is not overwritten",
			"2.0.0_rc1",
			&registry.PackageConfig{Suffix: "_pre"},
			"2.0.0_rc1",
		},
		{
			"suffixed version with a revision is untouched",
			"2.52.5_rc1-r600",
			&registry.PackageConfig{Suffix: "_pre"},
			"2.52.5_rc1-r600",
		},
		{
			"numbered suffix is appended verbatim",
			"4.8",
			&registry.PackageConfig{Suffix: "_alpha2"},
			"4.8_alpha2",
		},
		{
			// ValidatePackageConfig rejects this up front; a check that reached
			// here must degrade to the bare version rather than die.
			"uncompilable suffix_when is ignored",
			"26.8.0.1",
			&registry.PackageConfig{Suffix: "_pre", SuffixWhen: `^(26\.8`},
			"26.8.0.1",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := applySuffix(nil, tt.in, tt.cfg); got != tt.want {
				t.Fatalf("applySuffix(nil, %q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

// TestSelectVersionAppliesSuffix pins that selection orders the values that will
// actually become the PV: a candidate list spanning two release lines must be
// compared with the development line already marked.
func TestSelectVersionAppliesSuffix(t *testing.T) {
	cands := []string{"26.2.5.2", "26.8.0.1", "26.2.4.1"}
	cfg := &registry.PackageConfig{Select: "max", Suffix: "_pre", SuffixWhen: `^26\.8\.`}

	if got, want := selectVersion(nil, cands, cfg), "26.8.0.1_pre"; got != want {
		t.Fatalf("selectVersion = %q, want %q", got, want)
	}
}

// TestSelectVersionSuffixOrdersBelowRelease pins the ordering the suffix buys:
// once upstream promotes the line, the bare release outranks the _pre snapshot
// that shares its version, so the bump fires.
func TestSelectVersionSuffixOrdersBelowRelease(t *testing.T) {
	// Both candidates land in the same series; only the first is marked _pre.
	cfg := &registry.PackageConfig{Select: "max", Suffix: "_pre", SuffixWhen: `^26\.8\.0\.1$`}

	got := selectVersion(nil, []string{"26.8.0.1", "26.8.0.4"}, cfg)
	if got != "26.8.0.4" {
		t.Fatalf("selectVersion = %q, want %q (bare release must outrank the _pre one)", got, "26.8.0.4")
	}
}
