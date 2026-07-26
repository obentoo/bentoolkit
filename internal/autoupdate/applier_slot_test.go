package autoupdate

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestCopyEbuildRefusesExistingDestination pins the guard that keeps a copy from
// truncating an ebuild the applier never authored. The scenario is the real
// net-libs/webkit-gtk overlay layout, where the revision suffix discriminates
// the slots: -r411 is SLOT 4.1 and the bare PV is SLOT 6. Bumping the 4.1 ebuild
// from 2.52.4-r411 towards 2.52.5 targets webkit-gtk-2.52.5.ebuild, which is the
// SLOT 6 ebuild — os.Create would truncate it before io.Copy ever read a byte.
func TestCopyEbuildRefusesExistingDestination(t *testing.T) {
	overlayDir := filepath.Join(t.TempDir(), "overlay")
	pkg := "net-libs/webkit-gtk"

	const slot41 = "# SLOT 4.1 ebuild\nEAPI=8\nSLOT=\"4.1/0\"\n"
	const slot6 = "# SLOT 6 ebuild\nEAPI=8\nSLOT=\"6/0\"\n"

	createTestEbuildFileWithContent(t, overlayDir, pkg, "2.52.4-r411", slot41)
	createTestEbuildFileWithContent(t, overlayDir, pkg, "2.52.5", slot6)

	applier, err := NewApplier(overlayDir, t.TempDir())
	if err != nil {
		t.Fatalf("NewApplier: %v", err)
	}

	err = applier.copyEbuild(pkg, "2.52.4-r411", "2.52.5")
	if !errors.Is(err, ErrEbuildExists) {
		t.Fatalf("copyEbuild over an existing ebuild: got %v, want %v", err, ErrEbuildExists)
	}

	// The guard is worthless if it fires after the truncation, so assert the
	// destination is byte-identical to what it held before the call.
	dst := filepath.Join(overlayDir, "net-libs", "webkit-gtk", "webkit-gtk-2.52.5.ebuild")
	got, err := os.ReadFile(dst)
	if err != nil {
		t.Fatalf("read destination: %v", err)
	}
	if string(got) != slot6 {
		t.Errorf("destination ebuild was modified:\ngot:  %q\nwant: %q", got, slot6)
	}
}

// newWebkitSlotApplier builds an applier over the two-slot webkit-gtk overlay
// with one pending entry for the given slot key, mirroring how a real run
// reaches Apply. revision is the slot's pinned -rN (0 for a bare PV).
func newWebkitSlotApplier(t *testing.T, slotKey string, revision int, newVersion string) (*Applier, string) {
	t.Helper()

	tmpDir := t.TempDir()
	overlayDir := filepath.Join(tmpDir, "overlay")
	configDir := filepath.Join(tmpDir, "config")
	pkg := "net-libs/webkit-gtk"

	createTestEbuildFileWithContent(t, overlayDir, pkg, "2.52.4-r411",
		"EAPI=8\nSLOT=\"4.1/0\" # soname version of libwebkit2gtk-4.1\n")
	createTestEbuildFileWithContent(t, overlayDir, pkg, "2.52.5",
		"EAPI=8\nSLOT=\"6/0\" # soname version of libwebkit2gtk-6.0\n")

	pending, err := NewPendingList(configDir)
	if err != nil {
		t.Fatalf("NewPendingList: %v", err)
	}
	if err := pending.Add(PendingUpdate{
		Package:        slotKey,
		CurrentVersion: "2.52.4-r411",
		NewVersion:     newVersion,
		Status:         StatusPending,
	}); err != nil {
		t.Fatalf("pending.Add: %v", err)
	}

	applier, err := NewApplier(overlayDir, configDir,
		WithApplierPendingList(pending),
		WithExecCommand(mockExecCommandSuccess),
		WithApplierPackagesConfig(&PackagesConfig{Packages: map[string]PackageConfig{
			slotKey: {URL: "https://example.com", Parser: "regex", Revision: revision},
		}}),
	)
	if err != nil {
		t.Fatalf("NewApplier: %v", err)
	}
	return applier, overlayDir
}

