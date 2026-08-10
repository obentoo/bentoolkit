package autoupdate

// Authored for story 033, sub-task 14.1 (hermetic half) — R3, R3.5, R4, R4.1,
// R5, R5.1.
//
// MERGE FRAGMENT — AND A CORRECTION TO THE PLAN'S TARGET FILE. tasks.md names
// `internal/autoupdate/validate/golden_test.go`, and this half CANNOT live
// there: the assertion is about the STAGED APPLY PATH, which means driving
// `autoupdate.Applier`, and package `validate` cannot import `autoupdate`
// because `autoupdate` already imports `validate` (the isolation probe). A test
// file in package validate that imported the applier would not compile.
//
// So this fragment targets `internal/autoupdate/applier_golden_test.go`
// (package autoupdate), which is the only side of the dependency edge that can
// see both. The STANDALONE-command half of the golden pair already lives in
// validate/golden_test.go and story 031 wrote it; nothing here duplicates it.
// The live half (14.1b) stays in validate/live_test.go, because it drives
// RunBuildGates directly and needs no applier.
//
// A GATE THAT DOES NOT REJECT A BUG WE ALREADY KNOW ABOUT IS NOT A GATE. This
// reproduces obentoo/bentoo#33 through the path the operator actually uses:
// upstream removed the `aalib` and `libcaca` build options at 1.29, the ebuild
// kept passing `-Daalib=` and `-Dlibcaca=`, `pkgdev manifest` proved every hash,
// and every check in the toolkit stayed green.
//
// The two versions sit in one package directory exactly as they do in the real
// overlay, and one run has to separate them: 1.29.2 produces error findings
// naming both options and IS NOT PROMOTED; 1.28.6 passes.
//
// The archives are built in-process with archive/tar. The real gst-plugins-good
// tarballs are ~6 MB each and are never committed.
//
// hashOverlayTree comes from applier_promote_test.go (sub-task 4.1);
// createTestEbuildFileWithContent and mockExecCommandSuccess from
// applier_test.go. None is redeclared.

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// The ebuild both versions share — unchanged across the bump, which IS the
// defect. Nobody edited it; upstream moved underneath it.
const goldenApplyEbuild = `# Copyright 2026 Gentoo Authors
EAPI=8
DESCRIPTION="Qt6 plugin for GStreamer"
SLOT="0"
KEYWORDS="~amd64"
emesonargs=(
	-Dqt6=enabled
	-Daalib=disabled
	-Dlibcaca=disabled
)
`

