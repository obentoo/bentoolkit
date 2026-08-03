package autoupdate

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
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

// issuesFor returns every issue reported under the given rule, in report order.
func issuesFor(issues []LintIssue, rule string) []LintIssue {
	out := make([]LintIssue, 0, len(issues))
	for _, i := range issues {
		if i.Rule == rule {
			out = append(out, i)
		}
	}
	return out
}

// onlyIssueFor returns the single issue reported under the given rule, failing
// when the count is anything but one.
func onlyIssueFor(t *testing.T, issues []LintIssue, rule string) LintIssue {
	t.Helper()
	got := issuesFor(issues, rule)
	if len(got) != 1 {
		t.Fatalf("want exactly one %s issue, got %d in %v", rule, len(got), rules(issues))
	}
	return got[0]
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

// TestSavePackagesConfigEveryField is the round trip the fixture above cannot
// give: a record that sets ALL 37 fields, saved and read back, compared whole.
//
// The two-record fixture above passes even against a broken writer, because it
// declares no map field and no field whose canonical rank inverts against the
// struct's declaration order. Both defects the shared renderer was written to
// fix are invisible to it — a populated `headers` came out as the sub-table
// ["pkg".headers], which the scanner reads as a new record, and `timeout` came
// out before `select` because that is how the struct declares them. A fixture
// that omits the fields where a writer goes wrong proves only that it did not go
// wrong somewhere else.
//
// Round-trip equality is the real test of a writer: whatever it emits must parse
// back to the value it was handed, for every type the struct holds — string,
// bool, *bool, int, map, and the [][]string of transform. Quoting is where that
// breaks, so the fixture deliberately carries a pattern containing `"` and a
// meta value containing `'`: neither TOML string form can hold both, so the
// renderer has to choose per value rather than by rule.
func TestSavePackagesConfigEveryField(t *testing.T) {
	dir := t.TempDir()
	disabled := false
	saved := PackageConfig{
		Enabled:              &disabled,
		Hold:                 true,
		Track:                "commit",
		URL:                  "https://api.example.com/commits?per_page=50",
		Parser:               "json",
		Path:                 "0.commit.committer.date",
		Pattern:              `href="([0-9.]+)/"`,
		Selector:             "a.release",
		XPath:                "//a",
		Script:               "@vendor.js",
		Transform:            [][]string{{`^v`, ""}, {"-", "."}},
		Select:               "max",
		Suffix:               "_pre",
		SuffixWhen:           `^26\.8\.`,
		CommitSHAPath:        "0.sha",
		CommitMessagePath:    "commit.message",
		CommitVersionPattern: `v([0-9.]+)`,
		BaseFrom:             "commit_message",
		BaseURL:              "https://raw.example.com/VERSION",
		BasePattern:          `(?m)^version = "([0-9.]+)"`,
		BaseTagPattern:       `vulkan-sdk-([0-9.]+)`,
		Headers:              map[string]string{"User-Agent": "bentoo-autoupdate", "Accept": "application/json"},
		Timeout:              60,
		Meta:                 map[string]string{"fetch_url": "https://example.com/dl", "note": "it's fine"},
		Type:                 "bin",
		Series:               `^1\.`,
		AuxVar:               "MY_BUILD",
		AuxPattern:           `_([0-9]+)_amd64`,
		Revision:             411,
		Version:              "1.2.3-r411",
		FallbackURL:          "https://example.org/releases",
		FallbackParser:       "regex",
		FallbackPattern:      `([0-9.]+)`,
		LLMPrompt:            "extract the version",
		VersionsPath:         "versions",
		VersionsSelector:     ".version",
		Comments:             "x — every field the record model knows.\n",
	}

	a := &Analyzer{overlayPath: dir, config: &PackagesConfig{
		Packages: map[string]PackageConfig{"dev-util/x": saved},
	}}
	if err := a.savePackagesConfig(); err != nil {
		t.Fatalf("save: %v", err)
	}

	raw, err := os.ReadFile(filepath.Join(dir, ".autoupdate", "packages.toml"))
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	written := string(raw)

	// A map written as a sub-table would show up here as a second header, and
	// the record it belongs to would be reported unclosed and undocumented.
	if issues := lintRecordModel(written); len(issues) != 0 {
		t.Fatalf("saved record violates the model %v:\n%s", rules(issues), written)
	}

	loaded, err := LoadPackagesConfig(dir)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if got := loaded.Packages["dev-util/x"]; !reflect.DeepEqual(saved, got) {
		t.Errorf("round trip differs:\nsaved = %+v\ngot   = %+v", saved, got)
	}
}

// A note on why this stops at lintRecordModel rather than running the whole of
// LintPackagesConfig: a record setting all 37 fields is necessarily invalid
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

// canonicalRecord declares a wide slice of the field set in the canonical order.
// It is the positive control for every rule added here: nothing about it is a
// violation, so any issue it produces is a rule firing where it must not.
const canonicalRecord = `["dev-util/x"]
enabled = false
hold = true
track = "commit"
url = "https://api.example.com/commits?per_page=50"
parser = "json"
path = "0.commit.committer.date"
transform = [["-", "."]]
select = "max"
commit_sha_path = "0.sha"
commit_message_path = "commit.message"
commit_version_pattern = 'v([0-9.]+)'
base_from = "commit_message"
headers = { Accept = "application/json" }
timeout = 60
meta = { note = "annotation nothing reads" }
type = "bin"
series = '^1\.'
aux_var = "MY_BUILD"
aux_pattern = '_([0-9]+)_amd64'
revision = 411
version = "1.2.3"
fallback_url = "https://example.org/releases"
fallback_parser = "regex"
fallback_pattern = '([0-9.]+)'
llm_prompt = "extract the version"
versions_path = "versions"
versions_selector = ".version"
comments = """
x — every field the record model knows, in the order the model prescribes.
"""
# END
`

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

// TestLintFieldSetRules covers the four rules one fixture at a time, pinning the
// rule identifier, the line it points at and the repair it declares.
func TestLintFieldSetRules(t *testing.T) {
	tests := []struct {
		name    string
		content string
		rule    string
		line    int
		fix     string
	}{
		{
			// R1.2: nothing else classifies the package, so the line is rewritten.
			name: "binary in a record without type",
			content: `["net-misc/postman-bin"]
url = "https://registry.example.com/postman"
parser = "json"
path = "version"
binary = true
comments = "postman-bin — doc."
# END
`,
			rule: LintLegacyBinary, line: 5, fix: FixBinaryToType,
		},
		{
			// R1.3: type already says it, so the line just goes.
			name: "binary in a record that already declares type",
			content: `["net-ftp/filezilla-pro"]
url = "https://filezilla-project.org/"
parser = "json"
path = "version"
binary = true
type = "bin"
comments = "filezilla-pro — doc."
# END
`,
			rule: LintLegacyBinary, line: 5, fix: FixDropBinary,
		},
		{
			// binary = false is the default spelled out; it migrates to nothing.
			name: "binary = false is still the retired key",
			content: `["dev-util/x"]
url = "https://example.com"
parser = "json"
path = "version"
binary = false
comments = "x — doc."
# END
`,
			rule: LintLegacyBinary, line: 5, fix: FixDropBinary,
		},
		{
			name: "enabled = true is redundant",
			content: `["net-misc/rclone"]
enabled = true
url = "https://example.com"
parser = "json"
path = "version"
comments = "rclone — doc."
# END
`,
			rule: LintRedundantEnabled, line: 2, fix: FixDropEnabled,
		},
		{
			// The measured deviation: headers sitting inside the base_* block.
			name: "field order deviates",
			content: `["dev-util/vulkan-headers"]
track = "commit"
url = "https://api.example.com/commits?per_page=50"
parser = "json"
path = "0.commit.committer.date"
commit_sha_path = "0.sha"
headers = { Accept = "application/vnd.github+json" }
base_from = "tag"
base_url = "https://api.example.com/tags"
base_tag_pattern = 'vulkan-sdk-([0-9.]+)'
comments = "vulkan-headers — doc."
# END
`,
			rule: LintFieldOrder, line: 8, fix: FixReorderFields,
		},
		{
			// R6.1: reported at the track line, and deliberately not repaired.
			name: "commit tracking with no declared base source",
			content: `["sys-apps/asus-ec-sensors"]
track = "commit"
url = "https://api.example.com/commits?per_page=50"
parser = "json"
path = "0.commit.committer.date"
commit_sha_path = "0.sha"
comments = "asus-ec-sensors — doc."
# END
`,
			rule: LintLegacyBase, line: 2, fix: FixNone,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			issues := lintRecordModel(tt.content)
			got := onlyIssueFor(t, issues, tt.rule)
			if got.Line != tt.line {
				t.Errorf("issue points at line %d, want %d (%s)", got.Line, tt.line, got.String())
			}
			if got.Fix != tt.fix {
				t.Errorf("issue declares fix %q, want %q", got.Fix, tt.fix)
			}
			if got.Message == "" {
				t.Error("issue has no message")
			}
		})
	}
}

