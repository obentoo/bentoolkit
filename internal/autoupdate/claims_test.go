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
			name:       "f: the last non-live ebuild is never removed, whatever the pin says",
			atom:       "app-misc/hello",
			ebuilds:    []sweepEbuild{{version: "1.0.0"}},
			cfgs:       map[string]PackageConfig{"app-misc/hello": regEntry("2.0.0", "")},
			wantKeep:   map[string]string{"1.0.0": ""}, // kept by the floor, not by a claim
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