// TestApplySlottedPackageWritesSlotRevision is the end-to-end shape of the whole
// change: bumping SLOT 4.1 of net-libs/webkit-gtk must read the -r411 ebuild
// (not the higher-versioned SLOT 6 one), write 2.52.5-r410 (the slot's base
// revision, not a carried-over r411), and leave SLOT 6 alone.
func TestApplySlottedPackageWritesSlotRevision(t *testing.T) {
	const slotKey = "net-libs/webkit-gtk:4.1"
	applier, overlayDir := newWebkitSlotApplier(t, slotKey, 410, "2.52.5")

	result, err := applier.Apply(slotKey, false)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if !result.Success {
		t.Fatalf("Apply was not successful: %v (obsolete=%v %s)", result.Error, result.Obsolete, result.ObsoleteReason)
	}
	if result.OldVersion != "2.52.4-r411" {
		t.Errorf("OldVersion = %q, want %q (the SLOT 4.1 ebuild, not the higher SLOT 6 one)",
			result.OldVersion, "2.52.4-r411")
	}
	if result.NewVersion != "2.52.5-r410" {
		t.Errorf("NewVersion = %q, want %q", result.NewVersion, "2.52.5-r410")
	}

	pkgDir := filepath.Join(overlayDir, "net-libs", "webkit-gtk")

	// The new ebuild exists and carries the SLOT 4.1 body it was copied from.
	newEbuild, err := os.ReadFile(filepath.Join(pkgDir, "webkit-gtk-2.52.5-r410.ebuild"))
	if err != nil {
		t.Fatalf("read new ebuild: %v", err)
	}
	if !strings.Contains(string(newEbuild), `SLOT="4.1/0"`) {
		t.Errorf("new ebuild does not carry the SLOT 4.1 body:\n%s", newEbuild)
	}

	// And the SLOT 6 ebuild — the file the slot-blind naming would have written
	// over — is untouched.
	slot6, err := os.ReadFile(filepath.Join(pkgDir, "webkit-gtk-2.52.5.ebuild"))
	if err != nil {
		t.Fatalf("read SLOT 6 ebuild: %v", err)
	}
	if !strings.Contains(string(slot6), `SLOT="6/0"`) {
		t.Errorf("SLOT 6 ebuild was clobbered:\n%s", slot6)
	}
}

// TestApplySlottedPackageWithoutRevisionIsBlocked pins the safety net: an entry
// that declares a slot but forgets its `revision` aims straight at the other
// slot's filename. The apply must fail loudly and leave both ebuilds intact,
// rather than truncate one.
func TestApplySlottedPackageWithoutRevisionIsBlocked(t *testing.T) {
	const slotKey = "net-libs/webkit-gtk:4.1"
	applier, overlayDir := newWebkitSlotApplier(t, slotKey, 0, "2.52.5")

	result, err := applier.Apply(slotKey, false)
	if err == nil {
		t.Fatalf("Apply succeeded, want failure (result: %+v)", result)
	}
	if !errors.Is(err, ErrEbuildExists) {
		t.Fatalf("Apply error = %v, want %v", err, ErrEbuildExists)
	}

	pkgDir := filepath.Join(overlayDir, "net-libs", "webkit-gtk")
	slot6, err := os.ReadFile(filepath.Join(pkgDir, "webkit-gtk-2.52.5.ebuild"))
	if err != nil {
		t.Fatalf("read SLOT 6 ebuild: %v", err)
	}
	if !strings.Contains(string(slot6), `SLOT="6/0"`) {
		t.Errorf("SLOT 6 ebuild was clobbered despite the guard:\n%s", slot6)
	}
	if _, err := os.Stat(filepath.Join(pkgDir, "webkit-gtk-2.52.4-r411.ebuild")); err != nil {
		t.Errorf("SLOT 4.1 source ebuild went missing: %v", err)
	}
}

// TestPendingKeysSlotsIndependently pins that the two slots of one package hold
// two independent pending records instead of overwriting each other.
func TestPendingKeysSlotsIndependently(t *testing.T) {
	pending, err := NewPendingList(t.TempDir())
	if err != nil {
		t.Fatalf("NewPendingList: %v", err)
	}

	entries := []PendingUpdate{
		{Package: "net-libs/webkit-gtk:4.1", CurrentVersion: "2.52.4-r411", NewVersion: "2.52.5", Status: StatusPending},
		{Package: "net-libs/webkit-gtk:6", CurrentVersion: "2.52.5", NewVersion: "2.52.6", Status: StatusPending},
	}
	for _, e := range entries {
		if err := pending.Add(e); err != nil {
			t.Fatalf("pending.Add(%s): %v", e.Package, err)
		}
	}

	if got := pending.Len(); got != len(entries) {
		t.Fatalf("pending.Len() = %d, want %d (the slots overwrote each other)", got, len(entries))
	}
	for _, want := range entries {
		got, ok := pending.Get(want.Package)
		if !ok {
			t.Fatalf("pending.Get(%q): not found", want.Package)
		}
		if got.CurrentVersion != want.CurrentVersion || got.NewVersion != want.NewVersion {
			t.Errorf("pending[%q] = (%s -> %s), want (%s -> %s)", want.Package,
				got.CurrentVersion, got.NewVersion, want.CurrentVersion, want.NewVersion)
		}
	}
}

// TestCopyEbuildAllowsFreshDestination guards against the check above being so
// eager that it blocks the ordinary bump it must stay out of the way of.
func TestCopyEbuildAllowsFreshDestination(t *testing.T) {
	overlayDir := filepath.Join(t.TempDir(), "overlay")
	pkg := "app-misc/hello"

	createTestEbuildFile(t, overlayDir, pkg, "1.0.0")

	applier, err := NewApplier(overlayDir, t.TempDir())
	if err != nil {
		t.Fatalf("NewApplier: %v", err)
	}

	if err := applier.copyEbuild(pkg, "1.0.0", "1.1.0"); err != nil {
		t.Fatalf("copyEbuild to a fresh destination: %v", err)
	}

	dst := filepath.Join(overlayDir, "app-misc", "hello", "hello-1.1.0.ebuild")
	if _, err := os.Stat(dst); err != nil {
		t.Fatalf("destination ebuild not created: %v", err)
	}
}
