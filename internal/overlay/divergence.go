package overlay

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"unicode"

	"github.com/obentoo/bentoolkit/internal/common/ebuild"
)

// divergenceTag is the marker that turns an ordinary comment into a
// declaration. It is matched as a PREFIX of the comment's text, so a sentence
// that merely mentions the tag is prose and stays prose.
//
// The convention is not invented here: sys-devel/binutils-2.47 already writes
// this reason in a comment, in prose, and nothing reads it (D5). The tag is
// what makes the comment machine-readable.
const divergenceTag = "BENTOO-DIVERGENCE:"

// divergenceDropWhenKey introduces the optional continuation line carrying the
// condition under which the divergence stops applying.
const divergenceDropWhenKey = "drop-when:"

// DeclaredDivergence is one `# BENTOO-DIVERGENCE:` tag read out of an ebuild's
// text: the reason a particular change exists, and when it stops applying.
//
// # It is NOT overlay.Divergence
//
// Divergence (compare.go:143) is the REGISTRY axis — Patched, Reason, Entry —
// supplied by the caller from packages.toml, one answer per package, feeding the
// `redundant` verdict. This one is the EBUILD axis: one answer per hunk, living
// beside the code it describes so it travels when the ebuild is copied to a new
// version, which is exactly when a divergence is silently inherited today (D5).
// Two names because they answer two questions; merging them would make a
// registry entry and a maintainer's note mean the same thing.
//
// _Requirements: R3, R3.1, R3.4_
type DeclaredDivergence struct {
	// Axis is the property the divergence is about, AS THE MAINTAINER WROTE IT.
	// §7 of the protocol spells it upper-case (`INHERIT:`) and D4's axes are
	// lower-case (`inherit`); neither side is authoritative, so nothing is
	// normalised here and every consumer compares it with strings.EqualFold.
	// Rewriting the word would also cost the report the maintainer's own
	// spelling for the sake of a comparison that is one call away.
	Axis string
	// Reason is the maintainer's words, verbatim and uncapped. A declaration
	// without one is what R3 exists to abolish, so an empty Reason never
	// reaches this field: the tag is reported as malformed instead.
	Reason string
	// DropWhen is the condition from the `drop-when:` continuation line, empty
	// when the declaration states none. Empty is the ordinary case — most
	// divergences have no end in sight — and it is NOT an error: inventing a
	// condition would retire a divergence nobody retired.
	DropWhen string
	// Expired reports that DropWhen has been checked against the ::gentoo tree
	// and is met. It is written by EvaluateDeclarations, never by the parser:
	// expiry is a question about the tree, and reading it off the ebuild's own
	// text would answer it from the wrong source (R3.3).
	Expired bool
}

// ParseDivergences reads one ebuild and returns the divergences it declares.
//
// The shape, as §7 proposed it and D5 adopted it:
//
//	# BENTOO-DIVERGENCE: INHERIT: gstreamer-meson does not handle the qt6 option list
//	#   drop-when: gentoo-version >= 1.29
//
// The second line is optional and must immediately follow the first. A
// declaration parsed here is reported as declared and is questioned no further
// (R3.1); an axis that carries no declaration is reported as undeclared (R3.4).
//
// # An ebuild is a bash script, not a list of lines
//
// The tag is recognised only on a line that is WHOLLY a comment and is neither
// inside a quoted string nor inside a heredoc body. That is not fussiness: an
// ebuild that echoes or installs its own documentation writes the tag as data,
// and a line-oriented matcher would read a package's own output back as policy.
// Every phantom declaration silences a real divergence — the whole filter is
// "declared divergences are quiet" (D7), so a tag invented out of quoted text
// buys a package permanent silence on an axis nobody ever declared.
//
// Nothing is expanded, evaluated or executed: an ebuild that would delete the
// tree when sourced is just a file with some lines in it here.
//
// # A malformed tag is reported, not dropped
//
// A tag somebody meant as a declaration and mistyped comes back as an error
// naming the file, the line and the text, while the well-formed declarations
// beside it are still returned — buildDivergenceMap's precedent, whose own
// comment says one bad key must not blank the whole map. Silently dropping it
// would read as "this divergence was never declared", and the maintainer would
// be asked to declare it again on every run, forever.
//
// _Requirements: R3, R3.1, R3.4_
func ParseDivergences(ebuildPath string) ([]DeclaredDivergence, error) {
	data, err := os.ReadFile(ebuildPath) //nolint:gosec // the ebuild to read is the caller's whole request; the path is an overlay ebuild resolved from a directory listing, never from registry input
	if err != nil {
		return nil, fmt.Errorf("reading the ebuild to parse its divergence declarations: %w", err)
	}
	return divergencesInText(ebuildPath, string(data))
}

