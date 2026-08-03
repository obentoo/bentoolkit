package autoupdate

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
)

// This file is the repair half of the linter: lint.go says what deviates from
// the record model, this rewrites the file so it no longer does (R2.2, R3.3,
// R7).
//
// It is TEXTUAL on purpose, and that is the single most important thing to know
// before editing it. The obvious implementation — decode packages.toml into
// map[string]PackageConfig and re-emit it through RenderRecord — would rewrite
// every value from the parsed struct and destroy exactly what the registry keeps
// in its text: the maintainer's quoting (414 regexes written as literal strings
// so a backslash needs no escape, and 2 written as basic strings because the
// regex itself contains a "'"), the inline `headers = { … }` tables, and above
// all the `comments` blocks, which are the only documentation 411 records have.
// RenderRecord is for records the tool GENERATES; nothing in this file may call
// it.
//
// So the repair keeps the original lines and only reorders, drops or substitutes
// them. Every line the file already had comes out either unchanged or not at
// all, and verifyRepair proves that before anything is written.

// fieldBlock is one field of a record exactly as the file writes it.
//
// body holds the assignment line and everything that continues it: the doc lines
// of a `comments = """` block up to and including its closing delimiter, or the
// continuation lines of a value spread over several lines (`transform = [`…`]`).
// lead holds the blank or comment lines that immediately preceded the
// assignment; they travel with the field so that a comment written above a field
// still sits above it after a reorder.
type fieldBlock struct {
	key  string
	lead []string
	body []string
}

// recordBlock is one `["category/package"]` record split into the units the
// repair moves independently: the header line, the fields, and the tail — every
// line after the last field, which is where the `# END` marker and the blank
// line separating this record from the next one live. Keeping the tail verbatim
// is why a reorder cannot lose a marker or collapse the file's spacing.
type recordBlock struct {
	name   string
	header string
	fields []fieldBlock
	tail   []string
}

// registryLayout is packages.toml split into its preamble — the file header,
// which documents the record model itself and belongs to no record — and its
// records.
type registryLayout struct {
	preamble []string
	records  []recordBlock
}

// parseRegistryLayout splits the registry into the layout above.
//
// It does NOT run a second `comments = """` scanner: it asks commentsBodyMask,
// the one config.go's raw-text editors already use, which line sits inside a doc
// string. That matters more than it looks. `comments = """` both opens and ends
// with the delimiter, so an open-detector written as "the line does not end with
// the delimiter" reports every block as closed on its opening line — the
// prototype's measured bug, which truncated every documentation block and left
// the file unparseable. The correct test is whether what FOLLOWS the opening
// delimiter contains another one, and it is written down once, in
// commentsBodyMask, next to lintRecordModel's identical copy.
//
// A masked line is opaque: it can begin with "#" or "[" and is still doc text,
// never a comment, a header or an assignment.
func parseRegistryLayout(content string) registryLayout {
	// Split on "\n" (not bufio.Scanner) so a file with or without a trailing
	// newline is reproduced byte for byte by the Join in render.
	lines := strings.Split(content, "\n")
	mask := commentsBodyMask(lines)

	var layout registryLayout
	var cur *recordBlock
	// pending holds loose lines — blank or comment — not yet attached to
	// anything. They become the lead of the next field, or the tail of the
	// record when no field follows.
	var pending []string

	// appendToCurrentValue reports whether the line can extend the value above
	// it. It cannot when there is no field to extend, and must not when loose
	// lines are pending: a value does not resume after a blank line, and
	// swallowing the pending lines here would move them past text that precedes
	// them.
	appendToCurrentValue := func(line string) bool {
		if cur == nil || len(cur.fields) == 0 || len(pending) > 0 {
			return false
		}
		last := &cur.fields[len(cur.fields)-1]
		last.body = append(last.body, line)
		return true
	}

	closeRecord := func() {
		if cur == nil {
			layout.preamble = append(layout.preamble, pending...)
			pending = nil
			return
		}
		cur.tail = append(cur.tail, pending...)
		pending = nil
		layout.records = append(layout.records, *cur)
		cur = nil
	}

	for i, line := range lines {
		if mask[i] {
			if !appendToCurrentValue(line) {
				// Unreachable for a file that parses: a masked line always has the
				// `comments` assignment that opened it directly above. Kept so a
				// malformed file loses nothing.
				pending = append(pending, line)
			}
			continue
		}

		if name, isHeader := tomlTableName(line); isHeader {
			closeRecord()
			cur = &recordBlock{name: name, header: line}
			continue
		}

		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			pending = append(pending, line)
			continue
		}

		if m := keyAssignRegex.FindStringSubmatch(line); m != nil && cur != nil {
			cur.fields = append(cur.fields, fieldBlock{key: m[1], lead: pending, body: []string{line}})
			pending = nil
			continue
		}

		// Neither header, comment, blank line nor assignment: a continuation of
		// the value above — the `["-", ""],` line of a multi-line array. The real
		// registry writes none today, but a rewriter that dropped one would be
		// discovered by whoever wrote the first.
		if !appendToCurrentValue(line) {
			pending = append(pending, line)
		}
	}
	closeRecord()

	return layout
}

