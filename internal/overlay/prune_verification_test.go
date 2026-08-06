package overlay

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// This file pins R3: what "deleting our copy loses nothing" is allowed to mean,
// and the two ways the question can fail to have an answer at all.
//
// The distinction it exists to protect is the story's central one. A VERDICT is
// a statement about versions — "::gentoo ships the same or more". Whether our
// copy carries anything of ours is a statement about CONTENT, and only a byte
// comparison can make it. Measured on the live overlay, the two disagree on 8 of
// 74 redundant packages, so a prune driven by the verdict alone deletes kwin,
// plasma-desktop and nodejs with local changes still in them.
//
// Three properties carry that weight and each has a test that fails loudly if it
// is quietly narrowed:
//
//   - EVERY shared version is compared, not the newest one (R3.1). The natural
//     shortcut — compare the version the verdict already looked at — passes a
//     package whose old ebuild is the one carrying our patch.
//   - files/ is compared recursively, by path AND by content (R3.2). An
//     identical ebuild plus a same-named, different-bytes patch applies OUR
//     patch under THEIR ebuild, and nothing about the ebuild reveals it.
//   - "no answer" is never reported as "identical" (R3.3), and the two ways of
//     having no answer are told apart (R2.3): nothing to compare against, versus
//     nothing in common to compare.
//
// Manifest and metadata.xml are excluded by design (R3.4) and the test pins the
// exclusion rather than trusting the comment: metadata.xml names a maintainer
// and differs on all 314 packages we carry, so comparing it would mark the whole
// overlay divergent and the criterion would authorise nothing, ever.

const (
	// pruneEbuildStock is ::gentoo's copy — no local changes.
	pruneEbuildStock = "EAPI=8\nDESCRIPTION=\"zed editor\"\nSLOT=\"0\"\n"
	// pruneEbuildOurs carries a local change: the shape the comparison exists to
	// find before a removal discards it.
	pruneEbuildOurs = "EAPI=8\nDESCRIPTION=\"zed editor\"\nSLOT=\"0\"\nPATCHES=( \"${FILESDIR}/wayland.patch\" )\n"

	prunePkg = "zed"

	// pruneNoVersionInCommon is the reason R3.3 requires when the two trees hold
	// no version to compare. It is pinned as a literal because R2.3 asks the
	// operator to be told WHICH reason applies, and a reason that varies by call
	// site cannot be recognised in a report.
	pruneNoVersionInCommon = "no version in common"
)

// writePruneEbuild writes <dir>/<pkg>-<version>.ebuild — the filename shape both
// trees use and the only place PruneVerification handles a version string.
func writePruneEbuild(t *testing.T, dir, pkg, version, body string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o750); err != nil {
		t.Fatalf("mkdir %s: %v", dir, err)
	}
	path := filepath.Join(dir, pkg+"-"+version+".ebuild")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

