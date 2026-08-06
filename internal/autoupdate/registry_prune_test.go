package autoupdate

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// This file pins R5.2–R5.4: deleting a package's registry records without
// disturbing a byte of any other record.
//
// The stakes are not abstract. packages.toml is 6224 lines of hand-written
// documentation — more than half of it prose explaining why each record looks
// the way it does, some of it reconstructed by hand after a bulk regeneration
// lost it once already. A rewrite that drops or reflows a record it was not
// asked to touch is not recoverable from the diff alone, because the diff is
// where the damage would be hiding.
//
// Three properties, each with a test that fails loudly if it is relaxed:
//
//   - EVERY entry of an atom goes, not the first (R5.2). 90 of the registry's
//     321 atoms carry two or more entries — one per slot, one per release
//     channel. Deleting `net-libs/webkit-gtk` while leaving
//     `net-libs/webkit-gtk:4.1` behind orphans an entry whose package directory
//     no longer exists, and the next `--check` disables it silently.
//   - Every OTHER record survives byte-identical (R5.4), including its `# END`
//     marker, its blank-line spacing and its comments block.
//   - A call matching nothing leaves the file UNTOUCHED — not rewritten with the
//     same bytes. The overlay auto-commits and pushes, so an mtime change on
//     this file becomes a commit with no content.
//
// The result is re-parsed before it is accepted, because "the file still parses"
// and "the file still holds what it held" are different claims and only the
// second one matters here.

// pruneRegistryFixture is a registry in the real file's shape: a preamble that
// belongs to no record, records separated by a blank line, `# END` markers, a
// triple-quoted comments block, and one atom carrying three entries under
// different suffixes.
const pruneRegistryFixture = `# Bentoo Autoupdate Package Configuration
# Each section defines how to check upstream versions for a package.
#
# NOTE: BEGIN/END markers below are comments only. Do NOT use bare [BEGIN]/[END]
# table syntax here — TOML would parse them as phantom packages.

["app-editors/zed"]
url = "https://api.github.com/repos/zed-industries/zed/releases"
parser = "json"
path = "0.tag_name"
comments = """
Pre-releases are published constantly; the releases endpoint returns them in
order, so path 0 is whatever shipped last.
"""
# END

["net-libs/webkit-gtk"]
url = "https://www.webkitgtk.org/releases/"
parser = "regex"
pattern = 'webkitgtk-(2\.52\.[0-9]+)\.tar\.xz'
select = "max"
# END

["net-libs/webkit-gtk:4.1"]
url = "https://www.webkitgtk.org/releases/"
parser = "regex"
pattern = 'webkitgtk-(2\.52\.[0-9]+)\.tar\.xz'
select = "max"
revision = 410
comments = """
revision is the SLOT's BASE revision, not whatever the current ebuild carries.
Both slots live in ONE directory and share the PV series, so a single entry
cannot express them.
"""
# END

["net-libs/webkit-gtk@stable"]
url = "https://www.webkitgtk.org/releases/"
parser = "regex"
pattern = 'webkitgtk-(2\.52\.[0-9]+)\.tar\.xz'
select = "max"
# END

["sys-apps/pnpm"]
url = "https://registry.npmjs.org/pnpm"
parser = "json"
path = "dist-tags.latest"
enabled = false
# END
`

const (
	pruneAtomWebkit = "net-libs/webkit-gtk"
	pruneAtomZed    = "app-editors/zed"
	pruneAtomPnpm   = "sys-apps/pnpm"
)

// writePruneRegistry lays the fixture out at <overlay>/.autoupdate/packages.toml
// with the mode given, and returns the overlay root.
func writePruneRegistry(t *testing.T, body string, mode os.FileMode) string {
	t.Helper()
	overlay := t.TempDir()
	dir := filepath.Join(overlay, ".autoupdate")
	if err := os.MkdirAll(dir, 0o750); err != nil {
		t.Fatalf("mkdir %s: %v", dir, err)
	}
	path := filepath.Join(dir, "packages.toml")
	if err := os.WriteFile(path, []byte(body), mode); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
	// os.WriteFile applies the process umask to a file it creates, so the mode
	// is set explicitly afterwards — otherwise the "mode is preserved" test would
	// be comparing whatever umask allowed against itself.
	if err := os.Chmod(path, mode); err != nil {
		t.Fatalf("chmod %s: %v", path, err)
	}
	return overlay
}

