package autoupdate

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/obentoo/bentoolkit/internal/autoupdate/registry"
)

// blockWith renders a cooldown block (days=7) around raw entry lines.
func blockWith(entries ...string) string {
	return "# BEGIN bentoo-cooldown format=1 days=7\n" +
		"# Managed by `bentoo overlay autoupdate`. Edit with `bentoo overlay cooldown`, never by hand.\n" +
		strings.Join(entries, "") +
		"# END bentoo-cooldown\n"
}

// A new upstream release while the line's candidate is still masked
// REPLACES it: the new version is masked with the line's First, and the old
// candidate's ebuild and entry are gone once the promotion completed. The
// unmasked version below it stays installable.
func TestCooldownApply_ANewCandidateReplacesTheMaskedOne(t *testing.T) {
	const pkg = "app-misc/cooldown-line"
	first := cooldownNow.Add(-48 * time.Hour)
	mask := blockWith("# cooldown since=" + first.Format(time.RFC3339) + " origin=autoupdate line=" + pkg + "\n=" + pkg + "-1.1\n")
	f := newCooldownFixture(t, []cooldownBump{{pkg, "1.1", "1.2"}}, map[string][]string{pkg: {"1.0"}}, nil, mask)

	r := applyOne(t, f, pkg)
	if !r.Success {
		t.Fatalf("the apply failed: %v", r.Error)
	}
	if !f.ebuildExists(pkg, "1.2") || f.ebuildExists(pkg, "1.1") || !f.ebuildExists(pkg, "1.0") {
		t.Errorf("tree after the replacement: 1.2=%v 1.1=%v 1.0=%v, want 1.2 and 1.0 only",
			f.ebuildExists(pkg, "1.2"), f.ebuildExists(pkg, "1.1"), f.ebuildExists(pkg, "1.0"))
	}
	b := f.block(t)
	if _, ok := entryFor(b, pkg, "1.1"); ok {
		t.Errorf("the superseded candidate is still in the block:\n%s", f.mask(t))
	}
	e, ok := entryFor(b, pkg, "1.2")
	if !ok {
		t.Fatalf("the new candidate is not masked:\n%s", f.mask(t))
	}
	if e.Line != pkg || !e.Since.Equal(cooldownNow) || !e.First.Equal(first) {
		t.Errorf("entry = %+v, want line %s since now first %s", e, pkg, first)
	}
	if strings.Join(r.CooldownRetired, ",") != "1.1" {
		t.Errorf("CooldownRetired = %v, want [1.1]", r.CooldownRetired)
	}
	if got := f.unmaskList(t); strings.Join(got, " ") != "="+pkg+"-1.2" {
		t.Errorf("cooldown.unmask = %v, want only the new candidate", got)
	}
}

// Another record's line is not touched: the stable candidate stays when the
// preview line promotes.
func TestCooldownApply_AnotherLineKeepsItsCandidate(t *testing.T) {
	const pkg = "app-misc/cooldown-two-lines"
	since := cooldownNow.Add(-24 * time.Hour).Format(time.RFC3339)
	mask := blockWith("# cooldown since=" + since + " origin=autoupdate line=" + pkg + "@stable\n=" + pkg + "-1.0\n")
	f := newCooldownFixture(t, []cooldownBump{{pkg, "1.0", "1.1"}}, nil, nil, mask)

	if r := applyOne(t, f, pkg); !r.Success {
		t.Fatalf("the apply failed: %v", r.Error)
	}
	if !f.ebuildExists(pkg, "1.0") {
		t.Error("the other line's candidate was removed")
	}
	if _, ok := entryFor(f.block(t), pkg, "1.0"); !ok {
		t.Errorf("the other line's candidate lost its mask:\n%s", f.mask(t))
	}
}

