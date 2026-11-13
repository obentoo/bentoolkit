package validate

import (
	"context"
	"os/exec"
	"strings"
	"testing"
)

// emergeHardBlocked is `emerge -p --quiet` refusing a candidate an installed
// package blocks, as the 2026-11-13 run met with app-editors/zed against the
// installed app-editors/zeo: the block line, then the resolver's refusal, and a
// non-zero exit.
const emergeHardBlocked = "[ebuild  N    ] app-editors/zed-1.21.0\n" +
	"[blocks B      ] app-editors/zeo (\"app-editors/zeo\" is blocking app-editors/zed-1.21.0)\n" +
	"\n * Error: The above package list contains packages which cannot be\n" +
	" * installed at the same time on the same system.\n"

// TestDependenciesSatisfied_AHardBlockerIsUnsatisfiedNotUndetermined: a block
// is Portage's ANSWER — this host cannot build the bump as it stands — and it
// names the package in the way. Read as "could not be asked", the run lost
// that name and reported the bump as undetermined.
func TestDependenciesSatisfied_AHardBlockerIsUnsatisfiedNotUndetermined(t *testing.T) {
	deps := BuildDeps{
		ExecCommand: func(ctx context.Context, _ string, _ ...string) *exec.Cmd {
			return exec.CommandContext(ctx, "sh", "-c", `printf %s "$1"; exit 1`, "sh", emergeHardBlocked)
		},
		LookPath: func(name string) (string, error) { return "/usr/bin/" + name, nil },
	}

	ok, missing, err := DependenciesSatisfied(context.Background(), t.TempDir(), "app-editors/zed", "1.21.0", deps)

	if err != nil {
		t.Fatalf("a blocked resolve was reported as undetermined: %v", err)
	}
	if ok {
		t.Fatal("a blocked resolve was reported as satisfied")
	}
	if joined := strings.Join(missing, " "); !strings.Contains(joined, "app-editors/zeo") {
		t.Errorf("the unsatisfied list %v does not name the blocking package", missing)
	}
}

// TestDependencyAction_RemovesABlockerAndInstallsTheRest: the skip reason is
// the operator's next command, so a blocker must never be listed under
// "install".
func TestDependencyAction_RemovesABlockerAndInstallsTheRest(t *testing.T) {
	got := DependencyAction([]string{"dev-qt/qtbase-6.9.1", "app-editors/zeo" + blockerNote})

	if want := "install dev-qt/qtbase-6.9.1 and remove app-editors/zeo, which blocks it,"; got != want {
		t.Errorf("DependencyAction = %q, want %q", got, want)
	}
	if only := DependencyAction([]string{"dev-qt/qtbase-6.9.1"}); only != "install dev-qt/qtbase-6.9.1" {
		t.Errorf("an install-only list reads %q", only)
	}
}