// render writes the layout back out. Parsing and rendering are exact inverses:
// every input line is carried in exactly one slot and joined back with the
// separator it was split on, so an untouched layout reproduces its input byte
// for byte. buildRepair asserts precisely that before it transforms anything.
func (l registryLayout) render() string {
	out := make([]string, 0, len(l.preamble)+len(l.records)*8)
	out = append(out, l.preamble...)
	for _, rec := range l.records {
		out = append(out, rec.header)
		for _, f := range rec.fields {
			out = append(out, f.lead...)
			out = append(out, f.body...)
		}
		out = append(out, rec.tail...)
	}
	return strings.Join(out, "\n")
}

// commentsBlock returns the record's doc field exactly as the file writes it —
// the `comments = """` line through the closing delimiter — and whether the
// record has one. It is the unit UB2 pins byte for byte.
func (r recordBlock) commentsBlock() (string, bool) {
	for _, f := range r.fields {
		if f.key == "comments" {
			return strings.Join(f.body, "\n"), true
		}
	}
	return "", false
}

// recordRepair is what the repair declares it did to one record — and, read the
// other way, what the reparse gate is allowed to see change there. Nothing
// outside this list may differ (R7.1).
type recordRepair struct {
	name string
	// migratedBinary: `binary = true` became `type = "bin"` (R1.2). The parsed
	// record's Type goes from "" to "bin"; `binary` itself is invisible to both
	// parses, since no struct field claims it.
	migratedBinary bool
	// droppedBinary: a `binary` line was deleted outright (R1.3). It needs no
	// adjustment in the gate — the key has no struct field, so neither parse ever
	// saw it — and is declared anyway, because a repair the plan does not name is
	// a repair nobody reviewed.
	droppedBinary bool
	// droppedEnabled: a redundant `enabled = true` was deleted (R2.2). The parsed
	// record's Enabled goes from a pointer to true to nil, which IsEnabled reads
	// the same way.
	droppedEnabled bool
	// reordered: the fields were sorted into CanonicalFieldOrder (R3.2). Wholly
	// invisible to the parse — a TOML table is a map — which is why the gate
	// needs the byte-level checks beside it.
	reordered bool
}

// repairPlan is the whole declared transformation: what changed per record, and
// the exact lines deleted and inserted anywhere in the file. The line lists turn
// "the repair only reordered things" from a claim into an arithmetic identity
// the gate checks (see verifyRepair's line-inventory step).
type repairPlan struct {
	records []recordRepair
	removed []string
	added   []string
	// actions counts applications per Fix* identifier, for the summary the
	// caller prints.
	actions map[string]int
}

func (p *repairPlan) changed() bool {
	return len(p.records) > 0
}

// RepairResult is one computed repair of packages.toml: the file as it was read,
// the file as the repair would write it, and a tally of what it did. It is
// produced by RepairPackagesConfig and written by Write — two steps, because the
// registry auto-commits and auto-publishes, so the caller shows the diff and
// asks before anything lands (R7.3, wired in the command layer).
type RepairResult struct {
	// Path is the registry the result was computed from.
	Path string
	// Original is the file as read, and Repaired the file as it would be
	// written; equal when Changed is false. Both are held so the caller can diff
	// them without re-reading a file that may have moved underneath it.
	Original string
	Repaired string
	// Changed reports whether the repair has anything to write.
	Changed bool
	// Actions counts records per repair action, keyed by the Fix* identifiers
	// LintIssue.Fix uses — so the repair's summary and the lint report speak the
	// same vocabulary.
	Actions map[string]int

	// plan is the declared transformation, kept so Write can re-verify against
	// it rather than trusting that Repaired is still what the gate approved.
	plan repairPlan
}

