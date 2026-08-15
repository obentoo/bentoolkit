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

// goldenFixtureOpts parameterises the fixture WITHOUT changing what the three
// original cases get. Its zero value reproduces the fixture exactly as story 033
// wrote it, which is why the cases below it were left untouched.
type goldenFixtureOpts struct {
	// omitCandidateArchive leaves the CANDIDATE's archive out of the shared
	// distdir — the state of any host that has never fetched this release: a
	// fresh CI runner, a new machine, or simply the first bump of this package.
	//
	// It is the whole of story 035's R5.1. A fixture that pre-places the archive
	// cannot fail for this reason, so the absence is part of the acceptance
	// criteria rather than an incidental property: with the archive present the
	// gate reads the shared distdir and answers correctly BY ACCIDENT, and the
	// lifetime defect is invisible.
	omitCandidateArchive bool

	// fetchingPkgdev makes the exec seam behave the way the real `pkgdev
	// manifest` does: it writes the distfile into whatever `--distdir` it is
	// handed. mockExecCommandSuccess runs `true` and writes nothing, so with it
	// no run ever fetches anything and omitCandidateArchive would only prove
	// that a missing archive stays missing.
	fetchingPkgdev bool
}

// goldenApplyFixture lays out the real shape: an overlay holding 1.28.6, a
// pending bump to 1.29.2, a distdir holding both upstream archives — 1.28.6
// still declares aalib and libcaca, 1.29.2 no longer does — and an applier whose
// staging root sits outside the overlay.
func goldenApplyFixture(t *testing.T) (*Applier, *PendingList, string, string) {
	t.Helper()
	return goldenApplyFixtureWith(t, goldenFixtureOpts{})
}

// goldenApplyFixtureWith is goldenApplyFixture with the knobs story 035 needs.
func goldenApplyFixtureWith(t *testing.T, opts goldenFixtureOpts) (*Applier, *PendingList, string, string) {
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
	//
	// Where it is written is the variable this story turns on. In the shared
	// distdir it is what a host that already fetched the release looks like; in
	// the upstream directory it is what EVERY other host looks like, and the run
	// has to fetch it.
	const candidateArchive = "gst-plugins-good-1.29.2.tar.gz"
	execCommand := mockExecCommandSuccess
	if opts.omitCandidateArchive {
		upstream := filepath.Join(tmp, "upstream")
		if err := os.MkdirAll(upstream, 0o755); err != nil {
			t.Fatalf("creating the upstream directory: %v", err)
		}
		writeGoldenArchive(t, upstream, candidateArchive, "gst-plugins-good-1.29.2", []string{"qt6"})
		if opts.fetchingPkgdev {
			execCommand = mockPkgdevFetchesInto(filepath.Join(upstream, candidateArchive))
		}
	} else {
		writeGoldenArchive(t, distdir, candidateArchive, "gst-plugins-good-1.29.2", []string{"qt6"})
		if opts.fetchingPkgdev {
			execCommand = mockPkgdevFetchesInto(filepath.Join(distdir, candidateArchive))
		}
	}

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
		WithExecCommand(execCommand),
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

// mockPkgdevFetchesInto is the exec seam behaving like the real `pkgdev
// manifest`: it puts the distfile in whatever `--distdir` it was handed, and
// leaves every other child untouched.
//
// The distinction matters for the same reason the bug survived review. Every
// other mock in this suite writes NOTHING, so the archive a gate reads is always
// one the fixture pre-placed, and where the run would have put its own download
// is a question no test ever asked.
func mockPkgdevFetchesInto(archive string) func(ctx context.Context, name string, arg ...string) *exec.Cmd {
	return func(ctx context.Context, name string, arg ...string) *exec.Cmd {
		if name != "pkgdev" {
			return exec.CommandContext(ctx, "true")
		}
		distdir := ""
		for i, a := range arg {
			if a == "--distdir" && i+1 < len(arg) {
				distdir = arg[i+1]
				break
			}
		}
		if distdir == "" {
			return exec.CommandContext(ctx, "true")
		}
		// `cp` and not a Go copy: the seam hands back an *exec.Cmd, and the point
		// is that a CHILD PROCESS puts the file there — exactly as pkgdev does.
		return exec.CommandContext(ctx, "cp", archive, distdir+string(os.PathSeparator))
	}
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

// TestGoldenApply_TheGateReadsWhatTheRunFetched is story 035's whole point —
// R1.2 and R5.1 — and it is the golden pair run TWICE with exactly one thing
// different: whether the shared distdir already held the candidate's archive.
//
// # Why the difference must not matter, and today does
//
// The manifest step fetches into a PRIVATE distdir under fixSandboxRoot()
// (sweep_staged.go), removes it on the way out, and the static gates afterwards
// read the SHARED one (applier_gates.go, staticGateDistdir). On the maintainer's
// host both archives were already in the shared distdir, so the gate answered
// correctly without ever depending on the fetch. On a host that has never
// fetched this release, the only archive present is the PREVIOUS version's —
// story 033's sub-task 8.1 declines to read it, correctly, because answering
// about the wrong tarball is worse than not answering — the option gate reports
// SKIPPED, and R3.3's "PASS or SKIPPED" promotes the bump.
//
// Two correct behaviours composing into a wrong one: the bump that broke
// obentoo/bentoo#33 is PUBLISHED, unread, to a tree that auto-commits and
// pushes.
//
// The two sub-tests are deliberately in one function rather than inferred from
// the contrast with the cases above: the control proves the fixture can pass, so
// a red in the bug case is the lifetime and not the harness.
func TestGoldenApply_TheGateReadsWhatTheRunFetched(t *testing.T) {
	const pkg = "media-plugins/gst-plugins-qt6"

	for _, tc := range []struct {
		name string
		omit bool
	}{
		// Control. The host already fetched this release at some earlier point,
		// which is the only state every existing fixture has ever described.
		{name: "the shared distdir already holds the archive", omit: false},
		// The bug. Every other host, and every first bump of a package.
		{name: "the host has never fetched this release", omit: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			applier, _, overlayDir, _ := goldenApplyFixtureWith(t, goldenFixtureOpts{
				omitCandidateArchive: tc.omit,
				fetchingPkgdev:       true,
			})

			before := hashOverlayTree(t, overlayDir)
			result, _ := applier.Apply(pkg, false)

			if result.Success {
				t.Fatalf("1.29.2 was PUBLISHED. The run fetched the archive; the gate did not read it, so nothing " +
					"ever compared the ebuild's options against upstream's. This is obentoo/bentoo#33 going out " +
					"to an overlay that auto-commits and pushes.")
			}

			msg := ""
			if result.Error != nil {
				msg = result.Error.Error()
			}
			for _, want := range []string{"aalib", "libcaca"} {
				if !strings.Contains(msg, want) {
					t.Errorf("the failure does not name %q — a bump can fail for many reasons, and only the option "+
						"gate having READ the candidate's archive produces this one: %q", want, msg)
				}
			}

			if after := hashOverlayTree(t, overlayDir); after != before {
				t.Errorf("the published overlay changed for a bump that failed its gate: %s -> %s", before, after)
			}
			candidate := filepath.Join(overlayDir, "media-plugins", "gst-plugins-qt6", "gst-plugins-qt6-1.29.2.ebuild")
			if _, err := os.Stat(candidate); err == nil {
				t.Errorf("the rejected candidate is sitting in the published overlay at %q", candidate)
			}
		})
	}
}
