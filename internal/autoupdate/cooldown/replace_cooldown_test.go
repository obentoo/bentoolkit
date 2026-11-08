package cooldown_test

import (
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/obentoo/bentoolkit/internal/autoupdate/cooldown"
)

func lined(pkg, version, line string, since time.Time) cooldown.Entry {
	e := entry(pkg, version, since)
	e.Line = line
	return e
}

// A release line holds one candidate: a newer version of the same line
// supersedes the masked one instead of queueing behind it, and the new
// candidate inherits the line's First.
func TestCooldownReplace_ALineHoldsOneCandidate(t *testing.T) {
	b := &cooldown.Block{Days: 7}
	if added, sup := b.Replace(lined("app-editors/zed", "1.28.0_pre", "app-editors/zed@preview", queueT0)); !added || len(sup) != 0 {
		t.Fatalf("first candidate: added=%v superseded=%v, want true and none", added, sup)
	}
	added, sup := b.Replace(lined("app-editors/zed", "1.28.1_pre", "app-editors/zed@preview", queueT0.Add(day(2))))
	if !added || len(sup) != 1 || sup[0].Version != "1.28.0_pre" {
		t.Fatalf("replacement: added=%v superseded=%v, want true and [1.28.0_pre]", added, sup)
	}
	if len(b.Entries) != 1 {
		t.Fatalf("entries = %+v, want only the new candidate", b.Entries)
	}
	got := b.Entries[0]
	if got.Version != "1.28.1_pre" || !got.Since.Equal(queueT0.Add(day(2))) || !got.First.Equal(queueT0) {
		t.Errorf("candidate = %+v, want 1.28.1_pre since T0+2d first T0", got)
	}
	// Its own clock: masked two days later, released two days later.
	if due := b.EntryEligible(got); !due.Equal(queueT0.Add(day(9))) {
		t.Errorf("EntryEligible = %s, want %s (the replacement restarts the clock)", due, queueT0.Add(day(9)))
	}
}

// Lines are independent: a stable candidate is not superseded by a preview
// one of the same package, and an entry with no line (a manual add) never
// supersedes nor is superseded.
func TestCooldownReplace_OtherLinesAndManualEntriesStay(t *testing.T) {
	b := &cooldown.Block{Days: 7}
	b.Replace(lined("app-editors/zed", "1.27.1", "app-editors/zed@stable", queueT0))
	b.Add(entry("app-editors/zed", "1.27.0", queueT0))
	_, sup := b.Replace(lined("app-editors/zed", "1.28.0_pre", "app-editors/zed@preview", queueT0.Add(day(1))))
	if len(sup) != 0 || len(b.Entries) != 3 {
		t.Errorf("superseded=%v entries=%+v, want nothing superseded and 3 entries", sup, b.Entries)
	}
	if added, sup := b.Replace(lined("app-editors/zed", "1.28.0_pre", "app-editors/zed@preview", queueT0.Add(day(3)))); added || len(sup) != 0 {
		t.Errorf("re-adding the masked version: added=%v superseded=%v, want a no-op", added, sup)
	}
	if e, _ := findEntry(b, "app-editors/zed", "1.28.0_pre"); !e.Since.Equal(queueT0.Add(day(1))) {
		t.Errorf("re-add moved since to %s, want it kept at T0+1d", e.Since)
	}
}

// A line whose candidate is replaced faster than the cooldown is released
// anyway at First + MaxWaitFactor×days: upstream releasing every day must not
// keep a line masked for ever.
func TestCooldownReplace_TheLineWaitIsBounded(t *testing.T) {
	b := &cooldown.Block{Days: 7}
	line := "media-video/foo"
	for d := 0; d <= 20; d++ {
		b.Replace(lined("media-video/foo", "1."+strconv.Itoa(d), line, queueT0.Add(day(d))))
	}
	if len(b.Entries) != 1 {
		t.Fatalf("entries = %d, want 1 after 21 replacements", len(b.Entries))
	}
	cand := b.Entries[0]
	if want := queueT0.Add(day(21)); !b.EntryEligible(cand).Equal(want) {
		t.Errorf("EntryEligible = %s, want %s (First + 3×7 days)", b.EntryEligible(cand), want)
	}
	lifts, rest := cooldown.Sweep(b, queueT0.Add(day(21)), nil, nil, everyEbuildExists)
	if len(lifts) != 1 || lifts[0].Reason != "elapsed" || len(rest.Entries) != 0 {
		t.Errorf("lifts=%v rest=%+v, want the current candidate released as elapsed", lifts, rest.Entries)
	}
}

// days= on an entry (a record's cooldown_days) overrides the block's.
func TestCooldownEntryDays_OverridesTheBlock(t *testing.T) {
	b := &cooldown.Block{Days: 7}
	e := entry("dev-libs/foo", "1.0", queueT0)
	e.Days = 2
	b.Add(e)
	if due := b.EntryEligible(b.Entries[0]); !due.Equal(queueT0.Add(day(2))) {
		t.Errorf("EntryEligible = %s, want T0+2d", due)
	}
}

// line=, first= and days= survive a Save and a Load, and an entry without them
// renders exactly as before (no empty keys).
func TestCooldownSave_LineFirstAndDaysRoundTrip(t *testing.T) {
	root := writeOverlayMask(t, saveEmptyBlock)
	b := loadOrFatal(t, root)
	e := lined("app-editors/zed", "1.28.1_pre", "app-editors/zed@preview", queueT0.Add(day(2)))
	e.First = queueT0
	e.Days = 3
	b.Add(e)
	b.Add(entry("dev-libs/plain", "1.0", queueT0))
	if err := b.Save(root); err != nil {
		t.Fatalf("Save: %v", err)
	}
	text := string(readMask(t, root))
	if !strings.Contains(text, "line=app-editors/zed@preview first=2026-11-07T07:10:00Z days=3\n=app-editors/zed-1.28.1_pre\n") {
		t.Errorf("rendered block does not carry line/first/days:\n%s", text)
	}
	if !strings.Contains(text, "origin=autoupdate\n=dev-libs/plain-1.0\n") {
		t.Errorf("a plain entry gained keys:\n%s", text)
	}
	back := loadOrFatal(t, root)
	got, ok := findEntry(back, "app-editors/zed", "1.28.1_pre")
	if !ok || got.Line != e.Line || !got.First.Equal(queueT0) || got.Days != 3 {
		t.Errorf("read back %+v, want line/first/days preserved", got)
	}
}

// first= after since, and days= out of range, are malformed.
func TestCooldownLoad_RejectsBadLineFields(t *testing.T) {
	for name, comment := range map[string]string{
		"first after since": "# cooldown since=2026-11-07T00:00:00Z origin=autoupdate first=2026-11-08T00:00:00Z",
		"days zero":         "# cooldown since=2026-11-07T00:00:00Z origin=autoupdate days=0",
		"days not a number": "# cooldown since=2026-11-07T00:00:00Z origin=autoupdate days=x",
		"first not a time":  "# cooldown since=2026-11-07T00:00:00Z origin=autoupdate first=yesterday",
	} {
		t.Run(name, func(t *testing.T) {
			root := writeOverlayMask(t, "# BEGIN bentoo-cooldown format=1 days=7\n"+cooldownHeaderLineNL+comment+"\n=dev-libs/foo-1.0\n# END bentoo-cooldown\n")
			if _, err := cooldown.Load(root); err == nil {
				t.Errorf("Load accepted %q", comment)
			}
		})
	}
}
