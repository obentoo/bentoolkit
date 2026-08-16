package autoupdate

// Authored for story 033, sub-task 4.1 — R3, R3.2, R3.3, R3.4, R3.5.
//
// THE STORY'S CENTRAL CLAIM, ASSERTED RATHER THAN ARGUED: after a run in which
// every bump failed, the published overlay is byte-identical to what it was
// before the run. The overlay this toolkit writes to auto-commits and pushes, so
// "we roll it back afterwards" is not the same promise as "it was never there".
//
// Two hashes, not one. Hashing only before and after would pass TODAY by
// accident: copyEbuild writes the candidate into the overlay and the deferred
// orphan rollback (applier.go:633-646) removes it again, so the endpoints match
// while the overlay really did hold an unvalidated ebuild for the whole manifest
// and gate run. So the tree is ALSO hashed at the instant every child process is
// spawned — that is when a gate is running, which is exactly what R3.2 speaks
// about — and that is the assertion the current write order fails.
//
// This file pins three names design.md fixes in prose but not in code:
// the option `WithApplierStagingRoot`, the result field `ApplyResult.StagedPath`,
// and the rule that a bump which did not pass never reaches setVersionsFn.
//
// createTestEbuildFile, mockExecCommandSuccess and containsArg come from
// applier_test.go and applier_isolation_test.go; NewPendingList/PendingUpdate
// from pending.go. Nothing here is redeclared.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/obentoo/bentoolkit/internal/autoupdate/validate"
)

// hashOverlayTree returns one digest over every path and file body under root,
// so any creation, deletion or edit moves it. Named for the overlay rather than
// generically, because the point of the assertion is WHICH tree is being held
// still.
func hashOverlayTree(t *testing.T, root string) string {
	t.Helper()
	var paths []string
	err := filepath.WalkDir(root, func(path string, _ fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		paths = append(paths, path)
		return nil
	})
	if err != nil {
		t.Fatalf("walking %q: %v", root, err)
	}
	sort.Strings(paths)

	h := sha256.New()
	for _, p := range paths {
		h.Write([]byte(p))
		info, err := os.Stat(p)
		if err != nil {
			t.Fatalf("stat %q: %v", p, err)
		}
		if info.IsDir() {
			continue
		}
		data, err := os.ReadFile(p)
		if err != nil {
			t.Fatalf("reading %q: %v", p, err)
		}
		h.Write(data)
	}
	return hex.EncodeToString(h.Sum(nil))
}

// promoteFixture lays out an overlay holding 1.28.6, a pending bump to 1.29.2
// and an applier whose staging root is outside the overlay. Every child process
// FAILS, which is the run this test is about: one in which every bump failed.
//
// The returned watcher records the overlay's hash at the moment each child was
// spawned, plus whether the candidate ebuild was visible in the published tree
// at that instant.
type promoteWatch struct {
	spawns          int
	hashesAtSpawn   []string
	candidateSeenAt []string
}

func promoteFixture(t *testing.T, opts ...ApplierOption) (a *Applier, overlayDir, pkg string, watch *promoteWatch, pins map[string]string) {
	t.Helper()
	tmp := t.TempDir()
	overlayDir = filepath.Join(tmp, "overlay")
	configDir := filepath.Join(tmp, "config")
	stagingRoot := filepath.Join(tmp, "staging")
	pkg = "media-plugins/gst-plugins-qt6"

	createTestEbuildFile(t, overlayDir, pkg, "1.28.6")
	writePublishedManifest(t, overlayDir, publishedManifestBody)

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

	watch = &promoteWatch{}
	candidate := filepath.Join(overlayDir, "media-plugins", "gst-plugins-qt6", "gst-plugins-qt6-1.29.2.ebuild")

	// Every child fails, and every child is observed. `false` is used rather
	// than a scripted seam because WHAT the gate printed does not matter here —
	// only that the published tree was untouched while it ran.
	failingAndWatching := func(ctx context.Context, _ string, _ ...string) *exec.Cmd {
		return exec.CommandContext(ctx, "false")
	}

	pins = map[string]string{}
	base := []ApplierOption{
		WithApplierPendingList(pending),
		WithExecCommand(failingAndWatching),
		WithConfirmFunc(func(string) bool { return true }),
		WithApplierStagingRoot(stagingRoot),
		WithApplierSetVersionsFunc(func(_ string, written map[string]string) error {
			for k, v := range written {
				pins[k] = v
			}
			return nil
		}),
	}
	a, err = NewApplier(overlayDir, configDir, append(base, opts...)...)
	if err != nil {
		t.Fatalf("creating applier: %v", err)
	}

	// The watcher is installed LAST, wrapping whatever exec command finally
	// won. base sets one, but a caller passing WithExecCommand(...) REPLACES
	// it — options are applied in order and the caller's come after base's —
	// so a watcher installed only in base would never fire for any case that
	// supplies its own command, and watch.spawns would read 0 while the gates
	// really ran. Wrapping preserves both: the caller's command decides the
	// outcome, the watcher still observes every spawn.
	inner := a.execCommand
	a.execCommand = func(ctx context.Context, name string, arg ...string) *exec.Cmd {
		watch.spawns++
		watch.hashesAtSpawn = append(watch.hashesAtSpawn, hashOverlayTree(t, overlayDir))
		if _, err := os.Stat(candidate); err == nil {
			watch.candidateSeenAt = append(watch.candidateSeenAt, name+" "+strings.Join(arg, " "))
		}
		return inner(ctx, name, arg...)
	}
	return a, overlayDir, pkg, watch, pins
}

// TestApplierPromote_FailedBumpLeavesTheOverlayByteIdentical is the story's
// central claim at its coarsest: nothing to roll back, because nothing was
// placed.
func TestApplierPromote_FailedBumpLeavesTheOverlayByteIdentical(t *testing.T) {
	applier, overlayDir, pkg, _, _ := promoteFixture(t)
	before := hashOverlayTree(t, overlayDir)

	result, _ := applier.Apply(pkg, false)

	if result.Success {
		t.Fatal("every child process failed and the apply still reported success")
	}
	if after := hashOverlayTree(t, overlayDir); after != before {
		t.Errorf("the published overlay changed during a run in which every bump failed: %s -> %s", before, after)
	}
}

