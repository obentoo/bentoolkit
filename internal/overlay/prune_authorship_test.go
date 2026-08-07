package overlay

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// This file pins R6: a divergence the overlay's own content PROVES is ours is
// refused, and no flag on this command reaches it.
//
// It is the one hole story 029 left open. `overlay prune` decides on content,
// which already refuses a package whose bytes differ — but only by DEFAULT.
// --include-patched exists to say "yes, discard the local work in these", and
// story 025 established that a `patched` registry entry is what suppresses a
// removal permanently. A divergence that is OURS IN FACT AND UNDECLARED has no
// declaration to protect it, so the flag reaches it and the work goes. Measured
// on the live overlay on 2026-08-07, `--include-patched` plans 8 such packages
// and the authorship check proves 3 of them ours: net-libs/nodejs,
// kde-plasma/spectacle and kde-plasma/kdeplasma-addons, each by a patch under
// files/ that ::gentoo does not ship.
//
// Two properties carry the whole requirement, and they pull in opposite
// directions:
//
//   - PROVED means REFUSED, under every flag (R6.1), with the proving file named
//     so the operator can confirm the claim by looking (R6.2). A refusal nobody
//     can check is one nobody will check.
//   - The refusal must not LEAK. A package the content check found identical is
//     still removable, or `prune` stops being able to remove anything — 66 of the
//     74 packages the live plan would remove are in that batch.
//
// And the failure direction, which is not symmetric: the authorship check
// failing to run must leave the package exactly where today's classification put
// it. A check that could not look has found nothing, and nothing is never a
// reason to remove something.
//
// The classification is driven through PlanPrune over real temporary trees
// rather than through classifyPrune over hand-built results. That is deliberate:
// the proof is only worth anything if the PLANNER consults it, and a suite over
// hand-built CompareResults carrying Authorship already filled in would stay
// green while the live command refused nothing at all — which is exactly the
// state this branch found the code in.

const (
	// pruneAuthorshipCat is the category all the fixtures below live in. Every
	// one of them is a shape measured in the live overlay, and three of the eight
	// undeclared divergences there are kde-plasma packages.
	pruneAuthorshipCat = "kde-plasma"

	// provedPrunePkg / provedPruneVersion / provedPruneEbuild are
	// kde-plasma/spectacle-6.7.4.ebuild:72 verbatim — one of the three packages
	// the live plan proves ours, and the reason ${PN} expansion is not optional:
	// unexpanded, the reference reads "${PN}-opencv5.patch", which no tree ships,
	// and the miss would be reported as proof for every package on earth.
	provedPrunePkg     = "spectacle"
	provedPruneVersion = "6.7.4"
	provedPruneEbuild  = "EAPI=8\ninherit ecm\nPATCHES=(\n\t\"${FILESDIR}/${PN}-cmake.patch\"\n\t\"${FILESDIR}/${PN}-opencv5.patch\"\n)\n"
	// upstreamPruneEbuild is ::gentoo's copy of that version: the same file
	// without our patch line, which is what makes the two differ.
	upstreamPruneEbuild = "EAPI=8\ninherit ecm\n"
	// provedPruneFile is the name the refusal has to carry (R6.2), spelled
	// package-relative exactly as CompareResult.ProvedBy spells it: the operator
	// confirms the claim by pasting it after the package directory, not by
	// knowing what ${FILESDIR} expands to.
	provedPruneFile = "files/spectacle-opencv5.patch"
	// shippedPruneFile is the decoy. Our ebuild references it too, ::gentoo ships
	// it, and it therefore proves nothing — so a refusal naming IT would send the
	// operator to confirm a claim the report did not make.
	shippedPruneFile = "spectacle-cmake.patch"
)

