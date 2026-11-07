package registry

// Authored for bentoo story 003 (autoupdate cooldown), sub-task 2.1 —
// R4.4, R5.2 (and the field half of R5.1).
//
// Contract: design.md "Registry fields" — `cooldown_with` ([]string of
// cat/pkg: every target has a record, the relation is symmetric, no
// self-reference, sorted) and `cooldown_exempt` (a non-empty reason; an empty
// string is a load error), both appended to CanonicalFieldOrder after
// `published_path`, rendered and repaired like every other field. Field names
// per the refined contract: CooldownWith []string, CooldownExempt *string.
//
// "The registry load" is what every caller runs: decodePackagesConfig followed
// by ValidateAll. A rule enforced by either half satisfies these tests.
//
// Hostile halves first: a target that is a PREFIX-named package of an
// existing record must not be resolved to it; a group of THREE where one
// member reciprocates only one of its two partners (a pair check would pass);
// and an unsorted list.

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// cooldownRecord is a minimal valid record plus extra lines.
func cooldownRecord(key, extra string) string {
	return `["` + key + `"]
url = "https://example.org/` + key + `/releases"
parser = "regex"
pattern = 'v([0-9.]+)'
` + extra + "\n"
}

// loadRegistry runs the registry load as callers do: strict decode, then the
// whole-registry validation.
func loadRegistry(text string) (*PackagesConfig, error) {
	cfg, err := decodePackagesConfig([]byte(text))
	if err != nil {
		return nil, err
	}
	if err := cfg.ValidateAll(nil); err != nil {
		return cfg, err
	}
	return cfg, nil
}

func requireErrorNaming(t *testing.T, err error, names ...string) {
	t.Helper()
	if err == nil {
		t.Fatalf("the registry loaded; want an error naming %v", names)
	}
	if missing := unnamed(err.Error(), names...); len(missing) > 0 {
		t.Errorf("the error does not name %v: %v", missing, err)
	}
}

// unnamed returns the names text does not mention. Longer names are matched
// and removed first, so "sys-apps/ai-jail-bin" never counts as a mention of
// "sys-apps/ai-jail".
func unnamed(text string, names ...string) []string {
	sorted := append([]string(nil), names...)
	sort.Slice(sorted, func(i, j int) bool { return len(sorted[i]) > len(sorted[j]) })
	var missing []string
	for _, n := range sorted {
		if !strings.Contains(text, n) {
			missing = append(missing, n)
		}
		text = strings.ReplaceAll(text, n, "")
	}
	return missing
}

// TestCooldownExempt_AnEmptyReasonFailsAndAReasonLoads: R5.2, hostile half
// first — `cooldown_exempt = ""` fails the load naming the record — then the
// benign half: a set reason survives verbatim, and an absent key stays nil
// (the pointer is what tells "absent" from "set").
func TestCooldownExempt_AnEmptyReasonFailsAndAReasonLoads(t *testing.T) {
	_, err := loadRegistry(cooldownRecord("sys-apps/foo", `cooldown_exempt = ""`))
	requireErrorNaming(t, err, "sys-apps/foo", "cooldown_exempt")

	cfg, err := loadRegistry(
		cooldownRecord("sys-apps/foo", `cooldown_exempt = "rebuilt hourly by upstream CI; a mask would always be stale"`) +
			cooldownRecord("sys-apps/bar", ""))
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	got := cfg.Packages["sys-apps/foo"].CooldownExempt
	if got == nil || *got != "rebuilt hourly by upstream CI; a mask would always be stale" {
		t.Errorf("sys-apps/foo CooldownExempt = %v, want the reason", got)
	}
	if bar := cfg.Packages["sys-apps/bar"].CooldownExempt; bar != nil {
		t.Errorf("sys-apps/bar CooldownExempt = %q, want nil (absent)", *bar)
	}
}

