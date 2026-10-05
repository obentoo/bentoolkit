package autoupdate

import (
	"reflect"
	"regexp"
	"strings"
	"testing"
)

// requiresRenderKeyRegex captures the bare key of a rendered TOML assignment.
var requiresRenderKeyRegex = regexp.MustCompile(`(?m)^([a-z0-9_]+)\s*=`)

// requiresRenderRecord is a record whose requires entries are shaped to break a
// careless writer: a pattern needing a basic string (it holds a '), one
// needing a literal string (quotes and backslashes), braces, commas and '='
// inside values, and two atoms where one name is a prefix of the other, so a
// writer that merged or truncated keys would collapse two entries into one.
func requiresRenderRecord() *PackageConfig {
	return &PackageConfig{
		URL:        "https://storage.example.org/flutter/releases_linux.json",
		Parser:     "json",
		Path:       "current_release.stable",
		AuxVar:     "MY_BUILD",
		AuxPattern: `build ([0-9]+)`,
		AuxURL:     "https://example.org/{version}/build.txt",
		Revision:   1,
		Requires: map[string]RequireSpec{
			"dev-lang/dart": {
				Pattern: `"version":\s*"{version}",\s*"dart_sdk_version":\s*"([^"]+)"`,
				Pin:     "~",
			},
			"dev-lang/dart-sdk": {
				Pattern: `it's {version} = { sdk: ([0-9.]+) }, done`,
				URL:     "https://example.org/sdk/{version}/deps.txt?a=1,b=2",
				Pin:     ">=",
			},
			"dev-util/dart": {
				Pattern: `\bdart=([^\s,}]+)`,
				Pin:     "=",
			},
		},
		Comments: "flutter — pins the Dart SDK its release bundles.",
	}
}

// TestRequiresRenderRoundTrip: what RenderRecord writes decodes back to the
// same requires map, entry for entry. A writer that drops the field (the
// silent ok=false path for unhandled map kinds) or mangles one value fails
// here, which is what keeps `--lint --fix` and `overlay analyze --save` from
// erasing the field.
func TestRequiresRenderRoundTrip(t *testing.T) {
	cfg := requiresRenderRecord()
	rendered := RenderRecord("dev-lang/flutter", cfg)

	decoded, err := decodePackagesConfig([]byte(rendered))
	if err != nil {
		t.Fatalf("the rendered record does not decode: %v\n--- rendered ---\n%s", err, rendered)
	}
	got := decoded.Packages["dev-lang/flutter"].Requires
	if !reflect.DeepEqual(got, cfg.Requires) {
		t.Errorf("requires after a save round trip:\n got %#v\nwant %#v\n--- rendered ---\n%s", got, cfg.Requires, rendered)
	}
	if err := decoded.ValidateAll(nil); err != nil {
		t.Errorf("the rendered record no longer validates: %v", err)
	}
}

// TestRequiresRenderSingleInlineLine: requires is written as ONE line holding
// an inline table per entry. A sub-table header such as
// ["dev-lang/flutter".requires] is read by the record scanner as a new record.
func TestRequiresRenderSingleInlineLine(t *testing.T) {
	cfg := requiresRenderRecord()
	rendered := RenderRecord("dev-lang/flutter", cfg)

	var requiresLines []string
	for _, line := range strings.Split(rendered, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "[") && strings.Contains(trimmed, "requires") {
			t.Errorf("requires rendered as a sub-table header: %q\n--- rendered ---\n%s", line, rendered)
		}
		if strings.HasPrefix(line, "requires") {
			requiresLines = append(requiresLines, line)
		}
	}
	if len(requiresLines) != 1 {
		t.Fatalf("rendered %d requires line(s), want exactly 1\n--- rendered ---\n%s", len(requiresLines), rendered)
	}
	line := requiresLines[0]
	for atom := range cfg.Requires {
		if !strings.Contains(line, `"`+atom+`" = {`) {
			t.Errorf("the requires line lacks the inline entry for %s: %s", atom, line)
		}
	}
	// An absent url is omitted, never written empty.
	if strings.Contains(line, `url = ""`) {
		t.Errorf("an empty url was written: %s", line)
	}
	// Fixed inner order: pattern, url, pin.
	sdkAt := strings.Index(line, `"dev-lang/dart-sdk" =`)
	if sdkAt < 0 {
		t.Fatalf("the requires line lacks dev-lang/dart-sdk: %s", line)
	}
	sdk := line[sdkAt:]
	p, u, n := strings.Index(sdk, "pattern ="), strings.Index(sdk, "url ="), strings.Index(sdk, "pin =")
	if p < 0 || u < 0 || n < 0 || p >= u || u >= n {
		t.Errorf("inner keys of dev-lang/dart-sdk not in the order pattern, url, pin: %s", sdk)
	}
	// Outer keys sorted, so two saves of the same record are byte-identical.
	a, b, c := strings.Index(line, `"dev-lang/dart" =`), strings.Index(line, `"dev-lang/dart-sdk" =`), strings.Index(line, `"dev-util/dart" =`)
	if a < 0 || b < 0 || c < 0 || a >= b || b >= c {
		t.Errorf("outer requires keys not sorted: %s", line)
	}
	if again := RenderRecord("dev-lang/flutter", requiresRenderRecord()); again != rendered {
		t.Errorf("two renders of the same record differ:\n%s\n---\n%s", rendered, again)
	}
}

