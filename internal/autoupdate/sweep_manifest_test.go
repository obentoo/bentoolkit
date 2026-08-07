package autoupdate

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/obentoo/bentoolkit/internal/common/distfiles"
)

// =============================================================================
// The manifest step's distdir (S030 sub-task 5.1)
//
// Every test here drives sweeper.runManifest, which now resolves a real
// directory instead of making one in /tmp. That directory is, in production,
// the DISTDIR the host's own package manager downloads into — and this step
// moves files aside in it, takes locks in it and removes files from it.
//
// So the first thing in this file is the guard that keeps the suite out of it.
// =============================================================================

// init redirects the whole package's distdir resolution away from the host.
//
// It is an init and not a per-test helper because the fall-through has two
// entry points and only one of them is reachable from a test: a sweeper built
// with withSweeperDistdir names its own directory, but Applier.sweeper() passes
// no distdir option at all, so every applier test that reaches the manifest step
// would resolve /var/cache/distfiles — probing it, and (for a package whose
// Manifest lists distfiles) locking and quarantining inside it.
//
// With this in place the property is structural rather than a habit each test
// has to keep: there is exactly one resolver in the package, it cannot answer
// the host's DISTDIR, and a test that asks for it fails instead of touching it.
func init() {
	resolveDistdir = sandboxedResolveDistdir
}

// sandboxedResolveDistdir is the test build's distdir resolver. It defers to the
// real distfiles.Resolve — the tests below are about ITS behaviour, so stubbing
// it out would prove nothing — and only intervenes at the two ends that reach
// outside the test's own temporary tree:
//
//   - nothing named: the host would be asked next (`portageq distdir`), so a
//     directory belonging to this test process is substituted instead;
//   - the host's DISTDIR named outright: refused, loudly, as an error the
//     calling test fails on.
func sandboxedResolveDistdir(explicit, configured string) (distfiles.Dir, error) {
	if explicit == distfiles.DefaultCache || configured == distfiles.DefaultCache {
		return distfiles.Dir{}, fmt.Errorf("refused: a test asked for the host's DISTDIR (%s)", distfiles.DefaultCache)
	}
	if explicit == "" && configured == "" {
		configured = testDistdirSandbox()
	}
	return distfiles.Resolve(explicit, configured)
}

// testDistdirSandbox is where a sweeper that named no distdir lands.
//
// One fixed path, created once and reused by every run of this suite, for two
// reasons. It must PRE-EXIST when Resolve sees it, so Resolve reports
// Created = false and Cleanup never removes it — otherwise one worker of a
// concurrent sweep would delete the directory its siblings are still using. And
// a fixed name cannot accumulate: repeated `go test` runs share the one empty
// directory instead of leaving a new one behind each time.
var testDistdirSandbox = sync.OnceValue(func() string {
	dir := filepath.Join(os.TempDir(), "bentoo-autoupdate-test-distdir")
	// A failure here needs no handling: Resolve then fails to prepare the path
	// and the calling test reports it, naming the directory.
	_ = os.MkdirAll(dir, 0o700)
	return dir
})

// TestRunManifestNeverResolvesTheHostDistdir pins the guard above, because a
// guard nothing tests is a guard that quietly stops working — and the failure
// mode it prevents is damage to the machine running the suite, not a red test.
func TestRunManifestNeverResolvesTheHostDistdir(t *testing.T) {
	dir, err := resolveDistdir("", "")
	if err != nil {
		t.Fatalf("resolving with nothing named: %v", err)
	}
	if dir.Path == distfiles.DefaultCache {
		t.Fatalf("the fall-through resolved the host's DISTDIR (%s)", dir.Path)
	}
	if !strings.HasPrefix(dir.Path, os.TempDir()+string(filepath.Separator)) {
		t.Fatalf("the fall-through resolved %s, which is outside %s", dir.Path, os.TempDir())
	}
	if dir.Created {
		t.Errorf("the sandbox reported Created = true; Cleanup would remove a directory other workers share")
	}

	if _, err := resolveDistdir(distfiles.DefaultCache, ""); err == nil {
		t.Error("naming the host's DISTDIR explicitly was accepted; it must be refused")
	}
	if _, err := resolveDistdir("", distfiles.DefaultCache); err == nil {
		t.Error("naming the host's DISTDIR through the config rung was accepted; it must be refused")
	}
}

