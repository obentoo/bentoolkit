package autoupdate

// Authored for story 035, sub-task 3.1 — R3.1, and R2.1/R2.2 on this path.
//
// A CORRECTION TO THE PLAN'S TARGET FILE, on the same grounds
// applier_golden_test.go already records for story 033. tasks.md names
// `cmd/bentoo/overlay_autoupdate_check_test.go`, and this assertion cannot live
// there: the cases in that file drive the cobra command against a stubbed
// runner, so they never construct an Applier and never reach the manifest step
// whose distdir is the whole subject here. `(*Applier).Validate` — the `--check`
// runner — lives in package autoupdate, so the test does too.
//
// # Why --check needs the same rule and not a lighter one
//
// `--check` publishes nothing, which makes its failure mode quieter rather than
// smaller. An apply that reads an empty distdir publishes an unread bump and the
// damage is visible in the overlay; a check that reads an empty distdir prints
// "proved" for a bump nothing read, the operator believes the sweep, and the
// apply that follows is authorised by a report that measured nothing.
//
// Both callers run the same runStaticGates against the same staged tree. One
// rule, both callers.

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/obentoo/bentoolkit/internal/autoupdate/validate"
)

// TestCheckPath_ReadsWhatTheRunFetched is the golden pair again, through
// Validate instead of Apply, on a host that has never fetched the release.
func TestCheckPath_ReadsWhatTheRunFetched(t *testing.T) {
	sandbox := t.TempDir()
	prev := fixSandboxRoot
	fixSandboxRoot = func() string { return sandbox }
	t.Cleanup(func() { fixSandboxRoot = prev })

	applier, _, overlayDir, _ := goldenApplyFixtureWith(t, goldenFixtureOpts{
		omitCandidateArchive: true,
		fetchingPkgdev:       true,
	})
	const pkg = "media-plugins/gst-plugins-qt6"

	before := hashOverlayTree(t, overlayDir)
	result := applier.Validate(pkg, validate.DepthOptions)

	// The option gate must have an OPINION. Before this story it reported
	// SKIPPED — "the only distfile present does not belong to version 1.29.2" —
	// which on the apply path promotes and here reads as "nothing was wrong".
	var options *validate.GateResult
	for i := range result.Gates {
		if result.Gates[i].Gate == validate.GateOptions {
			options = &result.Gates[i]
			break
		}
	}
	if options == nil {
		t.Fatalf("the check produced no option gate at all: %+v", result.Gates)
	}
	if options.Outcome != validate.OutcomeFailed {
		t.Errorf("the option gate reported %s, want FAILED\nreason: %q\n"+
			"upstream 1.29.2 declares neither aalib nor libcaca and the ebuild passes both; a gate that could not "+
			"read the archive this very run fetched is answering about nothing",
			options.Outcome, options.Reason)
	}

	detail := options.Reason
	for _, f := range options.Findings {
		detail += " " + f.Detail
	}
	for _, want := range []string{"aalib", "libcaca"} {
		if !strings.Contains(detail, want) {
			t.Errorf("the option gate does not name %q: %q", want, detail)
		}
	}

	// R2.1/R2.2 on this path: the fetched distfiles do not outlive the run.
	entries, err := os.ReadDir(sandbox)
	if err != nil {
		t.Fatalf("reading the sandbox root: %v", err)
	}
	var leaked []string
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), "bentoo-staged-distfiles-") {
			leaked = append(leaked, e.Name())
		}
	}
	if len(leaked) != 0 {
		t.Errorf("the check left %v under the sandbox root; a sweep over the whole registry would keep one "+
			"archive per package it never asked to keep", leaked)
	}

	// And the promise --check exists for: nothing is published, whatever the
	// gates said.
	if after := hashOverlayTree(t, overlayDir); after != before {
		t.Errorf("--check changed the published overlay: %s -> %s", before, after)
	}
	candidate := filepath.Join(overlayDir, "media-plugins", "gst-plugins-qt6", "gst-plugins-qt6-1.29.2.ebuild")
	if _, err := os.Stat(candidate); err == nil {
		t.Errorf("--check wrote the candidate into the published overlay at %q", candidate)
	}
}

