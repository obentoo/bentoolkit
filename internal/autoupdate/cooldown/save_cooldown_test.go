package cooldown_test

// Authored for bentoo story 003 (autoupdate cooldown), sub-task 1.4 —
// R1.2, R1.3 (at the writer), R1.6, R3.9, Q11.
//
// Contract: design.md "(b *Block) Save(overlayRoot) error" — rewrites only
// the lines between the markers, plus .autoupdate/cooldown.unmask (the same
// atoms, package.unmask syntax, one per line under a header comment), both
// through fileutil.WriteFileAtomic; bytes outside the block preserved
// exactly. Block format: two lines per entry, bare atoms, sorted.
//
// Q11's "written through fileutil.WriteFileAtomic" is asserted by what that
// function does and a plain write does not: the path is REPLACED (a new
// inode), and no temporary file is left beside it.
//
// Hostile halves first: bytes outside the block that a line-based rewrite
// would normalise (trailing blanks, a tab, no final newline), an EMPTY block
// after the last lift (it must stay — removing it would switch cooldown off),
// and a prefix-named package whose atom must not be dropped with its
// neighbour's.

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/obentoo/bentoolkit/internal/autoupdate/cooldown"
)

// saveBefore and saveAfter are hand-written bytes a line-based rewrite would
// alter: trailing blanks, a tab, a blank line, and NO final newline.
const saveBefore = "# hand masks   \n=media-libs/sdl3-3.4.20\t# tab-separated note\n\n"

const saveAfter = "\n\n# trailing hand mask, no final newline\n>=dev-lang/foo-2"

const saveEmptyBlock = "# BEGIN bentoo-cooldown format=1 days=7\n" + cooldownHeaderLineNL + "# END bentoo-cooldown\n"

const cooldownHeaderLineNL = "# Managed by `bentoo overlay autoupdate`. Edit with `bentoo overlay cooldown`, never by hand.\n"

func loadOrFatal(t *testing.T, root string) *cooldown.Block {
	t.Helper()
	b, err := cooldown.Load(root)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if b == nil {
		t.Fatal("Load returned nil for an overlay carrying a block")
	}
	return b
}

// unmaskAtoms returns the non-comment, non-blank lines of the unmask list.
func unmaskAtoms(t *testing.T, root string) []string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(root, ".autoupdate", "cooldown.unmask"))
	if err != nil {
		t.Fatalf("reading .autoupdate/cooldown.unmask: %v", err)
	}
	var atoms []string
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		atoms = append(atoms, line)
	}
	sort.Strings(atoms)
	return atoms
}

// blockAtomLines returns the atom lines between BEGIN and END, in file order.
func blockAtomLines(t *testing.T, content string) []string {
	t.Helper()
	var atoms []string
	in := false
	for _, line := range strings.Split(content, "\n") {
		switch {
		case strings.HasPrefix(line, "# BEGIN bentoo-cooldown"):
			in = true
		case strings.HasPrefix(line, "# END bentoo-cooldown"):
			in = false
		case in && strings.HasPrefix(line, "="):
			atoms = append(atoms, line)
		}
	}
	return atoms
}

func inode(t *testing.T, path string) uint64 {
	t.Helper()
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		t.Skip("no inode on this platform")
	}
	return st.Ino
}

// TestCooldownSave_RewritesOnlyTheBlockAndKeepsEveryOtherByte: R1.6. The
// prefix and suffix come back byte for byte, and the new entry is read back
// with every field.
func TestCooldownSave_RewritesOnlyTheBlockAndKeepsEveryOtherByte(t *testing.T) {
	root := writeOverlayMask(t, saveBefore+saveEmptyBlock+saveAfter)
	b := loadOrFatal(t, root)

	since := time.Date(2026, 11, 7, 7, 10, 0, 0, time.UTC)
	b.Add(cooldown.Entry{Package: "sys-apps/ai-jail", Version: "3.2.0", Since: since,
		Origin: "autoupdate", Group: "sys-apps/ai-jail,sys-apps/ai-jail-bin"})
	if err := b.Save(root); err != nil {
		t.Fatalf("Save: %v", err)
	}

	got := string(readMask(t, root))
	if !strings.HasPrefix(got, saveBefore) {
		t.Errorf("the bytes before the block changed:\n got %q\nwant prefix %q", got[:min(len(got), len(saveBefore)+20)], saveBefore)
	}
	if !strings.HasSuffix(got, saveAfter) {
		t.Errorf("the bytes after the block changed:\n got tail %q\nwant suffix %q", got[max(0, len(got)-len(saveAfter)-20):], saveAfter)
	}
	if atoms := blockAtomLines(t, got); len(atoms) != 1 || atoms[0] != "=sys-apps/ai-jail-3.2.0" {
		t.Errorf("block atoms = %v, want [=sys-apps/ai-jail-3.2.0] (bare, no ::repo)", atoms)
	}

	back := loadOrFatal(t, root)
	e, ok := findEntry(back, "sys-apps/ai-jail", "3.2.0")
	if !ok {
		t.Fatalf("the saved entry did not read back: %+v", back.Entries)
	}
	if !e.Since.Equal(since) || e.Origin != "autoupdate" || e.Group != "sys-apps/ai-jail,sys-apps/ai-jail-bin" {
		t.Errorf("read back %+v, want since=%s origin=autoupdate group=sys-apps/ai-jail,sys-apps/ai-jail-bin", e, since)
	}
	if back.Days != 7 {
		t.Errorf("Days = %d after a round trip, want 7", back.Days)
	}
}

