package validate

// Authored for story 033, sub-task 2.1 — R3, R3.1, R3.7, R3.8.
//
// WHERE THE TREE MAY NOT LIVE, AND WHY THAT IS A TEST. `ScanOverlay` walks
// category/package two levels deep (run.go:64,107) and `planSweep`
// (sweep.go:404-422) enumerates every ebuild no registry pin claims. A staged
// ebuild parked anywhere under the overlay root is therefore an unclaimed
// ebuild, which `overlay autoupdate --clean` DELETES and `overlay validate`
// reports. The staging directory would be eaten by the very tool that created
// it, so "the staging root is never inside the overlay" is asserted rather than
// documented.
//
// WHY THE TREE MASTERS ONTO `gentoo` ALONE (design D2). `masters = bentoo`
// resolves — through repos.conf, to /var/db/repos/bentoo, the DEPLOYED copy.
// That copy syncs from origin and so lags the working tree by however long the
// auto-commit/push/sync cycle takes. Validating a new ebuild against a stale
// eclass is the same class of error as validating it against the wrong tarball,
// which story 031 already had to fix once. So the staged tree carries COPIES of
// eclass/ (32 KB) and profiles/ (56 KB) and declares `masters = gentoo`.
//
// WHY repo_name MUST DIFFER. The published repo's name is already registered
// with Portage and a duplicate is a repository-level conflict.
//
// Measured (design M-C): a staged skeleton plus one package is 24 KB, which is
// what makes R3.7's one-tree-per-package-version retention a non-question.
//
// This file pins `StageRequest{Overlay, StagingRoot, Atom, Version, EbuildBytes}`
// and `Stage(StageRequest) (string, error)`.
//
// hashTree comes from golden_test.go and is reused, never reimplemented.

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

const sourceRepoName = "bentoo"

// sourceOverlay lays out an overlay that looks like the real one where it
// matters: a repo_name, a layout.conf, an eclass the staged build must be able
// to inherit, a profiles file, and one package directory.
func sourceOverlay(t *testing.T) string {
	t.Helper()
	root := t.TempDir()

	write := func(rel, body string) {
		path := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatalf("laying out %q: %v", rel, err)
		}
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatalf("writing %q: %v", rel, err)
		}
	}

	write("profiles/repo_name", sourceRepoName+"\n")
	write("profiles/categories", "media-plugins\n")
	write("metadata/layout.conf", "masters = gentoo\nthin-manifests = true\nsign-manifests = false\nprofile-formats = portage-2\n")
	// The eclass that exists only in this overlay: if the staged tree cannot
	// resolve it, the gate cannot build the package that motivated the story.
	write("eclass/gstreamer-meson.eclass", "# @ECLASS: gstreamer-meson.eclass\n")
	write("media-plugins/gst-plugins-qt6/gst-plugins-qt6-1.28.6.ebuild", "EAPI=8\ninherit gstreamer-meson\n")
	write("media-plugins/gst-plugins-qt6/Manifest", "DIST gst-plugins-good-1.28.6.tar.xz 100 BLAKE2B ab SHA512 cd\n")

	return root
}

// stagedFor is the request every case starts from.
func stagedFor(t *testing.T, overlay, stagingRoot, body string) StageRequest {
	t.Helper()
	return StageRequest{
		Overlay:     overlay,
		StagingRoot: stagingRoot,
		Atom:        "media-plugins/gst-plugins-qt6",
		Version:     "1.29.2",
		EbuildBytes: []byte(body),
	}
}

