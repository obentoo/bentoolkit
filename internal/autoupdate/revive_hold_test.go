package autoupdate

// A held record (hold = true) is a maintainer's decision to keep the package
// out of autoupdate. A revive must never undo it: before this guard the
// reviver seeded the ::gentoo ebuild and re-enabled the record, and only the
// apply's hold check stopped the bump. That left a byte-identical ::gentoo copy
// behind, with enabled = false stripped from the record, after every
// `--revive all` (bentoo's media-libs/tg_owt came back three times this way).

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/obentoo/bentoolkit/internal/autoupdate/registry"
)

// TestFindRevivableOrphans_HeldSkipped: a disabled, absent, held entry whose
// upstream is newer than ::gentoo is not a candidate, and its upstream is not
// even fetched, so `--revive all` never reaches it.
func TestFindRevivableOrphans_HeldSkipped(t *testing.T) {
	pkg := "media-libs/declined"
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"version":"2.0.0"}`))
	}))
	t.Cleanup(srv.Close)

	checker := newReviveChecker(t, map[string]registry.PackageConfig{
		pkg: {Parser: "json", Path: "version", URL: srv.URL, Enabled: boolPtr(false), Hold: true},
	})
	prov := &fakeProvider{versions: map[string][]string{pkg: {"1.5.0"}}}

	got, err := checker.FindRevivableOrphans(t.Context(), prov)
	if err != nil {
		t.Fatalf("FindRevivableOrphans: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("expected 0 candidates (held), got %d: %+v", len(got), got)
	}
	if n := hits.Load(); n != 0 {
		t.Errorf("upstream fetched %d time(s) for a held entry; want 0", n)
	}
}

// TestReviveRefusesAHeldEntry: an explicit `--revive <pkg>` on a held record
// is skipped before anything is written: no ::gentoo lookup, no seeded
// ebuild, and packages.toml byte-identical.
func TestReviveRefusesAHeldEntry(t *testing.T) {
	rig := newS060ReviveRig(t, []string{"1.0.0"}, "2.0.0")
	regPath := filepath.Join(rig.overlayDir, ".autoupdate", "packages.toml")
	reg, err := os.ReadFile(regPath)
	if err != nil {
		t.Fatal(err)
	}
	reg = append(reg, []byte("hold = true\n")...)
	if err := os.WriteFile(regPath, reg, 0o644); err != nil {
		t.Fatal(err)
	}

	got := rig.reviver(t).Revive(t.Context(), s060RevivePkg)

	assertOutcome(t, got, s060RevivePkg, ReviveSkipped, reviveHeldDetail)
	if _, err := os.Stat(rig.anyOverlayEbuild()); err == nil {
		t.Errorf("a held entry was seeded: %s exists", rig.anyOverlayEbuild())
	}
	after, err := os.ReadFile(regPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != string(reg) {
		t.Errorf("packages.toml changed for a held entry:\n--- before\n%s--- after\n%s", reg, after)
	}
	for _, e := range rig.events.all() {
		if strings.HasPrefix(e, "lookup ") || strings.HasPrefix(e, "spawn ") || strings.HasPrefix(e, "newChecker") {
			t.Errorf("a held revive still ran a pipeline step: %v", rig.events.all())
			break
		}
	}
}
