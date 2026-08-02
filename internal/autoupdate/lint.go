package autoupdate

import (
	"errors"
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
	LintMissingSeries    = "missing-series"
	LintUnknownField     = "unknown-field"
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
// fields are semantically valid. The comment block that opens the file, before
// the first record, is the file header and is not a violation.
//
// It reports issues instead of failing on the first one, because the point is to
// hand back the whole list of what needs fixing. A nil slice means the registry
// is clean. An error is returned only when the file cannot be read or parsed at
// all — a TOML syntax error leaves nothing to lint. An unknown key is both: it
// fails the load AND is reported as an issue, so the maintainer reads the same
// per-record line the other rules produce instead of only a fatal error.
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
		// An unknown key is a lint finding in its own right, not merely a reason
		// the file would not load: reported per record it reads like every other
		// rule and says which of 411 entries to open (R4.1). It stays a returned
		// error too, because the semantic checks below need a config that could
		// not be built. No repair is offered — see UnknownKeysError (R4.2).
		var unknown *UnknownKeysError
		if errors.As(err, &unknown) {
			for _, k := range unknown.Keys {
				issues = append(issues, LintIssue{
					Package: k.Package,
					Rule:    LintUnknownField,
					Message: fmt.Sprintf("unknown key %q: no field claims it; check the spelling — an unknown key is reported, never repaired", k.Key),
				})
			}
		}
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

	// Overlay-aware check: an entry covering a directory that holds more than one
	// release line, without saying which line it tracks.
	issues = append(issues, lintUntrackedReleaseLines(overlayPath, cfg.Packages)...)

	return issues, nil
}

// releaseLineOf reduces a version to the release line it belongs to: the first
// two numeric components, so 1.28.4, 1.28.5 and 1.28.5-r1 all collapse to
// "1.28" while 1.29.2 and 1.29.2_pre collapse to "1.29". A single-component
// version (llama-cpp's "0_pre10202") yields that component alone, which keeps
// every build-numbered snapshot of such a package in one line.
func releaseLineOf(version string) string {
	m := releaseLineRegex.FindStringSubmatch(version)
	if m == nil {
		return ""
	}
	if m[2] != "" {
		return m[1] + "." + m[2]
	}
	return m[1]
}

// releaseLineRegex captures the first two DOT-separated numeric components of a
// version. Anchoring at the start and requiring the literal "." matters: a bare
// \d+ scan would read llama-cpp's "0_pre10202" as components 0 and 10202 and
// call it line "0.10202", making every build-numbered snapshot look like a
// release line of its own.
var releaseLineRegex = regexp.MustCompile(`^(\d+)(?:\.(\d+))?`)

// prereleaseSuffixRegex matches the Gentoo suffixes that mark a version as
// coming BEFORE its base: _alpha, _beta, _pre, _rc, each optionally numbered.
// _p is deliberately absent — it marks a post-release snapshot, which orders
// after the base and says nothing about a line being unstable.
var prereleaseSuffixRegex = regexp.MustCompile(`_(alpha|beta|pre|rc)\d*`)