// --urgent, the same version in ::gentoo, and an unkeyworded ebuild each
// promote unmasked, say why on the result, and write no entry.
func TestCooldownApply_AutomaticExemptions(t *testing.T) {
	const pkg = "app-misc/cooldown-exempt"
	cases := map[string]struct {
		opts      func(t *testing.T) []ApplierOption
		keywords  bool
		wantWhy   string
		wantEntry bool
	}{
		"urgent": {
			opts:     func(*testing.T) []ApplierOption { return []ApplierOption{WithApplierUrgent([]string{pkg})} },
			keywords: true, wantWhy: "urgent",
		},
		"same version in gentoo": {
			opts: func(t *testing.T) []ApplierOption {
				g := t.TempDir()
				dir := filepath.Join(g, "app-misc", "cooldown-exempt")
				if err := os.MkdirAll(dir, 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(dir, "cooldown-exempt-1.1.ebuild"), []byte("EAPI=8\n"), 0o644); err != nil {
					t.Fatal(err)
				}
				return []ApplierOption{WithApplierGentooPath(g)}
			},
			keywords: true, wantWhy: "same version in ::gentoo",
		},
		"unkeyworded": {
			opts:     func(*testing.T) []ApplierOption { return nil },
			keywords: false, wantWhy: "unkeyworded",
		},
		"control: keyworded, not urgent, not in gentoo": {
			opts:     func(t *testing.T) []ApplierOption { return []ApplierOption{WithApplierGentooPath(t.TempDir())} },
			keywords: true, wantEntry: true,
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			f := newCooldownFixture(t, []cooldownBump{{pkg, "1.0", "1.1"}}, nil, nil, cooldownEmptyBlock, tc.opts(t)...)
			if !tc.keywords {
				createTestEbuildFileWithContent(t, f.overlay, pkg, "1.0",
					"EAPI=8\nDESCRIPTION=\"x\"\nHOMEPAGE=\"https://example.com\"\nSRC_URI=\"\"\nLICENSE=\"MIT\"\nSLOT=\"0\"\n#KEYWORDS=\"~amd64\"\n")
			}
			r := applyOne(t, f, pkg)
			if !r.Success {
				t.Fatalf("the apply failed: %v", r.Error)
			}
			_, masked := entryFor(f.block(t), pkg, "1.1")
			if masked != tc.wantEntry {
				t.Errorf("masked = %v, want %v:\n%s", masked, tc.wantEntry, f.mask(t))
			}
			if r.CooldownSkipped != tc.wantWhy {
				t.Errorf("CooldownSkipped = %q, want %q", r.CooldownSkipped, tc.wantWhy)
			}
		})
	}
}

// cooldown_days reaches the entry as days=N.
func TestCooldownApply_CooldownDaysIsWrittenOnTheEntry(t *testing.T) {
	const pkg = "app-misc/cooldown-days"
	cfgs := map[string]registry.PackageConfig{pkg: {CooldownDays: 3}}
	f := newCooldownFixture(t, []cooldownBump{{pkg, "1.0", "1.1"}}, nil, cfgs, cooldownEmptyBlock)
	if r := applyOne(t, f, pkg); !r.Success {
		t.Fatalf("the apply failed: %v", r.Error)
	}
	e, ok := entryFor(f.block(t), pkg, "1.1")
	if !ok || e.Days != 3 {
		t.Fatalf("entry = %+v (found %v), want days 3", e, ok)
	}
	if !strings.Contains(f.mask(t), " days=3\n") {
		t.Errorf("days=3 not rendered:\n%s", f.mask(t))
	}
}

func TestEbuildHasKeywords(t *testing.T) {
	dir := t.TempDir()
	for content, want := range map[string]bool{
		"KEYWORDS=\"~amd64\"\n": true,
		"if [[ ${PV} == 9999 ]]; then\n\t:\nelse\n\tKEYWORDS=\"~amd64 ~arm64\"\nfi\n": true,
		"\t#KEYWORDS=\"~amd64\"\n": false,
		"KEYWORDS=\"\"\n":          false,
		"KEYWORDS=\" \"\n":         false,
		"EAPI=8\n":                 false,
	} {
		p := filepath.Join(dir, "x.ebuild")
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
		if got := ebuildHasKeywords(p); got != want {
			t.Errorf("ebuildHasKeywords(%q) = %v, want %v", content, got, want)
		}
	}
	if !ebuildHasKeywords(filepath.Join(dir, "missing.ebuild")) {
		t.Error("an unreadable ebuild must count as keyworded, so a read error never skips the cooldown")
	}
}
