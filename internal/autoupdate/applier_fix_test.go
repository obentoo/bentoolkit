package autoupdate

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/obentoo/bentoolkit/internal/common/distfiles"
)

// TestApply_ManifestFix_Recovers exercises the happy path: the first `pkgdev
// manifest` fails, the wired fixer "repairs" the ebuild, and bentoo's own re-run
// of the manifest then succeeds. The apply must succeed with Fixed/FixSummary set
// and the pending entry removed.
func TestApply_ManifestFix_Recovers(t *testing.T) {
	tmpDir := t.TempDir()
	overlayDir := filepath.Join(tmpDir, "overlay")
	configDir := filepath.Join(tmpDir, "config")

	pkg := "dev-games/godot"
	oldVersion := "4.7_rc3"
	newVersion := "4.7"

	createTestEbuildFile(t, overlayDir, pkg, oldVersion)

	pending, _ := NewPendingList(configDir)
	pending.Add(PendingUpdate{
		Package:        pkg,
		CurrentVersion: oldVersion,
		NewVersion:     newVersion,
		Status:         StatusPending,
	})

	fixer := &fakeFixer{summary: "rewrote SRC_URI to the 4.7-stable asset"}

	applier, err := NewApplier(overlayDir, configDir,
		WithApplierPendingList(pending),
		WithExecCommand(pkgdevFlakySeam()),
		WithApplierFixer(fixer),
	)
	if err != nil {
		t.Fatalf("NewApplier: %v", err)
	}

	result, err := applier.Apply(pkg, false)
	if err != nil {
		t.Fatalf("Apply returned error: %v", err)
	}
	if !result.Success {
		t.Fatalf("expected success after fix, got failure: %v", result.Error)
	}
	if !result.Fixed {
		t.Error("expected result.Fixed = true")
	}
	if result.FixSummary != fixer.summary {
		t.Errorf("FixSummary = %q, want %q", result.FixSummary, fixer.summary)
	}
	if fixer.called != 1 {
		t.Errorf("fixer called %d times, want exactly 1", fixer.called)
	}

	// The new ebuild must remain in place (not rolled back).
	if _, statErr := os.Stat(applier.EbuildPath(pkg, newVersion)); statErr != nil {
		t.Errorf("new ebuild missing after successful fix: %v", statErr)
	}
	// Pending entry removed on full success.
	if _, found := pending.Get(pkg); found {
		t.Error("expected pending entry to be removed after successful apply")
	}

	// The fixer received a well-formed request scoped to the package directory.
	wantPkgDir := filepath.Join(overlayDir, "dev-games", "godot")
	if fixer.lastReq.PkgDir != wantPkgDir {
		t.Errorf("fixer PkgDir = %q, want %q", fixer.lastReq.PkgDir, wantPkgDir)
	}
	if fixer.lastReq.Version != newVersion {
		t.Errorf("fixer Version = %q, want %q", fixer.lastReq.Version, newVersion)
	}
	if fixer.lastReq.DistDir == "" {
		t.Error("fixer DistDir should be a non-empty temp dir")
	}
}

// TestApply_ManifestFix_QAAdvisory verifies the post-fix QA gate: after a
// successful fix+manifest, pkgcheck findings are captured into QASummary as an
// advisory without flipping Success.
func TestApply_ManifestFix_QAAdvisory(t *testing.T) {
	// Make lookPath("pkgcheck") succeed regardless of host so the QA gate runs.
	stubLookPathFound(t)

	tmpDir := t.TempDir()
	overlayDir := filepath.Join(tmpDir, "overlay")
	configDir := filepath.Join(tmpDir, "config")

	pkg := "dev-games/godot"
	oldVersion := "4.7_rc3"
	newVersion := "4.7"
	createTestEbuildFile(t, overlayDir, pkg, oldVersion)

	pending, _ := NewPendingList(configDir)
	pending.Add(PendingUpdate{
		Package:        pkg,
		CurrentVersion: oldVersion,
		NewVersion:     newVersion,
		Status:         StatusPending,
	})

	// pkgdev: first call fails, then succeeds; pkgcheck: prints a finding.
	const finding = "WARNING: SourceUrlWarning: stale mirror"
	calls := 0
	seam := func(ctx context.Context, name string, arg ...string) *exec.Cmd {
		switch name {
		case "pkgdev":
			calls++
			if calls == 1 {
				return exec.CommandContext(ctx, "false")
			}
			return exec.CommandContext(ctx, "true")
		case "pkgcheck":
			return exec.CommandContext(ctx, "sh", "-c", "printf '%s' '"+finding+"'; exit 1")
		default:
			return exec.CommandContext(ctx, "true")
		}
	}

	fixer := &fakeFixer{summary: "fixed SRC_URI"}
	applier, err := NewApplier(overlayDir, configDir,
		WithApplierPendingList(pending),
		WithExecCommand(seam),
		WithApplierFixer(fixer),
	)
	if err != nil {
		t.Fatalf("NewApplier: %v", err)
	}

	result, err := applier.Apply(pkg, false)
	if err != nil {
		t.Fatalf("Apply returned error: %v", err)
	}
	if !result.Success || !result.Fixed {
		t.Fatalf("expected Success && Fixed, got success=%v fixed=%v err=%v", result.Success, result.Fixed, result.Error)
	}
	if !strings.Contains(result.QASummary, "SourceUrlWarning") {
		t.Errorf("QASummary = %q, want it to carry the pkgcheck finding", result.QASummary)
	}
}