// pruneRegistryPath is where the registry lives under an overlay root.
func pruneRegistryPath(overlay string) string {
	return filepath.Join(overlay, ".autoupdate", "packages.toml")
}

// readPruneRegistry returns the registry's current bytes as a string.
func readPruneRegistry(t *testing.T, overlay string) string {
	t.Helper()
	data, err := os.ReadFile(pruneRegistryPath(overlay))
	if err != nil {
		t.Fatalf("read registry: %v", err)
	}
	return string(data)
}

// recordTextFor returns the exact text of one record — from its `["key"]` header
// through the line before the next header (or end of file) — so a sibling can be
// compared byte for byte rather than merely "still present".
func recordTextFor(t *testing.T, content, key string) string {
	t.Helper()
	header := `["` + key + `"]`
	start := strings.Index(content, header)
	if start < 0 {
		t.Fatalf("record %q not found in:\n%s", key, content)
	}
	rest := content[start+len(header):]
	if next := strings.Index(rest, "\n[\""); next >= 0 {
		return header + rest[:next]
	}
	return header + rest
}

// TestRemovePackagesFromConfigDropsEveryEntryOfAnAtom pins R5.2.
//
// One atom, three entries: the bare key, a `:slot` entry and an `@label` entry.
// All three name the same package directory, so a removal that deletes that
// directory must delete all three — an entry left behind promises an endpoint
// for a package that no longer exists, and the next `--check` disables it
// without saying why.
//
// The sub-test passing the `:4.1` key proves the match is by ATOM, not by key:
// a caller holding any one of the three forms removes the whole atom, so there
// is no way to half-delete a package by picking the wrong spelling.
//
// _Requirements: R5.2_
func TestRemovePackagesFromConfigDropsEveryEntryOfAnAtom(t *testing.T) {
	for _, requested := range []string{
		pruneAtomWebkit,
		pruneAtomWebkit + ":4.1",
		pruneAtomWebkit + "@stable",
	} {
		t.Run("requested as "+requested, func(t *testing.T) {
			overlay := writePruneRegistry(t, pruneRegistryFixture, 0o644)

			if err := RemovePackagesFromConfig(overlay, []string{requested}); err != nil {
				t.Fatalf("RemovePackagesFromConfig returned %v, want nil", err)
			}

			got := readPruneRegistry(t, overlay)
			for _, gone := range []string{
				`["` + pruneAtomWebkit + `"]`,
				`["` + pruneAtomWebkit + `:4.1"]`,
				`["` + pruneAtomWebkit + `@stable"]`,
			} {
				if strings.Contains(got, gone) {
					t.Errorf("%s survives the removal; an entry whose package directory is gone is the orphan the next --check disables silently", gone)
				}
			}
			// The comments block belonged to the :4.1 entry and must go with it,
			// rather than being left stranded between two surviving records.
			if strings.Contains(got, "revision is the SLOT's BASE revision") {
				t.Error("the removed record's comments block survives; dropping a record means dropping its documentation with it")
			}
			for _, kept := range []string{pruneAtomZed, pruneAtomPnpm} {
				if !strings.Contains(got, `["`+kept+`"]`) {
					t.Errorf("%s was removed too; only the requested atom may go", kept)
				}
			}
		})
	}
}

