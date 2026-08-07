package overlay

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/obentoo/bentoolkit/internal/common/provider"
)

// This file pins R2: which packages a prune may touch, and the ORDER in which
// that is decided.
//
// The order is the requirement, not an implementation detail:
//
//	verdict  ->  declaration  ->  content
//
//  1. A verdict other than `redundant` refuses the package and its content is
//     NEVER read (R2.5). That is both a correctness rule and what keeps a scan
//     over 314 packages cheap, and the test proves it by counting provider
//     calls rather than by trusting the code to be in the right order.
//  2. A registry entry declaring `patched` moves the package out of the
//     removable batch REGARDLESS of what the bytes say (R2.4). A declaration
//     that turns out to be stale is 025's finding to report and the operator's
//     to clear; it is not this command's to overrule.
//  3. Only then does content decide (R2.1, R2.2, R2.3).
//
// The class/eligibility table the batch encodes, derived from R2, R4.1 and
// design D3:
//
//	| verdict is not redundant                | Refused   | never eligible        |
//	| redundant, content unverifiable         | Refused   | never eligible        |
//	| redundant, `patched` declared           | Diverging | only --include-patched|
//	| redundant, a shared version differs     | Diverging | only --include-patched|
//	| redundant, identical, nothing declared  | Identical | eligible              |
//
// Unverifiable is the row worth stopping on: --include-patched does NOT rescue
// it (R4.1 names R2.2 and R2.4 and pointedly not R2.3). A flag can say "yes, I
// know I am discarding local work"; no flag can authorise discarding work
// nobody could look at.
//
// Class is what the evidence says; Eligible is what THIS RUN may do about it.
// Keeping them apart is what lets the plan print a diverging package, with its
// reason, on a run that will not touch it (R1.4) — the operator's next question
// about a package missing from the output is always "why not this one".

// pruneRecordingProvider is a local-rooted provider that COUNTS the calls asking
// it for a package directory.
//
// The count is the whole point: R2.5 says a non-redundant package's content is
// not compared, and the only way to prove a read did not happen is to observe
// that the path it would have needed was never requested. Asserting on the
// resulting Reason string would pass just as happily for an implementation that
// read every package and then discarded the answer.
type pruneRecordingProvider struct {
	root     string
	versions map[string][]string // keyed by "category/pkg"
	dirCalls []string            // every "category/pkg" whose directory was asked for
}

func (p *pruneRecordingProvider) GetPackageVersions(category, pkg string) ([]string, error) {
	v, ok := p.versions[category+"/"+pkg]
	if !ok {
		return nil, provider.ErrNotFound
	}
	return v, nil
}

func (p *pruneRecordingProvider) GetName() string   { return "fake-recording" }
func (p *pruneRecordingProvider) SupportsAPI() bool { return false }
func (p *pruneRecordingProvider) Close() error      { return nil }

func (p *pruneRecordingProvider) LocalPackagePath(category, pkg string) (string, error) {
	p.dirCalls = append(p.dirCalls, category+"/"+pkg)
	return filepath.Join(p.root, category, pkg), nil
}

var (
	_ provider.Provider           = (*pruneRecordingProvider)(nil)
	_ provider.PackageDirProvider = (*pruneRecordingProvider)(nil)
)

const pruneCat = "app-editors"

// planResult builds one CompareResult in the shape CompareWithProvider produces.
func planResult(category, pkg, version string, verdict Verdict) CompareResult {
	return CompareResult{
		Category:      category,
		Package:       pkg,
		LocalVersion:  version,
		RemoteVersion: version,
		Status:        StatusUpToDate,
		Verdict:       verdict,
	}
}

// planTrees writes the same package into an overlay root and an upstream root,
// with the bodies given, and returns both roots plus a provider rooted at the
// upstream one. An empty body means "do not write this side".
func planTrees(t *testing.T, category, pkg, version, ourBody, theirBody string) (overlayRoot string, prov *pruneRecordingProvider) {
	t.Helper()
	overlayRoot, upstreamRoot := t.TempDir(), t.TempDir()
	if ourBody != "" {
		writePruneEbuild(t, filepath.Join(overlayRoot, category, pkg), pkg, version, ourBody)
	}
	if theirBody != "" {
		writePruneEbuild(t, filepath.Join(upstreamRoot, category, pkg), pkg, version, theirBody)
	}
	return overlayRoot, &pruneRecordingProvider{
		root:     upstreamRoot,
		versions: map[string][]string{category + "/" + pkg: {version}},
	}
}