// TestApply_ManifestFix_StillFails covers the case where the fixer runs but the
// authoritative manifest re-run still fails: the apply must fail, the orphan
// ebuild must be rolled back, and the pending entry marked failed.
func TestApply_ManifestFix_StillFails(t *testing.T) {
	tmpDir := t.TempDir()
	overlayDir := filepath.Join(tmpDir, "overlay")
	configDir := filepath.Join(tmpDir, "config")

	pkg := "dev-games/godot"
	oldVersion := "4.7_rc3"
	newVersion := "4.7"

	createTestEbuildFile(t, overlayDir, pkg, oldVersion)

	pending, _ := NewPendingList(configDir)
	pending.Add(PendingUpdate{
		Package:        pkg,
		CurrentVersion: oldVersion,
		NewVersion:     newVersion,
		Status:         StatusPending,
	})

	// Fixer "succeeds" but the manifest keeps failing (mockExecCommandFailure
	// fails every pkgdev call).
	fixer := &fakeFixer{summary: "tried, but no luck"}

	applier, err := NewApplier(overlayDir, configDir,
		WithApplierPendingList(pending),
		WithExecCommand(mockExecCommandFailure),
		WithApplierFixer(fixer),
	)
	if err != nil {
		t.Fatalf("NewApplier: %v", err)
	}

	result, applyErr := applier.Apply(pkg, false)
	if applyErr == nil {
		t.Fatal("expected an error when the manifest still fails after a fix")
	}
	if result.Success {
		t.Error("expected result.Success = false")
	}
	if result.Fixed {
		t.Error("Fixed must stay false when the post-fix manifest still fails")
	}
	if !errors.Is(applyErr, ErrManifestFailed) {
		t.Errorf("error %v should wrap ErrManifestFailed", applyErr)
	}
	if fixer.called != 1 {
		t.Errorf("fixer called %d times, want exactly 1", fixer.called)
	}
	// Orphan ebuild rolled back.
	if _, statErr := os.Stat(applier.EbuildPath(pkg, newVersion)); !os.IsNotExist(statErr) {
		t.Errorf("expected new ebuild to be rolled back, stat err = %v", statErr)
	}
	if update, _ := pending.Get(pkg); update.Status != StatusFailed {
		t.Errorf("pending status = %q, want %q", update.Status, StatusFailed)
	}
}

// TestApply_ManifestFix_FixerErrors covers the fixer itself returning an error:
// the apply fails like the no-fixer case (the original manifest failure is
// preserved and the fixer error is appended).
func TestApply_ManifestFix_FixerErrors(t *testing.T) {
	tmpDir := t.TempDir()
	overlayDir := filepath.Join(tmpDir, "overlay")
	configDir := filepath.Join(tmpDir, "config")

	pkg := "dev-games/godot"
	oldVersion := "4.7_rc3"
	newVersion := "4.7"

	createTestEbuildFile(t, overlayDir, pkg, oldVersion)

	pending, _ := NewPendingList(configDir)
	pending.Add(PendingUpdate{
		Package:        pkg,
		CurrentVersion: oldVersion,
		NewVersion:     newVersion,
		Status:         StatusPending,
	})

	fixer := &fakeFixer{err: errors.New("claude CLI exploded")}

	applier, err := NewApplier(overlayDir, configDir,
		WithApplierPendingList(pending),
		WithExecCommand(mockExecCommandFailure),
		WithApplierFixer(fixer),
	)
	if err != nil {
		t.Fatalf("NewApplier: %v", err)
	}

	result, applyErr := applier.Apply(pkg, false)
	if applyErr == nil || result.Success {
		t.Fatal("expected failure when the fixer errors")
	}
	if !errors.Is(applyErr, ErrManifestFailed) {
		t.Errorf("error %v should wrap ErrManifestFailed", applyErr)
	}
	if update, _ := pending.Get(pkg); update.Status != StatusFailed {
		t.Errorf("pending status = %q, want %q", update.Status, StatusFailed)
	}
}

// TestApply_NoFixer_Unchanged confirms that without a fixer the manifest-failure
// path is exactly the legacy behaviour (no fix attempt, Fixed stays false).
func TestApply_NoFixer_Unchanged(t *testing.T) {
	tmpDir := t.TempDir()
	overlayDir := filepath.Join(tmpDir, "overlay")
	configDir := filepath.Join(tmpDir, "config")

	pkg := "test-cat/test-pkg"
	oldVersion := "1.0.0"
	newVersion := "2.0.0"

	createTestEbuildFile(t, overlayDir, pkg, oldVersion)

	pending, _ := NewPendingList(configDir)
	pending.Add(PendingUpdate{
		Package:        pkg,
		CurrentVersion: oldVersion,
		NewVersion:     newVersion,
		Status:         StatusPending,
	})

	applier, err := NewApplier(overlayDir, configDir,
		WithApplierPendingList(pending),
		WithExecCommand(mockExecCommandFailure),
	)
	if err != nil {
		t.Fatalf("NewApplier: %v", err)
	}

	result, applyErr := applier.Apply(pkg, false)
	if applyErr == nil || result.Success {
		t.Fatal("expected the legacy manifest failure")
	}
	if result.Fixed {
		t.Error("Fixed must be false when no fixer is wired")
	}
	if !errors.Is(applyErr, ErrManifestFailed) {
		t.Errorf("error %v should wrap ErrManifestFailed", applyErr)
	}
}

// TestSubstituteCommitHash_AlreadyCorrect covers a pure base correction: the
// version moves (mesa 26.2.0 → 26.3.0) while upstream's HEAD has not, so the
// hash to write is the one already in the file. That used to be reported as
// "no commit hash variable found" — naming a variable that is sitting right
// there, and failing an apply that had nothing wrong with it.
func TestSubstituteCommitHash_AlreadyCorrect(t *testing.T) {
	sha := "bc99a094bd926ae7b5ab8643947ce1438c950720"
	dir := t.TempDir()
	path := filepath.Join(dir, "mesa-26.3.0_pre20260801.ebuild")
	body := "EAPI=8\n\tGIT_COMMIT=\"" + sha + "\"\n\tSRC_URI=\"…/${GIT_COMMIT}.tar.gz\"\n"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}

	if err := substituteCommitHash(path, sha); err != nil {
		t.Fatalf("substituteCommitHash with an already-correct hash: %v", err)
	}

	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if string(got) != body {
		t.Errorf("file was rewritten; want it left byte-for-byte identical")
	}
}