// TestCooldownSave_RendersEntriesSortedTwoLinesEach: design.md Block format.
// Every atom is preceded by its own `# cooldown` line, atoms are sorted by
// package then Gentoo version, and the unmask list carries the same atoms.
func TestCooldownSave_RendersEntriesSortedTwoLinesEach(t *testing.T) {
	root := writeOverlayMask(t, saveBefore+saveEmptyBlock+saveAfter)
	b := loadOrFatal(t, root)
	since := time.Date(2026, 11, 7, 0, 0, 0, 0, time.UTC)
	for _, a := range []struct{ pkg, ver string }{
		{"sys-apps/foo", "1.10"}, {"sys-apps/foo-bin", "1.0"}, {"app-misc/bar", "2.0"}, {"sys-apps/foo", "1.9"},
	} {
		b.Add(cooldown.Entry{Package: a.pkg, Version: a.ver, Since: since, Origin: "manual"})
	}
	if err := b.Save(root); err != nil {
		t.Fatalf("Save: %v", err)
	}

	content := string(readMask(t, root))
	want := []string{"=app-misc/bar-2.0", "=sys-apps/foo-1.9", "=sys-apps/foo-1.10", "=sys-apps/foo-bin-1.0"}
	got := blockAtomLines(t, content)
	if strings.Join(got, " ") != strings.Join(want, " ") {
		t.Errorf("block atoms in file order = %v, want %v", got, want)
	}
	lines := strings.Split(content, "\n")
	for i, line := range lines {
		if strings.HasPrefix(line, "=") && i > 0 && blockAtomContains(want, line) {
			if !strings.HasPrefix(lines[i-1], "# cooldown ") {
				t.Errorf("atom %q is not preceded by its `# cooldown` line (got %q)", line, lines[i-1])
			}
		}
	}

	wantUnmask := append([]string(nil), want...)
	sort.Strings(wantUnmask)
	if gotUnmask := unmaskAtoms(t, root); strings.Join(gotUnmask, " ") != strings.Join(wantUnmask, " ") {
		t.Errorf("cooldown.unmask atoms = %v, want %v (R1.2: the same atoms)", gotUnmask, wantUnmask)
	}
}

func blockAtomContains(atoms []string, line string) bool {
	for _, a := range atoms {
		if a == line {
			return true
		}
	}
	return false
}

// TestCooldownSave_ALiftRemovesTheAtomFromBothFiles: R3.9. Saving the block
// Sweep leaves drops the lifted atom from package.mask AND the unmask list;
// the prefix-named neighbour with the same version stays in both.
func TestCooldownSave_ALiftRemovesTheAtomFromBothFiles(t *testing.T) {
	root := writeOverlayMask(t, saveBefore+saveEmptyBlock+saveAfter)
	b := loadOrFatal(t, root)
	recent := time.Now().UTC().Add(-time.Hour).Truncate(time.Second)
	b.Add(cooldown.Entry{Package: "sys-apps/foo", Version: "1.0", Since: recent, Origin: "autoupdate"})
	b.Add(cooldown.Entry{Package: "sys-apps/foo-bin", Version: "1.0", Since: recent, Origin: "autoupdate"})
	if err := b.Save(root); err != nil {
		t.Fatalf("Save: %v", err)
	}

	b = loadOrFatal(t, root)
	_, rest := cooldown.Sweep(b, time.Now().UTC(), nil,
		[]cooldown.Confirm{confirmOf("sys-apps/foo", "1.0")}, everyEbuildExists)
	if rest == nil {
		t.Fatal("Sweep returned a nil remaining block")
	}
	if err := rest.Save(root); err != nil {
		t.Fatalf("Save after the lift: %v", err)
	}

	if got := blockAtomLines(t, string(readMask(t, root))); strings.Join(got, " ") != "=sys-apps/foo-bin-1.0" {
		t.Errorf("block atoms after lifting =sys-apps/foo-1.0 = %v, want [=sys-apps/foo-bin-1.0]", got)
	}
	if got := unmaskAtoms(t, root); strings.Join(got, " ") != "=sys-apps/foo-bin-1.0" {
		t.Errorf("cooldown.unmask after the lift = %v, want [=sys-apps/foo-bin-1.0]", got)
	}
}

