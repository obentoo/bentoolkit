// Package component holds one type per shape the bentoo commands actually
// print. The inventory was not invented: it was read out of the existing
// fmt.Print* call sites across cmd/ and internal/, and every type below has at
// least one caller in the tree today that it could replace.
//
// # Why JSON is not a mode
//
// The obvious model is "three renders: TUI, plain, JSON". It is wrong, and the
// wrongness is load-bearing. A JSON render would have to invent a schema per
// component, and that schema would drift from the text the moment somebody
// reworded a line — two representations of one fact, kept in sync by nobody.
//
// So a component is a TYPED VALUE with json tags on it. theme.Mode picks
// between two text faces; encoding/json serves the third consumer straight off
// the same struct. One definition, and `--json` cannot disagree with the human
// output about what happened because it is reading the same fields.
//
// # The render contract
//
// Render returns text with NO trailing newline, so components compose: a caller
// joins them with "\n" and decides its own spacing. A component that draws
// several lines joins them itself. Nothing here writes to a stream — rendering
// and printing are separate so a test can assert on the string.
//
// A component guarantees that ITS OWN glyphs suit the mode. It cannot guarantee
// that about the caller's words: a label or a reason is a string handed in from
// outside, and an em dash typed there reaches Plain untouched. Transliterating
// it was rejected — it would mangle the package atoms, paths and upstream error
// text that make up most of what callers pass.
package component

import (
	"strings"

	"github.com/obentoo/bentoolkit/misc/design/design-system/theme"
)

// Component is anything the catalogue can draw.
type Component interface {
	// Render draws the component for t, returning text with no trailing newline.
	Render(t theme.Theme) string
}

// indent shifts every line of s right by n spaces.
//
// Every line, not just the first: the commands nest to four levels (see
// overlay_prune.go, which indents 2/4/6/8), and a helper that indented only the
// head would silently unalign every wrapped or multi-line child.
func indent(s string, n int) string {
	if n <= 0 || s == "" {
		return s
	}
	pad := strings.Repeat(" ", n)
	lines := strings.Split(s, "\n")
	out := make([]string, 0, len(lines))
	for _, ln := range lines {
		if ln == "" {
			// An empty line stays empty. Padding it would emit trailing
			// whitespace, which shows up as a diff-noise line in every golden
			// file and in every `git diff` of captured output.
			out = append(out, ln)
			continue
		}
		out = append(out, pad+ln)
	}
	return strings.Join(out, "\n")
}

// pad right-pads s to w display cells, measuring with theme.Width so a glyph or
// an already-painted string is counted in cells rather than bytes.
func pad(s string, w int) string {
	n := theme.Width(s)
	if n >= w {
		return s
	}
	return s + strings.Repeat(" ", w-n)
}

// join assembles rendered lines, dropping nothing and adding no trailing
// newline.
func join(lines []string) string { return strings.Join(lines, "\n") }