// TestApply_SubstitutionFailureLeavesNoOrphan pins the second half of the
// asus-ec-sensors incident. The substitution runs AFTER the ebuild has been copied
// into the published package directory, and prepareInOverlay used to return the
// failure without the rollback — so Apply had nothing to arm and the copy stayed.
// In the overlay that produced this test, the leftover ebuild was then
// auto-committed and pushed: a new version pinning the OLD commit, with no
// Manifest entry, so any emerge of it fails on the digest.
//
// The failure is provoked the way the real one arose: a pending update carrying a
// CommitHash against an ebuild that declares no commit variable at all.
func TestApply_SubstitutionFailureLeavesNoOrphan(t *testing.T) {
	tmpDir := t.TempDir()
	overlayDir := filepath.Join(tmpDir, "overlay")
	configDir := filepath.Join(tmpDir, "config")

	pkg := "sys-apps/demo"
	oldVersion := "0_p20260711"
	newVersion := "0_p20260809"

	createTestEbuildFile(t, overlayDir, pkg, oldVersion)

	pending, _ := NewPendingList(configDir)
	pending.Add(PendingUpdate{
		Package:        pkg,
		CurrentVersion: oldVersion,
		NewVersion:     newVersion,
		CommitHash:     "bc99a094bd926ae7b5ab8643947ce1438c950720",
		Status:         StatusPending,
	})

	applier, err := NewApplier(overlayDir, configDir,
		WithApplierPendingList(pending),
		WithExecCommand(mockExecCommandSuccess),
	)
	if err != nil {
		t.Fatalf("NewApplier: %v", err)
	}

	result, applyErr := applier.Apply(pkg, false)
	if applyErr == nil || result.Success {
		t.Fatal("expected the substitution to fail on an ebuild with no commit variable")
	}
	if !strings.Contains(applyErr.Error(), "no commit hash variable") {
		t.Errorf("error = %v, want the missing-variable failure", applyErr)
	}

	// The whole point: nothing of the failed apply survives in the overlay.
	orphan := applier.EbuildPath(pkg, newVersion)
	if _, statErr := os.Stat(orphan); !errors.Is(statErr, fs.ErrNotExist) {
		t.Errorf("orphan ebuild %s survived a failed substitution (stat: %v)", orphan, statErr)
	}
	// And the version it was bumped FROM is untouched.
	if _, statErr := os.Stat(applier.EbuildPath(pkg, oldVersion)); statErr != nil {
		t.Errorf("the source ebuild was disturbed by the rollback: %v", statErr)
	}
}

// TestSubstituteCommitHash_QuotedCOMMIT covers the spelling that fell between the
// two patterns: COMMIT= WITH quotes. The unquoted pattern was the only one that
// named COMMIT and it requires the SHA to start right after the "=", while the
// quoted pattern listed the other three names — so sys-apps/asus-ec-sensors, whose
// ebuild pins COMMIT="<sha>", was told its commit variable did not exist.
func TestSubstituteCommitHash_QuotedCOMMIT(t *testing.T) {
	const (
		oldSHA = "503d0d3d3858f463973f2cfce4a3aa0173567500"
		newSHA = "bc99a094bd926ae7b5ab8643947ce1438c950720"
	)
	dir := t.TempDir()
	path := filepath.Join(dir, "asus-ec-sensors-0_p20260809.ebuild")
	body := "EAPI=8\nCOMMIT=\"" + oldSHA + "\"\n" +
		"SRC_URI=\"https://github.com/zeule/${PN}/archive/${COMMIT}.tar.gz -> ${P}.tar.gz\"\n" +
		"S=\"${WORKDIR}/${PN}-${COMMIT}\"\n"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}

	if err := substituteCommitHash(path, newSHA); err != nil {
		t.Fatalf("substituteCommitHash on COMMIT=\"<sha>\": %v", err)
	}

	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if strings.Contains(string(got), oldSHA) {
		t.Error("the previous SHA survived; SRC_URI would still fetch the old tarball")
	}
	if !strings.Contains(string(got), "COMMIT=\""+newSHA+"\"") {
		t.Errorf("COMMIT= was not rewritten to the new SHA:\n%s", got)
	}
}

// TestSubstituteCommitHash_QuotedCOMMITKeepsPrefix pins the one hazard of adding a
// bare COMMIT to the quoted alternation: a match may start at the COMMIT inside
// EGIT_COMMIT/GIT_COMMIT. That is harmless only because group 1 is written back
// verbatim, so the prefix is reproduced rather than eaten — which is a property of
// the replacement, not of the alternation, and therefore worth a test.
func TestSubstituteCommitHash_QuotedCOMMITKeepsPrefix(t *testing.T) {
	const (
		oldSHA = "503d0d3d3858f463973f2cfce4a3aa0173567500"
		newSHA = "bc99a094bd926ae7b5ab8643947ce1438c950720"
	)
	for _, varName := range []string{"EGIT_COMMIT", "GIT_COMMIT", "BUILD_ID", "COMMIT"} {
		t.Run(varName, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "demo-1.0.ebuild")
			body := "EAPI=8\n" + varName + "=\"" + oldSHA + "\"\n"
			if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
				t.Fatalf("write: %v", err)
			}

			if err := substituteCommitHash(path, newSHA); err != nil {
				t.Fatalf("substituteCommitHash: %v", err)
			}

			got, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("read back: %v", err)
			}
			want := "EAPI=8\n" + varName + "=\"" + newSHA + "\"\n"
			if string(got) != want {
				t.Errorf("got:\n%s\nwant:\n%s", got, want)
			}
		})
	}
}

// TestSubstituteCommitHash_TrulyMissing keeps the real error reachable.
func TestSubstituteCommitHash_TrulyMissing(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "demo-1.0.ebuild")
	if err := os.WriteFile(path, []byte("EAPI=8\nSLOT=\"0\"\n"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}

	err := substituteCommitHash(path, "bc99a094bd926ae7b5ab8643947ce1438c950720")
	if err == nil {
		t.Fatal("expected an error when no commit variable exists")
	}
	if !strings.Contains(err.Error(), "no commit hash variable") {
		t.Errorf("error = %v, want it to name the missing variable", err)
	}
}

// =============================================================================
// The gate: an environment failure never reaches the LLM fixer
// (S030 sub-task 4.2 — R3.3, R3.4, R3.6)
//
// The run this exists for failed two packages on `Cannot write to
// '/tmp/bentoo-distfiles-…' (Success)` — a statement about a filesystem. Both
// ebuilds were correct, and both applied on a later run with nothing changed
// (M3a). The fixer was invoked anyway: ten minutes and up to thirty turns per
// package, holding wget and pkgdev, and its only reachable conclusion is that
// SRC_URI is wrong. This repository commits and pushes on its own, so that is
// how a correct ebuild gets edited and published.
//
// The tests below pin the gate at the one place that decides: between the failed
// manifest and the fix attempt, in runManifestWithFix.
// =============================================================================

