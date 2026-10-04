package autoupdate

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// Story 064, R2.6: when runManifestWithFix combines the manifest failure with the
// error of a skipped or failed fix attempt, the MANIFEST FAILURE is the wrapped
// cause and the fix attempt's error is context only. errors.Is/errors.As on the
// apply's error must answer for the manifest failure, and must no longer answer
// for the fix attempt's error.
//
// The two identities, observed from outside runManifestWithFix:
//
//   - the manifest failure is the `pkgdev manifest` run that exited non-zero, so
//     it is reachable as an *exec.ExitError (and as the manifest step's own
//     *manifestRunError, which the environment gate already reads with
//     errors.As);
//   - the fix attempt's error is whatever the fixer returned (the "LLM fix
//     attempt failed" site) or whatever stopped the fixer's private distdir
//     from being made (the "manifest fix skipped" site).
//
// Hostile halves first, per requirement: (1) the fix attempt's error must NOT
// answer errors.Is — including a sentinel it wraps, the realistic reclassifier;
// (2) the manifest failure MUST answer. Then the benign half: ErrManifestFailed
// still answers and both texts still reach the operator, so a "fix" that simply
// drops the fix attempt's error from the message does not pass.

// s064ManifestOutput is what the failing pkgdev prints. It names a URL, not the
// machine, so the environment gate classifies it as repairable and the path
// reaches the fix attempt.
const s064ManifestOutput = "SRC_URI is unreachable: 404 Not Found"

// s064AssertManifestFailureIsTheCause is the shared hostile/benign check for
// both wrap sites. secondary is the fix attempt's error and hidden lists the
// errors.Is targets that only the fix attempt's error could satisfy.
func s064AssertManifestFailureIsTheCause(t *testing.T, err error, secondaryText string, hidden ...error) {
	t.Helper()

	// (1) Hostile: the fix attempt's error must not classify the apply.
	for _, target := range hidden {
		if errors.Is(err, target) {
			t.Errorf("errors.Is(applyErr, %q) = true, want false: the fix attempt's error is context, "+
				"not the cause (R2.6)\napplyErr: %v", target, err)
		}
	}

	// (2) Hostile converse: the manifest failure must still be reachable.
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) {
		t.Errorf("errors.As(applyErr, *exec.ExitError) = false, want true: the failed `pkgdev manifest` run "+
			"is the cause and must be wrapped with %%w (R2.6)\napplyErr: %v", err)
	}
	var mre *manifestRunError
	if !errors.As(err, &mre) {
		t.Errorf("errors.As(applyErr, *manifestRunError) = false, want true: the manifest step's failure "+
			"must be wrapped with %%w (R2.6)\napplyErr: %v", err)
	}

	// (3) Benign: classification by the manifest sentinel holds, and the
	// operator still reads both the manifest output and the fix attempt's error.
	if !errors.Is(err, ErrManifestFailed) {
		t.Errorf("errors.Is(applyErr, ErrManifestFailed) = false, want true\napplyErr: %v", err)
	}
	msg := err.Error()
	if !strings.Contains(msg, s064ManifestOutput) {
		t.Errorf("applyErr does not carry the manifest output %q\napplyErr: %v", s064ManifestOutput, err)
	}
	if !strings.Contains(msg, secondaryText) {
		t.Errorf("applyErr does not carry the fix attempt's error %q: it must stay in the text as context "+
			"(formatted with %%v), not be dropped\napplyErr: %v", secondaryText, err)
	}
}

// TestS064ManifestFixWrapKeepsManifestFailureAsCause covers both combining sites
// of runManifestWithFix through Apply.
func TestS064ManifestFixWrapKeepsManifestFailureAsCause(t *testing.T) {
	// Not parallel: fixSandboxRoot is a package-level seam.

	t.Run("LLM fix attempt failed", func(t *testing.T) {
		prev := fixSandboxRoot
		root := t.TempDir()
		fixSandboxRoot = func(context.Context) string { return root }
		t.Cleanup(func() { fixSandboxRoot = prev })

		pkg := "dev-games/godot"
		applier, fixer, _ := newGateApplier(t,
			pkgdevFailsPrinting(s064ManifestOutput),
			gatePkg{pkg, "4.7_rc3", "4.7"})

		// The fixer's error wraps a sentinel a report classifies on. Before
		// R2.6 it answered errors.Is from the apply's error; it must not.
		const fixText = "s064 fixer gave up"
		fixErr := fmt.Errorf("%s: %w", fixText, ErrClaudeTimedOut)
		fixer.err = fixErr

		result, err := applier.Apply(t.Context(), pkg, false)
		if err == nil || result == nil || result.Success {
			t.Fatalf("expected the apply to fail: err=%v result=%+v", err, result)
		}
		if fixer.called != 1 {
			t.Fatalf("the fixer ran %d time(s), want 1: this subtest is about the "+
				"\"LLM fix attempt failed\" site\napplyErr: %v", fixer.called, err)
		}
		if !strings.Contains(err.Error(), "LLM fix attempt failed") {
			t.Fatalf("applyErr does not come from the \"LLM fix attempt failed\" site\napplyErr: %v", err)
		}

		s064AssertManifestFailureIsTheCause(t, err, fixText, fixErr, ErrClaudeTimedOut)
	})

	t.Run("manifest fix skipped", func(t *testing.T) {
		// A root that does not exist makes the fixer's private distdir
		// impossible to create, which is the "manifest fix skipped" site. The
		// error that stops it is an *fs.PathError satisfying fs.ErrNotExist.
		prev := fixSandboxRoot
		missing := filepath.Join(t.TempDir(), "no-such-root")
		fixSandboxRoot = func(context.Context) string { return missing }
		t.Cleanup(func() { fixSandboxRoot = prev })

		pkg := "dev-games/godot"
		applier, fixer, _ := newGateApplier(t,
			pkgdevFailsPrinting(s064ManifestOutput),
			gatePkg{pkg, "4.7_rc3", "4.7"})

		result, err := applier.Apply(t.Context(), pkg, false)
		if err == nil || result == nil || result.Success {
			t.Fatalf("expected the apply to fail: err=%v result=%+v", err, result)
		}
		if fixer.called != 0 {
			t.Fatalf("the fixer ran %d time(s), want 0: its distdir could not be made", fixer.called)
		}
		if !strings.Contains(err.Error(), "manifest fix skipped") {
			t.Fatalf("applyErr does not come from the \"manifest fix skipped\" site\napplyErr: %v", err)
		}

		// The mkdir failure must not classify the apply: neither fs.ErrNotExist
		// (which the environment classifier keys on) nor the *fs.PathError.
		s064AssertManifestFailureIsTheCause(t, err, missing, fs.ErrNotExist)
		var pathErr *fs.PathError
		if errors.As(err, &pathErr) {
			t.Errorf("errors.As(applyErr, *fs.PathError) = true (%v), want false: the skipped fix's "+
				"mkdir error is context, not the cause (R2.6)", pathErr)
		}
	})
}
