package notice

import (
	"fmt"
	"strings"
	"unicode/utf8"
)

// newsWidth is the GLEP 42 body width, in characters.
const newsWidth = 72

// RenderNews renders n as a GLEP 42 news item (format 2.0): the headers in
// order, one Display-If-Installed per affects entry, one blank line, then the
// body wrapped at 72 columns with tabs expanded (R3.2 to R3.6).
//
// Display-If-Installed headers combine with OR, while the ranges of one feed
// entry combine with AND, so an entry with several ranges cannot be one
// header: it is written as the bare package and reported in warnings (R3.5).
func RenderNews(n Notice) (text string, warnings []string) {
	var b strings.Builder
	fmt.Fprintf(&b, "Title: %s\nAuthor: %s\nPosted: %s\nRevision: %d\nNews-Item-Format: 2.0\n",
		n.Title, n.Author, n.Published.UTC().Format(dateLayout), n.Revision)
	for _, a := range n.Affects {
		atom, warn := newsAtom(a)
		fmt.Fprintf(&b, "Display-If-Installed: %s\n", atom)
		if warn != "" {
			warnings = append(warnings, warn)
		}
	}
	b.WriteString("\n")
	b.WriteString(wrapBody(n.Body))
	return b.String(), warnings
}

// newsAtom maps one affects entry to an EAPI 5 atom: no range is the bare
// package, one range is `<op>cat/pkg-<ver>[:slot]`, several are the bare
// package plus a warning (R3.3 to R3.5).
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

// wrapBody expands tabs and wraps every line longer than 72 characters on
// word boundaries, keeping its indentation on the continuation lines. Lines
// are wrapped one by one rather than reflowed, so the author's layout —
// headings, their underlines, lists — survives. A word longer than the width
// stays whole on its own line. The result ends with exactly one newline.
func wrapBody(body string) string {
	body = strings.ReplaceAll(body, "\t", "    ")
	body = strings.TrimRight(body, "\n")
	var out []string
	for line := range strings.SplitSeq(body, "\n") {
		line = strings.TrimRight(line, " ")
		if utf8.RuneCountInString(line) <= newsWidth {
			out = append(out, line)
			continue
		}
		indent := line[:len(line)-len(strings.TrimLeft(line, " "))]
		cur, curLen := indent, len(indent)
		for w := range strings.FieldsSeq(line) {
			wLen := utf8.RuneCountInString(w)
			if curLen > len(indent) && curLen+1+wLen > newsWidth {
				out = append(out, cur)
				cur, curLen = indent, len(indent)
			}
			if curLen > len(indent) {
				cur += " "
				curLen++
			}
			cur += w
			curLen += wLen
		}
		out = append(out, cur)
	}
	return strings.Join(out, "\n") + "\n"
}
