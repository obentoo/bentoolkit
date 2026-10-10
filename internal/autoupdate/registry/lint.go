package registry

import (
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/obentoo/bentoolkit/internal/autoupdate/ebuilds"
)

// RecordEndMarker closes every record in packages.toml. TOML has no block
// delimiter, and a bare [END] table would not be one: it would parse as a
// package named "END" — and, repeated once per record, as a duplicate-table
// error that stops the whole file from loading. A comment on the record's last
// line is the closest valid equivalent, and unlike a floating comment it belongs
// to the record it terminates.
const RecordEndMarker = "# END"

// CanonicalFieldOrder is the sequence in which a packages.toml record assigns
// its fields — the single source the linter (LintFieldOrder), the repair and
// `overlay analyze` all use, so generator and linter agree by construction.
//
// IT IS DERIVED FROM MEASUREMENT, NOT FROM TASTE: it was read off the registry's
// 411 hand-written records, with the four `base_*` siblings grouped and
// `comments` pinned last (see PackageConfig.Comments and LintCommentsNotLast).
// That costs 13 records a reordering; a prettier order would have cost 178. Do
// not "tidy" it: every edit is a churn bill payable in records, so change it only
// with a fresh measurement in hand.
//
// `patched` sits right after `type` because both classify the PACKAGE rather
// than describe how to probe it, and `disabled_by` right after `enabled` because
// it names WHO wrote the `enabled = false` beside it. Both were placed while no
// record carried them, so their positions cost nothing now and churn later.
//
// It covers every `toml:` tag of PackageConfig exactly once, as
// TestCanonicalFieldOrderCoversPackageConfig pins; `binary` (no struct field, the
// classifier is `type`) is absent. Read-only: canonicalFieldRank is built from it.
var CanonicalFieldOrder = []string{
	"enabled", "disabled_by", "hold", "track",
	"url", "mirrors", "parser", "path", "pattern", "selector", "xpath", "script",
	"transform", "select", "suffix", "suffix_when",
	"commit_sha_path", "commit_message_path", "commit_version_pattern",
	"base_from", "base_url", "base_pattern", "base_tag_pattern",
	"headers", "timeout", "meta", "type", "patched", "series",
	"aux_var", "aux_pattern", "aux_url", "requires", "revision", "version",
	"fallback_url", "fallback_parser", "fallback_pattern", "llm_prompt",
	"versions_path", "versions_selector",
	"comments",
}

// canonicalFieldRank maps each canonical field to its position in
// CanonicalFieldOrder, so the order rule is a rank comparison rather than a
// linear search per field.
var canonicalFieldRank = func() map[string]int {
	m := make(map[string]int, len(CanonicalFieldOrder))
	for i, f := range CanonicalFieldOrder {
		m[f] = i
	}
	return m
}()

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
	LintLegacyBinary     = "legacy-binary"
	LintRedundantEnabled = "redundant-enabled"
	LintFieldOrder       = "field-order"
	LintLegacyBase       = "legacy-base"
)

// Repair actions. A rule says WHAT is wrong; the action says what `--lint --fix`
// would do about it, as a stable identifier the repair pass switches on.
//
// The pair exists because one rule can carry two repairs: a record declaring the
// retired `binary` is one finding (LintLegacyBinary), but the fix is a rewrite
// when nothing else classifies the package and a plain deletion when `type` is
// already there. Encoding that in the message text would force
// the repair to string-match prose, so it is encoded here instead.
const (
	// FixNone marks an issue that is reported and never repaired. It is the
	// zero value, so every rule that offers no repair says so by default.
	FixNone = ""
	// FixBinaryToType rewrites `binary = true` as `type = "bin"`.
	FixBinaryToType = "binary-to-type"
	// FixDropBinary deletes the `binary` line, leaving `type` untouched.
	FixDropBinary = "drop-binary"
	// FixDropEnabled deletes a redundant `enabled = true` line. It is
	// never produced for `enabled = false`, which carries real information.
	FixDropEnabled = "drop-enabled"
	// FixReorderFields sorts the record's assignments into CanonicalFieldOrder.
	FixReorderFields = "reorder-fields"
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
	// Fix is the repair `--lint --fix` would apply, one of the Fix* identifiers
	// above. FixNone (the zero value) means the issue is reported only — either
	// because the rule deliberately declines to guess (legacy-base cannot know
	// where upstream versions itself; unknown-field would write a value into a
	// field nobody meant) or because the violation needs a human
	// edit. Read this rather than the message when deciding what to do.
	Fix string
}

