package notices_test

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/obentoo/bentoolkit/internal/common/ebuild"
	"github.com/obentoo/bentoolkit/internal/notices"
)

// feedDoc returns a minimal valid JSON Feed 1.1 + _bentoo document as a
// generic map, so each test can break exactly one field.
func feedDoc() map[string]any {
	return map[string]any{
		"version":       "https://jsonfeed.org/version/1.1",
		"title":         "bentoo notices",
		"home_page_url": "https://obentoo.org/notices/",
		"feed_url":      "https://obentoo.org/notices.json",
		"language":      "en",
		"_bentoo":       map[string]any{"serial": 1790812800, "expires": "2026-10-31T00:00:00Z"},
		"items": []any{
			map[string]any{
				"id":             "2026-10-02-foo-cve",
				"url":            "https://obentoo.org/notices/2026-10-02-foo-cve/",
				"title":          "foo <1.2.3> & heap overflow",
				"summary":        "Upgrade foo.",
				"content_text":   "Body.",
				"date_published": "2026-10-02T14:00:00Z",
				"date_modified":  "2026-10-03T15:30:00Z",
				"_bentoo": map[string]any{
					"type":     "security",
					"severity": "critical",
					"affects": []any{
						map[string]any{
							"cp":   "dev-libs/foo",
							"slot": "1",
							"ranges": []any{
								map[string]any{"op": ">=", "ver": "1.0"},
								map[string]any{"op": "<", "ver": "1.2.3"},
							},
						},
					},
				},
			},
		},
	}
}

func item0(doc map[string]any) map[string]any {
	return doc["items"].([]any)[0].(map[string]any)
}

func bentoo0(doc map[string]any) map[string]any {
	return item0(doc)["_bentoo"].(map[string]any)
}

func affects0(doc map[string]any) map[string]any {
	return bentoo0(doc)["affects"].([]any)[0].(map[string]any)
}

func range0(doc map[string]any, i int) map[string]any {
	return affects0(doc)["ranges"].([]any)[i].(map[string]any)
}

