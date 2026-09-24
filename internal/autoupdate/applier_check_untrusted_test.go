package autoupdate

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/obentoo/bentoolkit/internal/autoupdate/validate"
)

// checkUntrustedGoodHash is a well-formed commit hash: 40 lowercase hex.
const checkUntrustedGoodHash = "bc99a094bd926ae7b5ab8643947ce1438c950720"

// commandRecorder is an exec seam that remembers every command it was asked to
// build. Each command it returns exits non-zero, so a Validate that reaches the
// manifest step stops there with the staged tree left in place — which is what
// lets the well-formed guard read the staged ebuild.
type commandRecorder struct {
	mu    sync.Mutex
	calls []string
}

func (r *commandRecorder) build(ctx context.Context, name string, arg ...string) *exec.Cmd {
	r.mu.Lock()
	r.calls = append(r.calls, strings.TrimSpace(name+" "+strings.Join(arg, " ")))
	r.mu.Unlock()
	return exec.CommandContext(ctx, "false")
}

func (r *commandRecorder) recorded() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.calls...)
}

// checkUntrustedRun lays out one pending bump carrying update, and an applier
// that validates it through a staging root at compile depth with every command
// recorded. It returns the applier, the fixture, the staging root and the
// recorder.
func checkUntrustedRun(t *testing.T, update PendingUpdate) (*Applier, *untrustedFixture, string, *commandRecorder) {
	t.Helper()
	f := newUntrustedFixture(t, update)
	stagingRoot := filepath.Join(f.configDir, "staging")
	rec := &commandRecorder{}
	a := f.applier(t,
		WithExecCommand(rec.build),
		WithConfirmFunc(func(string) bool { return true }),
		WithApplierStagingRoot(stagingRoot),
		WithApplierDepth(validate.DepthCompile),
		WithApplierIsolationProbe(func() (bool, string) { return true, "" }),
	)
	return a, f, stagingRoot, rec
}

// stagedCandidates lists every file under the staging root that is the
// candidate ebuild for the fixture's new version.
func stagedCandidates(t *testing.T, f *untrustedFixture, stagingRoot string) map[string]string {
	t.Helper()
	name := "betterbird-bin-" + f.newVersion + ".ebuild"
	out := map[string]string{}
	for rel, body := range snapshotTree(t, stagingRoot) {
		if filepath.Base(rel) == name {
			out[rel] = body
		}
	}
	return out
}