// -----------------------------------------------------------------------------
// Harness
// -----------------------------------------------------------------------------

// fakePkgdev stands in for `pkgdev manifest` and, more importantly, gives a test
// a foothold INSIDE the manifest step: before runs at invocation time, which is
// after the lock has been taken, after the quarantine and after the fetch scope
// was recorded, but before the command itself. That is the only moment at which
// "the quarantine happened BEFORE pkgdev" is an observable fact rather than an
// inference from the end state.
type fakePkgdev struct {
	mu      sync.Mutex
	invoked int
	distdir string
	args    []string

	// before observes the state pkgdev is handed. Assertions inside it run on
	// the goroutine driving the step, so t.Error is safe.
	before func(distdir string)
	// script is the shell run in pkgdev's place, built from the distdir it was
	// given. Empty means a plain success.
	script func(distdir string) string
}

func (f *fakePkgdev) seam() func(ctx context.Context, name string, arg ...string) *exec.Cmd {
	return func(ctx context.Context, name string, arg ...string) *exec.Cmd {
		if name != "pkgdev" {
			return exec.CommandContext(ctx, "true")
		}
		distdir, _ := flagValue(arg, "--distdir")
		f.mu.Lock()
		f.invoked++
		f.distdir = distdir
		f.args = append([]string(nil), arg...)
		f.mu.Unlock()

		if f.before != nil {
			f.before(distdir)
		}
		script := "true"
		if f.script != nil {
			script = f.script(distdir)
		}
		return exec.CommandContext(ctx, "sh", "-c", script)
	}
}

// manifestSweeper builds a sweeper for the manifest step and REFUSES to build
// one that has not named its own distdir.
//
// That refusal is the second half of the guard at the top of this file: the
// sandbox stops a fall-through from reaching the host, and this stops a test in
// this file from relying on the sandbox by accident — every test here works on a
// directory it created and can therefore assert about.
func manifestSweeper(t *testing.T, overlayPath, explicit, configured string, opts ...sweeperOption) *sweeper {
	t.Helper()
	named := false
	for _, p := range []string{explicit, configured} {
		if p == "" {
			continue
		}
		named = true
		if p == distfiles.DefaultCache || !insideThisTestsTempTree(t, p) {
			t.Fatalf("test distdir %q is outside this test's own temporary tree: a test must never act on a directory it does not own", p)
		}
	}
	if !named {
		t.Fatal("this test named no distdir; every test here must pass one so it acts on a directory it owns")
	}
	all := append([]sweeperOption{withSweeperDistdir(explicit, configured)}, opts...)
	return newSweeper(overlayPath, all...)
}

// insideThisTestsTempTree reports whether p lives in the temporary tree this
// test owns.
//
// The root is derived rather than assumed: t.TempDir() hands out a fresh
// numbered subdirectory per call, all of them under one per-test root, so the
// parent of any of them is that root. Comparing against os.TempDir() instead
// would be wrong here — the go command runs the test binary with its own TMPDIR
// (GOTMPDIR), so t.TempDir() and os.TempDir() are different trees, and the
// mismatch would reject every legitimate directory while proving nothing.
func insideThisTestsTempTree(t *testing.T, p string) bool {
	t.Helper()
	root := filepath.Dir(t.TempDir())
	if root == "" || root == string(filepath.Separator) {
		return false
	}
	return strings.HasPrefix(p, root+string(filepath.Separator))
}

// seedManifest writes a package Manifest with one DIST line per name.
func seedManifest(t *testing.T, pkgDir string, names ...string) {
	t.Helper()
	var b strings.Builder
	for _, n := range names {
		fmt.Fprintf(&b, "DIST %s 10 BLAKE2B deadbeef SHA512 deadbeef\n", n)
	}
	if err := os.WriteFile(filepath.Join(pkgDir, "Manifest"), []byte(b.String()), 0o600); err != nil {
		t.Fatalf("seeding Manifest: %v", err)
	}
}

