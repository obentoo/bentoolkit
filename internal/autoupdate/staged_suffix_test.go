package autoupdate

// Authored for story 040, task 5 — R5.1, R5.5.
//
// THE INVARIANT THAT CLOSES THE CLASS. promote.go's stagedCandidate asserts, in
// a comment, that the staged tree and the promoting lookup share one layout
// ("the two must be read together: a change there is a change here") — and
// nothing verified it. That unverified assertion is exactly where D4 passed: a
// suffixed registry key staged its content UNDER THE SUFFIXED NAME while every
// promoting-side path derivation stripped the suffix, so the first gate to
// touch the divergence (`pkgdev manifest`, chdir'ing into the clean directory)
// failed on a directory that never existed. Measured on the maintainer's host,
// 2026-08-19: `--apply all` failed 4/4 suffixed entries with
// `chdir …/zed-bin@preview/1.17.0_pre/app-editors/zed-bin: no such file or
// directory`.
//
// THE FIXER MUST NOT BE SPENT ON THAT FAILURE (R5.5). A spawned command whose
// working directory does not exist is the machine's (or, as in D4, the
// layout's) fault and never the ebuild's — the fixer's only repair is rewriting
// an intact file, and the fixer runs on the operator's subscription quota.

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/obentoo/bentoolkit/internal/autoupdate/validate"
)

// TestStagedAndPromotingLayoutsAgreeOnASuffixedKey is the Stage→Stat invariant:
// stage a suffixed key, then require the path the PROMOTING side derives for the
// same key to exist. Had this test existed, D4 could not have shipped.
func TestStagedAndPromotingLayoutsAgreeOnASuffixedKey(t *testing.T) {
	overlay := t.TempDir()
	stagingRoot := filepath.Join(t.TempDir(), "staging")

	const key = "app-editors/zed-bin@preview"
	const version = "1.17.0"

	stagedRoot, err := validate.Stage(validate.StageRequest{
		Overlay:     overlay,
		StagingRoot: stagingRoot,
		Key:         key,
		Version:     version,
		EbuildBytes: []byte("EAPI=8\n"),
	})
	if err != nil {
		t.Fatalf("Stage(%q): %v", key, err)
	}

	cand, err := stagedCandidate(stagedRoot, key, version)
	if err != nil {
		t.Fatalf("stagedCandidate(%q): %v", key, err)
	}

	if _, err := os.Stat(cand.pkgDir); err != nil {
		t.Errorf("the promoting layout expects the package directory %s and the staged tree does not "+
			"carry it: %v\nthe two layouts are asserted to be one line of code and this is the assertion",
			cand.pkgDir, err)
	}
	if _, err := os.Stat(cand.ebuildPath); err != nil {
		t.Errorf("the promoting layout expects the candidate at %s and the staged tree does not carry "+
			"it: %v\nevery gate from the manifest step onward chdir-fails on exactly this divergence",
			cand.ebuildPath, err)
	}
}

// startFailure produces the real error a spawned command yields when its working
// directory does not exist — the exact shape the staged manifest step produced
// in the field — rather than a hand-built imitation of it.
func startFailure(t *testing.T) error {
	t.Helper()
	cmd := exec.Command("true")
	cmd.Dir = filepath.Join(t.TempDir(), "vanished")
	err := cmd.Run()
	if err == nil {
		t.Fatalf("instrument: running in a nonexistent directory succeeded; no start failure to classify")
	}
	if !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("instrument: the start failure %v does not satisfy errors.Is(_, fs.ErrNotExist); "+
			"the classifier under test keys on that property", err)
	}
	return err
}

// stagedManifestFailure wraps err exactly as runStagedManifest builds its error
// (sweep_staged.go): ErrManifestFailed first, the run error chained with %w, the
// captured output appended. The classifier is handed THIS shape in production.
func stagedManifestFailure(err error) error {
	return fmt.Errorf("%w: staged manifest for %s-%s in %s: %w\nOutput: %s",
		ErrManifestFailed, "app-editors/zed-bin@preview", "1.17.0", "/staging/gone/app-editors/zed-bin", err, "")
}

// TestEnvironmentVerdict_MissingWorkdirNeverReachesTheFixer is R5.5: a command
// that could not START because its working directory does not exist is the
// environment's failure. No ebuild rewrite can repair it, so the verdict must be
// non-nil — that is what keeps runManifestWithFix from spending the fixer on an
// ebuild that was never broken.
func TestEnvironmentVerdict_MissingWorkdirNeverReachesTheFixer(t *testing.T) {
	verdict := environmentVerdict(stagedManifestFailure(startFailure(t)))
	if verdict == nil {
		t.Fatalf("environmentVerdict = nil for a chdir-ENOENT start failure; the fixer would be invoked " +
			"with the same nonexistent directory as its own cwd, spending quota on an intact ebuild")
	}
	if !errors.Is(verdict, ErrManifestEnvironment) {
		t.Errorf("the verdict %v does not carry ErrManifestEnvironment; refuseFixOnEnvironmentFailure "+
			"keys on that sentinel", verdict)
	}
}

// TestEnvironmentVerdict_AnExitedCommandStillReachesTheFixer is R5.5's guard: a
// command that RAN and exited non-zero is pkgdev telling us about the ebuild,
// and the fixer must still get to see it. The missing-workdir class is keyed on
// a failure to start, never on an *exec.ExitError.
func TestEnvironmentVerdict_AnExitedCommandStillReachesTheFixer(t *testing.T) {
	exitErr := exec.Command("false").Run()
	if exitErr == nil {
		t.Fatalf("instrument: `false` exited zero; no exit failure to classify")
	}
	var exit *exec.ExitError
	if !errors.As(exitErr, &exit) {
		t.Fatalf("instrument: %v is not an *exec.ExitError", exitErr)
	}

	if verdict := environmentVerdict(stagedManifestFailure(exitErr)); verdict != nil {
		t.Errorf("environmentVerdict = %v for a plain non-zero exit; a command that ran is evidence "+
			"about the ebuild, and refusing the fixer here would lose repairs the gate exists to allow", verdict)
	}
}