// TestRequiresRenderPlacesRequiresAfterAuxURL: RenderRecord emits requires
// directly between aux_url and the next field.
func TestRequiresRenderPlacesRequiresAfterAuxURL(t *testing.T) {
	rendered := RenderRecord("dev-lang/flutter", requiresRenderRecord())
	var keys []string
	for _, m := range requiresRenderKeyRegex.FindAllStringSubmatch(rendered, -1) {
		keys = append(keys, m[1])
	}
	auxURL, requires := -1, -1
	for i, k := range keys {
		switch k {
		case "aux_url":
			auxURL = i
		case "requires":
			requires = i
		}
	}
	if requires < 0 {
		t.Fatalf("RenderRecord emitted no requires line; keys %v\n--- rendered ---\n%s", keys, rendered)
	}
	if auxURL < 0 || requires != auxURL+1 {
		t.Errorf("emitted key order %v; want requires immediately after aux_url", keys)
	}
}

// TestRequiresLintOrderAfterAuxURL: the field order rule expects requires
// immediately after aux_url — the order slice says so, the linter accepts a
// record that writes it there, and reports one that writes it before aux_url.
func TestRequiresLintOrderAfterAuxURL(t *testing.T) {
	t.Run("order slice", func(t *testing.T) {
		auxURL, requires, count := -1, -1, 0
		for i, f := range CanonicalFieldOrder {
			switch f {
			case "aux_url":
				auxURL = i
			case "requires":
				requires = i
				count++
			}
		}
		if count != 1 {
			t.Fatalf("CanonicalFieldOrder lists requires %d time(s), want 1", count)
		}
		if requires != auxURL+1 {
			t.Errorf("requires at index %d, aux_url at %d; want requires immediately after aux_url", requires, auxURL)
		}
	})

	record := func(body string) string {
		return `["dev-lang/flutter"]
url = "https://storage.example.org/flutter/releases_linux.json"
parser = "json"
path = "current_release.stable"
` + body + `comments = """
flutter — pins the Dart SDK its release bundles.
"""
# END
`
	}
	const auxLines = "aux_var = \"MY_BUILD\"\naux_pattern = 'build ([0-9]+)'\naux_url = \"https://example.org/{version}/build.txt\"\n"
	const requiresLine = "requires = { \"dev-lang/dart\" = { pattern = '\"version\":\\s*\"{version}\",\\s*\"dart_sdk_version\":\\s*\"([^\"]+)\"', pin = \"~\" } }\n"

	t.Run("accepted after aux_url", func(t *testing.T) {
		issues, err := LintPackagesConfig(nil, writeRegistry(t, record(auxLines+requiresLine)))
		if err != nil {
			t.Fatalf("LintPackagesConfig: %v; requires must be a key the registry claims", err)
		}
		for _, iss := range issues {
			if iss.Rule == LintFieldOrder || iss.Rule == LintUnknownField {
				t.Errorf("unexpected issue on a canonically ordered record: %s", iss)
			}
		}
	})

	t.Run("reported before aux_url", func(t *testing.T) {
		issues, err := LintPackagesConfig(nil, writeRegistry(t, record(requiresLine+auxLines)))
		if err != nil {
			t.Fatalf("LintPackagesConfig: %v", err)
		}
		found := false
		for _, iss := range issues {
			if iss.Rule == LintUnknownField {
				t.Errorf("requires reported as unknown: %s", iss)
			}
			if iss.Rule == LintFieldOrder {
				found = true
			}
		}
		if !found {
			t.Errorf("a record writing requires before aux_url raised no field-order issue: %v", issues)
		}
	})
}