// divergencesInText is ParseDivergences over text already read, so the walk can
// be exercised without a file and the file error has exactly one origin.
//
// The error it folds together is one entry per malformed tag: a file with three
// broken tags names all three, because a maintainer fixing them wants the list
// and not the first one over and over.
func divergencesInText(path, text string) ([]DeclaredDivergence, error) {
	var (
		declared  []DeclaredDivergence
		malformed []error
		shell     divergenceShell
	)

	lines := divergencePhysicalLines(text)
	for i := 0; i < len(lines); i++ {
		line := lines[i]

		// Heredoc body first: its lines are data the script writes somewhere,
		// and a `#` opening one of them is part of that data.
		if shell.inHeredoc() {
			shell.readHeredocBody(line)
			continue
		}
		// A string still open from an earlier line: same reasoning, and the
		// scan continues only to find where the string closes.
		if shell.inString() {
			shell.readCode(line)
			continue
		}

		comment, isComment := divergenceComment(line)
		if !isComment {
			// Code, and the only place a heredoc or a multi-line string can
			// begin.
			shell.readCode(line)
			continue
		}

		tail, isTag := divergenceCutFold(comment, divergenceTag)
		if !isTag {
			continue
		}

		axis, reason, wellFormed := divergenceAxisReason(tail)
		if !wellFormed {
			malformed = append(malformed, fmt.Errorf(
				"%s:%d: %q declares no axis and no reason; want `# %s <axis>: <reason>`",
				path, i+1, strings.TrimSpace(line), divergenceTag))
			continue
		}

		declaration := DeclaredDivergence{Axis: axis, Reason: reason}
		if i+1 < len(lines) {
			if condition, stated := divergenceDropWhen(lines[i+1]); stated {
				declaration.DropWhen = condition
				// The continuation belongs to this declaration and is not
				// offered to the next iteration as a tag of its own.
				i++
			}
		}
		declared = append(declared, declaration)
	}

	return declared, errors.Join(malformed...)
}

// divergencePhysicalLines splits ebuild text into the lines the file actually
// has, with any CRLF ending trimmed.
//
// PHYSICAL lines, deliberately, where axisLogicalLines joins a trailing
// backslash into the line after it. Three reasons that reader does not fit
// here: a malformed tag is reported with its LINE NUMBER, and the join destroys
// the numbering; a heredoc body is delimited by physical lines, so joining
// inside one would swallow its terminator; and this parser's continuation is a
// second COMMENT line, a different mechanism entirely — that reader explicitly
// never continues a comment.
func divergencePhysicalLines(text string) []string {
	lines := strings.Split(text, "\n")
	for i, line := range lines {
		lines[i] = strings.TrimSuffix(line, "\r")
	}
	return lines
}

// divergenceComment returns the text of a WHOLE-LINE comment, without its `#`
// and the blanks around it, and reports whether the line is one.
//
// Only a whole-line comment counts. A mid-line `#` is either a trailing comment
// on a command or a parameter expansion like ${x#y}, and neither introduces a
// declaration — the same rule readAxisEbuild applies for the same reason.
func divergenceComment(line string) (string, bool) {
	rest, isComment := strings.CutPrefix(strings.TrimSpace(line), "#")
	if !isComment {
		return "", false
	}
	return strings.TrimSpace(rest), true
}

// divergenceAxisReason splits a tag's tail into `<axis>: <reason>`.
//
// The split is on the FIRST colon, so a reason may contain as many more as the
// maintainer needs — "see bug 942071: the eclass lost the option" survives
// whole. Both halves must carry something: an axis with no reason is the
// undocumented divergence R3 exists to abolish, and a reason with no axis names
// nothing the report can attach it to.
func divergenceAxisReason(tail string) (axis, reason string, ok bool) {
	axis, reason, found := strings.Cut(tail, ":")
	axis, reason = strings.TrimSpace(axis), strings.TrimSpace(reason)
	if !found || axis == "" || reason == "" {
		return "", "", false
	}
	return axis, reason, true
}

