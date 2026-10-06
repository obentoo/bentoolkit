// Story 043 — R3. A host failure is diagnosed once, not once per run.
//
// MATERIALISED PER SUB-TASK (Go: an undefined symbol breaks the package build,
// so a fragment landed early silences every other test in internal/autoupdate
// rather than failing). See .draft/red-evidence.yaml.
//
// The log excerpts below are VERBATIM from the run of 2026-08-22. Using the real
// text is the point: an extractor tuned to paraphrased fixtures passes while
// failing on what portage actually prints.

package autoupdate

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/obentoo/bentoolkit/internal/autoupdate/validate"
)

const (
	mt7927EnvFailure = `* ERROR: net-wireless/mt7927-dkms-2.14 failed (setup phase):
*   USE=modules-sign is set but the private key '/etc/kernel/keys/module-signing.key' was not found`

	edk2EnvFailure = `Could not open file or uri for loading private key from /var/lib/sbctl/keys/db/db.key
80FB4E24317F0000:error:8000000D:system library:BIO_new_file:Permission denied:../openssl-3.6.3/crypto/bio/bss_file.c:67:
* ERROR: sys-firmware/edk2-202608 failed (setup phase):
*   Secure Boot signing certificate or key not found or not PEM format.`
)

// --- sub-task 3.1 — record the unmet precondition ---------------------------

// R3.1 — the precondition is a path in both observed cases, and an unrecognised
// message must yield NOTHING.
//
// Fail-open is the load-bearing row. Recording a placeholder for a message the
// extractor did not understand would suppress a package forever on the strength
// of a parse that failed — strictly worse than today's wasteful retry.
func TestExtractUnmetPrecondition(t *testing.T) {
	cases := []struct {
		name string
		log  string
		want string
	}{
		{"module signing key", mt7927EnvFailure, "/etc/kernel/keys/module-signing.key"},
		{"secure boot key", edk2EnvFailure, "/var/lib/sbctl/keys/db/db.key"},
		{"unrecognised", "* ERROR: something else entirely failed (compile phase)", ""},
		{"empty", "", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := extractUnmetPrecondition(tc.log); got != tc.want {
				t.Errorf("extractUnmetPrecondition = %q, want %q", got, tc.want)
			}
		})
	}
}

// R3.1, fail-open — the rows above cover the two messages this host produced.
// These cover the shapes that would make a WRONG record, and each one, recorded,
// freezes a package on a precondition that can never be satisfied.
//
// A relative path is the sharpest of them: `stat` would resolve it against the
// checking process's working directory, which is not the build's, so it would
// answer about a different file every time it was asked.
func TestExtractUnmetPreconditionRefusesAnUnusableAnswer(t *testing.T) {
	cases := []struct {
		name string
		log  string
	}{
		{"a relative path resolves against the wrong directory", `* ERROR: x/y-1 failed (setup phase):
*   the private key 'keys/module-signing.key' was not found`},
		{"a bare word is not a path", `* ERROR: x/y-1 failed (setup phase):
*   the private key 'signing' was not found`},
		{"the root directory names nothing in particular", `* ERROR: x/y-1 failed (setup phase):
*   the private key '/' was not found`},
		{"a compile failure is the ebuild's fault, not the host's", `* ERROR: x/y-1 failed (compile phase):
*   /usr/include/foo.h: No such file or directory`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := extractUnmetPrecondition(tc.log); got != "" {
				t.Errorf("extractUnmetPrecondition = %q, want empty — recording it would freeze the package on a precondition nothing can satisfy", got)
			}
		})
	}
}

// --- sub-task 3.2 — decline the gate while the precondition is unmet --------

// R3.2, Constraint — the precondition must be evaluated AS THE BUILD USER sees
// it. The gate drops to the `portage` uid, and a `stat` from the calling user
// answers a different question: it says the file EXISTS, which is true for both
// rows below and therefore tells the two apart not at all. What separates them
// is the mode, read from the build user's vantage point.
//
// Host-independent by construction. portageGroupID is already a var "so a test
// can answer for a host it is not running on", and it is answered here with a
// gid nothing in the fixture belongs to — so the decision falls to the OTHER
// bits, which the test controls directly. No root, no portage group, and no
// skip: the CI that runs this package has neither, and a skip here would measure
// nothing on the only machine that gates the merge.
func TestPreconditionIsEvaluatedAsTheBuildUser(t *testing.T) {
	origGID := portageGroupID
	portageGroupID = func() (int, bool) { return 4294967, true } // a gid nothing here is in
	t.Cleanup(func() { portageGroupID = origGID })

	dir := t.TempDir()
	// t.TempDir is 0700, which no other user may traverse. The subject of this
	// test is the FILE's mode, so the directory is opened first — otherwise both
	// rows answer "unreadable" for the same reason and the flip proves nothing.
	if err := os.Chmod(dir, 0o755); err != nil {
		t.Fatalf("opening the fixture directory: %v", err)
	}
	key := filepath.Join(dir, "module-signing.key")
	if err := os.WriteFile(key, []byte("pretend key"), 0o600); err != nil {
		t.Fatalf("writing the fixture key: %v", err)
	}

	if buildUserCanRead(key) {
		t.Error("a 0600 file owned by another user reads as readable by the build user; " +
			"os.Stat would answer that way, and answering that way is the bug")
	}

	if err := os.Chmod(key, 0o644); err != nil {
		t.Fatalf("opening the fixture key: %v", err)
	}
	if !buildUserCanRead(key) {
		t.Error("a world-readable file reads as unreadable by the build user; " +
			"if the fixture's ancestors are the cause, this test cannot flip and must be fixed rather than skipped")
	}

	// A path that is not there at all is unmet, not an error: that is the
	// mt7927 case, where the key was never created.
	if buildUserCanRead(filepath.Join(dir, "absent.key")) {
		t.Error("an absent path reads as readable")
	}
}

