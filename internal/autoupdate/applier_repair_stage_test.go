package autoupdate

import (
	"context"
	"errors"
	"slices"
	"testing"

	"github.com/obentoo/bentoolkit/internal/autoupdate/fixer"
)

// stageFixer is a manifest fixer that returns a fixed result and counts calls.
type stageFixer struct {
	calls int
	res   fixer.ManifestFixResult
	err   error
}

func (f *stageFixer) FixManifest(_ context.Context, _ fixer.ManifestFixRequest) (fixer.ManifestFixResult, error) {
	f.calls++
	return f.res, f.err
}

// failedStagedApply runs a bump whose every child process fails, so the
// manifest step fails and the staged tree is kept.
func failedStagedApply(t *testing.T, opts ...ApplierOption) *ApplyResult {
	t.Helper()
	opts = append([]ApplierOption{WithApplierDistdir(t.TempDir(), "")}, opts...)
	a, _, pkg, _, _ := promoteFixture(t, opts...)
	result, err := a.Apply(t.Context(), pkg, false)
	if err == nil || result == nil || result.Success {
		t.Fatalf("fixture: the bump did not fail (err %v)", err)
	}
	if result.StagedPath == "" {
		t.Fatalf("fixture: the failed bump kept no staged tree")
	}
	return result
}

// TestApplyFailedStageIsManifestWithoutFixer: no fixer, the manifest step is
// the stage that failed.
func TestApplyFailedStageIsManifestWithoutFixer(t *testing.T) {
	result := failedStagedApply(t)
	if result.FailedStage != "manifest" {
		t.Fatalf("FailedStage = %q, want %q", result.FailedStage, "manifest")
	}
	if len(result.RefusedTools) != 0 {
		t.Fatalf("RefusedTools = %q with no agent run, want none", result.RefusedTools)
	}
}

// TestApplyFailedStageIsLLMFixWhenTheFixerErrors: the fixer itself failed.
func TestApplyFailedStageIsLLMFixWhenTheFixerErrors(t *testing.T) {
	f := &stageFixer{err: errors.New("agent exited 1")}
	result := failedStagedApply(t, WithApplierFixer(f))
	if f.calls != 1 {
		t.Fatalf("fixture: fixer called %d times, want 1", f.calls)
	}
	if result.FailedStage != "llm-fix" {
		t.Fatalf("FailedStage = %q, want %q", result.FailedStage, "llm-fix")
	}
}

// TestApplyFailedStageIsReCheckAfterAFixThatDidNotHold: the fix ran, bentoo's
// own manifest re-check failed.
func TestApplyFailedStageIsReCheckAfterAFixThatDidNotHold(t *testing.T) {
	f := &stageFixer{res: fixer.ManifestFixResult{Summary: "tried"}}
	result := failedStagedApply(t, WithApplierFixer(f))
	if result.FailedStage != "re-check" {
		t.Fatalf("FailedStage = %q, want %q", result.FailedStage, "re-check")
	}
}

// TestApplyRefusedToolsCarriesTheFixerDenialsDeduplicated: repeated labels
// collapse, near-identical labels (two WebFetch hosts) stay apart, order kept.
func TestApplyRefusedToolsCarriesTheFixerDenialsDeduplicated(t *testing.T) {
	f := &stageFixer{res: fixer.ManifestFixResult{
		Summary:     "tried",
		DeniedTools: []string{"Bash", "WebFetch(a.example.org)", "WebFetch(b.example.org)", "Bash", "WebFetch(a.example.org)"},
	}}
	result := failedStagedApply(t, WithApplierFixer(f))
	want := []string{"Bash", "WebFetch(a.example.org)", "WebFetch(b.example.org)"}
	if !slices.Equal(result.RefusedTools, want) {
		t.Fatalf("RefusedTools = %q, want %q", result.RefusedTools, want)
	}
}