// divergenceDropWhen returns the condition on a `drop-when:` continuation line.
//
// The condition comes back VERBATIM and unparsed. Which predicates are
// machine-checkable is D6's question and EvaluateDropWhen's job; a parser that
// rejected the prose ones here would delete exactly the conditions a human must
// check, which is the case D6 says matters most.
//
// A key with nothing after it states no condition and is treated as no
// continuation at all, because "no end in sight" is already the legal and
// ordinary case — reporting it as malformed would flag a declaration as broken
// for saying what most of them say.
func divergenceDropWhen(line string) (string, bool) {
	comment, isComment := divergenceComment(line)
	if !isComment {
		return "", false
	}
	condition, isDropWhen := divergenceCutFold(comment, divergenceDropWhenKey)
	if !isDropWhen {
		return "", false
	}
	condition = strings.TrimSpace(condition)
	return condition, condition != ""
}

// divergenceCutFold is strings.CutPrefix with a case-insensitive comparison.
//
// The vocabulary is written in two cases in the two places that define it — §7
// spells the tag and the axis upper-case, D4 and D6 spell the axes and the key
// lower-case — and neither is authoritative. Matching either way costs nothing
// and keeps a maintainer's `# Bentoo-Divergence:` from being read as prose,
// which would report a declared divergence as undeclared (R3.4).
func divergenceCutFold(s, prefix string) (string, bool) {
	if len(s) < len(prefix) || !strings.EqualFold(s[:len(prefix)], prefix) {
		return "", false
	}
	return s[len(prefix):], true
}

// divergenceShell is the small amount of bash a declaration reader must
// understand, and not one line more: whether the text in front of it is code, a
// string's content, or a heredoc's body.
//
// It parses nothing else about the script — no commands, no variables, no
// control flow — because the only question it answers is whether a `#` on this
// line opens a comment or is somebody's data.
type divergenceShell struct {
	// quote is the quote character an unclosed string opened on an earlier
	// line, or 0 when the reader is not inside one.
	quote byte
	// heredoc is the delimiter that will end the body being read, or "" when
	// the reader is not inside a heredoc.
	heredoc string
	// heredocStripsTabs records the `<<-` form, whose terminator may be
	// indented with tabs — which is how every heredoc inside a function body
	// is written, including the one this parser was tested against.
	heredocStripsTabs bool
}

// inHeredoc reports whether the next line is heredoc body.
func (s *divergenceShell) inHeredoc() bool { return s.heredoc != "" }

// inString reports whether the next line continues an unclosed string.
func (s *divergenceShell) inString() bool { return s.quote != 0 }

// readHeredocBody consumes one line of body, closing the heredoc when the line
// is its delimiter.
func (s *divergenceShell) readHeredocBody(line string) {
	closing := line
	if s.heredocStripsTabs {
		closing = strings.TrimLeft(closing, "\t")
	}
	// Trailing blanks are forgiven on the delimiter line. Bash is stricter, but
	// a heredoc this reader never closes would swallow every declaration below
	// it, and reporting a declared divergence as undeclared is the more
	// expensive way to be wrong (R3.4).
	if strings.TrimRight(closing, " \t") == s.heredoc {
		s.heredoc = ""
		s.heredocStripsTabs = false
	}
}

// readCode advances the string and heredoc state over one line of script.
//
// Left to right, one byte at a time, because that is the only order in which
// `"`, `'`, `\`, `#` and `<<` mean what the shell says they mean: the `#` in
// `echo "# BENTOO-DIVERGENCE: ..."` is quoted precisely because a `"` came
// before it.
func (s *divergenceShell) readCode(line string) {
	opened, stripsTabs := "", false

	for i := 0; i < len(line); {
		c := line[i]

		if s.quote != 0 {
			// A backslash escapes inside "…" and is literal inside '…'.
			if s.quote == '"' && c == '\\' {
				i += 2
				continue
			}
			if c == s.quote {
				s.quote = 0
			}
			i++
			continue
		}

		switch {
		case c == '\\':
			i += 2
			continue
		case c == '\'' || c == '"':
			s.quote = c
		case c == '#' && (i == 0 || line[i-1] == ' ' || line[i-1] == '\t'):
			// From here the line is a trailing comment: it opens no string and
			// no heredoc. The blank in front is what separates it from ${x#y},
			// which is an expansion and not a comment at all.
			i = len(line)
			continue
		case c == '<' && i+1 < len(line) && line[i+1] == '<':
			word, tabs, next, isHeredoc := divergenceHeredocWord(line, i)
			// Only the first heredoc on a line is tracked. `cat <<A <<B` queues
			// two bodies, and following the second would need a queue for a
			// shape no ebuild writes.
			if isHeredoc && opened == "" {
				opened, stripsTabs = word, tabs
			}
			i = next
			continue
		}
		i++
	}

	// The body starts on the NEXT line, so the whole of this one is read for
	// quotes first — `cat <<-EOF > "${D}/usr/share/doc/notes"` opens a heredoc
	// AND closes a string, in that order.
	if opened != "" {
		s.heredoc, s.heredocStripsTabs = opened, stripsTabs
	}
}