// shipUpstreamWaylandPatch writes, on ::gentoo's side, the patch
// pruneEbuildOurs references — which is what keeps this file's differing fixture
// an UNPROVED divergence.
//
// Without it the fixture proves its own authorship and R6.1 refuses the package
// outright: our ebuild applies files/wayland.patch and ::gentoo ships no such
// file, so the difference cannot have been inherited from ::gentoo. That row is
// prune_authorship_test.go's; the tests here are about the row below it, an
// undeclared divergence nothing attributes to anybody. Writing the patch upstream
// is the copied-ebuild shape that produces it — the reference came from ::gentoo
// along with the rest of the file, so it says nothing about who changed what.
//
// The ebuilds still differ, and PruneVerification reports that difference before
// it ever compares the two files/ trees, so the reason these tests assert on is
// unaffected.
func shipUpstreamWaylandPatch(t *testing.T, prov *pruneRecordingProvider) {
	t.Helper()
	writePruneFile(t,
		filepath.Join(prov.root, pruneCat, prunePkg),
		filepath.Join(pruneFilesDir, "wayland.patch"),
		"upstream patch\n")
}

// onlyPlan fails unless exactly one plan is in the bucket, and returns it.
func onlyPlan(t *testing.T, bucket []PrunePlan, name string) PrunePlan {
	t.Helper()
	if len(bucket) != 1 {
		t.Fatalf("%s holds %d plans, want exactly 1: %+v", name, len(bucket), bucket)
	}
	return bucket[0]
}

// TestPlanPruneOnlyRedundantIsConsidered pins step 1 of the order: the verdict
// filters, and only `redundant` survives it.
//
// All four packages here have byte-identical content, so content alone would
// make every one of them removable. Three are refused anyway, each with a reason
// naming its verdict — because a `keep` package is one the overlay is carrying on
// purpose, and "identical to ::gentoo right now" is not an argument against that.
//
// _Requirements: R2.1, R2.5_
func TestPlanPruneOnlyRedundantIsConsidered(t *testing.T) {
	overlayRoot, upstreamRoot := t.TempDir(), t.TempDir()

	type pkgCase struct {
		name    string
		verdict Verdict
	}
	cases := []pkgCase{
		{"redundant-pkg", VerdictRedundant},
		{"keep-pkg", VerdictKeep},
		{"rebase-pkg", VerdictNeedsRebase},
		{"unknown-pkg", VerdictUnknown},
	}

	results := make([]CompareResult, 0, len(cases))
	versions := make(map[string][]string, len(cases))
	for _, c := range cases {
		writePruneEbuild(t, filepath.Join(overlayRoot, pruneCat, c.name), c.name, "1.0", pruneEbuildStock)
		writePruneEbuild(t, filepath.Join(upstreamRoot, pruneCat, c.name), c.name, "1.0", pruneEbuildStock)
		results = append(results, planResult(pruneCat, c.name, "1.0", c.verdict))
		versions[pruneCat+"/"+c.name] = []string{"1.0"}
	}
	prov := &pruneRecordingProvider{root: upstreamRoot, versions: versions}

	batch := PlanPrune(results, prov, PruneOptions{OverlayPath: overlayRoot})

	got := onlyPlan(t, batch.Identical, "Identical")
	if got.Package != "redundant-pkg" {
		t.Errorf("Identical holds %q, want redundant-pkg", got.Package)
	}
	if !got.Eligible {
		t.Error("the identical package is not Eligible; identical content under a redundant verdict is exactly the case a prune exists for")
	}
	if got.Class != PruneIdentical {
		t.Errorf("Class = %v, want PruneIdentical", got.Class)
	}

	if len(batch.Refused) != 3 {
		t.Fatalf("Refused holds %d plans, want 3 (keep, needs-rebase, unknown): %+v", len(batch.Refused), batch.Refused)
	}
	for _, p := range batch.Refused {
		if p.Eligible {
			t.Errorf("%s is Eligible while refused; only a redundant verdict may reach a removal", p.Package)
		}
		if p.Reason == "" {
			t.Errorf("%s is refused with no reason; a plan that omits why reads as 'this package does not exist' and the operator's next question is always 'why not this one'", p.Package)
		}
	}
	if len(batch.Diverging) != 0 {
		t.Errorf("Diverging holds %d plans, want 0; nothing here declares a patch or differs", len(batch.Diverging))
	}
}