// seedDistfile writes one file into a distdir and returns its path.
func seedDistfile(t *testing.T, distdir, name, content string) string {
	t.Helper()
	path := filepath.Join(distdir, name)
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("seeding distfile %s: %v", name, err)
	}
	return path
}

// entries lists a directory's names, sorted, for a readable failure message.
func entries(t *testing.T, dir string) []string {
	t.Helper()
	found, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("reading %s: %v", dir, err)
	}
	var out []string
	for _, e := range found {
		out = append(out, e.Name())
	}
	return out
}

// tempDistdirsNow lists the temporary distdirs the replaced code used to create.
// A test brackets the run with two of these: the point is not that the count is
// stable but that this step no longer makes one at all (S030-R1.1).
func tempDistdirsNow(t *testing.T) []string {
	t.Helper()
	matches, err := filepath.Glob(filepath.Join(os.TempDir(), "bentoo-distfiles-*"))
	if err != nil {
		t.Fatalf("globbing %s: %v", os.TempDir(), err)
	}
	return matches
}

// -----------------------------------------------------------------------------
// The four the sub-task names
// -----------------------------------------------------------------------------

// TestRunManifestUsesResolvedDistdir: pkgdev is given the directory the
// precedence resolved, and no temporary directory is created anywhere
// (S030-R1.1, S030-R1.2).
func TestRunManifestUsesResolvedDistdir(t *testing.T) {
	overlayDir := filepath.Join(t.TempDir(), "overlay")
	pkg := "test-cat/test-pkg"
	createTestEbuildFile(t, overlayDir, pkg, "2.0.0")
	distdir := t.TempDir()

	before := tempDistdirsNow(t)
	fake := &fakePkgdev{}
	s := manifestSweeper(t, overlayDir, distdir, "", withSweeperExec(fake.seam()))
	if err := s.runManifest(pkg, "2.0.0"); err != nil {
		t.Fatalf("runManifest: %v", err)
	}

	if fake.invoked != 1 {
		t.Fatalf("pkgdev invocations = %d, want 1", fake.invoked)
	}
	if fake.distdir != distdir {
		t.Errorf("pkgdev --distdir = %q, want the resolved %q", fake.distdir, distdir)
	}
	wantArgs := []string{"manifest", "--distdir", distdir}
	if strings.Join(fake.args, " ") != strings.Join(wantArgs, " ") {
		t.Errorf("pkgdev args = %v, want %v", fake.args, wantArgs)
	}
	if _, err := os.Stat(distdir); err != nil {
		t.Errorf("the resolved distdir was removed: %v", err)
	}
	if after := tempDistdirsNow(t); len(after) != len(before) {
		t.Errorf("a temporary distdir was created: before=%v after=%v", before, after)
	}
}

// TestRunManifestDoesNotRemoveAConfiguredDistdir: a directory this run did not
// create survives it, contents and all (S030-R1.5). This is the case that
// matters in production — the resolved distdir is normally the host's own
// DISTDIR, holding thousands of files that belong to the package manager.
func TestRunManifestDoesNotRemoveAConfiguredDistdir(t *testing.T) {
	overlayDir := filepath.Join(t.TempDir(), "overlay")
	pkg := "test-cat/test-pkg"
	createTestEbuildFile(t, overlayDir, pkg, "2.0.0")

	// The CONFIGURED rung, not the flag: autoupdate.distdir is what 5.2 fills,
	// and it must be as safe from removal as an explicit --distdir.
	distdir := t.TempDir()
	sentinel := seedDistfile(t, distdir, "someone-elses-1.0.tar.gz", "not ours")

	fake := &fakePkgdev{}
	s := manifestSweeper(t, overlayDir, "", distdir, withSweeperExec(fake.seam()))
	if err := s.runManifest(pkg, "2.0.0"); err != nil {
		t.Fatalf("runManifest: %v", err)
	}

	if fake.distdir != distdir {
		t.Errorf("pkgdev --distdir = %q, want the configured %q", fake.distdir, distdir)
	}
	if _, err := os.Stat(distdir); err != nil {
		t.Fatalf("the configured distdir was removed: %v", err)
	}
	got, err := os.ReadFile(sentinel) // #nosec G304 -- path built by the test
	if err != nil {
		t.Fatalf("a file in the configured distdir was removed: %v", err)
	}
	if string(got) != "not ours" {
		t.Errorf("a file in the configured distdir was rewritten: %q", got)
	}
}

