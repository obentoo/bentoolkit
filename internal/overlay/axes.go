package overlay

import (
	"fmt"
	"maps"
	"os"
	"slices"
	"strings"

	"github.com/obentoo/bentoolkit/internal/common/ebuild"
)

// The four structural axes of D4, spelled once so a consumer selects on these
// rather than on a string it typed a second time.
const (
	// axisInherit is the axis that would have caught issue #33, and the reason
	// this file contains no model call at all.
	//
	// ::gentoo's gst-plugins-qt6-1.26.11 is `inherit gstreamer-meson` and passes
	// TWO `-D` options; our 1.29.2 is `inherit meson python-any-r1 xdg-utils` and
	// passes EIGHTY-FIVE, two of which stopped existing at 1.29. The whole
	// failure is visible in the difference between those two inherit lines: the
	// eclass is where ::gentoo keeps the option list, and dropping the eclass is
	// what put those 85 options in our hand to maintain. Detecting that is a text
	// comparison and a count — a `grep`, not a judgement.
	//
	// Which is why it MUST keep answering under `--no-review`. The operator who
	// turned every model off, and the machine with no `claude` installed at all,
	// still get this finding: CompareAxes takes two paths, contacts nothing, and
	// is called before any reviewer exists. A signal this cheap has no business
	// depending on a provider being configured — issue #33 shipped while nobody
	// was asking a model anything, and a finding that went quiet whenever the
	// model did would have been silent for exactly that run.
	axisInherit = "inherit"
	// axisOptions is the build options the ebuild passes — the count that turns
	// the dropped eclass into a number (85 against 2).
	axisOptions = "options"
	// axisIUSE is the flag set. A flag we add or drop is a decision somebody
	// made, and it is worth reporting whether or not they wrote it down.
	axisIUSE = "iuse"
	// axisDeps is the dependency atoms. Ours lower than ::gentoo's usually means
	// we fell behind, so the finding names both sides rather than merely saying
	// that they differ.
	axisDeps = "deps"
)

// axisBaselineLabel is how the baseline is named in a finding, in the ::repo
// spelling the rest of the report uses. Derived from baselineRepo so the two
// cannot drift apart.
const axisBaselineLabel = "::" + baselineRepo

// axisNameSample bounds how many names one finding lists before it summarises
// the rest as a count.
//
// It exists because of the very case this file was written for: 85 option names
// on one line is not a finding anybody reads, and the number is what carries the
// meaning anyway. The count of the omitted names is always printed, so the SIZE
// of the difference survives the truncation (R2.4).
const axisNameSample = 5

// AxisFinding is one structural difference between our ebuild and the ::gentoo
// baseline, on one of the four axes of D4.
//
// It renders NOTHING at its zero value, and a nil slice of them is the "nothing
// to say" state rather than a missing answer. That is what lets the field ride
// on an existing result without changing a byte of what `overlay compare`
// prints for a run that did not ask for a baseline review.
//
// _Requirements: R2, R2.4_
type AxisFinding struct {
	// Axis is the property that differs: inherit, options, iuse or deps.
	Axis string
	// Detail is what the difference IS, always stated in the same direction —
	// what we added on top of ::gentoo, and what ::gentoo has that we dropped.
	// A bare "the inherit lines differ" would not have told anyone which 85
	// options they had just taken over by hand.
	Detail string
}