// TestLintFieldSetRulesStayQuiet is the other half: the shapes that look like a
// violation and are not. Each one cost a real record its correctness at some
// point, so a rule that fired here would be worse than no rule.
func TestLintFieldSetRulesStayQuiet(t *testing.T) {
	tests := []struct {
		name    string
		content string
		absent  string
	}{
		{
			// enabled = false is the bookkeeping that parks an orphaned entry. It
			// carries information an absent key does not, so it is never redundant.
			name: "enabled = false",
			content: `["dev-util/x"]
enabled = false
url = "https://example.com"
parser = "json"
path = "version"
comments = "x — orphaned, kept for revival."
# END
`,
			absent: LintRedundantEnabled,
		},
		{
			// The repaired Khronos shape: base_* grouped, headers after them.
			name: "commit tracking with base_from declared",
			content: `["dev-util/glslang"]
track = "commit"
url = "https://api.example.com/commits?per_page=50"
parser = "json"
path = "0.commit.committer.date"
commit_sha_path = "0.sha"
base_from = "tag"
base_url = "https://api.example.com/tags"
base_tag_pattern = 'vulkan-sdk-([0-9.]+)'
headers = { Accept = "application/vnd.github+json" }
comments = "glslang — doc."
# END
`,
			absent: LintLegacyBase,
		},
		{
			name:    "a record already in canonical order",
			content: canonicalRecord,
			absent:  LintFieldOrder,
		},
		{
			// A record declares a handful of the 37 fields, never all of them. The
			// rule is subsequence, not contiguity — requiring the latter would
			// report all 411 records.
			name: "a sparse record skipping most of the order",
			content: `["dev-util/claude-code"]
url = "https://registry.npmjs.org/@anthropic-ai/claude-code"
parser = "json"
path = "dist-tags.latest"
comments = "claude-code — npm dist-tags.latest is the stable channel."
# END
`,
			absent: LintFieldOrder,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			issues := lintRecordModel(tt.content)
			if hasRule(issues, tt.absent) {
				t.Fatalf("%s fired where it must not: %v", tt.absent, issues)
			}
		})
	}

	t.Run("the canonical record is clean on every rule", func(t *testing.T) {
		if issues := lintRecordModel(canonicalRecord); len(issues) != 0 {
			t.Fatalf("canonical record reported %v", issues)
		}
	})
}