// TestCooldownWith_ARejectedRelationNamesBothRecords: R4.4, every way the
// relation can be wrong — each row's error must name both records involved —
// and then the one way it is right.
func TestCooldownWith_ARejectedRelationNamesBothRecords(t *testing.T) {
	cases := []struct {
		name  string
		text  string
		names []string
	}{
		{
			// Hostile: sys-apps/ai-jail exists, sys-apps/ai-jail-bin does not.
			name: "prefix-named target without a record",
			text: cooldownRecord("sys-apps/ai-jail", `cooldown_with = ["sys-apps/ai-jail-bin"]`) +
				cooldownRecord("sys-apps/ai", ""),
			names: []string{"sys-apps/ai-jail", "sys-apps/ai-jail-bin"},
		},
		{
			name: "not reciprocated",
			text: cooldownRecord("sys-apps/ai-jail", `cooldown_with = ["sys-apps/ai-jail-bin"]`) +
				cooldownRecord("sys-apps/ai-jail-bin", ""),
			names: []string{"sys-apps/ai-jail", "sys-apps/ai-jail-bin"},
		},
		{
			// Hostile third element: c reciprocates a but not b; every pair
			// check that starts from a passes.
			name: "group of three, one link missing",
			text: cooldownRecord("dev-lang/a", `cooldown_with = ["dev-lang/b", "dev-lang/c"]`) +
				cooldownRecord("dev-lang/b", `cooldown_with = ["dev-lang/a", "dev-lang/c"]`) +
				cooldownRecord("dev-lang/c", `cooldown_with = ["dev-lang/a"]`),
			names: []string{"dev-lang/b", "dev-lang/c"},
		},
		{
			name:  "self-reference",
			text:  cooldownRecord("net-print/hplip", `cooldown_with = ["net-print/hplip"]`),
			names: []string{"net-print/hplip"},
		},
		{
			name:  "target without a record",
			text:  cooldownRecord("dev-lang/flutter", `cooldown_with = ["dev-lang/dart"]`),
			names: []string{"dev-lang/flutter", "dev-lang/dart"},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := loadRegistry(c.text)
			requireErrorNaming(t, err, c.names...)
		})
	}

	// The benign half, decoded field for field: a complete, symmetric group
	// of three beside a prefix-named record that belongs to no group.
	t.Run("symmetric group of three loads", func(t *testing.T) {
		cfg, err := loadRegistry(
			cooldownRecord("dev-lang/a", `cooldown_with = ["dev-lang/b", "dev-lang/c"]`) +
				cooldownRecord("dev-lang/b", `cooldown_with = ["dev-lang/a", "dev-lang/c"]`) +
				cooldownRecord("dev-lang/c", `cooldown_with = ["dev-lang/a", "dev-lang/b"]`) +
				cooldownRecord("dev-lang/a-bin", ""))
		if err != nil {
			t.Fatalf("a symmetric group of three failed to load: %v", err)
		}
		if got := strings.Join(cfg.Packages["dev-lang/b"].CooldownWith, ","); got != "dev-lang/a,dev-lang/c" {
			t.Errorf("dev-lang/b CooldownWith = %q, want dev-lang/a,dev-lang/c", got)
		}
	})
}

// TestCooldownWith_AnUnsortedListIsRejected: design.md "Sorted."
func TestCooldownWith_AnUnsortedListIsRejected(t *testing.T) {
	_, err := loadRegistry(
		cooldownRecord("dev-lang/a", `cooldown_with = ["dev-lang/c", "dev-lang/b"]`) +
			cooldownRecord("dev-lang/b", `cooldown_with = ["dev-lang/a", "dev-lang/c"]`) +
			cooldownRecord("dev-lang/c", `cooldown_with = ["dev-lang/a", "dev-lang/b"]`))
	requireErrorNaming(t, err, "dev-lang/a", "cooldown_with")
}

// TestCooldownWith_LintReportsTheBrokenRelation: R4.4's `--lint` half. The
// linter reports (not just errors on) a non-reciprocated relation, naming
// both records in the finding.
func TestCooldownWith_LintReportsTheBrokenRelation(t *testing.T) {
	overlay := t.TempDir()
	if err := os.MkdirAll(filepath.Join(overlay, ".autoupdate"), 0o755); err != nil {
		t.Fatal(err)
	}
	text := cooldownRecord("net-print/hplip", `cooldown_with = ["net-print/hplip-plugin"]`) +
		cooldownRecord("net-print/hplip-plugin", "")
	if err := os.WriteFile(filepath.Join(overlay, ".autoupdate", "packages.toml"), []byte(text), 0o644); err != nil {
		t.Fatal(err)
	}

	issues, err := LintPackagesConfig(nil, overlay)
	if err != nil {
		t.Fatalf("LintPackagesConfig: %v", err)
	}
	found := false
	for _, is := range issues {
		if len(unnamed(is.Package+" "+is.Message, "net-print/hplip", "net-print/hplip-plugin")) == 0 {
			found = true
		}
	}
	if !found {
		t.Errorf("lint reported no finding naming both net-print/hplip and net-print/hplip-plugin: %v", issues)
	}
}