// TestStage_TreeCarriesEverythingPortageNeeds is R3.8: the eclasses and profiles
// of the published overlay have to be resolvable from the staged tree, or the
// gate cannot build an ebuild that inherits one.
func TestStage_TreeCarriesEverythingPortageNeeds(t *testing.T) {
	overlay := sourceOverlay(t)
	stagingRoot := filepath.Join(t.TempDir(), "staging")

	const body = "EAPI=8\ninherit gstreamer-meson\n# 1.29.2 candidate\n"
	staged, err := Stage(stagedFor(t, overlay, stagingRoot, body))
	if err != nil {
		t.Fatalf("Stage: %v", err)
	}

	for _, rel := range []string{
		"profiles/repo_name",
		"metadata/layout.conf",
		"eclass/gstreamer-meson.eclass",
		"profiles/categories",
		filepath.Join("media-plugins", "gst-plugins-qt6", "gst-plugins-qt6-1.29.2.ebuild"),
	} {
		if _, err := os.Stat(filepath.Join(staged, rel)); err != nil {
			t.Errorf("the staged tree is missing %s: %v", rel, err)
		}
	}

	got, err := os.ReadFile(filepath.Join(staged, "media-plugins", "gst-plugins-qt6", "gst-plugins-qt6-1.29.2.ebuild"))
	if err != nil {
		t.Fatalf("reading the staged candidate: %v", err)
	}
	if string(got) != body {
		t.Errorf("the staged candidate is %q, want the bytes handed to Stage (%q)", got, body)
	}
}

// TestStage_RepoNameDiffersFromThePublishedOne is D2's first consequence: the
// published name is already registered with Portage and a duplicate is a
// repository-level conflict.
func TestStage_RepoNameDiffersFromThePublishedOne(t *testing.T) {
	overlay := sourceOverlay(t)
	staged, err := Stage(stagedFor(t, overlay, filepath.Join(t.TempDir(), "staging"), "EAPI=8\n"))
	if err != nil {
		t.Fatalf("Stage: %v", err)
	}

	raw, err := os.ReadFile(filepath.Join(staged, "profiles", "repo_name"))
	if err != nil {
		t.Fatalf("reading the staged repo_name: %v", err)
	}
	name := strings.TrimSpace(string(raw))

	if name == sourceRepoName {
		t.Errorf("the staged tree calls itself %q, the same name the published overlay is registered under; "+
			"a duplicate repo name is a Portage-level conflict", name)
	}
	if name == "" {
		t.Error("the staged tree has an empty repo_name")
	}
}

// TestStage_MastersOntoGentooAndNotTheDeployedCopy is D2's second consequence,
// and the one with teeth: `masters = bentoo` resolves to /var/db/repos/bentoo,
// which lags the tree being validated.
func TestStage_MastersOntoGentooAndNotTheDeployedCopy(t *testing.T) {
	overlay := sourceOverlay(t)
	staged, err := Stage(stagedFor(t, overlay, filepath.Join(t.TempDir(), "staging"), "EAPI=8\n"))
	if err != nil {
		t.Fatalf("Stage: %v", err)
	}

	raw, err := os.ReadFile(filepath.Join(staged, "metadata", "layout.conf"))
	if err != nil {
		t.Fatalf("reading the staged layout.conf: %v", err)
	}
	layout := string(raw)

	var masters string
	for _, line := range strings.Split(layout, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "masters") {
			masters = strings.TrimSpace(line)
		}
	}
	if masters == "" {
		t.Fatalf("the staged layout.conf declares no masters:\n%s", layout)
	}
	if !strings.Contains(masters, "gentoo") {
		t.Errorf("masters line %q does not master onto gentoo", masters)
	}
	if strings.Contains(masters, sourceRepoName) {
		t.Errorf("masters line %q resolves %q through repos.conf to the DEPLOYED copy, which lags the tree being validated (D2)",
			masters, sourceRepoName)
	}
	// The overlay's own layout properties travel with it, or the staged build
	// digests differently from the published one. thin-manifests is NOT among
	// them any more — see the dedicated test below.
	for _, want := range []string{"sign-manifests", "profile-formats"} {
		if !strings.Contains(layout, want) {
			t.Errorf("the staged layout.conf does not carry %s from the source overlay", want)
		}
	}
}