func encode(t *testing.T, doc map[string]any) []byte {
	t.Helper()
	b, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// TestParseFeed_MapsEveryField pins the mapping from the wire format to the
// Notice and Feed types.
func TestParseFeed_MapsEveryField(t *testing.T) {
	f, err := notices.ParseFeed(encode(t, feedDoc()))
	if err != nil {
		t.Fatalf("ParseFeed of a valid document: %v", err)
	}
	if f.Serial != 1790812800 {
		t.Errorf("Serial = %d, want 1790812800", f.Serial)
	}
	if want := time.Date(2026, 10, 31, 0, 0, 0, 0, time.UTC); !f.Expires.Equal(want) {
		t.Errorf("Expires = %v, want %v", f.Expires, want)
	}
	if len(f.Items) != 1 {
		t.Fatalf("Items = %d, want 1", len(f.Items))
	}
	n := f.Items[0]
	if n.ID != "2026-10-02-foo-cve" || n.URL != "https://obentoo.org/notices/2026-10-02-foo-cve/" {
		t.Errorf("ID/URL = %q/%q", n.ID, n.URL)
	}
	// Strings are carried verbatim: escaping is the notifier's job, and doing it
	// here as well would double-escape.
	if n.Title != "foo <1.2.3> & heap overflow" || n.Summary != "Upgrade foo." {
		t.Errorf("Title/Summary = %q/%q, want them verbatim", n.Title, n.Summary)
	}
	if n.Type != "security" || n.Severity != "critical" {
		t.Errorf("Type/Severity = %q/%q", n.Type, n.Severity)
	}
	if !n.Published.Equal(time.Date(2026, 10, 2, 14, 0, 0, 0, time.UTC)) ||
		!n.Updated.Equal(time.Date(2026, 10, 3, 15, 30, 0, 0, time.UTC)) {
		t.Errorf("Published/Updated = %v/%v", n.Published, n.Updated)
	}
	if n.Source != notices.SourceFeed {
		t.Errorf("Source = %v, want SourceFeed", n.Source)
	}
	want := []notices.Affects{{
		CP: "dev-libs/foo", Slot: "1",
		Ranges: []notices.Range{{Op: ">=", Ver: "1.0"}, {Op: "<", Ver: "1.2.3"}},
	}}
	if len(n.Affects) != 1 || n.Affects[0].CP != want[0].CP || n.Affects[0].Slot != want[0].Slot ||
		len(n.Affects[0].Ranges) != 2 || n.Affects[0].Ranges[0] != want[0].Ranges[0] || n.Affects[0].Ranges[1] != want[0].Ranges[1] {
		t.Errorf("Affects = %+v, want %+v", n.Affects, want)
	}
}

// TestParseFeed_OptionalShapesAreAccepted: slot omitted, affects empty, a
// range-less entry, and JSON Feed fields this client does not read.
func TestParseFeed_OptionalShapesAreAccepted(t *testing.T) {
	doc := feedDoc()
	delete(affects0(doc), "slot")
	affects0(doc)["ranges"] = []any{}
	item0(doc)["authors"] = []any{map[string]any{"name": "bentoo"}}
	item0(doc)["tags"] = []any{"x"}
	doc["icon"] = "https://obentoo.org/icon.png"
	f, err := notices.ParseFeed(encode(t, doc))
	if err != nil {
		t.Fatalf("ParseFeed: %v", err)
	}
	if len(f.Items) != 1 || len(f.Items[0].Affects) != 1 {
		t.Fatalf("Items = %+v, want one item with one affects entry", f.Items)
	}
	if a := f.Items[0].Affects[0]; a.Slot != "" || len(a.Ranges) != 0 {
		t.Errorf("Affects[0] = %+v, want no slot and no ranges", a)
	}

	doc = feedDoc()
	bentoo0(doc)["affects"] = []any{}
	bentoo0(doc)["type"] = "announcement"
	bentoo0(doc)["severity"] = "info"
	f, err = notices.ParseFeed(encode(t, doc))
	if err != nil {
		t.Fatalf("ParseFeed with empty affects: %v", err)
	}
	if len(f.Items) != 1 {
		t.Fatalf("Items = %+v, want one item", f.Items)
	}
	if len(f.Items[0].Affects) != 0 {
		t.Errorf("Affects = %+v, want empty", f.Items[0].Affects)
	}
}

// TestParseFeed_EmptyItemsIsAValidFeed: a feed with no notices is legal and
// still carries serial and expiry.
func TestParseFeed_EmptyItemsIsAValidFeed(t *testing.T) {
	doc := feedDoc()
	doc["items"] = []any{}
	f, err := notices.ParseFeed(encode(t, doc))
	if err != nil {
		t.Fatalf("ParseFeed of an empty feed: %v", err)
	}
	if len(f.Items) != 0 || f.Serial != 1790812800 {
		t.Errorf("Feed = %+v", f)
	}
}

// TestParseFeed_AcceptsTheGoldenFixture is the parser half of Q13: the site's
// golden fixture (a copy lives in testdata/) parses without error.
func TestParseFeed_AcceptsTheGoldenFixture(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("testdata", "notices.golden.json"))
	if err != nil {
		t.Fatal(err)
	}
	f, err := notices.ParseFeed(data)
	if err != nil {
		t.Fatalf("ParseFeed rejected the site's golden fixture: %v", err)
	}
	if len(f.Items) != 4 {
		t.Errorf("golden fixture yielded %d items, want 4", len(f.Items))
	}
	// The shared edge notice exercises every corner of the cp, slot and version
	// patterns at once; the site, bentoo notice new and ParseFeed must all
	// accept it.
	var edge *notices.Notice
	for i := range f.Items {
		if f.Items[i].ID == "2026-09-28-edge+case_1" {
			edge = &f.Items[i]
		}
	}
	if edge == nil {
		t.Fatalf("the edge notice 2026-09-28-edge+case_1 is missing from %d items", len(f.Items))
	}
	want := notices.Affects{CP: "dev-libs/libfoo+", Slot: "0.1",
		Ranges: []notices.Range{{Op: ">=", Ver: "1.0_rc1_p2"}, {Op: "<", Ver: "1.0b-r0"}}}
	if len(edge.Affects) != 1 || edge.Affects[0].CP != want.CP || edge.Affects[0].Slot != want.Slot ||
		len(edge.Affects[0].Ranges) != 2 || edge.Affects[0].Ranges[0] != want.Ranges[0] || edge.Affects[0].Ranges[1] != want.Ranges[1] {
		t.Errorf("edge affects = %+v, want %+v", edge.Affects, want)
	}
}

