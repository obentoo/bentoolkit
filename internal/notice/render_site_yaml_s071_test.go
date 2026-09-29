package notice

// Story 071, sub-task 3.2: RenderSiteYAML emits the site notice schema
// (site story 002 "Data Models"): id, type, severity, title, summary, body,
// affects (cp, optional slot, ranges of op/ver), published, updated — in that
// order, with quoted RFC 3339 timestamps (R4.1).
//
// Hostile halves first. The site's YAML parser types every plain scalar, so a
// version `1.10` emitted bare is read back as the float 1.1 — the same value as
// version `1.1`, collapsing two different versions into one. A slot `1` read
// back as an integer, and a timestamp read back as a date, are the same class
// of defect. Every one of these scalars must round-trip as a STRING.

import (
	"reflect"
	"strings"
	"testing"
	"time"

	"gopkg.in/yaml.v3"
)

func siteNoticeS071() Notice {
	pub := time.Date(2026, 10, 2, 14, 0, 0, 0, time.UTC)
	return Notice{
		ID:        "2026-10-02-foo-cve",
		Type:      "security",
		Severity:  "critical",
		Title:     "foo: 1.2 \"heap\" overflow",
		Summary:   "One-paragraph summary: with a colon.",
		Author:    "Jane Doe <jane@example.org>",
		Body:      "First paragraph.\n\n# not a comment\n  indented line\nkey: value",
		Published: pub,
		Updated:   pub.Add(26 * time.Hour),
		Revision:  2,
		Affects: []Affects{
			{CP: "dev-libs/foo", Slot: "1", Ranges: []Range{{Op: ">=", Ver: "1.10"}, {Op: "<", Ver: "2"}}},
			{CP: "dev-libs/bar"},
		},
	}
}

func siteRootS071(t *testing.T, data []byte) *yaml.Node {
	t.Helper()
	var doc yaml.Node
	if err := yaml.Unmarshal(data, &doc); err != nil {
		t.Fatalf("the site YAML does not parse: %v\n%s", err, data)
	}
	if doc.Kind != yaml.DocumentNode || len(doc.Content) != 1 || doc.Content[0].Kind != yaml.MappingNode {
		t.Fatalf("the site YAML is not a single mapping:\n%s", data)
	}
	return doc.Content[0]
}

func siteFieldS071(m *yaml.Node, key string) *yaml.Node {
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Value == key {
			return m.Content[i+1]
		}
	}
	return nil
}

func TestRenderSiteYAML_VersionsSlotsAndTimestampsStayStrings(t *testing.T) {
	data, err := RenderSiteYAML(siteNoticeS071())
	if err != nil {
		t.Fatalf("RenderSiteYAML: %v", err)
	}
	root := siteRootS071(t, data)

	affects := siteFieldS071(root, "affects")
	if affects == nil || affects.Kind != yaml.SequenceNode || len(affects.Content) != 2 {
		t.Fatalf("affects is not a two-entry sequence:\n%s", data)
	}
	first := affects.Content[0]
	if slot := siteFieldS071(first, "slot"); slot == nil || slot.ShortTag() != "!!str" || slot.Value != "1" {
		t.Errorf("slot does not round-trip as the string \"1\": %+v\n%s", slot, data)
	}
	ranges := siteFieldS071(first, "ranges")
	if ranges == nil || ranges.Kind != yaml.SequenceNode || len(ranges.Content) != 2 {
		t.Fatalf("ranges is not a two-entry sequence:\n%s", data)
	}
	for i, want := range []Range{{Op: ">=", Ver: "1.10"}, {Op: "<", Ver: "2"}} {
		op := siteFieldS071(ranges.Content[i], "op")
		ver := siteFieldS071(ranges.Content[i], "ver")
		if op == nil || op.ShortTag() != "!!str" || op.Value != want.Op {
			t.Errorf("range %d op = %+v, want the string %q", i, op, want.Op)
		}
		if ver == nil || ver.ShortTag() != "!!str" || ver.Value != want.Ver {
			t.Errorf("range %d ver = %+v, want the string %q (a bare 1.10 reads back as the float 1.1)", i, ver, want.Ver)
		}
	}

	for key, want := range map[string]string{
		"published": "2026-10-02T14:00:00Z",
		"updated":   "2026-10-03T16:00:00Z",
	} {
		v := siteFieldS071(root, key)
		if v == nil || v.Value != want {
			t.Errorf("%s = %+v, want %q", key, v, want)
			continue
		}
		if v.Style&(yaml.DoubleQuotedStyle|yaml.SingleQuotedStyle) == 0 || v.ShortTag() != "!!str" {
			t.Errorf("%s is not a quoted string (style %v, tag %s); a bare timestamp is typed as a date", key, v.Style, v.ShortTag())
		}
	}
}

