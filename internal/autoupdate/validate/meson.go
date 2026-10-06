package validate

import "strings"

// parseMesonOptions returns the option names a meson.options or
// meson_options.txt file declares, in the order the file writes them.
//
// It is a scanner, not a line regex, because two shapes break a regex in
// opposite directions. Most declarations span several lines (`option(` then
// `'qt6',` on the next), so a per-line pattern under-reports, and every missed
// name becomes a FALSE "undeclared" error downstream. A commented-out
// declaration is the mirror image: counting it over-reports and MASKS a real
// finding. Comments are therefore stripped first, quote-aware, so a `#` inside a
// description does not truncate the declaration after it.
//
// Zero options is a valid answer, never an error: a Meson project may declare
// none. Only the name is filled in; the caller stamps Subproject and Source,
// since it knows which archive member these bytes came from.
func parseMesonOptions(data []byte) []Option {
	src := stripMesonComments(string(data))

	var opts []Option
	for i := 0; i < len(src); {
		start := strings.Index(src[i:], "option")
		if start < 0 {
			break
		}
		at := i + start
		i = at + len("option")

		// `get_option('x')` reads an option, it does not declare one. Requiring
		// a non-identifier character before the keyword is what keeps that, and
		// any other *_option helper, out of the declared set.
		if at > 0 && isIdentByte(src[at-1]) {
			continue
		}

		rest := skipSpace(src[i:])
		if !strings.HasPrefix(rest, "(") {
			continue
		}
		// Whitespace here spans newlines, which is the whole reason the
		// multi-line form parses without a special case.
		rest = skipSpace(rest[1:])
		if rest == "" {
			continue
		}

		quote := rest[0]
		if quote != '\'' && quote != '"' {
			continue
		}
		end := strings.IndexByte(rest[1:], quote)
		if end < 0 {
			continue
		}
		if name := rest[1 : 1+end]; name != "" {
			opts = append(opts, Option{Name: name})
		}
		i = len(src) - len(rest) + 1 + end + 1
	}
	return opts
}

// stripMesonComments removes each `#` comment, leaving the newlines in place so
// nothing that spans lines is joined together by the removal.
//
// The quote tracking is what makes it safe on a description containing a `#`.
// It is deliberately simple — Meson has no escape sequence that can hide a
// closing quote from it in an option file — and its failure mode is to strip
// too little, which the scanner above then ignores anyway.
func stripMesonComments(src string) string {
	var out strings.Builder
	out.Grow(len(src))

	var quote byte
	for i := 0; i < len(src); i++ {
		c := src[i]
		switch {
		case quote != 0:
			if c == quote {
				quote = 0
			}
		case c == '\'' || c == '"':
			quote = c
		case c == '#':
			// Drop to the end of the line, keeping the newline itself.
			nl := strings.IndexByte(src[i:], '\n')
			if nl < 0 {
				return out.String()
			}
			i += nl - 1
			continue
		}
		out.WriteByte(c)
	}
	return out.String()
}

func skipSpace(s string) string {
	return strings.TrimLeft(s, " \t\r\n")
}

func isIdentByte(c byte) bool {
	return c == '_' ||
		(c >= 'a' && c <= 'z') ||
		(c >= 'A' && c <= 'Z') ||
		(c >= '0' && c <= '9')
}
