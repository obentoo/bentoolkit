package autoupdate

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// The two GStreamer release lines, told apart the way the project's own entries
// do it: an even minor is a stable release, an odd one is a development
// release. Both are maintained in parallel, in one directory.
const (
	gstStableSeries = `^1\.[0-9]*[02468]\.`
	gstDevSeries    = `^1\.[0-9]*[13579]\.`
)

// sweepEbuild is one file to lay down in a fixture package directory. slot is
// the SLOT= value the file declares; empty means the ordinary SLOT="0" body
// every single-slot package has. The value matters because slot filtering reads
// file CONTENTS — a fixture that only names files cannot exercise it.
type sweepEbuild struct {
	version string
	slot    string
}

// writeSweepOverlay lays one package directory out in a fresh t.TempDir() and
// returns the overlay root.
//
// Every fixture is temporary on purpose: what is under test decides which
// ebuild FILES a later task deletes, so pointing it at a real overlay would
// compute a deletion plan against files someone else maintains.
func writeSweepOverlay(t *testing.T, atom string, ebuilds []sweepEbuild) string {
	t.Helper()
	overlay := filepath.Join(t.TempDir(), "overlay")
	for _, e := range ebuilds {
		if e.slot == "" {
			createTestEbuildFile(t, overlay, atom, e.version)
			continue
		}
		createTestEbuildFileWithContent(t, overlay, atom, e.version,
			"# Test ebuild\nEAPI=8\nDESCRIPTION=\"Test package\"\nSLOT=\""+e.slot+"\"\nKEYWORDS=\"~amd64\"\n")
	}
	return overlay
}

// regEntry is a registry entry reduced to what a sweep reads: its pin, and the
// series that tells its ebuilds from its siblings'. An empty version is an
// entry with no pin.
func regEntry(version, series string) PackageConfig {
	return PackageConfig{
		URL:     "https://example.invalid/releases",
		Parser:  "json",
		Path:    "version",
		Version: version,
		Series:  series,
	}
}

// TestResolveClaims pins the collection half of the verdict: which entries hold
// this directory, what each one pins, and what each one actually resolves to.
//
// The load-bearing property is D2 — resolution goes through selectCurrentEbuild,
// so ":slot" and `series` are filtered by the same code the checker and applier
// use. A second implementation here would drift, and the thing that drifts is
// which ebuild belongs to whom.
func TestResolveClaims(t *testing.T) {
	tests := []struct {
		name    string
		atom    string
		ebuilds []sweepEbuild
		cfgs    map[string]PackageConfig
		want    []claim
	}{
		{
			name:    "only entries for this atom claim",
			atom:    "net-misc/rclone",
			ebuilds: []sweepEbuild{{version: "1.71.0"}, {version: "1.71.1"}},
			cfgs: map[string]PackageConfig{
				"net-misc/rclone": regEntry("1.71.1", ""),
				// Same overlay, different directory: it must not be consulted.
				"app-misc/hello": regEntry("9.9.9", ""),
			},
			want: []claim{{Key: "net-misc/rclone", Pin: "1.71.1", Version: "1.71.1"}},
		},
		{
			name: "a :slot key resolves through the slot filter (R5.2)",
			atom: "net-libs/webkit-gtk",
			ebuilds: []sweepEbuild{
				{version: "2.52.4-r411", slot: "4.1/0"},
				{version: "2.52.5-r601", slot: "6/0"},
			},
			cfgs: map[string]PackageConfig{
				"net-libs/webkit-gtk:4.1": regEntry("2.52.4-r411", ""),
				"net-libs/webkit-gtk:6":   regEntry("2.52.5-r601", ""),
			},
			// Not both "2.52.5-r601": a slot-blind resolve would give the
			// directory's highest version to BOTH entries, and the 4.1 ebuild
			// would then be claimed by nobody.
			want: []claim{
				{Key: "net-libs/webkit-gtk:4.1", Pin: "2.52.4-r411", Version: "2.52.4-r411"},
				{Key: "net-libs/webkit-gtk:6", Pin: "2.52.5-r601", Version: "2.52.5-r601"},
			},
		},
		{
			name:    "an @label key resolves inside its own series",
			atom:    "media-plugins/gst-plugins-vpx",
			ebuilds: []sweepEbuild{{version: "1.28.5"}, {version: "1.29.2"}},
			cfgs: map[string]PackageConfig{
				"media-plugins/gst-plugins-vpx@stable": regEntry("1.28.5", gstStableSeries),
				"media-plugins/gst-plugins-vpx@dev":    regEntry("1.29.2", gstDevSeries),
			},
			want: []claim{
				{Key: "media-plugins/gst-plugins-vpx@dev", Pin: "1.29.2", Version: "1.29.2"},
				{Key: "media-plugins/gst-plugins-vpx@stable", Pin: "1.28.5", Version: "1.28.5"},
			},
		},
		{
			name: "an entry resolving to nothing claims with an empty version, not an error",
			atom: "net-libs/webkit-gtk",
			ebuilds: []sweepEbuild{
				{version: "2.52.4-r411", slot: "4.1/0"},
			},
			cfgs: map[string]PackageConfig{
				// ErrSlotNotFound: the package is there, this entry holds nothing
				// in it. A fact about one entry, not a reason to refuse a plan.
				"net-libs/webkit-gtk:6": regEntry("2.52.5-r601", ""),
			},
			want: []claim{{Key: "net-libs/webkit-gtk:6", Pin: "2.52.5-r601", Version: ""}},
		},
		{
			name:    "a pinless entry claims with an empty pin",
			atom:    "media-plugins/gst-plugins-vpx",
			ebuilds: []sweepEbuild{{version: "1.28.5"}},
			cfgs: map[string]PackageConfig{
				"media-plugins/gst-plugins-vpx@stable": regEntry("", gstStableSeries),
			},
			want: []claim{{Key: "media-plugins/gst-plugins-vpx@stable", Pin: "", Version: "1.28.5"}},
		},
		{
			name:    "a disabled entry still claims its ebuild",
			atom:    "net-misc/rclone",
			ebuilds: []sweepEbuild{{version: "1.71.1"}},
			cfgs: map[string]PackageConfig{
				// "stop checking upstream" is not "this ebuild is disposable".
				"net-misc/rclone": func() PackageConfig {
					c := regEntry("1.71.1", "")
					off := false
					c.Enabled = &off
					c.Hold = true
					return c
				}(),
			},
			want: []claim{{Key: "net-misc/rclone", Pin: "1.71.1", Version: "1.71.1"}},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			overlay := writeSweepOverlay(t, tt.atom, tt.ebuilds)
			got := resolveClaims(overlay, tt.cfgs, tt.atom)
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("resolveClaims(%q) =\n  %+v\nwant\n  %+v", tt.atom, got, tt.want)
			}
		})
	}

	t.Run("a nil registry and a malformed atom claim nothing", func(t *testing.T) {
		overlay := writeSweepOverlay(t, "net-misc/rclone", []sweepEbuild{{version: "1.71.1"}})
		if got := resolveClaims(overlay, nil, "net-misc/rclone"); got != nil {
			t.Errorf("resolveClaims with a nil registry = %+v, want nil", got)
		}
		cfgs := map[string]PackageConfig{"net-misc/rclone": regEntry("1.71.1", "")}
		if got := resolveClaims(overlay, cfgs, "rclone"); got != nil {
			t.Errorf("resolveClaims with a malformed atom = %+v, want nil", got)
		}
	})
}