// authorshipTrees writes one package into an overlay root and an upstream root
// and returns both roots, plus a provider rooted at the upstream one.
//
// It returns the UPSTREAM ROOT, which planTrees does not: every case here turns
// on what ::gentoo does or does not ship BESIDE its ebuild, so each test has to
// write into that tree itself. Everything else is planTrees' scaffolding reused
// as it stands — writePruneEbuild for the filename shape both trees use, and
// pruneRecordingProvider for the on-disk provider capability.
func authorshipTrees(t *testing.T, pkg, version, ourBody, theirBody string) (overlayRoot, upstreamRoot string, prov *pruneRecordingProvider) {
	t.Helper()
	overlayRoot, upstreamRoot = t.TempDir(), t.TempDir()
	writePruneEbuild(t, filepath.Join(overlayRoot, pruneAuthorshipCat, pkg), pkg, version, ourBody)
	writePruneEbuild(t, filepath.Join(upstreamRoot, pruneAuthorshipCat, pkg), pkg, version, theirBody)
	return overlayRoot, upstreamRoot, &pruneRecordingProvider{
		root:     upstreamRoot,
		versions: map[string][]string{pruneAuthorshipCat + "/" + pkg: {version}},
	}
}

// writeUpstreamFilesdirFile creates <root>/<cat>/<pkg>/files/<rel> — the tree
// ${FILESDIR} names, on ::gentoo's side. rel is slash-separated exactly as
// ebuildFilesdirRefs returns it and is converted here, so a fixture cannot
// accidentally agree with an implementation that forgot to.
func writeUpstreamFilesdirFile(t *testing.T, root, pkg, rel string) {
	t.Helper()
	writePruneFile(t,
		filepath.Join(root, pruneAuthorshipCat, pkg),
		filepath.Join(pruneFilesDir, filepath.FromSlash(rel)),
		"upstream file\n")
}

// planProved plans the standard proved fixture: our ebuild references two
// patches, ::gentoo ships one of them, and the overlay carries the other.
//
// The DECOY is the point. With only one reference, "the refusal names a file"
// and "the refusal names the file that proves it" are the same assertion, and an
// implementation that reported whichever reference it saw first would pass.
func planProved(t *testing.T, includePatched bool) PruneBatch {
	t.Helper()
	overlayRoot, upstreamRoot, prov := authorshipTrees(t,
		provedPrunePkg, provedPruneVersion, provedPruneEbuild, upstreamPruneEbuild)
	// ::gentoo has a files/ tree of its own, just not our patch: the miss is a
	// real absence rather than an absent directory.
	writeUpstreamFilesdirFile(t, upstreamRoot, provedPrunePkg, shippedPruneFile)
	// The overlay really carries the patch it references, which is what makes
	// this a removal that destroys something.
	writePruneFile(t,
		filepath.Join(overlayRoot, pruneAuthorshipCat, provedPrunePkg),
		filepath.FromSlash(provedPruneFile),
		"--- a/CMakeLists.txt\n")

	return PlanPrune(
		[]CompareResult{planResult(pruneAuthorshipCat, provedPrunePkg, provedPruneVersion, VerdictRedundant)},
		prov,
		PruneOptions{OverlayPath: overlayRoot, IncludePatched: includePatched},
	)
}

// assertRefusedAlone fails unless the package is in Refused, is not eligible,
// and is in NO other bucket.
//
// All three are asserted because the buckets and the eligibility are separate
// facts. ExecutePrune reads Eligible and never the bucket — deliberately, so
// that a plan filed under the wrong heading still cannot be removed — so a test
// that checked only the heading would pass for a plan the executor would delete.
func assertRefusedAlone(t *testing.T, batch PruneBatch, why string) PrunePlan {
	t.Helper()
	if len(batch.Diverging) != 0 {
		t.Errorf("Diverging holds %+v; %s. --include-patched acts on that batch, so a proved package landing in it is removable",
			batch.Diverging, why)
	}
	if len(batch.Identical) != 0 {
		t.Errorf("Identical holds %+v; %s", batch.Identical, why)
	}
	plan := onlyPlan(t, batch.Refused, "Refused")
	if plan.Class != PruneRefused {
		t.Errorf("Class = %v, want PruneRefused; %s", plan.Class, why)
	}
	if plan.Eligible {
		t.Errorf("%s/%s is Eligible; %s", plan.Category, plan.Package, why)
	}
	return plan
}

