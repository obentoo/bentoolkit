package autoupdate

// The harness for story 043's sub-task 3.2. It is separated from the tests it
// serves because those tests are a read-only input to the sub-task: the file
// next door states its assertions and this file has to satisfy them, not the
// other way round.
//
// It is built on promoteFixture rather than on a hand-rolled Applier so that
// runBuildGates is entered the way production enters it — a staged candidate,
// under a staging root outside the overlay, with every child observed.

import (
	"os"
	"path/filepath"
	"testing"
)

// preconditionGateEnv is one staged bump, one unreadable key and the cache the
// two meet in.
type preconditionGateEnv struct {
	// applier is a staged applier whose every child process fails and is
	// counted. Failing children are the right default here: the point of these
	// tests is the gate that must NOT be reached, and a run that gets past the
	// pre-check must be visible as a spawn rather than as a passing gate.
	applier *Applier
	// cand is staged and therefore past runBuildGates' early return, which
	// refuses to report anything for an unstaged candidate or a depth below
	// DepthPatches.
	cand candidatePaths
	pkg  string
	// key is the fixture's unmet precondition: a file the build user cannot
	// read, inside a directory the build user CAN traverse.
	key string
	// cache is opened over the applier's own config directory, so a record
	// written through it is the record the applier reads.
	cache *Cache

	watch          *promoteWatch
	spawnsAtSetup  int
	applierConfDir string
}

// reopen re-reads cache.json from disk, so an assertion made through it is about
// the FILE and not about the map this test still holds in memory. The applier
// writes through a Cache of its own; without this a cleared record would still
// be visible in env.cache and the flip test would pass over a package that was
// never actually released.
func (e *preconditionGateEnv) reopen(t *testing.T) *Cache {
	t.Helper()
	cache, err := NewCache(e.applierConfDir)
	if err != nil {
		t.Fatalf("reopening the cache: %v", err)
	}
	return cache
}

// builds reports how many child processes ran since the fixture was built. It is
// relative to setup rather than absolute so that anything the fixture itself
// spawns cannot be read as a build the gate ran.
func (e *preconditionGateEnv) builds() int {
	return e.watch.spawns - e.spawnsAtSetup
}

// preconditionGateFixture lays out that environment.
//
// # The portage group is answered, not looked up
//
// portageGroupID is seamed to a gid nothing in this fixture belongs to, which is
// what the seam exists for ("so a test can answer for a host it is not running
// on"). Two things follow, and both are the point: the decision falls to the
// mode bits this fixture sets directly, and the tests need neither root nor a
// portage group — so they measure the same thing on the CI that gates the merge,
// where neither exists, as they do on a Gentoo workstation. No skip is involved
// anywhere, because a skipped guard is a guard that measures nothing.
//
// # The key's directory is opened, and that is load-bearing
//
// t.TempDir hands back a directory nobody outside its owner may traverse. Left
// alone, a key inside it is unreadable by the build user because of the
// DIRECTORY, and the flip test would then see "unreadable" before and after its
// chmod — passing while proving nothing about the file's own mode. Opening the
// directory to 0755 makes the file's mode the only thing left to decide it.
func preconditionGateFixture(t *testing.T) *preconditionGateEnv {
	t.Helper()

	orig := portageGroupID
	portageGroupID = func() (int, bool) { return 4294967, true } // a gid nothing here is in
	t.Cleanup(func() { portageGroupID = orig })

	applier, overlayDir, pkg, watch, _ := promoteFixture(t)

	// A real staged tree, through validate.Stage, so cand names paths the
	// production code would recognise rather than a shape invented here.
	staged := stageCandidateFor(t, applier.StagingRoot(), overlayDir, candidateBody(t, overlayDir))
	cand, err := stagedCandidate(staged, pkg, "1.29.2")
	if err != nil {
		t.Fatalf("naming the staged candidate: %v", err)
	}

	dir := t.TempDir()
	if err := os.Chmod(dir, 0o755); err != nil {
		t.Fatalf("opening the fixture directory: %v", err)
	}
	key := filepath.Join(dir, "module-signing.key")
	if err := os.WriteFile(key, []byte("pretend key"), 0o600); err != nil {
		t.Fatalf("writing the fixture key: %v", err)
	}

	cache, err := NewCache(applier.configDir)
	if err != nil {
		t.Fatalf("opening the cache: %v", err)
	}

	return &preconditionGateEnv{
		applier:        applier,
		cand:           cand,
		pkg:            pkg,
		key:            key,
		cache:          cache,
		watch:          watch,
		spawnsAtSetup:  watch.spawns,
		applierConfDir: applier.configDir,
	}
}