// MERGE FRAGMENT — story 037, sub-task 3.2 (--check parity and concurrent
// isolation).
//
// Target file: internal/autoupdate/applier_check_distdir_test.go (APPEND at
// the end). Do NOT repeat the `package autoupdate` clause.
//
// IMPORTS MERGE, necessary and sufficient: "context", "os/exec" and "sync"
// join the target's existing block (os, path/filepath, strings, testing, and
// the validate import). One block, not two.
//
// # Symbols
//
// Added: parityGateFixture and the three tests. Borrowed, never re-declared:
// createTestEbuildFileWithContent (applier_test.go), goldenApplyEbuild and
// writeGoldenArchive (applier_golden_test.go), hashOverlayTree
// (applier_promote_test.go). Sub-task 3.1's fragment (applier_gates_test.go)
// is NOT borrowed from: this fragment must materialise and run on its own,
// whichever lands first, so the fixture is its own — under a distinct name.
//
// # PINNED CONTRACT (design D7 — S037-R3.3, R3.4, R3.5)
//
// `--check` reaches runStaticGates through its own driver (applier_check.go),
// so the per-bump seam values must be produced for BOTH drivers or --check
// silently regresses to SKIPPED — 035's commit 66de148 is the precedent, and
// the quiet failure mode is the worse one: a check that reads nothing prints
// "proved", and the apply that follows is authorised by a report that measured
// nothing. Concurrency: seam values ride candidatePaths, never Applier fields
// (S035-D2), and the two-package concurrent apply below is what -race holds
// that promise against.
//
// VALIDATION COMMAND for this fragment: go test ./internal/autoupdate/ -race
// -run 'TestCheckPath_WriteFree|TestCheckPath_Unproducible|TestApplyGates_Concurrent' -count=1

// parityGateFixture lays out the golden bump with a pkgdev seam that seals the
// staged package directory read-only the moment the manifest step is spawned —
// after staging, before the static gates — and answers with an applier whose
// depth stops at the static gates. `restore` lifts the seal; call it before a
// second driver restages the same bump, or Stage's replace-not-accumulate
// RemoveAll dies on the sealed directory.
func parityGateFixture(t *testing.T) (applier *Applier, overlayDir string, pkg string, restore func()) {
	t.Helper()
	tmp := t.TempDir()
	overlayDir = filepath.Join(tmp, "overlay")
	configDir := filepath.Join(tmp, "config")
	distdir := filepath.Join(tmp, "distdir")
	staging := filepath.Join(tmp, "staging")
	pkg = "media-plugins/gst-plugins-qt6"

	createTestEbuildFileWithContent(t, overlayDir, pkg, "1.28.6", goldenApplyEbuild)
	pkgDir := filepath.Join(overlayDir, "media-plugins", "gst-plugins-qt6")
	manifest := "DIST gst-plugins-good-1.28.6.tar.gz 100 BLAKE2B ab SHA512 cd\n" +
		"DIST gst-plugins-good-1.29.2.tar.gz 100 BLAKE2B ef SHA512 01\n"
	if err := os.WriteFile(filepath.Join(pkgDir, "Manifest"), []byte(manifest), 0o644); err != nil {
		t.Fatalf("writing the published Manifest: %v", err)
	}
	if err := os.MkdirAll(distdir, 0o755); err != nil {
		t.Fatalf("creating distdir: %v", err)
	}
	writeGoldenArchive(t, distdir, "gst-plugins-good-1.28.6.tar.gz", "gst-plugins-good-1.28.6",
		[]string{"qt6", "aalib", "libcaca"})
	writeGoldenArchive(t, distdir, "gst-plugins-good-1.29.2.tar.gz", "gst-plugins-good-1.29.2",
		[]string{"qt6"})

	pending, err := NewPendingList(configDir)
	if err != nil {
		t.Fatalf("creating pending list: %v", err)
	}
	pending.Add(PendingUpdate{Package: pkg, CurrentVersion: "1.28.6", NewVersion: "1.29.2", Status: StatusPending})

	var mu sync.Mutex
	var sealed []string
	restore = func() {
		mu.Lock()
		defer mu.Unlock()
		for _, dir := range sealed {
			_ = os.Chmod(dir, 0o755)
		}
		sealed = nil
	}
	t.Cleanup(restore)

	seam := func(ctx context.Context, name string, arg ...string) *exec.Cmd {
		if name == "pkgdev" {
			var dir string
			_ = filepath.Walk(staging, func(path string, info os.FileInfo, err error) error {
				if err != nil {
					return nil
				}
				if !info.IsDir() && info.Name() == "gst-plugins-qt6-1.29.2.ebuild" {
					dir = filepath.Dir(path)
				}
				return nil
			})
			if dir != "" {
				if err := os.Chmod(dir, 0o555); err == nil {
					mu.Lock()
					sealed = append(sealed, dir)
					mu.Unlock()
				}
			}
		}
		return exec.CommandContext(ctx, "true")
	}

	applier, err = NewApplier(overlayDir, configDir,
		WithApplierPendingList(pending),
		WithExecCommand(seam),
		WithConfirmFunc(func(string) bool { return true }),
		WithApplierStagingRoot(staging),
		WithApplierDistdir(distdir, ""),
		WithApplierDepth(validate.DepthOptions),
	)
	if err != nil {
		t.Fatalf("creating applier: %v", err)
	}
	return applier, overlayDir, pkg, restore
}