// TestStage_ThinManifestsIsImposedNotCarried is the one place the "validate
// under the published tree's own rules" principle is deliberately broken, and
// the reason is measurable in Portage's own source.
//
// Portage defaults thin-manifests to false. On a non-thin repository,
// digestcheck.py does more than verify distfile digests: past its thin/
// allow_missing early return it walks the package directory and requires every
// .ebuild to carry an EBUILD record in the Manifest, returning 0 under `strict`
// — which is in the default FEATURES and which doebuild.py passes. A staged
// tree's Manifest describes distfiles and nothing else by construction, so the
// candidate has no EBUILD record and the tree is refused before the first phase
// marker: every build gate SKIPPED, for a package that would have built.
//
// The three cases below are the three things an overlay can say, and all three
// must produce a thin staged tree — including the overlay that says false out
// loud, because the staged tree's shape, not the overlay's policy, is what makes
// the non-thin checks meaningless here.
func TestStage_ThinManifestsIsImposedNotCarried(t *testing.T) {
	for _, tc := range []struct{ name, sourceLayout string }{
		{"source says true", "masters = gentoo\nthin-manifests = true\n"},
		{"source says false", "masters = gentoo\nthin-manifests = false\n"},
		{"source says nothing", "masters = gentoo\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			overlay := sourceOverlay(t)
			if err := os.WriteFile(filepath.Join(overlay, "metadata", "layout.conf"), []byte(tc.sourceLayout), 0o644); err != nil {
				t.Fatalf("rewriting the source layout: %v", err)
			}

			staged, err := Stage(stagedFor(t, overlay, filepath.Join(t.TempDir(), "staging"), "EAPI=8\n"))
			if err != nil {
				t.Fatalf("Stage: %v", err)
			}
			raw, err := os.ReadFile(filepath.Join(staged, "metadata", "layout.conf"))
			if err != nil {
				t.Fatalf("reading the staged layout.conf: %v", err)
			}
			layout := string(raw)

			if !strings.Contains(layout, "thin-manifests = true") {
				t.Errorf("the staged layout.conf is not thin:\n%s\n\nOn a non-thin repo digestcheck refuses the "+
					"candidate for having no EBUILD record, and every build gate reports SKIPPED for an ebuild "+
					"that would have built", layout)
			}
			if strings.Contains(layout, "thin-manifests = false") {
				t.Errorf("the staged layout.conf carries thin-manifests = false:\n%s", layout)
			}
		})
	}
}

// TestStage_RestagingReplacesRatherThanAccumulates is R3.7. copyEbuild cannot be
// reused for this: it refuses an existing destination with ErrEbuildExists
// (applier.go:953), which is the exact opposite of the rule here and would
// hard-fail on the second staging of the same bump.
func TestStage_RestagingReplacesRatherThanAccumulates(t *testing.T) {
	overlay := sourceOverlay(t)
	stagingRoot := filepath.Join(t.TempDir(), "staging")

	first, err := Stage(stagedFor(t, overlay, stagingRoot, "EAPI=8\n# first attempt\n"))
	if err != nil {
		t.Fatalf("first Stage: %v", err)
	}
	second, err := Stage(stagedFor(t, overlay, stagingRoot, "EAPI=8\n# second attempt\n"))
	if err != nil {
		t.Fatalf("second Stage: %v — restaging the same package and version must replace, not refuse", err)
	}

	if first != second {
		t.Errorf("the two stagings of the same package and version returned different trees (%q, %q); "+
			"retention is one tree per package-version and the path itself expresses it", first, second)
	}

	// Exactly one tree under <stagingRoot>/<category>/<package>.
	versions := filepath.Join(stagingRoot, "media-plugins", "gst-plugins-qt6")
	entries, err := os.ReadDir(versions)
	if err != nil {
		t.Fatalf("reading %q: %v", versions, err)
	}
	if len(entries) != 1 {
		var names []string
		for _, e := range entries {
			names = append(names, e.Name())
		}
		t.Errorf("staging the same bump twice left %d trees (%v), want exactly 1", len(entries), names)
	}

	got, err := os.ReadFile(filepath.Join(second, "media-plugins", "gst-plugins-qt6", "gst-plugins-qt6-1.29.2.ebuild"))
	if err != nil {
		t.Fatalf("reading the restaged candidate: %v", err)
	}
	if !strings.Contains(string(got), "second attempt") {
		t.Errorf("the restaged candidate holds %q; the second call's bytes must win", got)
	}
}