// CompareAxes compares the four structural axes of two ebuilds and returns one
// finding per axis that differs.
//
// ourPath and baselinePath are the two EBUILD FILES, not their package
// directories: an axis is a property of one ebuild's text, and a directory would
// force this to guess which version it was asked about.
//
// # No model, no network, no provider
//
// It opens two files and reads nothing else. It starts no process, resolves no
// host, consults no git and takes no reviewer — a provider is not merely absent
// here, it is not a parameter, so the signature itself is the guarantee. That is
// deliberate and is the point of the whole file: `--no-review` already exists on
// `overlay compare`, and with it these findings still answer. See axisInherit
// for the case that makes this non-negotiable — the one axis that would have
// caught issue #33 is also the cheapest thing in the report.
//
// # One finding per axis, not per element
//
// A set difference is reported as ONE finding naming what moved, because four
// findings saying "we no longer inherit gstreamer-meson", "we now inherit
// meson", "we now inherit python-any-r1", "we now inherit xdg-utils" is the same
// sentence read four times, and it buries the one name that matters. The size of
// each difference is carried in the detail (R2.4).
//
// The error return is for a file that will not read, and for nothing else: two
// ebuilds that differ on every axis are four findings and a nil error, and two
// identical ebuilds are no findings and a nil error.
//
// _Requirements: R2, R2.4_
func CompareAxes(ourPath, baselinePath string) ([]AxisFinding, error) {
	ours, err := readAxisEbuild(ourPath)
	if err != nil {
		return nil, fmt.Errorf("comparing the structural axes: reading our ebuild: %w", err)
	}
	baseline, err := readAxisEbuild(baselinePath)
	if err != nil {
		return nil, fmt.Errorf("comparing the structural axes: reading the %s baseline ebuild: %w", axisBaselineLabel, err)
	}

	// Emitted in the order D4's table lists them, so a report reads the same way
	// twice. Map iteration decides nothing here.
	axes := []struct {
		name   string
		detail func(ours, baseline axisEbuild) string
	}{
		{axisInherit, axisInheritDetail},
		{axisOptions, axisOptionsDetail},
		{axisIUSE, axisIUSEDetail},
		{axisDeps, axisDepsDetail},
	}

	var findings []AxisFinding
	for _, axis := range axes {
		if detail := axis.detail(ours, baseline); detail != "" {
			findings = append(findings, AxisFinding{Axis: axis.name, Detail: detail})
		}
	}
	return findings, nil
}

// axisEbuild is one ebuild reduced to the four axes, and to nothing else.
type axisEbuild struct {
	// inherit is the eclasses named on the ebuild's inherit lines, as written.
	inherit []string
	// options is the name side of every `-D…=` the ebuild passes.
	options []string
	// iuse is the USE flags of IUSE, as written — a leading `+` is part of the
	// flag here, because a default that changed is a decision like any other.
	iuse []string
	// deps maps a category/package atom to the dependency specs written for it.
	// Grouping by the atom is what lets one finding say "ours 6.5.0, ::gentoo
	// 6.7.0" instead of two saying that a string appeared and another vanished.
	deps map[string][]string
}

// readAxisEbuild reads one ebuild and extracts the four axes from its text.
//
// It is a reader of ebuild TEXT and never a shell: nothing is expanded,
// evaluated or executed, so an ebuild that would delete the tree when sourced is
// just a file with some lines in it here.
func readAxisEbuild(path string) (axisEbuild, error) {
	data, err := os.ReadFile(path) //nolint:gosec // the ebuild to compare is the caller's whole request; the path is a baseline or overlay ebuild resolved from a directory listing, never from registry input
	if err != nil {
		return axisEbuild{}, fmt.Errorf("%s: %w", path, err)
	}

	read := axisEbuild{deps: make(map[string][]string)}
	lines := axisLogicalLines(string(data))
	for i := 0; i < len(lines); i++ {
		line := lines[i]
		trimmed := strings.TrimSpace(line)
		// A wholly commented line is skipped, following the option reader
		// internal/autoupdate/validate already ships: an option removed upstream
		// often survives as a commented `-Dfoo=` for a release or two, and
		// counting it would produce a finding about an argument the build never
		// receives. Only a FULL-line comment is dropped — a mid-line `#` can be a
		// parameter expansion like ${x#y}.
		if strings.HasPrefix(trimmed, "#") {
			continue
		}

		// Options are scanned on every line, because an ebuild passes them from
		// anywhere: an emesonargs array, an econf call, a src_configure body.
		read.options = axisCollectOptions(line, read.options)

		if rest, isInherit := axisAfterWord(trimmed, "inherit"); isInherit {
			read.inherit = append(read.inherit, axisWords(rest)...)
			continue
		}

		name, value, last, isVar := axisVarBlock(lines, i)
		if !isVar {
			continue
		}
		// The block was read whole, so its remaining lines are not visited again.
		i = last
		if name == axisIUSEVar {
			read.iuse = append(read.iuse, axisFlags(value)...)
			continue
		}
		for _, spec := range axisDepTokens(value) {
			atom, _, ok := axisDepAtom(spec)
			if !ok {
				continue
			}
			read.deps[atom] = append(read.deps[atom], spec)
		}
	}
	return read, nil
}