// TestRemovePackagesFromConfigLeavesSiblingRecordsByteIdentical pins R5.4, and
// it compares TEXT rather than parsed values on purpose.
//
// A round-trip through a TOML encoder would satisfy "the same records are still
// configured" while silently dropping every comment in the file — which is why
// DisablePackagesInConfig edits raw text and this must too. More than half of
// packages.toml is prose that no decoder preserves.
//
// _Requirements: R5.4_
func TestRemovePackagesFromConfigLeavesSiblingRecordsByteIdentical(t *testing.T) {
	overlay := writePruneRegistry(t, pruneRegistryFixture, 0o644)

	before := readPruneRegistry(t, overlay)
	wantZed := recordTextFor(t, before, pruneAtomZed)
	wantPnpm := recordTextFor(t, before, pruneAtomPnpm)

	if err := RemovePackagesFromConfig(overlay, []string{pruneAtomWebkit}); err != nil {
		t.Fatalf("RemovePackagesFromConfig returned %v, want nil", err)
	}

	after := readPruneRegistry(t, overlay)
	if got := recordTextFor(t, after, pruneAtomZed); got != wantZed {
		t.Errorf("the app-editors/zed record changed.\n--- want ---\n%q\n--- got ---\n%q", wantZed, got)
	}
	if got := recordTextFor(t, after, pruneAtomPnpm); got != wantPnpm {
		t.Errorf("the sys-apps/pnpm record changed.\n--- want ---\n%q\n--- got ---\n%q", wantPnpm, got)
	}
}

// TestRemovePackagesFromConfigPreservesPreambleAndEndMarkers pins the two parts
// of the file that belong to no record and are therefore the easiest to lose.
//
// The preamble documents the record model itself; it was destroyed once by a
// bulk regeneration and reconstructed by hand. The `# END` markers and the blank
// line before the next header live in each record's tail, which is why dropping
// a record is dropping its block rather than editing lines around it.
//
// _Requirements: R5.4_
func TestRemovePackagesFromConfigPreservesPreambleAndEndMarkers(t *testing.T) {
	overlay := writePruneRegistry(t, pruneRegistryFixture, 0o644)

	if err := RemovePackagesFromConfig(overlay, []string{pruneAtomWebkit}); err != nil {
		t.Fatalf("RemovePackagesFromConfig returned %v, want nil", err)
	}

	got := readPruneRegistry(t, overlay)

	preamble := strings.Index(pruneRegistryFixture, `["`)
	if preamble < 0 {
		t.Fatal("fixture has no record header (fixture check)")
	}
	if want := pruneRegistryFixture[:preamble]; !strings.HasPrefix(got, want) {
		t.Errorf("the preamble changed.\n--- want prefix ---\n%q\n--- got ---\n%q", want, got[:min(len(got), len(want)+80)])
	}

	if n := strings.Count(got, "# END"); n != 2 {
		t.Errorf("the file holds %d `# END` markers, want 2 (one per surviving record); the marker lives in a record's tail and is lost by editing around a record instead of dropping its block", n)
	}
	if strings.Contains(got, "\n\n\n") {
		t.Errorf("the removal left a doubled blank line, so the file's spacing no longer matches the record model:\n%s", got)
	}
}

// TestRemovePackagesFromConfigNoMatchLeavesFileUntouched pins the property that
// costs nothing to get right and is expensive to get wrong.
//
// The overlay auto-commits and pushes within minutes. A rewrite that produces
// identical bytes still changes the mtime, and a no-op commit on a file this
// size is noise in the one history someone would read to understand a bad
// removal. So: no match, no write at all — not a write that happens to be
// harmless.
//
// _Requirements: R5.4, R6.3_
func TestRemovePackagesFromConfigNoMatchLeavesFileUntouched(t *testing.T) {
	overlay := writePruneRegistry(t, pruneRegistryFixture, 0o644)
	path := pruneRegistryPath(overlay)

	before, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat before: %v", err)
	}

	if err := RemovePackagesFromConfig(overlay, []string{"media-libs/absent"}); err != nil {
		t.Fatalf("RemovePackagesFromConfig returned %v, want nil for an atom with no record", err)
	}

	after, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat after: %v", err)
	}
	if !after.ModTime().Equal(before.ModTime()) {
		t.Errorf("the file was rewritten (mtime %v -> %v) though nothing matched; the overlay auto-commits, so this is an empty commit", before.ModTime(), after.ModTime())
	}
	if got := readPruneRegistry(t, overlay); got != pruneRegistryFixture {
		t.Errorf("the file's bytes changed though nothing matched.\n--- got ---\n%s", got)
	}

	t.Run("an empty atom list is also a no-op", func(t *testing.T) {
		if err := RemovePackagesFromConfig(overlay, nil); err != nil {
			t.Fatalf("RemovePackagesFromConfig(nil) returned %v, want nil", err)
		}
		after, err := os.Stat(path)
		if err != nil {
			t.Fatalf("stat after: %v", err)
		}
		if !after.ModTime().Equal(before.ModTime()) {
			t.Error("an empty atom list rewrote the file")
		}
	})
}

