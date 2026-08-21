package main

import (
	"bytes"
	"errors"
	"strings"
	"testing"

	"github.com/obentoo/bentoolkit/misc/design/design-system/component"
	"github.com/obentoo/bentoolkit/misc/design/design-system/theme"
)

func TestSelectModes_ForcedNames(t *testing.T) {
	for _, tt := range []struct {
		in   string
		want theme.Mode
	}{
		{"plain", theme.Plain},
		{"unicode", theme.Unicode},
		{"styled", theme.Styled},
	} {
		got, err := selectModes(false, tt.in)
		if err != nil {
			t.Fatalf("-mode=%s returned %v", tt.in, err)
		}
		if len(got) != 1 || got[0] != tt.want {
			t.Errorf("-mode=%s selected %v, want exactly [%v]", tt.in, got, tt.want)
		}
	}
}

func TestSelectModes_AllCoversEveryMode(t *testing.T) {
	got, err := selectModes(true, "")
	if err != nil {
		t.Fatalf("-all returned %v", err)
	}
	want := []theme.Mode{theme.Plain, theme.Unicode, theme.Styled}
	if len(got) != len(want) {
		t.Fatalf("-all selected %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("-all selected %v, want %v", got, want)
		}
	}
}

// TestSelectModes_UnknownNameIsRefused: a typo must not silently fall back to a
// mode the operator did not ask for, which would make the gallery lie about
// which face it is showing.
func TestSelectModes_UnknownNameIsRefused(t *testing.T) {
	_, err := selectModes(false, "fancy")
	if err == nil {
		t.Fatal(`-mode=fancy was accepted; an unknown mode must be refused, not defaulted`)
	}
	if !strings.Contains(err.Error(), "fancy") {
		t.Errorf("the error %q does not quote the rejected name", err)
	}
}

// TestRender_DrawsEverySampleInTheCatalogue is what stops the gallery and the
// contract test drifting: both read Catalogue(), and this asserts the gallery
// actually shows all of it.
func TestRender_DrawsEverySampleInTheCatalogue(t *testing.T) {
	var buf bytes.Buffer
	if err := render(&buf, theme.Plain); err != nil {
		t.Fatalf("render returned %v", err)
	}
	out := buf.String()

	for _, s := range component.Catalogue() {
		if !strings.Contains(out, s.Name) {
			t.Errorf("the gallery does not show the catalogue entry %q", s.Name)
		}
	}
	if !strings.Contains(out, "mode: plain") {
		t.Error("the gallery does not name the mode it rendered")
	}
}

// TestRender_PlainOutputStaysASCIIExceptForTheGallerysOwnProse pins the
// boundary the README describes: component blocks are indented four spaces and
// must be pure ASCII in Plain; the descriptions above them are the gallery's
// prose and legitimately quote the glyphs they discuss.
func TestRender_PlainOutputStaysASCIIExceptForTheGallerysOwnProse(t *testing.T) {
	var buf bytes.Buffer
	if err := render(&buf, theme.Plain); err != nil {
		t.Fatalf("render returned %v", err)
	}

	for _, ln := range strings.Split(buf.String(), "\n") {
		if !strings.HasPrefix(ln, "    ") {
			continue // gallery prose, not component output
		}
		for i := 0; i < len(ln); i++ {
			if ln[i] >= 0x80 {
				t.Errorf("a component block carries a non-ASCII byte in Plain: %q", ln)
				break
			}
		}
	}
}

func TestWrap_BreaksOnWordsAndNeverExceedsTheWidth(t *testing.T) {
	const width = 20
	got := wrap("the quick brown fox jumps over the lazy dog", width)
	for _, ln := range strings.Split(got, "\n") {
		if theme.Width(ln) > width {
			t.Errorf("line %q is %d cells, over the %d requested", ln, theme.Width(ln), width)
		}
	}
	if strings.ReplaceAll(got, "\n", " ") != "the quick brown fox jumps over the lazy dog" {
		t.Errorf("wrap lost or reordered words: %q", got)
	}
}

func TestWrap_EmptyInputIsEmpty(t *testing.T) {
	if got := wrap("   ", 10); got != "" {
		t.Errorf("wrap of blank input = %q, want empty", got)
	}
}

// TestWrap_AWordLongerThanTheWidthIsNotSplit: breaking a package atom or a path
// mid-token would make it uncopyable, which is worse than overflowing.
func TestWrap_AWordLongerThanTheWidthIsNotSplit(t *testing.T) {
	long := "media-plugins/gst-plugins-qt6-1.29.2"
	got := wrap(long, 10)
	if got != long {
		t.Errorf("wrap split an over-long token: %q", got)
	}
}

// failingWriter fails after n successful writes, so the latch can be observed
// both stopping early and surfacing the cause.
type failingWriter struct {
	ok   int
	errs error
}

func (f *failingWriter) Write(p []byte) (int, error) {
	if f.ok > 0 {
		f.ok--
		return len(p), nil
	}
	return 0, f.errs
}

// TestRender_SurfacesAWriteFailureInsteadOfExitingZero. Writing to stdout does
// fail in practice — a full disk, a closed pipe — and a gallery that returns
// nil after failing to print would have main exit 0 on a lie.
func TestRender_SurfacesAWriteFailureInsteadOfExitingZero(t *testing.T) {
	boom := errors.New("disk full")
	w := &failingWriter{ok: 2, errs: boom}

	err := render(w, theme.Plain)
	if err == nil {
		t.Fatal("render returned nil after the writer failed")
	}
	if !errors.Is(err, boom) {
		t.Errorf("render returned %v, which lost the underlying cause %v", err, boom)
	}
}

// TestRender_StopsWritingOnceAWriteHasFailed pins the latch itself: without it
// every remaining line would be attempted against a writer already known to be
// broken.
func TestRender_StopsWritingOnceAWriteHasFailed(t *testing.T) {
	w := &countingWriter{failAfter: 1, err: errors.New("boom")}
	_ = render(w, theme.Plain)
	if w.attempts != 2 {
		t.Errorf("render attempted %d writes, want 2 — one success and one failure, then stop", w.attempts)
	}
}

type countingWriter struct {
	failAfter int
	attempts  int
	err       error
}

func (c *countingWriter) Write(p []byte) (int, error) {
	c.attempts++
	if c.attempts > c.failAfter {
		return 0, c.err
	}
	return len(p), nil
}