// TestApplierPromote_TheOverlayIsUntouchedWhileTheGatesRun is R3.2 read
// literally — "byte-identical WHILE any gate is running" — and it is the
// assertion the shipped write order fails: copyEbuild puts the candidate in the
// published tree before the manifest step, and the rollback only hides that
// afterwards.
func TestApplierPromote_TheOverlayIsUntouchedWhileTheGatesRun(t *testing.T) {
	applier, overlayDir, pkg, watch, _ := promoteFixture(t)
	before := hashOverlayTree(t, overlayDir)

	if _, err := applier.Apply(pkg, false); err == nil {
		t.Fatal("Apply reported no error although every child failed")
	}

	if watch.spawns == 0 {
		t.Fatal("no child process was spawned; the run under test never reached a gate")
	}
	for i, h := range watch.hashesAtSpawn {
		if h != before {
			t.Errorf("spawn %d observed a modified published overlay (%s, want %s); "+
				"the candidate must be materialised in the staging tree, never in the tree that publishes itself", i, h, before)
		}
	}
	if len(watch.candidateSeenAt) > 0 {
		t.Errorf("the unvalidated candidate ebuild was present in the published overlay while these ran: %v",
			watch.candidateSeenAt)
	}
}

// TestApplierPromote_FailedBumpWritesNoPin is the hazard applier.go:693-698
// already spells out: a pin for a bump that is not on disk aims `--clean` at the
// only ebuild present.
func TestApplierPromote_FailedBumpWritesNoPin(t *testing.T) {
	applier, _, pkg, _, pins := promoteFixture(t)

	if _, err := applier.Apply(pkg, false); err == nil {
		t.Fatal("Apply reported no error although every child failed")
	}

	if v, ok := pins[pkg]; ok {
		t.Errorf("a bump that was never promoted wrote the pin %s = %q; --clean would then delete the only ebuild present", pkg, v)
	}
}

// TestApplierPromote_FailedBumpRetainsAndNamesItsStagedTree is R3.6 seen from
// 4.1's side: the failure has to be inspectable, and the result is where its
// location is stated.
func TestApplierPromote_FailedBumpRetainsAndNamesItsStagedTree(t *testing.T) {
	applier, overlayDir, pkg, _, _ := promoteFixture(t)

	result, _ := applier.Apply(pkg, false)

	if result.StagedPath == "" {
		t.Fatal("a failed bump carries no StagedPath; the operator cannot inspect what was validated")
	}
	if _, err := os.Stat(result.StagedPath); err != nil {
		t.Errorf("the staged tree %q named on the result does not exist: %v", result.StagedPath, err)
	}
	if strings.HasPrefix(filepath.Clean(result.StagedPath), filepath.Clean(overlayDir)+string(os.PathSeparator)) {
		t.Errorf("the staged tree %q lives inside the published overlay; ScanOverlay would see an unclaimed ebuild and --clean would delete it",
			result.StagedPath)
	}
}

// publishedManifestBody is the Manifest the overlay already holds. It names the
// CURRENT version's distfile and nothing else, which is what makes a promotion
// an overwrite rather than a creation.
const publishedManifestBody = "DIST gst-plugins-good-1.28.6.tar.xz 100 BLAKE2B ab SHA512 cd\n"

// writePublishedManifest puts a Manifest in the overlay's package directory.
func writePublishedManifest(t *testing.T, overlayDir, body string) {
	t.Helper()
	dir := filepath.Join(overlayDir, "media-plugins", "gst-plugins-qt6")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("creating the package directory: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "Manifest"), []byte(body), 0o644); err != nil {
		t.Fatalf("writing the published Manifest: %v", err)
	}
}

// TestApplierPromote_FailureDuringPromotionRestoresThePublishedManifest is
// R3.11, and it exists because the Manifest is the one published path that
// promotion OVERWRITES rather than creates.
//
// The deferred orphan rollback (applier.go:633-646) removes one path. That is
// the right lever for the candidate ebuild, which did not exist before this
// apply: removing it restores the previous state exactly. It is the WRONG lever
// for the Manifest — removing it does not restore the previous state, it
// destroys a file the overlay had before this apply ever ran, and an overlay
// with no Manifest is worse than one with a stale Manifest. So the previous
// bytes are captured before the overwrite and put back if any later step of the
// promotion fails.
//
// The failure is induced without a test-only seam: a DIRECTORY is placed where
// the candidate ebuild must land, so the write-then-rename fails with a real
// filesystem error at a real point in the sequence. If the implementation writes
// the ebuild before the Manifest, the Manifest is simply never touched and the
// assertion still holds — which is why the whole-tree hash is checked too, and
// that one is not satisfiable by ordering alone.
func TestApplierPromote_FailureDuringPromotionRestoresThePublishedManifest(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("running as root: the induced rename failure depends on ordinary filesystem rules")
	}

	// Every child succeeds: this run gets all the way to promotion, which is
	// the only place R3.11 can be observed.
	applier, overlayDir, pkg, _, pins := promoteFixture(t, WithExecCommand(mockExecCommandSuccess))

	// A directory where the candidate ebuild has to go. A rename onto it fails,
	// and it fails AFTER promotion has begun.
	blocked := filepath.Join(overlayDir, "media-plugins", "gst-plugins-qt6", "gst-plugins-qt6-1.29.2.ebuild")
	if err := os.Mkdir(blocked, 0o755); err != nil {
		t.Fatalf("blocking the candidate path: %v", err)
	}

	manifestPath := filepath.Join(overlayDir, "media-plugins", "gst-plugins-qt6", "Manifest")
	before := hashOverlayTree(t, overlayDir)

	result, _ := applier.Apply(pkg, false)

	if result.Success {
		t.Fatal("the apply reported success although the candidate could not be written into the overlay")
	}

	got, err := os.ReadFile(manifestPath)
	if err != nil {
		t.Fatalf("the published Manifest is gone after a failed promotion: %v — removing it is worse than leaving a stale one, "+
			"and it is the failure mode a remove-one-path rollback produces on an overwritten file (R3.11)", err)
	}
	if string(got) != publishedManifestBody {
		t.Errorf("the published Manifest was not restored byte for byte after a failed promotion:\n  before %q\n  after  %q",
			publishedManifestBody, got)
	}

	if after := hashOverlayTree(t, overlayDir); after != before {
		t.Errorf("a promotion that failed partway left the published overlay changed: %s -> %s", before, after)
	}
	if v, ok := pins[pkg]; ok {
		t.Errorf("a bump whose promotion failed wrote the pin %s = %q", pkg, v)
	}
}

