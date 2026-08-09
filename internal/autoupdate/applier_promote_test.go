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
	failingAndWatching := func(ctx context.Context, name string, arg ...string) *exec.Cmd {
		watch.spawns++
		watch.hashesAtSpawn = append(watch.hashesAtSpawn, hashOverlayTree(t, overlayDir))
		if _, err := os.Stat(candidate); err == nil {
			watch.candidateSeenAt = append(watch.candidateSeenAt, name+" "+strings.Join(arg, " "))
		}
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