// String renders an issue the way a linter conventionally prints one.
func (i LintIssue) String() string {
	loc := "packages.toml"
	if i.Line > 0 {
		loc = fmt.Sprintf("packages.toml:%d", i.Line)
	}
	if i.Package != "" {
		return fmt.Sprintf("%s: [%s] %s: %s", loc, displayPackageKey(i.Package), i.Rule, i.Message)
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
func LintPackagesConfig(log *slog.Logger, overlayPath string) ([]LintIssue, error) {
	configPath := filepath.Join(overlayPath, ".autoupdate", "packages.toml")
	data, err := os.ReadFile(configPath) //nolint:gosec // G304: configPath is <overlay>/.autoupdate/packages.toml, a constant join on the overlay path the user configured
	if err != nil {
		if os.IsNotExist(err) {
			return nil, ErrPackagesConfigNotFound
		}
		return nil, fmt.Errorf("failed to read packages.toml: %w", err)
	}

	issues := LintRecordModel(string(data))

	// Semantic validation needs the parsed config; a parse failure is fatal
	// because the structural issues above were found by text scan alone and say
	// nothing about whether the file loads.
	cfg, err := LoadPackagesConfig(overlayPath)
	if err != nil {
		// An unknown key is a lint finding in its own right, not merely a reason
		// the file would not load: reported per record it reads like every other
		// rule and says which of 411 entries to open. It stays a returned
		// error too, because the semantic checks below need a config that could
		// not be built. No repair is offered — see UnknownKeysError.
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
		if verr := ValidatePackageConfig(log, pkg, &c); verr != nil {
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
// That combination fails silently and looks like success: selectCurrentEbuild
// takes the HIGHEST version as current, so every release of an older line
// compares older and the entry reports "up to date" forever. The overlay hit
// exactly this: 85 GStreamer packages carried a 1.29.x development ebuild, and
// the 1.28.5 stable release could never be picked up. Two lines in one directory
// are legitimate; leaving them undeclared is not. The fix is one entry per line,
// each with its own `series` and a distinct "@label".
//
// It stays conservative to stay useful: revisions and snapshots of the SAME line
// (1.28.4 beside 1.28.5-r1) and lines that merely SUCCEED each other (0.15.3
// beside 0.16.0, mid-rotation) are not reported. It fires only when one line
// carries _alpha/_beta/_pre/_rc and another does not — a stable line beside a
// testing one, like zed-bin-1.13.1 beside zed-bin-1.14.1_pre. _p is excluded: it
// marks a post-release snapshot. An unreadable directory yields no issue — the
// linter must not invent findings from a failed stat.
func lintUntrackedReleaseLines(overlayPath string, pkgs map[string]PackageConfig) []LintIssue {
	var issues []LintIssue

	for _, pkg := range sortedKeys(pkgs) {
		if declaresReleaseLine(pkg, pkgs[pkg]) {
			continue
		}

		lines := releaseLinesOnDisk(overlayPath, pkg)
		if len(lines) < 2 {
			continue
		}

		examples, mixed := mixedStabilityExamples(lines)
		if !mixed {
			continue
		}

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

// declaresReleaseLine reports whether lintUntrackedReleaseLines must skip an
// entry. A disabled entry tracks nothing, and a slot- or series-qualified one
// has already said which line it means.
func declaresReleaseLine(pkg string, cfg PackageConfig) bool {
	if cfg.Enabled != nil && !*cfg.Enabled {
		return true
	}
	if cfg.Series != "" {
		return true
	}
	_, slot := ebuilds.SplitPkgSlot(pkg)
	return slot != ""
}

// releaseLinesOnDisk maps each release line found among the package's ebuilds
// to one example version. It returns nil when the directory cannot be resolved
// or read, or holds fewer than two ebuilds.
func releaseLinesOnDisk(overlayPath, pkg string) map[string]string {
	dir := ebuilds.PkgDirFor(overlayPath, pkg)
	if dir == "" {
		return nil
	}
	paths, err := ebuilds.FindEbuilds(dir)
	if err != nil || len(paths) < 2 {
		return nil
	}

	lines := make(map[string]string, 2) // release line → one example version
	for _, p := range paths {
		v := ebuilds.ExtractVersionFromFilename(filepath.Base(p))
		if v == "" {
			continue
		}
		if line := releaseLineOf(v); line != "" {
			lines[line] = v
		}
	}
	return lines
}

// mixedStabilityExamples returns the sorted example versions of lines, and
// whether they form a stable/unstable pair.
//
// Only a stable/unstable pair counts: one line marked as a pre-release and
// another that is not. Successive versions of one line carry no such marker
// and are left alone.
func mixedStabilityExamples(lines map[string]string) ([]string, bool) {
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
		return nil, false
	}
	sort.Strings(examples)
	return examples, true
}

// recordField is one key/value assignment of a record, as written.
type recordField struct {
	key   string // the bare key, left of "="
	value string // the raw text right of "=", trimmed; "[" for a multi-line array
	line  int    // 1-indexed line of the assignment
}

// recordLintState tracks the record currently being scanned by lintRecordModel.
type recordLintState struct {
	name          string
	headerLine    int
	closed        bool // the `# END` marker has been seen
	hasComments   bool
	commentsLine  int
	fieldsAfterCm int           // fields assigned after comments — the doc must be last
	fields        []recordField // every assignment, in file order, for lintRecordFields
}

// LintRecordModel scans the raw file text for record-model violations.
//
// It works on text rather than on the parsed config because every rule here is
// about layout the TOML parser discards: where a comment sits, whether the doc
// field comes last, whether the record is closed. It tracks the multi-line
// string opened by `comments = """` so that a `#` line or a `[`-prefixed line
// inside the documentation is not mistaken for file structure.
//
// The comment block that opens the file is exempt: see recordModelScanner.seenRecord.
func LintRecordModel(content string) []LintIssue {
	s := &recordModelScanner{}
	for idx, line := range strings.Split(content, "\n") {
		s.scanLine(idx+1, line)
	}
	s.closeRecord()

	return s.issues
}

// recordModelScanner is the state LintRecordModel carries from line to line.
type recordModelScanner struct {
	issues     []LintIssue
	cur        *recordLintState
	inComments bool
	// seenRecord turns the stray-comment rule on. Everything before the first
	// record header is the file header — the text that documents the record
	// model itself (field order, enabled vs hold, the traps a new record has to
	// avoid). It describes every record, so it can live inside none of them, and
	// flagging it would push maintainers to delete the one thing that makes the
	// file editable by hand. Once a record has been seen, a floating comment is
	// documentation stranded between records, which is what the rule is for.
	seenRecord bool
}

// closeRecord reports the record-level rules of the record being scanned, if
// any, and forgets it.
func (s *recordModelScanner) closeRecord() {
	cur := s.cur
	if cur == nil {
		return
	}
	if !cur.closed {
		s.issues = append(s.issues, LintIssue{
			Line: cur.headerLine, Package: cur.name, Rule: LintMissingEnd,
			Message: fmt.Sprintf("record is not closed by a %q line", RecordEndMarker),
		})
	}
	if !cur.hasComments {
		s.issues = append(s.issues, LintIssue{
			Line: cur.headerLine, Package: cur.name, Rule: LintMissingComments,
			Message: "record has no comments field documenting why this source and parser",
		})
	} else if cur.fieldsAfterCm > 0 {
		s.issues = append(s.issues, LintIssue{
			Line: cur.commentsLine, Package: cur.name, Rule: LintCommentsNotLast,
			Message: fmt.Sprintf("comments must be the last field, but %d field(s) follow it", cur.fieldsAfterCm),
		})
	}
	s.issues = append(s.issues, lintRecordFields(cur)...)
	s.cur = nil
}

// scanLine applies the record-model rules to one line of the file.
func (s *recordModelScanner) scanLine(lineNo int, line string) {
	trimmed := strings.TrimSpace(line)

	if s.inComments {
		s.scanCommentsBody(lineNo, line, trimmed)
		return
	}

	if name, isHeader := tomlTableName(line); isHeader {
		s.closeRecord()
		s.cur = &recordLintState{name: name, headerLine: lineNo}
		s.seenRecord = true
		return
	}

	if trimmed == "" {
		return
	}

	if strings.HasPrefix(trimmed, "#") {
		s.scanCommentLine(lineNo, trimmed)
		return
	}

	if s.cur == nil {
		return
	}

	s.scanAssignment(lineNo, line)
}

// scanCommentsBody handles a line inside the doc string: it only looks for the
// terminator, and flags a line that the raw-text section scanner elsewhere
// would read as a header.
func (s *recordModelScanner) scanCommentsBody(lineNo int, line, trimmed string) {
	if strings.Contains(line, `"""`) {
		s.inComments = false
		return
	}
	if strings.HasPrefix(trimmed, "[") && s.cur != nil {
		s.issues = append(s.issues, LintIssue{
			Line: lineNo, Package: s.cur.name, Rule: LintBracketInComment,
			Message: `a comments line starting with "[" is read as a section header by the raw-text editors; indent it`,
		})
	}
}

// scanCommentLine handles a comment line. Before the first record it is the
// file header, which the model allows. Inside an open record it is either the
// end marker or a leftover doc line that belongs in comments; between records
// it is the floating comment the record model forbids.
func (s *recordModelScanner) scanCommentLine(lineNo int, trimmed string) {
	switch {
	case !s.seenRecord:
		// File header — allowed, see seenRecord.
	case s.cur == nil || s.cur.closed:
		s.issues = append(s.issues, LintIssue{
			Line: lineNo, Rule: LintStrayComment,
			Message: "comment outside any record; move it into the comments field of the record it describes",
		})
	case trimmed == RecordEndMarker:
		s.cur.closed = true
	default:
		s.issues = append(s.issues, LintIssue{
			Line: lineNo, Package: s.cur.name, Rule: LintInlineComment,
			Message: "comment inside a record; the documentation belongs in the comments field",
		})
	}
}

// scanAssignment records a key/value assignment of the open record, and opens
// the doc string when the assignment is a multi-line comments field.
func (s *recordModelScanner) scanAssignment(lineNo int, line string) {
	cur := s.cur
	m := keyAssignRegex.FindStringSubmatch(line)
	if m == nil {
		return
	}
	// Every assignment is recorded, comments included, because the field-set
	// and field-order rules need the whole sequence. m[0] ends at the "=", so
	// the remainder is the value even when the value itself contains one
	// (pattern = 'a=b').
	cur.fields = append(cur.fields, recordField{
		key:   m[1],
		value: strings.TrimSpace(line[len(m[0]):]),
		line:  lineNo,
	})
	if m[1] == "comments" {
		cur.hasComments = true
		cur.commentsLine = lineNo
		// A multi-line string stays open unless it also closes on this line.
		if commentsOpenRegex.MatchString(line) {
			rest := line[strings.Index(line, `"""`)+3:]
			s.inComments = !strings.Contains(rest, `"""`)
		}
		return
	}
	if cur.hasComments {
		cur.fieldsAfterCm++
	}
}

// orderedField is one field as the record will carry it AFTER repair: canonical
// is the name it will then have, written is the key literally in the file. The
// two differ only for a `binary` line that repairs into `type`.
type orderedField struct {
	canonical string
	written   string
	line      int
}

// lintRecordFields reports the four field-set rules of one record: the retired
// `binary` key, a redundant `enabled = true`, an assignment order that is not
// canonical, and a commit-tracked entry whose base version has no declared source.
//
// All four live in the text scanner: `binary` has no struct field, a redundant
// `enabled = true` parses the same as none, and the parser discards order; a
// `track = "commit"` record without `base_from` is legal (documented legacy
// behaviour), so it is reported rather than failing the load. Each also owes the
// maintainer a line number, which only the scan has.
//
// The order check ranks the EFFECTIVE field list — the record after the repairs
// above — not the literal one. A `binary` that becomes `type = "bin"` is ranked
// as `type`, so a record the repair would reorder is flagged first; a deleted
// line is dropped before ranking, so it cannot manufacture a deviation. Hence
// `--lint --fix` followed by `--lint` reports nothing, which is what makes the
// repair trustworthy. A key in neither CanonicalFieldOrder nor the retired set is
// skipped: it already fails the load as LintUnknownField, and a finding about
// where the typo sits would be noise.
func lintRecordFields(rec *recordLintState) []LintIssue {
	if rec == nil || len(rec.fields) == 0 {
		return nil
	}

	facts := collectRecordFieldFacts(rec.fields)
	issues, effective := lintFieldSet(rec, facts)

	if issue, ok := fieldOrderIssue(rec.name, effective); ok {
		issues = append(issues, issue)
	}

	// No repair is offered on purpose: which source applies depends on
	// where upstream versions itself, which only a human reading that upstream
	// knows.
	//
	// Any declared value silences this, `none` included — an upstream that does
	// not version itself at all is a real case, and one the rule would otherwise
	// report forever with no action its reader could take. Naming `none` in the
	// message is what keeps that from reading as a rule to be ignored.
	if facts.trackCommitLine > 0 && !facts.hasBaseFrom {
		issues = append(issues, LintIssue{
			Line: facts.trackCommitLine, Package: rec.name, Rule: LintLegacyBase, Fix: FixNone,
			Message: `track = "commit" without base_from: the base version is whatever the ebuild already carries and can freeze there unnoticed; declare base_from = "file", "tag" or "commit_message" — or "none" when upstream publishes no version at all`,
		})
	}

	// Emitted in line order so the report reads down the file, the way runLint
	// prints it. Stable, so two issues on one line keep the order above.
	sort.SliceStable(issues, func(i, j int) bool { return issues[i].Line < issues[j].Line })
	return issues
}

// recordFieldFacts is what lintRecordFields needs to know about the whole
// record before it judges any single field.
type recordFieldFacts struct {
	hasType         bool
	typeValue       string
	hasBaseFrom     bool
	trackCommitLine int
}

// collectRecordFieldFacts scans a record's assignments for the facts the
// field-set rules depend on.
func collectRecordFieldFacts(fields []recordField) recordFieldFacts {
	var facts recordFieldFacts
	for _, f := range fields {
		switch f.key {
		case "type":
			facts.hasType = true
			facts.typeValue = tomlStringValue(f.value)
		case "base_from":
			// An empty string declares nothing, so it does not silence the
			// legacy-base rule the way a real source does.
			facts.hasBaseFrom = !isEmptyTOMLString(f.value)
		case "track":
			if tomlStringValue(f.value) == "commit" {
				facts.trackCommitLine = f.line
			}
		}
	}
	return facts
}

// lintFieldSet applies the per-field rules (`binary`, redundant `enabled`) to a
// record and returns their issues, in field order, together with the effective
// field list the order check ranks.
func lintFieldSet(rec *recordLintState, facts recordFieldFacts) ([]LintIssue, []orderedField) {
	var issues []LintIssue
	effective := make([]orderedField, 0, len(rec.fields))

	for _, f := range rec.fields {
		switch f.key {
		case "binary":
			issue, becomesType := lintBinaryField(rec.name, f, facts)
			issues = append(issues, issue)
			if becomesType {
				effective = append(effective, orderedField{canonical: "type", written: f.key, line: f.line})
			}

		case "enabled":
			// Only `true` is redundant. `enabled = false` is the bookkeeping that
			// keeps an orphaned entry out of the run and must survive untouched.
			if on, isBool := tomlBoolValue(f.value); isBool && on {
				issues = append(issues, LintIssue{
					Line: f.line, Package: rec.name, Rule: LintRedundantEnabled, Fix: FixDropEnabled,
					Message: "enabled = true is redundant: an absent enabled already means enabled",
				})
				continue
			}
			effective = append(effective, orderedField{canonical: f.key, written: f.key, line: f.line})

		default:
			if _, ranked := canonicalFieldRank[f.key]; ranked {
				effective = append(effective, orderedField{canonical: f.key, written: f.key, line: f.line})
			}
		}
	}
	return issues, effective
}

// lintBinaryField reports the retired `binary` key of one record, and whether
// the repair turns the line into `type` (true) or deletes it (false).
func lintBinaryField(pkg string, f recordField, facts recordFieldFacts) (LintIssue, bool) {
	// The classifier is `type`. Which repair applies depends on whether
	// the record already declares one — carried in Fix, not in the prose.
	if on, isBool := tomlBoolValue(f.value); isBool && on && !facts.hasType {
		return LintIssue{
			Line: f.line, Package: pkg, Rule: LintLegacyBinary, Fix: FixBinaryToType,
			Message: `binary is retired: the record declares no type, so it becomes type = "bin"`,
		}, true
	}
	if _, isBool := tomlBoolValue(f.value); !isBool {
		return LintIssue{
			Line: f.line, Package: pkg, Rule: LintLegacyBinary, Fix: FixDropBinary,
			Message: fmt.Sprintf("binary is retired, and %s is not a boolean, so the line is deleted", tomlValueText(f.value)),
		}, false
	}
	detail := "the record already declares type"
	if facts.typeValue != "" {
		detail = fmt.Sprintf("the record already declares type = %q", facts.typeValue)
	}
	if !facts.hasType {
		// binary = false: the default classification spelled out. It says
		// nothing `type` would not say better, so the line just goes.
		detail = "it says nothing, auto-detection is the default"
	}
	return LintIssue{
		Line: f.line, Package: pkg, Rule: LintLegacyBinary, Fix: FixDropBinary,
		Message: fmt.Sprintf("binary is retired: %s, so the line is deleted", detail),
	}, false
}

// fieldOrderIssue reports the first field of effective that breaks the
// canonical order, if any.
//
// The rule is that the record's fields form a SUBSEQUENCE of the canonical
// order — each rank strictly greater than the one before. Demanding anything
// stronger, contiguity for instance, would flag all 411 records: no record
// declares more than a fraction of the 38 fields. One issue per record: the
// first offending field is what to look at.
func fieldOrderIssue(pkg string, effective []orderedField) (LintIssue, bool) {
	prevRank := -1
	prevName := ""
	for _, f := range effective {
		rank := canonicalFieldRank[f.canonical]
		if rank < prevRank {
			msg := fmt.Sprintf("field %q is out of canonical order: it belongs before %q", f.canonical, prevName)
			if f.written != f.canonical {
				msg = fmt.Sprintf("field %q (written as the retired %q) is out of canonical order: it belongs before %q",
					f.canonical, f.written, prevName)
			}
			return LintIssue{
				Line: f.line, Package: pkg, Rule: LintFieldOrder, Fix: FixReorderFields,
				Message: msg,
			}, true
		}
		prevRank = rank
		prevName = f.canonical
	}
	return LintIssue{}, false
}

// tomlBoolValue reports the boolean an assignment's right-hand side holds, and
// whether it holds one at all. It reads the first whitespace-separated token so
// a trailing inline comment (`enabled = true # legacy`) is not read as part of
// the value; anything that is not a bare TOML boolean yields ok = false.
func tomlBoolValue(raw string) (value, ok bool) {
	fields := strings.Fields(raw)
	if len(fields) == 0 {
		return false, false
	}
	switch fields[0] {
	case "true":
		return true, true
	case "false":
		return false, true
	}
	return false, false
}

// isEmptyTOMLString reports whether an assignment's right-hand side is the
// empty basic or literal string ("" or ”), optionally followed by an inline
// comment. A multi-line string ("""…""") is not empty, whatever it holds.
func isEmptyTOMLString(raw string) bool {
	v := strings.TrimSpace(raw)
	if !strings.HasPrefix(v, `""`) && !strings.HasPrefix(v, "''") {
		return false
	}
	tail := strings.TrimSpace(v[2:])
	return tail == "" || tail[0] == '#'
}

// tomlValueText is an assignment's right-hand side as a finding quotes it: a
// basic or literal string with its quotes, else the first token, so a
// trailing inline comment is not read as part of the value.
func tomlValueText(raw string) string {
	v := strings.TrimSpace(raw)
	if v != "" && (v[0] == '"' || v[0] == '\'') {
		if end := strings.IndexByte(v[1:], v[0]); end >= 0 {
			return v[:end+2]
		}
		return v
	}
	if fields := strings.Fields(v); len(fields) > 0 {
		return fields[0]
	}
	return v
}

// tomlStringValue unquotes the right-hand side of an assignment when it is a
// single-line basic ("…") or literal ('…') string, optionally followed by an
// inline comment (`track = "commit" # pinned`), and returns "" otherwise. It is
// deliberately minimal: the only values read through it are the closed
// vocabularies of `track`, `type` and `base_from`, which never carry an escape.
func tomlStringValue(raw string) string {
	v := strings.TrimSpace(raw)
	if len(v) < 2 || (v[0] != '"' && v[0] != '\'') {
		return ""
	}
	end := strings.IndexByte(v[1:], v[0])
	if end < 0 {
		return ""
	}
	if tail := strings.TrimSpace(v[end+2:]); tail != "" && tail[0] != '#' {
		return ""
	}
	return v[1 : end+1]
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