// TestApplierPromote_FailureDuringPromotionRemovesThePublishedEbuild is the
// half the existing rollback already handles, asserted beside the Manifest so
// the two levers are visibly different rather than accidentally the same.
//
// Here the MANIFEST path is blocked, so the ebuild may land first and the
// Manifest copy then fails. The candidate must not survive: a published ebuild
// with no matching Manifest entry is an unclaimed ebuild, which `--clean`
// deletes and `overlay validate` reports.
func TestApplierPromote_FailureDuringPromotionRemovesThePublishedEbuild(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("running as root: the induced write failure depends on ordinary filesystem rules")
	}

	applier, overlayDir, pkg, _, _ := promoteFixture(t, WithExecCommand(mockExecCommandSuccess))

	pkgDir := filepath.Join(overlayDir, "media-plugins", "gst-plugins-qt6")
	manifestPath := filepath.Join(pkgDir, "Manifest")
	if err := os.Remove(manifestPath); err != nil {
		t.Fatalf("clearing the Manifest: %v", err)
	}
	if err := os.Mkdir(manifestPath, 0o755); err != nil {
		t.Fatalf("blocking the Manifest path: %v", err)
	}

	result, _ := applier.Apply(pkg, false)

	if result.Success {
		t.Fatal("the apply reported success although the Manifest could not be written")
	}
	candidate := filepath.Join(pkgDir, "gst-plugins-qt6-1.29.2.ebuild")
	if _, err := os.Stat(candidate); err == nil {
		t.Errorf("the candidate %q survived a failed promotion; an ebuild no Manifest entry covers is an unclaimed ebuild, "+
			"and --clean deletes those", candidate)
	}
}

// TestApplierRetain_FailedBumpKeepsItsTreeOnDisk is the first half of R3.6. The
// tree is retained rather than cleaned up, which is the opposite of what the
// fixer's private distdir does — and deliberately so: that one is scratch, this
// one is evidence.
func TestApplierRetain_FailedBumpKeepsItsTreeOnDisk(t *testing.T) {
	applier, _, pkg, _, _ := promoteFixture(t)

	result, _ := applier.Apply(pkg, false)

	if result.Success {
		t.Fatal("every child process failed and the apply still reported success")
	}
	if result.StagedPath == "" {
		t.Fatal("a failed bump carries no StagedPath")
	}

	info, err := os.Stat(result.StagedPath)
	if err != nil {
		t.Fatalf("the staged tree %q was removed after the failure: %v — R3.6 retains it so the failure can be inspected without repeating the work",
			result.StagedPath, err)
	}
	if !info.IsDir() {
		t.Errorf("StagedPath %q is not a directory", result.StagedPath)
	}

	// The candidate itself has to be in there, or the retained tree answers no
	// question the operator would ask of it.
	candidate := filepath.Join(result.StagedPath, "media-plugins", "gst-plugins-qt6", "gst-plugins-qt6-1.29.2.ebuild")
	if _, err := os.Stat(candidate); err != nil {
		t.Errorf("the retained tree holds no candidate ebuild at %q: %v", candidate, err)
	}
}

// TestApplierRetain_TheStagedPathReachesTheReport is the second half. A field
// nothing renders is a field the next refactor deletes.
func TestApplierRetain_TheStagedPathReachesTheReport(t *testing.T) {
	applier, _, pkg, _, _ := promoteFixture(t)

	result, _ := applier.Apply(pkg, false)

	if result.StagedPath == "" {
		t.Fatal("a failed bump carries no StagedPath")
	}
	summary := applySummary(result)
	if !strings.Contains(summary, result.StagedPath) {
		t.Errorf("the failure summary %q does not name the retained staged tree %q; "+
			"a tree nobody is told about is not inspectable (R3.6)", summary, result.StagedPath)
	}
}

// TestApplierRetain_SuccessCarriesNoStagedPath keeps the field honest in the
// other direction. A promoted bump's staged tree has served its purpose, and
// printing a path beside a success invites the operator to go and read a tree
// that says nothing they do not already know from the overlay.
func TestApplierRetain_SuccessCarriesNoStagedPath(t *testing.T) {
	applier, _, pkg, _, _ := promoteFixture(t, WithExecCommand(mockExecCommandSuccess))

	result, err := applier.Apply(pkg, false)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if !result.Success {
		t.Fatalf("the apply failed with every child succeeding: %v", result.Error)
	}

	if result.StagedPath != "" {
		t.Errorf("a successful apply reports StagedPath %q; the retained tree is a failure's evidence, not a success's", result.StagedPath)
	}
	if summary := applySummary(result); strings.Contains(summary, "staging") {
		t.Errorf("the success summary %q mentions staging; the operator has nothing to inspect", summary)
	}
}

// TestApplierRetain_SecondFailureReplacesTheFirstTree is R3.7 seen from the
// retention side, and it is the assertion that keeps a nightly sweep from
// filling the config directory: one tree per package and version, holding the
// LATEST attempt.
//
// The second attempt's bytes are what an operator reads, so "exactly one tree"
// is not enough on its own — the surviving tree must be the new one.
func TestApplierRetain_SecondFailureReplacesTheFirstTree(t *testing.T) {
	applier, _, pkg, _, _ := promoteFixture(t)

	first, _ := applier.Apply(pkg, false)
	if first.StagedPath == "" {
		t.Fatal("the first failed bump carries no StagedPath")
	}

	// Mark the first tree so the second run's replacement is observable. The
	// marker sits beside the candidate rather than inside it, so it cannot be
	// mistaken for a change the applier made.
	marker := filepath.Join(first.StagedPath, ".first-attempt")
	if err := os.WriteFile(marker, []byte("attempt one"), 0o600); err != nil {
		t.Fatalf("marking the first staged tree: %v", err)
	}

	// The pending entry survives a failed apply, so the same bump can be
	// retried; that retry is what R3.7 is about.
	second, _ := applier.Apply(pkg, false)
	if second.StagedPath == "" {
		t.Fatal("the second failed bump carries no StagedPath")
	}

	if second.StagedPath != first.StagedPath {
		t.Errorf("the second attempt staged into %q and the first into %q; retention is one tree per package AND version, "+
			"and the path itself is what expresses it", second.StagedPath, first.StagedPath)
	}
	if _, err := os.Stat(marker); err == nil {
		t.Error("the first attempt's tree survived into the second run; restaging is RemoveAll followed by a fresh build, " +
			"or a stale file from a previous failure is read as evidence about this one")
	}

	// Exactly one tree under <staging>/<category>/<package>, whatever the two
	// runs did.
	versionsDir := filepath.Dir(second.StagedPath)
	entries, err := os.ReadDir(versionsDir)
	if err != nil {
		t.Fatalf("reading %q: %v", versionsDir, err)
	}
	if len(entries) != 1 {
		var names []string
		for _, e := range entries {
			names = append(names, e.Name())
		}
		t.Errorf("two failures for the same package and version left %d trees (%v), want exactly 1", len(entries), names)
	}
}