// RepairPackagesConfig computes the canonical rewrite of the overlay's
// packages.toml and proves it inert before returning it. It writes NOTHING; call
// Write on the result to do that.
//
// The repair applies exactly four transformations, all of them the repairs
// lint.go already reports (so `--lint --fix` never touches a record `--lint` did
// not name):
//
//   - `binary = true` in a record with no `type` becomes `type = "bin"`, in
//     place, so the field then sorts as `type` (R1.2);
//   - any other `binary` line is deleted — the record already classifies itself,
//     or `binary = false` was only spelling out the default (R1.3);
//   - `enabled = true` is deleted, `enabled = false` never (R2.2);
//   - the record's fields are sorted into CanonicalFieldOrder (R3.2).
//
// Everything else is carried through verbatim, `comments` blocks byte for byte
// (R3.3). `track = "commit"` without `base_from` is reported by the linter and
// deliberately not repaired here: which base source is right depends on where
// upstream versions itself (R6.1).
//
// An error means the repair was ABORTED and nothing was produced — either the
// file could not be read or parsed, or the verification found a difference
// outside the four transformations above. That is a bug report about this code,
// not a condition the caller can retry around.
func RepairPackagesConfig(overlayPath string) (*RepairResult, error) {
	configPath := filepath.Join(overlayPath, ".autoupdate", "packages.toml")

	data, err := os.ReadFile(configPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, ErrPackagesConfigNotFound
		}
		return nil, fmt.Errorf("failed to read packages.toml: %w", err)
	}
	original := string(data)

	// The original has to parse before anything else: the gate compares the
	// rewrite against it record by record, and there is nothing to compare
	// against when the file does not load. `binary` survives this parse only
	// because retiredKeys claims it — see there for why the migration would
	// otherwise be unable to read the file it migrates.
	if _, err := decodePackagesConfig(data); err != nil {
		return nil, fmt.Errorf("refusing to repair packages.toml: it does not load as it stands: %w", err)
	}

	repaired, plan, err := buildRepair(original)
	if err != nil {
		return nil, err
	}

	result := &RepairResult{
		Path:     configPath,
		Original: original,
		Repaired: repaired,
		Changed:  plan.changed(),
		Actions:  plan.actions,
		plan:     plan,
	}
	if !result.Changed {
		return result, nil
	}

	if err := verifyRepair(original, repaired, plan); err != nil {
		return nil, err
	}

	return result, nil
}

// Write replaces the registry with the repaired text, atomically and preserving
// the file's mode (R7.2). A result with nothing to change writes nothing and
// leaves the file's mtime alone.
//
// The verification runs again here, deliberately. RepairPackagesConfig already
// ran it, but between the two calls the result travels through a diff, a
// terminal and a confirmation, and the guarantee this file exists to make is
// that no text reaches packages.toml that was not proved inert immediately
// before the rename — not "was proved inert at some earlier point". The check
// costs one TOML parse of a 240 KB file.
func (r *RepairResult) Write() error {
	if r == nil || !r.Changed {
		return nil
	}
	if err := verifyRepair(r.Original, r.Repaired, r.plan); err != nil {
		return fmt.Errorf("repair aborted before writing, packages.toml is untouched: %w", err)
	}
	return writePackagesConfigAtomically(r.Path, []byte(r.Repaired))
}

// buildRepair produces the repaired text and the plan describing what it did.
//
// Before transforming anything it asserts that parsing and rendering the file
// round-trips byte for byte. That check is cheap and it is the one that fires on
// the failure mode this whole file is defensive about: if the layout model
// misreads a shape — a doc block it thinks closed on its opening line, a
// multi-line value it thinks is four separate fields — the round trip differs
// and the repair aborts on the spot, before a single transformation has been
// applied to a misunderstood structure.
func buildRepair(content string) (string, repairPlan, error) {
	plan := repairPlan{actions: make(map[string]int, 4)}

	layout := parseRegistryLayout(content)
	if round := layout.render(); round != content {
		return "", plan, fmt.Errorf(
			"refusing to repair packages.toml: reading and rewriting it unchanged did not reproduce it (%s) — the repair does not understand this file's layout",
			describeFirstDifference(content, round))
	}

	for i := range layout.records {
		repairRecord(&layout.records[i], &plan)
	}

	if !plan.changed() {
		return content, plan, nil
	}
	return layout.render(), plan, nil
}