// TestCheckPath_WriteFreeGateParityBetweenCheckAndApply is R3.3 on the fixture
// where the two drivers CAN diverge: a staged tree without a Manifest whose
// package directory cannot be written to. Both drivers must answer FAILED with
// the same option names — a check that answers SKIPPED where the apply answers
// FAILED (or the reverse) is the silent regression the seam must be produced
// on both drivers to prevent.
//
// The two drivers run against SEPARATE fixtures on purpose: parity is a claim
// about the same input shape, not about shared applier state, and sharing one
// applier would entangle this case with the retained-tree reuse rule (R10.1),
// which is not what R3.3 is about.
func TestCheckPath_WriteFreeGateParityBetweenCheckAndApply(t *testing.T) {
	// The check driver.
	checker, checkOverlay, pkg, _ := parityGateFixture(t)
	beforeCheck := hashOverlayTree(t, checkOverlay)
	res := checker.Validate(pkg, validate.DepthOptions)

	var options *validate.GateResult
	for i := range res.Gates {
		if res.Gates[i].Gate == validate.GateOptions {
			options = &res.Gates[i]
			break
		}
	}
	if options == nil {
		t.Fatalf("the check produced no option gate at all: %+v", res.Gates)
	}
	if options.Outcome != validate.OutcomeFailed {
		t.Errorf("--check option gate: got %s (reason %q), want FAILED — the gate had the published names "+
			"and the candidate's archive to answer from, and needed to write nothing (R3.3, R3.2)",
			options.Outcome, options.Reason)
	}
	checkDetail := options.Reason
	for _, f := range options.Findings {
		checkDetail += " " + f.Detail
	}
	for _, want := range []string{"aalib", "libcaca"} {
		if !strings.Contains(checkDetail, want) {
			t.Errorf("the check's option gate does not name %q: %q", want, checkDetail)
		}
	}
	if after := hashOverlayTree(t, checkOverlay); after != beforeCheck {
		t.Errorf("--check changed the published overlay: %s -> %s", beforeCheck, after)
	}

	// The apply driver, same input shape, fresh fixture.
	applier, applyOverlay, pkg2, _ := parityGateFixture(t)
	before := hashOverlayTree(t, applyOverlay)
	result, _ := applier.Apply(pkg2, false)

	if result.Success {
		t.Fatal("the apply PUBLISHED the bump the check just failed; --check and --apply must produce the " +
			"same outcomes from the same names (R3.3)")
	}
	msg := ""
	if result.Error != nil {
		msg = result.Error.Error()
	}
	for _, want := range []string{"aalib", "libcaca"} {
		if !strings.Contains(msg, want) {
			t.Errorf("the apply's refusal %q does not name %q; the two drivers' answers must carry the same "+
				"names (R3.3)", msg, want)
		}
	}
	if after := hashOverlayTree(t, applyOverlay); after != before {
		t.Errorf("the refused bump changed the published overlay: %s -> %s", before, after)
	}
}