// TestApplierRetain_TwoFailuresStillLeaveTheOverlayUntouched re-states 4.1's
// claim across a retry, which is the shape a real sweep has: the same bump fails
// on Monday and again on Tuesday, and the published overlay is the same tree it
// was on Sunday.
func TestApplierRetain_TwoFailuresStillLeaveTheOverlayUntouched(t *testing.T) {
	applier, overlayDir, pkg, _, pins := promoteFixture(t)
	before := hashOverlayTree(t, overlayDir)

	applier.Apply(pkg, false) //nolint:errcheck // the failure is the fixture
	applier.Apply(pkg, false) //nolint:errcheck // the failure is the fixture

	if after := hashOverlayTree(t, overlayDir); after != before {
		t.Errorf("the published overlay changed across two failed attempts: %s -> %s", before, after)
	}
	if len(pins) != 0 {
		t.Errorf("a pin was written for a bump that failed twice: %v", pins)
	}
}

// The digest the fixture's published Manifest names for the new version's
// distfile. Promotion re-checks it, because a tarball re-rolled under the same
// name between staging and promotion is a different input.
const (
	stagedDistfileDigest   = "ab"
	rerolledDistfileDigest = "ff"
)

// candidateBody returns the bytes the applier itself would write for the
// candidate: a copy of the current version's ebuild. Matching means matching
// THIS, so the fixture derives it rather than restating it.
func candidateBody(t *testing.T, overlayDir string) string {
	t.Helper()
	path := filepath.Join(overlayDir, "media-plugins", "gst-plugins-qt6", "gst-plugins-qt6-1.28.6.ebuild")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading the source ebuild: %v", err)
	}
	return string(data)
}

func digestOf(body string) string {
	sum := sha256.Sum256([]byte(body))
	return hex.EncodeToString(sum[:])
}

// stageCandidateFor materialises a staged tree holding exactly `body` as the
// candidate, WITHOUT a record. It goes through validate.Stage rather than
// writing the layout by hand, so these tests cannot pass against a tree the real
// code would not recognise.
func stageCandidateFor(t *testing.T, stagingRoot, overlayDir, body string) string {
	t.Helper()
	staged, err := validate.Stage(validate.StageRequest{
		Overlay:     overlayDir,
		StagingRoot: stagingRoot,
		Atom:        "media-plugins/gst-plugins-qt6",
		Version:     "1.29.2",
		EbuildBytes: []byte(body),
	})
	if err != nil {
		t.Fatalf("staging the candidate: %v", err)
	}
	return staged
}

// provedRecord is the record a successful validation leaves beside its tree:
// every gate clean, at the given depth, over the given inputs.
func provedRecord(depth validate.Depth, ebuildDigest, distfileDigest string) validate.StageRecord {
	return validate.StageRecord{
		Depth: depth,
		Gates: []validate.GateResult{
			{Gate: validate.GateOptions, Outcome: validate.OutcomePass},
			{Gate: validate.GatePatches, Outcome: validate.OutcomePass},
			{Gate: validate.GateConfigure, Outcome: validate.OutcomePass,
				Reason: "a configure pass does not cover compilation"},
		},
		EbuildDigest:   ebuildDigest,
		DistfileDigest: distfileDigest,
	}
}

// stageProvedCandidate stages the tree AND writes a clean record beside it —
// the state a proving `--check --llm` run leaves behind.
func stageProvedCandidate(t *testing.T, stagingRoot, overlayDir, body string, depth validate.Depth) string {
	t.Helper()
	staged := stageCandidateFor(t, stagingRoot, overlayDir, body)
	if err := validate.WriteStageRecord(staged, provedRecord(depth, digestOf(body), stagedDistfileDigest)); err != nil {
		t.Fatalf("writing the stage record: %v", err)
	}
	return staged
}

// TestApplierPromote_AMatchingProvedTreeIsPromotedWithoutRunningAGate is R10.1,
// and the "no gate ran" half is the whole economic argument: the hours were
// already spent, and spending them again is what makes an operator stop using
// `--check --llm` first.
func TestApplierPromote_AMatchingProvedTreeIsPromotedWithoutRunningAGate(t *testing.T) {
	applier, overlayDir, pkg, watch, pins := promoteFixture(t, WithExecCommand(mockExecCommandSuccess))

	stageProvedCandidate(t, applier.StagingRoot(), overlayDir, candidateBody(t, overlayDir), validate.DepthConfigure)
	spawnsBefore := watch.spawns

	result, err := applier.Apply(pkg, false)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}

	if !result.Success {
		t.Fatalf("promoting a validated tree failed: %v", result.Error)
	}
	if result.ValidationSource != "staged" {
		t.Errorf("ValidationSource = %q, want %q — this bump was proved before the run started (R10.3)", result.ValidationSource, "staged")
	}
	if watch.spawns != spawnsBefore {
		t.Errorf("%d child process(es) ran while promoting an already-validated tree; the gates were paid for once already (R10.1)",
			watch.spawns-spawnsBefore)
	}

	// R3.4: the bytes published are the bytes that were validated.
	published := filepath.Join(overlayDir, "media-plugins", "gst-plugins-qt6", "gst-plugins-qt6-1.29.2.ebuild")
	got, err := os.ReadFile(published)
	if err != nil {
		t.Fatalf("the promoted ebuild is not in the overlay: %v", err)
	}
	if string(got) != candidateBody(t, overlayDir) {
		t.Error("the published bytes differ from the staged ones; promotion publishes exactly what was validated (R3.4)")
	}
	if pins[pkg] != "1.29.2" {
		t.Errorf("the pin after promotion is %q, want %q", pins[pkg], "1.29.2")
	}
}

// TestApplierPromote_ARecordShowingAFailedGateIsRevalidated is THE blocker case.
// R3.6 retains the tree of every bump that was not promoted — every failure — so
// without this condition the retention rule feeds R10.1 directly and yesterday's
// rejected bump is published today.
//
// The tree here is a perfect match on package, version, bytes and digest. The
// only thing wrong with it is that it FAILED, and that has to be enough.
func TestApplierPromote_ARecordShowingAFailedGateIsRevalidated(t *testing.T) {
	applier, overlayDir, pkg, watch, _ := promoteFixture(t, WithExecCommand(mockExecCommandSuccess))
	body := candidateBody(t, overlayDir)

	staged := stageCandidateFor(t, applier.StagingRoot(), overlayDir, body)
	failed := provedRecord(validate.DepthConfigure, digestOf(body), stagedDistfileDigest)
	failed.Gates = append(failed.Gates, validate.GateResult{
		Gate:    validate.GateConfigure,
		Outcome: validate.OutcomeFailed,
		Findings: []validate.Finding{{
			Gate: validate.GateConfigure, Severity: validate.SeverityError,
			Detail: `meson.build:1:0: ERROR: Unknown option: "aalib".`,
		}},
	})
	if err := validate.WriteStageRecord(staged, failed); err != nil {
		t.Fatalf("writing the failed record: %v", err)
	}

	spawnsBefore := watch.spawns
	result, err := applier.Apply(pkg, false)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}

	if result.ValidationSource != "this-run" {
		t.Fatalf("ValidationSource = %q for a tree whose own record shows a FAILED gate; R3.6 retains the tree of every "+
			"failure, so promoting on a match alone republishes yesterday's rejected bump (R10.1)", result.ValidationSource)
	}
	if watch.spawns == spawnsBefore {
		t.Error("no gate ran for a tree recorded as failing; the bump was published on the strength of a record that says it did not pass")
	}
}

