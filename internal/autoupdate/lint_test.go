package autoupdate

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// cleanRecord is one record in the shape the model prescribes: fields, then the
// doc field, then the end marker.
const cleanRecord = `["app-office/libreoffice"]
url = "https://downloadarchive.documentfoundation.org/libreoffice/old/"
parser = "regex"
pattern = 'href="([0-9.]+)/"'
select = "max"
suffix = "_pre"
suffix_when = '^26\.8\.'
comments = """
libreoffice — old/ lists the stable 26.2 line and the testing 26.8 one, so
select=max always returns the latter; suffix_when marks it _pre.
"""
# END
`

// rules returns the rule identifiers of the reported issues, in report order.
func rules(issues []LintIssue) []string {
	out := make([]string, 0, len(issues))
	for _, i := range issues {
		out = append(out, i.Rule)
	}
	return out
}

// hasRule reports whether the issue list contains the given rule.
func hasRule(issues []LintIssue, rule string) bool {
	for _, i := range issues {
		if i.Rule == rule {
			return true
		}
	}
	return false
}

func TestLintRecordModelClean(t *testing.T) {
	if issues := lintRecordModel(cleanRecord); len(issues) != 0 {
		t.Fatalf("clean record reported %v", rules(issues))
	}
}

// TestLintRecordModelFileHeader pins the one comment block that is not a stray
// comment: the header that opens packages.toml. It documents the record model
// for whoever edits the file by hand, belongs to no single record, and the real
// registry carries ~112 lines of it — flagging them would read as an order to
// delete the documentation.
func TestLintRecordModelFileHeader(t *testing.T) {
	const header = `# Bentoo Autoupdate Package Configuration
# Every record obeys the field order below.
#
# NOTE: BEGIN/END markers are comments only.

# >>>>>>>>>>  BEGIN PACKAGES  <<<<<<<<<<

` + cleanRecord

	if issues := lintRecordModel(header); len(issues) != 0 {
		t.Fatalf("file header reported %v", rules(issues))
	}
}

// TestLintRecordModelStrayAfterLastRecord keeps the exemption anchored to the
// top of the file: a comment block that trails the final record is stranded
// documentation, not a header, and still has to be reported.
func TestLintRecordModelStrayAfterLastRecord(t *testing.T) {
	trailing := cleanRecord + `
# npm registry — a section banner left behind by an old layout.
`
	issues := lintRecordModel(trailing)
	if !hasRule(issues, LintStrayComment) {
		t.Fatalf("trailing comment not reported, got %v", rules(issues))
	}
}

func TestLintRecordModelViolations(t *testing.T) {
	tests := []struct {
		name    string
		content string
		want    string
	}{
		{
			name: "record without the end marker",
			content: `["dev-util/x"]
url = "https://example.com"
parser = "json"
path = "version"
comments = """
x — doc.
"""
`,
			want: LintMissingEnd,
		},
		{
			name: "record without a comments field",
			content: `["dev-util/x"]
url = "https://example.com"
parser = "json"
path = "version"
# END
`,
			want: LintMissingComments,
		},
		{
			name: "field assigned after comments",
			content: `["dev-util/x"]
url = "https://example.com"
comments = """
x — doc.
"""
parser = "json"
# END
`,
			want: LintCommentsNotLast,
		},
		{
			name: "comment floating between records",
			content: `["dev-util/x"]
url = "https://example.com"
comments = """
x — doc.
"""
# END

# ============ npm registry ============

["dev-util/y"]
url = "https://example.com"
comments = """
y — doc.
"""
# END
`,
			want: LintStrayComment,
		},
		{
			name: "doc left as a comment inside the record",
			content: `["dev-util/x"]
url = "https://example.com"
# x — this belongs in the comments field.
comments = """
x — doc.
"""
# END
`,
			want: LintInlineComment,
		},
		{
			name: "comments line that looks like a section header",
			content: `["dev-util/x"]
url = "https://example.com"
comments = """
x — the JSON path is
[0].version
which the raw-text editors would read as a header.
"""
# END
`,
			want: LintBracketInComment,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			issues := lintRecordModel(tt.content)
			if !hasRule(issues, tt.want) {
				t.Fatalf("got %v, want a %s issue", rules(issues), tt.want)
			}
		})
	}
}

// TestLintRecordModelIgnoresDocContent pins that the scanner tracks the doc
// string: a "#" line or an "# END" written inside the documentation is prose,
// not file structure.
func TestLintRecordModelIgnoresDocContent(t *testing.T) {
	content := `["dev-util/x"]
url = "https://example.com"
comments = """
x — the registry marks a record's end with:
# END
and a shell comment starts with # too.
"""
# END
`
	if issues := lintRecordModel(content); len(issues) != 0 {
		t.Fatalf("doc content treated as structure: %v", rules(issues))
	}
}

// TestLintRecordModelSingleLineComments accepts the one-line form of the doc
// field: the model asks for """…""" but a short single-line string is still a
// field, which is what the rule is actually about.
func TestLintRecordModelSingleLineComments(t *testing.T) {
	content := `["dev-util/x"]
url = "https://example.com"
comments = "x — npm dist-tags.latest."
# END
`
	if issues := lintRecordModel(content); len(issues) != 0 {
		t.Fatalf("single-line comments field rejected: %v", rules(issues))
	}
}

