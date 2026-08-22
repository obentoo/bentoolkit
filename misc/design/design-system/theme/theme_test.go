package theme_test

import (
	"bytes"
	"strings"
	"testing"

	"github.com/muesli/termenv"

	"github.com/obentoo/bentoolkit/misc/design/design-system/theme"
)

// styled builds a Styled theme whose colour profile and background are pinned,
// which is the only way an assertion about escape sequences means anything —
// see theme.NewFor.
func styled() theme.Theme {
	return theme.NewFor(theme.Styled, &bytes.Buffer{}, termenv.ANSI, true)
}

const esc = "\x1b"

func TestModeFor_MirrorsTheEnabledDecisionSurface(t *testing.T) {
	for _, tt := range []struct {
		name    string
		isTTY   bool
		noColor bool
		want    theme.Mode
	}{
		{"piped", false, false, theme.Plain},
		{"piped with NO_COLOR", false, true, theme.Plain},
		{"tty with NO_COLOR", true, true, theme.Unicode},
		{"tty", true, false, theme.Styled},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if got := theme.ModeFor(tt.isTTY, tt.noColor); got != tt.want {
				t.Errorf("ModeFor(isTTY=%v, noColor=%v) = %v, want %v",
					tt.isTTY, tt.noColor, got, tt.want)
			}
		})
	}
}

// TestModeFor_NoColorOnATTYKeepsUnicode is the whole reason Mode has three
// values instead of two. NO_COLOR is a statement about colour, not about UTF-8,
// and collapsing it into the pipe case throws away glyphs the terminal renders
// perfectly well.
func TestModeFor_NoColorOnATTYKeepsUnicode(t *testing.T) {
	m := theme.ModeFor(true, true)
	if m == theme.Plain {
		t.Fatal("NO_COLOR on a TTY resolved to Plain; that discards Unicode the terminal can draw")
	}
	if got := theme.New(m).Glyphs().OK; got != "✓" {
		t.Errorf("NO_COLOR on a TTY yields the OK glyph %q, want the Unicode one", got)
	}
}

func TestZeroThemeIsPlainAndUsable(t *testing.T) {
	var zero theme.Theme
	if zero.Mode() != theme.Plain {
		t.Errorf("the zero Theme reports mode %v, want Plain — the safe default must be the one nobody chose", zero.Mode())
	}
	if got := zero.Glyphs().OK; got != "[OK]" {
		t.Errorf("the zero Theme yields OK glyph %q, want the ASCII token", got)
	}
	if got := zero.Paint(theme.Fail, "x"); got != "x" {
		t.Errorf("the zero Theme painted %q; Plain must emit no escape sequences", got)
	}
}

// TestPaint_StyledActuallyEmitsColour is the non-vacuity guard. Without a
// pinned profile lipgloss degrades to Ascii against a buffer and emits nothing,
// so every "Styled has colour" assertion in this package would compare "" to ""
// and pass having measured nothing.
func TestPaint_StyledActuallyEmitsColour(t *testing.T) {
	got := styled().Paint(theme.Fail, "boom")

	if !strings.Contains(got, esc) {
		t.Fatalf("Styled painted %q with no escape sequence; the profile is not pinned and every "+
			"colour assertion in this package is passing vacuously", got)
	}
	if !strings.Contains(got, "boom") {
		t.Errorf("Styled painted %q, which lost the text it was given", got)
	}
}

func TestPaint_NonStyledModesEmitNoEscapes(t *testing.T) {
	for _, m := range []theme.Mode{theme.Plain, theme.Unicode} {
		t.Run(m.String(), func(t *testing.T) {
			for _, r := range []theme.Role{theme.OK, theme.Fail, theme.Warn, theme.Info, theme.Skip, theme.Accent, theme.Heading, theme.Muted} {
				if got := theme.New(m).Paint(r, "x"); got != "x" {
					t.Errorf("%v painted role %v as %q, want the bare text", m, r, got)
				}
			}
		})
	}
}

func TestGlyphs_PlainIsPureASCII(t *testing.T) {
	g := theme.New(theme.Plain).Glyphs()
	for _, s := range []string{g.OK, g.Fail, g.Warn, g.Info, g.Skip, g.Arrow, g.Dash, g.Bullet, g.VLine, g.HLine, g.TopLeft, g.BottomLeft} {
		for i := 0; i < len(s); i++ {
			if s[i] >= 0x80 {
				t.Errorf("the Plain glyph set contains the non-ASCII glyph %q; Plain is the face for a "+
					"pipe, a CI log and a non-UTF-8 locale, and must assume none of them render it", s)
				break
			}
		}
	}
}

func TestGlyphs_StatusWidthCoversTheWidestToken(t *testing.T) {
	for _, m := range []theme.Mode{theme.Plain, theme.Unicode, theme.Styled} {
		t.Run(m.String(), func(t *testing.T) {
			g := theme.New(m).Glyphs()
			w := g.StatusWidth()
			for _, r := range []theme.Role{theme.OK, theme.Fail, theme.Warn, theme.Info, theme.Skip} {
				if n := theme.Width(g.Status(r)); n > w {
					t.Errorf("StatusWidth() = %d but the token for role %v is %d cells wide", w, r, n)
				}
			}
		})
	}
}

func TestGlyphs_StatusIsEmptyForRolesThatCarryNoOutcome(t *testing.T) {
	g := theme.New(theme.Unicode).Glyphs()
	for _, r := range []theme.Role{theme.Default, theme.Accent, theme.Heading, theme.Muted} {
		if got := g.Status(r); got != "" {
			t.Errorf("role %v yielded the status token %q; only outcome roles have one", r, got)
		}
	}
}

// TestWidth_CountsCellsNotBytes pins the reason components measure through
// theme.Width: len("✓") is 3 and every column built on it misaligns.
func TestWidth_CountsCellsNotBytes(t *testing.T) {
	if got := theme.Width("✓"); got != 1 {
		t.Errorf("Width(\"✓\") = %d, want 1 cell (len would say %d)", got, len("✓"))
	}
	if got := theme.Width(styled().Paint(theme.OK, "ok")); got != 2 {
		t.Errorf("Width of painted %q = %d, want 2 — escape sequences occupy no cells", "ok", got)
	}
}

func TestMode_String(t *testing.T) {
	for _, tt := range []struct {
		m    theme.Mode
		want string
	}{
		{theme.Plain, "plain"},
		{theme.Unicode, "unicode"},
		{theme.Styled, "styled"},
		{theme.Mode(99), "plain"},
	} {
		if got := tt.m.String(); got != tt.want {
			t.Errorf("Mode(%d).String() = %q, want %q", tt.m, got, tt.want)
		}
	}
}
