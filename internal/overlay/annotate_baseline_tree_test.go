package overlay

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/obentoo/bentoolkit/internal/common/provider"
)

// This file pins the one shape baselineTreeOf could not reach: a result set that
// contains ONLY packages ::gentoo does not carry.
//
// It was found by MUTATION during sub-task 6.1 and nominated to 9.1 because the
// fix does not fit inside 6.1's scope. baselineTreeOf located the tree by asking
// the provider about each result in turn and keeping the first answer, so it
// needed one package ::gentoo actually carries to be IN VIEW. `--realign
// --only-outdated` on a run where nothing outdated has a counterpart leaves none:
// AnnotateBaseline returned at its first guard, wrote nothing, and the whole
// baseline review became a SILENT NO-OP AT EXIT 0 — no coverage line, no per
// package rows, no SKIPPED notice, nothing.
//
// That is the failure mode R1.5 exists to prevent, arriving through the back
// door: "we could not look" rendered as "there is nothing to say". A tree that is
// synced and perfectly readable would go unread because of which packages the
// operator happened to ask to see.
//
// # The two channels stay two channels
//
// Sub-task 1.2's whole point is that the run-level state and the per-package one
// are reported separately, and the fix must not blur them:
//
//   - THE TREE IS THERE and carries none of these packages -> every result is
//     Baseline.Found=false, NoBaselineCount says how many, and the coverage line
//     prints it with its denominator. BaselineSkipped stays EMPTY, because the
//     run succeeded. This is the case below, and asserting the empty
//     BaselineSkipped is what stops the fix from over-reaching: MarkBaselineSkipped
//     is read by exitOnSkippedBaseline, so setting it here would turn 84 packages
//     with no counterpart into a failed run.
//   - THERE IS NO TREE AT ALL -> nothing was examined, MarkBaselineSkipped says so
//     naming what was looked for, and NoBaselineCount stays 0 because a count of
//     len(Results) would assert that ::gentoo carries none of them. That is
//     TestAnnotateBaselineWithNoTreeSaysSoRatherThanNothing.
//
// It uses provider.NewLocalProvider — the REAL production type behind both
// `provider: local` and `--clone` — rather than the package's fake. That is
// load-bearing: localRootedFakeProvider answers LocalPackagePath for every
// package whether or not the tree carries it, so the bug is invisible through it,
// which is exactly why every existing test stayed green.

// baselineTreeFixture builds a synced ::gentoo tree and an overlay whose packages
// ::gentoo does not carry.
//
// `alsoInGentoo` is written into ::gentoo but NEVER into the overlay, so the tree
// is genuinely populated while no result in the report has a counterpart in it.
// Without it the fixture could not tell "the tree could not be reached" from "the
// tree is empty", and the assertions would hold for the wrong reason.
func baselineTreeFixture(t *testing.T, marker bool, alsoInGentoo string, ourPackages ...string) (overlayRoot string, prov provider.Provider, pkgs []PackageInfo) {
	t.Helper()

	home := t.TempDir()
	overlayRoot = filepath.Join(home, "overlay")
	gentooRoot := filepath.Join(home, "gentoo")

	if marker {
		// Portage's own repository marker: what LocateBaselineTree looks for, and
		// what tells a synced repository from a directory standing where one is
		// meant to be.
		if err := os.MkdirAll(filepath.Join(gentooRoot, "profiles"), 0o750); err != nil {
			t.Fatalf("mkdir gentoo/profiles: %v", err)
		}
		if err := os.WriteFile(filepath.Join(gentooRoot, "profiles", "repo_name"), []byte("gentoo\n"), 0o600); err != nil {
			t.Fatalf("write repo_name: %v", err)
		}
	} else if err := os.MkdirAll(gentooRoot, 0o750); err != nil {
		t.Fatalf("mkdir gentoo: %v", err)
	}

	const body = "EAPI=8\nDESCRIPTION=\"fixture\"\n"
	if alsoInGentoo != "" {
		category, pkg, ok := strings.Cut(alsoInGentoo, "/")
		if !ok {
			t.Fatalf("fixture atom %q is not category/package", alsoInGentoo)
		}
		writeVerifyEbuild(t, gentooRoot, category, pkg, "1.0.0", body)
	}

	for _, atom := range ourPackages {
		category, pkg, ok := strings.Cut(atom, "/")
		if !ok {
			t.Fatalf("fixture atom %q is not category/package", atom)
		}
		writeVerifyEbuild(t, overlayRoot, category, pkg, "1.0.0", body)
		pkgs = append(pkgs, PackageInfo{
			Category:      category,
			Package:       pkg,
			Versions:      []string{"1.0.0"},
			LatestVersion: "1.0.0",
		})
	}

	built, err := provider.NewLocalProvider(&provider.RepositoryInfo{Name: baselineRepo, Path: gentooRoot})
	if err != nil {
		t.Fatalf("NewLocalProvider(%s): %v", gentooRoot, err)
	}
	return overlayRoot, built, pkgs
}