// repairRecord applies the four transformations to one record, recording each in
// the plan. It mutates rec in place.
func repairRecord(rec *recordBlock, plan *repairPlan) {
	var done recordRepair
	done.name = rec.name

	hasType := false
	for _, f := range rec.fields {
		if f.key == "type" {
			hasType = true
			break
		}
	}

	kept := make([]fieldBlock, 0, len(rec.fields))
	// carried holds the lead lines of deleted fields. A comment written above a
	// line that is about to go still belongs to the record, so it moves down to
	// the next field instead of disappearing with the line it introduced.
	var carried []string

	for _, f := range rec.fields {
		lead := f.lead
		if len(carried) > 0 {
			lead = append(append([]string{}, carried...), lead...)
			carried = nil
		}

		// Only a single-line assignment is transformed. A `binary` or `enabled`
		// spread over several lines is not a shape this repair understands, and
		// guessing at one is how a rewriter eats a value.
		singleLine := len(f.body) == 1

		switch {
		case f.key == "binary" && singleLine:
			if on, isBool := tomlBoolValue(assignedValue(f.body[0])); isBool && on && !hasType {
				// The substitution happens where `binary` stood, and the field is
				// then ranked as `type` — the same effective sequence lint.go
				// ranks, so the reorder the linter reported is the reorder that
				// happens here (see lintRecordFields on why nxplayer is the 13th).
				line := lineIndent(f.body[0]) + `type = "bin"`
				plan.removed = append(plan.removed, f.body[0])
				plan.added = append(plan.added, line)
				plan.actions[FixBinaryToType]++
				done.migratedBinary = true
				kept = append(kept, fieldBlock{key: "type", lead: lead, body: []string{line}})
				continue
			}
			plan.removed = append(plan.removed, f.body[0])
			plan.actions[FixDropBinary]++
			done.droppedBinary = true
			carried = lead

		case f.key == "enabled" && singleLine:
			// Only `true` goes. `enabled = false` is the bookkeeping that keeps an
			// orphaned entry out of the run and must survive untouched (R2.2).
			if on, isBool := tomlBoolValue(assignedValue(f.body[0])); isBool && on {
				plan.removed = append(plan.removed, f.body[0])
				plan.actions[FixDropEnabled]++
				done.droppedEnabled = true
				carried = lead
				continue
			}
			kept = append(kept, fieldBlock{key: f.key, lead: lead, body: f.body})

		default:
			kept = append(kept, fieldBlock{key: f.key, lead: lead, body: f.body})
		}
	}
	if len(carried) > 0 {
		rec.tail = append(append([]string{}, carried...), rec.tail...)
	}

	rec.fields = kept
	if reorderFields(rec) {
		plan.actions[FixReorderFields]++
		done.reordered = true
	}

	if done.migratedBinary || done.droppedBinary || done.droppedEnabled || done.reordered {
		plan.records = append(plan.records, done)
	}
}

// reorderFields sorts the record's fields into CanonicalFieldOrder and reports
// whether that moved anything.
//
// A record carrying a field the canonical order does not name is left alone
// rather than sorted with the stranger pushed to one end: the repair must never
// decide where a field it does not recognise belongs. In practice such a record
// never reaches here — an unknown key fails the load (R4.1) and the repair
// refuses to start on a file that does not load — so this is the guard for the
// case where that stops being true.
func reorderFields(rec *recordBlock) bool {
	for _, f := range rec.fields {
		if _, ranked := canonicalFieldRank[f.key]; !ranked {
			return false
		}
	}

	before := make([]string, len(rec.fields))
	for i, f := range rec.fields {
		before[i] = f.key
	}

	// Stable, so two fields of equal rank — which valid TOML cannot produce, a
	// duplicate key being a parse error — keep the order the file gave them.
	sort.SliceStable(rec.fields, func(i, j int) bool {
		return canonicalFieldRank[rec.fields[i].key] < canonicalFieldRank[rec.fields[j].key]
	})

	for i, f := range rec.fields {
		if before[i] != f.key {
			return true
		}
	}
	return false
}

