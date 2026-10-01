package notices_test

import (
	"sort"
	"testing"

	"github.com/obentoo/bentoolkit/internal/gentoo/pkgdb"
	"github.com/obentoo/bentoolkit/internal/notices"
)

func pkg(cp, version, slot, repo string) pkgdb.Package {
	for i := 0; i < len(cp); i++ {
		if cp[i] == '/' {
			return pkgdb.Package{Category: cp[:i], Name: cp[i+1:], Version: version, Slot: slot, Repo: repo}
		}
	}
	panic("pkg: cp without a slash: " + cp)
}

func feedNotice(id string, affects ...notices.Affects) notices.Notice {
	return notices.Notice{ID: id, Title: id, Type: "security", Severity: "critical", Source: notices.SourceFeed, Affects: affects}
}

func rng(op, ver string) notices.Range { return notices.Range{Op: op, Ver: ver} }

// fooCVE affects dev-libs/foo slot 1 in [1.0, 1.2.3).
var fooCVE = feedNotice("2026-10-02-foo-cve", notices.Affects{
	CP: "dev-libs/foo", Slot: "1",
	Ranges: []notices.Range{rng(">=", "1.0"), rng("<", "1.2.3")},
})

// TestApplies_HostileNearMatchesDoNotApply is the hostile half of R5.1/R5.2,
// authored first: inputs that look like the affected package but are not.
// Each would make a sloppy matcher notify a user who is not affected.
func TestApplies_HostileNearMatchesDoNotApply(t *testing.T) {
	cases := map[string]pkgdb.Package{
		"same cp and version from gentoo":            pkg("dev-libs/foo", "1.1", "1", "gentoo"),
		"same cp and version, no repository record":  pkg("dev-libs/foo", "1.1", "1", ""),
		"repository named like bentoo":               pkg("dev-libs/foo", "1.1", "1", "bentoo-extra"),
		"repository bentoo in another case":          pkg("dev-libs/foo", "1.1", "1", "Bentoo"),
		"package whose name extends the cp":          pkg("dev-libs/foobar", "1.1", "1", "bentoo"),
		"package whose name is a prefix of the cp":   pkg("dev-libs/fo", "1.1", "1", "bentoo"),
		"same name in another category":              pkg("app-misc/foo", "1.1", "1", "bentoo"),
		"other slot":                                 pkg("dev-libs/foo", "1.1", "2", "bentoo"),
		"slot that extends the entry's slot":         pkg("dev-libs/foo", "1.1", "10", "bentoo"),
		"version equal to the exclusive upper bound": pkg("dev-libs/foo", "1.2.3", "1", "bentoo"),
		"version below the lower bound":              pkg("dev-libs/foo", "0.9", "1", "bentoo"),
		"numerically above, lexically below":         pkg("dev-libs/foo", "1.10", "1", "bentoo"),
	}
	for name, p := range cases {
		t.Run(name, func(t *testing.T) {
			if notices.Applies(fooCVE, []pkgdb.Package{p}) {
				t.Errorf("Applies = true for %+v; the notice does not affect it", p)
			}
		})
	}
}

// TestApplies_HostileSplitsStillApply is the converse: inputs spelled
// differently from the entry that ARE the affected package. A matcher that
// compares text instead of PMS versions, or the whole SLOT instead of the slot,
// misses them and stays silent for an affected user.
func TestApplies_HostileSplitsStillApply(t *testing.T) {
	cases := map[string]struct {
		n notices.Notice
		p pkgdb.Package
	}{
		"revision inside the range": {fooCVE, pkg("dev-libs/foo", "1.2.2-r3", "1", "bentoo")},
		"1.0-r0 equals 1.0 under =": {
			feedNotice("2026-10-02-eq", notices.Affects{CP: "dev-libs/foo", Ranges: []notices.Range{rng("=", "1.0")}}),
			pkg("dev-libs/foo", "1.0-r0", "0", "bentoo"),
		},
		"1.010 equals 1.01 under =": {
			feedNotice("2026-10-02-eq2", notices.Affects{CP: "dev-libs/foo", Ranges: []notices.Range{rng("=", "1.01")}}),
			pkg("dev-libs/foo", "1.010", "0", "bentoo"),
		},
		"numerically below, lexically above": {
			feedNotice("2026-10-02-num", notices.Affects{CP: "dev-libs/foo", Ranges: []notices.Range{rng("<", "1.10")}}),
			pkg("dev-libs/foo", "1.9", "0", "bentoo"),
		},
		"entry without slot matches any slot": {
			feedNotice("2026-10-02-anyslot", notices.Affects{CP: "dev-libs/foo", Ranges: []notices.Range{rng("<", "2")}}),
			pkg("dev-libs/foo", "1.5", "7", "bentoo"),
		},
		"entry without ranges matches every version": {
			feedNotice("2026-10-02-allver", notices.Affects{CP: "dev-libs/foo"}),
			pkg("dev-libs/foo", "9999", "0", "bentoo"),
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			if !notices.Applies(tc.n, []pkgdb.Package{tc.p}) {
				t.Errorf("Applies = false for %+v against %+v; the package is affected", tc.p, tc.n.Affects)
			}
		})
	}
}