// TestRemovePackagesFromConfigResultStillParses pins the check that runs BEFORE
// the rename, not after the damage.
//
// Re-parsing the candidate text and refusing the write when it fails, or when an
// atom other than the requested ones has vanished, is what makes a bug here a
// returned error instead of a published deletion. The registry is the file that
// says which packages the overlay maintains; losing a record from it is losing
// the knowledge that the package was ever being tracked.
//
// _Requirements: R5.2, R5.4_
func TestRemovePackagesFromConfigResultStillParses(t *testing.T) {
	overlay := writePruneRegistry(t, pruneRegistryFixture, 0o644)

	if err := RemovePackagesFromConfig(overlay, []string{pruneAtomWebkit}); err != nil {
		t.Fatalf("RemovePackagesFromConfig returned %v, want nil", err)
	}

	data, err := os.ReadFile(pruneRegistryPath(overlay))
	if err != nil {
		t.Fatalf("read registry: %v", err)
	}
	cfg, err := decodePackagesConfig(data)
	if err != nil {
		t.Fatalf("the surviving registry does not parse: %v\n--- file ---\n%s", err, data)
	}

	for _, want := range []string{pruneAtomZed, pruneAtomPnpm} {
		if _, ok := cfg.Packages[want]; !ok {
			t.Errorf("%q is missing from the parsed registry; only the requested atom may go", want)
		}
	}
	for key := range cfg.Packages {
		if category, name, ok := SplitPackageKey(key); ok && category+"/"+name == pruneAtomWebkit {
			t.Errorf("%q still parses out of the registry after its atom was removed", key)
		}
	}
	// pnpm carries `enabled = false`; if a rewrite dropped or reordered fields the
	// record would still parse while meaning something else.
	if pnpm, ok := cfg.Packages[pruneAtomPnpm]; ok && pnpm.Enabled == nil {
		t.Error("sys-apps/pnpm lost its `enabled = false`; a record that still parses is not the same claim as a record that still says what it said")
	}
}

// TestRemovePackagesFromConfigPreservesFileMode pins the last thing an atomic
// write forgets.
//
// temp file + rename replaces the inode, so the new file carries whatever mode
// it was created with — typically 0600 under the process umask — unless the mode
// is copied across deliberately. A registry that silently becomes unreadable to
// the group is a permission bug discovered by whatever runs next.
//
// _Requirements: R5.4_
func TestRemovePackagesFromConfigPreservesFileMode(t *testing.T) {
	const mode os.FileMode = 0o640
	overlay := writePruneRegistry(t, pruneRegistryFixture, mode)
	path := pruneRegistryPath(overlay)

	before, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat before: %v", err)
	}
	if before.Mode().Perm() != mode {
		t.Fatalf("fixture mode is %v, want %v (fixture check)", before.Mode().Perm(), mode)
	}

	if err := RemovePackagesFromConfig(overlay, []string{pruneAtomWebkit}); err != nil {
		t.Fatalf("RemovePackagesFromConfig returned %v, want nil", err)
	}

	after, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat after: %v", err)
	}
	if after.Mode().Perm() != mode {
		t.Errorf("mode = %v, want %v; a temp-file-and-rename write replaces the inode and carries the umask unless the mode is copied over", after.Mode().Perm(), mode)
	}
}