// TestParseFeed_RejectsDocumentLevelViolations: each is not a bentoo notices
// feed and must be reported as ErrInvalidFeed.
func TestParseFeed_RejectsDocumentLevelViolations(t *testing.T) {
	cases := map[string]func() []byte{
		"not json": func() []byte { return []byte("<html>captive portal</html>") },
		"empty":    func() []byte { return nil },
		"json array": func() []byte {
			return []byte(`[]`)
		},
		"wrong version": func() []byte {
			d := feedDoc()
			d["version"] = "https://jsonfeed.org/version/1"
			return encode(t, d)
		},
		"missing version": func() []byte {
			d := feedDoc()
			delete(d, "version")
			return encode(t, d)
		},
		"missing _bentoo": func() []byte {
			d := feedDoc()
			delete(d, "_bentoo")
			return encode(t, d)
		},
		"zero serial": func() []byte {
			d := feedDoc()
			d["_bentoo"] = map[string]any{"serial": 0, "expires": "2026-10-31T00:00:00Z"}
			return encode(t, d)
		},
		"negative serial": func() []byte {
			d := feedDoc()
			d["_bentoo"] = map[string]any{"serial": -5, "expires": "2026-10-31T00:00:00Z"}
			return encode(t, d)
		},
		"missing expires": func() []byte {
			d := feedDoc()
			d["_bentoo"] = map[string]any{"serial": 1790812800}
			return encode(t, d)
		},
		"unparsable expires": func() []byte {
			d := feedDoc()
			d["_bentoo"] = map[string]any{"serial": 1790812800, "expires": "next month"}
			return encode(t, d)
		},
		"items not an array": func() []byte {
			d := feedDoc()
			d["items"] = map[string]any{}
			return encode(t, d)
		},
	}
	for name, build := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := notices.ParseFeed(build())
			if err == nil {
				t.Fatal("ParseFeed accepted it")
			}
			if !errors.Is(err, notices.ErrInvalidFeed) {
				t.Errorf("err = %v, want it to wrap ErrInvalidFeed", err)
			}
		})
	}
}

// TestParseFeed_RejectsItemViolationsNamingIDAndField is R2.6: an item that
// breaks the contract is reported with the item ID and the failing field.
func TestParseFeed_RejectsItemViolationsNamingIDAndField(t *testing.T) {
	const id = "2026-10-02-foo-cve"
	cases := []struct {
		name, wantID, wantField string
		mutate                  func(d map[string]any)
	}{
		{"unknown type", id, "type", func(d map[string]any) { bentoo0(d)["type"] = "advisory" }},
		{"type in another case", id, "type", func(d map[string]any) { bentoo0(d)["type"] = "Security" }},
		{"missing type", id, "type", func(d map[string]any) { delete(bentoo0(d), "type") }},
		{"unknown severity", id, "severity", func(d map[string]any) { bentoo0(d)["severity"] = "urgent" }},
		{"cp without category", id, "cp", func(d map[string]any) { affects0(d)["cp"] = "foo" }},
		{"cp with three parts", id, "cp", func(d map[string]any) { affects0(d)["cp"] = "dev-libs/foo/bar" }},
		{"cp with a version", id, "cp", func(d map[string]any) { affects0(d)["cp"] = ">=dev-libs/foo-1.0" }},
		{"slot with a space", id, "slot", func(d map[string]any) { affects0(d)["slot"] = "1 2" }},
		{"slot with a subslot", id, "slot", func(d map[string]any) { affects0(d)["slot"] = "1/2" }},
		{"unknown operator", id, "op", func(d map[string]any) { range0(d, 0)["op"] = "~>" }},
		{"empty operator", id, "op", func(d map[string]any) { range0(d, 0)["op"] = "" }},
		{"non-PMS version", id, "ver", func(d map[string]any) { range0(d, 1)["ver"] = "1.2.3-beta" }},
		{"prefixed version", id, "ver", func(d map[string]any) { range0(d, 1)["ver"] = "v1.2.3" }},
		{"empty version", id, "ver", func(d map[string]any) { range0(d, 1)["ver"] = "" }},
		{"bad published date", id, "publish", func(d map[string]any) { item0(d)["date_published"] = "yesterday" }},
		{"uppercase short name", "2026-10-02-Foo", "id", func(d map[string]any) { item0(d)["id"] = "2026-10-02-Foo" }},
		{"impossible date", "2026-02-30-foo", "id", func(d map[string]any) { item0(d)["id"] = "2026-02-30-foo" }},
		{"month 13", "2026-13-01-foo", "id", func(d map[string]any) { item0(d)["id"] = "2026-13-01-foo" }},
		{"short name too long", "2026-10-02-abcdefghijklmnopqrstu", "id", func(d map[string]any) {
			item0(d)["id"] = "2026-10-02-abcdefghijklmnopqrstu" // 21 characters
		}},
		{"path traversal", "../../etc/passwd", "id", func(d map[string]any) { item0(d)["id"] = "../../etc/passwd" }},
		{"no date prefix", "foo-cve", "id", func(d map[string]any) { item0(d)["id"] = "foo-cve" }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			d := feedDoc()
			tc.mutate(d)
			_, err := notices.ParseFeed(encode(t, d))
			if err == nil {
				t.Fatal("ParseFeed accepted the violation")
			}
			if !errors.Is(err, notices.ErrInvalidFeed) {
				t.Errorf("err = %v, want it to wrap ErrInvalidFeed", err)
			}
			var ie *notices.ItemError
			if !errors.As(err, &ie) {
				t.Fatalf("err = %v, want an *ItemError", err)
			}
			if ie.ID != tc.wantID {
				t.Errorf("ItemError.ID = %q, want %q", ie.ID, tc.wantID)
			}
			if !strings.Contains(ie.Field, tc.wantField) {
				t.Errorf("ItemError.Field = %q, want it to name %q", ie.Field, tc.wantField)
			}
			if !strings.Contains(err.Error(), tc.wantID) {
				t.Errorf("error text %q does not carry the item ID", err)
			}
		})
	}
}

