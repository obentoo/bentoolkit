package notice

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/x/ansi"
)

// newsWidth is the GLEP 42 body width, in characters.
const newsWidth = 72

// RenderNews renders n as a GLEP 42 news item (format 2.0): the headers in
// order, one Display-If-Installed per affects entry, one blank line, then the
// body wrapped at 72 columns with tabs expanded.
//
// Display-If-Installed headers combine with OR, while the ranges of one feed
// entry combine with AND, so an entry with several ranges cannot be one
// header: it is written as the bare package and reported in warnings.
func RenderNews(n Notice) (text string, warnings []string) {
	return renderItem(n, n.Title, nil, n.Body)
}

// renderItem renders one file of n's news item — the English one or a
// translation — with the given title, Translator lines and body. Every other
// header comes from n, so all languages of an item agree on them.
func renderItem(n Notice, title string, translators []string, body string) (text string, warnings []string) {
	var b strings.Builder
	fmt.Fprintf(&b, "Title: %s\nAuthor: %s\n", title, n.Author)
	for _, t := range translators {
		fmt.Fprintf(&b, "Translator: %s\n", t)
	}
	fmt.Fprintf(&b, "Posted: %s\nRevision: %d\nNews-Item-Format: 2.0\n",
		n.Published.UTC().Format(dateLayout), n.Revision)
	for _, a := range n.Affects {
		atom, warn := newsAtom(a)
		fmt.Fprintf(&b, "Display-If-Installed: %s\n", atom)
		if warn != "" {
			warnings = append(warnings, warn)
		}
	}
	b.WriteString("\n")
	b.WriteString(wrapBody(body))
	return b.String(), warnings
}

// newsAtom maps one affects entry to an EAPI 5 atom: no range is the bare
// package, one range is `<op>cat/pkg-<ver>[:slot]`, several are the bare
// package plus a warning.
func newsAtom(a Affects) (atom, warning string) {
	slot := ""
	if a.Slot != "" {
		slot = ":" + a.Slot
	}
	switch len(a.Ranges) {
	case 0:
		return a.CP + slot, ""
	case 1:
		r := a.Ranges[0]
		return r.Op + a.CP + "-" + r.Ver + slot, ""
	default:
		return a.CP + slot, fmt.Sprintf(
			"affects %q: several ranges cannot be one news atom; the news item targets every installed version",
			AffectsString(a))
	}
}

// AffectsString renders a in the --affects syntax it was parsed from.
func AffectsString(a Affects) string {
	s := a.CP
	if a.Slot != "" {
		s += ":" + a.Slot
	}
	if len(a.Ranges) == 0 {
		return s
	}
	parts := make([]string, len(a.Ranges))
	for i, r := range a.Ranges {
		parts[i] = r.Op + r.Ver
	}
	return s + " " + strings.Join(parts, ",")
}

// wrapBody expands tabs and wraps every line wider than 72 columns,
// keeping its indentation on the continuation lines. Lines are wrapped one by
// one rather than reflowed, so the author's layout — headings, their
// underlines, lists — survives. A line breaks at spaces and, for text written
// without them (Chinese, Japanese), between two wide characters; a wide
// character counts two columns, as a terminal shows it. A word wider than the
// line stays whole on its own line. The result ends with exactly one newline.
func wrapBody(body string) string {
	body = strings.ReplaceAll(body, "\t", "    ")
	body = strings.TrimRight(body, "\n")
	var out []string
	for line := range strings.SplitSeq(body, "\n") {
		line = strings.TrimRight(line, " ")
		if ansi.StringWidth(line) <= newsWidth {
			out = append(out, line)
			continue
		}
		indent := line[:len(line)-len(strings.TrimLeft(line, " "))]
		cur, curLen := indent, len(indent)
		for _, tok := range wrapTokens(line) {
			sep := 0
			if tok.space && curLen > len(indent) {
				sep = 1
			}
			if curLen > len(indent) && curLen+sep+tok.width > newsWidth {
				out = append(out, cur)
				cur, curLen, sep = indent, len(indent), 0
			}
			if sep == 1 {
				cur += " "
				curLen++
			}
			cur += tok.text
			curLen += tok.width
		}
		out = append(out, cur)
	}
	return strings.Join(out, "\n") + "\n"
}

// wrapToken is a piece of a line that wrapBody never splits: a word, or one
// wide character with the punctuation that must not start a line glued to it.
// space says whether a space precedes it in the line.
type wrapToken struct {
	text  string
	width int
	space bool
}

// noLineStart is the punctuation that must not begin a line: CJK closing
// marks, and the ASCII ones that may follow a wide character. It stays with
// the character before it.
const noLineStart = "、。，．：；！？）」』》〉】〕”’…・ー.,;:!?)]}\"'"

// wrapTokens splits line into its words, and a word holding wide characters
// further: a line may break before a wide character and between a wide
// character and a narrow one, except before punctuation in noLineStart.
func wrapTokens(line string) []wrapToken {
	var toks []wrapToken
	for word := range strings.FieldsSeq(line) {
		start := len(toks)
		lastWide := false
		for _, r := range word {
			wide := ansi.StringWidth(string(r)) > 1
			breakBefore := len(toks) > start && (wide || lastWide) && !strings.ContainsRune(noLineStart, r)
			if len(toks) == start || breakBefore {
				toks = append(toks, wrapToken{space: len(toks) == start})
			}
			last := &toks[len(toks)-1]
			last.text += string(r)
			last.width += ansi.StringWidth(string(r))
			lastWide = wide
		}
	}
	return toks
}