// TestRunManifestRemovesADistdirItCreated: the other half of R1.5. A path that
// did not exist is created by the resolution, used, and taken away again — so
// "never delete what you did not make" does not become "never clean up".
func TestRunManifestRemovesADistdirItCreated(t *testing.T) {
	overlayDir := filepath.Join(t.TempDir(), "overlay")
	pkg := "test-cat/test-pkg"
	createTestEbuildFile(t, overlayDir, pkg, "2.0.0")

	distdir := filepath.Join(t.TempDir(), "made-by-this-run")
	if _, err := os.Stat(distdir); !os.IsNotExist(err) {
		t.Fatalf("precondition: %s must not exist yet (%v)", distdir, err)
	}

	existedDuringTheRun := false
	fake := &fakePkgdev{before: func(dir string) {
		if _, err := os.Stat(dir); err == nil {
			existedDuringTheRun = true
		}
	}}
	s := manifestSweeper(t, overlayDir, distdir, "", withSweeperExec(fake.seam()))
	if err := s.runManifest(pkg, "2.0.0"); err != nil {
		t.Fatalf("runManifest: %v", err)
	}

	if fake.distdir != distdir {
		t.Errorf("pkgdev --distdir = %q, want %q", fake.distdir, distdir)
	}
	if !existedDuringTheRun {
		t.Error("the distdir did not exist while pkgdev ran; it must be created before the step, not after it")
	}
	if _, err := os.Stat(distdir); !os.IsNotExist(err) {
		t.Errorf("a distdir this run created was left behind: %v", err)
	}
}

// TestRunManifestQuarantinesBeforeInvokingPkgdev is the R2.2 test, and the
// "before" is the whole assertion: a truncated file moved aside AFTER pkgdev has
// been handed the directory protects nothing, because pkgdev has already
// digested it and this overlay publishes what it digests.
//
// The fixture is the shape of the defect: a bump from 1.0.0 to 2.0.0 whose
// current Manifest lists only the 1.0.0 distfile, with a half-written 2.0.0
// distfile already sitting in the shared directory from a fetch that died.
func TestRunManifestQuarantinesBeforeInvokingPkgdev(t *testing.T) {
	overlayDir := filepath.Join(t.TempDir(), "overlay")
	pkg := "test-cat/test-pkg"
	createTestEbuildFile(t, overlayDir, pkg, "1.0.0")
	createTestEbuildFile(t, overlayDir, pkg, "2.0.0")
	pkgDir := filepath.Join(overlayDir, "test-cat", "test-pkg")
	seedManifest(t, pkgDir, "test-pkg-1.0.0.tar.gz")

	distdir := t.TempDir()
	verified := seedDistfile(t, distdir, "test-pkg-1.0.0.tar.gz", "a distfile the Manifest vouches for")
	truncated := seedDistfile(t, distdir, "test-pkg-2.0.0.tar.gz", "half a dow")

	rep := &recordingReporter{}
	fake := &fakePkgdev{before: func(dir string) {
		if _, err := os.Stat(truncated); !os.IsNotExist(err) {
			t.Errorf("the unverifiable distfile was still under its own name when pkgdev started (%v); it would have been digested", err)
		}
		quarantined := 0
		for _, name := range entries(t, dir) {
			if strings.Contains(name, "._bentoo_quarantine_.") {
				quarantined++
				if !strings.HasPrefix(name, "test-pkg-2.0.0.tar.gz") {
					t.Errorf("quarantined name %q does not carry the original filename as its prefix", name)
				}
				body, err := os.ReadFile(filepath.Join(dir, name)) // #nosec G304 -- inside the test's own distdir
				if err != nil || string(body) != "half a dow" {
					t.Errorf("the quarantined file lost its content: %q (%v)", body, err)
				}
			}
		}
		if quarantined != 1 {
			t.Errorf("quarantined files = %d, want exactly 1 (%v)", quarantined, entries(t, dir))
		}
		// R2.1: a file the current Manifest DOES list is verified, so it is
		// reused rather than moved. Moving it would force a re-download of a
		// file that is already known-good.
		body, err := os.ReadFile(verified) // #nosec G304 -- inside the test's own distdir
		if err != nil || string(body) != "a distfile the Manifest vouches for" {
			t.Errorf("the Manifest-listed distfile was disturbed: %q (%v)", body, err)
		}
	}}

	s := manifestSweeper(t, overlayDir, distdir, "",
		withSweeperExec(fake.seam()), withSweeperReporter(rep))
	if err := s.runManifest(pkg, "2.0.0"); err != nil {
		t.Fatalf("runManifest: %v", err)
	}
	if fake.invoked != 1 {
		t.Fatalf("pkgdev invocations = %d, want 1", fake.invoked)
	}

	// The rename is only auditable if it is reported: the file is still on disk
	// under a name nobody would look for otherwise.
	reported := false
	for _, e := range rep.snapshot() {
		if strings.HasPrefix(e, "Log:") && strings.Contains(e, "quarantined") && strings.Contains(e, "test-pkg-2.0.0.tar.gz") {
			reported = true
		}
	}
	if !reported {
		t.Errorf("no quarantine was reported; events = %v", rep.snapshot())
	}
}