// TestApplies_AffectedBentooPackage is the benign case of R5.1/R5.2.
func TestApplies_AffectedBentooPackage(t *testing.T) {
	installed := []pkgdb.Package{
		pkg("sys-apps/portage", "3.0.70", "0", "gentoo"),
		pkg("dev-libs/foo", "1.1", "1", "bentoo"),
	}
	if !notices.Applies(fooCVE, installed) {
		t.Error("Applies = false for bentoo dev-libs/foo-1.1 slot 1, inside [1.0, 1.2.3)")
	}
}

// TestApplies_EachOperatorAtItsBoundary pins the five operators on both sides.
func TestApplies_EachOperatorAtItsBoundary(t *testing.T) {
	cases := []struct {
		op, bound, version string
		want               bool
	}{
		{"<", "2.0", "1.9", true}, {"<", "2.0", "2.0", false},
		{"<=", "2.0", "2.0", true}, {"<=", "2.0", "2.0-r1", false},
		{"=", "2.0", "2.0", true}, {"=", "2.0", "2.0.1", false},
		{">=", "2.0", "2.0", true}, {">=", "2.0", "2.0_rc1", false},
		{">", "2.0", "2.0-r1", true}, {">", "2.0", "2.0", false},
	}
	for _, c := range cases {
		n := feedNotice("2026-10-02-op", notices.Affects{CP: "dev-libs/foo", Ranges: []notices.Range{rng(c.op, c.bound)}})
		got := notices.Applies(n, []pkgdb.Package{pkg("dev-libs/foo", c.version, "0", "bentoo")})
		if got != c.want {
			t.Errorf("%s %s %s: Applies = %v, want %v", c.version, c.op, c.bound, got, c.want)
		}
	}
}

// TestApplies_RangesAreANDedWithinOneEntry: two ranges where only one holds do
// not match (R5.2 "every one of the entry's ranges").
func TestApplies_RangesAreANDedWithinOneEntry(t *testing.T) {
	n := feedNotice("2026-10-02-and", notices.Affects{
		CP: "dev-libs/foo", Ranges: []notices.Range{rng(">=", "2.0"), rng("<", "1.0")},
	})
	for _, v := range []string{"0.5", "1.5", "2.5"} {
		if notices.Applies(n, []pkgdb.Package{pkg("dev-libs/foo", v, "0", "bentoo")}) {
			t.Errorf("version %s matched two ranges that no version can satisfy together", v)
		}
	}
}

// TestApplies_AnyEntryMatching is R5.1 "at least one entry matches": entries
// are ORed, and each entry is judged against one package at a time.
func TestApplies_AnyEntryMatching(t *testing.T) {
	n := feedNotice("2026-10-02-any",
		notices.Affects{CP: "dev-libs/foo", Ranges: []notices.Range{rng("<", "1.0")}},
		notices.Affects{CP: "dev-libs/bar", Ranges: []notices.Range{rng("<", "3.0")}},
	)
	if !notices.Applies(n, []pkgdb.Package{pkg("dev-libs/foo", "5.0", "0", "bentoo"), pkg("dev-libs/bar", "2.0", "0", "bentoo")}) {
		t.Error("the second entry matches dev-libs/bar-2.0 from bentoo; the notice applies")
	}
}

// TestApplies_OnePackageAtATime is the third-element case: an affected version
// from gentoo and an unaffected version from bentoo, both installed. Mixing the
// repository of one with the version of the other would wrongly apply.
func TestApplies_OnePackageAtATime(t *testing.T) {
	installed := []pkgdb.Package{
		pkg("dev-libs/foo", "1.1", "1", "gentoo"), // in range, wrong repository
		pkg("dev-libs/foo", "2.0", "1", "bentoo"), // right repository, out of range
	}
	if notices.Applies(fooCVE, installed) {
		t.Error("Applies = true: the matcher combined the repository of one installed package with the version of another")
	}
}

// TestApplies_NoAffectsAppliesEverywhere is R5.3 (and R5.5's degraded mode,
// where the installed list is empty).
func TestApplies_NoAffectsAppliesEverywhere(t *testing.T) {
	n := notices.Notice{ID: "2026-10-02-hello", Type: "announcement", Severity: "info", Source: notices.SourceFeed}
	for _, installed := range [][]pkgdb.Package{nil, {}, {pkg("dev-libs/foo", "1.0", "0", "gentoo")}} {
		if !notices.Applies(n, installed) {
			t.Errorf("a notice with no affects did not apply with installed = %+v", installed)
		}
	}
}