// TestCooldownFields_FollowPublishedPathInTheCanonicalOrder: design.md
// "appended to CanonicalFieldOrder (after published_path)".
func TestCooldownFields_FollowPublishedPathInTheCanonicalOrder(t *testing.T) {
	rank := map[string]int{}
	for i, f := range CanonicalFieldOrder {
		rank[f] = i
	}
	pp, ok := rank["published_path"]
	if !ok {
		t.Fatal("published_path is not in CanonicalFieldOrder")
	}
	for _, f := range []string{"cooldown_with", "cooldown_exempt"} {
		r, ok := rank[f]
		if !ok {
			t.Errorf("%s is not in CanonicalFieldOrder", f)
			continue
		}
		if r <= pp {
			t.Errorf("%s sits at %d, before published_path at %d", f, r, pp)
		}
	}
}

// TestCooldownFields_RenderAndReadBack: RenderRecord emits both fields and
// the emitted text decodes to the same values.
func TestCooldownFields_RenderAndReadBack(t *testing.T) {
	reason := "upstream re-tags the same version; masking would hide a fix"
	cfg := &PackageConfig{
		URL: "https://example.org/a/releases", Parser: "regex", Pattern: `v([0-9.]+)`,
		CooldownWith:   []string{"dev-lang/b", "dev-lang/c"},
		CooldownExempt: &reason,
	}
	text := RenderRecord("dev-lang/a", cfg)
	for _, key := range []string{"cooldown_with", "cooldown_exempt"} {
		if !strings.Contains(text, key) {
			t.Errorf("RenderRecord omitted %s:\n%s", key, text)
		}
	}
	back, err := decodePackagesConfig([]byte(text))
	if err != nil {
		t.Fatalf("the rendered record does not decode: %v\n%s", err, text)
	}
	got := back.Packages["dev-lang/a"]
	if strings.Join(got.CooldownWith, ",") != "dev-lang/b,dev-lang/c" {
		t.Errorf("CooldownWith read back as %v", got.CooldownWith)
	}
	if got.CooldownExempt == nil || *got.CooldownExempt != reason {
		t.Errorf("CooldownExempt read back as %v", got.CooldownExempt)
	}
}

// TestCooldownFields_LintFixReordersThemAndKeepsTheirValues: lintfix.go
// support. A record with both fields written first is repaired into the
// canonical order, its values intact.
func TestCooldownFields_LintFixReordersThemAndKeepsTheirValues(t *testing.T) {
	overlay := t.TempDir()
	if err := os.MkdirAll(filepath.Join(overlay, ".autoupdate"), 0o755); err != nil {
		t.Fatal(err)
	}
	text := `["dev-lang/a"]
cooldown_exempt = "a reason"
cooldown_with = ["dev-lang/b"]
url = "https://example.org/a/releases"
parser = "regex"
pattern = 'v([0-9.]+)'

["dev-lang/b"]
url = "https://example.org/b/releases"
parser = "regex"
pattern = 'v([0-9.]+)'
cooldown_with = ["dev-lang/a"]
`
	if err := os.WriteFile(filepath.Join(overlay, ".autoupdate", "packages.toml"), []byte(text), 0o644); err != nil {
		t.Fatal(err)
	}
	res, err := RepairPackagesConfig(overlay)
	if err != nil {
		t.Fatalf("RepairPackagesConfig: %v", err)
	}
	// The record header line, not the bare string: `["dev-lang/b"]` also
	// appears inside dev-lang/a's own cooldown_with value.
	end := strings.Index(res.Repaired, "\n[\"dev-lang/b\"]\n")
	if end < 0 {
		t.Fatalf("the repaired registry lost the dev-lang/b record:\n%s", res.Repaired)
	}
	rec := res.Repaired[:end]
	if strings.Index(rec, "cooldown_with") < strings.Index(rec, "pattern") ||
		strings.Index(rec, "cooldown_exempt") < strings.Index(rec, "pattern") {
		t.Errorf("the repaired record does not place the cooldown fields after the earlier canonical keys:\n%s", rec)
	}
	back, err := decodePackagesConfig([]byte(res.Repaired))
	if err != nil {
		t.Fatalf("the repaired registry does not decode: %v", err)
	}
	a := back.Packages["dev-lang/a"]
	if a.CooldownExempt == nil || *a.CooldownExempt != "a reason" || strings.Join(a.CooldownWith, ",") != "dev-lang/b" {
		t.Errorf("repair changed the values: %+v", a)
	}
}