// TestPlanPruneKeepAndUnknownAreNeverCompared proves R2.5 as an ABSENCE OF WORK,
// not as a message.
//
// The provider counts every request for a package directory, which is the only
// thing a content comparison could ask it for. A non-redundant package must
// produce none. The redundant sub-test is the positive control: without it, a
// "zero calls" assertion would also pass for an implementation that never
// compares anything at all.
//
// This is a cost property as well as a correctness one — 240 of the overlay's
// 314 packages are not redundant, and reading all of them would be most of the
// work of a scan that has no use for the answer.
//
// _Requirements: R2.5_
func TestPlanPruneKeepAndUnknownAreNeverCompared(t *testing.T) {
	build := func(t *testing.T, verdict Verdict) *pruneRecordingProvider {
		t.Helper()
		overlayRoot, prov := planTrees(t, pruneCat, prunePkg, "1.0", pruneEbuildStock, pruneEbuildStock)
		PlanPrune(
			[]CompareResult{planResult(pruneCat, prunePkg, "1.0", verdict)},
			prov,
			PruneOptions{OverlayPath: overlayRoot},
		)
		return prov
	}

	for _, verdict := range []Verdict{VerdictKeep, VerdictNeedsRebase, VerdictUnknown} {
		t.Run(verdict.String()+" is never read", func(t *testing.T) {
			prov := build(t, verdict)
			if len(prov.dirCalls) != 0 {
				t.Errorf("the provider was asked for %v, but a %s verdict refuses the package before content is consulted (R2.5)", prov.dirCalls, verdict)
			}
		})
	}

	t.Run("redundant is read (positive control)", func(t *testing.T) {
		prov := build(t, VerdictRedundant)
		if len(prov.dirCalls) == 0 {
			t.Fatal("the provider was never asked for the redundant package's directory, so the 'never compared' assertions above prove nothing")
		}
	})
}

// TestPlanPrunePatchedGoesToDivergingEvenWhenIdentical pins step 2: the
// declaration outranks the bytes (R2.4).
//
// Content here is byte-identical, so content alone would clear the package for
// removal. It is still Diverging, because a `patched` entry is a human saying
// "this copy exists for a reason". If the reason has since been fixed upstream
// the declaration is stale — and 025 already prints that finding, which is how
// the operator learns to clear it. Deleting the package on our own reading of
// the bytes would resolve that disagreement in the one direction that cannot be
// undone.
//
// It is not Eligible without --include-patched, and it IS with it: the flag is
// the operator overruling their own declaration, which is a thing they are
// allowed to do out loud.
//
// _Requirements: R2.4, R4.1_
func TestPlanPrunePatchedGoesToDivergingEvenWhenIdentical(t *testing.T) {
	newCase := func(t *testing.T) (string, *pruneRecordingProvider, CompareResult) {
		t.Helper()
		overlayRoot, prov := planTrees(t, pruneCat, prunePkg, "1.0", pruneEbuildStock, pruneEbuildStock)
		res := planResult(pruneCat, prunePkg, "1.0", VerdictRedundant)
		res.Patched = true
		res.PatchedBy = "app-editors/zed@stable"
		res.PatchedReason = "keeps our wayland patch"
		return overlayRoot, prov, res
	}

	t.Run("without --include-patched it is planned but not eligible", func(t *testing.T) {
		overlayRoot, prov, res := newCase(t)

		batch := PlanPrune([]CompareResult{res}, prov, PruneOptions{OverlayPath: overlayRoot})

		if len(batch.Identical) != 0 {
			t.Errorf("Identical holds %+v; a declared patch vetoes removal regardless of what the bytes say (R2.4)", batch.Identical)
		}
		got := onlyPlan(t, batch.Diverging, "Diverging")
		if got.Class != PruneDiverging {
			t.Errorf("Class = %v, want PruneDiverging", got.Class)
		}
		if got.Eligible {
			t.Error("the package is Eligible without --include-patched; the default batch must contain nothing whose removal discards declared work")
		}
		if got.Reason == "" {
			t.Error("Reason is empty; the plan prints this package and the operator has to be told why it is set apart")
		}
	})

	t.Run("with --include-patched it becomes eligible", func(t *testing.T) {
		overlayRoot, prov, res := newCase(t)

		batch := PlanPrune([]CompareResult{res}, prov, PruneOptions{OverlayPath: overlayRoot, IncludePatched: true})

		got := onlyPlan(t, batch.Diverging, "Diverging")
		if got.Class != PruneDiverging {
			t.Errorf("Class = %v, want PruneDiverging; the flag changes what may be done about the finding, not the finding", got.Class)
		}
		if !got.Eligible {
			t.Error("the package is still not Eligible under --include-patched, so the flag does nothing and R4.1 is unimplemented")
		}
	})
}