// stubExhaustedDistdirSpace makes the classifier's free-space check answer "no
// room left at all" for the rest of the test, and restores the real query after.
//
// It replaces defaultSpaceQuery rather than filling a real filesystem, which is
// the only other way to reach this verdict. Two things follow from WHICH seam it
// replaces. The gate passes a nil SpaceFunc, as production must; a nil one
// normalises to defaultSpaceQuery inside ClassifyManifestFailure instead of
// disabling the check — so a stub that is never consulted would mean the gate
// had turned free-space checking off, and these tests would go green on a gate
// that had stopped looking. And nothing here touches a real distdir: the
// verdict comes from an answer, not from a full disk.
func stubExhaustedDistdirSpace(t *testing.T) {
	t.Helper()
	orig := defaultSpaceQuery
	defaultSpaceQuery = func(string) (uint64, error) { return 0, nil }
	t.Cleanup(func() { defaultSpaceQuery = orig })
}

// productionManifestFailure is what `pkgdev manifest` printed on the run this
// story was measured on. The parenthesised "(Success)" is errno 0 — the write
// failed and nothing set an error — and the sentence names a directory, not a
// URL. It is used verbatim so the tests can assert the operator still reads it
// even though the fixer never ran.
const productionManifestFailure = "Cannot write to '/tmp/bentoo-distfiles-4f2a/godot-4.7.tar.xz' (Success)"

// pkgdevFailsPrinting returns an exec seam whose `pkgdev` calls print out and
// exit non-zero; every other command succeeds. out is passed as an ARGUMENT to
// sh, never interpolated into the script, so quotes inside it cannot change what
// the shell runs.
func pkgdevFailsPrinting(out string) func(ctx context.Context, name string, arg ...string) *exec.Cmd {
	return func(ctx context.Context, name string, arg ...string) *exec.Cmd {
		if name != "pkgdev" {
			return exec.CommandContext(ctx, "true")
		}
		return exec.CommandContext(ctx, "sh", "-c", `printf '%s\n' "$0"; exit 1`, out)
	}
}

// pkgdevFailsUntilFixed returns an exec seam whose FIRST `pkgdev` call fails and
// whose every later one succeeds; every non-`pkgdev` command succeeds (S043).
//
// It is the two-sided seam the fix path needs: the first failure is what drives
// runManifestWithFix past refuseFixOnEnvironmentFailure and into the fixer, and
// the success that follows is what lets the authoritative re-check pass — so a
// test using it exercises a repair that WORKED, which is the case story 043 is
// about. A repair that failed is the other row, and pkgdevFailsPrinting is that.
//
// The failure PRINTS a fetch diagnostic rather than exiting silently, for the
// reason TestRepairableFailureStillInvokesTheFixer prints one: the environment
// classifier reads what pkgdev said, and a failure that says nothing about the
// machine is what makes the verdict "repairable" — the seam has to reach the
// fixer or the tests below cannot fail for their own reason. The text is passed
// as an ARGUMENT to sh, never interpolated into the script.
//
// The counter lives in the closure because the call site takes no arguments and
// returns one value; the mutex is there because applyAllPackages runs applies
// concurrently and a seam is shared by construction.
func pkgdevFailsUntilFixed() func(ctx context.Context, name string, arg ...string) *exec.Cmd {
	var mu sync.Mutex
	calls := 0
	return func(ctx context.Context, name string, arg ...string) *exec.Cmd {
		if name != "pkgdev" {
			return exec.CommandContext(ctx, "true")
		}
		mu.Lock()
		calls++
		first := calls == 1
		mu.Unlock()
		if first {
			return exec.CommandContext(ctx, "sh", "-c", `printf '%s\n' "$0"; exit 1`,
				"SRC_URI is unreachable: 404 Not Found")
		}
		return exec.CommandContext(ctx, "true")
	}
}

// gateDistdirFrom answers WHICH DIRECTORY THIS RUN'S GATES WERE POINTED AT, and
// "" when no gate of this run reported one (S043-R2.1, R2.4).
//
// # Why this observation point and not another
//
// It reads result.CompileDistdir, which recordCompileDistdir sets to
// staticGateDistdir(cand) — literally the same resolver the option gate is given
// through validate.Options.Distdir, called once per attempt so the directory
// REPORTED and the directory the child was handed are one resolution rather than
// two readings of a filesystem a moment apart (see buildAttempt). So a directory
// answered here is the directory the gates of this apply searched, which is the
// claim the tests using it make.
//
// The stage record beside a staged tree carries the same fact in its gate
// reasons — it is the artefact the 2026-08-22 defect was diagnosed from — and it
// would be the richer source. It is not reachable from this harness: newGateApplier
// builds an applier with NO staging root, runStaticGates returns nil for an
// unstaged candidate, and giving the harness a staging root would take the four
// story-030 tests that share it off the path their gate exists on (the pre-flight
// that raises ErrDistdirUnusable is runManifest's, and the staged manifest has
// none), turning six passing environment-classification tests red for a reason
// that has nothing to do with them.
//
// # The host dependency, stated rather than hidden
//
// CompileDistdir is populated only once a build child has actually spawned, and
// reaching that costs a privilege tool: detectPrivilegeTool (applier.go) calls
// exec.LookPath DIRECTLY, not through the a.lookPath seam, so it cannot be
// injected. On a host with neither sudo nor doas the compile gate returns before
// compileOnce and there is nothing to observe — so this SKIPS, in the same shape
// and for the same reason TestApplierCompileLogPreserved already skips. A skip is
// reported; a "" quietly returned would let a guard pass while observing nothing,
// which is the failure story 043 exists to correct.
func gateDistdirFrom(t *testing.T, res *ApplyResult) string {
	t.Helper()
	if !hasPrivilegeTool() {
		t.Skip("the compile gate's distdir is only observable where sudo/doas is on PATH (detectPrivilegeTool)")
	}
	if res == nil {
		return ""
	}
	return res.CompileDistdir
}

// gatePkg is one pending bump for newGateApplier.
type gatePkg struct{ name, from, to string }