// axisLogicalLines splits ebuild text into LOGICAL lines: a line ending in a
// backslash is joined to the one after it, which is how a shell reads it.
//
// Without the join, an `inherit meson \` wrapped onto a second line would report
// an eclass we plainly do inherit as one we dropped — a finding invented out of
// where the author happened to wrap the line, on the one axis this file exists
// to get right.
//
// A comment never continues. A trailing backslash inside a `#` line is text, not
// a continuation, and joining there would swallow the next line — which could be
// the inherit line itself.
func axisLogicalLines(text string) []string {
	raw := strings.Split(text, "\n")
	lines := make([]string, 0, len(raw))

	var joined strings.Builder
	for _, line := range raw {
		line = strings.TrimSuffix(line, "\r")
		continued := strings.HasSuffix(line, `\`) &&
			!strings.HasSuffix(line, `\\`) &&
			!strings.HasPrefix(strings.TrimSpace(line), "#")
		if continued {
			joined.WriteString(strings.TrimSuffix(line, `\`))
			joined.WriteString(" ")
			continue
		}
		if joined.Len() > 0 {
			joined.WriteString(line)
			lines = append(lines, joined.String())
			joined.Reset()
			continue
		}
		lines = append(lines, line)
	}
	if joined.Len() > 0 {
		// A file whose last line continues into nothing. Kept rather than
		// dropped: the text it carries is still the ebuild's.
		lines = append(lines, joined.String())
	}
	return lines
}

// axisAfterWord returns what follows word at the start of line, when line really
// begins with that word and not merely with those letters — `inherit_foo` and
// `inheritance` are not inherit lines.
func axisAfterWord(line, word string) (string, bool) {
	rest, ok := strings.CutPrefix(line, word)
	if !ok {
		return "", false
	}
	if rest == "" {
		// A bare `inherit` names no eclass. It is still an inherit line, and it
		// contributes nothing.
		return "", true
	}
	if rest[0] != ' ' && rest[0] != '\t' {
		return "", false
	}
	return rest, true
}

// axisWords splits the tail of a line into words, stopping at a trailing
// comment. An eclass name carries no `#`, so a word starting with one ends the
// list rather than joining it.
func axisWords(rest string) []string {
	var words []string
	for _, word := range strings.Fields(rest) {
		if strings.HasPrefix(word, "#") {
			break
		}
		words = append(words, word)
	}
	return words
}

// axisCollectOptions appends the name side of every `-D…=` on one line.
//
// THE DEFINITION, stated here because a second reader must count the same way:
// an option passed is the text between a `-D` that STARTS an argument and the
// FIRST `=` after it, on a line that is not wholly a comment; the axis compares
// the DISTINCT such names, so an option written twice is one option.
//
// It is the definition internal/autoupdate/validate.OptionsFromEbuild already
// uses, re-stated rather than imported because internal/overlay imports nothing
// from internal/autoupdate and keeps it that way on purpose (D8b).
//
// Only the name side is taken. Values are irrelevant to this comparison and
// ignoring them is what keeps it quiet: `-Dexamples=$(usex test enabled
// disabled)` is an ordinary line whose NAME is perfectly static.
//
// A name assembled at runtime — `-D${plugin}=` — is kept AS WRITTEN rather than
// dropped. It is an option the build receives, and an option list that quietly
// omitted the ones it could not resolve would under-count the very thing this
// axis measures: how much we are maintaining by hand.
func axisCollectOptions(text string, into []string) []string {
	for i := 0; i+1 < len(text); i++ {
		if text[i] != '-' || text[i+1] != 'D' {
			continue
		}
		// The character before must not be part of a longer word, so `--Dfoo=`
		// and `x-Dfoo=` are not read as options.
		if i > 0 && !axisIsArgBoundary(text[i-1]) {
			continue
		}

		rest := text[i+2:]
		eq := strings.IndexByte(rest, '=')
		if eq < 0 {
			// No `=` left on the line: a `-DNDEBUG` in CFLAGS, or a wrapped
			// argument. There is no name/value pair to compare, and there will
			// be none further along either.
			break
		}
		name := rest[:eq]
		i += 2 + eq
		if name == "" {
			// `-D=value` names nothing.
			continue
		}
		into = append(into, name)
	}
	return into
}

// axisIsArgBoundary reports whether c can precede an argument on a command line.
func axisIsArgBoundary(c byte) bool {
	switch c {
	case ' ', '\t', '(', '"', '\'', '`':
		return true
	}
	return false
}

// axisIUSEVar is the flag variable, named as a constant because readAxisEbuild
// branches on it.
const axisIUSEVar = "IUSE"

// axisVarNames are the assignments this reader recognises: the flag variable and
// every dependency variable an EAPI 8 ebuild may write.
//
// The list is CLOSED, and that is not tidiness. A recognised assignment consumes
// the lines it spans, and a consumed line is one the option scan no longer sees;
// swallowing every assignment would hide a `-D` inside a multi-line SRC_URI or a
// src_configure body, and an option that goes uncounted is a false clean on the
// axis that measures 85 against 2.
var axisVarNames = []string{axisIUSEVar, "DEPEND", "RDEPEND", "BDEPEND", "PDEPEND", "IDEPEND"}

// axisVarBlock reads the assignment starting at lines[i], when it is one of
// axisVarNames, and returns its value with the index of the LAST line consumed.
//
// The name is matched at the START of the line, so RDEPEND is never mistaken for
// DEPEND — a substring search here would fold every RDEPEND into the DEPEND
// answer and report the first one it met as both.
func axisVarBlock(lines []string, i int) (name, value string, last int, ok bool) {
	trimmed := strings.TrimSpace(lines[i])
	for _, candidate := range axisVarNames {
		rest, found := strings.CutPrefix(trimmed, candidate)
		if !found {
			continue
		}
		// `VAR+=` appends and is as much an assignment as `VAR=`.
		rest = strings.TrimPrefix(rest, "+")
		body, isAssignment := strings.CutPrefix(rest, "=")
		if !isAssignment {
			// IUSE_EXPAND and friends: the letters match, the assignment does not.
			continue
		}
		read, lastLine := axisVarValue(lines, i, body)
		return candidate, read, lastLine, true
	}
	return "", "", i, false
}

// axisVarValue reads a possibly multi-line quoted value, returning it flattened
// onto one line together with the index of the last line it consumed.
func axisVarValue(lines []string, i int, body string) (string, int) {
	if body == "" {
		return "", i
	}

	quote := body[0]
	if quote != '"' && quote != '\'' {
		// An unquoted value is a single word by construction — the shell would
		// have split it otherwise.
		return body, i
	}

	rest := body[1:]
	if end := strings.IndexByte(rest, quote); end >= 0 {
		return rest[:end], i
	}

	var value strings.Builder
	value.WriteString(rest)
	for j := i + 1; j < len(lines); j++ {
		value.WriteString(" ")
		if end := strings.IndexByte(lines[j], quote); end >= 0 {
			value.WriteString(lines[j][:end])
			return value.String(), j
		}
		value.WriteString(lines[j])
	}
	// A quote that never closes. What was read is returned rather than
	// discarded: this reads text to compare it, and refusing to answer over a
	// malformed ebuild would cost a package its cheapest findings.
	return value.String(), len(lines) - 1
}

// axisFlags splits an IUSE value into flags.
//
// A token carrying a `$` is skipped: it names no flag, it names a variable this
// reader does not expand, and reporting `${PYTHON_USEDEP}` as "a flag we added"
// would be a wrong statement about a flag. The difference is not lost — the
// content diff still sees the line.
func axisFlags(value string) []string {
	var flags []string
	for _, token := range strings.Fields(value) {
		if strings.Contains(token, "$") {
			continue
		}
		flags = append(flags, token)
	}
	return flags
}

// axisDepTokens splits a dependency value into the specs written in it, dropping
// the shell and USE-conditional scaffolding that holds them together: `(`, `)`,
// `||`, a `use?` guard, and any token built out of a variable.
//
// The conditional structure is deliberately flattened. This axis answers "which
// packages do the two ebuilds name, and with which constraints" — under which
// USE combination is a question for the content diff, not for a set comparison.
func axisDepTokens(value string) []string {
	var specs []string
	for _, token := range strings.Fields(value) {
		if token == "(" || token == ")" || token == "||" {
			continue
		}
		if strings.HasSuffix(token, "?") || strings.Contains(token, "$") {
			continue
		}
		specs = append(specs, token)
	}
	return specs
}

// axisDepAtom splits a dependency spec into the category/package it names and
// the version written on it, and reports whether it is a spec at all.
//
// `>=dev-qt/qtbase-6.7.0:6` is dev-qt/qtbase at 6.7.0. The slot, the subslot and
// the USE dependencies qualify the spec rather than name a different package, so
// they are cut here and survive on the spec as written.
func axisDepAtom(spec string) (atom, version string, ok bool) {
	// Leading operators and blockers: >=, <=, ~, =, !, !!.
	body := strings.TrimLeft(spec, "!<>=~")
	if cut := strings.IndexAny(body, ":["); cut >= 0 {
		body = body[:cut]
	}

	slash := strings.IndexByte(body, '/')
	if slash <= 0 || slash == len(body)-1 {
		// No category, or nothing after it. Not an atom, so not a dependency
		// this axis can say anything about.
		return "", "", false
	}

	atom, version = body, ""
	// The version starts at the first `-` followed by a digit AFTER the slash.
	// Starting after the slash is what keeps a category like dev-qt whole.
	for i := slash + 1; i+1 < len(body); i++ {
		if body[i] == '-' && body[i+1] >= '0' && body[i+1] <= '9' {
			atom, version = body[:i], strings.TrimSuffix(body[i+1:], "*")
			break
		}
	}
	return atom, version, true
}

// axisInheritDetail is the finding for the eclass set — see axisInherit for why
// this one is the whole of issue #33.
func axisInheritDetail(ours, baseline axisEbuild) string {
	return axisChangeDetail(axisSetDiff(ours.inherit, baseline.inherit))
}

// axisIUSEDetail is the finding for the flag set.
func axisIUSEDetail(ours, baseline axisEbuild) string {
	return axisChangeDetail(axisSetDiff(ours.iuse, baseline.iuse))
}

// axisOptionsDetail is the finding for the build options, and it leads with the
// two COUNTS because that is the shape the measurement took: 85 against 2 is a
// finding before anybody reads a single option name.
func axisOptionsDetail(ours, baseline axisEbuild) string {
	// The counts and the difference are taken from the SAME two sets, so a
	// finding cannot quote a total that disagrees with the names beside it.
	ourNames, baseNames := axisUnique(ours.options), axisUnique(baseline.options)
	added, dropped := axisSetDiff(ourNames, baseNames)
	if len(added) == 0 && len(dropped) == 0 {
		return ""
	}

	detail := fmt.Sprintf("we pass %d -D options, %s passes %d",
		len(ourNames), axisBaselineLabel, len(baseNames))
	if len(added) > 0 {
		detail += fmt.Sprintf("; %d only ours (%s)", len(added), axisSample(added))
	}
	if len(dropped) > 0 {
		detail += fmt.Sprintf("; %d only %s (%s)", len(dropped), axisBaselineLabel, axisSample(dropped))
	}
	return detail
}

// axisDepsDetail is the finding for the dependency atoms, one clause per package
// whose specs differ.
//
// It names BOTH sides, because the direction is the finding: ours lower than
// ::gentoo's usually means we fell behind, and "the atoms differ" would leave
// the reader to open both files to learn which way.
func axisDepsDetail(ours, baseline axisEbuild) string {
	named := make(map[string]struct{}, len(ours.deps)+len(baseline.deps))
	for atom := range ours.deps {
		named[atom] = struct{}{}
	}
	for atom := range baseline.deps {
		named[atom] = struct{}{}
	}

	var clauses []string
	// Sorted, so the same pair of ebuilds reports the same sentence on every
	// host: map order would otherwise decide what the operator reads first.
	for _, atom := range slices.Sorted(maps.Keys(named)) {
		ourSpecs, baseSpecs := axisUnique(ours.deps[atom]), axisUnique(baseline.deps[atom])
		if slices.Equal(ourSpecs, baseSpecs) {
			continue
		}
		clauses = append(clauses, axisDepClause(atom, ourSpecs, baseSpecs))
	}
	if len(clauses) == 0 {
		return ""
	}

	if len(clauses) > axisNameSample {
		return fmt.Sprintf("%d atoms differ: %s, +%d more",
			len(clauses), strings.Join(clauses[:axisNameSample], "; "), len(clauses)-axisNameSample)
	}
	return strings.Join(clauses, "; ")
}

// axisDepClause says what happened to one package's dependency specs.
func axisDepClause(atom string, ourSpecs, baseSpecs []string) string {
	switch {
	case len(ourSpecs) == 0:
		return fmt.Sprintf("%s: only %s depends on it (%s)", atom, axisBaselineLabel, strings.Join(baseSpecs, " "))
	case len(baseSpecs) == 0:
		return fmt.Sprintf("%s: only we depend on it (%s)", atom, strings.Join(ourSpecs, " "))
	default:
		return fmt.Sprintf("%s: ours %s, %s %s%s", atom,
			strings.Join(ourSpecs, " "), axisBaselineLabel, strings.Join(baseSpecs, " "),
			axisDepDirection(ourSpecs, baseSpecs))
	}
}

// axisDepDirection names which side asks for the older version, and says nothing
// at all when it cannot tell.
//
// It answers only for the unambiguous shape — one spec on each side, both
// carrying a version this repository's own comparison can order. Two specs under
// different USE conditions, or a spec with no version, have no single direction,
// and inventing one would be a claim about a dependency nobody made.
func axisDepDirection(ourSpecs, baseSpecs []string) string {
	if len(ourSpecs) != 1 || len(baseSpecs) != 1 {
		return ""
	}
	_, ourVersion, ourOK := axisDepAtom(ourSpecs[0])
	_, baseVersion, baseOK := axisDepAtom(baseSpecs[0])
	if !ourOK || !baseOK {
		return ""
	}
	if !ebuild.IsValidVersion(ourVersion) || !ebuild.IsValidVersion(baseVersion) {
		return ""
	}

	switch cmp := ebuild.CompareVersions(ourVersion, baseVersion); {
	case cmp < 0:
		return " — ours is lower"
	case cmp > 0:
		return " — ours is higher"
	default:
		return ""
	}
}

// axisChangeDetail renders a set difference in the direction the report always
// reads: what WE added on top of ::gentoo, and what ::gentoo has that we
// dropped. "" when the two sets are the same, which is how an axis says it has
// nothing to report.
func axisChangeDetail(added, dropped []string) string {
	switch {
	case len(added) > 0 && len(dropped) > 0:
		return fmt.Sprintf("we dropped %s and added %s", axisSample(dropped), axisSample(added))
	case len(added) > 0:
		return fmt.Sprintf("we added %s", axisSample(added))
	case len(dropped) > 0:
		return fmt.Sprintf("we dropped %s", axisSample(dropped))
	default:
		return ""
	}
}

// axisSetDiff reports what ours has that baseline does not, and the reverse.
// Both come back sorted and deduplicated, so the same pair of ebuilds produces
// the same sentence on every host.
func axisSetDiff(ours, baseline []string) (added, dropped []string) {
	inOurs, inBaseline := axisMembers(ours), axisMembers(baseline)
	for _, name := range axisUnique(ours) {
		if _, held := inBaseline[name]; !held {
			added = append(added, name)
		}
	}
	for _, name := range axisUnique(baseline) {
		if _, held := inOurs[name]; !held {
			dropped = append(dropped, name)
		}
	}
	return added, dropped
}

// axisMembers is a set of the given names, for membership tests.
func axisMembers(names []string) map[string]struct{} {
	members := make(map[string]struct{}, len(names))
	for _, name := range names {
		members[name] = struct{}{}
	}
	return members
}

// axisUnique sorts and deduplicates. The count it yields is the count the
// findings quote: an option written twice is one option.
func axisUnique(names []string) []string {
	if len(names) == 0 {
		return nil
	}
	unique := slices.Clone(names)
	slices.Sort(unique)
	return slices.Compact(unique)
}

// axisSample lists names up to axisNameSample and counts the rest, so a
// difference of 85 is legible on one line and still reports its size (R2.4).
func axisSample(names []string) string {
	if len(names) <= axisNameSample {
		return strings.Join(names, ", ")
	}
	return fmt.Sprintf("%s, +%d more", strings.Join(names[:axisNameSample], ", "), len(names)-axisNameSample)
}