// TestPlanPruneDifferingContentIsRefusedWithReason is the case the story exists
// for: the verdict says redundant and the bytes disagree.
//
// Measured on the live overlay, this is 8 of 74 redundant packages — kwin,
// plasma-desktop and nodejs among them — every one already reported by 025 as an
// undeclared divergence. Following the verdict alone deletes local work that
// nobody wrote a declaration for, which is precisely the work most at risk of
// being forgotten.
//
// It is not eligible by default and the Reason names the differing version, so
// the operator can go and look at the file rather than diffing a directory.
//
// _Requirements: R2.2, R4.1, R4.2_
func TestPlanPruneDifferingContentIsRefusedWithReason(t *testing.T) {
	overlayRoot, prov := planTrees(t, pruneCat, prunePkg, "1.0", pruneEbuildOurs, pruneEbuildStock)
	shipUpstreamWaylandPatch(t, prov)
	res := planResult(pruneCat, prunePkg, "1.0", VerdictRedundant)

	batch := PlanPrune([]CompareResult{res}, prov, PruneOptions{OverlayPath: overlayRoot})

	if len(batch.Identical) != 0 {
		t.Fatalf("Identical holds %+v; the overlay copy carries a local change and removing it would discard that change", batch.Identical)
	}
	got := onlyPlan(t, batch.Diverging, "Diverging")
	if got.Eligible {
		t.Error("a package with undeclared local changes is Eligible by default; that is the exact removal the story exists to prevent")
	}
	if !strings.Contains(got.Reason, "1.0") {
		t.Errorf("Reason = %q, want it to name the differing version 1.0", got.Reason)
	}

	t.Run("--include-patched makes it eligible", func(t *testing.T) {
		overlayRoot, prov := planTrees(t, pruneCat, prunePkg, "1.0", pruneEbuildOurs, pruneEbuildStock)
		shipUpstreamWaylandPatch(t, prov)

		batch := PlanPrune([]CompareResult{res}, prov, PruneOptions{OverlayPath: overlayRoot, IncludePatched: true})

		got := onlyPlan(t, batch.Diverging, "Diverging")
		if !got.Eligible {
			t.Error("--include-patched leaves an undeclared divergence ineligible; R4.1 names R2.2 alongside R2.4")
		}
	})
}

// TestPlanPruneUnverifiableStatesWhichReason pins the row no flag rescues.
//
// The upstream tree holds a version we do not, so there is nothing to line up
// and compare. That is not a difference and not a match — it is an absence of
// evidence, and R4.1 conspicuously does not list R2.3 among the things
// --include-patched may override. A flag can say "I know I am discarding local
// work". No flag can say "I know I am discarding work nobody could look at".
//
// _Requirements: R2.3, R3.3, R1.5_
func TestPlanPruneUnverifiableStatesWhichReason(t *testing.T) {
	overlayRoot, upstreamRoot := t.TempDir(), t.TempDir()
	writePruneEbuild(t, filepath.Join(overlayRoot, pruneCat, prunePkg), prunePkg, "1.0", pruneEbuildStock)
	writePruneEbuild(t, filepath.Join(upstreamRoot, pruneCat, prunePkg), prunePkg, "2.0", pruneEbuildStock)
	prov := &pruneRecordingProvider{
		root:     upstreamRoot,
		versions: map[string][]string{pruneCat + "/" + prunePkg: {"2.0"}},
	}
	res := planResult(pruneCat, prunePkg, "1.0", VerdictRedundant)

	for _, includePatched := range []bool{false, true} {
		batch := PlanPrune([]CompareResult{res}, prov, PruneOptions{
			OverlayPath:    overlayRoot,
			IncludePatched: includePatched,
		})

		got := onlyPlan(t, batch.Refused, "Refused")
		if got.Class != PruneRefused {
			t.Errorf("--include-patched=%v: Class = %v, want PruneRefused", includePatched, got.Class)
		}
		if got.Eligible {
			t.Errorf("--include-patched=%v: an unverifiable package is Eligible; no flag can authorise discarding work nobody could look at", includePatched)
		}
		if got.Reason != pruneNoVersionInCommon {
			t.Errorf("--include-patched=%v: Reason = %q, want %q; R2.3 asks which of the two unverifiable reasons applies", includePatched, got.Reason, pruneNoVersionInCommon)
		}
	}
}