// TestLintLegacyBinaryDistinguishesTheRepairs pins the distinction sub-task 5.1
// consumes: which of the two repairs applies is carried in Fix, so the rewriter
// switches on an identifier instead of parsing English out of Message.
func TestLintLegacyBinaryDistinguishesTheRepairs(t *testing.T) {
	content := `["net-misc/postman-bin"]
url = "https://registry.example.com/postman"
parser = "json"
path = "version"
binary = true
comments = "postman-bin — doc."
# END

["net-ftp/filezilla-pro"]
url = "https://filezilla-project.org/"
parser = "json"
path = "version"
binary = true
type = "bin"
comments = "filezilla-pro — doc."
# END
`

	got := issuesFor(lintRecordModel(content), LintLegacyBinary)
	if len(got) != 2 {
		t.Fatalf("want two %s issues, got %d: %v", LintLegacyBinary, len(got), got)
	}

	byPkg := map[string]LintIssue{}
	for _, i := range got {
		byPkg[i.Package] = i
	}
	if fix := byPkg["net-misc/postman-bin"].Fix; fix != FixBinaryToType {
		t.Errorf("record without type declares fix %q, want %q", fix, FixBinaryToType)
	}
	if fix := byPkg["net-ftp/filezilla-pro"].Fix; fix != FixDropBinary {
		t.Errorf("record with type declares fix %q, want %q", fix, FixDropBinary)
	}
	// One rule, two repairs: the identifier is shared on purpose so a maintainer
	// filtering "legacy-binary" sees all 23 records, and only Fix separates them.
	if byPkg["net-misc/postman-bin"].Rule != byPkg["net-ftp/filezilla-pro"].Rule {
		t.Error("the two cases were split into two rules; the distinction belongs in Fix")
	}
}