func TestRenderSiteYAML_SchemaFieldsInOrder(t *testing.T) {
	data, err := RenderSiteYAML(siteNoticeS071())
	if err != nil {
		t.Fatalf("RenderSiteYAML: %v", err)
	}
	root := siteRootS071(t, data)

	var keys []string
	for i := 0; i < len(root.Content); i += 2 {
		keys = append(keys, root.Content[i].Value)
	}
	want := []string{"id", "type", "severity", "title", "summary", "body", "affects", "published", "updated"}
	if !reflect.DeepEqual(keys, want) {
		t.Errorf("top-level keys = %q, want exactly %q (no author, no revision: the schema has neither)", keys, want)
	}

	// The slot key is omitted when empty — never null, never "".
	affects := siteFieldS071(root, "affects")
	if affects != nil && len(affects.Content) == 2 {
		if slot := siteFieldS071(affects.Content[1], "slot"); slot != nil {
			t.Errorf("an entry with no slot carries a slot key (%q)", slot.Value)
		}
		var entryKeys []string
		for i := 0; i < len(affects.Content[0].Content); i += 2 {
			entryKeys = append(entryKeys, affects.Content[0].Content[i].Value)
		}
		if !reflect.DeepEqual(entryKeys, []string{"cp", "slot", "ranges"}) {
			t.Errorf("affects entry keys = %q, want [cp slot ranges]", entryKeys)
		}
	}
}

func TestRenderSiteYAML_ValuesRoundTrip(t *testing.T) {
	n := siteNoticeS071()
	data, err := RenderSiteYAML(n)
	if err != nil {
		t.Fatalf("RenderSiteYAML: %v", err)
	}
	var got struct {
		ID, Type, Severity, Title, Summary, Body string
		Affects                                  []struct {
			CP     string `yaml:"cp"`
			Slot   string `yaml:"slot"`
			Ranges []struct{ Op, Ver string }
		}
	}
	if err := yaml.Unmarshal(data, &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.ID != n.ID || got.Type != n.Type || got.Severity != n.Severity || got.Title != n.Title || got.Summary != n.Summary {
		t.Errorf("scalar fields did not round-trip: %+v", got)
	}
	if strings.TrimRight(got.Body, "\n") != n.Body {
		t.Errorf("body did not round-trip:\n got %q\nwant %q", got.Body, n.Body)
	}
	root := siteRootS071(t, data)
	if b := siteFieldS071(root, "body"); b == nil || b.Style != yaml.LiteralStyle {
		t.Errorf("body is not written as a literal block:\n%s", data)
	}
	if len(got.Affects) != 2 || got.Affects[0].CP != "dev-libs/foo" || got.Affects[1].CP != "dev-libs/bar" ||
		len(got.Affects[0].Ranges) != 2 || got.Affects[0].Ranges[0].Ver != "1.10" {
		t.Errorf("affects did not round-trip: %+v", got.Affects)
	}
}

func TestRenderSiteYAML_NoPackageIsAnEmptyList(t *testing.T) {
	n := siteNoticeS071()
	n.Type = "news"
	n.Affects = nil
	data, err := RenderSiteYAML(n)
	if err != nil {
		t.Fatalf("RenderSiteYAML: %v", err)
	}
	a := siteFieldS071(siteRootS071(t, data), "affects")
	if a == nil || a.Kind != yaml.SequenceNode || len(a.Content) != 0 {
		t.Errorf("a notice with no affected package must carry affects: [], got:\n%s", data)
	}
}

// R4.1: a notice made by `notice new` is dated at midnight UTC of its
// --published date, for both published and updated. Hostile half: the time
// of day New ran at (an evening in UTC-3, already the next day in UTC) must
// not leak into either timestamp.
func TestRenderSiteYAML_NewNoticeIsDatedMidnightUTC(t *testing.T) {
	in := Input{
		Type: "news", Severity: "info", Name: "midnight", Title: "t", Summary: "s",
		Author: "A <a@b.c>", Body: "b", Affects: []string{"dev-libs/foo >=1.0"},
	}
	evening := time.Date(2026, 10, 2, 23, 30, 0, 0, time.FixedZone("UTC-3", -3*3600))
	n, err := New(in, evening)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	data, err := RenderSiteYAML(n)
	if err != nil {
		t.Fatalf("RenderSiteYAML: %v", err)
	}
	root := siteRootS071(t, data)
	for _, key := range []string{"published", "updated"} {
		v := siteFieldS071(root, key)
		if v == nil || v.Value != "2026-10-03T00:00:00Z" {
			t.Errorf("%s = %+v, want \"2026-10-03T00:00:00Z\" (midnight UTC of the UTC date)", key, v)
			continue
		}
		if v.Style&(yaml.DoubleQuotedStyle|yaml.SingleQuotedStyle) == 0 {
			t.Errorf("%s is not quoted:\n%s", key, data)
		}
	}
	affects := siteFieldS071(root, "affects")
	if affects == nil || len(affects.Content) != 1 {
		t.Fatalf("affects is not a one-entry sequence:\n%s", data)
	}
	if slot := siteFieldS071(affects.Content[0], "slot"); slot != nil {
		t.Errorf("an entry with no slot carries slot: %q", slot.Value)
	}
	if strings.Contains(string(data), "slot:") {
		t.Errorf("the document mentions slot although no entry has one:\n%s", data)
	}
}
