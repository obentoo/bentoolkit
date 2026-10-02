package notices_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/obentoo/bentoolkit/internal/notices"
)

// The golden fixture is site story 002's tests/fixtures/notices.golden.json,
// copied into testdata/. Its content is fixed by the site's task 3.3 and the
// cross-artifact review: four notices built with now = 2026-10-01T00:00:00Z
// and origin https://obentoo.org, each at midnight UTC with updated ==
// published:
//
//   - a security/critical notice with a cp, a slot and two ranges;
//   - a release notice with one "<" range for app-portage/bentoolkit;
//   - the shared edge notice 2026-09-28-edge+case_1 (news/info,
//     dev-libs/libfoo+, slot 0.1, >=1.0_rc1_p2, <1.0b-r0) that the site
//     schema, `bentoo notice new` and ParseFeed must all accept;
//   - an announcement with no affects and a '+' in its short name.
//
// The tests pin that structure and the shared edge notice exactly, not the
// site's other titles, so a refreshed copy that honours the contract passes.

const edgeID = "2026-09-28-edge+case_1"

func loadGolden(t *testing.T) notices.Feed {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", "notices.golden.json"))
	if err != nil {
		t.Fatal(err)
	}
	f, err := notices.ParseFeed(data)
	if err != nil {
		t.Fatalf("ParseFeed(golden): %v", err)
	}
	return f
}

// TestContract_FeedLevelFields: serial is the build time in whole Unix
// seconds and expires is the build time plus 30 days (site R3.4).
func TestContract_FeedLevelFields(t *testing.T) {
	f := loadGolden(t)
	built := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	if f.Serial != built.Unix() {
		t.Errorf("Serial = %d, want %d (the fixture's build time)", f.Serial, built.Unix())
	}
	if !f.Expires.Equal(built.Add(30 * 24 * time.Hour)) {
		t.Errorf("Expires = %v, want %v", f.Expires, built.Add(30*24*time.Hour))
	}
}

func isMidnightUTC(ts time.Time) bool {
	u := ts.UTC()
	return !ts.IsZero() && u.Hour() == 0 && u.Minute() == 0 && u.Second() == 0 && u.Nanosecond() == 0
}

// TestContract_FourNoticesWithTheirAffects pins the four fixture notices'
// shapes: every field the tray reads.
func TestContract_FourNoticesWithTheirAffects(t *testing.T) {
	f := loadGolden(t)
	if len(f.Items) != 4 {
		t.Fatalf("Items = %d, want 4", len(f.Items))
	}
	var security, release, edge, announcement *notices.Notice
	for i := range f.Items {
		n := &f.Items[i]
		switch {
		case n.ID == edgeID:
			edge = n
		case n.Type == "security":
			security = n
		case n.Type == "release":
			release = n
		case n.Type == "announcement":
			announcement = n
		}
		if n.URL != "https://obentoo.org/notices/"+n.ID+"/" {
			t.Errorf("%s URL = %q, want its notice page on https://obentoo.org", n.ID, n.URL)
		}
		if n.Title == "" || n.Summary == "" {
			t.Errorf("%s has an empty title or summary", n.ID)
		}
		if !isMidnightUTC(n.Published) || !n.Updated.Equal(n.Published) {
			t.Errorf("%s published/updated = %v/%v, want midnight UTC with updated == published", n.ID, n.Published, n.Updated)
		}
		if n.Source != notices.SourceFeed {
			t.Errorf("%s Source = %v, want SourceFeed", n.ID, n.Source)
		}
	}
	if security == nil || release == nil || edge == nil || announcement == nil {
		t.Fatalf("want one security, one release, the edge notice %s and one announcement; got %+v", edgeID, f.Items)
	}

	if security.Severity != "critical" || len(security.Affects) != 1 {
		t.Fatalf("security notice = %+v, want critical with one affects entry", security)
	}
	if a := security.Affects[0]; a.CP == "" || a.Slot == "" || len(a.Ranges) != 2 {
		t.Errorf("security affects = %+v, want a cp, a slot and two ranges", a)
	}

	if len(release.Affects) != 1 {
		t.Fatalf("release affects = %+v, want one entry", release.Affects)
	}
	if a := release.Affects[0]; a.CP != "app-portage/bentoolkit" || len(a.Ranges) != 1 || a.Ranges[0].Op != "<" {
		t.Errorf("release affects = %+v, want app-portage/bentoolkit with one '<' range", a)
	}

	if edge.Type != "news" || edge.Severity != "info" {
		t.Errorf("edge notice type/severity = %s/%s, want news/info", edge.Type, edge.Severity)
	}
	want := notices.Affects{CP: "dev-libs/libfoo+", Slot: "0.1",
		Ranges: []notices.Range{{Op: ">=", Ver: "1.0_rc1_p2"}, {Op: "<", Ver: "1.0b-r0"}}}
	if len(edge.Affects) != 1 || edge.Affects[0].CP != want.CP || edge.Affects[0].Slot != want.Slot ||
		len(edge.Affects[0].Ranges) != 2 || edge.Affects[0].Ranges[0] != want.Ranges[0] || edge.Affects[0].Ranges[1] != want.Ranges[1] {
		t.Errorf("edge affects = %+v, want %+v", edge.Affects, want)
	}

	if len(announcement.Affects) != 0 {
		t.Errorf("announcement affects = %+v, want none", announcement.Affects)
	}
	short := announcement.ID[len("2006-01-02-"):]
	if !strings.Contains(short, "+") {
		t.Errorf("announcement ID %q has no '+' in its short name", announcement.ID)
	}
}

// TestContract_ItemsAreNewestFirst: the site sorts by updated descending, then
// id ascending (site R3.5).
func TestContract_ItemsAreNewestFirst(t *testing.T) {
	f := loadGolden(t)
	if len(f.Items) < 2 {
		t.Fatalf("Items = %d; the order cannot be checked", len(f.Items))
	}
	for i := 1; i < len(f.Items); i++ {
		a, b := f.Items[i-1], f.Items[i]
		if a.Updated.Before(b.Updated) || (a.Updated.Equal(b.Updated) && a.ID > b.ID) {
			t.Errorf("items %s and %s are out of order", a.ID, b.ID)
		}
	}
}