// writePruneFile writes an arbitrary file at a path relative to a package
// directory, creating parents. Used for files/, Manifest and metadata.xml.
func writePruneFile(t *testing.T, dir, rel, body string) {
	t.Helper()
	path := filepath.Join(dir, rel)
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		t.Fatalf("mkdir %s: %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

// prunePair returns an overlay-side and an upstream-side package directory.
//
// They are separate temp roots on purpose: PruneVerification receives the two
// PACKAGE directories, already resolved by the caller from the overlay scan and
// the provider (R3.5). It never joins a category or a package name itself, so
// nothing it reads can be steered by a registry key.
func prunePair(t *testing.T) (ourDir, theirDir string) {
	t.Helper()
	return t.TempDir(), t.TempDir()
}

// assertChecked compares got.Checked against the versions the call should have
// examined, ignoring order — the plan prints them, it does not depend on their
// sequence.
func assertChecked(t *testing.T, got Content, want ...string) {
	t.Helper()
	seen := make(map[string]bool, len(got.Checked))
	for _, v := range got.Checked {
		seen[v] = true
	}
	if len(got.Checked) != len(want) {
		t.Errorf("Checked = %q, want the %d shared version(s) %q", got.Checked, len(want), want)
		return
	}
	for _, v := range want {
		if !seen[v] {
			t.Errorf("Checked = %q, missing shared version %q; a version left uncompared is a version whose local change survives into the removal", got.Checked, v)
		}
	}
}

// TestPruneVerificationIdenticalAcrossEverySharedVersion is the authorising
// case: every version the two trees share is byte-identical, so deleting our
// copy loses nothing.
//
// The fixture gives each side a version the other lacks (3.0 ours, 4.0 theirs)
// to pin that an unshared version is not a difference. It is the ordinary shape
// of this overlay — 157 packages are present upstream with no version in common
// precisely because we run ahead — and treating "they do not have our 3.0" as a
// divergence would refuse nearly everything.
//
// Neither side has a files/ directory, which is 225 of the 314 packages: absent
// on both sides must read as identical, not as unverifiable.
//
// _Requirements: R3.1, R2.1_
func TestPruneVerificationIdenticalAcrossEverySharedVersion(t *testing.T) {
	ourDir, theirDir := prunePair(t)

	writePruneEbuild(t, ourDir, prunePkg, "1.0", pruneEbuildStock)
	writePruneEbuild(t, ourDir, prunePkg, "2.0", pruneEbuildStock)
	writePruneEbuild(t, ourDir, prunePkg, "3.0", pruneEbuildStock)

	writePruneEbuild(t, theirDir, prunePkg, "1.0", pruneEbuildStock)
	writePruneEbuild(t, theirDir, prunePkg, "2.0", pruneEbuildStock)
	writePruneEbuild(t, theirDir, prunePkg, "4.0", pruneEbuildStock)

	got := PruneVerification(ourDir, theirDir, prunePkg)

	if got.Result != ContentIdentical {
		t.Errorf("Result = %v (reason %q), want ContentIdentical; every shared version is byte-identical and nothing else was asked about", got.Result, got.Reason)
	}
	assertChecked(t, got, "1.0", "2.0")
}

// TestPruneVerificationDiffersOnANonLatestVersion is the test that makes R3.1
// load-bearing rather than decorative.
//
// The newest shared version is identical, so a comparison that looked only at
// the version the verdict already agreed on would report this package as safe to
// remove — and the removal would discard the patch sitting in 1.0. The Reason
// must name the version, because "this package differs" sends the operator to
// diff a directory while "1.0 differs" sends them to a file.
//
// _Requirements: R3.1, R2.2_
func TestPruneVerificationDiffersOnANonLatestVersion(t *testing.T) {
	ourDir, theirDir := prunePair(t)

	writePruneEbuild(t, ourDir, prunePkg, "1.0", pruneEbuildOurs)
	writePruneEbuild(t, ourDir, prunePkg, "2.0", pruneEbuildStock)

	writePruneEbuild(t, theirDir, prunePkg, "1.0", pruneEbuildStock)
	writePruneEbuild(t, theirDir, prunePkg, "2.0", pruneEbuildStock)

	got := PruneVerification(ourDir, theirDir, prunePkg)

	if got.Result != ContentDiffers {
		t.Fatalf("Result = %v, want ContentDiffers; 1.0 carries a local patch and only 2.0 matches upstream", got.Result)
	}
	if !strings.Contains(got.Reason, "1.0") {
		t.Errorf("Reason = %q, want it to name the differing version 1.0; a reason that does not name the version leaves the operator diffing a whole directory", got.Reason)
	}
}

// TestPruneVerificationNoSharedVersionIsUnverifiable covers R3.3: the upstream
// tree holds none of our versions, so there is nothing to compare against.
//
// The wrong answer here is not "differs" but "identical-by-vacuous-truth" — a
// loop over an empty intersection finds no difference and returns success unless
// the empty case is handled before it. That is the bug this test exists for.
//
// Measured 0 such packages among today's 74 redundant ones, which is why the
// requirement exists rather than being dismissed: ::gentoo does drop old
// versions, and the day it drops ours the criterion must refuse, not shrug.
//
// _Requirements: R3.3, R2.3_
func TestPruneVerificationNoSharedVersionIsUnverifiable(t *testing.T) {
	ourDir, theirDir := prunePair(t)

	writePruneEbuild(t, ourDir, prunePkg, "1.0", pruneEbuildStock)
	writePruneEbuild(t, theirDir, prunePkg, "2.0", pruneEbuildStock)

	got := PruneVerification(ourDir, theirDir, prunePkg)

	if got.Result != ContentUnverifiable {
		t.Fatalf("Result = %v, want ContentUnverifiable; an empty intersection is no evidence, and an empty comparison loop reports no difference", got.Result)
	}
	if got.Reason != pruneNoVersionInCommon {
		t.Errorf("Reason = %q, want %q; R2.3 asks the operator to be told which of the two unverifiable reasons applies", got.Reason, pruneNoVersionInCommon)
	}
}

// TestPruneVerificationFilesDirOnlyOnOurSideDiffers covers half of R3.2: a path
// present on one side only is a difference.
//
// The ebuild is byte-identical, so every check that stops at the ebuild passes
// this package for removal — and takes our patch file with it.
//
// _Requirements: R3.2, R2.2_
func TestPruneVerificationFilesDirOnlyOnOurSideDiffers(t *testing.T) {
	ourDir, theirDir := prunePair(t)

	writePruneEbuild(t, ourDir, prunePkg, "1.0", pruneEbuildStock)
	writePruneEbuild(t, theirDir, prunePkg, "1.0", pruneEbuildStock)
	writePruneFile(t, ourDir, filepath.Join("files", "wayland.patch"), "--- a/x\n+++ b/x\n")

	got := PruneVerification(ourDir, theirDir, prunePkg)

	if got.Result != ContentDiffers {
		t.Errorf("Result = %v (reason %q), want ContentDiffers; files/wayland.patch exists only on our side and would be deleted with the directory", got.Result, got.Reason)
	}
}

// TestPruneVerificationFilesDirSameNameDifferentBytesDiffers covers the other
// half of R3.2, and the case M2 calls constructible and invisible.
//
// Same ebuild, same patch filename, different patch contents: the ebuild applies
// ${FILESDIR}/wayland.patch either way, so upstream's identical ebuild builds
// something else entirely. Nothing about comparing ebuilds can see it.
//
// _Requirements: R3.2, R2.2_
func TestPruneVerificationFilesDirSameNameDifferentBytesDiffers(t *testing.T) {
	ourDir, theirDir := prunePair(t)

	writePruneEbuild(t, ourDir, prunePkg, "1.0", pruneEbuildStock)
	writePruneEbuild(t, theirDir, prunePkg, "1.0", pruneEbuildStock)
	writePruneFile(t, ourDir, filepath.Join("files", "wayland.patch"), "--- a/x\n+++ b/x\n+our hunk\n")
	writePruneFile(t, theirDir, filepath.Join("files", "wayland.patch"), "--- a/x\n+++ b/x\n+their hunk\n")

	got := PruneVerification(ourDir, theirDir, prunePkg)

	if got.Result != ContentDiffers {
		t.Errorf("Result = %v (reason %q), want ContentDiffers; the two files/wayland.patch share a name and nothing else, so the identical ebuild applies a different patch on each side", got.Result, got.Reason)
	}
}

// TestPruneVerificationIgnoresManifestAndMetadata pins R3.4 as behaviour rather
// than as a comment.
//
// Manifest holds distfile hashes that legitimately differ by revision, and
// metadata.xml names a maintainer — which differs on all 314 packages the
// overlay carries. Comparing either would mark the entire overlay divergent and
// the criterion would authorise no removal at all, which is a criterion that has
// stopped being one.
//
// _Requirements: R3.4, R2.1_
func TestPruneVerificationIgnoresManifestAndMetadata(t *testing.T) {
	ourDir, theirDir := prunePair(t)

	writePruneEbuild(t, ourDir, prunePkg, "1.0", pruneEbuildStock)
	writePruneEbuild(t, theirDir, prunePkg, "1.0", pruneEbuildStock)
	writePruneFile(t, ourDir, filepath.Join("files", "shared.patch"), "same on both sides\n")
	writePruneFile(t, theirDir, filepath.Join("files", "shared.patch"), "same on both sides\n")

	writePruneFile(t, ourDir, "Manifest", "DIST zed-1.0.tar.gz 1 BLAKE2B aaaa SHA512 aaaa\n")
	writePruneFile(t, theirDir, "Manifest", "DIST zed-1.0.tar.gz 2 BLAKE2B bbbb SHA512 bbbb\n")
	writePruneFile(t, ourDir, "metadata.xml", "<pkgmetadata><maintainer><email>otakugeekx@gmail.com</email></maintainer></pkgmetadata>\n")
	writePruneFile(t, theirDir, "metadata.xml", "<pkgmetadata><maintainer><email>gentoo@gentoo.org</email></maintainer></pkgmetadata>\n")

	got := PruneVerification(ourDir, theirDir, prunePkg)

	if got.Result != ContentIdentical {
		t.Errorf("Result = %v (reason %q), want ContentIdentical; only Manifest and metadata.xml differ, and both are excluded because they differ on every package we carry", got.Result, got.Reason)
	}
	assertChecked(t, got, "1.0")
}

// TestPruneVerificationUnreadableDirIsUnverifiable covers the other unverifiable
// reason (R2.3): the tree could not be read at all.
//
// It must NOT collapse into "no version in common". Both produce an empty list
// of upstream ebuilds, and a comparison that only counts them tells the operator
// "::gentoo dropped your versions" when what actually happened is that the clone
// is missing or unreadable. One of those is a reason to look at the overlay; the
// other is a reason to look at the provider.
//
// _Requirements: R3.3, R2.3_
func TestPruneVerificationUnreadableDirIsUnverifiable(t *testing.T) {
	t.Run("upstream directory does not exist", func(t *testing.T) {
		ourDir, theirRoot := prunePair(t)
		writePruneEbuild(t, ourDir, prunePkg, "1.0", pruneEbuildStock)
		theirDir := filepath.Join(theirRoot, "absent")

		got := PruneVerification(ourDir, theirDir, prunePkg)

		if got.Result != ContentUnverifiable {
			t.Fatalf("Result = %v, want ContentUnverifiable; there is no upstream tree to compare against", got.Result)
		}
		if got.Reason == "" {
			t.Error("Reason is empty; R2.3 requires the unverifiable case to say which reason applies")
		}
		if got.Reason == pruneNoVersionInCommon {
			t.Errorf("Reason = %q, but the directory could not be read at all; reporting it as an empty intersection sends the operator to inspect the overlay when the provider is what failed", got.Reason)
		}
	})

	t.Run("upstream directory is not readable", func(t *testing.T) {
		if os.Geteuid() == 0 {
			t.Skip("running as root: permission bits do not restrict reads")
		}
		ourDir, theirDir := prunePair(t)
		writePruneEbuild(t, ourDir, prunePkg, "1.0", pruneEbuildStock)
		writePruneEbuild(t, theirDir, prunePkg, "1.0", pruneEbuildStock)

		if err := os.Chmod(theirDir, 0o000); err != nil {
			t.Fatalf("chmod %s: %v", theirDir, err)
		}
		// Restore before TempDir's own cleanup runs, or the harness fails to
		// remove the tree and the failure is reported against this test.
		t.Cleanup(func() { _ = os.Chmod(theirDir, 0o750) })

		got := PruneVerification(ourDir, theirDir, prunePkg)

		if got.Result != ContentUnverifiable {
			t.Fatalf("Result = %v, want ContentUnverifiable; an unreadable tree is no evidence either way", got.Result)
		}
		if got.Reason == "" {
			t.Error("Reason is empty; R2.3 requires the unverifiable case to say which reason applies")
		}
		if got.Reason == pruneNoVersionInCommon {
			t.Errorf("Reason = %q, but the read failed; that is a provider problem, not an upstream-dropped-our-version one", got.Reason)
		}
	})
}
