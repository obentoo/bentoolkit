package overlay

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/obentoo/bentoolkit/internal/common/git"
)

// Story 054, S054-R5.1. git_ctx_test.go proves a cancel reaches the FIRST git
// call of PushWithExecutor and PullWithRunner, and pull stops there: its first
// call is CurrentBranch. These two pin every other call of the thirteen
// functions, pull's Fetch included — the network call a stalled remote hangs.

// callerMark tags the caller's context. A git call made under
// context.Background() instead would not carry it, and would not stop on the
// caller's cancel either.
type callerMark struct{}

func TestExecutorFunctionsPassTheCallersContext(t *testing.T) {
	ctx := context.WithValue(context.Background(), callerMark{}, true)
	reached := map[string]bool{}
	check := func(op string, got context.Context) {
		reached[op] = true
		if got.Value(callerMark{}) != true {
			t.Errorf("git %s ran under a context that is not the caller's", op)
		}
	}
	mock := &git.MockGitRunner{
		StatusFunc: func(c context.Context) ([]git.StatusEntry, error) {
			check("Status", c)
			return nil, nil
		},
		StagedStatusFunc: func(c context.Context) ([]git.StatusEntry, error) {
			check("StagedStatus", c)
			return nil, nil
		},
		CommitFunc:      func(c context.Context, _, _, _ string) error { check("Commit", c); return nil },
		PushFunc:        func(c context.Context) error { check("Push", c); return nil },
		FetchFunc:       func(c context.Context, _ string) error { check("Fetch", c); return nil },
		MergeFunc:       func(c context.Context, _ string) error { check("Merge", c); return nil },
		MergeFFOnlyFunc: func(c context.Context, _ string) error { check("MergeFFOnly", c); return nil },
		RebaseFunc:      func(c context.Context, _ string) error { check("Rebase", c); return nil },
		CurrentBranchFunc: func(c context.Context) (string, error) {
			check("CurrentBranch", c)
			return "main", nil
		},
		UpstreamFunc: func(c context.Context) (string, error) {
			check("Upstream", c)
			return "origin/main", nil
		},
		CountRangeFunc: func(c context.Context, _, _ string) (int, error) {
			check("CountRange", c)
			return 1, nil // behind, so pull goes on to its dirty check and integrates
		},
	}

	_, cfg, cleanup := setupTestOverlay(t)
	defer cleanup()
	if _, err := PushWithExecutor(ctx, mock); err != nil {
		t.Fatalf("PushWithExecutor: %v", err)
	}
	if err := CommitWithExecutor(ctx, cfg, "msg", mock); err != nil {
		t.Fatalf("CommitWithExecutor: %v", err)
	}
	if _, err := StatusWithExecutor(ctx, mock); err != nil {
		t.Fatalf("StatusWithExecutor: %v", err)
	}
	if _, err := StagedStatusWithExecutor(ctx, mock); err != nil {
		t.Fatalf("StagedStatusWithExecutor: %v", err)
	}
	for _, mode := range []PullMode{PullFFOnly, PullMerge, PullRebase} {
		if _, err := PullWithRunner(ctx, mock, "origin", mode, false); err != nil {
			t.Fatalf("PullWithRunner(%s): %v", mode, err)
		}
	}

	for _, op := range []string{"Push", "Commit", "Status", "StagedStatus", "CurrentBranch",
		"Upstream", "Fetch", "CountRange", "MergeFFOnly", "Merge", "Rebase"} {
		if !reached[op] {
			t.Errorf("git %s was never reached, so nothing above checked its context", op)
		}
	}
}

// TestConfigFunctionsPassTheirContext hands the functions that build their own
// GitRunner a context that is already cancelled. git never starts, so each must
// report context.Canceled; one that ran git under a context of its own would
// report git's answer instead.
func TestConfigFunctionsPassTheirContext(t *testing.T) {
	tmpDir, cfg, cleanup := setupTestOverlay(t)
	defer cleanup()
	if err := os.WriteFile(filepath.Join(tmpDir, "file.txt"), []byte("x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	firstAddError := func(result *AddResult, err error) error {
		if err != nil || len(result.Errors) == 0 {
			return err
		}
		return result.Errors[0]
	}
	calls := []struct {
		name string
		call func() error
	}{
		{"Push", func() error { _, err := Push(ctx, cfg); return err }},
		{"PushDryRun", func() error { _, err := PushDryRun(ctx, cfg); return err }},
		{"Pull", func() error { _, err := Pull(ctx, cfg, PullFFOnly, false); return err }},
		{"Commit", func() error { return Commit(ctx, cfg, "msg") }},
		{"GetStagedChanges", func() error { _, err := GetStagedChanges(ctx, cfg); return err }},
		{"Status", func() error { _, err := Status(ctx, cfg); return err }},
		{"StagedStatus", func() error { _, err := StagedStatus(ctx, cfg); return err }},
		{"AddFiles()", func() error { return firstAddError(AddFiles(ctx, cfg)) }},
		{"AddFiles(file.txt)", func() error { return firstAddError(AddFiles(ctx, cfg, "file.txt")) }},
	}
	for _, c := range calls {
		if err := c.call(); !errors.Is(err, context.Canceled) {
			t.Errorf("%s under a cancelled context: error = %v, want one wrapping context.Canceled", c.name, err)
		}
	}
}

// A cancel stops AddFiles at the path it reached: the paths after it are
// reported once as not attempted, not failed one by one.
func TestAddFilesStopsAtTheCancel(t *testing.T) {
	_, cfg, cleanup := setupTestOverlay(t)
	defer cleanup()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	result, err := AddFiles(ctx, cfg, "a", "b", "c", "d", "e")
	if err != nil {
		t.Fatalf("AddFiles: %v", err)
	}
	if len(result.Errors) != 1 || !errors.Is(result.Errors[0], context.Canceled) {
		t.Fatalf("AddFiles under a cancelled context reported %d error(s) %v, want one wrapping context.Canceled", len(result.Errors), result.Errors)
	}
	if msg := result.Errors[0].Error(); !strings.Contains(msg, "a") || !strings.Contains(msg, "5 path(s)") {
		t.Errorf("AddFiles cancel error = %q, want it to name the first path and the 5 path(s) not added", msg)
	}
	if len(result.Added) != 0 {
		t.Errorf("AddFiles under a cancelled context added %v", result.Added)
	}
}