// assignedValue returns the raw text right of the "=" of an assignment line,
// trimmed. It mirrors what lintRecordModel captures into recordField.value, so
// the repair reads a value exactly the way the rule that reported it did.
func assignedValue(line string) string {
	m := keyAssignRegex.FindStringSubmatch(line)
	if m == nil {
		return ""
	}
	return strings.TrimSpace(line[len(m[0]):])
}

// lineIndent returns the leading whitespace of a line, so a substituted
// assignment keeps the indentation of the one it replaces.
func lineIndent(line string) string {
	return line[:len(line)-len(strings.TrimLeft(line, " \t"))]
}

// verifyRepair is R7.1: it proves the rewrite differs from the original only by
// the transformations the plan declares, and returns an error — meaning ABORT,
// write nothing — as soon as it does not.
//
// It runs five checks, and the redundancy between them is the point. Each one
// alone has a blind spot the next covers:
//
//  1. THE REWRITE PARSES. A truncated `comments` block usually shows up here:
//     the doc text spills out of the string and the file stops being TOML.
//  2. THE SAME RECORDS EXIST. A record gained, lost or renamed is fatal
//     whatever else matches.
//  3. EVERY RECORD PARSES TO THE SAME VALUES, after applying the declared
//     transformations to the original. This is what catches a comments block
//     that was truncated at a point where the file still parses, a value
//     re-quoted into something different, or a line that migrated from one
//     record to another.
//  4. EVERY `comments` BLOCK IS BYTE-IDENTICAL (R3.3, UB2). Check 3 compares
//     DECODED values, so it cannot see a doc block rewritten into different
//     bytes that decode the same — a re-escaped quote, a re-wrapped line. This
//     one can, and it is the only check that pins the ORDER of the doc lines.
//  5. THE LINE INVENTORY BALANCES. The multiset of lines in the rewrite must
//     equal the original's, minus the lines the plan says were deleted, plus the
//     ones it says were inserted. This is the check that sees what the parser
//     cannot: the `# END` markers, the file header, the blank lines between
//     records — none of which any TOML parse can tell you went missing, because
//     to a parser they were never there.
func verifyRepair(original, repaired string, plan repairPlan) error {
	// 1. The rewrite parses at all.
	after, err := decodePackagesConfig([]byte(repaired))
	if err != nil {
		return fmt.Errorf("repair aborted: the rewritten packages.toml does not parse: %w", err)
	}
	before, err := decodePackagesConfig([]byte(original))
	if err != nil {
		return fmt.Errorf("repair aborted: the original packages.toml does not parse: %w", err)
	}

	// 2. The same records, no more and no fewer.
	if len(before.Packages) != len(after.Packages) {
		return fmt.Errorf("repair aborted: %d record(s) before the repair, %d after",
			len(before.Packages), len(after.Packages))
	}
	for _, name := range sortedKeys(before.Packages) {
		if _, ok := after.Packages[name]; !ok {
			return fmt.Errorf("repair aborted: record %q disappeared from the rewritten file", name)
		}
	}

	// 3. Every record holds the same values, once the declared transformations
	// are applied to the original.
	declared := make(map[string]recordRepair, len(plan.records))
	for _, r := range plan.records {
		declared[r.name] = r
	}
	for _, name := range sortedKeys(before.Packages) {
		want := before.Packages[name]
		if r, ok := declared[name]; ok {
			if r.migratedBinary {
				want.Type = "bin"
			}
			if r.droppedEnabled {
				want.Enabled = nil
			}
		}
		if got := after.Packages[name]; !reflect.DeepEqual(want, got) {
			return fmt.Errorf("repair aborted: record %q changed outside the declared repairs%s",
				name, describeRecordDifference(want, got))
		}
	}

	// 4. Every doc block survived byte for byte (R3.3, UB2).
	beforeDocs := parseRegistryLayout(original).records
	afterDocs := make(map[string]recordBlock, len(beforeDocs))
	for _, rec := range parseRegistryLayout(repaired).records {
		afterDocs[rec.name] = rec
	}
	for _, rec := range beforeDocs {
		was, hadDoc := rec.commentsBlock()
		is, hasDoc := afterDocs[rec.name].commentsBlock()
		if hadDoc != hasDoc {
			return fmt.Errorf("repair aborted: record %q had a comments block before the repair (%t) and after (%t)",
				rec.name, hadDoc, hasDoc)
		}
		if was != is {
			return fmt.Errorf("repair aborted: the comments block of record %q is not byte-identical (%s)",
				rec.name, describeFirstDifference(was, is))
		}
	}

	// 5. The line inventory balances.
	return verifyLineInventory(original, repaired, plan)
}

