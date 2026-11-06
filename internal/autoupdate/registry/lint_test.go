package registry

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// writeRegistry creates an overlay whose .autoupdate/packages.toml holds content
// and returns the overlay path.
func writeRegistry(t *testing.T, content string) string {
	t.Helper()
	dir := t.TempDir()
	autoDir := filepath.Join(dir, ".autoupdate")
	if err := os.MkdirAll(autoDir, 0o750); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(autoDir, "packages.toml"), []byte(content), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	return dir
}

// A note on why this stops at lintRecordModel rather than running the whole of
// LintPackagesConfig: a record setting all 38 fields is necessarily invalid
// SEMANTICALLY, because several of them are mutually exclusive by design — this
// one pairs suffix with track = "commit", which ValidatePackageConfig rejects
// because a snapshot suffix comes from the current ebuild. That is a real rule
// working correctly. The scan above covers every rule this sub-task can break
// (layout, field order, the legacy fields); semantic validity belongs to a
// fixture that is semantically coherent, and TestSavePackagesConfigRecordModel
// above is one.

// TestRenderRecordNeverEmitsRedundantEnabled pins the one value a writer must
// swallow. An absent `enabled` already means enabled, so `enabled = true` is the
// redundancy the linter reports — a writer emitting it would generate the finding
// the record was just checked against. `enabled = false` must survive: it is the
// only way to say disabled.
func TestRenderRecordNeverEmitsRedundantEnabled(t *testing.T) {
	base := PackageConfig{
		URL: "https://e.com", Parser: "json", Path: "v",
		Comments: "x — doc.\n",
	}

	on, off := true, false

	withOn := base
	withOn.Enabled = &on
	if got := RenderRecord("dev-util/x", &withOn); strings.Contains(got, "enabled") {
		t.Errorf("enabled = true was emitted:\n%s", got)
	}

	withOff := base
	withOff.Enabled = &off
	if got := RenderRecord("dev-util/x", &withOff); !strings.Contains(got, "enabled = false") {
		t.Errorf("enabled = false was dropped:\n%s", got)
	}
}

// TestFormatCommentsFieldEscaping covers the two ways a doc string could break
// the file it is written into.
func TestFormatCommentsFieldEscaping(t *testing.T) {
	t.Run("a triple quote cannot close the string early", func(t *testing.T) {
		got := formatCommentsField(`x — upstream writes """ in its changelog.`)
		if strings.Count(got, `"""`) != 2 {
			t.Fatalf("unescaped triple quote in:\n%s", got)
		}
	})

	t.Run("a bracket line is indented out of header shape", func(t *testing.T) {
		got := formatCommentsField("x — the path is\n[0].version\nfor this API.")
		if !strings.Contains(got, "\n [0].version\n") {
			t.Fatalf("bracket line not indented:\n%s", got)
		}
	})

	t.Run("a backslash survives the round trip", func(t *testing.T) {
		got := formatCommentsField(`x — the pattern is '\d+'.`)
		if !strings.Contains(got, `'\\d+'`) {
			t.Fatalf("backslash not escaped:\n%s", got)
		}
	})
}

