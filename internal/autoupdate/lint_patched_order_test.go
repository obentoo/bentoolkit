package autoupdate

import (
	"regexp"
	"strings"
	"testing"
)

// patchedOrderKeyRegex captures the bare key of a rendered TOML assignment, so
// the emitted field sequence can be read off RenderRecord's output.
var patchedOrderKeyRegex = regexp.MustCompile(`(?m)^([a-z0-9_]+)\s*=`)

// TestCanonicalFieldOrderPlacesPatchedAfterType pins R1.4: `patched` has ONE
// agreed position in a record — immediately after `type` — so the linter, the
// repair pass and `overlay analyze` all write it in the same place.
//
// The placement is asserted three times over, at each of the three surfaces
// CanonicalFieldOrder feeds, because agreeing in the slice but disagreeing in
// what is emitted or what is flagged is exactly the drift the single source of
// order exists to prevent:
//
//  1. the slice itself — one occurrence, ranked directly after `type`;
//  2. RenderRecord — the generator emits it there;
//  3. LintPackagesConfig — a record that writes it elsewhere is reported as
//     misordered, and a record that writes it there is not.
//
// `type` is the neighbour on purpose: both classify the package rather than
// describe how to probe it. The position is free of churn today (no record
// carries the field), which is why it must be chosen deliberately now — every
// later edit to the order is, in CanonicalFieldOrder's own words, "a churn bill
// payable in records".
func TestCanonicalFieldOrderPlacesPatchedAfterType(t *testing.T) {
	t.Run("the order slice ranks patched immediately after type", func(t *testing.T) {
		typeIdx, patchedIdx, patchedCount := -1, -1, 0
		for i, f := range CanonicalFieldOrder {
			switch f {
			case "type":
				typeIdx = i
			case "patched":
				patchedIdx = i
				patchedCount++
			}
		}

		if patchedCount != 1 {
			t.Fatalf("CanonicalFieldOrder lists %q %d time(s), want exactly 1", "patched", patchedCount)
		}
		if typeIdx < 0 {
			t.Fatal("CanonicalFieldOrder no longer lists type; the placement rule has no anchor")
		}
		if patchedIdx != typeIdx+1 {
			lo, hi := max(typeIdx-1, 0), min(typeIdx+3, len(CanonicalFieldOrder))
			t.Errorf("patched is at index %d, type at %d; patched must sit immediately after type (want index %d). Neighbours are %v",
				patchedIdx, typeIdx, typeIdx+1, CanonicalFieldOrder[lo:hi])
		}
	})

	t.Run("RenderRecord emits patched between type and series", func(t *testing.T) {
		cfg := &PackageConfig{
			URL:      "https://api.github.com/repos/zed-industries/zed/releases/latest",
			Parser:   "json",
			Path:     "tag_name",
			Type:     "source",
			Patched:  "keeps our wayland-by-default patch",
			Series:   `^0\.2`,
			Comments: "zed — carries a local patch that must survive every bump.",
		}

		rendered := RenderRecord("app-editors/zed", cfg)

		var keys []string
		for _, m := range patchedOrderKeyRegex.FindAllStringSubmatch(rendered, -1) {
			keys = append(keys, m[1])
		}
		typeAt := patchedOrderIndexOf(keys, "type")
		patchedAt := patchedOrderIndexOf(keys, "patched")
		seriesAt := patchedOrderIndexOf(keys, "series")
		if patchedAt < 0 {
			t.Fatalf("RenderRecord emitted no patched line; a field the generator never writes cannot be ordered.\n--- rendered ---\n%s", rendered)
		}
		if typeAt < 0 || seriesAt < 0 {
			t.Fatalf("RenderRecord emitted keys %v; expected both type and series", keys)
		}
		if patchedAt != typeAt+1 || seriesAt != patchedAt+1 {
			t.Errorf("emitted key order is %v; want type, patched, series adjacent in that sequence.\n--- rendered ---\n%s", keys, rendered)
		}
	})

	t.Run("the linter accepts patched after type and reports it before type", func(t *testing.T) {
		wellOrdered := writeRegistry(t, `["app-editors/zed"]
url = "https://api.github.com/repos/zed-industries/zed/releases/latest"
parser = "json"
path = "tag_name"
type = "source"
patched = "keeps our wayland-by-default patch"
comments = """
zed — carries a local patch that must survive every bump.
"""
# END
`)

		issues, err := LintPackagesConfig(wellOrdered)
		if err != nil {
			t.Fatalf("LintPackagesConfig on a canonically ordered record returned %v; patched must be a key the registry claims", err)
		}
		for _, iss := range issues {
			if iss.Rule == LintFieldOrder || iss.Rule == LintUnknownField {
				t.Errorf("unexpected issue on a canonically ordered record: %s", iss)
			}
		}

		misordered := writeRegistry(t, `["app-editors/zed"]
url = "https://api.github.com/repos/zed-industries/zed/releases/latest"
parser = "json"
path = "tag_name"
patched = "keeps our wayland-by-default patch"
type = "source"
comments = """
zed — carries a local patch that must survive every bump.
"""
# END
`)

		issues, err = LintPackagesConfig(misordered)
		if err != nil {
			t.Fatalf("LintPackagesConfig on a misordered record returned %v, want nil (the file still loads)", err)
		}
		var found *LintIssue
		for i := range issues {
			if issues[i].Rule == LintFieldOrder && issues[i].Package == "app-editors/zed" {
				found = &issues[i]
				break
			}
		}
		if found == nil {
			t.Fatalf("patched written before type was not reported as %s; issues: %v", LintFieldOrder, issues)
		}
		if found.Fix != FixReorderFields {
			t.Errorf("field-order issue offers Fix %q, want %q", found.Fix, FixReorderFields)
		}
	})
}

// patchedOrderIndexOf returns the position of key in keys, or -1.
func patchedOrderIndexOf(keys []string, key string) int {
	for i, k := range keys {
		if strings.EqualFold(k, key) {
			return i
		}
	}
	return -1
}