// verifyLineInventory checks that the rewrite is a permutation of the original
// with exactly the plan's deletions and insertions applied — nothing lost,
// nothing invented, nothing duplicated. Order is deliberately ignored here:
// reordering fields is the whole point of the repair, and checks 3 and 4 above
// pin the order that does matter.
func verifyLineInventory(original, repaired string, plan repairPlan) error {
	want := countLines(strings.Split(original, "\n"))
	for _, line := range plan.removed {
		if want[line] == 0 {
			return fmt.Errorf("repair aborted: it reports deleting %q, a line the original file does not have", line)
		}
		want[line]--
		if want[line] == 0 {
			delete(want, line)
		}
	}
	for _, line := range plan.added {
		want[line]++
	}

	got := countLines(strings.Split(repaired, "\n"))
	if reflect.DeepEqual(want, got) {
		return nil
	}

	// Name the first imbalance rather than dumping two inventories: the caller
	// prints this to a terminal, and one missing line is the whole diagnosis.
	for _, line := range sortedCountKeys(want) {
		if got[line] != want[line] {
			return fmt.Errorf("repair aborted: line %q appears %d time(s) in the original and %d in the rewrite, which no declared repair explains",
				line, want[line], got[line])
		}
	}
	for _, line := range sortedCountKeys(got) {
		if want[line] == 0 {
			return fmt.Errorf("repair aborted: the rewrite invented the line %q, which no declared repair explains", line)
		}
	}
	return fmt.Errorf("repair aborted: the rewritten file's lines do not account for the original's")
}

// countLines tallies how often each line occurs.
func countLines(lines []string) map[string]int {
	counts := make(map[string]int, len(lines))
	for _, line := range lines {
		counts[line]++
	}
	return counts
}

// sortedCountKeys returns a line tally's keys in ascending order, so an abort
// message names the same line on every run.
func sortedCountKeys(counts map[string]int) []string {
	keys := make([]string, 0, len(counts))
	for k := range counts {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// describeFirstDifference locates where two texts stop agreeing, as a line
// number and the pair of lines, so an abort says what to look at instead of
// only that something is wrong.
func describeFirstDifference(a, b string) string {
	al := strings.Split(a, "\n")
	bl := strings.Split(b, "\n")
	for i := 0; i < len(al) && i < len(bl); i++ {
		if al[i] != bl[i] {
			return fmt.Sprintf("first difference at line %d: %q became %q", i+1, al[i], bl[i])
		}
	}
	if len(al) != len(bl) {
		return fmt.Sprintf("%d line(s) before, %d after", len(al), len(bl))
	}
	return "no textual difference"
}

// describeRecordDifference names the fields whose parsed values differ, so a
// record-level abort says which key moved rather than dumping two structs.
func describeRecordDifference(want, got PackageConfig) string {
	wv := reflect.ValueOf(want)
	gv := reflect.ValueOf(got)
	t := wv.Type()

	var diffs []string
	for i := 0; i < t.NumField(); i++ {
		if reflect.DeepEqual(wv.Field(i).Interface(), gv.Field(i).Interface()) {
			continue
		}
		key := strings.Split(t.Field(i).Tag.Get("toml"), ",")[0]
		if key == "" {
			key = t.Field(i).Name
		}
		diffs = append(diffs, fmt.Sprintf("%s: %v became %v",
			key, wv.Field(i).Interface(), gv.Field(i).Interface()))
	}
	if len(diffs) == 0 {
		return ""
	}
	return " — " + strings.Join(diffs, "; ")
}