// TestStage_NeverPlacesTheTreeInsideTheOverlay is the deletion hazard, asserted.
func TestStage_NeverPlacesTheTreeInsideTheOverlay(t *testing.T) {
	overlay := sourceOverlay(t)
	staged, err := Stage(stagedFor(t, overlay, filepath.Join(t.TempDir(), "staging"), "EAPI=8\n"))
	if err != nil {
		t.Fatalf("Stage: %v", err)
	}

	stagedAbs, err := filepath.Abs(staged)
	if err != nil {
		t.Fatalf("resolving %q: %v", staged, err)
	}
	overlayAbs, err := filepath.Abs(overlay)
	if err != nil {
		t.Fatalf("resolving %q: %v", overlay, err)
	}
	if strings.HasPrefix(stagedAbs, overlayAbs+string(os.PathSeparator)) || stagedAbs == overlayAbs {
		t.Errorf("the staged tree %q is inside the published overlay %q; ScanOverlay would see an unclaimed ebuild and --clean would delete it",
			stagedAbs, overlayAbs)
	}
}

// TestStage_LeavesTheSourceOverlayByteIdentical is R3.1 read strictly. Staging
// COPIES out of the overlay; nothing it does may write back into the tree that
// publishes itself.
func TestStage_LeavesTheSourceOverlayByteIdentical(t *testing.T) {
	overlay := sourceOverlay(t)
	before := hashTree(t, overlay)

	if _, err := Stage(stagedFor(t, overlay, filepath.Join(t.TempDir(), "staging"), "EAPI=8\n# candidate\n")); err != nil {
		t.Fatalf("Stage: %v", err)
	}

	if after := hashTree(t, overlay); after != before {
		t.Errorf("staging modified the published overlay: %s -> %s", before, after)
	}
	// Specifically: the candidate must not appear in the overlay's own package
	// directory, which is where copyEbuild would have put it.
	candidate := filepath.Join(overlay, "media-plugins", "gst-plugins-qt6", "gst-plugins-qt6-1.29.2.ebuild")
	if _, err := os.Stat(candidate); err == nil {
		t.Errorf("staging wrote the candidate into the published overlay at %q", candidate)
	}
}

// TestStage_DirectoriesAreNotWorldReadable pins the mode the applier already
// uses for logs/ (applier.go:471). The staged tree holds an unreviewed candidate
// and, during a fix, whatever an agent wrote into it.
func TestStage_DirectoriesAreNotWorldReadable(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("running as root: umask and ownership assertions pass for the wrong reason")
	}
	overlay := sourceOverlay(t)
	staged, err := Stage(stagedFor(t, overlay, filepath.Join(t.TempDir(), "staging"), "EAPI=8\n"))
	if err != nil {
		t.Fatalf("Stage: %v", err)
	}

	info, err := os.Stat(staged)
	if err != nil {
		t.Fatalf("stat %q: %v", staged, err)
	}
	if perm := info.Mode().Perm(); perm != 0o750 {
		t.Errorf("the staged tree is mode %04o, want 0750 — the mode the applier already uses for logs/", perm)
	}
}

