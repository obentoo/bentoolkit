package cooldown_test

// Reconcile brings entries already in the block in line with the registry as
// it stands now. Without it a record moved to cooldown_days = 0 stayed masked
// until its old clock ran out, and a changed cooldown_days only reached the
// next candidate: the entry froze the value it was masked with (2026-11-25).
//
// Hostile halves first: a manual entry of a package whose line goes off (no
// line, so the registry does not own it), a line the registry no longer
// lists (left alone, not reset), and a record that dropped its override
// (reset to the block's days, not kept).

import (
	"testing"
	"time"

	"github.com/obentoo/bentoolkit/internal/autoupdate/cooldown"
)

func reconcileBlock() *cooldown.Block {
	since := time.Date(2026, 11, 25, 17, 0, 0, 0, time.UTC)
	return &cooldown.Block{Days: 7, Entries: []cooldown.Entry{
		{Package: "app-portage/tool", Version: "1.2.0", Since: since, Line: "app-portage/tool", Origin: cooldown.OriginAutoupdate},
		{Package: "app-portage/tool", Version: "1.1.9", Since: since, Origin: cooldown.OriginManual},
		{Package: "www-client/browser", Version: "161.0", Since: since, Line: "www-client/browser", Origin: cooldown.OriginAutoupdate},
		{Package: "dev-util/gone", Version: "2.0", Since: since, Line: "dev-util/gone", Origin: cooldown.OriginAutoupdate, Days: 3},
		{Package: "dev-util/reset", Version: "4.0", Since: since, Line: "dev-util/reset", Origin: cooldown.OriginAutoupdate, Days: 3},
		{Package: "app-editors/zed", Version: "1.30.2_pre", Since: since, Line: "app-editors/zed@preview", Origin: cooldown.OriginAutoupdate},
	}}
}

func TestCooldownReconcile_AnOffLineLiftsOnlyTheEntriesItOwns(t *testing.T) {
	b := reconcileBlock()
	lifts, _ := b.Reconcile(map[string]cooldown.LinePolicy{
		"app-portage/tool":        {Off: true},
		"www-client/browser":      {},
		"dev-util/reset":          {},
		"app-editors/zed@preview": {},
	})
	if len(lifts) != 1 || lifts[0].Package != "app-portage/tool" || lifts[0].Version != "1.2.0" || lifts[0].Reason != cooldown.ReasonRegistry {
		t.Fatalf("lifts = %+v, want only =app-portage/tool-1.2.0 for %q", lifts, cooldown.ReasonRegistry)
	}
	for _, e := range b.Entries {
		if e.Package == "app-portage/tool" && e.Version == "1.2.0" {
			t.Errorf("the lifted entry is still in the block")
		}
	}
	if !reconcileHasEntry(b, "app-portage/tool", "1.1.9") {
		t.Errorf("the manual entry was lifted: the registry does not own an entry with no line")
	}
}

func TestCooldownReconcile_ARetimedLineTakesTheRegistrysDays(t *testing.T) {
	b := reconcileBlock()
	_, retimed := b.Reconcile(map[string]cooldown.LinePolicy{
		"app-portage/tool":        {},
		"www-client/browser":      {Days: 2},
		"dev-util/reset":          {},
		"app-editors/zed@preview": {Days: 2},
	})
	want := map[string]int{"www-client/browser": 2, "dev-util/reset": 0, "app-editors/zed": 2, "dev-util/gone": 3}
	for _, e := range b.Entries {
		if d, ok := want[e.Package]; ok && e.Days != d {
			t.Errorf("%s-%s days = %d, want %d", e.Package, e.Version, e.Days, d)
		}
	}
	if len(retimed) != 3 {
		t.Errorf("retimed = %+v, want browser, reset and zed (gone is not in the registry)", retimed)
	}
	for _, r := range retimed {
		if r.Package == "dev-util/reset" && (r.From != 3 || r.To != 0) {
			t.Errorf("reset retimed %d -> %d, want 3 -> 0 (the block's days)", r.From, r.To)
		}
	}
}

func TestCooldownReconcile_AnUnchangedRegistryChangesNothing(t *testing.T) {
	b := reconcileBlock()
	lifts, retimed := b.Reconcile(map[string]cooldown.LinePolicy{
		"app-portage/tool": {}, "www-client/browser": {}, "dev-util/gone": {Days: 3}, "dev-util/reset": {Days: 3},
		"app-editors/zed@preview": {},
	})
	if len(lifts) != 0 || len(retimed) != 0 || len(b.Entries) != len(reconcileBlock().Entries) {
		t.Errorf("lifts=%+v retimed=%+v entries=%d, want no change", lifts, retimed, len(b.Entries))
	}
}

func reconcileHasEntry(b *cooldown.Block, pkg, ver string) bool {
	for _, e := range b.Entries {
		if e.Package == pkg && e.Version == ver {
			return true
		}
	}
	return false
}