// newGateApplier builds an applier over a fresh overlay carrying one pending
// bump per pkg, with a fixer double and a recording reporter attached — the
// harness the four gate tests share. The fixer is always wired, because "the
// fixer was available and was not used" is the claim under test.
func newGateApplier(t *testing.T, seam func(ctx context.Context, name string, arg ...string) *exec.Cmd, pkgs ...gatePkg) (*Applier, *fakeFixer, *recordingReporter) {
	t.Helper()

	tmpDir := t.TempDir()
	overlayDir := filepath.Join(tmpDir, "overlay")
	configDir := filepath.Join(tmpDir, "config")

	pending, err := NewPendingList(configDir)
	if err != nil {
		t.Fatalf("NewPendingList: %v", err)
	}
	for _, p := range pkgs {
		createTestEbuildFile(t, overlayDir, p.name, p.from)
		pending.Add(PendingUpdate{
			Package:        p.name,
			CurrentVersion: p.from,
			NewVersion:     p.to,
			Status:         StatusPending,
		})
	}

	fixer := &fakeFixer{summary: "rewrote SRC_URI"}
	rec := &recordingReporter{}
	applier, err := NewApplier(overlayDir, configDir,
		WithApplierPendingList(pending),
		WithExecCommand(seam),
		WithApplierFixer(fixer),
		WithApplierReporter(rec),
		// Answered explicitly (S043). The four story-030 tests apply with
		// compile=false and never reach the prompt, so nothing here changes for
		// them; what changes is that the tests which DO reach it stop being
		// answered by accident — the default reads os.Stdin, and under `go test`
		// that is a closed file whose EOF reads as "no", so a compile gate would
		// decline for a reason no test wrote down.
		WithConfirmFunc(func(string) bool { return true }),
	)
	if err != nil {
		t.Fatalf("NewApplier: %v", err)
	}
	return applier, fixer, rec
}

// TestEnvironmentFailureSkipsTheManifestFixer is R3.3, and it is the point of
// the story: with a fixer wired and willing, an environment failure must not
// reach it — not the invocation, and not the announcement that precedes it.
func TestEnvironmentFailureSkipsTheManifestFixer(t *testing.T) {
	stubExhaustedDistdirSpace(t)

	pkg := "dev-games/godot"
	applier, fixer, rec := newGateApplier(t,
		pkgdevFailsPrinting(productionManifestFailure),
		gatePkg{pkg, "4.7_rc3", "4.7"})

	result, err := applier.Apply(pkg, false)
	if err == nil || result == nil || result.Success {
		t.Fatalf("expected the apply to fail: err=%v result=%+v", err, result)
	}

	if fixer.called != 0 {
		t.Errorf("the fixer ran %d time(s); an environment failure must never reach it (R3.3)", fixer.called)
	}
	if result.Fixed {
		t.Error("result.Fixed = true, but nothing was fixed")
	}
	if result.FixSummary != "" {
		t.Errorf("FixSummary = %q, want empty: no fix was attempted", result.FixSummary)
	}
	// The stages the fix attempt emits are its footprint on the reporter. Their
	// absence says the attempt was not merely unsuccessful — it was never set up.
	for _, ev := range rec.snapshot() {
		if strings.Contains(ev, "llm-fix") || strings.Contains(ev, "re-check") {
			t.Errorf("reporter recorded %q; the fix attempt must not even be announced", ev)
		}
	}
}

// TestEnvironmentFailureReportsCauseAsEnvironment is R3.4. A refusal that says
// nothing is worse than the fixer running: the operator is left with a failed
// package, no diagnosis, and a correct ebuild that now looks suspect. So the
// report must name the package, say the cause was the environment and NOT the
// ebuild, and still carry what pkgdev printed.
//
// Note the boundary this deliberately does not assert: Apply re-wraps with
// "%w: %v" (ErrManifestFailed and then the text), so ErrManifestEnvironment is
// not reachable with errors.Is from Apply's error. The TEXT survives that
// boundary, which is what an operator reads, and that is what is pinned here.
func TestEnvironmentFailureReportsCauseAsEnvironment(t *testing.T) {
	stubExhaustedDistdirSpace(t)
	warns := captureWarnLogs(t)

	pkg := "dev-games/godot"
	applier, _, rec := newGateApplier(t,
		pkgdevFailsPrinting(productionManifestFailure),
		gatePkg{pkg, "4.7_rc3", "4.7"})

	_, err := applier.Apply(pkg, false)
	if err == nil {
		t.Fatal("expected the apply to fail")
	}

	// Every clause R3.4 asks for, checked on the error the apply fails with.
	for _, want := range []string{
		pkg + "-4.7",                    // which package
		"not on the ebuild",             // the cause, stated as a negative too
		"no usable free space",          // which observation reached that verdict
		"the LLM fixer was not invoked", // what was skipped, and that it was deliberate
		productionManifestFailure,       // the underlying error, verbatim
	} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("apply error is missing %q\n  got: %v", want, err)
		}
	}

	// The same statement has to reach the operator where a batch shows it: the
	// warn sink and the reporter, not only the returned error.
	if !anyContains(warns.all(), "not on the ebuild") {
		t.Errorf("nothing was logged about the cause; warnings = %v", warns.all())
	}
	if !anyContains(rec.snapshot(), "not on the ebuild") {
		t.Errorf("the reporter was told nothing about the cause; events = %v", rec.snapshot())
	}
}