// TestApplierPromote_ATreeWithNoRecordIsRevalidated is R10.5. An unrecorded tree
// is not evidence of anything: it could be a half-written staging, a tree from a
// version of bentoo that predates the record, or one an operator copied in by
// hand. "No claim" is not "a passing claim".
func TestApplierPromote_ATreeWithNoRecordIsRevalidated(t *testing.T) {
	applier, overlayDir, pkg, watch, _ := promoteFixture(t, WithExecCommand(mockExecCommandSuccess))

	stageCandidateFor(t, applier.StagingRoot(), overlayDir, candidateBody(t, overlayDir))
	spawnsBefore := watch.spawns

	result, err := applier.Apply(pkg, false)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}

	if result.ValidationSource != "this-run" {
		t.Errorf("ValidationSource = %q for a staged tree carrying no record; absence of a claim is not a passing claim (R10.5)",
			result.ValidationSource)
	}
	if watch.spawns == spawnsBefore {
		t.Error("no gate ran for a tree with no record at all")
	}
}

// TestApplierPromote_ATreeRecordedShallowerThanThisRunNeedsIsRevalidated is the
// depth half of R10.1. A tree proved at `options` says nothing about whether the
// package configures, so a run that selected `configure` cannot borrow it — and
// the depth can legitimately rise between two runs, through a policy change, a
// `--depth` flag or a reviewer escalation.
func TestApplierPromote_ATreeRecordedShallowerThanThisRunNeedsIsRevalidated(t *testing.T) {
	applier, overlayDir, pkg, watch, _ := promoteFixture(t,
		WithExecCommand(mockExecCommandSuccess),
		WithApplierDepth(validate.DepthConfigure),
	)

	// Proved, matching, clean — but only to the static gate.
	stageProvedCandidate(t, applier.StagingRoot(), overlayDir, candidateBody(t, overlayDir), validate.DepthOptions)
	spawnsBefore := watch.spawns

	result, err := applier.Apply(pkg, false)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}

	if result.ValidationSource != "this-run" {
		t.Errorf("ValidationSource = %q for a tree recorded at depth options while this run selected configure; "+
			"a shallower proof does not cover a deeper question (R10.1)", result.ValidationSource)
	}
	if watch.spawns == spawnsBefore {
		t.Error("no gate ran although the retained proof stopped short of the depth this run needs")
	}
}

// TestApplierPromote_ATreeRecordedDeeperThanNeededIsStillPromoted is the same
// rule read the other way: "not below" means a compile-depth proof satisfies a
// configure-depth run. Requiring equality would throw away the most expensive
// evidence there is whenever policy relaxed by one rung.
func TestApplierPromote_ATreeRecordedDeeperThanNeededIsStillPromoted(t *testing.T) {
	applier, overlayDir, pkg, watch, _ := promoteFixture(t,
		WithExecCommand(mockExecCommandSuccess),
		WithApplierDepth(validate.DepthConfigure),
	)

	stageProvedCandidate(t, applier.StagingRoot(), overlayDir, candidateBody(t, overlayDir), validate.DepthCompile)
	spawnsBefore := watch.spawns

	result, err := applier.Apply(pkg, false)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}

	if result.ValidationSource != "staged" {
		t.Errorf("ValidationSource = %q for a tree proved at compile depth while this run needed configure; "+
			"the condition is \"not below\", and a deeper proof covers a shallower question", result.ValidationSource)
	}
	if watch.spawns != spawnsBefore {
		t.Errorf("%d gate(s) ran although the retained proof went deeper than this run required", watch.spawns-spawnsBefore)
	}
}

// TestApplierPromote_ARecordOfSkippedGatesIsStillPromotable keeps R10.1 aligned
// with R3.3 and R3.12: SKIPPED is a reported outcome, not a failure, and a host
// that could not build for want of an installed dependency still produced a
// promotable result. Reading "PASS or SKIPPED" as "PASS only" would quietly
// revalidate — and then re-skip — every such bump on every run.
func TestApplierPromote_ARecordOfSkippedGatesIsStillPromotable(t *testing.T) {
	applier, overlayDir, pkg, watch, _ := promoteFixture(t, WithExecCommand(mockExecCommandSuccess))
	body := candidateBody(t, overlayDir)

	staged := stageCandidateFor(t, applier.StagingRoot(), overlayDir, body)
	skipped := validate.StageRecord{
		Depth: validate.DepthConfigure,
		Gates: []validate.GateResult{
			{Gate: validate.GateOptions, Outcome: validate.OutcomePass},
			{Gate: validate.GatePatches, Outcome: validate.OutcomeSkipped, Reason: "dev-qt/qtbase-6.9.1 is not installed"},
			{Gate: validate.GateConfigure, Outcome: validate.OutcomeSkipped, Reason: "dev-qt/qtbase-6.9.1 is not installed"},
		},
		EbuildDigest:   digestOf(body),
		DistfileDigest: stagedDistfileDigest,
	}
	if err := validate.WriteStageRecord(staged, skipped); err != nil {
		t.Fatalf("writing the record: %v", err)
	}

	spawnsBefore := watch.spawns
	result, err := applier.Apply(pkg, false)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}

	if result.ValidationSource != "staged" {
		t.Errorf("ValidationSource = %q for a record of PASS and SKIPPED outcomes; SKIPPED is a reported outcome and R3.3 "+
			"promotes on \"PASS or SKIPPED\"", result.ValidationSource)
	}
	if watch.spawns != spawnsBefore {
		t.Errorf("%d gate(s) re-ran a question this host had already answered with a named skip", watch.spawns-spawnsBefore)
	}
}