// unwritableStagingRoot returns a directory nothing may create inside, and
// restores its mode afterwards so t.TempDir's own cleanup can remove it.
func unwritableStagingRoot(t *testing.T) string {
	t.Helper()
	if os.Geteuid() == 0 {
		t.Skip("running as root: an unwritable directory is writable to root, so this would pass for the wrong reason (this is also why it is never validated under `act`)")
	}
	root := filepath.Join(t.TempDir(), "staging")
	if err := os.MkdirAll(root, 0o750); err != nil {
		t.Fatalf("creating the staging root: %v", err)
	}
	if err := os.Chmod(root, 0o500); err != nil {
		t.Fatalf("chmod: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(root, 0o750) })
	return root
}

// TestStage_UnwritableRootIsATypedErrorNamingTheRoot is the first half of R3.9:
// the failure is recognisable by type and readable by a human. Both, because the
// caller matches on the type and the operator reads the string.
func TestStage_UnwritableRootIsATypedErrorNamingTheRoot(t *testing.T) {
	overlay := sourceOverlay(t)
	root := unwritableStagingRoot(t)

	staged, err := Stage(stagedFor(t, overlay, root, "EAPI=8\n"))

	if err == nil {
		t.Fatalf("Stage returned %q and no error against an unwritable staging root; "+
			"a tree that was never built must not read as one that was", staged)
	}
	if !errors.Is(err, ErrStageUnpreparable) {
		t.Errorf("the error %v does not wrap ErrStageUnpreparable; the caller would have to match on message text to recognise it", err)
	}
	if !strings.Contains(err.Error(), root) {
		t.Errorf("the error %q does not name the staging root it could not prepare (%q)", err, root)
	}
	if staged != "" {
		t.Errorf("Stage returned the path %q alongside its error; a half-made tree handed back is a tree somebody will use", staged)
	}
}

// TestStage_UnpreparableTreeAttemptsNoBuild is the second half, and the one
// worth watching a seam for: with no tree there is nothing to build FROM, so an
// `ebuild` invocation here would run against whatever path was passed — in the
// worst case the published overlay itself.
func TestStage_UnpreparableTreeAttemptsNoBuild(t *testing.T) {
	overlay := sourceOverlay(t)
	root := unwritableStagingRoot(t)

	var spawned []string
	orig := execCommand
	execCommand = func(ctx context.Context, name string, arg ...string) *exec.Cmd {
		spawned = append(spawned, name+" "+strings.Join(arg, " "))
		return exec.CommandContext(ctx, "true")
	}
	t.Cleanup(func() { execCommand = orig })

	if _, err := Stage(stagedFor(t, overlay, root, "EAPI=8\n")); err == nil {
		t.Fatal("Stage succeeded against an unwritable staging root")
	}

	if len(spawned) != 0 {
		t.Errorf("a staging failure spawned %v; R3.9 asks for no build at all", spawned)
	}
}

// TestStage_UnpreparableTreeIsNotAPanic pins the third outcome nobody asks for.
// A sweep over the whole registry must survive one package's unpreparable tree,
// and a panic takes the other forty with it.
func TestStage_UnpreparableTreeIsNotAPanic(t *testing.T) {
	overlay := sourceOverlay(t)
	root := unwritableStagingRoot(t)

	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("Stage panicked on an unpreparable tree (%v); one package's failure must not end the sweep", r)
		}
	}()

	if _, err := Stage(stagedFor(t, overlay, root, "EAPI=8\n")); err == nil {
		t.Fatal("Stage succeeded against an unwritable staging root")
	}
}

// TestStage_UnpreparableTreeLeavesTheOverlayAlone keeps the failure path inside
// the same promise as the success path: nothing is written to the tree that
// publishes itself, including when staging gives up.
func TestStage_UnpreparableTreeLeavesTheOverlayAlone(t *testing.T) {
	overlay := sourceOverlay(t)
	root := unwritableStagingRoot(t)
	before := hashTree(t, overlay)

	if _, err := Stage(stagedFor(t, overlay, root, "EAPI=8\n")); err == nil {
		t.Fatal("Stage succeeded against an unwritable staging root")
	}

	if after := hashTree(t, overlay); after != before {
		t.Errorf("a failed staging modified the published overlay: %s -> %s", before, after)
	}
}

