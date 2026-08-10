package overlay

import (
	"errors"
	"fmt"
	"os"
	"strings"
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
