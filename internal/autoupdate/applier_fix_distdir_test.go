// Story 043 — R2. The distfiles a repair fetched reach the gates that need them.
//
// MATERIALISED PER SUB-TASK, NOT ALL AT ONCE (Go: a test naming a symbol that
// does not exist yet stops the whole package compiling, so a fragment landed
// early silences every other test in internal/autoupdate rather than failing).
// See .draft/red-evidence.yaml.
//
// R5.1 IS LOAD-BEARING HERE. No fixture below may pre-place the candidate's
// archive in the shared distdir. Story 035 shipped with exactly that defect: a
// fixture holding the archive cannot fail for the reason the test exists, and it
// reported green over the bug for a whole story.
//
// HARNESS NOTE. The authored fragment called a helper `newTestSweeper` that does
// not exist in this package. The real harness is stagedManifestFixture /
// pkgdevCall (sweep_staged_test.go), and the tests below are written against it.
// Recorded in .draft/deviations.yaml — the assertions are the fragment's, only
// the way the sweeper is built changed.

package autoupdate

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// --- sub-task 2.1 — the staged manifest accepts a supplied distdir ----------

// R2.2 + R2.3 — the argv is the contract. --force is what makes the re-check
// recompute a COMPLETE Manifest; without it pkgdev does nothing, which is the
// whole bug. It must appear only on the supplied-distdir path, so the ordinary
// path stays byte-identical to today.
func TestStagedManifestForcesOnlyWithASuppliedDistdir(t *testing.T) {
	t.Run("supplied", func(t *testing.T) {
		env := stagedManifestFixture(t, false)
		supplied := t.TempDir()

		got, err := env.sweeper.runStagedManifestIn(supplied, env.stagedPkg, "media-plugins/gst-plugins-qt6", "1.29.2")
		if err != nil {
			t.Fatalf("runStagedManifestIn: %v", err)
		}
		if got != supplied {
			t.Errorf("distdir = %q, want the supplied %q — a fresh one was created", got, supplied)
		}

		call := pkgdevCall(t, *env.calls)
		if call.distdir != supplied {
			t.Errorf("pkgdev was given --distdir %q, want the supplied %q", call.distdir, supplied)
		}
		if !containsArg(call.args, "--force") {
			t.Errorf("--force absent from %v: a complete Manifest will not be recomputed, which is the defect itself", call.args)
		}
	})

	t.Run("not supplied", func(t *testing.T) {
		env := stagedManifestFixture(t, false)

		got, err := env.sweeper.runStagedManifest(env.stagedPkg, "media-plugins/gst-plugins-qt6", "1.29.2")
		if err != nil {
			t.Fatalf("runStagedManifest: %v", err)
		}
		if got == "" {
			t.Fatal("the ordinary path returned no distdir")
		}

		call := pkgdevCall(t, *env.calls)
		if containsArg(call.args, "--force") {
			t.Errorf("--force leaked into the ordinary path: %v", call.args)
		}
	})
}