// TestStage_UnpreparableTreeSkipsEveryBuildGate is what the caller does with the
// typed error, and it is the assertion R3.9 is actually about: every gate above
// `options` reports SKIPPED, each carrying the reason, and the static gates are
// untouched because they never needed a staged tree.
//
// See the ordering note in this file's header: this case needs GateResult
// (sub-task 5.1) and Depth (sub-task 1.2).
func TestStage_UnpreparableTreeSkipsEveryBuildGate(t *testing.T) {
	const reason = "the staged tree could not be prepared: /var/lib/bentoo/staging: permission denied"

	gates := SkippedGates(DepthCompile, reason)

	if len(gates) == 0 {
		t.Fatal("skippedGates produced no gates; a depth that could not be reached still has to report the gates it covers")
	}

	want := map[string]bool{GatePatches: false, GateConfigure: false, GateCompile: false}
	for _, g := range gates {
		if g.Gate == GateOptions || g.Gate == GateQA {
			t.Errorf("the static %s gate was reported as skipped by a STAGING failure; it never needed a staged tree", g.Gate)
			continue
		}
		if _, expected := want[g.Gate]; !expected {
			t.Errorf("unexpected gate %q in a staging-failure result", g.Gate)
			continue
		}
		want[g.Gate] = true

		if g.Outcome != OutcomeSkipped {
			t.Errorf("%s gate: got %q, want SKIPPED — the gate did not run, so it cannot have passed or failed", g.Gate, g.Outcome)
		}
		if g.Reason == "" {
			t.Errorf("%s gate reports SKIPPED with no reason; a skip nobody can read is a pass", g.Gate)
		}
		if !strings.Contains(g.Reason, "staged tree") {
			t.Errorf("%s gate's reason %q does not say the staged tree was the problem", g.Gate, g.Reason)
		}
		if len(g.Findings) != 0 {
			t.Errorf("%s gate carries %d finding(s) from a gate that never ran", g.Gate, len(g.Findings))
		}
	}
	for gate, seen := range want {
		if !seen {
			t.Errorf("the %s gate is missing from the result; an unreported gate is indistinguishable from one that passed", gate)
		}
	}

	// A shallower request reports fewer gates: nothing above the selected depth
	// is claimed, not even as a skip.
	shallow := SkippedGates(DepthPatches, reason)
	for _, g := range shallow {
		if g.Gate == GateConfigure || g.Gate == GateCompile {
			t.Errorf("a patches-depth request reported the %s gate; a depth reports the gates it covers and no others", g.Gate)
		}
	}
}

// TestStage_UnpreparableTreeIsNeverPromoted is R3.10, and it closes a hole the
// two rules either side of it composed into.
//
// R3.3 promotes a candidate once every gate up to the selected depth reports
// PASS **or SKIPPED**. R3.9 turns a staging failure into SKIPPED for every gate
// above `options`. Read together and with nothing between them, a bump whose
// staged tree could not be built satisfies R3.3 VACUOUSLY — every build gate
// skipped, therefore nothing failed, therefore publish — and the overlay that
// auto-commits and pushes receives a candidate no gate ever looked at. R3.4
// forbids exactly that from the other direction: what is published is what was
// validated, and here nothing was.
//
// So a staging failure is not merely a skip; it withdraws the bump from
// promotion. The decision is a pure function of the gates and the staging error,
// which is what lets it be asserted here rather than only through the applier.
//
// This case pins `PromotionDecision(gates []GateResult, stageErr error) (bool, string)`.
// Its composition with the real Apply — overlay hash unchanged, no pin written —
// is asserted in internal/autoupdate/applier_gates_test.go (sub-task 12.1),
// because promotion itself lives on the other side of the import edge.
func TestStage_UnpreparableTreeIsNeverPromoted(t *testing.T) {
	const reason = "the staged tree could not be prepared: /var/lib/bentoo/staging: permission denied"
	stageErr := fmt.Errorf("%w: %s", ErrStageUnpreparable, "/var/lib/bentoo/staging: permission denied")

	promote, why := PromotionDecision(SkippedGates(DepthCompile, reason), stageErr)

	if promote {
		t.Fatal("a bump whose staged tree could not be prepared was cleared for promotion; every build gate SKIPPED satisfies " +
			"R3.3's \"PASS or SKIPPED\" vacuously, and the published overlay would receive a candidate no gate ever read (R3.10)")
	}
	if why == "" {
		t.Error("the refusal carries no reason; the operator sees a bump that did not publish and no statement of why")
	}
	if !strings.Contains(why, "staged tree") {
		t.Errorf("the refusal %q does not say the staged tree was the problem", why)
	}
}