// -----------------------------------------------------------------------------
// The rest of the wiring: the failure branch, the lock, and the derived names
// -----------------------------------------------------------------------------

// TestRunManifestCleansUpOnlyWhatItCreatedWhenPkgdevFails is R2.3 in both
// directions at once, which is the only way it means anything: the artefact this
// run's fetch created goes away, and the file that was already there does not.
//
// The pre-existing file is one the current Manifest lists, because that is the
// case R2.1 protects — it survives the quarantine, so it is still sitting there
// when the failure branch runs, which is exactly where a cleanup that deleted by
// name rather than by provenance would take it.
func TestRunManifestCleansUpOnlyWhatItCreatedWhenPkgdevFails(t *testing.T) {
	overlayDir := filepath.Join(t.TempDir(), "overlay")
	pkg := "test-cat/test-pkg"
	createTestEbuildFile(t, overlayDir, pkg, "1.0.0")
	createTestEbuildFile(t, overlayDir, pkg, "2.0.0")
	pkgDir := filepath.Join(overlayDir, "test-cat", "test-pkg")
	seedManifest(t, pkgDir, "test-pkg-1.0.0.tar.gz")

	distdir := t.TempDir()
	pre := seedDistfile(t, distdir, "test-pkg-1.0.0.tar.gz", "already here, verified")

	rep := &recordingReporter{}
	// pkgdev writes half of the new distfile and dies, which is what an
	// interrupted fetch leaves behind.
	fake := &fakePkgdev{script: func(dir string) string {
		return "printf 'half a download' > '" + filepath.Join(dir, "test-pkg-2.0.0.tar.gz") + "'; exit 1"
	}}
	s := manifestSweeper(t, overlayDir, distdir, "",
		withSweeperExec(fake.seam()), withSweeperReporter(rep))

	err := s.runManifest(pkg, "2.0.0")
	if err == nil {
		t.Fatal("runManifest: expected the failure the fake pkgdev produced")
	}

	if _, statErr := os.Stat(filepath.Join(distdir, "test-pkg-2.0.0.tar.gz")); !os.IsNotExist(statErr) {
		t.Errorf("the incomplete distfile this run created was left behind: %v", statErr)
	}
	body, readErr := os.ReadFile(pre) // #nosec G304 -- inside the test's own distdir
	if readErr != nil || string(body) != "already here, verified" {
		t.Errorf("a distfile this run did not create was removed or rewritten: %q (%v)", body, readErr)
	}
	reported := false
	for _, e := range rep.snapshot() {
		if strings.HasPrefix(e, "Log:") && strings.Contains(e, "test-pkg-2.0.0.tar.gz") && strings.Contains(e, "removed") {
			reported = true
		}
	}
	if !reported {
		t.Errorf("the cleanup was not reported; events = %v", rep.snapshot())
	}
}