// TestPlanPruneListsFilesAndRegistryKeys pins R1.4: the plan says exactly what
// would go, on both sides of the removal.
//
// Files is every path under the package directory, RELATIVE to it — the plan
// already prints category/package as the heading, so repeating it on every line
// would be noise. It includes Manifest and metadata.xml, which the COMPARISON
// deliberately ignores: they are not evidence about whether the copy is ours,
// but they are certainly files the removal deletes, and a plan that hid them
// would understate what it is about to do.
//
// Versions is every version in OUR directory, not the versions that were
// compared. Removing the directory removes all of them.
//
// RegistryKeys arrives from the caller and is not looked up here — internal/
// overlay never learns what TOML is, which is the layer rule 025 established and
// the reason this package has no import edge to internal/autoupdate.
//
// _Requirements: R1.4, R5.1, R5.2_
func TestPlanPruneListsFilesAndRegistryKeys(t *testing.T) {
	overlayRoot, upstreamRoot := t.TempDir(), t.TempDir()
	ourDir := filepath.Join(overlayRoot, pruneCat, prunePkg)
	theirDir := filepath.Join(upstreamRoot, pruneCat, prunePkg)

	writePruneEbuild(t, ourDir, prunePkg, "1.0", pruneEbuildStock)
	writePruneEbuild(t, ourDir, prunePkg, "2.0", pruneEbuildStock)
	writePruneEbuild(t, theirDir, prunePkg, "1.0", pruneEbuildStock)
	writePruneEbuild(t, theirDir, prunePkg, "2.0", pruneEbuildStock)
	writePruneFile(t, ourDir, "Manifest", "DIST zed-1.0.tar.gz 1 BLAKE2B aa SHA512 aa\n")
	writePruneFile(t, ourDir, "metadata.xml", "<pkgmetadata/>\n")
	writePruneFile(t, ourDir, filepath.Join("files", "shared.patch"), "same\n")
	writePruneFile(t, theirDir, filepath.Join("files", "shared.patch"), "same\n")

	prov := &pruneRecordingProvider{
		root:     upstreamRoot,
		versions: map[string][]string{pruneCat + "/" + prunePkg: {"1.0", "2.0"}},
	}
	atom := pruneCat + "/" + prunePkg
	wantKeys := []string{atom, atom + ":4.1", atom + "@stable"}

	batch := PlanPrune(
		[]CompareResult{planResult(pruneCat, prunePkg, "2.0", VerdictRedundant)},
		prov,
		PruneOptions{
			OverlayPath:  overlayRoot,
			RegistryKeys: map[string][]string{atom: wantKeys},
		},
	)

	got := onlyPlan(t, batch.Identical, "Identical")

	for _, want := range []string{
		prunePkg + "-1.0.ebuild",
		prunePkg + "-2.0.ebuild",
		"Manifest",
		"metadata.xml",
		filepath.Join("files", "shared.patch"),
	} {
		if !pruneListHas(got.Files, want) {
			t.Errorf("Files = %q, missing %q; R1.4 asks the plan to list every file the removal deletes, including the ones the comparison ignored", got.Files, want)
		}
	}

	for _, want := range []string{"1.0", "2.0"} {
		if !pruneListHas(got.Versions, want) {
			t.Errorf("Versions = %q, missing %q; the directory removal takes every version in it, not only the compared one", got.Versions, want)
		}
	}

	if len(got.RegistryKeys) != len(wantKeys) {
		t.Errorf("RegistryKeys = %q, want all %d keys %q; 90 of 321 atoms carry more than one entry and leaving one behind orphans it", got.RegistryKeys, len(wantKeys), wantKeys)
	}
	for _, want := range wantKeys {
		if !pruneListHas(got.RegistryKeys, want) {
			t.Errorf("RegistryKeys = %q, missing %q", got.RegistryKeys, want)
		}
	}
}

// TestPlanPruneEmptyInputYieldsEmptyBatch pins R1.5's floor: nothing in, nothing
// out, and no panic on the way.
//
// It is the shape a restricted run produces when its target matched nothing, and
// the caller has to be able to tell that apart from "everything was refused" —
// which it can, because all three buckets are empty rather than one of them
// holding a refusal.
//
// _Requirements: R1.5_
func TestPlanPruneEmptyInputYieldsEmptyBatch(t *testing.T) {
	prov := &pruneRecordingProvider{root: t.TempDir(), versions: map[string][]string{}}

	batch := PlanPrune(nil, prov, PruneOptions{OverlayPath: t.TempDir()})

	if len(batch.Identical) != 0 || len(batch.Diverging) != 0 || len(batch.Refused) != 0 {
		t.Errorf("batch = %+v, want every bucket empty", batch)
	}
	if len(prov.dirCalls) != 0 {
		t.Errorf("the provider was asked for %v with no results to plan", prov.dirCalls)
	}
}

// pruneListHas reports whether haystack holds needle.
//
// Named apart from rename_test.go's containsString, which is a SUBSTRING test
// over two strings — a different question with a confusingly similar name.
func pruneListHas(haystack []string, needle string) bool {
	for _, s := range haystack {
		if s == needle {
			return true
		}
	}
	return false
}