func TestLintPackagesConfig(t *testing.T) {
	t.Run("clean registry", func(t *testing.T) {
		dir := writeRegistry(t, cleanRecord)
		issues, err := LintPackagesConfig(dir)
		if err != nil {
			t.Fatalf("lint failed: %v", err)
		}
		if len(issues) != 0 {
			t.Fatalf("clean registry reported %v", rules(issues))
		}
	})

	t.Run("semantic error is reported alongside the layout ones", func(t *testing.T) {
		// suffix_when with no suffix: valid TOML, valid layout, invalid record.
		dir := writeRegistry(t, `["dev-util/x"]
url = "https://example.com"
parser = "json"
path = "version"
suffix_when = '^1\.'
comments = "x — doc."
# END
`)
		issues, err := LintPackagesConfig(dir)
		if err != nil {
			t.Fatalf("lint failed: %v", err)
		}
		if !hasRule(issues, LintInvalidConfig) {
			t.Fatalf("got %v, want an %s issue", rules(issues), LintInvalidConfig)
		}
	})

	t.Run("missing registry", func(t *testing.T) {
		if _, err := LintPackagesConfig(t.TempDir()); !errors.Is(err, ErrPackagesConfigNotFound) {
			t.Fatalf("got %v, want ErrPackagesConfigNotFound", err)
		}
	})

	t.Run("an unknown key is reported as an issue, not only as a load error", func(t *testing.T) {
		dir := writeRegistry(t, `["dev-util/x"]
url = "https://example.com"
parser = "json"
path = "version"
serie = '^1\.'
comments = "x — doc."
# END
`)
		issues, err := LintPackagesConfig(dir)
		// The error stays: the config could not be built, so the semantic checks
		// below it never ran and the caller must not read a short list as clean.
		if err == nil {
			t.Fatal("an unknown key did not fail the load")
		}
		var found *LintIssue
		for i := range issues {
			if issues[i].Rule == LintUnknownField {
				found = &issues[i]
			}
		}
		if found == nil {
			t.Fatalf("got %v, want a %s issue", rules(issues), LintUnknownField)
		}
		if found.Package != "dev-util/x" {
			t.Errorf("issue names record %q, want dev-util/x", found.Package)
		}
		if !strings.Contains(found.Message, "serie") {
			t.Errorf("issue message omits the key: %q", found.Message)
		}
	})

	t.Run("a retired key is not an unknown field", func(t *testing.T) {
		// `binary` must reach the linter as a lintable record, not as a dead file:
		// --lint --fix is the only migration path for the 23 records carrying it.
		dir := writeRegistry(t, `["net-misc/postman-bin"]
url = "https://example.com"
parser = "json"
path = "version"
binary = true
comments = "postman-bin — doc."
# END
`)
		issues, err := LintPackagesConfig(dir)
		if err != nil {
			t.Fatalf("a retired key broke the lint: %v", err)
		}
		if hasRule(issues, LintUnknownField) {
			t.Fatalf("retired key reported as unknown: %v", rules(issues))
		}
	})

	t.Run("unparseable registry still reports layout issues", func(t *testing.T) {
		dir := writeRegistry(t, `["dev-util/x"]
url = "https://example.com
comments = "x — doc."
`)
		issues, err := LintPackagesConfig(dir)
		if err == nil {
			t.Fatal("broken TOML accepted")
		}
		if !hasRule(issues, LintMissingEnd) {
			t.Fatalf("got %v, want the layout issues to survive the parse error", rules(issues))
		}
	})
}

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

// TestSavePackagesConfigRecordModel pins that a rewritten registry still obeys
// the model — the rewrite path is exactly what used to erase the registry's
// documentation, so it must now round-trip the doc field AND close each record.
func TestSavePackagesConfigRecordModel(t *testing.T) {
	dir := t.TempDir()
	a := &Analyzer{
		overlayPath: dir,
		config: &PackagesConfig{Packages: map[string]PackageConfig{
			"app-office/libreoffice": {
				URL:        "https://downloadarchive.documentfoundation.org/libreoffice/old/",
				Parser:     "regex",
				Pattern:    `href="([0-9.]+)/"`,
				Select:     "max",
				Suffix:     "_pre",
				SuffixWhen: `^26\.8\.`,
				Comments:   "libreoffice — old/ mixes the stable and testing lines.\nsuffix_when marks the testing one _pre.",
			},
			"dev-util/claude-code": {
				URL:      "https://registry.npmjs.org/@anthropic-ai/claude-code",
				Parser:   "json",
				Path:     "dist-tags.latest",
				Comments: "claude-code — npm dist-tags.latest is the stable channel.",
			},
		}},
	}

	if err := a.savePackagesConfig(); err != nil {
		t.Fatalf("save failed: %v", err)
	}

	raw, err := os.ReadFile(filepath.Join(dir, ".autoupdate", "packages.toml"))
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	written := string(raw)

	if n := strings.Count(written, "\n"+recordEndMarker+"\n"); n != 2 {
		t.Fatalf("want one %q per record, got %d in:\n%s", recordEndMarker, n, written)
	}
	// The doc must survive as text, not as one escaped line.
	if !strings.Contains(written, "suffix_when marks the testing one _pre.\n") {
		t.Fatalf("doc field lost its line breaks:\n%s", written)
	}
	if issues := lintRecordModel(written); len(issues) != 0 {
		t.Fatalf("rewritten registry violates the model %v:\n%s", rules(issues), written)
	}

	// And it must load back with every field intact.
	loaded, err := LoadPackagesConfig(dir)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	lo := loaded.Packages["app-office/libreoffice"]
	if lo.Suffix != "_pre" || lo.SuffixWhen != `^26\.8\.` {
		t.Fatalf("suffix fields lost: %+v", lo)
	}
	if !strings.Contains(lo.Comments, "old/ mixes the stable and testing lines.") {
		t.Fatalf("doc field lost: %q", lo.Comments)
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