// TestCooldownSave_AnEmptyBlockStaysAndKeepsCooldownOn: after the last lift
// the markers remain, so the next Load still says "enabled", and the unmask
// list holds no atom.
func TestCooldownSave_AnEmptyBlockStaysAndKeepsCooldownOn(t *testing.T) {
	root := writeOverlayMask(t, saveBefore+saveEmptyBlock+saveAfter)
	b := loadOrFatal(t, root)
	b.Add(cooldown.Entry{Package: "sys-apps/foo", Version: "1.0", Since: time.Now().UTC().Truncate(time.Second), Origin: "manual"})
	if err := b.Save(root); err != nil {
		t.Fatalf("Save: %v", err)
	}
	b = loadOrFatal(t, root)
	b.Entries = nil
	if err := b.Save(root); err != nil {
		t.Fatalf("Save of an empty block: %v", err)
	}

	back := loadOrFatal(t, root)
	if len(back.Entries) != 0 || back.Days != 7 {
		t.Errorf("empty block read back as Days %d with %d entries, want Days 7 and none", back.Days, len(back.Entries))
	}
	if got := unmaskAtoms(t, root); len(got) != 0 {
		t.Errorf("cooldown.unmask still lists %v after the last lift", got)
	}
	if got := string(readMask(t, root)); !strings.HasPrefix(got, saveBefore) || !strings.HasSuffix(got, saveAfter) {
		t.Errorf("bytes outside the block changed after emptying it: %q", got)
	}
}

// TestCooldownSave_ReplacesBothFilesAtomically: Q11. A replace-by-rename
// gives each path a new inode and leaves no temporary file in either
// directory; an in-place write keeps the inode.
func TestCooldownSave_ReplacesBothFilesAtomically(t *testing.T) {
	root := writeOverlayMask(t, saveBefore+saveEmptyBlock+saveAfter)
	unmask := filepath.Join(root, ".autoupdate", "cooldown.unmask")
	if err := os.WriteFile(unmask, []byte("# placeholder\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	mask := filepath.Join(root, "profiles", "package.mask")
	maskIno, unmaskIno := inode(t, mask), inode(t, unmask)

	b := loadOrFatal(t, root)
	b.Add(cooldown.Entry{Package: "sys-apps/foo", Version: "1.0", Since: time.Now().UTC().Truncate(time.Second), Origin: "manual"})
	if err := b.Save(root); err != nil {
		t.Fatalf("Save: %v", err)
	}

	if inode(t, mask) == maskIno {
		t.Error("profiles/package.mask kept its inode: it was written in place, not through WriteFileAtomic")
	}
	if inode(t, unmask) == unmaskIno {
		t.Error(".autoupdate/cooldown.unmask kept its inode: it was written in place, not through WriteFileAtomic")
	}
	for dir, only := range map[string]string{
		filepath.Join(root, "profiles"):    "package.mask",
		filepath.Join(root, ".autoupdate"): "cooldown.unmask",
	} {
		entries, err := os.ReadDir(dir)
		if err != nil {
			t.Fatal(err)
		}
		for _, e := range entries {
			if e.Name() != only {
				t.Errorf("%s holds %q after Save; a temporary file was left behind", dir, e.Name())
			}
		}
	}
}

// TestCooldownSave_AnUnwritableUnmaskListIsAnErrorNamingIt: R1.3 at the
// writer. A directory squatting on the unmask path cannot be replaced by a
// file (this holds for root too, unlike a permission bit), so Save fails and
// names the path.
func TestCooldownSave_AnUnwritableUnmaskListIsAnErrorNamingIt(t *testing.T) {
	root := writeOverlayMask(t, saveBefore+saveEmptyBlock+saveAfter)
	if err := os.MkdirAll(filepath.Join(root, ".autoupdate", "cooldown.unmask", "squatter"), 0o755); err != nil {
		t.Fatal(err)
	}
	b := loadOrFatal(t, root)
	b.Add(cooldown.Entry{Package: "sys-apps/foo", Version: "1.0", Since: time.Now().UTC().Truncate(time.Second), Origin: "manual"})

	err := b.Save(root)
	if err == nil {
		t.Fatal("Save succeeded although .autoupdate/cooldown.unmask cannot be written")
	}
	if !strings.Contains(err.Error(), "cooldown.unmask") {
		t.Errorf("the error does not name the unmask list: %v", err)
	}
}
