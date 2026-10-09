package notice

import (
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	yaml "go.yaml.in/yaml/v3"
)

var updateGolden = flag.Bool("update", false, "rewrite the golden files under testdata/")

// goldenNotice exercises every header form and the wrapping rules at once.
func goldenNotice() Notice {
	pub := time.Date(2026, 10, 2, 0, 0, 0, 0, time.UTC)
	return Notice{
		ID: "2026-10-02-foo-cve", Type: "security", Severity: "critical",
		Title:   "foo 1.2 heap overflow",
		Summary: "A crafted archive overflows a heap buffer in foo before 1.2.3.",
		Author:  "Jane Doe <jane@example.org>",
		Body: "A crafted archive overflows a heap buffer in libfoo when it is unpacked, " +
			"which lets an attacker run code as the user running the extraction.\n\n" +
			"What to do\n==========\n\n\tUpgrade to dev-libs/foo-1.2.3 or later, then rebuild every package linked against it.",
		Affects: []Affects{
			{CP: "dev-libs/foo", Slot: "1", Ranges: []Range{{Op: ">=", Ver: "1.0"}, {Op: "<", Ver: "1.2.3"}}},
			{CP: "dev-libs/foo", Slot: "0", Ranges: []Range{{Op: "<", Ver: "0.9.8"}}},
			{CP: "app-misc/foo-tools"},
		},
		Published: pub, Updated: pub, Revision: 1,
	}
}

// compareGolden checks got against testdata/<name>, rewriting it under -update.
func compareGolden(t *testing.T, name string, got []byte) {
	t.Helper()
	path := filepath.Join("testdata", name)
	if *updateGolden {
		if err := os.WriteFile(path, got, 0o600); err != nil {
			t.Fatalf("writing golden %s: %v", path, err)
		}
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading golden %s (run with -update to create it): %v", path, err)
	}
	if string(got) != string(want) {
		t.Errorf("output differs from %s:\n--- got\n%s\n--- want\n%s", path, got, want)
	}
}

func TestRenderNews_MatchesTheGoldenFile(t *testing.T) {
	text, warnings := RenderNews(goldenNotice())
	compareGolden(t, "news.golden.txt", []byte(text))
	if len(warnings) != 1 {
		t.Errorf("warnings = %q, want one for the multi-range dev-libs/foo:1", warnings)
	}
}

func TestRenderSiteYAML_MatchesTheGoldenFile(t *testing.T) {
	data, err := RenderSiteYAML(goldenNotice())
	if err != nil {
		t.Fatalf("RenderSiteYAML: %v", err)
	}
	compareGolden(t, "site.golden.yaml", data)
}

// Tech review of 3.2: the site reads this file with js-yaml (YAML 1.2), which
// types more plain scalars than yaml.v3 does, and treats U+2028/U+2029 as
// ordinary characters. Free text is therefore always quoted, and a body that a
// literal block would alter is double-quoted instead.
func TestRenderSiteYAML_FreeTextIsQuotedForTheSiteReader(t *testing.T) {
	n := goldenNotice()
	n.Title = "2026-10-02 10:00:00Z"
	n.Summary = "._5"
	for _, tc := range []struct {
		body    string
		literal bool
	}{
		{"plain text\n\nsecond", true},
		{"a\u2028b", false},
		{"a\u2029b\nc", false},
		{"\n\nleading blank lines", false},
	} {
		n.Body = tc.body
		data, err := RenderSiteYAML(n)
		if err != nil {
			t.Fatalf("RenderSiteYAML: %v", err)
		}
		root := siteRootS071(t, data)
		for _, k := range []string{"title", "summary"} {
			if v := siteFieldS071(root, k); v == nil || v.Style != yaml.DoubleQuotedStyle {
				t.Errorf("%s is not double-quoted:\n%s", k, data)
			}
		}
		b := siteFieldS071(root, "body")
		if got := b.Style == yaml.LiteralStyle; got != tc.literal {
			t.Errorf("body %q: literal = %v, want %v:\n%s", tc.body, got, tc.literal, data)
		}
		if strings.TrimRight(b.Value, "\n") != tc.body {
			t.Errorf("body %q read back as %q", tc.body, b.Value)
		}
	}
}