// TestRepairableFailureStillInvokesTheFixer is the other half of the gate, and
// the one a wrong classification would quietly destroy: on a healthy machine
// nothing is observably wrong with the environment, so the verdict is
// "repairable" and today's path runs untouched — fixer invoked, and the
// AUTHORITATIVE second run of pkgdev, not the agent's self-report, deciding
// success. A gate that answered "environment" too eagerly would gate away a
// repair that exists, which R3.5 forbids.
func TestRepairableFailureStillInvokesTheFixer(t *testing.T) {
	// No space stub here, on purpose: the real query runs against a real, healthy
	// temporary distdir and finds nothing to complain about.
	pkgdevCalls := 0
	seam := func(ctx context.Context, name string, arg ...string) *exec.Cmd {
		if name != "pkgdev" {
			return exec.CommandContext(ctx, "true")
		}
		pkgdevCalls++
		if pkgdevCalls == 1 {
			return exec.CommandContext(ctx, "sh", "-c", `printf '%s\n' "$0"; exit 1`, "SRC_URI is unreachable: 404")
		}
		return exec.CommandContext(ctx, "true")
	}

	pkg := "dev-games/godot"
	applier, fixer, rec := newGateApplier(t, seam, gatePkg{pkg, "4.7_rc3", "4.7"})

	result, err := applier.Apply(pkg, false)
	if err != nil || result == nil || !result.Success {
		t.Fatalf("expected the repaired apply to succeed: err=%v result=%+v", err, result)
	}
	if fixer.called != 1 {
		t.Errorf("fixer called %d times, want exactly 1", fixer.called)
	}
	if !result.Fixed || result.FixSummary != fixer.summary {
		t.Errorf("Fixed=%v FixSummary=%q, want true / %q", result.Fixed, result.FixSummary, fixer.summary)
	}
	// Two pkgdev runs: the one that failed, and the one that decided success.
	if pkgdevCalls != 2 {
		t.Errorf("pkgdev ran %d time(s), want 2 — the authoritative re-run must still happen", pkgdevCalls)
	}
	assertOrder(t, rec.snapshot(),
		"TaskStage:"+pkg+":manifest",
		"TaskStage:"+pkg+":llm-fix",
		"TaskStage:"+pkg+":re-check",
	)
}

// TestEnvironmentFailureContinuesTheBatchAndExitsNonZero is R3.6, tested where
// each half is actually real.
//
// Continuation is the applier's: one package's environment failure must leave
// nothing latched behind it, so the next package applies normally. That is
// asserted here, with two packages through one applier.
//
// The exit code is NOT the applier's and is not asserted here, because asserting
// it here would assert nothing. A failed apply returns a non-nil error with
// Success == false — the exact shape applyAllPackages tallies
// (cmd/bentoo/overlay_autoupdate.go, the concurrent worker's `if err != nil`),
// and runApplyAll then calls osExit(1) whenever that tally is above zero. An
// environment failure is a failed apply like any other, so it already rides that
// path; TestApplyAllPackagesCountsFailures pins the tally and this test pins the
// shape it counts. No machinery was added for R3.6 — that is the finding.
func TestEnvironmentFailureContinuesTheBatchAndExitsNonZero(t *testing.T) {
	stubExhaustedDistdirSpace(t)

	// The first pkgdev call fails (the environment-failed package, applied
	// first); every later call succeeds, so the package after it is healthy.
	calls := 0
	seam := func(ctx context.Context, name string, arg ...string) *exec.Cmd {
		if name != "pkgdev" {
			return exec.CommandContext(ctx, "true")
		}
		calls++
		if calls == 1 {
			return exec.CommandContext(ctx, "sh", "-c", `printf '%s\n' "$0"; exit 1`, productionManifestFailure)
		}
		return exec.CommandContext(ctx, "true")
	}

	failed, healthy := "dev-games/godot", "dev-libs/foo"
	applier, fixer, _ := newGateApplier(t, seam,
		gatePkg{failed, "4.7_rc3", "4.7"},
		gatePkg{healthy, "1.0", "1.1"})

	badResult, badErr := applier.Apply(failed, false)
	if badErr == nil || badResult == nil || badResult.Success {
		t.Fatalf("the environment failure must fail the apply: err=%v result=%+v", badErr, badResult)
	}
	// The shape the batch counts, and the only thing the exit code is derived
	// from: a non-nil error and an unsuccessful result.
	if badResult.Error == nil {
		t.Error("result.Error is nil on a failed apply; the batch summary would show it as fine")
	}

	goodResult, goodErr := applier.Apply(healthy, false)
	if goodErr != nil || goodResult == nil || !goodResult.Success {
		t.Fatalf("the batch did not continue past the environment failure: err=%v result=%+v", goodErr, goodResult)
	}
	if fixer.called != 0 {
		t.Errorf("the fixer ran %d time(s) across the batch; the environment failure must not have reached it", fixer.called)
	}
}

// anyContains reports whether any of lines contains want.
func anyContains(lines []string, want string) bool {
	for _, line := range lines {
		if strings.Contains(line, want) {
			return true
		}
	}
	return false
}

// =============================================================================
// The pre-flight's verdict reaches the gate (S030 sub-task 7.1)
//
// `stories validate 030` found R3.1 half-implemented. The verification shipped —
// the probe fails loudly and names the path — but the failure it raises carries
// no *manifestRunError, and that type was the gate's only entry condition, so
// every pre-flight failure walked straight past it into a fixer invocation.
//
// These tests pin the half that was missing. The one that matters most is the
// negative at the end: over-gating would cost a repair that exists today, which
// R3.5 forbids, so "the fixer is skipped" is only correct next to a case that
// proves it still runs.
// =============================================================================

// withResolveDistdir swaps the package's distdir resolver for one test.
//
// The suite's init() already replaced the production resolver with a sandboxed
// one (see sweep_manifest_test.go); this restores THAT, not distfiles.Resolve,
// so a test cannot leave the package pointed at the host's DISTDIR.
func withResolveDistdir(t *testing.T, fn func(explicit, configured string) (distfiles.Dir, error)) {
	t.Helper()
	prev := resolveDistdir
	resolveDistdir = fn
	t.Cleanup(func() { resolveDistdir = prev })
}

// unwritableDistdirError is the error shape sweep.runManifest returns when the
// pre-flight refuses the resolved directory, built the way Probe builds it so
// the sentinel sits under the same number of wraps as in production.
func unwritableDistdirError(dir string) error {
	return fmt.Errorf("%w: distdir %s: %w", ErrManifestFailed,
		dir, fmt.Errorf("%w: %s: %w", distfiles.ErrDistdirNotWritable, dir, fs.ErrPermission))
}

