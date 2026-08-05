package autoupdate

import (
	"strings"
	"testing"
)

// TestValidatePatched pins the `patched` field's whole contract (R1.1-R1.3).
//
// The field is the reason, not a flag: a non-empty string means "this ebuild
// deliberately diverges from ::gentoo" AND says how. Three readings must hold,
// and only three:
//
//   - a stated reason is accepted and is read back verbatim, because whoever
//     bumps the package next re-applies the delta from that text (R1.1);
//   - an absent field is not a divergence, so the 411 records that predate this
//     story load unchanged and mean exactly what they meant before (R1.2);
//   - a present-but-whitespace-only value is a hard validation failure, because
//     it claims a divergence it does not describe — the same rule that already
//     rejects an empty "@" label (R1.3).
//
// The whitespace case is the load-bearing one. Accepting it would silently
// protect a package from the removal recommendation for no stated reason, which
// is precisely the confidently-wrong outcome the story exists to prevent.
func TestValidatePatched(t *testing.T) {
	// validBase is a record that passes validation for reasons unrelated to
	// `patched`, so every failure below is attributable to `patched` alone.
	validBase := func() *PackageConfig {
		return &PackageConfig{
			URL:    "https://api.github.com/repos/example/example/releases/latest",
			Parser: "json",
			Path:   "tag_name",
		}
	}

	t.Run("a stated reason is accepted and kept verbatim (R1.1)", func(t *testing.T) {
		cfg := validBase()
		cfg.Patched = "keeps our wayland-by-default patch and the extra USE flag"

		if err := ValidatePackageConfig("app-editors/zed", cfg); err != nil {
			t.Fatalf("ValidatePackageConfig with a stated reason returned %v, want nil", err)
		}
		if cfg.Patched != "keeps our wayland-by-default patch and the extra USE flag" {
			t.Errorf("Patched = %q; validation must not rewrite the declared reason", cfg.Patched)
		}
	})

	t.Run("an absent field is not a divergence and validates (R1.2)", func(t *testing.T) {
		cfg := validBase()

		if err := ValidatePackageConfig("app-editors/vscode", cfg); err != nil {
			t.Fatalf("ValidatePackageConfig without patched returned %v, want nil", err)
		}
		if cfg.Patched != "" {
			t.Errorf("Patched = %q, want %q: absent means identical to ::gentoo apart from the version", cfg.Patched, "")
		}
	})

	t.Run("a whitespace-only claim fails validation (R1.3)", func(t *testing.T) {
		for _, blank := range []string{" ", "   ", "\t", "\n", " \t\n "} {
			cfg := validBase()
			cfg.Patched = blank

			err := ValidatePackageConfig("app-editors/zed", cfg)
			if err == nil {
				t.Errorf("ValidatePackageConfig with patched = %q returned nil; a divergence that describes nothing must be rejected", blank)
				continue
			}
			msg := err.Error()
			if !strings.Contains(msg, "patched") {
				t.Errorf("error for patched = %q is %q; it must name the field so the maintainer knows what to fix", blank, msg)
			}
			if !strings.Contains(msg, "app-editors/zed") {
				t.Errorf("error for patched = %q is %q; it must name the record, one of ~411", blank, msg)
			}
		}
	})
}

// TestLoadPackagesConfigPatched pins the field at the registry boundary: the
// decoder must claim the key (strict decoding fails the load for a key no field
// claims), the declared text must survive the round trip, and a record without
// the key must load with an empty Patched — the no-migration guarantee of R1.2.
func TestLoadPackagesConfigPatched(t *testing.T) {
	dir := writeRegistry(t, `["app-editors/zed"]
url = "https://api.github.com/repos/zed-industries/zed/releases/latest"
parser = "json"
path = "tag_name"
type = "source"
patched = "keeps our wayland-by-default patch"
comments = """
zed — carries a local patch that must survive every bump.
"""
# END

["app-editors/vscode"]
url = "https://api.github.com/repos/microsoft/vscode/releases/latest"
parser = "json"
path = "tag_name"
comments = """
vscode — ::gentoo's ebuild apart from the version.
"""
# END
`)

	cfg, err := LoadPackagesConfig(dir)
	if err != nil {
		t.Fatalf("LoadPackagesConfig with a patched record returned %v; the key must be claimed by a struct field", err)
	}

	zed, ok := cfg.Packages["app-editors/zed"]
	if !ok {
		t.Fatal("app-editors/zed missing from the loaded registry")
	}
	if zed.Patched != "keeps our wayland-by-default patch" {
		t.Errorf("zed.Patched = %q, want the declared reason verbatim", zed.Patched)
	}

	vscode, ok := cfg.Packages["app-editors/vscode"]
	if !ok {
		t.Fatal("app-editors/vscode missing from the loaded registry")
	}
	if vscode.Patched != "" {
		t.Errorf("vscode.Patched = %q, want empty: a record without the key predates the field and must load unchanged", vscode.Patched)
	}
}