// TestPlanSweep is the verdict itself: what a --clean would keep, what it would
// remove, and when it must refuse. Each case is one rule, because each rule
// alone is what stands between a plan and a deleted release line.
func TestPlanSweep(t *testing.T) {
	tests := []struct {
		name    string
		atom    string
		ebuilds []sweepEbuild
		cfgs    map[string]PackageConfig
		// wantWouldRemove is nil on every unblocked case: a caller must never
		// have to wonder which of the two lists is the live one.
		wantKeep        map[string]string
		wantRemove      []string
		wantWouldRemove []string
		wantBlocked     string
	}{
		{
			// (a) The residue case — the only shape a sweep is actually for.
			name:    "a: one entry, pin on the newer, the superseded ebuild is removed",
			atom:    "net-misc/rclone",
			ebuilds: []sweepEbuild{{version: "1.71.0"}, {version: "1.71.1"}},
			cfgs:    map[string]PackageConfig{"net-misc/rclone": regEntry("1.71.1", "")},
			wantKeep: map[string]string{
				"1.71.1": "net-misc/rclone",
			},
			wantRemove: []string{"1.71.0"},
		},
		{
			// (b) UB3 regression guard. 85 GStreamer directories look like this,
			// and a "keep only the highest" sweep deletes the stable line in
			// every one of them.
			name:    "b: two entries by series, both pinned, nothing is removed",
			atom:    "media-plugins/gst-plugins-vpx",
			ebuilds: []sweepEbuild{{version: "1.28.5"}, {version: "1.29.2"}},
			cfgs: map[string]PackageConfig{
				"media-plugins/gst-plugins-vpx@stable": regEntry("1.28.5", gstStableSeries),
				"media-plugins/gst-plugins-vpx@dev":    regEntry("1.29.2", gstDevSeries),
			},
			wantKeep: map[string]string{
				"1.28.5": "media-plugins/gst-plugins-vpx@stable",
				"1.29.2": "media-plugins/gst-plugins-vpx@dev",
			},
			wantRemove: nil,
		},
		{
			// (b2) THE BATCH REGRESSION — case (b) with one detail changed, and
			// that detail used to delete the stable release line.
			//
			// Everything in (b) has each sibling pinned to exactly what is on
			// disk. Here @stable's pin is one release behind, which is not an
			// exotic misconfiguration: it is the state `--apply all --clean`
			// produces by itself. That command builds ONE Applier, snapshots the
			// registry at construction and never reloads it, and cleanPackageDir
			// freshens the pin of the entry being applied ONLY (sweepConfigs).
			// So on a release day where both GStreamer lines move — routine:
			//
			//	apply @stable 1.28.4 -> 1.28.5 --clean   removes 1.28.4   fine
			//	apply @dev    1.29.2 -> 1.29.3 --clean   plans @stable against
			//	                                         the snapshot's 1.28.4
			//
			// Claiming by pin alone, the 1.28.5 built seconds earlier is held by
			// nobody and goes into Remove — Success still true, no CleanWarning,
			// and the registry left pinning a file that no longer exists. The
			// floor cannot catch it: two other ebuilds survive, so it never
			// fires. What holds 1.28.5 is that @stable RESOLVES to it, which is
			// this package's rule everywhere else (unclaimedIn) and now here.
			//
			// Reversing the apply order loses the dev line instead. Either order
			// destroys one, in any of the 90 two-entry directories.
			//
			// The sweep must still do its job: 1.29.2, the release @dev really
			// did supersede, is removed.
			name: "b2: a sibling's stale pin does not license deleting the ebuild the batch just applied",
			atom: "media-plugins/gst-plugins-vpx",
			ebuilds: []sweepEbuild{
				{version: "1.28.5"}, // built by the @stable apply moments ago
				{version: "1.29.2"}, // the release @dev is superseding
				{version: "1.29.3"}, // built by the @dev apply now running
			},
			cfgs: map[string]PackageConfig{
				// The pre-run pin, now stale: 1.28.4 was deleted by @stable's
				// own clean earlier in this very command.
				"media-plugins/gst-plugins-vpx@stable": regEntry("1.28.4", gstStableSeries),
				// Fresh, the way sweepConfigs overlays the entry being applied.
				"media-plugins/gst-plugins-vpx@dev": regEntry("1.29.3", gstDevSeries),
			},
			wantKeep: map[string]string{
				// Attributed to the entry that holds it by RESOLUTION rather
				// than by pin — the report can still name who holds it.
				"1.28.5": "media-plugins/gst-plugins-vpx@stable",
				"1.29.3": "media-plugins/gst-plugins-vpx@dev",
			},
			wantRemove: []string{"1.29.2"},
		},
		{
			// (c) The other half of UB3, and R5.2: two SLOTs out of one
			// directory, told apart only by the SLOT= line inside each file.
			name: "c: two entries by :slot, both pinned, nothing is removed",
			atom: "net-libs/webkit-gtk",
			ebuilds: []sweepEbuild{
				{version: "2.52.4-r411", slot: "4.1/0"},
				{version: "2.52.5-r601", slot: "6/0"},
			},
			cfgs: map[string]PackageConfig{
				"net-libs/webkit-gtk:4.1": regEntry("2.52.4-r411", ""),
				"net-libs/webkit-gtk:6":   regEntry("2.52.5-r601", ""),
			},
			wantKeep: map[string]string{
				"2.52.4-r411": "net-libs/webkit-gtk:4.1",
				"2.52.5-r601": "net-libs/webkit-gtk:6",
			},
			wantRemove: nil,
		},
		{
			// (c2) The same batch drift as (b2), across a ":slot" boundary
			// instead of a series one — and this is the shape where nothing
			// else can save the file.
			//
			// The R4.3 floor is per-DIRECTORY, not per-slot or per-line. Here
			// the 6 slot keeps an ebuild of its own, so the directory never
			// comes close to empty and the floor never fires; there is nothing
			// at all between :4.1's ONLY ebuild and deletion. Claiming by pin
			// alone, Remove would be [2.52.4-r601, 2.52.5-r411] and the 4.1 slot
			// would disappear from the overlay entirely.
			//
			// The batch: :4.1 was bumped first, so its pin is still the pre-run
			// -r411 revision, and :6 is the entry being applied now.
			name: "c2: a stale :slot pin does not license deleting the sibling slot's only ebuild",
			atom: "net-libs/webkit-gtk",
			ebuilds: []sweepEbuild{
				{version: "2.52.5-r411", slot: "4.1/0"}, // built by the :4.1 apply moments ago
				{version: "2.52.4-r601", slot: "6/0"},   // the revision :6 is superseding
				{version: "2.52.5-r601", slot: "6/0"},   // built by the :6 apply now running
			},
			cfgs: map[string]PackageConfig{
				"net-libs/webkit-gtk:4.1": regEntry("2.52.4-r411", ""), // pre-run, stale
				"net-libs/webkit-gtk:6":   regEntry("2.52.5-r601", ""), // fresh
			},
			wantKeep: map[string]string{
				"2.52.5-r411": "net-libs/webkit-gtk:4.1", // held by resolution, not by pin
				"2.52.5-r601": "net-libs/webkit-gtk:6",
			},
			wantRemove: []string{"2.52.4-r601"},
		},
		{
			// (d) R5.1/D3: one entry cannot say what it holds, so the whole
			// directory is off limits. Guessing here deletes a release line.
			name:    "d: one pinless entry blocks the directory and names itself",
			atom:    "media-plugins/gst-plugins-vpx",
			ebuilds: []sweepEbuild{{version: "1.28.5"}, {version: "1.29.2"}},
			cfgs: map[string]PackageConfig{
				"media-plugins/gst-plugins-vpx@stable": regEntry("1.28.5", gstStableSeries),
				"media-plugins/gst-plugins-vpx@dev":    regEntry("", gstDevSeries),
			},
			wantKeep: map[string]string{
				"1.28.5": "media-plugins/gst-plugins-vpx@stable",
			},
			wantRemove: nil,
			// R5.1's second clause: remove nothing, but SAY what was at stake.
			// The floor is respected here too (1.28.5 is pinned, so removing
			// 1.29.2 would not have emptied the directory) and no -9999 can
			// appear, because live ebuilds never become candidates.
			wantWouldRemove: []string{"1.29.2"},
			wantBlocked:     "media-plugins/gst-plugins-vpx@dev",
		},
		{
			// (d2) The same block with both properties in play: a live ebuild
			// present, and every non-live ebuild a candidate. WouldRemove must
			// be what Remove would really have been — 9999 absent because live
			// ebuilds are never candidates, 1.29.2 absent because the R4.3 floor
			// spares the highest. A report that named either would send a
			// maintainer hunting a deletion that could never have happened.
			// This is also today's registry shape: no pins written yet.
			name:    "d2: a blocked plan's candidates respect the floor and exclude -9999",
			atom:    "media-plugins/gst-plugins-vpx",
			ebuilds: []sweepEbuild{{version: "1.28.5"}, {version: "1.29.2"}, {version: "9999"}},
			cfgs: map[string]PackageConfig{
				"media-plugins/gst-plugins-vpx@stable": regEntry("", gstStableSeries),
				"media-plugins/gst-plugins-vpx@dev":    regEntry("", gstDevSeries),
			},
			wantKeep:        map[string]string{"9999": ""},
			wantRemove:      nil,
			wantWouldRemove: []string{"1.28.5"},
			wantBlocked:     "media-plugins/gst-plugins-vpx@dev",
		},
		{
			// (e) The live ebuild is claimed by nobody — selection skips it, so
			// no pin can ever name it — and must survive anyway, while a real
			// removal goes ahead in the same directory.
			name: "e: a live -9999 ebuild is kept and never removed",
			atom: "app-editors/neovim",
			ebuilds: []sweepEbuild{
				{version: "0.11.0"}, {version: "0.11.1"}, {version: "9999"},
			},
			cfgs: map[string]PackageConfig{"app-editors/neovim": regEntry("0.11.1", "")},
			wantKeep: map[string]string{
				"0.11.1": "app-editors/neovim",
				"9999":   "", // kept by the live rule, claimed by no entry
			},
			wantRemove: []string{"0.11.0"},
		},
		{
			// (f) R4.3: the pin says 2.0.0, the overlay has only 1.0.0. Honour
			// the pin literally and the directory ends up with no ebuild at all.
			name:    "f: the last non-live ebuild is never removed, whatever the pin says",
			atom:    "app-misc/hello",
			ebuilds: []sweepEbuild{{version: "1.0.0"}},
			cfgs:    map[string]PackageConfig{"app-misc/hello": regEntry("2.0.0", "")},
			// The entry pins a version that is not there but RESOLVES to 1.0.0,
			// so rule 4's resolved half keeps it and the report can name who
			// holds it — strictly more informative than the anonymous keep the
			// floor would have produced. The floor is the backstop underneath:
			// it catches the same directory when the entry resolves to nothing
			// at all (see f2), and Remove is nil either way.
			wantKeep:   map[string]string{"1.0.0": "app-misc/hello"},
			wantRemove: nil,
		},
		{
			// (f2) R4.3 with nothing else underneath it — the case (f) used to
			// be, and stopped being once an entry could hold an ebuild by
			// resolving to it. The @dev entry pins a dev release the overlay
			// does not have and its `series` matches nothing there either, so it
			// resolves to NOTHING: no pin holds a file, no resolution holds a
			// file, and every ebuild in the directory is a removal candidate.
			// The floor is the only thing left, and what it keeps is anonymous
			// (kept by a rule, not by an entry) — which is exactly the
			// distinction sweepPlan.Keep's empty value exists to record.
			//
			// Real shape: an @dev entry registered before the first dev bump, or
			// left behind after the dev ebuild was dropped.
			name:    "f2: the floor still keeps the last ebuild when the entry resolves to nothing at all",
			atom:    "media-plugins/gst-plugins-vpx",
			ebuilds: []sweepEbuild{{version: "1.28.5"}},
			cfgs: map[string]PackageConfig{
				"media-plugins/gst-plugins-vpx@dev": regEntry("1.29.2", gstDevSeries),
			},
			wantKeep:   map[string]string{"1.28.5": ""},
			wantRemove: nil,
		},
		{
			// The report's order is Gentoo's, not the alphabet's: sorted as
			// strings this would read 1.10.0, 1.71.0, 1.9.0.
			name: "removals are ordered by Gentoo comparison, not lexically",
			atom: "net-misc/rclone",
			ebuilds: []sweepEbuild{
				{version: "1.9.0"}, {version: "1.10.0"}, {version: "1.71.0"}, {version: "1.71.1"},
			},
			cfgs:       map[string]PackageConfig{"net-misc/rclone": regEntry("1.71.1", "")},
			wantKeep:   map[string]string{"1.71.1": "net-misc/rclone"},
			wantRemove: []string{"1.9.0", "1.10.0", "1.71.0"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			overlay := writeSweepOverlay(t, tt.atom, tt.ebuilds)
			got, err := planSweep(overlay, tt.cfgs, tt.atom)
			if err != nil {
				t.Fatalf("planSweep(%q): %v", tt.atom, err)
			}
			if !reflect.DeepEqual(got.Keep, tt.wantKeep) {
				t.Errorf("Keep = %v, want %v", got.Keep, tt.wantKeep)
			}
			if !reflect.DeepEqual(got.Remove, tt.wantRemove) {
				t.Errorf("Remove = %v, want %v", got.Remove, tt.wantRemove)
			}
			if !reflect.DeepEqual(got.WouldRemove, tt.wantWouldRemove) {
				t.Errorf("WouldRemove = %v, want %v", got.WouldRemove, tt.wantWouldRemove)
			}
			if got.Blocked != tt.wantBlocked {
				t.Errorf("Blocked = %q, want %q", got.Blocked, tt.wantBlocked)
			}
		})
	}

	// (g) An unreadable directory must fail loudly. The alternative — an empty
	// plan — reads as "nothing is claimed here", which is one caller away from
	// "remove everything".
	t.Run("g: an unreadable directory yields an error, never an empty plan", func(t *testing.T) {
		if os.Geteuid() == 0 {
			t.Skip("running as root: the directory mode is not enforced, so nothing here is unreadable")
		}
		atom := "app-misc/hello"
		overlay := writeSweepOverlay(t, atom, []sweepEbuild{{version: "1.0.0"}})
		pkgDir := filepath.Join(overlay, "app-misc", "hello")
		if err := os.Chmod(pkgDir, 0o000); err != nil {
			t.Fatalf("chmod: %v", err)
		}
		// Restore the mode, or t.TempDir()'s own cleanup cannot remove the tree.
		t.Cleanup(func() { _ = os.Chmod(pkgDir, 0o755) })

		got, err := planSweep(overlay, map[string]PackageConfig{atom: regEntry("1.0.0", "")}, atom)
		if err == nil {
			t.Fatalf("an unreadable directory produced a plan instead of an error: %+v", got)
		}
		if !strings.Contains(err.Error(), atom) {
			t.Errorf("the error does not name the package it is about: %v", err)
		}
		if got.Keep != nil || got.Remove != nil || got.Blocked != "" {
			t.Errorf("the failed plan is not the zero value: %+v", got)
		}
	})

	// The security note, asserted: a key carrying ":slot"/"@label" must be split
	// before it reaches a path. If it were not, this call would look for a
	// directory literally named "webkit-gtk:4.1" — and the sweep DELETES files,
	// so a key-to-path mistake is destructive, not merely wrong.
	t.Run("a suffixed key names the same directory as its bare atom", func(t *testing.T) {
		atom := "net-libs/webkit-gtk"
		overlay := writeSweepOverlay(t, atom, []sweepEbuild{
			{version: "2.52.4-r411", slot: "4.1/0"},
			{version: "2.52.5-r601", slot: "6/0"},
		})
		cfgs := map[string]PackageConfig{
			"net-libs/webkit-gtk:4.1": regEntry("2.52.4-r411", ""),
			"net-libs/webkit-gtk:6":   regEntry("2.52.5-r601", ""),
		}

		want, err := planSweep(overlay, cfgs, atom)
		if err != nil {
			t.Fatalf("planSweep(bare atom): %v", err)
		}
		for _, key := range []string{"net-libs/webkit-gtk:4.1", "net-libs/webkit-gtk:6@lts"} {
			got, err := planSweep(overlay, cfgs, key)
			if err != nil {
				t.Fatalf("planSweep(%q): %v", key, err)
			}
			if !reflect.DeepEqual(got, want) {
				t.Errorf("planSweep(%q) = %+v, want the bare atom's plan %+v", key, got, want)
			}
		}
	})

	// Two entries without a pin: the report must name the same one every run,
	// or a maintainer chasing the block is chasing a coin flip.
	t.Run("the blocking entry is the first in key order", func(t *testing.T) {
		atom := "media-plugins/gst-plugins-vpx"
		overlay := writeSweepOverlay(t, atom, []sweepEbuild{{version: "1.28.5"}, {version: "1.29.2"}})
		cfgs := map[string]PackageConfig{
			atom + "@stable": regEntry("", gstStableSeries),
			atom + "@dev":    regEntry("", gstDevSeries),
		}
		for i := 0; i < 8; i++ {
			got, err := planSweep(overlay, cfgs, atom)
			if err != nil {
				t.Fatalf("planSweep: %v", err)
			}
			if got.Blocked != atom+"@dev" {
				t.Fatalf("Blocked = %q, want %q", got.Blocked, atom+"@dev")
			}
			if len(got.Remove) != 0 {
				t.Fatalf("a blocked plan removes %v", got.Remove)
			}
		}
	})

	// A directory NO entry claims is blocked, not swept.
	//
	// This is the one path that reaches the 89-directory disaster without any pin
	// being wrong: an atom that fails to match its key, a registry that loaded
	// empty, a filter that selected nothing — and a sweep reading R4.1 literally
	// ("keep what is claimed, remove the rest") would then delete every release
	// line but the newest from a directory it simply failed to look up. Zero
	// claims is the least-informed state there is, so it deletes nothing and
	// reports instead. Blocked stays empty because there is no entry to name.
	t.Run("a directory no entry claims is blocked, not swept", func(t *testing.T) {
		atom := "app-misc/hello"
		// The -9999 is here to make the Keep rule observable: it would be kept by
		// the live rule in any other plan, and must still NOT appear as a keep
		// here, because a keep the report cannot attribute to an entry is
		// fabricated evidence. Nothing is deleted either way.
		overlay := writeSweepOverlay(t, atom, []sweepEbuild{
			{version: "1.0.0"}, {version: "2.0.0"}, {version: "9999"},
		})
		for _, cfgs := range []map[string]PackageConfig{
			{},  // registry loaded, nothing matches this atom
			nil, // registry did not load at all
			{"app-misc/other": regEntry("1.0.0", "")}, // matches, but another directory
		} {
			got, err := planSweep(overlay, cfgs, atom)
			if err != nil {
				t.Fatalf("planSweep: %v", err)
			}
			if len(got.Remove) != 0 {
				t.Errorf("cfgs %v: Remove = %v, want nothing deleted", cfgs, got.Remove)
			}
			if got.Blocked != "" {
				t.Errorf("cfgs %v: Blocked = %q, want empty — there is no entry to name", cfgs, got.Blocked)
			}
			if !reflect.DeepEqual(got.WouldRemove, []string{"1.0.0"}) {
				t.Errorf("cfgs %v: WouldRemove = %v, want [1.0.0]", cfgs, got.WouldRemove)
			}
			if len(got.Keep) != 0 {
				t.Errorf("cfgs %v: Keep = %v, want empty — nothing is claimed by anything", cfgs, got.Keep)
			}
		}
	})
}