// TestUnwritableDistdirPreflightSkipsTheManifestFixer is R3.1's second clause,
// and it is the defect validation found: an unwritable distdir is answered by
// Probe BEFORE pkgdev is spawned, so it is never a statement about the ebuild.
//
// The host this matters on is the one M2 measures: an invoking user outside
// group `portage` cannot write /var/cache/distfiles, and before this the whole
// batch went to the fixer at ten minutes and thirty turns each.
func TestUnwritableDistdirPreflightSkipsTheManifestFixer(t *testing.T) {
	dir := t.TempDir()
	withResolveDistdir(t, func(string, string) (distfiles.Dir, error) {
		return distfiles.Dir{Path: dir}, unwritableDistdirError(dir)
	})

	pkg := "dev-games/godot"
	applier, fixer, rec := newGateApplier(t,
		pkgdevFailsPrinting(productionManifestFailure),
		gatePkg{pkg, "4.7_rc3", "4.7"})

	result, err := applier.Apply(pkg, false)
	if err == nil || result == nil || result.Success {
		t.Fatalf("expected the apply to fail: err=%v result=%+v", err, result)
	}

	if fixer.called != 0 {
		t.Errorf("the fixer ran %d time(s); a distdir that failed the pre-flight must never reach it (R3.1, R3.3)", fixer.called)
	}
	if result.Fixed {
		t.Error("result.Fixed = true, but nothing was fixed")
	}
	// The announcement is the fixer's footprint even when the call itself is
	// cheap: an operator reading "invoking LLM fixer" for an unwritable
	// directory learns the wrong thing about their machine.
	for _, ev := range rec.snapshot() {
		if strings.Contains(ev, "llm-fix") || strings.Contains(ev, "re-check") {
			t.Errorf("reporter recorded %q; the fix attempt must not even be announced", ev)
		}
	}
}

// TestUncreatableDistdirSkipsTheManifestFixer drives the REAL distfiles.Resolve,
// not a stub, so it proves the whole chain: MkdirAll fails, Resolve wraps the
// sentinel around it, sweep.runManifest wraps that, and the gate still finds it.
//
// It is the rung R1.4 names alongside writability ("does not exist, cannot be
// created, or is not writable") and the one a sentinel-only reading would have
// missed, because expandAndCreate's error carried no sentinel at all.
//
// ENOTDIR is the failure used because it needs no privileges and behaves the
// same as root — os.Chmod(dir, 0500) does not, which is why probe_test.go has to
// skip those subtests for euid 0.
func TestUncreatableDistdirSkipsTheManifestFixer(t *testing.T) {
	blocker := filepath.Join(t.TempDir(), "not-a-directory")
	if err := os.WriteFile(blocker, []byte("regular file"), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	// A path UNDER a regular file: every component after it is uncreatable.
	target := filepath.Join(blocker, "distfiles")
	withResolveDistdir(t, func(string, string) (distfiles.Dir, error) {
		return distfiles.Resolve(target, "")
	})

	pkg := "dev-games/godot"
	applier, fixer, _ := newGateApplier(t,
		pkgdevFailsPrinting(productionManifestFailure),
		gatePkg{pkg, "4.7_rc3", "4.7"})

	_, err := applier.Apply(pkg, false)
	if err == nil {
		t.Fatal("expected the apply to fail")
	}
	if fixer.called != 0 {
		t.Errorf("the fixer ran %d time(s); a distdir that could not be created is the machine's fault, not the ebuild's (R1.4, R3.3)", fixer.called)
	}
	// R1.4's other half: the operator is told WHICH directory.
	if !strings.Contains(err.Error(), target) {
		t.Errorf("the error does not name the directory it could not prepare\n  want substring: %s\n  got: %v", target, err)
	}
}

// TestLockedDistfileSkipsTheManifestFixer covers the second sentinel. Its own
// documentation (internal/common/distfiles/lock.go) says it exists so "D5's
// classifier must be able to tell them apart without reading either message",
// and nothing consulted it before 7.1.
//
// This drives refuseFixOnEnvironmentFailure directly rather than a whole Apply,
// and the reason is mechanical: producing a real ErrDistfileLocked means holding
// the lock until a second writer gives up, and that bound (lockWait) is two
// minutes and unexported, so this package cannot shorten it. What IS end-to-end
// is the error's shape — it is built exactly as sweep.runManifest builds it, so
// the wrap depth the gate has to see through is the production one.
//
// The consequence of that choice, so nobody reads more into a green than is
// there: this is a SHAPE test. If sweep.go:547 ever changed its inner `%w` to
// `%v`, production would stop carrying the sentinel and this test would stay
// green. `%w` is what it uses today; a change there needs its own assertion.
func TestLockedDistfileSkipsTheManifestFixer(t *testing.T) {
	applier, _, rec := newGateApplier(t,
		pkgdevFailsPrinting(productionManifestFailure),
		gatePkg{"dev-games/godot", "4.7_rc3", "4.7"})

	distdir := t.TempDir()
	held := fmt.Errorf("%w: claiming the distfiles for dev-games/godot in %s: %w",
		ErrManifestFailed, distdir,
		fmt.Errorf("%w: godot-4.7.tar.xz in %s is held by pid 4242", distfiles.ErrDistfileLocked, distdir))

	envErr := applier.refuseFixOnEnvironmentFailure("dev-games/godot", "4.7", held)
	if envErr == nil {
		t.Fatal("refuseFixOnEnvironmentFailure returned nil; a distfile another writer holds is not something an LLM can repair (R3.3)")
	}
	if !errors.Is(envErr, ErrManifestEnvironment) {
		t.Errorf("the refusal does not carry ErrManifestEnvironment: %v", envErr)
	}
	// The wrapped chain has to survive, or the operator loses the only two facts
	// worth acting on: which distfile, and who holds it.
	if !errors.Is(envErr, distfiles.ErrDistfileLocked) {
		t.Errorf("the refusal lost distfiles.ErrDistfileLocked: %v", envErr)
	}
	if !strings.Contains(envErr.Error(), "pid 4242") {
		t.Errorf("the refusal dropped the holder from the message: %v", envErr)
	}
	if !anyContains(rec.snapshot(), "not on the ebuild") {
		t.Errorf("the reporter was told nothing about the cause; events = %v", rec.snapshot())
	}
}

// TestPreflightRefusalStatesTheCauseWasTheEnvironment is R3.4 on the pre-flight
// path. The post-pkgdev path already had this
// (TestEnvironmentFailureReportsCauseAsEnvironment); a refusal that says nothing
// leaves the operator with a failed package, no diagnosis, and a correct ebuild
// that now looks suspect.
func TestPreflightRefusalStatesTheCauseWasTheEnvironment(t *testing.T) {
	warns := captureWarnLogs(t)
	dir := t.TempDir()
	withResolveDistdir(t, func(string, string) (distfiles.Dir, error) {
		return distfiles.Dir{Path: dir}, unwritableDistdirError(dir)
	})

	pkg := "dev-games/godot"
	applier, _, rec := newGateApplier(t,
		pkgdevFailsPrinting(productionManifestFailure),
		gatePkg{pkg, "4.7_rc3", "4.7"})

	_, err := applier.Apply(pkg, false)
	if err == nil {
		t.Fatal("expected the apply to fail")
	}

	for _, want := range []string{
		pkg + "-4.7",                    // which package
		"not on the ebuild",             // the cause, stated as a negative too
		"distdir is not writable",       // which observation reached that verdict
		dir,                             // and which directory
		"the LLM fixer was not invoked", // what was skipped, and that it was deliberate
	} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("apply error is missing %q\n  got: %v", want, err)
		}
	}

	if !anyContains(warns.all(), "not on the ebuild") {
		t.Errorf("nothing was logged about the cause; warnings = %v", warns.all())
	}
	if !anyContains(rec.snapshot(), "not on the ebuild") {
		t.Errorf("the reporter was told nothing about the cause; events = %v", rec.snapshot())
	}
}

