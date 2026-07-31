package autoupdate

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// recordEndMarker closes every record in packages.toml. TOML has no block
// delimiter, and a bare [END] table would not be one: it would parse as a
// package named "END" — and, repeated once per record, as a duplicate-table
// error that stops the whole file from loading. A comment on the record's last
// line is the closest valid equivalent, and unlike a floating comment it belongs
// to the record it terminates.
const recordEndMarker = "# END"

// tripleQuoteRegex matches a run of three or more double quotes, the only quote
// sequence that can close a multi-line basic string early.
var tripleQuoteRegex = regexp.MustCompile(`"{3,}`)

// keyAssignRegex matches the start of a TOML key/value assignment line and
// captures the bare (unquoted) key.
var keyAssignRegex = regexp.MustCompile(`^\s*([A-Za-z0-9_-]+)\s*=`)

// commentsOpenRegex matches the line that opens the doc field as a multi-line
// basic string (comments = """), the form the record model requires.
var commentsOpenRegex = regexp.MustCompile(`^\s*comments\s*=\s*"""`)

// Lint rule identifiers. They are stable strings so a caller can filter or
// suppress a single rule without matching on message text.
const (
	LintMissingEnd       = "missing-end"
	LintMissingComments  = "missing-comments"
	LintCommentsNotLast  = "comments-not-last"
	LintStrayComment     = "stray-comment"
	LintInlineComment    = "inline-comment"
	LintBracketInComment = "bracket-line-in-comments"
	LintInvalidConfig    = "invalid-config"
	LintAmbiguousEntries = "ambiguous-entries"
)

// LintIssue is one violation of the packages.toml record model.
type LintIssue struct {
	// Line is the 1-indexed line the issue points at, or 0 when it is not tied
	// to a specific line (a semantic error from ValidatePackageConfig).
	Line int
	// Package is the record the issue belongs to, empty when the issue sits
	// outside every record.
	Package string
	// Rule is one of the Lint* identifiers above.
	Rule string
	// Message states the violation in one line.
	Message string
}

// String renders an issue the way a linter conventionally prints one.
func (i LintIssue) String() string {
	loc := "packages.toml"
	if i.Line > 0 {
		loc = fmt.Sprintf("packages.toml:%d", i.Line)
	}
	if i.Package != "" {
		return fmt.Sprintf("%s: [%s] %s: %s", loc, i.Package, i.Rule, i.Message)
	}
	return fmt.Sprintf("%s: %s: %s", loc, i.Rule, i.Message)
}

// LintPackagesConfig checks the overlay's packages.toml against the record
// model: every record ends with the `# END` marker, its documentation lives in a
// trailing comments field rather than in floating `#` lines, and each record's
// fields are semantically valid.
//
// It reports issues instead of failing on the first one, because the point is to
// hand back the whole list of what needs fixing. A nil slice means the registry
// is clean. An error is returned only when the file cannot be read or parsed at
// all — a TOML syntax error leaves nothing to lint.
func LintPackagesConfig(overlayPath string) ([]LintIssue, error) {
	configPath := filepath.Join(overlayPath, ".autoupdate", "packages.toml")
	data, err := os.ReadFile(configPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, ErrPackagesConfigNotFound
		}
		return nil, fmt.Errorf("failed to read packages.toml: %w", err)
	}

	issues := lintRecordModel(string(data))

	// Semantic validation needs the parsed config; a parse failure is fatal
	// because the structural issues above were found by text scan alone and say
	// nothing about whether the file loads.
	cfg, err := LoadPackagesConfig(overlayPath)
	if err != nil {
		return issues, err
	}
	for _, pkg := range sortedKeys(cfg.Packages) {
		c := cfg.Packages[pkg]
		if verr := ValidatePackageConfig(pkg, &c); verr != nil {
			issues = append(issues, LintIssue{
				Package: pkg,
				Rule:    LintInvalidConfig,
				Message: verr.Error(),
			})
		}
	}

	// Cross-entry check: two entries that scan the same ebuilds would race each
	// other over one ebuild, which reads as a checker bug rather than the config
	// mistake it is.
	if derr := validateDistinctEntries(cfg.Packages); derr != nil {
		issues = append(issues, LintIssue{
			Rule:    LintAmbiguousEntries,
			Message: derr.Error(),
		})
	}

	return issues, nil
}

