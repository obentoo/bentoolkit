package registry

import (
	"errors"
	"strings"
	"testing"
)

// The `requires` field of a packages.toml record: which packages the record
// pins, where their version is published, and with which operator. Every case
// below is load-time validation, so a malformed entry is refused before any
// check or apply can act on it.

// requiresFlutterPattern is the motivating pattern: the Dart SDK version that
// sits in the same release object as the detected flutter version.
const requiresFlutterPattern = `"version":\s*"{version}",\s*"dart_sdk_version":\s*"([^"]+)"`

// requiresBaseConfig is a valid record with no requires yet.
func requiresBaseConfig() PackageConfig {
	return PackageConfig{
		URL:    "https://storage.example.org/flutter/releases_linux.json",
		Parser: "json",
		Path:   "current_release.stable",
	}
}

// requiresValidate validates record pkg carrying exactly one requires entry.
func requiresValidate(pkg, atom string, spec RequireSpec) error {
	cfg := requiresBaseConfig()
	cfg.Requires = map[string]RequireSpec{atom: spec}
	return ValidatePackageConfig(nil, pkg, &cfg)
}

// TestRequiresConfigKeyMustBeAPlainAtom: the key is a bare category/package.
// The hostile half comes first — a key decorated with a version, a slot, a
// label or an operator names something narrower than a package and must be
// refused — then the converse, names that merely LOOK decorated (a package
// name with a hyphenated suffix) and must be accepted.
func TestRequiresConfigKeyMustBeAPlainAtom(t *testing.T) {
	spec := RequireSpec{Pattern: requiresFlutterPattern, Pin: "~"}

	refused := []string{
		"dev-lang/dart-3.14.0", // version
		"dev-lang/dart-3",      // short version
		"dev-lang/dart:0",      // slot
		"dev-lang/dart@stable", // label
		"~dev-lang/dart",       // operator
		">=dev-lang/dart",      // operator
		"dart",                 // no category
		"dev-lang/",            // no package
		"/dart",                // no category
		"dev-lang/dart/extra",  // too many parts
		"",                     // empty
	}
	for _, key := range refused {
		t.Run("refused "+key, func(t *testing.T) {
			err := requiresValidate("dev-lang/flutter", key, spec)
			if err == nil {
				t.Fatalf("ValidatePackageConfig accepted requires key %q", key)
			}
			if !strings.Contains(err.Error(), "dev-lang/flutter") {
				t.Errorf("error does not name the record dev-lang/flutter: %v", err)
			}
			if key != "" && !strings.Contains(err.Error(), key) {
				t.Errorf("error does not name the key %q: %v", key, err)
			}
		})
	}

	accepted := []string{
		"dev-lang/dart",
		"dev-lang/dart-sdk",     // hyphenated name, not a version
		"dev-lang/dart-sdk-bin", // two hyphenated parts
		"dev-python/python3",    // digit inside the name
		"x11-libs/gtk+",         // '+' is legal in a package name
	}
	for _, key := range accepted {
		t.Run("accepted "+key, func(t *testing.T) {
			if err := requiresValidate("dev-lang/flutter", key, spec); err != nil {
				t.Fatalf("ValidatePackageConfig refused requires key %q: %v", key, err)
			}
		})
	}
}