// divergenceHeredocWord reads the `<<` or `<<-` redirection starting at i and
// returns the delimiter that will end its body, whether it is the tab-stripping
// form, and where scanning resumes.
//
// The delimiter must follow the operator IMMEDIATELY. Bash permits a blank
// between them, but `$(( 1 << n ))` looks exactly like that, and a shift
// mistaken for a heredoc opens a body that never ends — silencing every
// declaration below it in the file. Requiring the word to be attached costs a
// spelling nobody uses and removes that whole class of false silence.
func divergenceHeredocWord(line string, i int) (word string, stripsTabs bool, next int, ok bool) {
	rest := line[i+2:]
	next = i + 2

	if strings.HasPrefix(rest, "<") {
		// `<<<` is a here-STRING: its content is on this line and it opens no
		// body at all.
		return "", false, next + 1, false
	}
	if strings.HasPrefix(rest, "-") {
		stripsTabs = true
		rest = rest[1:]
		next++
	}

	end := strings.IndexAny(rest, " \t;&|<>()")
	if end < 0 {
		end = len(rest)
	}
	raw := rest[:end]
	next += end
	if raw == "" || !divergenceDelimiterStart(raw[0]) {
		return "", false, next, false
	}

	// `<<'EOF'`, `<<"EOF"` and `<<\EOF` all end at a line reading EOF: the
	// quoting says the body is not expanded, and says nothing about the word.
	word = strings.Map(func(r rune) rune {
		if r == '\'' || r == '"' || r == '\\' {
			return -1
		}
		return r
	}, raw)
	return word, stripsTabs, next, word != ""
}

// divergenceDelimiterStart reports whether c can open a heredoc delimiter word.
// A digit is excluded on purpose: `1<<3` is arithmetic, and bash's willingness
// to accept a numeric delimiter is not worth the heredoc it would invent.
func divergenceDelimiterStart(c byte) bool {
	switch {
	case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z':
		return true
	case c == '_' || c == '\'' || c == '"' || c == '\\':
		return true
	default:
		return false
	}
}

// The CLOSED vocabulary of exit conditions (D6): three predicates, each of them
// answerable by reading the local ::gentoo tree and nothing else, and each of
// them a question about ONE PACKAGE rather than about ::gentoo at large — which
// is why the atom travels with the condition through everything below.
//
// Closed on purpose. A vocabulary that grew by working out what a sentence
// probably meant would sooner or later decide, silently, that a divergence
// nobody retired is over. Refusing is the whole value of this evaluator.
//
// They are named `vocab*` rather than `dropWhen*` like everything else in this
// half of the file, and the reason is worth writing down because it is invisible
// and it will happen again. gosec's G101 matches the IDENTIFIER against
// `(?i)…|pw|…`, and `dropWhen` contains "pW" — the p of "drop" against the W of
// "When". Any constant so named whose value is a string long enough to clear
// G101's entropy threshold reads as a hardcoded credential and fails the CI
// lint; `gentoo-has-package` is the one of the three that is long enough, which
// is why only it was flagged and why renaming the SUFFIX changed nothing.
//
// Fixed by the name rather than by a //nolint, for the reason filesdirMarker
// gives in authorship.go: a suppression is something every later reader has to
// re-evaluate, and these are three public vocabulary keywords.
const (
	// vocabVersionWord asks whether ::gentoo now ships a release at least as
	// new as the one named. It is D5's worked case: sys-devel/binutils-2.47 says
	// in prose that our ebuild goes away as soon as ::gentoo ships 2.47.
	vocabVersionWord = "gentoo-version"
	// vocabHasPackageWord asks whether ::gentoo carries the package at all. It
	// takes no argument — the package is the one the declaration was read out of.
	vocabHasPackageWord = "gentoo-has-package"
	// vocabInheritsWord asks whether ::gentoo's own ebuild inherits an eclass.
	// It is issue #33 read backwards: the divergence exists because the eclass
	// did not carry the option list, and it ends when ::gentoo's ebuild inherits
	// one that does.
	vocabInheritsWord = "gentoo-inherits"
)