// baselineTreeOpts is the options value a review run uses, as annotateReviewOpts
// spells it: IncludeNotInRemote is what gives the Bentoo-only packages rows at
// all, and without it this fixture would produce an empty report.
func baselineTreeOpts(overlayRoot string) CompareOptions {
	return CompareOptions{IncludeSynced: true, IncludeNotInRemote: true, OverlayPath: overlayRoot}
}

// TestAnnotateBaselineReviewsAResultSetWithNoCarriedPackage is the nominated
// defect: a review whose whole result set is Bentoo-only must still be a review.
//
// _Requirements: R1, R1.3, R1.5, R6, R6.4_
func TestAnnotateBaselineReviewsAResultSetWithNoCarriedPackage(t *testing.T) {
	overlayRoot, prov, pkgs := baselineTreeFixture(t, true, "sys-devel/binutils",
		"app-editors/zed", "app-arch/bentoo-tools")
	opts := baselineTreeOpts(overlayRoot)

	report, err := CompareWithProvider(pkgs, prov, opts)
	if err != nil {
		t.Fatalf("CompareWithProvider returned %v, want nil", err)
	}
	if len(report.Results) != len(pkgs) {
		t.Fatalf("report holds %d results, want %d (IncludeNotInRemote gives the Bentoo-only packages their rows)", len(report.Results), len(pkgs))
	}

	// THE PREMISE, asserted rather than assumed: the provider answers for none of
	// the packages in view. If it ever answers for one, the walk finds the tree
	// through it and this test stops exercising the defect at all.
	dirProv, ok := prov.(provider.PackageDirProvider)
	if !ok {
		t.Fatal("the local provider is not a PackageDirProvider; the review is refused for such a provider and this fixture would prove nothing")
	}
	for _, r := range report.Results {
		if _, err := dirProv.LocalPackagePath(r.Category, r.Package); !errors.Is(err, provider.ErrNotFound) {
			t.Fatalf("%s/%s: LocalPackagePath returned %v, want ErrNotFound — the fixture is meant to hold nothing ::gentoo carries", r.Category, r.Package, err)
		}
	}

	AnnotateBaseline(report, prov, opts)

	if report.NoBaselineCount != len(report.Results) {
		t.Errorf("NoBaselineCount is %d of %d results, want all of them — with no package ::gentoo carries in view the review wrote nothing at all, and a run that examined a perfectly readable tree reported neither a finding nor a failure (R6.4)",
			report.NoBaselineCount, len(report.Results))
	}

	// The run-level state stays the OTHER channel's. The tree is there and was
	// read; nothing about these packages is a failure of the run, and
	// exitOnSkippedBaseline reads this field to decide the exit code.
	if report.BaselineSkipped != "" {
		t.Errorf("BaselineSkipped is %q although the ::gentoo tree is present and readable; a package ::gentoo does not carry is a per-package answer (R1.3) and must never become a failed run (R1.5, D9)", report.BaselineSkipped)
	}

	rendered := FormatReport(report)
	if !strings.Contains(rendered, baselineSummaryLead) {
		t.Errorf("the report carries no baseline coverage line, so a review that could not measure a single package rendered exactly like one that found nothing to say:\n--- report ---\n%s", rendered)
	}
}

// TestAnnotateBaselineWithNoTreeSaysSoRatherThanNothing is the other channel: no
// ::gentoo repository at all is a RUN-level outcome, and silence is the one
// rendering it may not have (R1.5).
//
// The candidate directory exists and carries no profiles/repo_name — the
// /var/db/repos/gentoo of a machine that has never synced it, which is the case
// portageRepoMarker's own comment says would otherwise report all 321 packages as
// Bentoo's own work by a run that exited 0 and said nothing.
//
// _Requirements: R1, R1.5, R7.5_
func TestAnnotateBaselineWithNoTreeSaysSoRatherThanNothing(t *testing.T) {
	overlayRoot, prov, pkgs := baselineTreeFixture(t, false, "", "app-editors/zed")
	opts := baselineTreeOpts(overlayRoot)

	report, err := CompareWithProvider(pkgs, prov, opts)
	if err != nil {
		t.Fatalf("CompareWithProvider returned %v, want nil", err)
	}

	AnnotateBaseline(report, prov, opts)

	if report.BaselineSkipped == "" {
		t.Fatal("the review found no ::gentoo tree and recorded nothing; 'we could not look' rendered as 'nothing differs' is the exact failure R1.5 exists to prevent")
	}
	if !strings.Contains(report.BaselineSkipped, portageRepoMarker) {
		t.Errorf("BaselineSkipped is %q and does not name %s — a mistyped path and a repository that was never synced need opposite fixes, and R1.5 asks the review to say what it looked for", report.BaselineSkipped, portageRepoMarker)
	}

	// NOT counted as "::gentoo carries none of them". Nothing was examined, so
	// there is no coverage to report, and a count of len(Results) would be an
	// assertion about a tree nobody read.
	if report.NoBaselineCount != 0 {
		t.Errorf("NoBaselineCount is %d although no tree was located; 'we could not look' is not '::gentoo carries none of them'", report.NoBaselineCount)
	}

	if rendered := FormatReport(report); !strings.Contains(rendered, baselineSkippedLead) {
		t.Errorf("the SKIPPED state never reaches the operator:\n--- report ---\n%s", rendered)
	}
}
