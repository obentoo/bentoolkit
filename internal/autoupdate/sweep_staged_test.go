package autoupdate

// Authored for story 033, sub-task 3.1 — R3, R3.2.
//
// THE HAZARD THIS SUB-TASK AVOIDS, IN ONE PARAGRAPH. `runManifest` wraps
// `pkgdev manifest` in LockFetch, Quarantine, PrepopulateFromCache and
// RecordFetchScope, and all four are keyed on the PACKAGE DIRECTORY'S Manifest
// (sweep.go:538-591). Quarantine's contract is "a distfile present under a name
// the current Manifest does not list cannot be verified, so move it aside".
// Point that at a staged tree whose Manifest does not YET name the new
// distfile, and the names it moves aside are the HOST'S REAL DISTFILES — and a
// quarantine failure is fatal by contract (sweep.go:563). That is how a
// validation run would come to rearrange /var/cache/distfiles.
//
// The fix needs no new machinery, which is why this sub-task is small:
// `runManifestWithFix` already solves the identical problem for the LLM fixer by
// creating a private distdir under `fixSandboxRoot()` and removing it on the way
// out (applier.go:1112-1117). The staged manifest does the same and seeds it
// through the existing --distfiles-cache symlink path, so nothing is fetched
// twice.
//
// THE MIDDLE ASSERTION IS THE ONE THAT MATTERS: a host distdir populated before
// the run is byte-identical after it. R3.2 says "byte-identical while any gate
// is running" about the overlay; this is the same promise for the other shared
// directory the run touches, and it is the one an operator would notice last.
//
// This file pins `(*sweeper).runStagedManifest(stagedPkgDir, pkg, version string) error`.
//
// fixSandboxRoot (applier.go:35) is a var precisely so a test can answer the
// host's question without a portageq on the machine running the suite; it is
// replaced and restored here, never read from the host.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// stagedManifestCall records one spawned command with the two properties this
// sub-task is about: where it ran, and which distdir it was given.
type stagedManifestCall struct {
	name    string
	args    []string
	dir     string
	distdir string

	// cmd is the child the seam handed back, retained so `dir` can be read
	// AFTER the caller has set it. cmd.Dir is necessarily assigned by the
	// caller once the factory has returned, so recording a value copy at
	// factory time captures dir as "" forever. This is the same reason
	// fixerSeam (manifest_fixer_test.go:39) retains a **exec.Cmd.
	cmd *exec.Cmd
}

// stagedManifestSeam captures every invocation and lets a case decide whether
// the manifest step succeeds. The child is `true`/`false` so nothing real runs.
func stagedManifestSeam(calls *[]stagedManifestCall, fail bool) func(ctx context.Context, name string, arg ...string) *exec.Cmd {
	return func(ctx context.Context, name string, arg ...string) *exec.Cmd {
		child := "true"
		if fail {
			child = "false"
		}
		cmd := exec.CommandContext(ctx, child)

		call := stagedManifestCall{name: name, args: arg, cmd: cmd}
		for i, a := range arg {
			if a == "--distdir" && i+1 < len(arg) {
				call.distdir = arg[i+1]
			}
		}
		*calls = append(*calls, call)
		return cmd
	}
}