// dropWhenAtLeast is the only comparison `gentoo-version` admits.
//
// Only `>=`, because it is the only one the vocabulary defines and the only one
// the end of a divergence is ever stated with — "drop this when ::gentoo reaches
// X". A `>` or a `==` would each need a decision about what it means against a
// tree carrying a dozen versions, and inventing that decision here is precisely
// the guess this file refuses: anything else is reported unevaluated instead.
const dropWhenAtLeast = ">="

// EvaluateDropWhen answers a declaration's exit condition against the local
// ::gentoo tree — and says whether it answered it at all (R3.2).
//
// # `checkable` is an answer, not an error
//
// met=false says the condition WAS evaluated and is not met: the divergence
// still earns its place. checkable=false says it was NOT EVALUATED — the
// condition is outside the vocabulary above, or the tree could not answer it.
// Both come back with met=false and only `checkable` tells them apart, which is
// why it is a return value rather than an error: a caller that dropped it would
// collapse "a human must check this" into "we checked, and no".
//
// Both collapses are silent and both are expensive (D6). Read as unmet, a prose
// condition keeps its divergence alive forever, because nothing will ever retire
// it. Read as met, it retires the divergence with no cause at all, and work
// somebody did deliberately rejoins the realignment queue.
//
// It reads one directory listing and at most one ebuild, both inside gentooTree.
// It starts no process, resolves no host and never consults git: the local
// /var/db/repos/gentoo is a shallow clone whose history is absent by
// construction, so an evaluator reaching for `git log` would be answering from a
// source that is not there (R1.4). The answer is in the tree's content.
//
// _Requirements: R3, R3.2_
func EvaluateDropWhen(condition, gentooTree, atom string) (met, checkable bool) {
	keyword, argument, stated := dropWhenPredicate(condition)
	if !stated {
		// No condition at all — the ordinary case, since most divergences state
		// no end. There is nothing here to evaluate, and a divergence that stated
		// no end has certainly not ended.
		return false, false
	}

	switch {
	case strings.EqualFold(keyword, vocabVersionWord):
		return dropWhenVersionMet(argument, gentooTree, atom)
	case strings.EqualFold(keyword, vocabHasPackageWord):
		return dropWhenHasPackageMet(argument, gentooTree, atom)
	case strings.EqualFold(keyword, vocabInheritsWord):
		return dropWhenInheritsMet(argument, gentooTree, atom)
	default:
		// Free prose. Reported verbatim by whoever prints the declaration, and
		// evaluated by nobody.
		return false, false
	}
}

// EvaluateDeclarations returns the declarations with Expired decided against the
// local ::gentoo tree.
//
// A declaration is EXPIRED when its condition was evaluated and is met, and in
// no other case (R3.3). An unmet condition leaves the divergence declared,
// because it still earns its place; a condition the vocabulary does not cover
// leaves it declared too, because nothing was evaluated that could retire it.
//
// Expiry is what puts a divergence back in front of the model — an expired
// declaration is treated as UNDECLARED from then on, where a declared and
// unexpired one is never sent (D7) — so the field is written here, in
// production. Left to each reader to derive from `met && checkable`, the one
// reader that got it wrong would silence a divergence or resurrect one, and the
// struct would carry a field nothing ever set.
//
// Everything else comes back VERBATIM: the axis, the reason and the condition
// are the maintainer's words, and this pass reads the tree and reports rather
// than editing anybody's ebuild (R3.6). The input slice is not modified.
//
// _Requirements: R3, R3.2, R3.3_
func EvaluateDeclarations(declared []DeclaredDivergence, gentooTree, atom string) []DeclaredDivergence {
	evaluated := slices.Clone(declared)
	for i := range evaluated {
		met, checkable := EvaluateDropWhen(evaluated[i].DropWhen, gentooTree, atom)
		// Written unconditionally, including back to false. Expiry is a question
		// about the tree, and whatever the field arrived carrying was not an
		// answer to it.
		evaluated[i].Expired = met && checkable
	}
	return evaluated
}

// dropWhenPredicate splits a condition into its leading keyword and whatever
// follows it, and reports whether there was anything at all to split.
//
// The keyword is the first WORD, so the vocabulary is spelled with blanks
// between its parts exactly as D6 writes it. `gentoo-version>=2.47` is therefore
// prose and is reported as prose rather than repaired — a reader that repaired
// one spelling would have to decide how far to go, and every step past the first
// is a guess about what somebody meant.
func dropWhenPredicate(condition string) (keyword, argument string, stated bool) {
	trimmed := strings.TrimSpace(condition)
	if trimmed == "" {
		return "", "", false
	}
	end := strings.IndexFunc(trimmed, unicode.IsSpace)
	if end < 0 {
		return trimmed, "", true
	}
	return trimmed[:end], strings.TrimSpace(trimmed[end:]), true
}