// TestNonEnvironmentPreflightFailureStillReachesTheFixer is the negative that
// makes the four above meaningful, and it is R3.5 stated as a test: a failure
// before pkgdev that names no machine condition is still UNANSWERED, and an
// unanswered check must mean "repairable".
//
// Without this, the cheapest way to pass every test in this file would be to
// refuse the fixer whenever pkgdev did not run — which would silently delete a
// repair path that works today.
func TestNonEnvironmentPreflightFailureStillReachesTheFixer(t *testing.T) {
	withResolveDistdir(t, func(string, string) (distfiles.Dir, error) {
		// No sentinel: something went wrong that says nothing about the machine.
		return distfiles.Dir{}, fmt.Errorf("%w: something unclassifiable", ErrManifestFailed)
	})

	pkg := "dev-games/godot"
	applier, fixer, _ := newGateApplier(t,
		pkgdevFailsPrinting(productionManifestFailure),
		gatePkg{pkg, "4.7_rc3", "4.7"})

	if _, err := applier.Apply(pkg, false); err == nil {
		t.Fatal("expected the apply to fail: the re-run fails too")
	}
	if fixer.called != 1 {
		t.Errorf("the fixer ran %d time(s), want 1; an unclassifiable failure must keep today's repair path (R3.5)", fixer.called)
	}
}

// =============================================================================
// The fixer's sandbox is not made in RAM (S030 sub-task 7.4)
// =============================================================================

// TestManifestFixSandboxIsMadeUnderTheHostTempRoot is S030-R1.1 applied to the
// second distdir on this path.
//
// R1.1 says the autoupdate manifest step must not default its distdir to
// os.TempDir(). Tasks 1-6 removed that default from sweep.runManifest and left it
// standing here, where the LLM fixer is handed a private directory to verify its
// own repair in. The measured host's os.TempDir() is a 31 GB tmpfs, so the
// agent's verification downloads went into RAM.
//
// The privacy is deliberate and unchanged — the agent must not be turned loose on
// the system DISTDIR, holding Bash(wget *) and none of the lock or quarantine
// machinery sweep.runManifest wraps around a shared directory. Only the ROOT
// moves.
func TestManifestFixSandboxIsMadeUnderTheHostTempRoot(t *testing.T) {
	root := t.TempDir()
	prev := fixSandboxRoot
	fixSandboxRoot = func() string { return root }
	t.Cleanup(func() { fixSandboxRoot = prev })

	pkg := "dev-games/godot"
	applier, fixer, _ := newGateApplier(t,
		pkgdevFailsPrinting("some failure a fixer could plausibly repair"),
		gatePkg{pkg, "4.7_rc3", "4.7"})

	if _, err := applier.Apply(pkg, false); err == nil {
		t.Fatal("expected the apply to fail: the re-run fails too")
	}
	if fixer.called != 1 {
		t.Fatalf("the fixer ran %d time(s), want 1; this test is about the distdir it was GIVEN", fixer.called)
	}

	got := fixer.lastReq.DistDir
	if got == "" {
		t.Fatal("the fixer was given no distdir at all")
	}
	if filepath.Dir(got) != root {
		t.Errorf("the fixer's distdir was made under %q, want %q\n"+
			"an empty root means os.TempDir(), which is the tmpfs S030-R1.1 exists to stop using",
			filepath.Dir(got), root)
	}

	// And it is still private and still cleaned up: the root must hold nothing
	// once the apply returns.
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	if len(entries) != 0 {
		t.Errorf("the sandbox survived the apply: %v; it is created per-fix and removed on return", entries)
	}
}

// TestManifestFixSandboxFallsBackWhenTheHostCannotAnswer is the Unchanged
// Behavior half. A machine with no portageq — a CI runner, a non-Gentoo
// developer box — gets "" from the root query, and "" is precisely what
// os.MkdirTemp already means by "use the default". Nothing about today's
// behaviour may depend on the query succeeding.
func TestManifestFixSandboxFallsBackWhenTheHostCannotAnswer(t *testing.T) {
	prev := fixSandboxRoot
	fixSandboxRoot = func() string { return "" }
	t.Cleanup(func() { fixSandboxRoot = prev })

	pkg := "dev-games/godot"
	applier, fixer, _ := newGateApplier(t,
		pkgdevFailsPrinting("some failure a fixer could plausibly repair"),
		gatePkg{pkg, "4.7_rc3", "4.7"})

	if _, err := applier.Apply(pkg, false); err == nil {
		t.Fatal("expected the apply to fail")
	}
	if fixer.called != 1 {
		t.Fatalf("the fixer ran %d time(s), want 1", fixer.called)
	}
	if got := fixer.lastReq.DistDir; filepath.Dir(got) != os.TempDir() {
		t.Errorf("with an unanswerable root the sandbox went to %q, want it under os.TempDir() (%q): "+
			"an unanswered query must cost nothing, not fail the apply", filepath.Dir(got), os.TempDir())
	}
}