// TestApplies_AffectsWithNothingInstalled is the converse of R5.3: a notice
// WITH affects never applies when nothing is installed (R5.5's degraded mode
// must not notify every affects-bearing notice).
func TestApplies_AffectsWithNothingInstalled(t *testing.T) {
	if notices.Applies(fooCVE, nil) {
		t.Error("a notice with affects applied to an empty installed list")
	}
}

// TestApplies_NewsItemsAlwaysApply is R5.4: portage already filtered the
// unread list.
func TestApplies_NewsItemsAlwaysApply(t *testing.T) {
	n := notices.Notice{
		ID: "2026-10-02-foo-news", Type: "news", Severity: "info", Source: notices.SourceNews,
		Affects: []notices.Affects{{CP: "dev-libs/not-installed"}},
	}
	if !notices.Applies(n, nil) {
		t.Error("a news item from the unread list did not apply")
	}
}

func ids(ns []notices.Notice) []string {
	out := make([]string, 0, len(ns))
	for _, n := range ns {
		out = append(out, n.ID)
	}
	sort.Strings(out)
	return out
}

func newsNotice(id, title string) notices.Notice {
	return notices.Notice{ID: id, Title: title, Type: "news", Severity: "info", Source: notices.SourceNews}
}

// TestMerge_NearIdenticalIDsStayApart is the hostile half of R4.3 authored
// first: IDs that share a prefix or differ in case are different notices.
func TestMerge_NearIdenticalIDsStayApart(t *testing.T) {
	feed := []notices.Notice{feedNotice("2026-10-02-foo")}
	news := []notices.Notice{
		newsNotice("2026-10-02-foo-cve", "extends the feed ID"),
		newsNotice("2026-10-02-fo", "prefix of the feed ID"),
		newsNotice("2026-10-02-Foo", "differs in case"),
	}
	got := ids(notices.Merge(feed, news))
	want := []string{"2026-10-02-Foo", "2026-10-02-fo", "2026-10-02-foo", "2026-10-02-foo-cve"}
	if len(got) != len(want) {
		t.Fatalf("Merge = %q, want %q (near-identical IDs collapsed)", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("Merge = %q, want %q", got, want)
		}
	}
}

// TestMerge_SharedIDBecomesOneNoticeWithFeedData is R4.3: one notice, the feed
// item's data.
func TestMerge_SharedIDBecomesOneNoticeWithFeedData(t *testing.T) {
	f := feedNotice("2026-10-02-foo-cve")
	f.Title = "feed title"
	f.Summary = "feed summary"
	got := notices.Merge([]notices.Notice{f}, []notices.Notice{newsNotice("2026-10-02-foo-cve", "news title")})
	if len(got) != 1 {
		t.Fatalf("Merge = %+v, want one notice", got)
	}
	if got[0].Title != "feed title" || got[0].Source != notices.SourceFeed || got[0].Type != "security" {
		t.Errorf("Merge kept %+v, want the feed item's data", got[0])
	}
}

// TestMerge_ThirdCopyDoesNotReappear: the unread list may repeat an ID; the
// feed item still wins and exactly one notice results.
func TestMerge_ThirdCopyDoesNotReappear(t *testing.T) {
	f := feedNotice("2026-10-02-foo-cve")
	f.Title = "feed title"
	news := []notices.Notice{
		newsNotice("2026-10-02-foo-cve", "news title"),
		newsNotice("2026-10-02-foo-cve", "news title again"),
		newsNotice("2026-10-02-other", "other"),
	}
	got := notices.Merge([]notices.Notice{f}, news)
	if len(got) != 2 {
		t.Fatalf("Merge = %+v, want two notices", got)
	}
	for _, n := range got {
		if n.ID == "2026-10-02-foo-cve" && n.Title != "feed title" {
			t.Errorf("the shared ID carries %q, want the feed title", n.Title)
		}
	}
}

// TestMerge_DisjointSourcesAreUnited: nothing is lost when IDs differ, and
// either source may be empty.
func TestMerge_DisjointSourcesAreUnited(t *testing.T) {
	feed := []notices.Notice{feedNotice("2026-10-02-a"), feedNotice("2026-10-02-b")}
	news := []notices.Notice{newsNotice("2026-10-02-c", "c")}
	if got := ids(notices.Merge(feed, news)); len(got) != 3 {
		t.Errorf("Merge = %q, want three notices", got)
	}
	if got := ids(notices.Merge(nil, news)); len(got) != 1 || got[0] != "2026-10-02-c" {
		t.Errorf("Merge(nil, news) = %q", got)
	}
	if got := ids(notices.Merge(feed, nil)); len(got) != 2 {
		t.Errorf("Merge(feed, nil) = %q", got)
	}
}