// dropWhenVersionMet answers `gentoo-version >= X`: does ::gentoo ship a release
// of this package at least as new as X?
//
// The argument has to be a version this repository's own comparison can order.
// "two-point-forty-seven" is not one, and it comes back UNEVALUATED rather than
// as a quiet false: a vocabulary keyword carrying an argument nobody can read is
// exactly the line a human must look at, and working out what it meant is the
// guess this function exists to refuse.
//
// # A live ebuild ships nothing
//
// Live ebuilds are excluded, and that is the difference between this predicate
// working and being meaningless. ::gentoo's sys-devel/binutils — D5's own worked
// case — carries binutils-9999.ebuild, which builds from git master and orders
// ABOVE every release there is. Counted, it would satisfy `gentoo-version >= X`
// for every X anybody could write, retiring each such divergence on the day it
// was declared. The question is what ::gentoo SHIPS.
func dropWhenVersionMet(argument, gentooTree, atom string) (met, checkable bool) {
	wanted, isAtLeast := strings.CutPrefix(argument, dropWhenAtLeast)
	wanted = strings.TrimSpace(wanted)
	if !isAtLeast || !ebuild.IsValidVersion(wanted) {
		return false, false
	}

	_, carried, examined := dropWhenCarried(gentooTree, atom)
	if !examined {
		return false, false
	}
	for _, release := range dropWhenReleases(carried) {
		if ebuild.CompareVersions(release.version, wanted) >= 0 {
			return true, true
		}
	}
	return false, true
}

// dropWhenHasPackageMet answers `gentoo-has-package`: does ::gentoo carry this
// package at all?
//
// It takes NO argument, the package being the one the declaration was read out
// of. A trailing word is therefore not a second package to ask about but a
// spelling nobody defined, and it is reported unevaluated rather than answered
// about the wrong package.
//
// Live ebuilds DO count here, where dropWhenVersionMet excludes them: a package
// ::gentoo carries only as a live ebuild is still a package ::gentoo carries.
// The two predicates ask different questions, and they differ here on purpose.
func dropWhenHasPackageMet(argument, gentooTree, atom string) (met, checkable bool) {
	if argument != "" {
		return false, false
	}

	_, carried, examined := dropWhenCarried(gentooTree, atom)
	if !examined {
		return false, false
	}
	return len(carried) > 0, true
}

// dropWhenInheritsMet answers `gentoo-inherits <eclass>`: does ::gentoo's own
// ebuild for this package inherit that eclass?
//
// It reads ONE ebuild — the baseline dropWhenBaselineEbuild picks — through the
// very reader the inherit axis uses, so what an ebuild inherits has one answer
// in this package rather than two that can drift apart.
//
// The eclass must be exactly one word. `gentoo-inherits meson python-any-r1`
// names two, and whether the maintainer meant both of them or either of them is
// written down nowhere; it is reported unevaluated instead of decided here. A
// `gentoo-inherits` naming none is the same refusal for the same reason.
func dropWhenInheritsMet(eclass, gentooTree, atom string) (met, checkable bool) {
	if len(strings.Fields(eclass)) != 1 {
		return false, false
	}

	dir, carried, examined := dropWhenCarried(gentooTree, atom)
	if !examined {
		return false, false
	}
	if len(carried) == 0 {
		// ::gentoo carries no ebuild for this package, so there is none that
		// inherits anything. The tree was read and the answer is no — which is
		// not the same as not having been able to look.
		return false, true
	}

	read, err := readAxisEbuild(filepath.Join(dir, dropWhenBaselineEbuild(carried).filename))
	if err != nil {
		// The ebuild is there and will not read. "We could not look" is not "it
		// does not inherit that", so nothing is evaluated — the same line
		// Baseline.Unexamined draws for the same reason (D2).
		return false, false
	}
	// Exact match: an eclass name is a filename in ::gentoo's eclass/ directory,
	// so its case is part of it — unlike the tag and the axis word, which two
	// documents spell two ways and which are therefore folded.
	return slices.Contains(read.inherit, eclass), true
}