// R3.2 + R3.4 — while the precondition holds, the gate is DECLINED rather than
// run, it names the path, and it carries Declined = host — which is what keeps
// the package out of the errored count without touching the tally logic.
func TestRecordedPreconditionDeclinesTheGate(t *testing.T) {
	env := preconditionGateFixture(t)

	if err := env.cache.SetPrecondition(env.pkg, env.key); err != nil {
		t.Fatalf("SetPrecondition: %v", err)
	}

	gates, err := env.applier.runBuildGates(t.Context(), env.cand, env.pkg, "1.29.2", validate.DepthConfigure, &ApplyResult{})
	if err != nil {
		t.Fatalf("runBuildGates: %v", err)
	}
	if len(gates) == 0 {
		t.Fatal("no gate was reported at configure depth")
	}
	for _, g := range gates {
		if g.Outcome != validate.OutcomeSkipped {
			t.Errorf("gate %s reported %v, want SKIPPED — the build must not be attempted", g.Gate, g.Outcome)
		}
		if g.Declined != validate.DeclineHost {
			t.Errorf("gate %s declined as %v, want the HOST cause: without it the bump reads as a candidate nothing measured and promotion refuses it (R3.4)", g.Gate, g.Declined)
		}
		if !strings.Contains(g.Reason, env.key) {
			t.Errorf("gate %s does not name the unmet precondition %q: %q", g.Gate, env.key, g.Reason)
		}
	}
	if env.builds() != 0 {
		t.Errorf("%d build child(ren) ran; a build that cannot succeed must not be attempted", env.builds())
	}
}

// R3.3 + R5.3 — THE test that matters, and it is one test on purpose. Asserting
// only the skip would pass just as well over a package frozen forever; the
// precondition has to FLIP, and the second run has to be the consequence of the
// first rather than a fresh fixture that never recorded anything.
//
// Run one: unreadable → gate declined, record written.
// Run two: readable → record cleared, gate runs — with no flag and no waiting.
func TestASatisfiedPreconditionRunsTheGateAgain(t *testing.T) {
	env := preconditionGateFixture(t)

	// --- run one: the key is not readable by the build user ------------------
	if err := env.cache.SetPrecondition(env.pkg, env.key); err != nil {
		t.Fatalf("SetPrecondition: %v", err)
	}
	first, err := env.applier.runBuildGates(t.Context(), env.cand, env.pkg, "1.29.2", validate.DepthConfigure, &ApplyResult{})
	if err != nil {
		t.Fatalf("run one: %v", err)
	}
	if len(first) == 0 || !strings.Contains(first[0].Reason, env.key) {
		t.Fatalf("run one did not decline on the precondition: %+v", first)
	}
	if _, ok := env.reopen(t).Precondition(env.pkg); !ok {
		t.Fatal("run one did not leave the record behind, so run two proves nothing")
	}

	// --- run two: the same key, now readable ---------------------------------
	if err := os.Chmod(env.key, 0o644); err != nil {
		t.Fatalf("opening the key: %v", err)
	}

	second, err := env.applier.runBuildGates(t.Context(), env.cand, env.pkg, "1.29.2", validate.DepthConfigure, &ApplyResult{})
	if err != nil {
		t.Fatalf("run two: %v", err)
	}
	for _, g := range second {
		if strings.Contains(g.Reason, env.key) {
			t.Errorf("run two still declined on the precondition although it is now satisfied: %q", g.Reason)
		}
	}
	if rec, ok := env.reopen(t).Precondition(env.pkg); ok {
		t.Errorf("the record survived the precondition being satisfied: %+v — nothing else clears it, so the package stays frozen", rec)
	}
}
