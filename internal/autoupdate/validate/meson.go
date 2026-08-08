package validate

import "strings"

// parseMesonOptions returns the option names a meson.options or
// meson_options.txt file declares, in the order the file writes them.
//
// # Why a scanner and not a regular expression
//
// Two shapes make a line-oriented regex wrong in opposite directions. A real
// option file spreads most declarations over several lines —
//
//	option(
//	    'qt6',
//	    type : 'feature',
//	)
//
// so the name is not on the `option(` line, and a per-line pattern under-reports
// the upstream side. Every name it misses becomes a FALSE error downstream,
// because an option the ebuild passes and the parser never saw looks undeclared.
//
// A commented-out declaration is the mirror image: counting it over-reports, and
// an over-reported upstream MASKS a real finding by making an option the build
// no longer accepts look accepted. Comments are therefore stripped before the
// scan, and stripped quote-aware, so a `#` inside a description does not
// truncate the declaration that follows it.
//
// Zero options is a valid answer and never an error: a Meson project is allowed
// to declare none (R1.4).
//
// Only the name is filled in here. Subproject and Source are stamped by the
// caller, which is the layer that knows which archive member these bytes came
// from — see OptionsFromArchive.
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