// TestCheckPath_UnproducibleNamesSkipNamesThePackage is R3.5: when the
// published names cannot be produced — no published Manifest, no staged one,
// nothing fetched — the gate is a reported SKIP naming the package, preserving
// the error→SKIPPED mapping the lend used to provide.
//
// EXPECT THIS GREEN ON ARRIVAL: today's path reaches an equivalent SKIP
// through the lend's no-op branch, so this is a REGRESSION PIN across the
// lend's deletion (design D4). In Run mode, prove it by mutation: make the
// seam producer swallow its error into an empty success and confirm the skip
// stops naming what failed.
func TestCheckPath_UnproducibleNamesSkipNamesThePackage(t *testing.T) {
	tmp := t.TempDir()
	overlayDir := filepath.Join(tmp, "overlay")
	configDir := filepath.Join(tmp, "config")
	const pkg = "media-plugins/gst-plugins-qt6"

	// A published package directory WITHOUT a Manifest: nothing can name the
	// candidate's archives, on any path. The distdir EXISTS and is empty on
	// purpose — an absent distdir skips earlier, for a different reason, and
	// this case is about the names, not the directory.
	createTestEbuildFileWithContent(t, overlayDir, pkg, "1.28.6", goldenApplyEbuild)
	emptyDistdir := filepath.Join(tmp, "empty-distdir")
	if err := os.MkdirAll(emptyDistdir, 0o755); err != nil {
		t.Fatalf("creating the empty distdir: %v", err)
	}

	pending, err := NewPendingList(configDir)
	if err != nil {
		t.Fatalf("creating pending list: %v", err)
	}
	pending.Add(PendingUpdate{Package: pkg, CurrentVersion: "1.28.6", NewVersion: "1.29.2", Status: StatusPending})

	applier, err := NewApplier(overlayDir, configDir,
		WithApplierPendingList(pending),
		WithExecCommand(func(ctx context.Context, name string, arg ...string) *exec.Cmd {
			return exec.CommandContext(ctx, "true")
		}),
		WithConfirmFunc(func(string) bool { return true }),
		WithApplierStagingRoot(filepath.Join(tmp, "staging")),
		WithApplierDistdir(emptyDistdir, ""),
		WithApplierDepth(validate.DepthOptions),
	)
	if err != nil {
		t.Fatalf("creating applier: %v", err)
	}

	res := applier.Validate(pkg, validate.DepthOptions)
	var options *validate.GateResult
	for i := range res.Gates {
		if res.Gates[i].Gate == validate.GateOptions {
			options = &res.Gates[i]
			break
		}
	}
	if options == nil {
		t.Fatalf("the check produced no option gate at all: %+v", res.Gates)
	}
	if options.Outcome != validate.OutcomeSkipped {
		t.Errorf("option gate: got %s (reason %q), want SKIPPED — no source can name this package's archives, "+
			"and anything but a skip claims a measurement that never happened (R3.5)", options.Outcome, options.Reason)
	}
	if !strings.Contains(options.Reason, "gst-plugins-qt6") {
		t.Errorf("the skip %q does not name the package it is about; inside a sweep of forty packages an "+
			"anonymous skip is unactionable (R3.5)", options.Reason)
	}
}