// writeGoldenArchive builds a .tar.gz holding one meson.options declaring the
// given options, and puts it in distdir under name.
func writeGoldenArchive(t *testing.T, distdir, name, prefix string, options []string) {
	t.Helper()
	path := filepath.Join(distdir, name)
	f, err := os.Create(path)
	if err != nil {
		t.Fatalf("creating %q: %v", path, err)
	}
	defer func() { _ = f.Close() }()

	gz := gzip.NewWriter(f)
	tw := tar.NewWriter(gz)

	var body strings.Builder
	for _, o := range options {
		body.WriteString("option('" + o + "')\n")
	}
	members := map[string]string{
		prefix + "/meson.build":   "project('gst-plugins-good')\n",
		prefix + "/meson.options": body.String(),
	}
	for member, content := range members {
		if err := tw.WriteHeader(&tar.Header{Name: member, Mode: 0o644, Size: int64(len(content))}); err != nil {
			t.Fatalf("writing header %q: %v", member, err)
		}
		if _, err := tw.Write([]byte(content)); err != nil {
			t.Fatalf("writing body %q: %v", member, err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatalf("closing tar: %v", err)
	}
	if err := gz.Close(); err != nil {
		t.Fatalf("closing gzip: %v", err)
	}
}

// goldenApplyFixture lays out the real shape: an overlay holding 1.28.6, a
// pending bump to 1.29.2, a distdir holding both upstream archives — 1.28.6
// still declares aalib and libcaca, 1.29.2 no longer does — and an applier whose
// staging root sits outside the overlay.
func goldenApplyFixture(t *testing.T) (*Applier, *PendingList, string, string) {
	t.Helper()
	tmp := t.TempDir()
	overlayDir := filepath.Join(tmp, "overlay")
	configDir := filepath.Join(tmp, "config")
	distdir := filepath.Join(tmp, "distdir")
	const pkg = "media-plugins/gst-plugins-qt6"

	createTestEbuildFileWithContent(t, overlayDir, pkg, "1.28.6", goldenApplyEbuild)

	pkgDir := filepath.Join(overlayDir, "media-plugins", "gst-plugins-qt6")
	manifest := "DIST gst-plugins-good-1.28.6.tar.gz 100 BLAKE2B ab SHA512 cd\n" +
		"DIST gst-plugins-good-1.29.2.tar.gz 100 BLAKE2B ef SHA512 01\n"
	if err := os.WriteFile(filepath.Join(pkgDir, "Manifest"), []byte(manifest), 0o644); err != nil {
		t.Fatalf("writing Manifest: %v", err)
	}

	if err := os.MkdirAll(distdir, 0o755); err != nil {
		t.Fatalf("creating distdir: %v", err)
	}
	writeGoldenArchive(t, distdir, "gst-plugins-good-1.28.6.tar.gz", "gst-plugins-good-1.28.6",
		[]string{"qt6", "aalib", "libcaca"})
	// 1.29.2: aalib and libcaca are gone from upstream. Nothing else changed.
	writeGoldenArchive(t, distdir, "gst-plugins-good-1.29.2.tar.gz", "gst-plugins-good-1.29.2",
		[]string{"qt6"})

	pending, err := NewPendingList(configDir)
	if err != nil {
		t.Fatalf("creating pending list: %v", err)
	}
	pending.Add(PendingUpdate{
		Package:        pkg,
		CurrentVersion: "1.28.6",
		NewVersion:     "1.29.2",
		Status:         StatusPending,
	})

	applier, err := NewApplier(overlayDir, configDir,
		WithApplierPendingList(pending),
		WithExecCommand(mockExecCommandSuccess),
		WithConfirmFunc(func(string) bool { return true }),
		WithApplierStagingRoot(filepath.Join(tmp, "staging")),
		WithApplierDistdir(distdir, ""),
		WithApplierIsolationProbe(func() (bool, string) { return true, "" }),
	)
	if err != nil {
		t.Fatalf("creating applier: %v", err)
	}
	return applier, pending, overlayDir, distdir
}

// TestGoldenApply_TheBumpThatBrokeIsNotPublished is the acceptance criterion of
// the whole story, through the staged apply path. The static gate rejects the
// bump, and the published overlay never receives it.
func TestGoldenApply_TheBumpThatBrokeIsNotPublished(t *testing.T) {
	applier, _, overlayDir, _ := goldenApplyFixture(t)
	const pkg = "media-plugins/gst-plugins-qt6"

	before := hashOverlayTree(t, overlayDir)
	result, _ := applier.Apply(pkg, false)

	if result.Success {
		t.Fatal("1.29.2 was published; upstream declares neither aalib nor libcaca and the ebuild passes both — " +
			"this is obentoo/bentoo#33 and the gate exists to catch it")
	}

	// The failure names the two options, or the operator has to go and diff two
	// tarballs by hand — which is what this whole story replaces.
	msg := ""
	if result.Error != nil {
		msg = result.Error.Error()
	}
	for _, want := range []string{"aalib", "libcaca"} {
		if !strings.Contains(msg, want) {
			t.Errorf("the failure does not name %q: %q", want, msg)
		}
	}

	// R3.5: not promoted means the overlay is untouched, byte for byte.
	if after := hashOverlayTree(t, overlayDir); after != before {
		t.Errorf("the published overlay changed for a bump that failed its gate: %s -> %s", before, after)
	}
	candidate := filepath.Join(overlayDir, "media-plugins", "gst-plugins-qt6", "gst-plugins-qt6-1.29.2.ebuild")
	if _, err := os.Stat(candidate); err == nil {
		t.Errorf("the rejected candidate is sitting in the published overlay at %q, which auto-commits and pushes", candidate)
	}
}

// TestGoldenApply_ThePreviousVersionStillPasses is the other half, and the one
// that stops the gate being a rubber stamp in reverse: a gate that fails
// everything is as useless as one that fails nothing.
func TestGoldenApply_ThePreviousVersionStillPasses(t *testing.T) {
	applier, pending, overlayDir, distdir := goldenApplyFixture(t)
	const pkg = "media-plugins/gst-plugins-qt6"

	// Re-aim the fixture at the bump that is FINE: 1.28.5 → 1.28.6, whose
	// archive still declares every option the ebuild passes.
	createTestEbuildFileWithContent(t, overlayDir, pkg, "1.28.5", goldenApplyEbuild)
	if err := os.Remove(filepath.Join(overlayDir, "media-plugins", "gst-plugins-qt6", "gst-plugins-qt6-1.28.6.ebuild")); err != nil {
		t.Fatalf("removing the newer ebuild: %v", err)
	}
	writeGoldenArchive(t, distdir, "gst-plugins-good-1.28.5.tar.gz", "gst-plugins-good-1.28.5",
		[]string{"qt6", "aalib", "libcaca"})
	pending.Add(PendingUpdate{
		Package: pkg, CurrentVersion: "1.28.5", NewVersion: "1.28.6", Status: StatusPending,
	})

	result, err := applier.Apply(pkg, false)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}

	if !result.Success {
		t.Fatalf("1.28.6 was rejected: %v — upstream still declares every option this ebuild passes", result.Error)
	}
	published := filepath.Join(overlayDir, "media-plugins", "gst-plugins-qt6", "gst-plugins-qt6-1.28.6.ebuild")
	got, err := os.ReadFile(published)
	if err != nil {
		t.Fatalf("the passing bump was not published: %v", err)
	}
	if string(got) != goldenApplyEbuild {
		t.Error("the published bytes differ from the validated ones (R3.4)")
	}
}

// TestGoldenApply_EveryBumpFailedAndTheOverlayIsByteIdentical is the story's
// central claim at the scale it is stated: not one bump, a RUN. Three packages,
// every gate failing, and the tree that auto-commits and pushes is the same tree
// afterwards.
//
// It is cheap — a hash before and after — which is exactly why there is no
// excuse for arguing it instead.
func TestGoldenApply_EveryBumpFailedAndTheOverlayIsByteIdentical(t *testing.T) {
	tmp := t.TempDir()
	overlayDir := filepath.Join(tmp, "overlay")
	configDir := filepath.Join(tmp, "config")

	packages := []string{
		"media-plugins/gst-plugins-qt6",
		"media-libs/gst-plugins-base",
		"dev-libs/somethingelse",
	}

	pending, err := NewPendingList(configDir)
	if err != nil {
		t.Fatalf("creating pending list: %v", err)
	}
	for _, pkg := range packages {
		createTestEbuildFileWithContent(t, overlayDir, pkg, "1.28.6", goldenApplyEbuild)
		pending.Add(PendingUpdate{
			Package: pkg, CurrentVersion: "1.28.6", NewVersion: "1.29.2", Status: StatusPending,
		})
	}

	pins := map[string]string{}
	applier, err := NewApplier(overlayDir, configDir,
		WithApplierPendingList(pending),
		// Every child fails: this is the run in which every bump failed.
		WithExecCommand(func(ctx context.Context, _ string, _ ...string) *exec.Cmd {
			return exec.CommandContext(ctx, "false")
		}),
		WithConfirmFunc(func(string) bool { return true }),
		WithApplierStagingRoot(filepath.Join(tmp, "staging")),
		WithApplierSetVersionsFunc(func(_ string, written map[string]string) error {
			for k, v := range written {
				pins[k] = v
			}
			return nil
		}),
		WithApplierIsolationProbe(func() (bool, string) { return true, "" }),
	)
	if err != nil {
		t.Fatalf("creating applier: %v", err)
	}

	before := hashOverlayTree(t, overlayDir)

	for _, pkg := range packages {
		result, _ := applier.Apply(pkg, false)
		if result.Success {
			t.Fatalf("%s succeeded although every child process failed", pkg)
		}
	}

	if after := hashOverlayTree(t, overlayDir); after != before {
		t.Errorf("after a run in which every bump failed, the published overlay is NOT byte-identical:\n  before %s\n  after  %s\n"+
			"this is the story's central claim, and it is asserted rather than argued", before, after)
	}
	if len(pins) != 0 {
		t.Errorf("pins were written for bumps that all failed: %v — --clean sweeps by pin, and a pin for an absent ebuild "+
			"aims the deletion rule at the only one present", pins)
	}
}