// TestRequiresConfigPatternNeedsOneCaptureGroup: the pattern must compile once
// {version} is replaced and capture exactly one value. The hostile cases count
// groups the way a naive parenthesis count would get wrong: a non-capturing
// group and an escaped parenthesis are not captures.
func TestRequiresConfigPatternNeedsOneCaptureGroup(t *testing.T) {
	for _, tc := range []struct {
		name    string
		pattern string
		ok      bool
	}{
		{"two capture groups", `"version": "{version}", "dart": "([^"]+)", "(x)"`, false},
		{"no capture group", `"version": "{version}"`, false},
		{"does not compile", `"version": "{version}", "dart": "([^"]+"`, false},
		{"empty pattern", ``, false},
		{"one group after a non-capturing group", `(?:"version"):\s*"{version}",\s*"d":\s*"([^"]+)"`, true},
		{"escaped parentheses are not groups", `\("{version}"\)\s*dart=([0-9.]+)`, true},
		{"flutter release object", requiresFlutterPattern, true},
		{"no {version} placeholder", `dart_sdk_version":\s*"([^"]+)"`, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := requiresValidate("dev-lang/flutter", "dev-lang/dart", RequireSpec{Pattern: tc.pattern, Pin: "~"})
			if (err == nil) != tc.ok {
				t.Fatalf("ValidatePackageConfig(nil, pattern=%q) = %v, want ok=%v", tc.pattern, err, tc.ok)
			}
			if err != nil {
				for _, want := range []string{"dev-lang/flutter", "dev-lang/dart"} {
					if !strings.Contains(err.Error(), want) {
						t.Errorf("error does not name %s: %v", want, err)
					}
				}
			}
		})
	}
}

// TestRequiresConfigPinOperators: exactly three operators are accepted. The
// refused ones are near-misses of the accepted ones, so a prefix or
// containment check would let them through.
func TestRequiresConfigPinOperators(t *testing.T) {
	for _, pin := range []string{"", ">", "<=", "<", "==", "~=", "~>", "=~", ">=~", " ~", "~ ", "!~"} {
		t.Run("refused "+strings.ReplaceAll(pin, " ", "_"), func(t *testing.T) {
			err := requiresValidate("dev-lang/flutter", "dev-lang/dart", RequireSpec{Pattern: requiresFlutterPattern, Pin: pin})
			if err == nil {
				t.Fatalf("ValidatePackageConfig accepted pin %q", pin)
			}
			for _, op := range []string{`~`, `=`, `>=`} {
				if !strings.Contains(err.Error(), op) {
					t.Errorf("error does not name the accepted operator %q: %v", op, err)
				}
			}
		})
	}
	for _, pin := range []string{"~", "=", ">="} {
		t.Run("accepted "+pin, func(t *testing.T) {
			if err := requiresValidate("dev-lang/flutter", "dev-lang/dart", RequireSpec{Pattern: requiresFlutterPattern, Pin: pin}); err != nil {
				t.Fatalf("ValidatePackageConfig refused pin %q: %v", pin, err)
			}
		})
	}
}

// TestRequiresConfigURL: an optional url is an absolute http(s) URL whose
// {version} may appear only in the path or query, never in the scheme or host.
func TestRequiresConfigURL(t *testing.T) {
	for _, tc := range []struct {
		name string
		url  string
		ok   bool
	}{
		{"placeholder in host", "https://{version}.example.org/deps.txt", false},
		{"placeholder in scheme", "{version}://example.org/deps.txt", false},
		{"not http", "file:///etc/passwd", false},
		{"relative", "/flutter/{version}/deps.txt", false},
		{"no host", "https:///deps.txt", false},
		{"placeholder in path", "https://example.org/flutter/{version}/deps.txt", true},
		{"placeholder in query", "https://example.org/deps?v={version}", true},
		{"no placeholder", "https://example.org/deps.txt", true},
		{"absent", "", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := requiresValidate("dev-lang/flutter", "dev-lang/dart", RequireSpec{Pattern: requiresFlutterPattern, URL: tc.url, Pin: "~"})
			if (err == nil) != tc.ok {
				t.Fatalf("ValidatePackageConfig(nil, url=%q) = %v, want ok=%v", tc.url, err, tc.ok)
			}
			if err != nil && !strings.Contains(err.Error(), "dev-lang/flutter") {
				t.Errorf("error does not name the record: %v", err)
			}
		})
	}
}