// TestLintFieldOrderFollowsTheRepairedShape covers the interaction that makes
// lint and --fix agree. The order rule ranks a record as it will be AFTER the
// retired keys are resolved, so a `binary` that becomes `type` is ranked as
// `type` (and can therefore land out of order), while a `binary` that is merely
// deleted cannot manufacture a deviation. Without this, --fix would reorder
// records the lint never reported.
func TestLintFieldOrderFollowsTheRepairedShape(t *testing.T) {
	// The real net-misc/nxplayer shape: aux_* then binary. type ranks before
	// aux_var, so the migration puts it two ranks too late.
	migrates := `["net-misc/nxplayer"]
url = "https://download.example.com/?id=43"
parser = "regex"
pattern = 'personal-edition_([0-9.]+)_[0-9]+_amd64\.deb'
aux_var = "MY_BUILD"
aux_pattern = 'personal-edition_[0-9.]+_([0-9]+)_amd64\.deb'
binary = true
comments = "nxplayer — doc."
# END
`
	issues := lintRecordModel(migrates)
	order := onlyIssueFor(t, issues, LintFieldOrder)
	if order.Line != 7 {
		t.Errorf("order issue points at line %d, want 7 (the binary line)", order.Line)
	}
	// The line the maintainer opens says `binary`, so the message must say which
	// field the rank belongs to and that it is not written there yet.
	if !strings.Contains(order.Message, `"type"`) || !strings.Contains(order.Message, `"binary"`) {
		t.Errorf("message names neither the ranked field nor the written key: %q", order.Message)
	}
	if !hasRule(issues, LintLegacyBinary) {
		t.Errorf("the binary line itself went unreported: %v", rules(issues))
	}

	// Same position, but the line is deleted rather than migrated: no deviation.
	deletes := strings.Replace(migrates, "binary = true", "binary = false", 1)
	issues = lintRecordModel(deletes)
	if hasRule(issues, LintFieldOrder) {
		t.Errorf("a deleted line manufactured an order deviation: %v", issues)
	}
	if fix := onlyIssueFor(t, issues, LintLegacyBinary).Fix; fix != FixDropBinary {
		t.Errorf("binary = false declares fix %q, want %q", fix, FixDropBinary)
	}
}

// TestLintFieldOrderReportsTheFirstOffender pins R3.2's "first offending field":
// a record with several backwards steps yields one issue, at the earliest one.
// Reporting every step would bury the record under its own noise.
func TestLintFieldOrderReportsTheFirstOffender(t *testing.T) {
	content := `["dev-util/x"]
series = '^1\.'
url = "https://example.com"
parser = "json"
timeout = 30
path = "version"
comments = "x — doc."
# END
`
	got := onlyIssueFor(t, lintRecordModel(content), LintFieldOrder)
	if got.Line != 3 {
		t.Errorf("order issue points at line %d, want 3 (url, the first field out of order)", got.Line)
	}
	if !strings.Contains(got.Message, `"url"`) || !strings.Contains(got.Message, `"series"`) {
		t.Errorf("message does not name the offending field and the one it must precede: %q", got.Message)
	}
}

// TestLintPreExistingRulesUnchanged is the UB3 pin. It runs the fixtures the
// pre-existing rules were written against and asserts the EXACT set of rules
// each one still produces — not merely that the old rule is somewhere in the
// list. Four rules were added around these bodies without editing any of them,
// and this is what says so.
func TestLintPreExistingRulesUnchanged(t *testing.T) {
	tests := []struct {
		name    string
		content string
		want    []string
	}{
		{name: "clean record", content: cleanRecord, want: nil},
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
			want: []string{LintMissingEnd},
		},
		{
			name: "record without a comments field",
			content: `["dev-util/x"]
url = "https://example.com"
parser = "json"
path = "version"
# END
`,
			want: []string{LintMissingComments},
		},
		{
			// The one fixture that gains a second finding, and correctly so: a
			// field after comments is out of canonical order by definition, since
			// the order pins comments last. Both statements are true and the same
			// reordering settles both.
			name: "field assigned after comments",
			content: `["dev-util/x"]
url = "https://example.com"
comments = """
x — doc.
"""
parser = "json"
# END
`,
			want: []string{LintCommentsNotLast, LintFieldOrder},
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
			want: []string{LintStrayComment},
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
			want: []string{LintInlineComment},
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
			want: []string{LintBracketInComment},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			issues := lintRecordModel(tt.content)
			got := rules(issues)
			if len(got) != len(tt.want) {
				t.Fatalf("got %v, want exactly %v", got, tt.want)
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Fatalf("got %v, want exactly %v", got, tt.want)
				}
			}
			// The pre-existing rules describe a layout a human has to fix; none of
			// them claims an automatic repair.
			for _, issue := range issues {
				if issue.Rule == LintFieldOrder {
					continue
				}
				if issue.Fix != FixNone {
					t.Errorf("pre-existing rule %s now declares fix %q", issue.Rule, issue.Fix)
				}
			}
		})
	}
}
