package component_test

import (
	"bytes"
	"strings"
	"testing"

	"github.com/muesli/termenv"

	"github.com/obentoo/bentoolkit/misc/design/design-system/component"
	"github.com/obentoo/bentoolkit/misc/design/design-system/theme"
)

const esc = "\x1b"

// styled pins the colour profile and the background. Without the pin lipgloss
// degrades to Ascii against a non-terminal and emits nothing, so every
// assertion below about Styled carrying colour would compare "" with "" and
// pass having measured nothing. Measured: unpinned Paint returns "boom",
// pinned-ANSI returns "\x1b[91mboom\x1b[0m".
func styled() theme.Theme {
	return theme.NewFor(theme.Styled, &bytes.Buffer{}, termenv.ANSI, true)
}

// TestEveryComponent_HonoursTheModeContract is the central promise of this
// package, asserted over the WHOLE catalogue rather than per component, so a
// new shape cannot be added that quietly breaks it.
//
//	Plain   — pure ASCII, no escape sequences. The face for a pipe, a script,
//	          a CI log, a file, a non-UTF-8 locale.
//	Unicode — no escape sequences. NO_COLOR on a real terminal.
//	Styled  — escape sequences present.
//
// The first two are the ones that matter: they are promises to a consumer that
// cannot answer back. A component that hardcodes "→" or a colour breaks them
// silently everywhere else, and here it fails by name.
//
// # What the contract does NOT cover, and cannot
//
// The caller's own words. A component chooses its glyphs from the theme, but a
// label, a reason or a note is a string handed in from outside, and an em dash
// typed there survives into Plain untouched. This test caught exactly that in
// its own catalogue on first run, which is the useful demonstration: the
// boundary is real, it is at the argument, and the only enforcement possible on
// the far side of it is the reviewer.
//
// Transliterating caller text was considered and rejected — it would mangle the
// package atoms, paths and upstream error text that make up most of what gets
// passed in.
func TestEveryComponent_HonoursTheModeContract(t *testing.T) {
	samples := component.Catalogue()
	if len(samples) == 0 {
		t.Fatal("the catalogue is empty, so this test asserts nothing about anything")
	}

	for _, s := range samples {
		t.Run(s.Name, func(t *testing.T) {
			plain := s.C.Render(theme.New(theme.Plain))
			if i := strings.IndexFunc(plain, func(r rune) bool { return r >= 0x80 }); i >= 0 {
				t.Errorf("Plain render carries the non-ASCII rune %q at byte %d:\n%s",
					[]rune(plain[i:])[0], i, plain)
			}
			if strings.Contains(plain, esc) {
				t.Errorf("Plain render carries an escape sequence:\n%q", plain)
			}

			if uni := s.C.Render(theme.New(theme.Unicode)); strings.Contains(uni, esc) {
				t.Errorf("Unicode render carries an escape sequence; NO_COLOR means no colour:\n%q", uni)
			}

			if st := s.C.Render(styled()); !strings.Contains(st, esc) {
				t.Errorf("Styled render carries NO escape sequence, so this sample exercises no "+
					"semantic role and its half of this test is vacuous:\n%q", st)
			}
		})
	}
}

// TestEveryComponent_RendersWithoutATrailingNewline pins the composition
// contract. A component that ended in "\n" would double-space every caller that
// joins with "\n", and the bug would only show up in assembled output.
func TestEveryComponent_RendersWithoutATrailingNewline(t *testing.T) {
	for _, s := range component.Catalogue() {
		t.Run(s.Name, func(t *testing.T) {
			for _, m := range []theme.Mode{theme.Plain, theme.Unicode, theme.Styled} {
				got := s.C.Render(theme.New(m))
				if strings.HasSuffix(got, "\n") {
					t.Errorf("%v render ends with a newline:\n%q", m, got)
				}
			}
		})
	}
}

// TestEveryComponent_RendersNoTrailingWhitespace keeps padded columns from
// leaking spaces at end of line, which show up as diff noise in every captured
// transcript and golden file this repository keeps.
func TestEveryComponent_RendersNoTrailingWhitespace(t *testing.T) {
	for _, s := range component.Catalogue() {
		t.Run(s.Name, func(t *testing.T) {
			for _, ln := range strings.Split(s.C.Render(theme.New(theme.Plain)), "\n") {
				if ln != strings.TrimRight(ln, " \t") {
					t.Errorf("line ends in whitespace: %q", ln)
				}
			}
		})
	}
}

func TestCatalogue_NamesAreUniqueAndDocumented(t *testing.T) {
	seen := map[string]bool{}
	for _, s := range component.Catalogue() {
		if s.Name == "" {
			t.Error("a catalogue entry has no name")
		}
		if seen[s.Name] {
			t.Errorf("two catalogue entries are named %q; the gallery would show them as one", s.Name)
		}
		seen[s.Name] = true
		if s.Why == "" {
			t.Errorf("%s has no Why; a component with no call site it replaces is speculative", s.Name)
		}
	}
}