// TestLintIssueString covers the two shapes a reported issue takes: one anchored
// to a record and line, and one that belongs to no record.
func TestLintIssueString(t *testing.T) {
	withPkg := LintIssue{Line: 42, Package: "app-office/libreoffice", Rule: LintMissingEnd, Message: "not closed"}
	want := `packages.toml:42: [app-office/libreoffice] missing-end: not closed`
	if got := withPkg.String(); got != want {
		t.Fatalf("got %q, want %q", got, want)
	}

	stray := LintIssue{Line: 3, Rule: LintStrayComment, Message: "floating"}
	want = `packages.toml:3: stray-comment: floating`
	if got := stray.String(); got != want {
		t.Fatalf("got %q, want %q", got, want)
	}

	noLine := LintIssue{Package: "dev-util/x", Rule: LintInvalidConfig, Message: "bad suffix"}
	want = `packages.toml: [dev-util/x] invalid-config: bad suffix`
	if got := noLine.String(); got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

// TestCanonicalFieldOrderCoversPackageConfig is the drift guard the
// CanonicalFieldOrder doc comment points at. The order rule ranks a field by
// looking it up in that slice and SKIPS what it cannot rank, so a field added to
// PackageConfig and forgotten here would not be reported as misplaced — it would
// stop being ordered at all, silently. Reflection over the toml tags is what
// makes forgetting impossible.
func TestCanonicalFieldOrderCoversPackageConfig(t *testing.T) {
	rt := reflect.TypeOf(PackageConfig{})

	tagged := make(map[string]bool, rt.NumField())
	for i := 0; i < rt.NumField(); i++ {
		f := rt.Field(i)
		tag := f.Tag.Get("toml")
		if tag == "" || tag == "-" {
			t.Fatalf("PackageConfig.%s has no toml tag; it can never be ordered", f.Name)
		}
		name, _, _ := strings.Cut(tag, ",")
		tagged[name] = true
	}

	seen := make(map[string]int, len(CanonicalFieldOrder))
	for _, field := range CanonicalFieldOrder {
		seen[field]++
		if !tagged[field] {
			t.Errorf("CanonicalFieldOrder lists %q, which no PackageConfig field claims", field)
		}
	}
	for field := range tagged {
		if seen[field] != 1 {
			t.Errorf("field %q appears %d time(s) in CanonicalFieldOrder, want exactly 1", field, seen[field])
		}
	}
	if len(CanonicalFieldOrder) != len(tagged) {
		t.Errorf("CanonicalFieldOrder has %d entries, PackageConfig has %d toml tags",
			len(CanonicalFieldOrder), len(tagged))
	}
	// Explicit, because its absence is a decision rather than an oversight: the
	// classifier is `type`, and `binary` only survives as a retired key the
	// repair migrates away (R1.1).
	if seen["binary"] != 0 {
		t.Error("CanonicalFieldOrder lists the retired key binary; type is the classifier")
	}
}

// TestLintMinAgeFieldPair pins that min_age_days and published_path only work
// as a pair. Either alone is a gate that cannot decide: an age with no place to
// read the publication time from, or a place to read it with no age to compare
// against. The lint must name the half that is missing, because the half that
// is present is the one the maintainer already typed.
func TestLintMinAgeFieldPair(t *testing.T) {
	const pkg = "dev-util/x"
	record := func(body string) string {
		return `["` + pkg + `"]
url = "https://example.com/api"
parser = "json"
path = "dist-tags.latest"
` + body + `comments = """
x — a test record.
"""
# END
`
	}
	tests := []struct {
		name    string
		body    string
		missing string // "" means the record must lint clean
	}{
		{"min_age_days alone", "min_age_days = 7\n", "published_path"},
		{"published_path alone", "published_path = \"time.{version}\"\n", "min_age_days"},
		{"both", "min_age_days = 7\npublished_path = \"time.{version}\"\n", ""},
		{"neither", "", ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			issues, err := LintPackagesConfig(nil, writeRegistry(t, record(tc.body)))
			if err != nil {
				t.Fatalf("LintPackagesConfig: %v", err)
			}
			if tc.missing == "" {
				if len(issues) != 0 {
					t.Fatalf("record reported: %v", issues)
				}
				return
			}
			var found bool
			for _, iss := range issues {
				if iss.Package == pkg && iss.Rule == LintInvalidConfig && strings.Contains(iss.Message, tc.missing) {
					found = true
				}
			}
			if !found {
				t.Fatalf("no %s issue naming %q; got %v", LintInvalidConfig, tc.missing, issues)
			}
		})
	}
}

// TestLintMinAgeFieldOrder pins where the pair sits: right after select, the
// last field that decides WHICH upstream version is the candidate, because the
// age gate decides whether that candidate may be taken.
func TestLintMinAgeFieldOrder(t *testing.T) {
	selectRank, ok := canonicalFieldRank["select"]
	if !ok {
		t.Fatal("select is not ranked")
	}
	if got := canonicalFieldRank["min_age_days"]; got != selectRank+1 {
		t.Errorf("min_age_days rank = %d, want %d (right after select)", got, selectRank+1)
	}
	if got := canonicalFieldRank["published_path"]; got != selectRank+2 {
		t.Errorf("published_path rank = %d, want %d (right after min_age_days)", got, selectRank+2)
	}

	outOfOrder := `["dev-util/x"]
url = "https://example.com/api"
parser = "json"
path = "dist-tags.latest"
published_path = "time.{version}"
min_age_days = 7
comments = """
x — a test record.
"""
# END
`
	var flagged bool
	for _, iss := range LintRecordModel(outOfOrder) {
		if iss.Rule == LintFieldOrder && iss.Package == "dev-util/x" {
			flagged = true
		}
	}
	if !flagged {
		t.Error("published_path before min_age_days was not flagged as out of order")
	}
}

// TestValidateMinAgeDaysNegative pins that a negative age is refused, naming
// the record: it can only be a typo, and read literally it would make every
// candidate old enough — the gate silently off.
func TestValidateMinAgeDaysNegative(t *testing.T) {
	cfg := PackageConfig{
		URL: "https://example.com/api", Parser: "json", Path: "dist-tags.latest",
		MinAgeDays: -1, PublishedPath: "time.{version}",
	}
	err := ValidatePackageConfig(nil, "dev-util/x", &cfg)
	if err == nil {
		t.Fatal("min_age_days = -1 was accepted")
	}
	if !strings.Contains(err.Error(), "dev-util/x") || !strings.Contains(err.Error(), "min_age_days") {
		t.Errorf("error does not name the record and the field: %v", err)
	}
}

// TestValidatePublishedPathNeedsJSON pins that the gate is refused on a record
// whose response is not a JSON document: published_path could never be
// navigated there, so every candidate would be held forever with no error.
func TestValidatePublishedPathNeedsJSON(t *testing.T) {
	cfg := PackageConfig{
		URL: "https://example.com/", Parser: "regex", Pattern: `v(\d+)`,
		MinAgeDays: 7, PublishedPath: "time.{version}",
	}
	err := ValidatePackageConfig(nil, "dev-util/x", &cfg)
	if err == nil || !strings.Contains(err.Error(), "published_path") {
		t.Fatalf("published_path on a regex record: got %v, want an error naming published_path", err)
	}
}
