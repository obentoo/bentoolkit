package notice

// Story 071, sub-task 3.1: RenderNews writes a GLEP 42 news item — headers in
// order, one Display-If-Installed per affects entry, one blank line, then the
// text wrapped at 72 columns with no tabs (R3.2 to R3.6).
//
// Hostile halves first:
//   - an entry with ONE range must not collapse into the bare package, and an
//     entry with SEVERAL must not be squeezed into one versioned atom;
//   - two entries for the same package in different slots must stay two
//     distinct headers, and several multi-range entries two distinct warnings;
//   - a line of exactly 72 characters built from two-byte runes must not be
//     split (a byte count reads it as ~100), while 73 characters must be.

import (
	"reflect"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

func newsNoticeS071() Notice {
	pub := time.Date(2026, 10, 2, 0, 0, 0, 0, time.UTC)
	return Notice{
		ID:        "2026-10-02-foo-cve",
		Type:      "security",
		Severity:  "critical",
		Title:     "foo 1.2 heap overflow",
		Summary:   "One-paragraph summary.",
		Author:    "Jane Doe <jane@example.org>",
		Body:      "Upgrade now.",
		Affects:   []Affects{{CP: "dev-libs/foo"}},
		Published: pub,
		Updated:   pub,
		Revision:  1,
	}
}

// splitNewsS071 separates the header lines from the text after the first
// blank line.
func splitNewsS071(t *testing.T, text string) (headers []string, body string) {
	t.Helper()
	head, rest, ok := strings.Cut(text, "\n\n")
	if !ok {
		t.Fatalf("the news item has no blank line after its headers:\n%s", text)
	}
	return strings.Split(head, "\n"), rest
}

func displayIfInstalledS071(headers []string) []string {
	var out []string
	for _, h := range headers {
		if v, ok := strings.CutPrefix(h, "Display-If-Installed: "); ok {
			out = append(out, v)
		}
	}
	return out
}

func TestRenderNews_HeadersInGLEP42Order(t *testing.T) {
	n := newsNoticeS071()
	text, _ := RenderNews(n)
	headers, _ := splitNewsS071(t, text)

	want := []string{
		"Title: foo 1.2 heap overflow",
		"Author: Jane Doe <jane@example.org>",
		"Posted: 2026-10-02",
		"Revision: 1",
		"News-Item-Format: 2.0",
		"Display-If-Installed: dev-libs/foo",
	}
	if !reflect.DeepEqual(headers, want) {
		t.Errorf("headers =\n%s\nwant\n%s", strings.Join(headers, "\n"), strings.Join(want, "\n"))
	}

	n.Revision = 3
	text, _ = RenderNews(n)
	if !strings.Contains(text, "\nRevision: 3\n") {
		t.Errorf("a notice at revision 3 did not render Revision: 3:\n%s", text)
	}
}

func TestRenderNews_OneRangeIsAnAtomAndSeveralAreTheBarePackage(t *testing.T) {
	n := newsNoticeS071()
	n.Affects = []Affects{
		{CP: "dev-libs/qux", Ranges: []Range{{Op: ">=", Ver: "1.0"}, {Op: "<", Ver: "1.2.3"}}},
		{CP: "dev-libs/bar", Slot: "2", Ranges: []Range{{Op: ">=", Ver: "1.0"}}},
		{CP: "dev-libs/baz", Ranges: []Range{{Op: "<", Ver: "3"}}},
		{CP: "dev-libs/eq", Ranges: []Range{{Op: "=", Ver: "2.0-r1"}}},
		{CP: "dev-libs/foo", Slot: "1"},
	}
	text, warnings := RenderNews(n)
	headers, _ := splitNewsS071(t, text)

	want := []string{
		"dev-libs/qux",
		">=dev-libs/bar-1.0:2",
		"<dev-libs/baz-3",
		"=dev-libs/eq-2.0-r1",
		"dev-libs/foo:1",
	}
	if got := displayIfInstalledS071(headers); !reflect.DeepEqual(got, want) {
		t.Errorf("Display-If-Installed = %q, want %q", got, want)
	}

	if len(warnings) != 1 {
		t.Fatalf("warnings = %q, want exactly one (for the multi-range dev-libs/qux)", warnings)
	}
	if !strings.Contains(warnings[0], "dev-libs/qux") {
		t.Errorf("the warning %q does not name the entry it is about", warnings[0])
	}
	if !strings.Contains(warnings[0], "every installed version") {
		t.Errorf("the warning %q does not say the news item targets every installed version", warnings[0])
	}
}

// Same package, two slots, two ranges each: two headers and two warnings,
// each telling the entries apart.
func TestRenderNews_EntriesForOnePackageStayDistinct(t *testing.T) {
	n := newsNoticeS071()
	n.Affects = []Affects{
		{CP: "dev-libs/foo", Slot: "1", Ranges: []Range{{Op: ">=", Ver: "1.0"}, {Op: "<", Ver: "1.4"}}},
		{CP: "dev-libs/foo", Slot: "2", Ranges: []Range{{Op: ">=", Ver: "2.0"}, {Op: "<", Ver: "2.1"}}},
		{CP: "dev-libs/foo", Slot: "3", Ranges: []Range{{Op: "<", Ver: "3.2"}}},
	}
	text, warnings := RenderNews(n)
	headers, _ := splitNewsS071(t, text)

	want := []string{"dev-libs/foo:1", "dev-libs/foo:2", "<dev-libs/foo-3.2:3"}
	if got := displayIfInstalledS071(headers); !reflect.DeepEqual(got, want) {
		t.Errorf("Display-If-Installed = %q, want %q", got, want)
	}
	if len(warnings) != 2 {
		t.Fatalf("warnings = %q, want two, one per multi-range entry", warnings)
	}
	if warnings[0] == warnings[1] {
		t.Errorf("the two warnings are identical (%q); a reader cannot tell which entry each is about", warnings[0])
	}
	for i, slot := range []string{":1", ":2"} {
		if !strings.Contains(warnings[i], slot) {
			t.Errorf("warning %d %q does not name its entry's slot %s", i, warnings[i], slot)
		}
	}
}

func TestRenderNews_NoWarningWithoutAMultiRangeEntry(t *testing.T) {
	n := newsNoticeS071()
	n.Affects = []Affects{{CP: "dev-libs/foo", Ranges: []Range{{Op: "<", Ver: "2"}}}, {CP: "dev-libs/bar"}}
	if _, warnings := RenderNews(n); len(warnings) != 0 {
		t.Errorf("warnings = %q, want none", warnings)
	}
}

// exactRunesS071 builds a line of words whose length is exactly n runes, every
// word made of two-byte runes.
func exactRunesS071(n int) string {
	var b strings.Builder
	for utf8.RuneCountInString(b.String()) < n {
		left := n - utf8.RuneCountInString(b.String())
		if b.Len() > 0 {
			b.WriteString(" ")
			left--
		}
		w := 5
		if left < 5 || left-5 == 1 {
			w = left
		}
		b.WriteString(strings.Repeat("ç", w))
	}
	return b.String()
}

func TestRenderNews_WrapsTextAt72Columns(t *testing.T) {
	exact := exactRunesS071(72)
	over := exactRunesS071(73)
	if utf8.RuneCountInString(exact) != 72 || utf8.RuneCountInString(over) != 73 {
		t.Fatalf("fixture lengths %d/%d, want 72/73", utf8.RuneCountInString(exact), utf8.RuneCountInString(over))
	}
	longWord := "https://example.org/" + strings.Repeat("x", 80)
	para := strings.Repeat("lorem ipsum dolor sit amet ", 12) + "end."
	n := newsNoticeS071()
	n.Body = exact + "\n\n" + over + "\n\n" + para + "\n\nA\ttab and " + longWord + " after."

	text, _ := RenderNews(n)
	if !utf8.ValidString(text) {
		t.Fatal("the news item is not valid UTF-8")
	}
	if strings.Contains(text, "\t") {
		t.Error("the news item still contains a tab")
	}
	if !strings.HasSuffix(text, "\n") {
		t.Error("the news item does not end with a newline")
	}
	_, body := splitNewsS071(t, text)
	if strings.HasPrefix(body, "\n") {
		t.Error("more than one blank line follows the headers")
	}

	lines := strings.Split(strings.TrimRight(body, "\n"), "\n")
	for _, l := range lines {
		if utf8.RuneCountInString(l) > 72 && strings.ContainsRune(strings.TrimSpace(l), ' ') {
			t.Errorf("line of %d characters exceeds 72 columns: %q", utf8.RuneCountInString(l), l)
		}
	}

	// Hostile half (wrongly split): exactly 72 characters is one line.
	if lines[0] != exact {
		t.Errorf("a 72-character line was split or altered; first line = %q", lines[0])
	}
	// Hostile half (wrongly kept): 73 characters are two lines.
	paras := strings.Split(strings.TrimRight(body, "\n"), "\n\n")
	if len(paras) != 4 {
		t.Fatalf("paragraphs = %d, want 4 (blank lines between paragraphs must be kept):\n%s", len(paras), body)
	}
	if strings.Count(paras[1], "\n") != 1 {
		t.Errorf("a 73-character paragraph should wrap into two lines, got %q", paras[1])
	}
	// The very long word stays whole, on a line of its own length.
	if !strings.Contains(body, longWord) {
		t.Errorf("a word longer than 72 characters was broken")
	}
	// No word is lost, reordered or split.
	if got, want := strings.Fields(body), strings.Fields(n.Body); !reflect.DeepEqual(got, want) {
		t.Errorf("wrapping changed the words:\n got %q\nwant %q", got, want)
	}
}