// TestRunManifestFailureCarriesTheDistdirAndExpectedNames: the failure has to
// carry the state it happened in, because the caller that classifies it cannot
// derive that state again — the distdir comes from a precedence that asks the
// host, and the names from a directory the run has since changed.
//
// The message must stay byte-identical while it happens: it is what an operator
// reads, and existing tests pin it.
func TestRunManifestFailureCarriesTheDistdirAndExpectedNames(t *testing.T) {
	overlayDir := filepath.Join(t.TempDir(), "overlay")
	pkg := "test-cat/test-pkg"
	createTestEbuildFile(t, overlayDir, pkg, "1.0.0")
	createTestEbuildFile(t, overlayDir, pkg, "2.0.0")
	pkgDir := filepath.Join(overlayDir, "test-cat", "test-pkg")
	seedManifest(t, pkgDir, "test-pkg-1.0.0.tar.gz")

	distdir := t.TempDir()
	fake := &fakePkgdev{script: func(string) string { return "echo 'boom' >&2; exit 3" }}
	s := manifestSweeper(t, overlayDir, distdir, "", withSweeperExec(fake.seam()))

	err := s.runManifest(pkg, "2.0.0")
	if err == nil {
		t.Fatal("runManifest: expected a failure")
	}

	var mre *manifestRunError
	if !errors.As(err, &mre) {
		t.Fatalf("the failure does not carry the manifest run state: %T", err)
	}
	if mre.Distdir != distdir {
		t.Errorf("Distdir = %q, want %q", mre.Distdir, distdir)
	}
	if len(mre.Expected) != 1 || mre.Expected[0] != "test-pkg-2.0.0.tar.gz" {
		t.Errorf("Expected = %v, want [test-pkg-2.0.0.tar.gz]", mre.Expected)
	}
	if !strings.HasPrefix(err.Error(), "command failed: ") || !strings.Contains(err.Error(), "\nOutput: ") {
		t.Errorf("the failure message changed: %q", err.Error())
	}
	if !strings.Contains(err.Error(), "boom") {
		t.Errorf("the captured output is missing from the message: %q", err.Error())
	}
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) {
		t.Error("errors.As no longer reaches the exec failure underneath")
	}
}

// TestRunManifestHoldsTheDistfileLockAcrossPkgdev: the claim is what makes "this
// name was absent when we started, so what is there now is ours" true. Held only
// up to the invocation, it would leave the whole download — the long part —
// unprotected, so the observation that matters is taken while pkgdev runs.
func TestRunManifestHoldsTheDistfileLockAcrossPkgdev(t *testing.T) {
	overlayDir := filepath.Join(t.TempDir(), "overlay")
	pkg := "test-cat/test-pkg"
	createTestEbuildFile(t, overlayDir, pkg, "1.0.0")
	createTestEbuildFile(t, overlayDir, pkg, "2.0.0")
	pkgDir := filepath.Join(overlayDir, "test-cat", "test-pkg")
	seedManifest(t, pkgDir, "test-pkg-1.0.0.tar.gz")

	distdir := t.TempDir()
	lockPath := filepath.Join(distdir, ".test-pkg-2.0.0.tar.gz.bentoo_lockfile")
	fake := &fakePkgdev{before: func(string) {
		if _, err := os.Stat(lockPath); err != nil {
			t.Errorf("no lock was held over the distfile while pkgdev ran: %v", err)
		}
	}}
	s := manifestSweeper(t, overlayDir, distdir, "", withSweeperExec(fake.seam()))
	if err := s.runManifest(pkg, "2.0.0"); err != nil {
		t.Fatalf("runManifest: %v", err)
	}
	if _, err := os.Stat(lockPath); !os.IsNotExist(err) {
		t.Errorf("the lock outlived the step: %v", err)
	}
}