// recordLintState tracks the record currently being scanned by lintRecordModel.
type recordLintState struct {
	name          string
	headerLine    int
	closed        bool // the `# END` marker has been seen
	hasComments   bool
	commentsLine  int
	fieldsAfterCm int // fields assigned after comments — the doc must be last
}

// lintRecordModel scans the raw file text for record-model violations.
//
// It works on text rather than on the parsed config because every rule here is
// about layout the TOML parser discards: where a comment sits, whether the doc
// field comes last, whether the record is closed. It tracks the multi-line
// string opened by `comments = """` so that a `#` line or a `[`-prefixed line
// inside the documentation is not mistaken for file structure.
func lintRecordModel(content string) []LintIssue {
	var issues []LintIssue
	var cur *recordLintState
	inComments := false

	closeRecord := func() {
		if cur == nil {
			return
		}
		if !cur.closed {
			issues = append(issues, LintIssue{
				Line: cur.headerLine, Package: cur.name, Rule: LintMissingEnd,
				Message: fmt.Sprintf("record is not closed by a %q line", recordEndMarker),
			})
		}
		if !cur.hasComments {
			issues = append(issues, LintIssue{
				Line: cur.headerLine, Package: cur.name, Rule: LintMissingComments,
				Message: "record has no comments field documenting why this source and parser",
			})
		} else if cur.fieldsAfterCm > 0 {
			issues = append(issues, LintIssue{
				Line: cur.commentsLine, Package: cur.name, Rule: LintCommentsNotLast,
				Message: fmt.Sprintf("comments must be the last field, but %d field(s) follow it", cur.fieldsAfterCm),
			})
		}
		cur = nil
	}

	for idx, line := range strings.Split(content, "\n") {
		lineNo := idx + 1
		trimmed := strings.TrimSpace(line)

		// Inside the doc string: only look for its terminator, and flag a line
		// that the raw-text section scanner elsewhere would read as a header.
		if inComments {
			if strings.Contains(line, `"""`) {
				inComments = false
				continue
			}
			if strings.HasPrefix(trimmed, "[") && cur != nil {
				issues = append(issues, LintIssue{
					Line: lineNo, Package: cur.name, Rule: LintBracketInComment,
					Message: `a comments line starting with "[" is read as a section header by the raw-text editors; indent it`,
				})
			}
			continue
		}

		if name, isHeader := tomlTableName(line); isHeader {
			closeRecord()
			cur = &recordLintState{name: name, headerLine: lineNo}
			continue
		}

		if trimmed == "" {
			continue
		}

		// A comment line. Inside an open record it is either the end marker or a
		// leftover doc line that belongs in comments; outside one it is the
		// floating comment the record model forbids.
		if strings.HasPrefix(trimmed, "#") {
			switch {
			case cur == nil || cur.closed:
				issues = append(issues, LintIssue{
					Line: lineNo, Rule: LintStrayComment,
					Message: "comment outside any record; move it into the comments field of the record it describes",
				})
			case trimmed == recordEndMarker:
				cur.closed = true
			default:
				issues = append(issues, LintIssue{
					Line: lineNo, Package: cur.name, Rule: LintInlineComment,
					Message: "comment inside a record; the documentation belongs in the comments field",
				})
			}
			continue
		}

		if cur == nil {
			continue
		}

		m := keyAssignRegex.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		if m[1] == "comments" {
			cur.hasComments = true
			cur.commentsLine = lineNo
			// A multi-line string stays open unless it also closes on this line.
			if commentsOpenRegex.MatchString(line) {
				rest := line[strings.Index(line, `"""`)+3:]
				inComments = !strings.Contains(rest, `"""`)
			}
			continue
		}
		if cur.hasComments {
			cur.fieldsAfterCm++
		}
	}
	closeRecord()

	return issues
}

// sortedKeys returns the map's keys in ascending order, so lint output is
// deterministic across runs.
func sortedKeys(m map[string]PackageConfig) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
