package autoupdate

import (
	"context"
	"os"
	"os/exec"
	"testing"

	"github.com/obentoo/bentoolkit/internal/autoupdate/fixer"
	"github.com/obentoo/bentoolkit/internal/autoupdate/llm"
)

// newTestFixer constructs a ClaudeCodeFixer with lookPath stubbed to "find"
// claude and the given options applied.
func newTestFixer(t *testing.T, cfg llm.LLMConfig, opts ...fixer.ClaudeCodeFixerOption) *fixer.ClaudeCodeFixer {
	t.Helper()
	stubLookPathFound(t)
	f, err := fixer.NewClaudeCodeFixer(cfg, opts...)
	if err != nil {
		t.Fatalf("NewClaudeCodeFixer: unexpected error: %v", err)
	}
	return f
}

// fakeFixer is a ManifestFixer test double for the applier integration tests.
type fakeFixer struct {
	called  int
	summary string
	err     error
	// onCall, when non-nil, runs on each invocation (e.g. to flip a seam flag or
	// edit the ebuild so the subsequent manifest re-run can succeed).
	onCall  func(req fixer.ManifestFixRequest)
	lastReq fixer.ManifestFixRequest
	// noEdit leaves the ebuild untouched. By default a successful call appends
	// a comment to it, as a real fixer that reports a change edits the file.
	noEdit bool
}

func (f *fakeFixer) FixManifest(_ context.Context, req fixer.ManifestFixRequest) (fixer.ManifestFixResult, error) {
	f.called++
	f.lastReq = req
	if f.onCall != nil {
		f.onCall(req)
	}
	if f.err != nil {
		return fixer.ManifestFixResult{}, f.err
	}
	if !f.noEdit && req.EbuildPath != "" {
		if fh, err := os.OpenFile(req.EbuildPath, os.O_APPEND|os.O_WRONLY, 0); err == nil {
			_, _ = fh.WriteString("# edited by fakeFixer\n")
			_ = fh.Close()
		}
	}
	return fixer.ManifestFixResult{Summary: f.summary}, nil
}

var _ fixer.ManifestFixer = (*fakeFixer)(nil)

// pkgdevFlakySeam returns an exec seam where the first `pkgdev` call fails and
// every subsequent `pkgdev` call succeeds (simulating a manifest that passes once
// the fixer has repaired the ebuild). Non-pkgdev commands always succeed.
func pkgdevFlakySeam() func(ctx context.Context, name string, arg ...string) *exec.Cmd {
	calls := 0
	return func(ctx context.Context, name string, arg ...string) *exec.Cmd {
		if name == "pkgdev" {
			calls++
			if calls == 1 {
				return exec.CommandContext(ctx, "false")
			}
		}
		return exec.CommandContext(ctx, "true")
	}
}