// writeReconcileOverlay lays SEVERAL package directories out in one fresh
// t.TempDir() and returns the overlay root.
//
// Reconcile walks the whole registry rather than one atom, so its fixtures need
// a tree with more than one directory in it — which is the only reason this is
// not writeSweepOverlay. Temporary for the same reason that one is: the output
// feeds a write to a registry that auto-publishes, so a fixture must never point
// at a real overlay.
func writeReconcileOverlay(t *testing.T, dirs map[string][]sweepEbuild) string {
	t.Helper()
	overlay := filepath.Join(t.TempDir(), "overlay")
	for atom, ebuilds := range dirs {
		for _, e := range ebuilds {
			if e.slot == "" {
				createTestEbuildFile(t, overlay, atom, e.version)
				continue
			}
			createTestEbuildFileWithContent(t, overlay, atom, e.version,
				"# Test ebuild\nEAPI=8\nDESCRIPTION=\"Test package\"\nSLOT=\""+e.slot+"\"\nKEYWORDS=\"~amd64\"\n")
		}
	}
	return overlay
}

// offEntry is a registry entry the checker skips: enabled = false, the state the
// existing orphan reconciliation writes and owns (R3.5).
func offEntry(version, series string) PackageConfig {
	c := regEntry(version, series)
	off := false
	c.Enabled = &off
	return c
}

