//go:build unix

package provider

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// Stories 054 and 055 meet in ensureRepo: 055 gave it the lookup's context,
// 054 gave updateRepo a context parameter. The update of an existing clone must
// run under the lookup's context, so a cancelled lookup never starts a git
// command on a live one.
func TestEnsureRepoUpdateUsesTheLookupContext(t *testing.T) {
	dir := t.TempDir()
	// An existing clone with no FETCH_HEAD: needsUpdate reports true.
	if err := os.Mkdir(filepath.Join(dir, ".git"), 0o750); err != nil {
		t.Fatal(err)
	}
	var liveRuns int
	swapExecCommand(t, func(ctx context.Context, _ string, _ ...string) *exec.Cmd {
		if ctx.Err() == nil {
			liveRuns++
		}
		return exec.CommandContext(ctx, "false")
	})
	prov := &GitCloneProvider{LocalPath: dir, RepoURL: "https://example.invalid/r.git", Branch: "master"}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := prov.ensureRepo(ctx)

	if !errors.Is(err, context.Canceled) {
		t.Errorf("ensureRepo(cancelled ctx) error = %v, want one wrapping context.Canceled", err)
	}
	if liveRuns != 0 {
		t.Errorf("ensureRepo ran %d git command(s) on a live context after the lookup was cancelled, want 0", liveRuns)
	}
}
