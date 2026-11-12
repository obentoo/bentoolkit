package cooldown_test

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/obentoo/bentoolkit/internal/autoupdate/cooldown"
)

func enableMaskText(t *testing.T, root string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(root, cooldown.MaskRel))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func emptyBlock(days string) string {
	return "# BEGIN bentoo-cooldown format=1 days=" + days + "\n" + cooldownHeaderLineNL + "# END bentoo-cooldown\n"
}

func TestEnable_AppendsAnEmptyBlockAndKeepsEveryByte(t *testing.T) {
	// No final newline: the block must start on a line of its own.
	root := writeOverlayMask(t, saveBefore+saveAfter)
	outcome, prev, err := cooldown.Enable(root, 7)
	if err != nil || outcome != cooldown.EnableCreated || prev != 0 {
		t.Fatalf("Enable = %v, %d, %v; want EnableCreated, 0, nil", outcome, prev, err)
	}
	want := saveBefore + saveAfter + "\n\n" + emptyBlock("7")
	if got := enableMaskText(t, root); got != want {
		t.Errorf("package.mask =\n%q\nwant\n%q", got, want)
	}
	b, err := cooldown.Load(root)
	if err != nil || b == nil || b.Days != 7 || len(b.Entries) != 0 {
		t.Fatalf("Load after Enable = %+v, %v; want an empty days=7 block", b, err)
	}
	if _, err := os.Stat(filepath.Join(root, cooldown.UnmaskRel)); err != nil {
		t.Errorf("cooldown.unmask not derived: %v", err)
	}
}

func TestEnable_CreatesAMissingMaskFile(t *testing.T) {
	root := t.TempDir()
	if _, _, err := cooldown.Enable(root, 5); err != nil {
		t.Fatalf("Enable on an overlay without profiles/: %v", err)
	}
	if got := enableMaskText(t, root); got != emptyBlock("5") {
		t.Errorf("package.mask = %q, want only the block", got)
	}
}

func TestEnable_RetunesAndKeepsEntries(t *testing.T) {
	entry := "# cooldown since=2026-11-07T07:10:00Z origin=autoupdate\n=dev-libs/foo-1.0\n"
	root := writeOverlayMask(t, saveBefore+"# BEGIN bentoo-cooldown format=1 days=7\n"+cooldownHeaderLineNL+entry+"# END bentoo-cooldown\n")
	outcome, prev, err := cooldown.Enable(root, 14)
	if err != nil || outcome != cooldown.EnableRetuned || prev != 7 {
		t.Fatalf("Enable = %v, %d, %v; want EnableRetuned, 7, nil", outcome, prev, err)
	}
	got := enableMaskText(t, root)
	if !strings.HasPrefix(got, saveBefore) || !strings.Contains(got, "days=14\n") || !strings.Contains(got, entry) {
		t.Errorf("retuned package.mask lost bytes or the entry:\n%s", got)
	}
	if outcome, _, err := cooldown.Enable(root, 14); err != nil || outcome != cooldown.EnableUnchanged {
		t.Errorf("a second Enable(14) = %v, %v; want EnableUnchanged", outcome, err)
	}
}

func TestEnable_RefusesBadDaysAndAMalformedBlock(t *testing.T) {
	root := writeOverlayMask(t, saveBefore)
	for _, d := range []int{0, -1, cooldown.MaxDays + 1} {
		if _, _, err := cooldown.Enable(root, d); err == nil {
			t.Errorf("Enable(days=%d) succeeded", d)
		}
	}
	if got := enableMaskText(t, root); got != saveBefore {
		t.Errorf("a refused Enable wrote package.mask:\n%s", got)
	}
	bad := writeOverlayMask(t, "# BEGIN bentoo-cooldown format=1 days=7\n=dev-libs/foo-1.0\n")
	if _, _, err := cooldown.Enable(bad, 7); !errors.Is(err, cooldown.ErrMalformed) {
		t.Errorf("Enable on a malformed block = %v, want ErrMalformed", err)
	}
}