// heldEntry is a registry entry the maintainer has parked: hold = true.
func heldEntry(version, series string) PackageConfig {
	c := regEntry(version, series)
	c.Hold = true
	return c
}

// TestReconcile pins the divergence set itself: which of R3.1's three classes
// each disagreement between the registry and the overlay falls into.
//
// What makes each case load-bearing is that the output is about to be turned
// into a WRITE to a registry that auto-publishes. A class that is wrong here is
// a wrong pin on origin minutes later, so every case below is a shape that
// really occurs in the overlay, not an invented one.
func TestReconcile(t *testing.T) {
	tests := []struct {
		name string
		dirs map[string][]sweepEbuild
		cfgs map[string]PackageConfig
		want []Divergence
	}{
		{
			// All three classes at once, which is also the shape of a real run:
			// most of the noise is in one or two directories.
			name: "one of each class",
			dirs: map[string][]sweepEbuild{
				"net-misc/rclone": {{version: "1.70.0"}, {version: "1.71.0"}, {version: "1.71.1"}},
			},
			cfgs: map[string]PackageConfig{
				"net-misc/rclone": regEntry("1.71.0", ""),
				// No directory for it at all: the package was removed from the
				// overlay. R3.1's third class.
				"app-misc/hello": regEntry("1.0.0", ""),
			},
			want: []Divergence{
				{Key: "app-misc/hello", Kind: NoEbuild, Pin: "1.0.0"},
				// The entry pins 1.71.0 but resolves to the higher 1.71.1.
				{Key: "net-misc/rclone", Kind: StalePin, Pin: "1.71.0", Disk: "1.71.1"},
				// 1.71.0 is NOT unclaimed — the pin names it — but 1.70.0 is
				// held by nothing at all. Key is the directory, not the entry.
				{Key: "net-misc/rclone", Kind: UnclaimedEbuild, Disk: "1.70.0"},
			},
		},
		{
			// A1, the whole point of the first run: all 409 records are pinless
			// today, so an enabled entry that resolves to an ebuild is a stale
			// pin whose Pin happens to be empty. 5.2 builds its write batch from
			// exactly these — classify them as anything else and the ~317-entry
			// bulk fill has nothing to write.
			name: "an empty pin that resolves to an ebuild is a stale pin (A1)",
			dirs: map[string][]sweepEbuild{
				"media-plugins/gst-plugins-vpx": {{version: "1.28.5"}, {version: "1.29.2"}},
			},
			cfgs: map[string]PackageConfig{
				"media-plugins/gst-plugins-vpx@stable": regEntry("", gstStableSeries),
				"media-plugins/gst-plugins-vpx@dev":    regEntry("", gstDevSeries),
			},
			want: []Divergence{
				{Key: "media-plugins/gst-plugins-vpx@dev", Kind: StalePin, Pin: "", Disk: "1.29.2"},
				{Key: "media-plugins/gst-plugins-vpx@stable", Kind: StalePin, Pin: "", Disk: "1.28.5"},
			},
			// And no UnclaimedEbuild anywhere: with nothing pinned, an entry
			// still holds the file it RESOLVES to. Claiming by pin alone would
			// report every ebuild in the overlay as unclaimed on the first run
			// and bury the pins that actually need writing.
		},
		{
			// R3.5: the enabled = false reconciliation owns these entries. This
			// must neither report them nor — since the directory is never
			// reached — call their ebuilds unclaimed.
			name: "a disabled entry and a held entry produce nothing (R3.5)",
			dirs: map[string][]sweepEbuild{
				"net-misc/rclone": {{version: "1.70.0"}, {version: "1.71.1"}},
				"app-misc/hello":  {{version: "1.0.0"}},
			},
			cfgs: map[string]PackageConfig{
				"net-misc/rclone": offEntry("", ""),  // stale pin AND residue, both invisible
				"app-misc/hello":  heldEntry("", ""), // a deliberate maintainer decision
			},
			want: nil,
		},
		{
			// The other half of R3.5, and the reason resolveClaims does not skip
			// disabled entries: "stop checking upstream" is not "this ebuild is
			// disposable". The :6 entry is switched off, so it is not reported —
			// but it still holds its ebuild, which must not be offered up as
			// claimed by nobody.
			name: "a disabled sibling still claims its ebuild",
			dirs: map[string][]sweepEbuild{
				"net-libs/webkit-gtk": {
					{version: "2.52.4-r411", slot: "4.1/0"},
					{version: "2.52.5-r601", slot: "6/0"},
				},
			},
			cfgs: map[string]PackageConfig{
				"net-libs/webkit-gtk:4.1": regEntry("2.52.4-r411", ""),
				"net-libs/webkit-gtk:6":   offEntry("2.52.5-r601", ""),
			},
			want: nil,
		},
		{
			// R5.2 through the reconciliation: the package is right there, the
			// entry's slot filter matches nothing in it. A fact about one entry
			// — R3.1's third class — never an error, and never an
			// ErrNoEbuildFound that would read as "the package was removed".
			name: "a slot that matches nothing is a NoEbuild, and leaves the sibling ebuild unclaimed",
			dirs: map[string][]sweepEbuild{
				"net-libs/webkit-gtk": {{version: "2.52.4-r411", slot: "4.1/0"}},
			},
			cfgs: map[string]PackageConfig{
				"net-libs/webkit-gtk:6": regEntry("2.52.5-r601", ""),
			},
			want: []Divergence{
				{Key: "net-libs/webkit-gtk", Kind: UnclaimedEbuild, Disk: "2.52.4-r411"},
				{Key: "net-libs/webkit-gtk:6", Kind: NoEbuild, Pin: "2.52.5-r601"},
			},
		},
		{
			// The pin is compared to the resolved version as an exact STRING,
			// never through ebuild.CompareVersions — and the two disagree
			// exactly here: the comparison calls "1.71.1-r0" and "1.71.1" equal.
			// The sweep sides with the string, because planSweep keeps a file by
			// looking its version up in a map keyed by the pin's text, so a pin
			// of "1.71.1-r0" protects rclone-1.71.1.ebuild from nothing.
			// Declaring it "not stale" would leave a pin that reads correct and
			// lets --clean delete the ebuild it names.
			name: "a pin that only COMPARES equal is still stale",
			dirs: map[string][]sweepEbuild{
				"net-misc/rclone": {{version: "1.71.1"}},
			},
			cfgs: map[string]PackageConfig{"net-misc/rclone": regEntry("1.71.1-r0", "")},
			want: []Divergence{
				{Key: "net-misc/rclone", Kind: StalePin, Pin: "1.71.1-r0", Disk: "1.71.1"},
			},
		},
		{
			// A live ebuild is claimed by nobody by construction — selection
			// skips it (UB2), so no pin can ever name one — and reporting that
			// would put the one file an overlay cannot re-fetch at the top of a
			// removal candidate list.
			name: "a live -9999 ebuild is never unclaimed",
			dirs: map[string][]sweepEbuild{
				"app-editors/neovim": {{version: "0.11.0"}, {version: "0.11.1"}, {version: "9999"}},
			},
			cfgs: map[string]PackageConfig{
				"app-editors/neovim": regEntry("0.11.1", ""),
			},
			want: []Divergence{
				{Key: "app-editors/neovim", Kind: UnclaimedEbuild, Disk: "0.11.0"},
			},
		},
		{
			// The registry agrees with the overlay: no prompt, no write, nothing
			// to confirm. The state every run after the first one should be in.
			name: "a registry that matches the overlay diverges in nothing",
			dirs: map[string][]sweepEbuild{
				"net-misc/rclone": {{version: "1.71.1"}},
			},
			cfgs: map[string]PackageConfig{"net-misc/rclone": regEntry("1.71.1", "")},
			want: nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			overlay := writeReconcileOverlay(t, tt.dirs)
			got := Reconcile(overlay, tt.cfgs)
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("Reconcile() =\n  %+v\nwant\n  %+v", got, tt.want)
			}
		})
	}

	// One stray file, several entries sharing the directory: the finding belongs
	// to the FILE, so it is reported once. Emitted per sibling entry it would
	// double in the 85 GStreamer directories built exactly like this, and the
	// prompt's count of what is about to be reviewed would be fiction.
	t.Run("a stray ebuild in a two-entry directory is reported once, not per entry", func(t *testing.T) {
		atom := "media-plugins/gst-plugins-vpx"
		overlay := writeReconcileOverlay(t, map[string][]sweepEbuild{
			atom: {{version: "1.28.4"}, {version: "1.28.5"}, {version: "1.29.2"}},
		})
		cfgs := map[string]PackageConfig{
			atom + "@stable": regEntry("1.28.5", gstStableSeries),
			atom + "@dev":    regEntry("1.29.2", gstDevSeries),
		}

		got := Reconcile(overlay, cfgs)
		want := []Divergence{{Key: atom, Kind: UnclaimedEbuild, Disk: "1.28.4"}}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("Reconcile() =\n  %+v\nwant exactly one unclaimed ebuild\n  %+v", got, want)
		}
	})

	// An unreadable directory is the one case that must produce NOTHING. An
	// invented divergence here is an invented pin in a registry that publishes
	// itself minutes later, so the entry is dropped and the warning says which
	// one and why.
	t.Run("an unreadable directory invents no divergence and warns", func(t *testing.T) {
		if os.Geteuid() == 0 {
			t.Skip("running as root: the directory mode is not enforced, so nothing here is unreadable")
		}
		atom := "app-misc/hello"
		overlay := writeReconcileOverlay(t, map[string][]sweepEbuild{
			atom: {{version: "1.0.0"}, {version: "2.0.0"}},
		})
		pkgDir := filepath.Join(overlay, "app-misc", "hello")
		if err := os.Chmod(pkgDir, 0o000); err != nil {
			t.Fatalf("chmod: %v", err)
		}
		// Restore the mode, or t.TempDir()'s own cleanup cannot remove the tree.
		t.Cleanup(func() { _ = os.Chmod(pkgDir, 0o755) })

		lc := captureWarnLogs(t)
		got := Reconcile(overlay, map[string]PackageConfig{atom: regEntry("1.0.0", "")})
		if got != nil {
			t.Fatalf("an unreadable directory produced divergences: %+v", got)
		}
		if lc.count() == 0 {
			t.Fatalf("the skipped entry was not warned about")
		}
		if !strings.Contains(strings.Join(lc.all(), "\n"), atom) {
			t.Errorf("the warning does not name the entry it skipped: %v", lc.all())
		}
	})

	// A key that is not an atom cannot be resolved to a directory, and a
	// reconciliation that guessed one would be building a path out of a string
	// it does not understand.
	t.Run("a malformed key is skipped with a warning", func(t *testing.T) {
		overlay := writeReconcileOverlay(t, map[string][]sweepEbuild{
			"net-misc/rclone": {{version: "1.71.1"}},
		})
		lc := captureWarnLogs(t)
		if got := Reconcile(overlay, map[string]PackageConfig{"rclone": regEntry("1.71.1", "")}); got != nil {
			t.Fatalf("a malformed key produced divergences: %+v", got)
		}
		if lc.count() == 0 {
			t.Errorf("the malformed key was skipped silently")
		}
	})

	t.Run("a nil registry diverges in nothing", func(t *testing.T) {
		overlay := writeReconcileOverlay(t, map[string][]sweepEbuild{
			"net-misc/rclone": {{version: "1.71.1"}},
		})
		if got := Reconcile(overlay, nil); got != nil {
			t.Errorf("Reconcile with a nil registry = %+v, want nil", got)
		}
	})
}