// TestParseFeed_DuplicateIDIsRejected: the ID is the notice's identity (read
// state is keyed by it). Two items with one ID cannot both be honoured.
func TestParseFeed_DuplicateIDIsRejected(t *testing.T) {
	d := feedDoc()
	items := d["items"].([]any)
	clone := map[string]any{}
	raw, _ := json.Marshal(items[0])
	_ = json.Unmarshal(raw, &clone)
	clone["title"] = "a different notice with the same id"
	d["items"] = append(items, clone)
	_, err := notices.ParseFeed(encode(t, d))
	var ie *notices.ItemError
	if !errors.As(err, &ie) || ie.ID != "2026-10-02-foo-cve" || !strings.Contains(ie.Field, "id") {
		t.Fatalf("ParseFeed of two items sharing an ID: err = %v, want an ItemError naming the ID and field id", err)
	}
}

// TestParseFeed_NearIdenticalIDsStayDistinct is the converse of the duplicate
// rule: IDs that merely share a prefix, or differ only in a '+', are different
// notices, and a leap day is a real calendar date.
func TestParseFeed_NearIdenticalIDsStayDistinct(t *testing.T) {
	d := feedDoc()
	items := d["items"].([]any)
	for _, id := range []string{"2026-10-02-foo", "2026-10-02-foo-cve2", "2026-10-02-foo+cve", "2028-02-29-leap"} {
		clone := map[string]any{}
		raw, _ := json.Marshal(items[0])
		_ = json.Unmarshal(raw, &clone)
		clone["id"] = id
		items = append(items, clone)
	}
	d["items"] = items
	f, err := notices.ParseFeed(encode(t, d))
	if err != nil {
		t.Fatalf("ParseFeed rejected distinct IDs: %v", err)
	}
	if len(f.Items) != 5 {
		t.Errorf("Items = %d, want 5 distinct notices", len(f.Items))
	}
}

// TestParseFeed_AcceptsEveryEnumOperatorAndVersion is the converse of the
// rejection table: a validator that rejects too much would pass it.
func TestParseFeed_AcceptsEveryEnumOperatorAndVersion(t *testing.T) {
	for _, typ := range []string{"security", "release", "news", "announcement"} {
		for _, sev := range []string{"info", "warning", "critical"} {
			d := feedDoc()
			bentoo0(d)["type"] = typ
			bentoo0(d)["severity"] = sev
			if _, err := notices.ParseFeed(encode(t, d)); err != nil {
				t.Errorf("type %q severity %q rejected: %v", typ, sev, err)
			}
		}
	}
	for _, op := range []string{"<", "<=", "=", ">=", ">"} {
		d := feedDoc()
		range0(d, 0)["op"] = op
		if _, err := notices.ParseFeed(encode(t, d)); err != nil {
			t.Errorf("operator %q rejected: %v", op, err)
		}
	}
	for _, ver := range []string{"1", "1.2.3a", "1.0_alpha1_p2", "2.0-r3", "9999", "1.0_rc", "1.0_rc1_p2", "1.0b-r0"} {
		d := feedDoc()
		range0(d, 1)["ver"] = ver
		if _, err := notices.ParseFeed(encode(t, d)); err != nil {
			t.Errorf("version %q rejected: %v", ver, err)
		}
	}
	for _, slot := range []string{"0", "0.1", "1.2", "a+b_c-d"} {
		d := feedDoc()
		affects0(d)["slot"] = slot
		if _, err := notices.ParseFeed(encode(t, d)); err != nil {
			t.Errorf("slot %q rejected: %v", slot, err)
		}
	}
}

