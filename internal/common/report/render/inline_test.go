package render

import (
	"bytes"
	"io"
	"os"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
)

// captureStdout runs fn with os.Stdout redirected through a pipe and returns
// what it wrote.
//
// It exists because Inline's signature — Inline(r, opts) error — carries no
// io.Writer, unlike Plain, Markdown and JSON. That is the design's choice, not
// an oversight to work around here: an inline renderer owns a region of the
// terminal, and a terminal is not an arbitrary writer. The cost is that the
// only way to see its output is to capture the descriptor, which is also how
// sub-task 5.2 must test the exit dump.
func captureStdout(t *testing.T, fn func() error) string {
	t.Helper()

	original := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("creating the capture pipe: %v", err)
	}
	os.Stdout = w

	// Drain concurrently: a pipe's buffer is finite, and a report larger than
	// it would deadlock the writer if nothing were reading.
	captured := make(chan string, 1)
	go func() {
		var buf bytes.Buffer
		_, _ = io.Copy(&buf, r)
		captured <- buf.String()
	}()

	fnErr := fn()

	if err := w.Close(); err != nil {
		t.Fatalf("closing the capture pipe: %v", err)
	}
	os.Stdout = original
	out := <-captured
	_ = r.Close()

	if fnErr != nil {
		t.Fatalf("the function under test returned an error: %v", fnErr)
	}
	return out
}

// trimTrailing drops trailing spaces from every line.
//
// This is the ONE relaxation in the comparison below, and it is deliberate: a
// styled renderer pads a cell to its column width, and trailing spaces are
// invisible on a terminal. They are presentation, which is exactly what R2.4
// says the modes may differ in. Everything else must match character for
// character.
func trimTrailing(s string) string {
	lines := strings.Split(s, "\n")
	for i, line := range lines {
		lines[i] = strings.TrimRight(line, " \t")
	}
	return strings.Join(lines, "\n")
}

// TestInlineMatchesPlainContent pins R2.4, and it is the only mechanical proof
// the story has that "the modes differ in presentation only" is true rather
// than intended.
//
// Comparing the two renders on stripped content is what makes it a measurement.
// Asserting that Inline merely runs without error would pass for a renderer
// that dropped a section, reordered the tally, or shortened a reason the plain
// mode kept — which is the class of divergence that produced three table styles
// on one screen in the first place.
func TestInlineMatchesPlainContent(t *testing.T) {
	opts := Options{Width: 100}

	plain := trimTrailing(renderPlain(t, opts))
	inline := trimTrailing(ansi.Strip(captureStdout(t, func() error {
		return Inline(fixtureReport(), opts)
	})))

	if inline == plain {
		return
	}

	// Report the first divergence rather than two 42-line blobs.
	plainLines, inlineLines := strings.Split(plain, "\n"), strings.Split(inline, "\n")
	for i := 0; i < len(plainLines) || i < len(inlineLines); i++ {
		var p, in string
		if i < len(plainLines) {
			p = plainLines[i]
		}
		if i < len(inlineLines) {
			in = inlineLines[i]
		}
		if p != in {
			t.Fatalf("inline and plain diverge at line %d (R2.4 — the modes may differ in presentation, not in content)\n  plain:  %q\n  inline: %q\n\nplain has %d lines, inline has %d",
				i+1, p, in, len(plainLines), len(inlineLines))
		}
	}
}

// TestInlineIsStyled is the guard that keeps the test above from being
// satisfied the lazy way. Inline could match Plain perfectly by BEING Plain —
// and then R2.2's live region would not exist while R2.4 reported success.
func TestInlineIsStyled(t *testing.T) {
	out := captureStdout(t, func() error {
		return Inline(fixtureReport(), Options{Width: 100})
	})

	if !strings.ContainsRune(out, 0x1b) {
		t.Fatal("the inline render carries no escape sequence — it is plain output wearing another name (R2.2)")
	}
}

// TestInlineHonoursShowAll — the flag reaches the terminal modes. The exports
// deliberately ignore it (they have no Options at all); inline is a screen
// mode, so it must not.
func TestInlineHonoursShowAll(t *testing.T) {
	without := ansi.Strip(captureStdout(t, func() error {
		return Inline(fixtureReport(), Options{Width: 100})
	}))
	with := ansi.Strip(captureStdout(t, func() error {
		return Inline(fixtureReport(), Options{Width: 100, ShowAll: true})
	}))

	if without == with {
		t.Fatal("ShowAll changed nothing in the inline render")
	}
	for _, pkg := range []string{"app-editors/zed", "dev-lang/go"} {
		if !strings.Contains(with, pkg) {
			t.Errorf("ShowAll did not reveal %q", pkg)
		}
	}
}