// TestRunManifestExpectedNamesAreDerivedConservatively pins what the derivation
// claims and, just as deliberately, what it does not.
//
// The names it produces authorise a rename and (through the fetch scope) a
// removal, so the test that matters most is the LAST one: an upstream that
// renames its archive yields no name at all, rather than a plausible-looking one
// that would move a stranger's file aside.
func TestRunManifestExpectedNamesAreDerivedConservatively(t *testing.T) {
	overlayDir := filepath.Join(t.TempDir(), "overlay")
	pkg := "test-cat/test-pkg"
	createTestEbuildFile(t, overlayDir, pkg, "0.209.4")
	createTestEbuildFile(t, overlayDir, pkg, "0.212.0")
	pkgDir := filepath.Join(overlayDir, "test-cat", "test-pkg")
	s := newSweeper(overlayDir)

	got := s.expectedDistfiles(pkg, pkgDir, "test-pkg",
		[]string{"test-pkg-0.209.4.tar.gz"}, []string{"0.212.0"})
	if len(got) != 1 || got[0] != "test-pkg-0.212.0.tar.gz" {
		t.Errorf("expected names = %v, want [test-pkg-0.212.0.tar.gz]", got)
	}

	// A revision bump renames no archive.
	got = s.expectedDistfiles(pkg, pkgDir, "test-pkg",
		[]string{"test-pkg-0.209.4.tar.gz"}, []string{"0.212.0-r1"})
	if len(got) != 1 || got[0] != "test-pkg-0.212.0.tar.gz" {
		t.Errorf("expected names for a revision = %v, want [test-pkg-0.212.0.tar.gz]", got)
	}

	// A name that carries the version in some other shape is still derived —
	// the substitution is about the version, not about a fixed ${P} template.
	got = s.expectedDistfiles(pkg, pkgDir, "test-pkg",
		[]string{"v0.209.4-linux-x86_64.tar.gz"}, []string{"0.212.0"})
	if len(got) != 1 || got[0] != "v0.212.0-linux-x86_64.tar.gz" {
		t.Errorf("expected names = %v, want [v0.212.0-linux-x86_64.tar.gz]", got)
	}

	// A distfile whose name carries no version — a commit hash, a per-release
	// build id, a SRC_URI arrow that renames it — cannot be derived, and nothing
	// is claimed rather than something invented. Every consumer is then a no-op:
	// less protection for that package, never a wrong action on the host's
	// directory. (An upstream that changes the SHAPE around the version is the
	// same bargain from the other side: the derived name is wrong, so it names
	// no file, so nothing is moved, locked or removed under it.)
	got = s.expectedDistfiles(pkg, pkgDir, "test-pkg",
		[]string{"test-pkg-9a1f2c3d.tar.gz"}, []string{"0.212.0"})
	if len(got) != 0 {
		t.Errorf("expected names for a hash-named archive = %v, want none", got)
	}

	// A stray digit must not manufacture a filename: with 1 and 1.2.11 both on
	// disk, the version that names the file wins and the bare "1" inside it is
	// not a match at all.
	other := filepath.Join(t.TempDir(), "overlay")
	createTestEbuildFile(t, other, pkg, "1")
	createTestEbuildFile(t, other, pkg, "1.2.11")
	createTestEbuildFile(t, other, pkg, "1.3.0")
	otherDir := filepath.Join(other, "test-cat", "test-pkg")
	got = newSweeper(other).expectedDistfiles(pkg, otherDir, "test-pkg",
		[]string{"test-pkg-1.2.11.tar.gz"}, []string{"1.3.0"})
	if len(got) != 1 || got[0] != "test-pkg-1.3.0.tar.gz" {
		t.Errorf("expected names = %v, want [test-pkg-1.3.0.tar.gz]", got)
	}
}