// dropWhenCarried lists what ::gentoo carries for atom, and reports whether the
// tree was READ at all.
//
// examined=false is "we could not look": no tree was given, the atom names no
// package, or a directory that is there would not list. It is kept apart from
// "::gentoo does not carry this package", which lists nothing and is a perfectly
// good answer — 84 of the overlay's 321 packages are in that state, and
// reporting them as unexaminable would hand a human 84 conditions the tool had
// just evaluated. ResolveBaseline draws the same line for the same reason.
func dropWhenCarried(gentooTree, atom string) (dir string, carried []carriedEbuild, examined bool) {
	if gentooTree == "" {
		return "", nil, false
	}
	category, pkg, err := splitBaselineAtom(atom)
	if err != nil {
		// Not one package, so there is no directory to look in. The error text is
		// not carried: this returns a state and not a diagnosis, and the caller's
		// answer is the same either way — unevaluated.
		return "", nil, false
	}

	dir = filepath.Join(gentooTree, category, pkg)
	entries, err := os.ReadDir(dir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return dir, nil, true
		}
		return dir, nil, false
	}
	return dir, carriedVersions(entries, category, pkg), true
}

// dropWhenReleases keeps the versions that are RELEASES, dropping the live
// ebuilds — see dropWhenVersionMet for what counting them would cost.
func dropWhenReleases(carried []carriedEbuild) []carriedEbuild {
	var releases []carriedEbuild
	for _, candidate := range carried {
		if !dropWhenLiveVersion(candidate.version) {
			releases = append(releases, candidate)
		}
	}
	return releases
}

// dropWhenLiveVersion reports whether a version is a live ebuild's: one that
// builds from the VCS rather than from a release, conventionally numbered 9999.
//
// ANY component counts, because ::gentoo writes both forms — binutils-9999
// follows git master and binutils-2.47.9999 follows the 2.47 branch, and neither
// is a release anyone can install a known version of. The convention is
// Portage's, and internal/autoupdate already reads it the same way; it is
// re-stated here rather than imported because internal/overlay imports nothing
// from that package and keeps it that way on purpose (D8b).
//
// It reads the components through versionComponents, so "9999-r1" and "9999_p1"
// are live too: that reader takes the numeric head and stops at the revision.
func dropWhenLiveVersion(version string) bool {
	for _, component := range versionComponents(version) {
		if component == 9999 || component == 99999999 {
			return true
		}
	}
	return false
}

// candidateExitPlaceholder is what a candidate's `drop-when:` line carries, and
// the fact that it is NOT a condition is the entire point of it.
//
// The tool knows THAT the two ebuilds differ; it does not know when the
// difference stops being wanted. That is a decision, and D6 says a condition
// nobody decided must never be evaluated: a machine-checkable one written here
// would be answered by EvaluateDropWhen on the very next run and would retire
// the divergence on the tool's own authority — "retired with no cause", arriving
// through the candidate generator instead of through a maintainer's ebuild.
//
// So it is deliberately outside the closed vocabulary. Pasted unedited, it comes
// back from EvaluateDropWhen as unevaluated, which is the report line that says a
// human must look — the honest answer while nobody has decided anything.
//
// It also names no axis word. `gentoo-inherits` would have been the obvious
// example to offer, and printing it on the OPTIONS candidate would have put the
// word "inherit" in a block about something else.
const candidateExitPlaceholder = "TODO: state when this divergence stops applying, or delete this line"

// candidateContinuation opens the second line of a candidate — the comment mark
// and the indent that sets it under the tag, in the shape §7 proposed and D5
// adopted: `#`, three blanks, then the key.
const candidateContinuation = "#   "