// TestApplierPromote_AnOlderStagedTreeThatStillMatchesIsPromoted is the case
// that stops ModTime. The staged tree here is a full day older than the overlay
// and identical in content; git, rsync, a container build and a restored backup
// all produce exactly this.
func TestApplierPromote_AnOlderStagedTreeThatStillMatchesIsPromoted(t *testing.T) {
	applier, overlayDir, pkg, watch, _ := promoteFixture(t, WithExecCommand(mockExecCommandSuccess))

	staged := stageProvedCandidate(t, applier.StagingRoot(), overlayDir, candidateBody(t, overlayDir), validate.DepthConfigure)

	// Age the whole staged tree by a day, and touch the overlay's own ebuild so
	// the ordering is unambiguous: staged is OLDER, content is IDENTICAL.
	yesterday := time.Now().Add(-24 * time.Hour)
	if err := filepath.Walk(staged, func(path string, _ os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		return os.Chtimes(path, yesterday, yesterday)
	}); err != nil {
		t.Fatalf("ageing the staged tree: %v", err)
	}
	source := filepath.Join(overlayDir, "media-plugins", "gst-plugins-qt6", "gst-plugins-qt6-1.28.6.ebuild")
	now := time.Now()
	if err := os.Chtimes(source, now, now); err != nil {
		t.Fatalf("touching the source ebuild: %v", err)
	}

	spawnsBefore := watch.spawns
	result, err := applier.Apply(pkg, false)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}

	if result.ValidationSource != "staged" {
		t.Errorf("ValidationSource = %q for a staged tree that is older but byte-identical; "+
			"a mtime moves on checkout, rsync, a container build and a restored backup without a byte changing (R10.1)",
			result.ValidationSource)
	}
	if watch.spawns != spawnsBefore {
		t.Errorf("%d gate(s) ran because the staged tree was older, not because anything differed", watch.spawns-spawnsBefore)
	}
}

// TestApplierPromote_ATreeTheFixerModifiedIsStillPromotable is the pre-fixer
// rule, and it is worth its own case because getting it wrong is invisible: the
// bump still publishes, it just pays for a second configure — on exactly the
// packages that needed a repair, which are the slowest ones.
//
// The tree here has been edited on disk, as R8.1 has the fixer edit it. The
// record still carries the digest of the candidate AS STAGED, and that is what
// promotion compares against.
func TestApplierPromote_ATreeTheFixerModifiedIsStillPromotable(t *testing.T) {
	applier, overlayDir, pkg, watch, _ := promoteFixture(t, WithExecCommand(mockExecCommandSuccess))
	asStaged := candidateBody(t, overlayDir)

	staged := stageProvedCandidate(t, applier.StagingRoot(), overlayDir, asStaged, validate.DepthConfigure)

	// The fixer's edit: the staged ebuild on disk no longer matches the bytes
	// that were staged, and the record deliberately still names the original.
	repaired := asStaged + "\n# the build fixer dropped -Daalib= and -Dlibcaca=\n"
	candidate := filepath.Join(staged, "media-plugins", "gst-plugins-qt6", "gst-plugins-qt6-1.29.2.ebuild")
	if err := os.WriteFile(candidate, []byte(repaired), 0o644); err != nil {
		t.Fatalf("simulating the fixer's edit: %v", err)
	}

	spawnsBefore := watch.spawns
	result, err := applier.Apply(pkg, false)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}

	if result.ValidationSource != "staged" {
		t.Errorf("ValidationSource = %q for a tree the fixer edited; the comparison is against the candidate's bytes "+
			"AS STAGED, or every repaired bump revalidates — and those are the expensive ones (R10.1)", result.ValidationSource)
	}
	if watch.spawns != spawnsBefore {
		t.Errorf("%d gate(s) re-ran because the fixer had edited the staged tree", watch.spawns-spawnsBefore)
	}

	// And what is published is the REPAIRED ebuild, since that is what the
	// re-run validated.
	published := filepath.Join(overlayDir, "media-plugins", "gst-plugins-qt6", "gst-plugins-qt6-1.29.2.ebuild")
	got, err := os.ReadFile(published)
	if err != nil {
		t.Fatalf("reading the promoted ebuild: %v", err)
	}
	if string(got) != repaired {
		t.Error("the published bytes are not the repaired ones; promotion publishes the tree that was validated, fixes included (R3.4)")
	}
}

// TestApplierPromote_AChangedDistfileDigestIsNotPromoted covers the input that
// is not in the overlay at all. A tarball re-rolled upstream under the SAME name
// between staging and promotion is a different source tree, and every gate's
// answer was about the old one.
func TestApplierPromote_AChangedDistfileDigestIsNotPromoted(t *testing.T) {
	applier, overlayDir, pkg, _, pins := promoteFixture(t, WithExecCommand(mockExecCommandSuccess))
	body := candidateBody(t, overlayDir)

	stageProvedCandidate(t, applier.StagingRoot(), overlayDir, body, validate.DepthConfigure)

	// The published Manifest now names a different digest for the same file.
	writePublishedManifest(t, overlayDir,
		"DIST gst-plugins-good-1.29.2.tar.xz 100 BLAKE2B "+rerolledDistfileDigest+" SHA512 "+rerolledDistfileDigest+"\n")

	result, _ := applier.Apply(pkg, false)

	if result.Success && result.ValidationSource == "staged" {
		t.Fatal("a retained tree was promoted although the distfile digest changed under it; every gate's answer was about " +
			"a source tree that is no longer the one being published")
	}
	msg := ""
	if result.Error != nil {
		msg = result.Error.Error()
	}
	report := msg + " " + result.DepthReason + " " + applySummary(result)
	if result.ValidationSource == "this-run" {
		return // revalidating is an acceptable answer; naming the mismatch is not optional below
	}
	for _, want := range []string{stagedDistfileDigest, rerolledDistfileDigest} {
		if !strings.Contains(report, want) {
			t.Errorf("the refusal does not name the digest %q; the operator cannot tell WHICH input moved: %q", want, report)
		}
	}
	if len(pins) != 0 {
		t.Errorf("a bump refused for a changed distfile wrote a pin: %v", pins)
	}
}

// TestApplierPromote_DifferentEbuildBytesTriggerRevalidation is R10.2. A staged
// tree whose candidate no longer matches what this apply would produce is
// evidence about a different bump.
func TestApplierPromote_DifferentEbuildBytesTriggerRevalidation(t *testing.T) {
	applier, overlayDir, pkg, watch, _ := promoteFixture(t, WithExecCommand(mockExecCommandSuccess))

	stageProvedCandidate(t, applier.StagingRoot(), overlayDir,
		candidateBody(t, overlayDir)+"\n# an edit nobody validated\n", validate.DepthConfigure)
	spawnsBefore := watch.spawns

	result, err := applier.Apply(pkg, false)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}

	if result.ValidationSource != "this-run" {
		t.Errorf("ValidationSource = %q, want %q — the staged candidate differs from the one this apply produces, "+
			"so the retained tree proves nothing about it (R10.2)", result.ValidationSource, "this-run")
	}
	if watch.spawns == spawnsBefore {
		t.Error("no gate ran although the staged tree did not match; the bump was published on the strength of a tree that describes a different candidate")
	}
}

