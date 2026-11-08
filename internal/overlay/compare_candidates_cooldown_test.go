package overlay

// Authored for bentoo story 003 (autoupdate cooldown), sub-task 4.3 —
// R1.1 (the block is a mask every reader honours).
//
// Data-contract check between the block's writer (cooldown.Save) and the
// compare reader (readPackageMask), which design.md says "must keep parsing
// the file with the block inside it": the atoms Save writes are read back as
// `=` masks of exactly their package and version.
//
// Hostile half first: a `-bin` package masked at the same version must not be
// returned for its prefix-named neighbour, and the neighbour's hand mask
// outside the block is still read.

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/obentoo/bentoolkit/internal/autoupdate/cooldown"
)

func TestCooldownContract_TheCompareReaderSeesBlockAtomsAsMasks(t *testing.T) {
	root := t.TempDir()
	for _, d := range []string{"profiles", ".autoupdate"} {
		if err := os.MkdirAll(filepath.Join(root, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	mask := filepath.Join(root, "profiles", "package.mask")
	initial := "# hand mask\n=sys-apps/foo-0.9\n\n" +
		"# BEGIN bentoo-cooldown format=1 days=7\n" +
		"# Managed by `bentoo overlay autoupdate`. Edit with `bentoo overlay cooldown`, never by hand.\n" +
		"# END bentoo-cooldown\n"
	if err := os.WriteFile(mask, []byte(initial), 0o644); err != nil {
		t.Fatal(err)
	}

	b, err := cooldown.Load(root)
	if err != nil || b == nil {
		t.Fatalf("cooldown.Load = (%v, %v), want the empty block", b, err)
	}
	since := time.Date(2026, 11, 7, 7, 10, 0, 0, time.UTC)
	b.Add(cooldown.Entry{Package: "sys-apps/foo", Version: "1.2.3-r1", Since: since, Origin: "autoupdate"})
	b.Add(cooldown.Entry{Package: "sys-apps/foo-bin", Version: "1.2.3-r1", Since: since, Origin: "autoupdate"})
	if err := b.Save(root); err != nil {
		t.Fatalf("cooldown.Save: %v", err)
	}

	versions := map[string]bool{}
	for _, m := range readPackageMask(mask, "sys-apps/foo") {
		if m.op != "=" {
			t.Errorf("a mask for sys-apps/foo was read with op %q, want =", m.op)
		}
		versions[m.version] = true
	}
	if !versions["1.2.3-r1"] {
		t.Errorf("readPackageMask does not see the block's =sys-apps/foo-1.2.3-r1: %v", versions)
	}
	if !versions["0.9"] {
		t.Errorf("the hand mask outside the block was lost: %v", versions)
	}
	if len(versions) != 2 {
		t.Errorf("readPackageMask(sys-apps/foo) = %v, want exactly 0.9 and 1.2.3-r1 (the -bin atom is another package)", versions)
	}
}
