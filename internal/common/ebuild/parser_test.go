package ebuild

import (
	"testing"

	"github.com/leanovate/gopter"
	"github.com/leanovate/gopter/gen"
	"github.com/leanovate/gopter/prop"
)

// genCategory generates valid Gentoo category names (e.g., "app-misc", "sys-apps")
func genCategory() gopter.Gen {
	categories := []interface{}{
		"app-misc", "app-util", "dev-libs", "dev-util",
		"sys-apps", "sys-libs", "net-misc", "net-client",
		"www-client", "www-server", "media-libs", "x11-libs",
	}
	return gen.OneConstOf(categories...)
}

// genVersion generates valid Gentoo version strings
func genVersion() gopter.Gen {
	// Use predefined version patterns that are known to be valid
	versions := []interface{}{
		"1", "2", "10", "99",
		"1.0", "1.1", "2.0", "10.5", "99.99",
		"1.0.1", "1.2.3", "10.20.30",
		"1.0_rc1", "1.0_rc2", "2.0_rc1",
		"1.0_beta1", "1.0_beta2", "2.0_beta1",
		"1.0_alpha", "2.0_alpha",
		"1.0_p1", "1.0_p2",
		"1.0-r1", "1.0-r2", "1.0-r3",
		"1.0_rc1-r1", "1.0_beta2-r3",
		"120.0", "120.0_rc1", "120.0-r1",
	}
	return gen.OneConstOf(versions...)
}


// genPackageName generates valid package names
func genPackageName() gopter.Gen {
	packages := []interface{}{
		"hello", "world", "test", "foo", "bar",
		"firefox", "chrome", "vim", "emacs",
		"firefox-bin", "chrome-bin", "bentoo-utils",
	}
	return gen.OneConstOf(packages...)
}

// genEbuild generates valid Ebuild structs using predefined combinations
func genEbuild() gopter.Gen {
	return gopter.CombineGens(
		genCategory(),
		genPackageName(),
		genVersion(),
	).Map(func(values []interface{}) *Ebuild {
		cat := values[0].(string)
		pkg := values[1].(string)
		ver := values[2].(string)
		return &Ebuild{
			Category: cat,
			Package:  pkg,
			Name:     pkg, // Name equals Package for valid ebuilds
			Version:  ver,
		}
	})
}

// TestPropertyEbuildRoundTrip tests Property 2: Ebuild path parsing round-trip
// **Feature: overlay-manager, Property 2: Ebuild path parsing round-trip**
// **Validates: Requirements 7.1, 7.4**
func TestPropertyEbuildRoundTrip(t *testing.T) {
	parameters := gopter.DefaultTestParameters()
	parameters.MinSuccessfulTests = 100
	properties := gopter.NewProperties(parameters)

	properties.Property("String() then ParsePath() returns equivalent Ebuild", prop.ForAll(
		func(original *Ebuild) bool {
			// Convert to string
			pathStr := original.String()

			// Parse back
			parsed, err := ParsePath(pathStr)
			if err != nil {
				t.Logf("ParsePath failed for %q: %v", pathStr, err)
				return false
			}

			// Compare all fields
			return parsed.Category == original.Category &&
				parsed.Package == original.Package &&
				parsed.Name == original.Name &&
				parsed.Version == original.Version
		},
		genEbuild(),
	))

	properties.TestingRun(t)
}


// TestPropertyVersionComparisonConsistency tests Property 3: Version comparison consistency
// **Feature: overlay-manager, Property 3: Version comparison consistency**
// **Validates: Requirements 7.2**
func TestPropertyVersionComparisonConsistency(t *testing.T) {
	parameters := gopter.DefaultTestParameters()
	parameters.MinSuccessfulTests = 100
	properties := gopter.NewProperties(parameters)

	// Test antisymmetry: CompareVersions(v1, v2) == -CompareVersions(v2, v1)
	properties.Property("antisymmetry: CompareVersions(v1, v2) == -CompareVersions(v2, v1)", prop.ForAll(
		func(v1, v2 string) bool {
			cmp1 := CompareVersions(v1, v2)
			cmp2 := CompareVersions(v2, v1)
			return cmp1 == -cmp2
		},
		genVersion(),
		genVersion(),
	))

	// Test reflexivity: CompareVersions(v, v) == 0
	properties.Property("reflexivity: CompareVersions(v, v) == 0", prop.ForAll(
		func(v string) bool {
			return CompareVersions(v, v) == 0
		},
		genVersion(),
	))

	properties.TestingRun(t)
}


// Unit tests for Ebuild edge cases
// _Requirements: 7.1, 7.2, 7.3_