// TestValidateRefusesMalformedUpstreamValues pins S050-R1.6: `--check` refuses a
// malformed aux value or commit hash BEFORE it stages anything or runs any
// command.
//
// A SKIPPED outcome alone would not prove that. Once applySubstitutions refuses
// the value too (R1.7), a Validate without its own early gate would still stage
// the source ebuild, fail inside prepareInStagingTree and report SKIPPED — having
// written a candidate tree first. So the assertions that carry R1.6 are the
// empty staging root and the zero recorded commands; the reason checks carry the
// "names the package, the %q value and the sentinel text" half.
func TestValidateRefusesMalformedUpstreamValues(t *testing.T) {
	cases := []struct {
		name     string
		update   PendingUpdate
		value    string
		sentinel error
	}{
		{"quote-escape aux", PendingUpdate{AuxValue: `x"; touch /tmp/pwned; "`}, `x"; touch /tmp/pwned; "`, ErrInvalidAuxValue},
		{"template reference aux", PendingUpdate{AuxValue: "a${1}b"}, "a${1}b", ErrInvalidAuxValue},
		{"uppercase 40-hex hash", PendingUpdate{CommitHash: strings.ToUpper(checkUntrustedGoodHash)}, strings.ToUpper(checkUntrustedGoodHash), ErrInvalidCommitHash},
		{"39-hex hash", PendingUpdate{CommitHash: checkUntrustedGoodHash[:39]}, checkUntrustedGoodHash[:39], ErrInvalidCommitHash},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			a, f, stagingRoot, rec := checkUntrustedRun(t, tc.update)
			pendingPath := filepath.Join(f.configDir, "pending.json")
			pendingBefore, err := os.ReadFile(pendingPath)
			if err != nil {
				t.Fatalf("read pending.json before: %v", err)
			}
			overlayBefore := snapshotTree(t, f.overlayDir)

			result := a.Validate(f.pkg, validate.DepthNone)

			if calls := rec.recorded(); len(calls) != 0 {
				t.Errorf("Validate ran %d command(s) for a malformed value, want none: %q", len(calls), calls)
			}
			if staged := snapshotTree(t, stagingRoot); len(staged) != 0 {
				t.Errorf("Validate staged files for a malformed value, want none: %v", snapshotPaths(staged))
			}
			if cands := stagedCandidates(t, f, stagingRoot); len(cands) != 0 {
				t.Errorf("the staging root holds a candidate ebuild: %v", snapshotPaths(cands))
			}

			if len(result.Gates) == 0 {
				t.Fatalf("Validate reported no gate; a refusal must say why")
			}
			for _, gate := range result.Gates {
				if gate.Outcome != validate.OutcomeSkipped {
					t.Errorf("gate %s outcome = %s, want %s (reason %q)", gate.Gate, gate.Outcome, validate.OutcomeSkipped, gate.Reason)
				}
			}
			if worst := result.WorstOutcome(); worst != validate.OutcomeSkipped {
				t.Errorf("WorstOutcome = %s, want %s", worst, validate.OutcomeSkipped)
			}

			quoted := fmt.Sprintf("%q", tc.value)
			named := false
			for _, gate := range result.Gates {
				if strings.Contains(gate.Reason, f.pkg) &&
					strings.Contains(gate.Reason, quoted) &&
					strings.Contains(gate.Reason, tc.sentinel.Error()) {
					named = true
				}
			}
			if !named {
				reasons := make([]string, 0, len(result.Gates))
				for _, gate := range result.Gates {
					reasons = append(reasons, gate.Reason)
				}
				t.Errorf("no gate reason names the package %s, the value %s and %q together: %q",
					f.pkg, quoted, tc.sentinel.Error(), reasons)
			}

			pendingAfter, err := os.ReadFile(pendingPath)
			if err != nil {
				t.Fatalf("read pending.json after: %v", err)
			}
			if string(pendingAfter) != string(pendingBefore) {
				t.Errorf("pending.json changed on a --check refusal\nbefore: %s\n after: %s", pendingBefore, pendingAfter)
			}
			if overlayAfter := snapshotTree(t, f.overlayDir); len(overlayAfter) != len(overlayBefore) {
				t.Errorf("the published overlay changed: before %v, after %v", snapshotPaths(overlayBefore), snapshotPaths(overlayAfter))
			}
		})
	}
}

// TestValidateAcceptsWellFormedUpstreamValues is the regression guard for R1.6
// and R7.9: well-formed values are not refused early, they reach the staged
// candidate verbatim, and Validate goes on to run the manifest step. Green
// before the gate lands, and must stay green after.
func TestValidateAcceptsWellFormedUpstreamValues(t *testing.T) {
	a, f, stagingRoot, rec := checkUntrustedRun(t, PendingUpdate{
		AuxValue:   "esr-bb24",
		CommitHash: checkUntrustedGoodHash,
	})

	result := a.Validate(f.pkg, validate.DepthNone)

	if len(rec.recorded()) == 0 {
		t.Errorf("Validate ran no command for well-formed values; it was refused before the manifest step: %+v", result)
	}
	cands := stagedCandidates(t, f, stagingRoot)
	if len(cands) != 1 {
		t.Fatalf("staged candidate ebuilds = %v, want exactly one", snapshotPaths(cands))
	}
	for rel, body := range cands {
		for _, want := range []string{
			`MY_BUILD="esr-bb24"` + "\n",
			`EGIT_COMMIT="` + checkUntrustedGoodHash + `"` + "\n",
		} {
			if !strings.Contains(body, want) {
				t.Errorf("staged candidate %s lacks %q:\n%s", rel, want, body)
			}
		}
	}
	for _, gate := range result.Gates {
		if strings.Contains(gate.Reason, ErrInvalidAuxValue.Error()) || strings.Contains(gate.Reason, ErrInvalidCommitHash.Error()) {
			t.Errorf("gate %s refused a well-formed value: %q", gate.Gate, gate.Reason)
		}
	}
}