// TestPruneAuthorship is R6's table: what the planner does about a divergence
// once the overlay's own content has been asked who wrote it.
//
//	| proved ours, any flag           | Refused   | never eligible (R6.1) |
//	| unproved, undeclared            | Diverging | only --include-patched|
//	| the check could not run         | Diverging | only --include-patched|
//	| content identical               | Identical | eligible              |
//
// Row 3 is the one that has to be read twice. "The check could not run" and "the
// check ran and proved nothing" produce the same class on purpose: neither is
// evidence, and R6 only ever ADDS a refusal. A failure that changed the outcome
// in the other direction would be a removal authorised by a broken read.
//
// _Requirements: R6.1, R6.2_
func TestPruneAuthorship(t *testing.T) {
	t.Run("a proved divergence is refused under --include-patched (R6.1)", func(t *testing.T) {
		batch := planProved(t, true)

		assertRefusedAlone(t, batch, "our ebuild applies a patch ::gentoo does not ship, so the divergence cannot "+
			"have been inherited from ::gentoo and removing the package destroys the only copy of it")
	})

	t.Run("no flag makes a proved divergence eligible (R6.1)", func(t *testing.T) {
		// Without the flag today's code already declines to remove it — but as a
		// DIVERGING plan the flag would then reach. The refusal has to hold on its
		// own, so that the run which does type the flag finds nothing to act on.
		batch := planProved(t, false)

		plan := assertRefusedAlone(t, batch, "a proved divergence is refused on its own evidence, not on the absence of a flag")
		if !strings.Contains(plan.Reason, provedPruneFile) {
			t.Errorf("Reason = %q, want it to name %q even without the flag; the refusal is the same finding either way",
				plan.Reason, provedPruneFile)
		}
	})

	t.Run("the refusal names the file that proves the authorship (R6.2)", func(t *testing.T) {
		batch := planProved(t, true)
		plan := onlyPlan(t, batch.Refused, "Refused")

		if !strings.Contains(plan.Reason, provedPruneFile) {
			t.Errorf("Reason = %q, want it to name %q; a refusal the operator cannot check without re-deriving the path "+
				"from what ${FILESDIR} expands to is one they will not check", plan.Reason, provedPruneFile)
		}
		if strings.Contains(plan.Reason, shippedPruneFile) {
			t.Errorf("Reason = %q, but %q is shipped by ::gentoo and proves nothing; naming it sends the operator to "+
				"confirm a claim the report did not make", plan.Reason, shippedPruneFile)
		}
	})

	t.Run("an unproved divergence keeps today's diverging classification", func(t *testing.T) {
		// Two shapes, both measured. The first is four of the eight live
		// divergences — a one-line difference, ::gentoo's own in-place revision as
		// often as ours, with no ${FILESDIR} reference anywhere in the file. The
		// second is the copied-ebuild case: the reference came from ::gentoo along
		// with the rest of the file, so it says nothing about who changed what.
		//
		// Neither is "the change is upstream's". Unproved means the content cannot
		// tell, which is why the package keeps the classification it had: printed,
		// with its reason, and removable only by an operator who types the flag and
		// answers the prompt that names it.
		cases := []struct {
			name    string
			ourBody string
			shipped string
		}{
			{
				name:    "the ebuild references no file at all",
				ourBody: "EAPI=8\nPYTHON_COMPAT=( python3_{11..14} )\ninherit ecm\n",
			},
			{
				name:    "::gentoo ships every file the ebuild references",
				ourBody: "EAPI=8\ninherit ecm\nPATCHES=( \"${FILESDIR}/${PN}-cmake.patch\" )\nSLOT=\"0\"\n",
				shipped: shippedPruneFile,
			},
		}

		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				plan := func(includePatched bool) PrunePlan {
					t.Helper()
					overlayRoot, upstreamRoot, prov := authorshipTrees(t,
						provedPrunePkg, provedPruneVersion, tc.ourBody, upstreamPruneEbuild)
					if tc.shipped != "" {
						writeUpstreamFilesdirFile(t, upstreamRoot, provedPrunePkg, tc.shipped)
					}
					batch := PlanPrune(
						[]CompareResult{planResult(pruneAuthorshipCat, provedPrunePkg, provedPruneVersion, VerdictRedundant)},
						prov,
						PruneOptions{OverlayPath: overlayRoot, IncludePatched: includePatched},
					)
					if len(batch.Refused) != 0 {
						t.Fatalf("Refused holds %+v; nothing here proves the divergence is ours, and R6 adds a refusal "+
							"only where there is a proof", batch.Refused)
					}
					return onlyPlan(t, batch.Diverging, "Diverging")
				}

				if got := plan(false); got.Eligible {
					t.Error("the package is Eligible by default; an undeclared divergence is not removed without the flag (R4.1)")
				}
				if got := plan(true); !got.Eligible {
					t.Error("--include-patched leaves an unproved divergence ineligible; R6 refuses the PROVED ones and " +
						"changes nothing about the rest, or the flag stops meaning anything")
				}
			})
		}
	})

	t.Run("an authorship check that could not run keeps today's classification", func(t *testing.T) {
		// The failure shape from authorship_test.go, for the same reason: a
		// regular FILE where files/icon/ would be a directory makes the stat fail
		// with ENOTDIR rather than ENOENT, and it keeps failing when the test runs
		// as root — which the CI runner does, so a chmod-based fixture would
		// silently stop failing there.
		//
		// The control is the case above it: with the directory readable this same
		// reference IS a proof and the package is refused. Here the check could not
		// look, so it has found nothing, and the package keeps exactly the
		// classification it had before R6 existed. That costs it the protection a
		// proof would have given — it does not manufacture an eligibility, and the
		// operator still has to type the flag and answer the prompt that names it.
		plan := func(includePatched bool) PrunePlan {
			t.Helper()
			overlayRoot, upstreamRoot, prov := authorshipTrees(t,
				provedPrunePkg, provedPruneVersion,
				"EAPI=8\ninherit ecm\nPATCHES=( \"${FILESDIR}/icon/${PN}.desktop\" )\n",
				upstreamPruneEbuild)
			blocked := filepath.Join(upstreamRoot, pruneAuthorshipCat, provedPrunePkg, pruneFilesDir, "icon")
			if err := os.MkdirAll(filepath.Dir(blocked), 0o750); err != nil {
				t.Fatalf("mkdir %s: %v", filepath.Dir(blocked), err)
			}
			if err := os.WriteFile(blocked, []byte("not a directory\n"), 0o600); err != nil {
				t.Fatalf("write %s: %v", blocked, err)
			}

			batch := PlanPrune(
				[]CompareResult{planResult(pruneAuthorshipCat, provedPrunePkg, provedPruneVersion, VerdictRedundant)},
				prov,
				PruneOptions{OverlayPath: overlayRoot, IncludePatched: includePatched},
			)
			if len(batch.Identical) != 0 {
				t.Fatalf("Identical holds %+v; the bytes differ, and a failed authorship check cannot promote a package "+
					"into the batch a default run removes", batch.Identical)
			}
			return onlyPlan(t, batch.Diverging, "Diverging")
		}

		got := plan(false)
		if got.Class != PruneDiverging {
			t.Errorf("Class = %v, want PruneDiverging; a check that could not look has found nothing, in either direction", got.Class)
		}
		if got.Eligible {
			t.Error("the package is Eligible by default; a failed authorship check must not be the thing that authorises a removal")
		}
		if !plan(true).Eligible {
			t.Error("the package is not Eligible under --include-patched either, so a failed check silently protects it; " +
				"R6 promises a refusal on PROOF, and a refusal that also fires on an unreadable directory would be a " +
				"different promise nobody could act on")
		}
	})

	t.Run("a package the content check found identical is untouched by the refusal", func(t *testing.T) {
		// The adversarial fixture: the two copies are byte-identical, INCLUDING a
		// reference to a patch that neither tree ships. Consulted without asking
		// whether anything diverges at all, that reference proves "ours" — and 66
		// of the 74 packages the live plan would remove are in this batch, so a
		// refusal leaking here stops `prune` removing anything ever again.
		//
		// There is nothing to attribute: every byte of this package is in
		// ::gentoo, so nothing would be lost by deleting our copy of it.
		stale := "EAPI=8\ninherit ecm\nPATCHES=( \"${FILESDIR}/${PN}-opencv5.patch\" )\n"
		overlayRoot, _, prov := authorshipTrees(t, provedPrunePkg, provedPruneVersion, stale, stale)

		batch := PlanPrune(
			[]CompareResult{planResult(pruneAuthorshipCat, provedPrunePkg, provedPruneVersion, VerdictRedundant)},
			prov,
			PruneOptions{OverlayPath: overlayRoot, IncludePatched: true},
		)

		if len(batch.Refused) != 0 {
			t.Fatalf("Refused holds %+v; the two copies hold the same bytes, so there is no divergence whose authorship "+
				"there is anything to prove", batch.Refused)
		}
		got := onlyPlan(t, batch.Identical, "Identical")
		if !got.Eligible {
			t.Error("an identical package is not Eligible; that is the case a prune exists for, and R6 must not reach it")
		}
	})

	t.Run("a declared patch that is also proved is refused, not merely diverging", func(t *testing.T) {
		// Defence in depth, and stated as such: a `patched` declaration makes
		// deriveVerdict return keep or needs-rebase, so PlanPrune refuses the
		// package at the verdict gate and this row is unreachable in production
		// today. It is here because R6.1 refuses a PROVED divergence with no
		// exception for a declared one, and if the verdict ever stops disqualifying
		// a declaration, --include-patched would otherwise reach a package whose
		// files prove the work is ours and exists nowhere else.
		overlayRoot, upstreamRoot, prov := authorshipTrees(t,
			provedPrunePkg, provedPruneVersion, provedPruneEbuild, upstreamPruneEbuild)
		writeUpstreamFilesdirFile(t, upstreamRoot, provedPrunePkg, shippedPruneFile)

		res := planResult(pruneAuthorshipCat, provedPrunePkg, provedPruneVersion, VerdictRedundant)
		res.Patched = true
		res.PatchedBy = "kde-plasma/spectacle@stable"
		res.PatchedReason = "keeps our opencv5 patch"

		batch := PlanPrune([]CompareResult{res}, prov, PruneOptions{OverlayPath: overlayRoot, IncludePatched: true})

		plan := assertRefusedAlone(t, batch, "the divergence is proved ours, and R6.1 names no flag and no declaration "+
			"that makes such a package removable")
		if !strings.Contains(plan.Reason, provedPruneFile) {
			t.Errorf("Reason = %q, want it to name %q (R6.2)", plan.Reason, provedPruneFile)
		}
	})
}