// CandidateDeclarations proposes one `# BENTOO-DIVERGENCE:` block per structural
// difference that no declaration covers (R3.5).
//
// On today's overlay this is the whole output: the registry declares ZERO
// divergences, so every difference in every package is undeclared and the first
// run's real product is not a realignment but a set of declarations somebody can
// accept. The second run is the cheap one.
//
// # It returns text, and writes nothing (R3.6)
//
// The blocks come back as strings for a human to paste. That is not tidiness: the
// overlay auto-commits and pushes within minutes, so a declaration this function
// wrote would be a declaration it PUBLISHED, with nobody having read it. R3.5
// emits candidates; a maintainer accepts them. Nothing here opens a file at all,
// in any branch.
//
// # No model, no reviewer, no provider
//
// A candidate is built from the axis and the deterministic finding ALONE, and the
// signature is the guarantee rather than a promise: none of the three is a
// parameter, so none can be reached. It has to be that way in both directions —
// group 3 declares no dependency on group 5, and under `--no-review` there is no
// model description in existence, so a candidate that needed one would be
// unsatisfiable on the run that most needs the output. Enriching these blocks with
// the model's words belongs where the model already is.
//
// The consequence is that the block states WHAT diverges, not why. "ours passes 85
// build options, ::gentoo's passes 2" is a fact, and the reason it is acceptable
// is the maintainer's to write — which is what "accept or EDIT" means. What the
// candidate saves is the transcription, and it is enough: a block a maintainer has
// to go and research before pasting is not a candidate.
//
// # One candidate per axis, and declared axes are silent
//
// Per axis rather than per package, because a package diverging on both its
// inherit line and its option list has made two decisions, and one blanket
// declaration covering both would retire them together the day either one
// expires.
//
// An axis a declaration already covers produces nothing (R3.1): re-proposing what
// the ebuild already says trains a maintainer to skip the section, and then the one
// real candidate in it goes unread too. An EXPIRED declaration is the exception and
// gets its candidate back (R3.3) — the day a reason runs out must not be the day
// its divergence goes quiet forever.
//
// The axis is matched with strings.EqualFold, because the two are spelled in two
// cases by the two documents that define them and DeclaredDivergence.Axis keeps
// the maintainer's spelling verbatim.
//
// _Requirements: R3, R3.5, R3.6_
func CandidateDeclarations(axes []AxisFinding, declared []DeclaredDivergence) []string {
	var candidates []string
	for _, finding := range axes {
		// A finding missing either half would render a tag ParseDivergences reports
		// as malformed, and proposing text that is broken on arrival is worse than
		// proposing nothing.
		if finding.Axis == "" || finding.Detail == "" {
			continue
		}
		if candidateCovered(finding.Axis, declared) {
			continue
		}
		candidates = append(candidates, candidateBlock(finding))
	}
	return candidates
}

// candidateCovered reports whether a declaration already answers for this axis.
//
// Expiry is read, not computed: EvaluateDeclarations decides it against the
// ::gentoo tree, and this only asks. A caller that skipped that pass hands in
// declarations with Expired false throughout, which reports every one of them as
// covering its axis — the same answer the parser's own output means.
func candidateCovered(axis string, declared []DeclaredDivergence) bool {
	for _, declaration := range declared {
		if declaration.Expired {
			// Expired is undeclared from here on (R3.3, D7): the divergence rejoins
			// the queue, so it earns a fresh candidate.
			continue
		}
		if strings.EqualFold(declaration.Axis, axis) {
			return true
		}
	}
	return false
}

// candidateBlock renders one finding as the two-line declaration D5 adopted:
//
//	# BENTOO-DIVERGENCE: INHERIT: ::gentoo inherits gstreamer-meson; ours inherits meson
//	#   drop-when: TODO: state when this divergence stops applying, or delete this line
//
// The axis is upper-cased to match §7's spelling of the tag. Nothing else is
// rewritten, and what comes out parses back through ParseDivergences as exactly
// one well-formed declaration — a candidate that would not survive being pasted is
// not one.
//
// The detail is flattened onto a single line for the same reason: the whole block
// is a COMMENT, and a newline inside it would paste a bare line of prose into a
// bash script.
func candidateBlock(finding AxisFinding) string {
	// Named as the two lines the parser itself reads — a tag line and its
	// continuation — rather than as one format string, so what is emitted here
	// and what divergencesInText looks for stay legibly the same two things.
	tag := fmt.Sprintf("# %s %s: %s",
		divergenceTag, strings.ToUpper(finding.Axis), strings.Join(strings.Fields(finding.Detail), " "))
	exit := fmt.Sprintf("%s%s %s",
		candidateContinuation, divergenceDropWhenKey, candidateExitPlaceholder)
	return tag + "\n" + exit
}

// dropWhenBaselineEbuild picks the ebuild to read for a question about
// ::gentoo's own version of a package: the newest RELEASE it carries, or the
// newest of what it carries when every one of them is live. It is only ever
// called with at least one candidate.
//
// The newest is the right one to ask, because it is what ::gentoo maintains now
// and what a realignment would move toward. An older version that happened to
// inherit the eclass would retire a divergence on evidence ::gentoo itself has
// moved past.
//
// The fallback matters for a package ::gentoo only ever ships live. Reading
// nothing there would report "we could not look" at a tree that plainly carries
// the package, when the live ebuild is the only evidence there is.
func dropWhenBaselineEbuild(carried []carriedEbuild) carriedEbuild {
	candidates := dropWhenReleases(carried)
	if len(candidates) == 0 {
		candidates = carried
	}

	newest := candidates[0]
	for _, candidate := range candidates[1:] {
		if ebuild.CompareVersions(candidate.version, newest.version) > 0 {
			newest = candidate
		}
	}
	return newest
}