// TestApplyGates_ConcurrentAppliesKeepSeamValuesPerBump is R3.4, in the shape
// applyAllPackages actually runs: two bumps, one applier, concurrent applies.
// Package A's gate must answer from A's names (FAILED — its archive dropped
// aalib and libcaca) and package B's from B's (PASS — its archive declares
// exactly what its ebuild passes). A seam value parked on an Applier field
// hands one package's names to the other: B's gate then finds no name carrying
// its version and SKIPS — visible below as DepthReached "none" instead of
// "options" — and -race sees the shared write.
//
// EXPECT THIS GREEN ON ARRIVAL — it is the guard that keeps D7 true under
// change, mirroring TestApplyGates_NoFetchedDistfilesSurviveAnApply's shape.
// In Run mode, prove it by mutation: move the seam values onto an Applier
// field and confirm this fails (or races).
func TestApplyGates_ConcurrentAppliesKeepSeamValuesPerBump(t *testing.T) {
	tmp := t.TempDir()
	overlayDir := filepath.Join(tmp, "overlay")
	configDir := filepath.Join(tmp, "config")
	distdir := filepath.Join(tmp, "distdir")
	const pkgA = "media-plugins/gst-plugins-qt6"
	const pkgB = "media-libs/goodlib"

	createTestEbuildFileWithContent(t, overlayDir, pkgA, "1.28.6", goldenApplyEbuild)
	manifestA := "DIST gst-plugins-good-1.28.6.tar.gz 100 BLAKE2B ab SHA512 cd\n" +
		"DIST gst-plugins-good-1.29.2.tar.gz 100 BLAKE2B ef SHA512 01\n"
	if err := os.WriteFile(filepath.Join(overlayDir, "media-plugins", "gst-plugins-qt6", "Manifest"),
		[]byte(manifestA), 0o644); err != nil {
		t.Fatalf("writing A's Manifest: %v", err)
	}

	createTestEbuildFileWithContent(t, overlayDir, pkgB, "1.0", "emesonargs=(\n\t-Dqt6=enabled\n)\n")
	manifestB := "DIST goodlib-1.0.tar.gz 100 BLAKE2B ab SHA512 cd\n" +
		"DIST goodlib-2.0.tar.gz 100 BLAKE2B ef SHA512 01\n"
	if err := os.WriteFile(filepath.Join(overlayDir, "media-libs", "goodlib", "Manifest"),
		[]byte(manifestB), 0o644); err != nil {
		t.Fatalf("writing B's Manifest: %v", err)
	}

	if err := os.MkdirAll(distdir, 0o755); err != nil {
		t.Fatalf("creating distdir: %v", err)
	}
	writeGoldenArchive(t, distdir, "gst-plugins-good-1.29.2.tar.gz", "gst-plugins-good-1.29.2", []string{"qt6"})
	writeGoldenArchive(t, distdir, "goodlib-2.0.tar.gz", "goodlib-2.0", []string{"qt6"})

	pending, err := NewPendingList(configDir)
	if err != nil {
		t.Fatalf("creating pending list: %v", err)
	}
	pending.Add(PendingUpdate{Package: pkgA, CurrentVersion: "1.28.6", NewVersion: "1.29.2", Status: StatusPending})
	pending.Add(PendingUpdate{Package: pkgB, CurrentVersion: "1.0", NewVersion: "2.0", Status: StatusPending})

	applier, err := NewApplier(overlayDir, configDir,
		WithApplierPendingList(pending),
		WithExecCommand(func(ctx context.Context, name string, arg ...string) *exec.Cmd {
			return exec.CommandContext(ctx, "true")
		}),
		WithConfirmFunc(func(string) bool { return true }),
		WithApplierStagingRoot(filepath.Join(tmp, "staging")),
		WithApplierDistdir(distdir, ""),
		WithApplierDepth(validate.DepthOptions),
	)
	if err != nil {
		t.Fatalf("creating applier: %v", err)
	}

	var wg sync.WaitGroup
	var resultA, resultB *ApplyResult
	wg.Add(2)
	go func() {
		defer wg.Done()
		resultA, _ = applier.Apply(pkgA, false)
	}()
	go func() {
		defer wg.Done()
		resultB, _ = applier.Apply(pkgB, false)
	}()
	wg.Wait()

	if resultA.Success {
		t.Error("package A was published although ITS archive declares neither aalib nor libcaca; if A's gate " +
			"answered from B's names it could only skip, and a skip publishes (R3.4)")
	}
	msgA := ""
	if resultA.Error != nil {
		msgA = resultA.Error.Error()
	}
	if !strings.Contains(msgA, "aalib") {
		t.Errorf("A's refusal %q does not name aalib; A's gate must have answered from A's own archive", msgA)
	}
	if strings.Contains(msgA, "goodlib") {
		t.Errorf("A's refusal %q mentions package B's archive; one bump's seam values leaked into the other's "+
			"gate (R3.4)", msgA)
	}

	if !resultB.Success {
		t.Errorf("package B was refused (%v) although its archive declares exactly what its ebuild passes; "+
			"a leak of A's names into B's gate produces exactly this", resultB.Error)
	}
	if resultB.DepthReached != "options" {
		t.Errorf("B's DepthReached = %q, want %q — a PASS, not a promoted skip: B's gate answering from A's "+
			"names finds no name carrying version 2.0 and skips, and DepthReached is where that shows (R3.4)",
			resultB.DepthReached, "options")
	}
}