// TestApplierPromote_NoStagedTreeAtAllValidatesFirst is R10.2's ordinary case:
// the operator ran `--apply` without a proving `--check` first, which is how
// most applies happen.
func TestApplierPromote_NoStagedTreeAtAllValidatesFirst(t *testing.T) {
	applier, _, pkg, watch, _ := promoteFixture(t, WithExecCommand(mockExecCommandSuccess))
	spawnsBefore := watch.spawns

	result, err := applier.Apply(pkg, false)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}

	if result.ValidationSource != "this-run" {
		t.Errorf("ValidationSource = %q with no retained tree on disk, want %q", result.ValidationSource, "this-run")
	}
	if watch.spawns == spawnsBefore {
		t.Error("no gate ran and no staged tree existed; the bump was published without being validated at all")
	}
}

// TestApplierPromote_ARecordIsWrittenBesideEveryStagedTree is R10.4 from the
// producing side. Without it the whole condition above is unreachable: the first
// run writes nothing, so the second run always revalidates and the feature is
// dead code that still costs the hours.
func TestApplierPromote_ARecordIsWrittenBesideEveryStagedTree(t *testing.T) {
	applier, _, pkg, _, _ := promoteFixture(t, WithExecCommand(mockExecCommandSuccess))

	result, err := applier.Apply(pkg, false)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	// Located through StagingRoot rather than result.StagedPath: 4.2 CLEARS
	// StagedPath on a success on purpose — "the retained tree is a failure's
	// evidence, not a success's" — and this case runs a successful apply, so
	// reading the path off the result would contradict a rule the story
	// deliberately made. The tree is where Stage always puts it, which is what
	// the sibling cases below already rely on. Surface only; every assertion
	// below is the one that was authored.
	staged, err := validate.StagedTreePath(applier.StagingRoot(), pkg, "1.29.2")
	if err != nil {
		t.Fatalf("deriving the staged tree path: %v", err)
	}

	record, err := validate.ReadStageRecord(staged)
	if err != nil {
		t.Fatalf("no stage record beside %q: %v — R10.4 writes one for every staged tree, and R10.1 cannot promote without it",
			staged, err)
	}
	if len(record.Gates) == 0 {
		t.Error("the record names no gates; it has to say what was checked, not merely that something was")
	}
	if record.Depth == validate.DepthNone && result.DepthReached != "none" {
		t.Errorf("the record's depth is none although the run reached %q", result.DepthReached)
	}
	if record.EbuildDigest == "" {
		t.Error("the record carries no candidate digest, so a later run cannot tell whether the inputs moved")
	}
}

// TestApplierPromote_TheReportStatesWhichPathWasTaken is R10.3 at the surface
// the operator reads. A field nothing renders is a field the next refactor
// deletes — the same argument 4.2 makes for the staged path.
func TestApplierPromote_TheReportStatesWhichPathWasTaken(t *testing.T) {
	applier, overlayDir, pkg, _, _ := promoteFixture(t, WithExecCommand(mockExecCommandSuccess))
	stageProvedCandidate(t, applier.StagingRoot(), overlayDir, candidateBody(t, overlayDir), validate.DepthConfigure)

	result, err := applier.Apply(pkg, false)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}

	summary := applySummary(result)
	if !strings.Contains(strings.ToLower(summary), "validated") && !strings.Contains(strings.ToLower(summary), "staged") {
		t.Errorf("the success summary %q does not say whether the gates ran now or were paid for earlier; "+
			"a fast green and a proved green look identical without it (R10.3)", summary)
	}
}

// TestApplierPromote_ValidationSourceIsOneOfTwoKnownValues keeps the field from
// becoming free text. Two callers render it and a third will eventually branch
// on it.
func TestApplierPromote_ValidationSourceIsOneOfTwoKnownValues(t *testing.T) {
	applier, overlayDir, pkg, _, _ := promoteFixture(t, WithExecCommand(mockExecCommandSuccess))
	stageProvedCandidate(t, applier.StagingRoot(), overlayDir, candidateBody(t, overlayDir), validate.DepthConfigure)

	result, err := applier.Apply(pkg, false)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}

	switch result.ValidationSource {
	case "staged", "this-run":
	default:
		t.Errorf("ValidationSource = %q; the two values are \"staged\" and \"this-run\"", result.ValidationSource)
	}
}

// Authored for the S037 review, round 6 — the interrupt invariant.
//
// AN INTERRUPT MUST NOT PUBLISH A BUMP, AND THIS IS THE THIRD ATTEMPT AT SAYING
// SO. The first put the guard in RunBuildGates; the second wrapped
// validate.Run's return. Both guarded a VERDICT, and each time the next review
// found a route that reached promotion without passing through the guarded one:
//
//   - runStaticGates turns any validate.Run error — ctx.Err() included — into a
//     single SKIPPED option gate, and PromotionDecision promotes on SKIPPED.
//   - DependenciesSatisfied turns a cancelled probe into SkippedGates with a NIL
//     error, so runBuildGates reports a promotable list and no failure at all.
//   - the reuse path (R10.1) calls promote() without consulting
//     PromotionDecision in the first place.
//
// So the invariant now sits at the WRITE (promote), and these two tests are one
// per route into it. They assert the overlay's bytes and the pin rather than the
// error text: a guard that returns the right sentence and publishes anyway is
// the bug, not the fix.

// TestApplierPromote_AnInterruptedRunPublishesNothingOnTheReusePath drives the
// route no verdict-side guard can cover — nothing is validated in this run, so
// there is no gate list to inspect and nothing for PromotionDecision to refuse.
func TestApplierPromote_AnInterruptedRunPublishesNothingOnTheReusePath(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	applier, overlayDir, pkg, _, pins := promoteFixture(t,
		WithExecCommand(mockExecCommandSuccess), WithApplierContext(ctx))

	stageProvedCandidate(t, applier.StagingRoot(), overlayDir, candidateBody(t, overlayDir), validate.DepthConfigure)
	before := hashOverlayTree(t, overlayDir)

	// The operator's Ctrl-C, landing before Apply reaches the reuse branch. The
	// tree really is proved and really does match: everything about this bump is
	// publishable EXCEPT that the run was stopped.
	cancel()

	result, err := applier.Apply(pkg, false)

	if err == nil || result.Success {
		t.Fatal("an interrupted run promoted a bump from a retained tree; a cancelled context must never reach the published overlay")
	}
	if after := hashOverlayTree(t, overlayDir); after != before {
		t.Errorf("the published overlay changed during an interrupted run: %s -> %s", before, after)
	}
	if pins[pkg] != "" {
		t.Errorf("an interrupted run wrote the pin %q; `--clean` sweeps by pin, so a pin for a bump that is not on disk "+
			"aims the deletion rule at the only ebuild present", pins[pkg])
	}
}