// TestReconcileOrderIsStable is the prompt's precondition: a maintainer
// confirms this list in one go, so two runs over an unchanged overlay must
// produce the identical list in the identical order. A reshuffle between runs
// is indistinguishable from the overlay having changed.
//
// The fixture is built to catch an order that merely LOOKS sorted: the entry
// divergences already come out in key order (the walk is over sorted keys), so
// the only way to observe the sort is a directory-level UnclaimedEbuild, whose
// key is the bare atom and therefore sorts BEFORE the "@label" siblings that
// produced it — but is appended after the first of them.
func TestReconcileOrderIsStable(t *testing.T) {
	vpx := "media-plugins/gst-plugins-vpx"
	overlay := writeReconcileOverlay(t, map[string][]sweepEbuild{
		vpx: {{version: "1.28.4"}, {version: "1.28.5"}, {version: "1.29.2"}},
		// Two strays in one directory, out of Gentoo order lexically: sorted as
		// strings 1.10.0 precedes 1.9.0.
		"net-misc/rclone":    {{version: "1.9.0"}, {version: "1.10.0"}, {version: "1.71.1"}},
		"app-editors/neovim": {{version: "0.11.1"}},
	})
	cfgs := map[string]PackageConfig{
		vpx + "@stable":      regEntry("1.28.5", gstStableSeries),
		vpx + "@dev":         regEntry("1.29.2", gstDevSeries),
		"net-misc/rclone":    regEntry("1.71.1", ""),
		"app-editors/neovim": regEntry("", ""),
		"app-misc/hello":     regEntry("1.0.0", ""), // no directory: NoEbuild
	}

	want := []Divergence{
		{Key: "app-editors/neovim", Kind: StalePin, Pin: "", Disk: "0.11.1"},
		{Key: "app-misc/hello", Kind: NoEbuild, Pin: "1.0.0"},
		{Key: vpx, Kind: UnclaimedEbuild, Disk: "1.28.4"},
		{Key: "net-misc/rclone", Kind: UnclaimedEbuild, Disk: "1.9.0"},
		{Key: "net-misc/rclone", Kind: UnclaimedEbuild, Disk: "1.10.0"},
	}

	// Eight runs, because a map-iteration order that leaks into the output is a
	// coin flip, not a constant failure.
	for i := 0; i < 8; i++ {
		got := Reconcile(overlay, cfgs)
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("run %d: Reconcile() =\n  %+v\nwant\n  %+v", i, got, want)
		}
	}
}

// TestDivergenceKindString pins the identifiers a report and a filter both key
// on: they are stable strings, not the integers they happen to be.
func TestDivergenceKindString(t *testing.T) {
	want := map[DivergenceKind]string{
		StalePin:        "stale-pin",
		UnclaimedEbuild: "unclaimed-ebuild",
		NoEbuild:        "no-ebuild",
	}
	seen := make(map[string]bool)
	for kind, s := range want {
		if got := kind.String(); got != s {
			t.Errorf("DivergenceKind(%d).String() = %q, want %q", int(kind), got, s)
		}
		if seen[s] {
			t.Errorf("two kinds render as %q", s)
		}
		seen[s] = true
	}
	if got := DivergenceKind(42).String(); !strings.Contains(got, "42") {
		t.Errorf("an unknown kind renders as %q, which names no value", got)
	}
}