// TestRequiresConfigSelfRequirement: a record may not require its own package.
//
// Both directions of the identity rule are pinned. Wrongly split: a record
// keyed with a label or a slot IS still the package, so requiring the bare
// atom is a self-requirement. Wrongly collapsed: a package whose name merely
// starts with the record's name, or the same name in another category, is a
// different package and must be accepted.
func TestRequiresConfigSelfRequirement(t *testing.T) {
	spec := RequireSpec{Pattern: requiresFlutterPattern, Pin: "~"}
	for _, tc := range []struct {
		name   string
		record string
		atom   string
		ok     bool
	}{
		{"labelled record requiring itself", "dev-lang/flutter@stable", "dev-lang/flutter", false},
		{"slotted record requiring itself", "dev-lang/flutter:0", "dev-lang/flutter", false},
		{"plain record requiring itself", "dev-lang/flutter", "dev-lang/flutter", false},
		{"name prefix is another package", "dev-lang/flutter", "dev-lang/flutter-bin", true},
		{"record name is a prefix of the record", "dev-lang/flutter-bin", "dev-lang/flutter", true},
		{"same name in another category", "dev-lang/flutter", "dev-util/flutter", true},
		{"unrelated package", "dev-lang/flutter", "dev-lang/dart", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := requiresValidate(tc.record, tc.atom, spec)
			if (err == nil) != tc.ok {
				t.Fatalf("ValidatePackageConfig(nil, %s requires %s) = %v, want ok=%v", tc.record, tc.atom, err, tc.ok)
			}
			if err != nil && !strings.Contains(err.Error(), tc.record) {
				t.Errorf("error does not name the record %s: %v", tc.record, err)
			}
		})
	}
}

// requiresRegistry is one record whose requires entry carries extra inner
// text (empty for a well-formed entry).
func requiresRegistry(extra string) string {
	return `["dev-lang/flutter"]
url = "https://storage.example.org/flutter/releases_linux.json"
parser = "json"
path = "current_release.stable"
requires = { "dev-lang/dart" = { pattern = '"version":\s*"{version}",\s*"dart_sdk_version":\s*"([^"]+)"', ` + extra + `pin = "~" } }
`
}

// TestRequiresConfigLoadsFromTOML: a well-formed inline-table entry decodes
// into the record and passes validation, field for field.
func TestRequiresConfigLoadsFromTOML(t *testing.T) {
	cfg, err := decodePackagesConfig([]byte(requiresRegistry(`url = "https://example.org/{version}/deps.txt", `)))
	if err != nil {
		t.Fatalf("decodePackagesConfig: %v", err)
	}
	if err := cfg.ValidateAll(nil); err != nil {
		t.Fatalf("ValidateAll: %v", err)
	}
	got, ok := cfg.Packages["dev-lang/flutter"].Requires["dev-lang/dart"]
	if !ok {
		t.Fatalf("requires entry lost on load: %+v", cfg.Packages["dev-lang/flutter"].Requires)
	}
	want := RequireSpec{Pattern: requiresFlutterPattern, URL: "https://example.org/{version}/deps.txt", Pin: "~"}
	if got != want {
		t.Errorf("decoded entry = %+v, want %+v", got, want)
	}
}

// TestRequiresConfigUnknownInnerKey: a typo inside an entry fails the load
// naming the unknown key, exactly as a typo at the record level does. A
// misspelt url would otherwise silently fall back to the record's own page.
func TestRequiresConfigUnknownInnerKey(t *testing.T) {
	for _, typo := range []string{`form = "x", `, `pins = "~", `, `uri = "https://example.org/x", `} {
		key, _, _ := strings.Cut(typo, " =")
		t.Run(key, func(t *testing.T) {
			_, err := decodePackagesConfig([]byte(requiresRegistry(typo)))
			if err == nil {
				t.Fatalf("decodePackagesConfig accepted the unknown inner key %q", key)
			}
			var unknown *UnknownKeysError
			if !errors.As(err, &unknown) {
				t.Fatalf("err = %v (%T), want *UnknownKeysError", err, err)
			}
			if !strings.Contains(err.Error(), key) {
				t.Errorf("error does not name the unknown key %q: %v", key, err)
			}
			if !strings.Contains(err.Error(), "dev-lang/flutter") {
				t.Errorf("error does not name the record: %v", err)
			}
		})
	}
}