// TestApplierPromote_AnInterruptedStaticGatePublishesNothing drives the FIRST
// open door on its own.
//
// Depth `options` is chosen so that runBuildGates returns before it builds
// anything, which takes the guard d784377 put inside RunBuildGates out of the
// picture entirely — a run at compile depth reaches that guard first and would
// pass this assertion without the new one, which is how the first draft of this
// test came to prove nothing. What is left is the option gate alone: cancelled,
// it becomes one SKIPPED gate, and PromotionDecision promotes on SKIPPED.
func TestApplierPromote_AnInterruptedStaticGatePublishesNothing(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	applier, overlayDir, pkg, _, pins := promoteFixture(t,
		WithExecCommand(mockExecCommandSuccess),
		WithApplierContext(ctx),
		WithApplierDepth(validate.DepthOptions))
	before := hashOverlayTree(t, overlayDir)

	// The child is built on a context of its OWN and the applier's is cancelled
	// once it has been. That is the scenario deliberately: a Ctrl-C that KILLS
	// the manifest child fails the apply long before promotion and proves
	// nothing, whereas an interrupt arriving after the child returned leaves the
	// gates below to answer under a dead context.
	var spawned int
	inner := applier.execCommand
	applier.execCommand = func(_ context.Context, name string, arg ...string) *exec.Cmd {
		spawned++
		// NOT the applier's context, deliberately: the child has to COMPLETE and
		// the applier's context has to be dead afterwards. Inheriting it would
		// kill the child instead, which fails the apply at the manifest step and
		// never reaches the gate this test is about.
		cmd := inner(context.Background(), name, arg...) //nolint:contextcheck // see above: the detached child IS the scenario
		cancel()
		return cmd
	}

	result, err := applier.Apply(pkg, false)

	if spawned == 0 {
		t.Fatal("no child was spawned, so the cancellation never fired and this test asserted nothing")
	}
	if err == nil || result.Success {
		t.Fatal("an interrupted run promoted a bump whose option gate was asked under a cancelled context")
	}
	if after := hashOverlayTree(t, overlayDir); after != before {
		t.Errorf("the published overlay changed during an interrupted run: %s -> %s", before, after)
	}
	if pins[pkg] != "" {
		t.Errorf("an interrupted run wrote the pin %q", pins[pkg])
	}
}

// TestApplierPromote_AnInterruptedDependencyProbePublishesNothing drives the
// SECOND open door, which sits in front of the guard that was supposed to cover
// it: DependenciesSatisfied runs before RunBuildGates and turns a cancelled
// probe into SkippedGates with a NIL error, so the apply never sees a failure at
// all.
//
// Only the manifest child is spared the cancellation. Sparing every child — the
// obvious way to write this — lets the probe succeed and hands the run to
// RunBuildGates, whose own guard then answers, and the door under test is never
// opened.
func TestApplierPromote_AnInterruptedDependencyProbePublishesNothing(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	applier, overlayDir, pkg, _, pins := promoteFixture(t,
		WithExecCommand(mockExecCommandSuccess),
		WithApplierContext(ctx),
		WithApplierDepth(validate.DepthConfigure))
	before := hashOverlayTree(t, overlayDir)

	var probed bool
	inner := applier.execCommand
	applier.execCommand = func(c context.Context, name string, arg ...string) *exec.Cmd {
		if name == "pkgdev" {
			// Detached on purpose — see the sibling test above. Only the manifest
			// child is spared, so the cancellation is observed by the step after it.
			cmd := inner(context.Background(), name, arg...) //nolint:contextcheck // the detached manifest child IS the scenario
			cancel()
			return cmd
		}
		if name == "emerge" {
			probed = true
		}
		return inner(c, name, arg...)
	}

	result, err := applier.Apply(pkg, false)

	if !probed {
		t.Fatal("the dependency pre-check never spawned `emerge`, so the door under test was never reached")
	}
	if err == nil || result.Success {
		t.Fatal("an interrupted run promoted a bump whose dependency probe was cancelled; " +
			"DependenciesSatisfied reports that as SKIPPED gates and a nil error, which PromotionDecision promotes on")
	}
	if after := hashOverlayTree(t, overlayDir); after != before {
		t.Errorf("the published overlay changed during an interrupted run: %s -> %s", before, after)
	}
	if pins[pkg] != "" {
		t.Errorf("an interrupted run wrote the pin %q", pins[pkg])
	}
}

// TestApplierPromote_AnInterruptedRunBlamesTheInterruptNotTheProofPolicy pins
// the SECOND placement of the invariant, the one in Apply.
//
// promote() alone already makes an interrupted run unpublishable — the mutation
// matrix for this round says so: removing the early check leaves every
// assertion above green. What it does NOT preserve is WHICH refusal the
// operator reads. With `require_proof` set, refuseUnproved runs first and
// answers "proof at depth configure is required and the build gates did not
// run", which is true and useless: it sends the operator to change a policy
// that had nothing to do with it. The early check exists to say "you stopped
// me" instead, and without an assertion on the message it is a line nothing
// defends.
func TestApplierPromote_AnInterruptedRunBlamesTheInterruptNotTheProofPolicy(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	applier, _, pkg, _, _ := promoteFixture(t,
		WithExecCommand(mockExecCommandSuccess),
		WithApplierContext(ctx),
		WithApplierDepth(validate.DepthConfigure),
		WithApplierRequireProof(true))

	inner := applier.execCommand
	applier.execCommand = func(c context.Context, name string, arg ...string) *exec.Cmd {
		if name == "pkgdev" {
			// Detached on purpose — see the sibling test above. Only the manifest
			// child is spared, so the cancellation is observed by the step after it.
			cmd := inner(context.Background(), name, arg...) //nolint:contextcheck // the detached manifest child IS the scenario
			cancel()
			return cmd
		}
		return inner(c, name, arg...)
	}

	_, err := applier.Apply(pkg, false)
	if err == nil {
		t.Fatal("an interrupted run under require_proof reported no error at all")
	}

	msg := err.Error()
	if !strings.Contains(msg, "interrupted") {
		t.Errorf("the refusal does not mention the interruption: %s", msg)
	}
	if strings.Contains(msg, "proof at depth") {
		t.Errorf("the refusal blames `require_proof` for a Ctrl-C, sending the operator to change a policy "+
			"that had nothing to do with it: %s", msg)
	}
}