// TestPromotionDecision_SkippedGatesStillPromoteWhenStagingSucceeded is the
// other side of the same function, and it is what stops R3.10 from being
// over-read into "any skip blocks promotion". A gate skipped because this host
// cannot answer — unsatisfied dependencies, no privilege — is exactly what R3.3
// means to allow, and R3.12 requires that bump to be promoted with its unreached
// depth named. Only a STAGING failure withdraws the bump.
func TestPromotionDecision_SkippedGatesStillPromoteWhenStagingSucceeded(t *testing.T) {
	gates := []GateResult{
		{Gate: GateOptions, Outcome: OutcomePass},
		{Gate: GatePatches, Outcome: OutcomeSkipped, Reason: "dev-libs/unsatisfied-1.0 is not installed"},
		{Gate: GateConfigure, Outcome: OutcomeSkipped, Reason: "dev-libs/unsatisfied-1.0 is not installed"},
	}

	promote, why := PromotionDecision(gates, nil)

	if !promote {
		t.Errorf("a bump whose build gates were skipped for want of an installed dependency was refused (%q); "+
			"that is the case R3.3 exists to allow, and R3.12 promotes it with the unreached depth named", why)
	}
}

// TestPromotionDecision_AFailedGateBlocksPromotion keeps the obvious case
// nailed down beside the subtle ones, since all three now go through one
// function.
func TestPromotionDecision_AFailedGateBlocksPromotion(t *testing.T) {
	gates := []GateResult{
		{Gate: GateOptions, Outcome: OutcomePass},
		{Gate: GateConfigure, Outcome: OutcomeFailed, Findings: []Finding{{
			Gate: GateConfigure, Severity: SeverityError, Detail: `meson.build:1:0: ERROR: Unknown option: "aalib".`,
		}}},
	}

	promote, why := PromotionDecision(gates, nil)

	if promote {
		t.Fatal("a bump whose configure gate FAILED was cleared for promotion")
	}
	if !strings.Contains(why, "configure") {
		t.Errorf("the refusal %q does not name the gate that failed", why)
	}
}

// TestStage_CarriesThePackageFilesDirectory closes the gap sub-task 7.1 made
// visible. R3.8 is written about eclasses and profiles because those are the
// repository-level resources; ${FILESDIR} is the package-level one, and the
// patch gate cannot tell the difference.
//
// Before this, an ebuild carrying PATCHES=( "${FILESDIR}"/foo.patch ) staged
// without its files/ died in src_prepare — and RunBuildGates attributes a
// failure to the last phase that started, so the patches gate reported FAILED.
// A confident false failure, on exactly the class of package (patched bumps)
// the gate exists for.
func TestStage_CarriesThePackageFilesDirectory(t *testing.T) {
	overlay := sourceOverlay(t)
	patch := filepath.Join(overlay, "media-plugins", "gst-plugins-qt6", "files", "qt6-detection.patch")
	if err := os.MkdirAll(filepath.Dir(patch), 0o755); err != nil {
		t.Fatalf("laying out files/: %v", err)
	}
	const body = "--- a/meson.build\n+++ b/meson.build\n"
	if err := os.WriteFile(patch, []byte(body), 0o644); err != nil {
		t.Fatalf("writing the patch: %v", err)
	}

	staged, err := Stage(stagedFor(t, overlay, filepath.Join(t.TempDir(), "staging"), "EAPI=8\n"))
	if err != nil {
		t.Fatalf("Stage: %v", err)
	}

	got, err := os.ReadFile(filepath.Join(staged, "media-plugins", "gst-plugins-qt6", "files", "qt6-detection.patch"))
	if err != nil {
		t.Fatalf("the staged tree cannot resolve ${FILESDIR}: %v; src_prepare would die and the patches gate would report FAILED for a bump that is fine", err)
	}
	if string(got) != body {
		t.Errorf("the staged patch is %q, want the overlay's bytes (%q)", got, body)
	}
}