// hashDistdirTree digests every path and file body under root, so a distfile
// moved, renamed, created or removed changes the answer. Named for the distdir
// because that is the directory whose stillness is the claim here; the overlay's
// own digest helper lives with the promotion tests.
func hashDistdirTree(t *testing.T, root string) string {
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
		info, err := os.Lstat(p)
		if err != nil {
			t.Fatalf("lstat %q: %v", p, err)
		}
		if info.IsDir() {
			continue
		}
		// Symlinks are hashed by their TARGET NAME, not by following them: a
		// cache symlink appearing in the host distdir is exactly the kind of
		// change this assertion must catch.
		if info.Mode()&os.ModeSymlink != 0 {
			target, err := os.Readlink(p)
			if err != nil {
				t.Fatalf("readlink %q: %v", p, err)
			}
			h.Write([]byte("-> " + target))
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

// stagedManifestFixture lays out the four directories this sub-task keeps
// separate — the staged package dir, the host distdir, the read-only cache, and
// the sandbox root private distdirs are created under — and returns a sweeper
// wired to all of them.
type stagedManifestEnv struct {
	sweeper     *sweeper
	stagedPkg   string
	hostDistdir string
	cacheDir    string
	sandboxRoot string
	calls       *[]stagedManifestCall
}

func stagedManifestFixture(t *testing.T, fail bool) *stagedManifestEnv {
	t.Helper()
	tmp := t.TempDir()

	overlayDir := filepath.Join(tmp, "overlay")
	stagedPkg := filepath.Join(tmp, "staging", "media-plugins", "gst-plugins-qt6", "1.29.2", "media-plugins", "gst-plugins-qt6")
	hostDistdir := filepath.Join(tmp, "host-distdir")
	cacheDir := filepath.Join(tmp, "cache")
	sandboxRoot := filepath.Join(tmp, "sandbox")

	for _, dir := range []string{overlayDir, stagedPkg, hostDistdir, cacheDir, sandboxRoot} {
		if err := os.MkdirAll(dir, 0o750); err != nil {
			t.Fatalf("creating %q: %v", dir, err)
		}
	}

	// The staged package directory as Stage leaves it: the candidate ebuild and
	// a Manifest that does NOT yet name the new distfile. That gap is the whole
	// hazard — quarantine keys on this file.
	if err := os.WriteFile(filepath.Join(stagedPkg, "gst-plugins-qt6-1.29.2.ebuild"), []byte("EAPI=8\n"), 0o644); err != nil {
		t.Fatalf("writing the staged candidate: %v", err)
	}
	if err := os.WriteFile(filepath.Join(stagedPkg, "Manifest"),
		[]byte("DIST gst-plugins-good-1.28.6.tar.xz 100 BLAKE2B ab SHA512 cd\n"), 0o644); err != nil {
		t.Fatalf("writing the staged Manifest: %v", err)
	}

	// The host's real distfiles, including the NEW version's — the file
	// quarantine would move aside if it were pointed here with the staged
	// Manifest in hand.
	for _, name := range []string{"gst-plugins-good-1.28.6.tar.xz", "gst-plugins-good-1.29.2.tar.xz", "unrelated-3.2.1.tar.gz"} {
		if err := os.WriteFile(filepath.Join(hostDistdir, name), []byte("pretend tarball "+name), 0o644); err != nil {
			t.Fatalf("seeding the host distdir: %v", err)
		}
	}
	if err := os.WriteFile(filepath.Join(cacheDir, "gst-plugins-good-1.29.2.tar.xz"), []byte("cached tarball"), 0o644); err != nil {
		t.Fatalf("seeding the cache: %v", err)
	}

	// The sandbox root the private distdir is created under, answered by the
	// test rather than by the host.
	origRoot := fixSandboxRoot
	fixSandboxRoot = func() string { return sandboxRoot }
	t.Cleanup(func() { fixSandboxRoot = origRoot })

	calls := &[]stagedManifestCall{}
	s := newSweeper(overlayDir,
		withSweeperExec(stagedManifestSeam(calls, fail)),
		withSweeperDistdir(hostDistdir, ""),
		withSweeperDistfilesCache(cacheDir),
	)

	return &stagedManifestEnv{
		sweeper:     s,
		stagedPkg:   stagedPkg,
		hostDistdir: hostDistdir,
		cacheDir:    cacheDir,
		sandboxRoot: sandboxRoot,
		calls:       calls,
	}
}

// pkgdevCall returns the single `pkgdev manifest` invocation, failing when there
// is not exactly one.
func pkgdevCall(t *testing.T, calls []stagedManifestCall) stagedManifestCall {
	t.Helper()
	var found []stagedManifestCall
	for _, c := range calls {
		if c.name == "pkgdev" {
			found = append(found, c)
		}
	}
	if len(found) != 1 {
		t.Fatalf("got %d pkgdev invocation(s), want exactly 1: %+v", len(found), calls)
	}
	// Resolved here, not at capture time: see the note on stagedManifestCall.cmd.
	if found[0].cmd != nil {
		found[0].dir = found[0].cmd.Dir
	}
	return found[0]
}

// TestRunStagedManifest_RunsInsideTheStagedTree pins where pkgdev works. It
// discovers the ebuild from its own cwd, so a cwd in the published overlay would
// manifest the published package — the opposite of what staging is for.
func TestRunStagedManifest_RunsInsideTheStagedTree(t *testing.T) {
	env := stagedManifestFixture(t, false)

	if err := env.sweeper.runStagedManifest(env.stagedPkg, "media-plugins/gst-plugins-qt6", "1.29.2"); err != nil {
		t.Fatalf("runStagedManifest: %v", err)
	}

	call := pkgdevCall(t, *env.calls)
	if call.dir != env.stagedPkg {
		t.Errorf("pkgdev ran in %q, want the staged package directory %q", call.dir, env.stagedPkg)
	}
	if !strings.Contains(strings.Join(call.args, " "), "manifest") {
		t.Errorf("the pkgdev invocation %v is not a manifest run", call.args)
	}
}

// TestRunStagedManifest_UsesAPrivateDistdir pins the directory pkgdev is given:
// neither the host's, nor the read-only cache, but a fresh one under the
// sandbox root — the pattern runManifestWithFix already uses.
func TestRunStagedManifest_UsesAPrivateDistdir(t *testing.T) {
	env := stagedManifestFixture(t, false)

	if err := env.sweeper.runStagedManifest(env.stagedPkg, "media-plugins/gst-plugins-qt6", "1.29.2"); err != nil {
		t.Fatalf("runStagedManifest: %v", err)
	}

	call := pkgdevCall(t, *env.calls)
	if call.distdir == "" {
		t.Fatalf("pkgdev was given no --distdir: %v", call.args)
	}
	if call.distdir == env.hostDistdir {
		t.Errorf("pkgdev was pointed at the HOST distdir %q; quarantine would then move the host's real distfiles aside (D3)", call.distdir)
	}
	if call.distdir == env.cacheDir {
		t.Errorf("pkgdev was pointed at the read-only cache %q; the cache is only ever read from", call.distdir)
	}
	if !strings.HasPrefix(filepath.Clean(call.distdir), filepath.Clean(env.sandboxRoot)+string(os.PathSeparator)) {
		t.Errorf("the private distdir %q is not under the sandbox root %q; os.TempDir() is a tmpfs on the host this was measured on, "+
			"which is the defect story 030 removed from the manifest step", call.distdir, env.sandboxRoot)
	}
}

// TestRunStagedManifest_LeavesTheHostDistdirByteIdentical is the assertion this
// sub-task exists for, and the one that would go unnoticed longest if it broke:
// a validation run must not rearrange /var/cache/distfiles.
func TestRunStagedManifest_LeavesTheHostDistdirByteIdentical(t *testing.T) {
	env := stagedManifestFixture(t, false)
	before := hashDistdirTree(t, env.hostDistdir)

	if err := env.sweeper.runStagedManifest(env.stagedPkg, "media-plugins/gst-plugins-qt6", "1.29.2"); err != nil {
		t.Fatalf("runStagedManifest: %v", err)
	}

	if after := hashDistdirTree(t, env.hostDistdir); after != before {
		t.Errorf("the host distdir changed during a staged manifest run: %s -> %s\n"+
			"quarantine keys on the package directory's Manifest, and the staged one does not yet name the new distfile — "+
			"pointed at the host, it moves the host's real distfiles aside (D3)", before, after)
	}

	// Named explicitly, because the digest alone would not say WHICH file went:
	// the new version's tarball is the one quarantine would take.
	for _, name := range []string{"gst-plugins-good-1.28.6.tar.xz", "gst-plugins-good-1.29.2.tar.xz", "unrelated-3.2.1.tar.gz"} {
		if _, err := os.Stat(filepath.Join(env.hostDistdir, name)); err != nil {
			t.Errorf("the host distfile %s is gone after the run: %v", name, err)
		}
	}
}

// TestRunStagedManifest_HostDistdirSurvivesAFailedRun repeats the promise on the
// path where cleanup is easiest to forget.
func TestRunStagedManifest_HostDistdirSurvivesAFailedRun(t *testing.T) {
	env := stagedManifestFixture(t, true)
	before := hashDistdirTree(t, env.hostDistdir)

	if err := env.sweeper.runStagedManifest(env.stagedPkg, "media-plugins/gst-plugins-qt6", "1.29.2"); err == nil {
		t.Fatal("runStagedManifest reported success although pkgdev exited non-zero")
	}

	if after := hashDistdirTree(t, env.hostDistdir); after != before {
		t.Errorf("a FAILED staged manifest changed the host distdir: %s -> %s", before, after)
	}
}

// TestRunStagedManifest_PrivateDistdirIsRemovedEvenOnFailure pins the deferred
// cleanup. The private directory is per-invocation, and a sweep over the whole
// registry that leaks one per failed package fills the scratch filesystem it was
// created to protect.
func TestRunStagedManifest_PrivateDistdirIsRemovedEvenOnFailure(t *testing.T) {
	for _, tc := range []struct {
		name string
		fail bool
	}{
		{"successful run", false},
		{"failed run", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			env := stagedManifestFixture(t, tc.fail)

			err := env.sweeper.runStagedManifest(env.stagedPkg, "media-plugins/gst-plugins-qt6", "1.29.2")
			if tc.fail && err == nil {
				t.Fatal("a failing pkgdev reported success")
			}
			if !tc.fail && err != nil {
				t.Fatalf("runStagedManifest: %v", err)
			}

			entries, readErr := os.ReadDir(env.sandboxRoot)
			if readErr != nil {
				t.Fatalf("reading the sandbox root: %v", readErr)
			}
			if len(entries) != 0 {
				var names []string
				for _, e := range entries {
					names = append(names, e.Name())
				}
				t.Errorf("the private distdir was left behind: %v — it is removed on the way out, on every path", names)
			}
		})
	}
}

// TestRunStagedManifest_ErrorNamesThePackage keeps the failure actionable inside
// a sweep, where one line of output has to identify which of forty packages it
// belongs to. It also pins that the failure is an ordinary error rather than a
// panic or a silent nil.
func TestRunStagedManifest_ErrorNamesThePackage(t *testing.T) {
	env := stagedManifestFixture(t, true)

	err := env.sweeper.runStagedManifest(env.stagedPkg, "media-plugins/gst-plugins-qt6", "1.29.2")

	if err == nil {
		t.Fatal("a failing pkgdev produced no error")
	}
	if !strings.Contains(err.Error(), "gst-plugins-qt6") {
		t.Errorf("the error %q does not name the package; inside a sweep it cannot be attributed", err)
	}
	if !errors.Is(err, ErrManifestFailed) {
		t.Errorf("the error %v does not wrap ErrManifestFailed; the apply path classifies on that sentinel", err)
	}
}