// TestParseAffectsRange_AcceptsEveryOperator and its rejection sibling pin
// the range constructor on its own.
func TestParseAffectsRange_AcceptsEveryOperator(t *testing.T) {
	for _, op := range []string{"<", "<=", "=", ">=", ">"} {
		r, err := notices.ParseAffectsRange(op, "1.2.3-r1")
		if err != nil {
			t.Errorf("ParseAffectsRange(%q): %v", op, err)
			continue
		}
		if r.Op != op || r.Ver != "1.2.3-r1" {
			t.Errorf("ParseAffectsRange(%q) = %+v", op, r)
		}
	}
}

func TestParseAffectsRange_RejectsBadOperatorsAndVersions(t *testing.T) {
	cases := [][2]string{
		{"==", "1.0"}, {"!=", "1.0"}, {"~", "1.0"}, {"", "1.0"}, {"=<", "1.0"},
		{"<", ""}, {"<", "latest"}, {"<", "v1.0"}, {"<", "1.0-beta"}, {"<", " 1.0 x"},
	}
	for _, c := range cases {
		if r, err := notices.ParseAffectsRange(c[0], c[1]); err == nil {
			t.Errorf("ParseAffectsRange(%q, %q) = %+v, want an error", c[0], c[1], r)
		}
	}
}

// FuzzParseFeed: arbitrary bytes never panic, and whatever is accepted honours
// the contract.
func FuzzParseFeed(f *testing.F) {
	if golden, err := os.ReadFile(filepath.Join("testdata", "notices.golden.json")); err == nil {
		f.Add(golden)
	}
	f.Add([]byte(`{}`))
	f.Add([]byte(`{"version":"https://jsonfeed.org/version/1.1","_bentoo":{"serial":1,"expires":"2026-01-01T00:00:00Z"},"items":[]}`))
	f.Add([]byte(`null`))
	f.Fuzz(func(t *testing.T, data []byte) {
		feed, err := notices.ParseFeed(data)
		if err != nil {
			return
		}
		if feed.Serial <= 0 {
			t.Errorf("accepted serial %d", feed.Serial)
		}
		seen := map[string]bool{}
		for _, n := range feed.Items {
			if seen[n.ID] {
				t.Errorf("accepted duplicate ID %q", n.ID)
			}
			seen[n.ID] = true
			switch n.Type {
			case "security", "release", "news", "announcement":
			default:
				t.Errorf("accepted type %q", n.Type)
			}
			switch n.Severity {
			case "info", "warning", "critical":
			default:
				t.Errorf("accepted severity %q", n.Severity)
			}
			if strings.ContainsAny(n.ID, "/\\") || strings.Contains(n.ID, "..") {
				t.Errorf("accepted ID %q", n.ID)
			}
			for _, a := range n.Affects {
				for _, r := range a.Ranges {
					if !ebuild.IsValidVersion(r.Ver) {
						t.Errorf("accepted range bound %q", r.Ver)
					}
				}
			}
		}
	})
}

// FuzzParseAffectsRange: never panics; an accepted range has a contract
// operator and a valid PMS version.
func FuzzParseAffectsRange(f *testing.F) {
	f.Add("<", "1.0")
	f.Add(">=", "1.2.3_p1-r2")
	f.Add("~", "x")
	f.Fuzz(func(t *testing.T, op, ver string) {
		r, err := notices.ParseAffectsRange(op, ver)
		if err != nil {
			return
		}
		switch r.Op {
		case "<", "<=", "=", ">=", ">":
		default:
			t.Errorf("accepted operator %q", r.Op)
		}
		if !ebuild.IsValidVersion(r.Ver) {
			t.Errorf("accepted version %q", r.Ver)
		}
	})
}
