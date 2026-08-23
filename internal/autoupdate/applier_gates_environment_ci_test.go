// Story 043 — R3, the branch CI itself takes.
//
// Orchestrator-authored during Run mode, in a file of its own so the frozen
// pre-authored test is untouched. Sub-task 3.2's report named three branches its
// tests could not reach; this pins the one that is not a corner case anywhere but
// the machine that gates the merge.
//
// On a host with no `portage` group — which the CI running this package is, as
// portageGroupID's own comment says — buildUserCanRead cannot read group bits and
// answers MET. So the whole precondition mechanism stands down there: nothing is
// ever declined, and every record is cleared on sight. That is the deliberate
// fail-open direction, and it is worth an assertion precisely because it means
// the three tests above measure a behaviour CI does not exercise.

package autoupdate

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/obentoo/bentoolkit/internal/autoupdate/validate"
)

// R3, fail-open — without a portage gid the answer is MET, in both directions of
// the mode bit. A wrong "unmet" freezes a package forever and only this function
// could release it; a wrong "met" costs one build that fails exactly as it failed
// yesterday, which is the floor the change cannot fall below.
func TestBuildUserCanReadFailsOpenWithNoPortageGroup(t *testing.T) {
	orig := portageGroupID
	portageGroupID = func() (int, bool) { return 0, false }
	t.Cleanup(func() { portageGroupID = orig })

	dir := t.TempDir()
	if err := os.Chmod(dir, 0o755); err != nil {
		t.Fatalf("opening the fixture directory: %v", err)
	}
	key := filepath.Join(dir, "module-signing.key")
	if err := os.WriteFile(key, []byte("pretend key"), 0o600); err != nil {
		t.Fatalf("writing the fixture key: %v", err)
	}

	// The same 0600 file the seamed-gid test reads as UNMET. The only difference
	// is that the group cannot be identified, so its bits cannot be scored.
	if !buildUserCanRead(key) {
		t.Error("a host with no portage group answered UNMET; it cannot score group bits, " +
			"so a 0640 root:portage key would read as unreadable and the package would be frozen " +
			"by a check that had no evidence")
	}
}

// R3.3, the consequence — on such a host a recorded precondition is cleared on
// sight rather than accumulating. A record nothing can ever satisfy is the one
// outcome worse than the daily rebuild this story removes.
func TestARecordedPreconditionIsClearedWhereItCannotBeJudged(t *testing.T) {
	env := preconditionGateFixture(t)

	// AFTER the fixture, deliberately: preconditionGateFixture seams
	// portageGroupID to a gid nothing belongs to, so seaming first would simply
	// be overwritten and this test would silently measure the ordinary host
	// instead of the one it names. It was written the other way round first, and
	// failed for exactly that reason.
	orig := portageGroupID
	portageGroupID = func() (int, bool) { return 0, false }
	t.Cleanup(func() { portageGroupID = orig })
	if err := env.cache.SetPrecondition(env.pkg, env.key); err != nil {
		t.Fatalf("SetPrecondition: %v", err)
	}

	// The verdict is not what this test is about; the record is.
	_, _ = env.applier.runBuildGates(env.cand, env.pkg, "1.29.2", validate.DepthConfigure, &ApplyResult{})

	if rec, ok := env.reopen(t).Precondition(env.pkg); ok {
		t.Errorf("the record survived on a host that cannot judge it: %+v — it could never be cleared, "+
			"which is a permanent freeze rather than the daily rebuild this replaces", rec)
	}
}