// lintUntrackedReleaseLines reports entries whose package directory carries more
// than one release line while the entry declares neither `series` nor a `:slot`.
//
// It exists because that combination fails silently and looks like success.
// selectCurrentEbuild takes the directory's HIGHEST version as "the current
// one", so once a newer line lands beside an older one, every release of the
// older line compares older than the current version and the entry reports
// "up to date" forever — the line simply stops being maintained. The overlay hit
// exactly this: 85 GStreamer packages carried a 1.29.x development ebuild beside
// nothing else, and the 1.28.5 stable release could never be picked up because
// 1.28.5 < 1.29.2.
//
// Two lines in one directory are perfectly legitimate (a stable line beside a
// testing one); what is not legitimate is leaving it undeclared. The fix is one
// entry per line, each with its own `series` and a distinct "@label".
//
// Deliberately conservative to stay useful, on two counts. Revisions and
// snapshot suffixes of the SAME line (1.28.4 next to 1.28.5-r1) are not
// reported, because keeping the previous version around is ordinary overlay
// hygiene. Neither are two lines that merely SUCCEED each other: bentoolkit
// itself ships 0.15.3 beside 0.16.0, which is one line mid-rotation, not two
// maintained in parallel — warning there would bury the real finding under a
// warning on almost every directory.
//
// What distinguishes the real case is the pre-release suffix. A line kept in
// parallel on purpose is the unstable one and says so: libreoffice-26.2.5.2
// beside libreoffice-26.8.0.1_pre, zed-bin-1.13.1 beside zed-bin-1.14.1_pre.
// So the rule fires only when one line carries _alpha/_beta/_pre/_rc and
// another does not — the shape of a stable line coexisting with a testing one.
// Note _p is excluded on purpose: it marks a post-release snapshot, so two _p
// lines are two snapshot lines rather than a stable/unstable pair.
//
// An unreadable directory yields no issue — the linter must not invent findings
// from a failed stat.
func lintUntrackedReleaseLines(overlayPath string, pkgs map[string]PackageConfig) []LintIssue {
	var issues []LintIssue

	for _, pkg := range sortedKeys(pkgs) {
		cfg := pkgs[pkg]
		// A disabled entry tracks nothing, and a slot- or series-qualified one has
		// already said which line it means.
		if cfg.Enabled != nil && !*cfg.Enabled {
			continue
		}
		if cfg.Series != "" {
			continue
		}
		if _, slot := splitPkgSlot(pkg); slot != "" {
			continue
		}

		dir := pkgDirFor(overlayPath, pkg)
		if dir == "" {
			continue
		}
		paths, err := findEbuilds(dir)
		if err != nil || len(paths) < 2 {
			continue
		}

		lines := make(map[string]string, 2) // release line → one example version
		for _, p := range paths {
			v := extractVersionFromFilename(filepath.Base(p))
			if v == "" {
				continue
			}
			if line := releaseLineOf(v); line != "" {
				lines[line] = v
			}
		}
		if len(lines) < 2 {
			continue
		}

		// Only a stable/unstable pair counts: one line marked as a pre-release
		// and another that is not. Successive versions of one line carry no such
		// marker and are left alone.
		var withPre, withoutPre bool
		examples := make([]string, 0, len(lines))
		for _, v := range lines {
			if prereleaseSuffixRegex.MatchString(v) {
				withPre = true
			} else {
				withoutPre = true
			}
			examples = append(examples, v)
		}
		if !withPre || !withoutPre {
			continue
		}
		sort.Strings(examples)

		issues = append(issues, LintIssue{
			Package: pkg,
			Rule:    LintMissingSeries,
			Message: fmt.Sprintf(
				"directory holds a stable and a pre-release line (%s) but the entry declares no series; "+
					"only the highest is tracked, so the other reports \"up to date\" forever",
				strings.Join(examples, ", ")),
		})
	}

	return issues
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
//
// The comment block that opens the file is exempt: see seenRecord below.
func lintRecordModel(content string) []LintIssue {
	var issues []LintIssue
	var cur *recordLintState
	inComments := false
	// seenRecord turns the stray-comment rule on. Everything before the first
	// record header is the file header — the text that documents the record
	// model itself (field order, enabled vs hold, the traps a new record has to
	// avoid). It describes every record, so it can live inside none of them, and
	// flagging it would push maintainers to delete the one thing that makes the
	// file editable by hand. Once a record has been seen, a floating comment is
	// documentation stranded between records, which is what the rule is for.
	seenRecord := false

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
			seenRecord = true
			continue
		}

		if trimmed == "" {
			continue
		}

		// A comment line. Before the first record it is the file header, which
		// the model allows. Inside an open record it is either the end marker or
		// a leftover doc line that belongs in comments; between records it is the
		// floating comment the record model forbids.
		if strings.HasPrefix(trimmed, "#") {
			switch {
			case !seenRecord:
				// File header — allowed, see seenRecord.
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
