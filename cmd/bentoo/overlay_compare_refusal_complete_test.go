package main

// Authored for story 047, sub-task 8.2 — S047-R5.1, S047-R5.2, S047-R5.3,
// S047-R3.3.
//
// This is the assertion the story lacked: the ENVELOPE, on a machine with no
// reviewer. internal/overlay's own test pins that the comparison records a
// refused pair; this one pins that the record reaches `complete` and
// `not_evaluated`, which is the pair of fields a machine reads to decide
// whether the document may be acted on.
//
// Both directions are here on purpose, because a fix for one is a plausible way
// to break the other. Treating "no reviewer" itself as an incompleteness would
// pass the first case and fail the second: `--no-review` is the operator
// narrowing the run, and a choice is not a shortfall (S047-R5.3). Only the
// producer knows the difference between "nobody asked" and "the check refused",
// which is why the fact is recorded there and only counted here.
//
// Reused rather than restated: reviewDirProvider and writeReviewEbuild come
// from overlay_compare_review_test.go.

import (
	"path/filepath"
	"testing"

	"github.com/obentoo/bentoolkit/internal/gentoo/repo"

	"github.com/obentoo/bentoolkit/internal/common/provider"
	"github.com/obentoo/bentoolkit/internal/overlay"
)

// refusalFixture compares the packages named by locals against an upstream that
// carries versions, and returns the finished report with the provider and
// options it came from — the same three values runCompare holds.
//
// Every package is written on both sides with the version each side declares,
// so a pair whose two versions differ is refused by the content check for the
// reason S047-R3.3 names and not because a file is missing.
func refusalFixture(t *testing.T, locals map[string]string, upstream map[string]string) (*overlay.CompareReport, provider.Provider, overlay.CompareOptions) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())

	const body = "EAPI=8\ninherit ecm\n"
	overlayRoot, upstreamRoot := t.TempDir(), t.TempDir()
	versions := map[string][]string{}
	var packages []repo.PackageInfo
	divergence := map[string]overlay.Divergence{}

	for atom, local := range locals {
		category, pkg := filepath.Split(atom)
		category = filepath.Clean(category)
		writeReviewEbuild(t, overlayRoot, category, pkg, local, body)
		writeReviewEbuild(t, upstreamRoot, category, pkg, upstream[atom], body)
		versions[atom] = []string{upstream[atom]}
		packages = append(packages, repo.PackageInfo{Category: category, Package: pkg, LatestVersion: local})
		divergence[atom] = overlay.Divergence{}
	}

	prov := &reviewDirProvider{root: upstreamRoot, versions: versions}
	opts := overlay.CompareOptions{IncludeSynced: true, OverlayPath: overlayRoot, Divergence: divergence}

	report, err := overlay.CompareWithProvider(t.Context(), packages, prov, opts)
	if err != nil {
		t.Fatalf("CompareWithProvider returned %v, want nil", err)
	}
	return report, prov, opts
}

// TestCompareRefusedPairIsNotEvaluatedWithoutAReviewer is the case the defect
// hid: a pair the content check refused, on a run that never had a reviewer to
// return early from.
func TestCompareRefusedPairIsNotEvaluatedWithoutAReviewer(t *testing.T) {
	report, prov, opts := refusalFixture(t,
		map[string]string{"net-libs/nodejs": "1.0", "dev-libs/libixion": "1.0"},
		map[string]string{"net-libs/nodejs": "2.0", "dev-libs/libixion": "1.0"})

	// The nil reviewer IS the case. runCompare calls this pass with whatever
	// compareDivergenceReviewer returned, and it returns nil both for
	// `--no-review` and for a machine with no `claude` on PATH.
	overlay.AnnotateReviews(t.Context(), report, nil, prov, opts)

	run := buildCompareReport(report, "gentoo", nil)
	if run.NotEvaluated < 1 {
		t.Errorf("NotEvaluated = %d, want at least 1: net-libs/nodejs-1.0 was never compared against 2.0, so the recommendation "+
			"printed for it stands on evidence nobody has, and a run with no reviewer must count it exactly as a run with one does (S047-R5.2)",
			run.NotEvaluated)
	}
	if run.Complete {
		t.Errorf("Complete = true beside NotEvaluated = %d: the document tells a machine reader that everything this run set out "+
			"to establish was established, over a package whose two ebuilds were never compared (S047-R5.1)", run.NotEvaluated)
	}
}

// TestCompareNoReviewOverACleanRunStaysComplete is the other side, and it is the
// one that fails if the hole above is closed in the adapter by reading a nil
// reviewer as an incompleteness.
//
// Nothing here was refused: one package, present on both sides at the same
// version, compared byte for byte. The operator asked for no review, and asking
// for no review is not a gap in what the run established (S047-R5.3).
func TestCompareNoReviewOverACleanRunStaysComplete(t *testing.T) {
	report, prov, opts := refusalFixture(t,
		map[string]string{"dev-libs/libixion": "1.0"},
		map[string]string{"dev-libs/libixion": "1.0"})

	overlay.AnnotateReviews(t.Context(), report, nil, prov, opts)

	run := buildCompareReport(report, "gentoo", nil)
	if run.NotEvaluated != 0 {
		t.Errorf("NotEvaluated = %d, want 0: every package was compared and nothing was refused, so `--no-review` has left "+
			"nothing unestablished — counting the reading nobody asked for would report a narrowed run as a holed one (S047-R5.3)",
			run.NotEvaluated)
	}
	if !run.Complete {
		t.Errorf("Complete = false on a run that compared every package it dispatched and refused none; the only thing missing " +
			"is the commentary the operator declined (S047-R5.3)")
	}
}