func TestParsePath_ValidPaths(t *testing.T) {
	tests := []struct {
		name     string
		path     string
		expected *Ebuild
	}{
		{
			name: "simple version",
			path: "app-misc/hello/hello-1.0.ebuild",
			expected: &Ebuild{
				Category: "app-misc",
				Package:  "hello",
				Name:     "hello",
				Version:  "1.0",
			},
		},
		{
			name: "version with patch",
			path: "sys-apps/bentoo-utils/bentoo-utils-1.0.1.ebuild",
			expected: &Ebuild{
				Category: "sys-apps",
				Package:  "bentoo-utils",
				Name:     "bentoo-utils",
				Version:  "1.0.1",
			},
		},
		{
			name: "version with rc suffix",
			path: "www-client/firefox/firefox-120.0_rc1.ebuild",
			expected: &Ebuild{
				Category: "www-client",
				Package:  "firefox",
				Name:     "firefox",
				Version:  "120.0_rc1",
			},
		},
		{
			name: "version with revision",
			path: "dev-libs/openssl/openssl-3.0.1-r1.ebuild",
			expected: &Ebuild{
				Category: "dev-libs",
				Package:  "openssl",
				Name:     "openssl",
				Version:  "3.0.1-r1",
			},
		},
		{
			name: "version with beta and revision",
			path: "app-misc/test/test-1.0_beta2-r3.ebuild",
			expected: &Ebuild{
				Category: "app-misc",
				Package:  "test",
				Name:     "test",
				Version:  "1.0_beta2-r3",
			},
		},
		{
			name: "package with hyphen",
			path: "www-client/firefox-bin/firefox-bin-120.0.ebuild",
			expected: &Ebuild{
				Category: "www-client",
				Package:  "firefox-bin",
				Name:     "firefox-bin",
				Version:  "120.0",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := ParsePath(tt.path)
			if err != nil {
				t.Fatalf("ParsePath(%q) returned error: %v", tt.path, err)
			}
			if result.Category != tt.expected.Category {
				t.Errorf("Category = %q, want %q", result.Category, tt.expected.Category)
			}
			if result.Package != tt.expected.Package {
				t.Errorf("Package = %q, want %q", result.Package, tt.expected.Package)
			}
			if result.Name != tt.expected.Name {
				t.Errorf("Name = %q, want %q", result.Name, tt.expected.Name)
			}
			if result.Version != tt.expected.Version {
				t.Errorf("Version = %q, want %q", result.Version, tt.expected.Version)
			}
		})
	}
}

func TestParsePath_InvalidPaths(t *testing.T) {
	tests := []struct {
		name string
		path string
	}{
		{"empty path", ""},
		{"no category", "hello/hello-1.0.ebuild"},
		{"no version", "app-misc/hello/hello.ebuild"},
		{"wrong extension", "app-misc/hello/hello-1.0.txt"},
		{"mismatched name", "app-misc/hello/world-1.0.ebuild"},
		{"missing ebuild suffix", "app-misc/hello/hello-1.0"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := ParsePath(tt.path)
			if err == nil {
				t.Errorf("ParsePath(%q) should return error", tt.path)
			}
		})
	}
}

func TestEbuild_FullName(t *testing.T) {
	e := &Ebuild{Category: "app-misc", Package: "hello", Name: "hello", Version: "1.0"}
	expected := "app-misc/hello"
	if got := e.FullName(); got != expected {
		t.Errorf("FullName() = %q, want %q", got, expected)
	}
}

func TestEbuild_String(t *testing.T) {
	e := &Ebuild{Category: "app-misc", Package: "hello", Name: "hello", Version: "1.0"}
	expected := "app-misc/hello/hello-1.0.ebuild"
	if got := e.String(); got != expected {
		t.Errorf("String() = %q, want %q", got, expected)
	}
}

func TestCompareVersions_EdgeCases(t *testing.T) {
	tests := []struct {
		name     string
		v1       string
		v2       string
		expected int
	}{
		{"equal simple", "1.0", "1.0", 0},
		{"equal with revision", "1.0-r1", "1.0-r1", 0},
		{"major difference", "2.0", "1.0", 1},
		{"minor difference", "1.1", "1.0", 1},
		{"patch difference", "1.0.1", "1.0", 1},
		{"revision difference", "1.0-r2", "1.0-r1", 1},
		{"rc vs release", "1.0_rc1", "1.0", -1},
		{"beta vs rc", "1.0_beta1", "1.0_rc1", -1},
		{"alpha vs beta", "1.0_alpha", "1.0_beta1", -1},
		{"patch suffix", "1.0_p1", "1.0", 1},
		{"rc1 vs rc2", "1.0_rc1", "1.0_rc2", -1},
		{"beta with revision", "1.0_beta2-r1", "1.0_beta2", 1},
		{"different lengths", "1.0.0", "1.0", 0},
		{"complex comparison", "1.0_beta2-r3", "1.0_rc1", -1},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := CompareVersions(tt.v1, tt.v2)
			if result != tt.expected {
				t.Errorf("CompareVersions(%q, %q) = %d, want %d", tt.v1, tt.v2, result, tt.expected)
			}
		})
	}
}