// TestStage_AbsentFilesDirectoryIsNotAnError keeps the common case common: most
// ebuilds carry no patches at all, and staging one must not start failing.
func TestStage_AbsentFilesDirectoryIsNotAnError(t *testing.T) {
	overlay := sourceOverlay(t)

	staged, err := Stage(stagedFor(t, overlay, filepath.Join(t.TempDir(), "staging"), "EAPI=8\n"))
	if err != nil {
		t.Fatalf("Stage: %v — an overlay whose package has no files/ is the ordinary case", err)
	}
	if _, err := os.Stat(filepath.Join(staged, "media-plugins", "gst-plugins-qt6", "files")); err == nil {
		t.Error("staging invented a files/ directory the overlay does not have")
	}
}

// TestCopyRegularFile_CarriesBytesAndPermissions pins the contract every staged
// file depends on. It also anchors the close-error path: copyRegularFile reports
// the close failure through its named return, so a copy that silently truncated
// on flush stops being a success. That path is not injectable from a test
// without a seam through os.File, so it is held by mutation instead — replacing
// the deferred Close with a failing one must turn this case red.
func TestCopyRegularFile_CarriesBytesAndPermissions(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "src.ebuild")
	const body = "EAPI=8\ninherit gstreamer-meson\n"
	if err := os.WriteFile(src, []byte(body), 0o640); err != nil {
		t.Fatalf("writing the source: %v", err)
	}

	dst := filepath.Join(dir, "dst.ebuild")
	if err := copyRegularFile(src, dst); err != nil {
		t.Fatalf("copyRegularFile: %v", err)
	}

	got, err := os.ReadFile(dst)
	if err != nil {
		t.Fatalf("reading the copy: %v", err)
	}
	if string(got) != body {
		t.Errorf("the copy holds %q, want the source's bytes (%q)", got, body)
	}
	info, err := os.Stat(dst)
	if err != nil {
		t.Fatalf("stating the copy: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o640 {
		t.Errorf("the copy is mode %o, want the source's %o — Portage reads permissions off the staged tree", perm, 0o640)
	}
}

// TestCopyRegularFile_ExistingDestinationIsRefused holds the O_EXCL the doc
// comment promises: the staged tree is rebuilt from scratch, so two sources
// claiming one path is a bug worth surfacing rather than a last-writer-wins.
func TestCopyRegularFile_ExistingDestinationIsRefused(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "src.ebuild")
	if err := os.WriteFile(src, []byte("EAPI=8\n"), 0o644); err != nil {
		t.Fatalf("writing the source: %v", err)
	}
	dst := filepath.Join(dir, "dst.ebuild")
	const occupant = "written by whoever got here first\n"
	if err := os.WriteFile(dst, []byte(occupant), 0o644); err != nil {
		t.Fatalf("occupying the destination: %v", err)
	}

	err := copyRegularFile(src, dst)
	if err == nil {
		t.Fatal("copyRegularFile overwrote an existing destination; two sources claiming one staged path must be an error")
	}
	if !errors.Is(err, os.ErrExist) {
		t.Errorf("copyRegularFile returned %v, want an error wrapping os.ErrExist", err)
	}
	got, err := os.ReadFile(dst)
	if err != nil {
		t.Fatalf("reading the destination: %v", err)
	}
	if string(got) != occupant {
		t.Errorf("the refused copy still rewrote the destination to %q", got)
	}
}