// R2.3 — a supplied directory is already the answer, so the seeding step must
// not run against it. PrepopulateFromCache seeds by SYMLINK, and a link planted
// beside the bytes the repair actually fetched is a second, unnecessary claim
// about what this version needs — one that outlives the cache entry it points at.
//
// The assertion is on the directory's contents rather than on a call count,
// because the seeding is what would be observable to pkgdev and to the gate.
func TestStagedManifestDoesNotSeedASuppliedDistdir(t *testing.T) {
	env := stagedManifestFixture(t, false)

	// THE FIXTURE HAS TO BE ABLE TO REACH THE ASSERTION, and the shipped one
	// cannot: expectedDistfiles derives a name only by substituting the version of
	// ANOTHER ebuild in the staged directory into the Manifest's DIST names, and
	// stagedManifestFixture puts exactly one ebuild there — the version being
	// manifested. So `expected` comes back empty, PrepopulateFromCache is never
	// called on either path, and a guard written without this line PASSES with the
	// seeding mutated back in. It was written without it, and the mutation proof
	// is what caught it.
	//
	// A second ebuild gives the derivation something to substitute, so `expected`
	// becomes ["gst-plugins-good-1.29.2.tar.xz"] — a name the fixture's cache
	// holds, and therefore a seed that would really happen.
	if err := os.WriteFile(filepath.Join(env.stagedPkg, "gst-plugins-qt6-1.28.6.ebuild"), []byte("EAPI=8\n"), 0o644); err != nil {
		t.Fatalf("writing the second staged ebuild: %v", err)
	}

	// The supplied directory as the fixer would leave it: the bytes it fetched,
	// and nothing else. Its name is deliberately NOT one of the expected names —
	// PrepopulateFromCache skips a name already present, which would hide the seed
	// behind a second, unrelated reason.
	supplied := t.TempDir()
	fetched := filepath.Join(supplied, "gst-plugins-qt6-1.29.2.tar.xz")
	if err := os.WriteFile(fetched, []byte("the bytes the repair fetched"), 0o600); err != nil {
		t.Fatalf("seeding the supplied distdir: %v", err)
	}

	if _, err := env.sweeper.runStagedManifestIn(supplied, env.stagedPkg, "media-plugins/gst-plugins-qt6", "1.29.2"); err != nil {
		t.Fatalf("runStagedManifestIn: %v", err)
	}

	entries, err := os.ReadDir(supplied)
	if err != nil {
		t.Fatalf("reading the supplied distdir: %v", err)
	}
	if len(entries) != 1 {
		t.Errorf("the supplied distdir gained %d entries, want the 1 it arrived with: %v", len(entries), entries)
	}
	for _, e := range entries {
		info, err := e.Info()
		if err != nil {
			t.Fatalf("stat %s: %v", e.Name(), err)
		}
		if info.Mode()&os.ModeSymlink != 0 {
			t.Errorf("%s is a symlink: the seeding step ran against a directory that was already the answer", e.Name())
		}
	}

	// And the bytes the repair fetched are untouched — a seed that overwrote them
	// would be worse than one that merely added a link.
	body, err := os.ReadFile(fetched)
	if err != nil || string(body) != "the bytes the repair fetched" {
		t.Errorf("the supplied archive was replaced: %q, %v", body, err)
	}
}

// R2.4 — the ordinary path is byte-identical to today. Everything that runs
// without a supplied distdir still creates its own under the sandbox root, still
// seeds it from the cache, and still leaves the host distdir alone.
func TestStagedManifestWithoutASuppliedDistdirIsUnchanged(t *testing.T) {
	env := stagedManifestFixture(t, false)
	before := hashDistdirTree(t, env.hostDistdir)

	distdir, err := env.sweeper.runStagedManifest(env.stagedPkg, "media-plugins/gst-plugins-qt6", "1.29.2")
	if err != nil {
		t.Fatalf("runStagedManifest: %v", err)
	}

	if !filepathHasPrefix(distdir, env.sandboxRoot) {
		t.Errorf("the private distdir %q is not under the sandbox root %q", distdir, env.sandboxRoot)
	}
	if after := hashDistdirTree(t, env.hostDistdir); after != before {
		t.Errorf("the host distdir changed: %s -> %s", before, after)
	}
}

// filepathHasPrefix answers whether path sits under root, comparing cleaned
// paths and requiring a separator so /a/bc is not read as being under /a/b.
func filepathHasPrefix(path, root string) bool {
	rel, err := filepath.Rel(filepath.Clean(root), filepath.Clean(path))
	return err == nil && rel != ".." && !filepath.IsAbs(rel) &&
		!(len(rel) >= 3 && rel[:3] == ".."+string(filepath.Separator))
}

// --- sub-task 2.2 — the fixer's distdir is transferred, not discarded -------