// TestPruneAuthorshipEndToEnd runs the pipeline the command runs — the real
// comparison, then the real planner — over a temporary overlay and upstream
// tree, and asserts that the proved package is refused with its file named.
//
// It exists because every assertion above could be satisfied without the live
// command refusing a single package. `overlay prune` calls CompareWithProvider
// and nothing else: no AnnotateAuthorship pass runs on that path, so every
// CompareResult reaching PlanPrune carries the zero Authorship. A suite that
// handed the planner results with the proof already filled in would be green
// while the overlay's three proved packages went on being planned for removal
// under --include-patched.
//
// So the fixture asserts the absence FIRST: the report the planner is given
// carries no proof at all. Whatever refuses the package after that is the
// planner having gone and looked for itself.
//
// The second package is the control. kde-plasma/kwin is one of the five live
// divergences nothing proves — a one-line difference, no ${FILESDIR} reference —
// and it must come through this pipeline exactly as it does today: diverging,
// planned, printed, and eligible under the flag. Without it the test could not
// tell a working refusal from one that refuses everything.
//
// _Requirements: R6.1_
func TestPruneAuthorshipEndToEnd(t *testing.T) {
	overlayRoot, upstreamRoot := t.TempDir(), t.TempDir()

	// Proved: our ebuild applies a patch ::gentoo does not ship for the package.
	writeVerifyEbuild(t, overlayRoot, pruneAuthorshipCat, provedPrunePkg, provedPruneVersion, provedPruneEbuild)
	writeVerifyEbuild(t, upstreamRoot, pruneAuthorshipCat, provedPrunePkg, provedPruneVersion, upstreamPruneEbuild)
	writeUpstreamFilesdirFile(t, upstreamRoot, provedPrunePkg, shippedPruneFile)
	writePruneFile(t,
		filepath.Join(overlayRoot, pruneAuthorshipCat, provedPrunePkg),
		filepath.FromSlash(provedPruneFile),
		"--- a/CMakeLists.txt\n")

	// Unproved: the bytes differ and nothing in them says who changed anything.
	const unprovedPkg = "kwin"
	writeVerifyEbuild(t, overlayRoot, pruneAuthorshipCat, unprovedPkg, provedPruneVersion,
		"EAPI=8\nPYTHON_COMPAT=( python3_{11..14} )\ninherit ecm\n")
	writeVerifyEbuild(t, upstreamRoot, pruneAuthorshipCat, unprovedPkg, provedPruneVersion,
		"EAPI=8\nPYTHON_COMPAT=( python3_{11..13} )\ninherit ecm\n")

	prov := &localRootedFakeProvider{
		root: upstreamRoot,
		versions: map[string][]string{
			pruneAuthorshipCat + "/" + provedPrunePkg: {provedPruneVersion},
			pruneAuthorshipCat + "/" + unprovedPkg:    {provedPruneVersion},
		},
	}

	// The options runPrune builds, minus the concurrency and the context: both
	// packages must reach the plan (IncludeSynced), and both must be KNOWN to the
	// divergence map, since an atom missing from it yields VerdictUnknown and
	// would be refused at the verdict gate for a reason that has nothing to do
	// with authorship.
	report, err := CompareWithProvider(
		[]PackageInfo{
			{Category: pruneAuthorshipCat, Package: provedPrunePkg, LatestVersion: provedPruneVersion},
			{Category: pruneAuthorshipCat, Package: unprovedPkg, LatestVersion: provedPruneVersion},
		},
		prov,
		CompareOptions{
			IncludeSynced: true,
			OverlayPath:   overlayRoot,
			Divergence: map[string]Divergence{
				pruneAuthorshipCat + "/" + provedPrunePkg: {},
				pruneAuthorshipCat + "/" + unprovedPkg:    {},
			},
		},
	)
	if err != nil {
		t.Fatalf("CompareWithProvider returned %v, want nil", err)
	}
	if len(report.Results) != 2 {
		t.Fatalf("report holds %d results, want 2", len(report.Results))
	}
	for _, r := range report.Results {
		// Fixture checks. A package that arrived as anything but redundant-and-
		// differing would be refused or cleared for a reason R6 has nothing to do
		// with, and every assertion below would pass for the wrong reason.
		if r.Verdict != VerdictRedundant || r.Verified != VerifiedDiffers {
			t.Fatalf("%s/%s arrived as verdict %v / verified %v, want redundant / differs",
				r.Category, r.Package, r.Verdict, r.Verified)
		}
		// THE POINT OF THIS TEST. Nothing on the prune path annotates authorship,
		// so the planner is handed exactly what the live command hands it: a
		// result that proves nothing about anybody. If this ever starts failing
		// because a caller annotates first, the planner still must not depend on
		// it — the refusal is its own to make.
		if r.Authorship != AuthorshipUnproved || r.ProvedBy != "" {
			t.Fatalf("%s/%s reached the planner already carrying Authorship %d / ProvedBy %q; this test proves the "+
				"planner looks for itself, and a pre-annotated result would let it pass without looking",
				r.Category, r.Package, r.Authorship, r.ProvedBy)
		}
	}

	batch := PlanPrune(report.Results, prov, PruneOptions{OverlayPath: overlayRoot, IncludePatched: true})

	refused := onlyPlan(t, batch.Refused, "Refused")
	if refused.Package != provedPrunePkg {
		t.Fatalf("Refused holds %s, want %s", refused.Package, provedPrunePkg)
	}
	if refused.Eligible {
		t.Error("the proved package is Eligible; --include-patched reaches whatever is eligible, and this removal " +
			"destroys the only copy of the patch our ebuild applies")
	}
	if !strings.Contains(refused.Reason, provedPruneFile) {
		t.Errorf("Reason = %q, want it to name %q (R6.2)", refused.Reason, provedPruneFile)
	}

	diverging := onlyPlan(t, batch.Diverging, "Diverging")
	if diverging.Package != unprovedPkg {
		t.Fatalf("Diverging holds %s, want %s; the proved package must never appear in the batch the flag acts on",
			diverging.Package, unprovedPkg)
	}
	if !diverging.Eligible {
		t.Error("the unproved package is not Eligible under --include-patched; R6 refuses what is proved and leaves " +
			"the rest exactly as story 029 left it")
	}
	if len(batch.Identical) != 0 {
		t.Errorf("Identical holds %+v; both packages differ from ::gentoo", batch.Identical)
	}
}
