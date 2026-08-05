package main

import (
	"testing"

	"github.com/obentoo/bentoolkit/internal/overlay"
)

// divergenceRegistry is the shape the real registry has: keys carry `:slot` and
// `@label` suffixes, 90 of 321 atoms carry more than one entry, and most entries
// declare no divergence at all.
const divergenceRegistry = `["net-libs/webkit-gtk:4.1"]
url = "https://api.github.com/repos/WebKit/WebKit/tags"
parser = "json"
path = "0.name"
patched = "our GTK4 backport"
comments = """
webkit-gtk — slot-suffixed key.
"""
# END

["media-libs/gstreamer@dev"]
url = "https://gstreamer.freedesktop.org/src/gstreamer/"
parser = "regex"
pattern = 'gstreamer-([0-9.]+)\.tar\.xz'
patched = "keeps the extra plugin directory"
comments = """
gstreamer dev line.
"""
# END

["media-libs/gstreamer@stable"]
url = "https://gstreamer.freedesktop.org/src/gstreamer/"
parser = "regex"
pattern = 'gstreamer-([0-9.]+)\.tar\.xz'
patched = "the same delta, declared twice"
comments = """
gstreamer stable line.
"""
# END

["dev-lang/rust:beta"]
url = "https://api.github.com/repos/rust-lang/rust/releases"
parser = "json"
path = "0.tag_name"
comments = """
rust beta — declares nothing.
"""
# END

["dev-lang/rust:nightly"]
url = "https://api.github.com/repos/rust-lang/rust/releases"
parser = "json"
path = "0.tag_name"
patched = "our bootstrap patch"
comments = """
rust nightly — the only entry of this atom that declares a divergence.
"""
# END

["app-editors/zed"]
url = "https://api.github.com/repos/zed-industries/zed/releases/latest"
parser = "json"
path = "tag_name"
comments = """
zed — known to the registry and declaring nothing.
"""
# END

["justapackage"]
url = "https://example.com/releases"
parser = "json"
path = "tag_name"
patched = "a key with no category at all"
comments = """
malformed — not a two-component atom.
"""
# END
`