// R2.1 + R5.1 — the defect itself, end to end. The fixer succeeds, so the
// Manifest is complete and the old re-check downloads nothing; the run's distdir
// is then empty and the gate falls back to the host DISTDIR. Asserting the gate
// reads a path UNDER the run's own directory is what fails today.
//
// R5.1 IS THE FIXTURE CONTRACT: the shared distdir deliberately holds nothing
// belonging to this candidate. newGateApplier creates ebuilds and no distfiles,
// and nothing below adds one — a fixture that pre-placed the archive could not
// fail for the reason this test exists, which is how story 035 shipped green
// over this same bug.
func TestGateReadsTheDistdirTheFixFetched(t *testing.T) {
	applier, fixer, _ := newGateApplier(t, pkgdevFailsUntilFixed(), gatePkg{"a/b", "1.0", "2.0"})

	var fetched string
	fixer.onCall = func(req ManifestFixRequest) {
		// Stand in for the agent's own `pkgdev manifest --distdir`: the bytes
		// land in the private directory the fixer was handed, and the Manifest it
		// writes is COMPLETE — which is exactly why the re-check then downloads
		// nothing and the second directory stays empty.
		fetched = req.DistDir
		if err := os.WriteFile(filepath.Join(req.DistDir, "b-2.0.tar.gz"), []byte("x"), 0o600); err != nil {
			t.Errorf("seed the fix distdir: %v", err)
		}
	}

	res, _ := applier.Apply("a/b", true)
	if fetched == "" {
		t.Fatal("the fixer was never invoked; this test cannot fail for its own reason")
	}

	read := gateDistdirFrom(t, res)
	if read == "" {
		t.Fatal("no gate reported which directory it searched")
	}
	if !strings.HasPrefix(read, fetched) {
		t.Errorf("the gate read %q, not the directory this run fetched into (%q)", read, fetched)
	}
}

// R2.4 + story 035 R2.2 — ownership moved, the removal did not. The directory
// must be gone when Apply returns, including when the re-check fails: a sweep
// over forty packages must not retain forty tarballs.
func TestTheTransferredDistdirIsStillRemoved(t *testing.T) {
	for _, tc := range []struct {
		name    string
		recheck func() func(ctx context.Context, name string, arg ...string) *exec.Cmd
	}{
		{"re-check succeeds", pkgdevFailsUntilFixed},
		{"re-check still fails", func() func(context.Context, string, ...string) *exec.Cmd {
			return pkgdevFailsPrinting("still broken")
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			applier, fixer, _ := newGateApplier(t, tc.recheck(), gatePkg{"a/b", "1.0", "2.0"})
			var dir string
			fixer.onCall = func(req ManifestFixRequest) {
				dir = req.DistDir
				// Non-empty, because an empty directory would be removed by a
				// code path that never owned it and the assertion would hold for
				// the wrong reason.
				if err := os.WriteFile(filepath.Join(req.DistDir, "b-2.0.tar.gz"), []byte("x"), 0o600); err != nil {
					t.Errorf("seed the fix distdir: %v", err)
				}
			}

			applier.Apply("a/b", true) //nolint:errcheck // the apply's verdict is not what this test is about

			if dir == "" {
				t.Fatal("the fixer was never invoked")
			}
			if _, err := os.Stat(dir); !os.IsNotExist(err) {
				t.Errorf("the distdir survived the apply: %v", err)
			}
		})
	}
}

// R2.4 — a repair that fetched nothing must leave the fall-back exactly as it is
// today. An EMPTY private directory means "nothing was fetched", not "nothing is
// there", and preferring it would hand the gate the empty room this story exists
// to remove — with the shared distdir, which does hold something, right there
// unread. This is the row that stops the fix from becoming the bug's mirror image.
func TestAnEmptyFixDistdirStillFallsBack(t *testing.T) {
	applier, fixer, _ := newGateApplier(t, pkgdevFailsUntilFixed(), gatePkg{"a/b", "1.0", "2.0"})

	var fetched string
	fixer.onCall = func(req ManifestFixRequest) {
		// The repair succeeds WITHOUT fetching: it rewrote SRC_URI to a name
		// already digested, so its private directory stays empty.
		fetched = req.DistDir
	}

	res, _ := applier.Apply("a/b", true)
	if fetched == "" {
		t.Fatal("the fixer was never invoked; this test cannot fail for its own reason")
	}

	read := gateDistdirFrom(t, res)
	if read != "" && strings.HasPrefix(read, fetched) {
		t.Errorf("the gate was pointed at the empty directory %q instead of falling back; "+
			"an empty private distdir means nothing was fetched, not that nothing is there", read)
	}
}