// TestBuildDivergenceMap pins the seam that keeps the comparator and the
// registry apart (R2.4): cmd/ turns packages.toml into the per-atom view compare
// consumes, so `internal/overlay` never learns what TOML is and
// `internal/autoupdate` never learns what a comparison is.
//
// Four rules, all of them measured rather than hypothetical:
//
//   - keys are split with autoupdate.SplitPackageKey, so `:slot` and `@label`
//     suffixes land on the bare atom (a second, slot-blind copy of that split is
//     the bug the suffix invites);
//   - any patched entry marks the atom AND names itself — 90 of 321 atoms carry
//     several entries that may disagree, and failing toward "patched" can only
//     suppress a removal recommendation, never produce one (R2.2);
//   - a present entry that declares nothing is NOT the same as an absent one:
//     the first is "known, not patched", the second is unknown (R2.3);
//   - a malformed key is skipped, not fatal: one bad key must not blank the
//     whole map.
//
// _Requirements: R2.1, R2.2, R2.4, R2.5_
func TestBuildDivergenceMap(t *testing.T) {
	dir := writeLintRegistry(t, divergenceRegistry)

	m, err := buildDivergenceMap(dir)
	if err != nil {
		t.Fatalf("buildDivergenceMap returned %v, want nil", err)
	}

	t.Run("a slot-suffixed key lands on its bare atom and names the entry", func(t *testing.T) {
		d, ok := m["net-libs/webkit-gtk"]
		if !ok {
			t.Fatalf("no entry for the bare atom net-libs/webkit-gtk; map holds %v", keysOf(m))
		}
		if !d.Patched {
			t.Error("net-libs/webkit-gtk: Patched = false, want true")
		}
		if d.Entry != "net-libs/webkit-gtk:4.1" {
			t.Errorf("net-libs/webkit-gtk: Entry = %q, want the declaring registry key %q", d.Entry, "net-libs/webkit-gtk:4.1")
		}
		if d.Reason != "our GTK4 backport" {
			t.Errorf("net-libs/webkit-gtk: Reason = %q, want the declared text", d.Reason)
		}
		if _, ok := m["net-libs/webkit-gtk:4.1"]; ok {
			t.Error("the slot-suffixed key survived into the map; compare keys by bare atom")
		}
	})

	t.Run("one patched entry among several marks the atom and names itself", func(t *testing.T) {
		// dev-lang/rust:beta declares nothing and sorts FIRST; the declaration
		// comes from :nightly. Asserting only the boolean would miss the half of
		// R2.2 that matters when the maintainer asks "which entry says so?".
		d, ok := m["dev-lang/rust"]
		if !ok {
			t.Fatalf("no entry for dev-lang/rust; map holds %v", keysOf(m))
		}
		if !d.Patched {
			t.Error("dev-lang/rust: Patched = false; any patched entry marks the atom")
		}
		if d.Entry != "dev-lang/rust:nightly" {
			t.Errorf("dev-lang/rust: Entry = %q, want %q — the entry that actually declared it", d.Entry, "dev-lang/rust:nightly")
		}
		if d.Reason != "our bootstrap patch" {
			t.Errorf("dev-lang/rust: Reason = %q, want %q", d.Reason, "our bootstrap patch")
		}
		if _, ok := m["dev-lang/rust:beta"]; ok {
			t.Error("a slot-suffixed key survived into the map")
		}
	})

	t.Run("several declaring entries resolve deterministically", func(t *testing.T) {
		// Both gstreamer entries declare. Sorted key order decides, so the report
		// says the same thing on every run rather than whatever map iteration
		// happened to yield.
		d := m["media-libs/gstreamer"]
		if !d.Patched {
			t.Fatal("media-libs/gstreamer: Patched = false, want true")
		}
		if d.Entry != "media-libs/gstreamer@dev" {
			t.Errorf("media-libs/gstreamer: Entry = %q, want %q (first declaring key in sorted order)", d.Entry, "media-libs/gstreamer@dev")
		}
		if d.Reason != "keeps the extra plugin directory" {
			t.Errorf("media-libs/gstreamer: Reason = %q, want the first declaring entry's text", d.Reason)
		}
	})

	t.Run("a silent entry is present and not patched, which is not unknown", func(t *testing.T) {
		d, ok := m["app-editors/zed"]
		if !ok {
			t.Fatalf("app-editors/zed absent from the map; a registered package that declares nothing is KNOWN not to be patched")
		}
		if d.Patched {
			t.Error("app-editors/zed: Patched = true, want false")
		}
		if d.Entry != "" || d.Reason != "" {
			t.Errorf("app-editors/zed: Entry = %q, Reason = %q; nothing declared it", d.Entry, d.Reason)
		}
	})

	t.Run("a malformed key is skipped and the rest of the map survives", func(t *testing.T) {
		if _, ok := m["justapackage"]; ok {
			t.Error("the malformed key justapackage reached the map")
		}
		want := []string{"net-libs/webkit-gtk", "media-libs/gstreamer", "dev-lang/rust", "app-editors/zed"}
		if len(m) != len(want) {
			t.Errorf("map holds %d atoms (%v), want %d (%v)", len(m), keysOf(m), len(want), want)
		}
		for _, atom := range want {
			if _, ok := m[atom]; !ok {
				t.Errorf("%s missing: one bad key must not blank the map", atom)
			}
		}
	})

	t.Run("an unreadable registry degrades to everything unknown", func(t *testing.T) {
		// No .autoupdate/packages.toml at all. The command warns once and compares
		// with a nil map; refusing to run would make the registry a hard
		// dependency of a command that never had one (R2.5).
		empty, err := buildDivergenceMap(t.TempDir())
		if err == nil {
			t.Error("buildDivergenceMap on a missing registry returned nil error; the caller has nothing to warn about")
		}
		if empty != nil {
			t.Errorf("buildDivergenceMap on a missing registry returned %v, want a nil map", empty)
		}
		if d, ok := empty["app-editors/zed"]; ok || d.Patched {
			t.Error("a lookup in the degraded map reported something known")
		}
	})

	t.Run("the map is exactly what the comparator consumes", func(t *testing.T) {
		// The contract between the two halves of the story: what cmd/ builds is
		// what CompareOptions carries, without conversion.
		opts := overlay.CompareOptions{Divergence: m}
		if len(opts.Divergence) != len(m) {
			t.Errorf("CompareOptions.Divergence holds %d atoms, want %d", len(opts.Divergence), len(m))
		}
		if got := opts.Divergence["dev-lang/rust"]; got.Entry != "dev-lang/rust:nightly" {
			t.Errorf("through CompareOptions, dev-lang/rust names %q, want %q", got.Entry, "dev-lang/rust:nightly")
		}
	})
}

// keysOf returns the map's keys, for readable failure messages.
func keysOf(m map[string]overlay.Divergence) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
