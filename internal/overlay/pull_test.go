package overlay

import (
	"errors"
	"strings"
	"testing"

	"github.com/leanovate/gopter"
	"github.com/leanovate/gopter/gen"
	"github.com/leanovate/gopter/prop"
	"github.com/obentoo/bentoolkit/internal/common/git"
)

// behindBy returns a CountRangeFunc reporting the upstream as n commits ahead,
// so a pull has something to integrate. Without it the mock reports 0 and every
// pull short-circuits as up-to-date.
func behindBy(n int) func(from, to string) (int, error) {
	return func(from, to string) (int, error) { return n, nil }
}

// TestPullWithoutConflictsSucceeds tests Property 3: pull without conflicts succeeds
// **Feature: overlay-improvements, Property 3: Sync without conflicts succeeds**
// **Validates: Requirements 6.2**
func TestPullWithoutConflictsSucceeds(t *testing.T) {
	parameters := gopter.DefaultTestParameters()
	parameters.MinSuccessfulTests = 100
	properties := gopter.NewProperties(parameters)

	// Generate random remote names from a set of valid git remote names
	genRemoteName := gen.OneConstOf("origin", "upstream", "fork", "backup", "remote1", "my-remote")

	properties.Property("Pull without conflicts returns Success=true and empty Conflicts", prop.ForAll(
		func(remote string) bool {
			// Create mock that simulates successful fetch and merge (no conflicts)
			mock := &git.MockGitRunner{
				FetchFunc: func(r string) error {
					return nil // Fetch succeeds
				},
				CountRangeFunc: behindBy(2),
				MergeFFOnlyFunc: func(branch string) error {
					return nil // Fast-forward succeeds without conflicts
				},
			}

			result, err := PullWithRunner(mock, remote, PullFFOnly, false)

			// Property: No error should be returned
			if err != nil {
				t.Logf("Expected no error, got: %v", err)
				return false
			}

			// Property: Success should be true
			if !result.Success {
				t.Logf("Expected Success=true, got false")
				return false
			}

			// Property: Conflicts slice should be empty
			if len(result.Conflicts) != 0 {
				t.Logf("Expected empty Conflicts, got: %v", result.Conflicts)
				return false
			}

			return true
		},
		genRemoteName,
	))

	properties.TestingRun(t)
}

// TestPullWithConflictsReportsThem tests Property 4: pull with conflicts reports them
// **Feature: overlay-improvements, Property 4: Sync with conflicts reports them**
// **Validates: Requirements 6.3**
func TestPullWithConflictsReportsThem(t *testing.T) {
	parameters := gopter.DefaultTestParameters()
	parameters.MinSuccessfulTests = 100
	properties := gopter.NewProperties(parameters)

	// Generate random file paths for conflicts using alphanumeric strings
	genFilePath := gen.AlphaString().Map(func(s string) string {
		if s == "" {
			return "file.txt"
		}
		return s + ".ebuild"
	})

	// Generate a list of 1-5 conflicting files
	genConflictFiles := gen.SliceOfN(5, genFilePath).Map(func(files []string) []string {
		// Ensure at least one file
		if len(files) == 0 {
			return []string{"conflict.txt"}
		}
		return files
	})

	properties.Property("Pull with conflicts returns Success=false and reports all conflicts", prop.ForAll(
		func(conflictFiles []string) bool {
			// Build a conflict error message like git would produce
			var errParts []string
			for _, file := range conflictFiles {
				errParts = append(errParts, "CONFLICT (content): Merge conflict in "+file)
			}
			errParts = append(errParts, "Automatic merge failed; fix conflicts and then commit the result.")
			conflictErr := errors.New(strings.Join(errParts, "\n"))

			// Create mock that simulates merge with conflicts
			mock := &git.MockGitRunner{
				FetchFunc: func(r string) error {
					return nil // Fetch succeeds
				},
				CountRangeFunc: behindBy(1),
				MergeFunc: func(branch string) error {
					return conflictErr // Merge fails with conflicts
				},
			}

			result, err := PullWithRunner(mock, "origin", PullMerge, false)

			// Property: No error should be returned (conflicts are reported in result)
			if err != nil {
				t.Logf("Expected no error, got: %v", err)
				return false
			}

			// Property: Success should be false
			if result.Success {
				t.Logf("Expected Success=false, got true")
				return false
			}

			// Property: Conflicts slice should contain all conflicting files
			if len(result.Conflicts) != len(conflictFiles) {
				t.Logf("Expected %d conflicts, got %d: %v", len(conflictFiles), len(result.Conflicts), result.Conflicts)
				return false
			}

			// Verify each conflict file is reported
			for _, expected := range conflictFiles {
				found := false
				for _, actual := range result.Conflicts {
					if actual == expected {
						found = true
						break
					}
				}
				if !found {
					t.Logf("Expected conflict file %q not found in result: %v", expected, result.Conflicts)
					return false
				}
			}

			return true
		},
		genConflictFiles,
	))

	properties.TestingRun(t)
}

// TestPullFetchError tests that fetch errors are propagated
// _Requirements: 6.1_
func TestPullFetchError(t *testing.T) {
	fetchErr := errors.New("network error: could not reach remote")

	mock := &git.MockGitRunner{
		FetchFunc: func(r string) error {
			return fetchErr
		},
	}

	_, err := PullWithRunner(mock, "origin", PullFFOnly, false)
	if err == nil {
		t.Error("Expected error when fetch fails")
	}
	if !strings.Contains(err.Error(), "network error") {
		t.Errorf("Expected fetch error to be propagated, got: %v", err)
	}
}

// TestPullNoRemote tests that empty remote returns error
// _Requirements: 6.1_
func TestPullNoRemote(t *testing.T) {
	mock := &git.MockGitRunner{}

	_, err := PullWithRunner(mock, "", PullFFOnly, false)
	if err == nil {
		t.Error("Expected error when remote is empty")
	}
	if !errors.Is(err, ErrNoRemote) {
		t.Errorf("Expected ErrNoRemote, got: %v", err)
	}
}

// TestPullMergeNonConflictError tests that non-conflict merge errors are propagated
// _Requirements: 6.2_
func TestPullMergeNonConflictError(t *testing.T) {
	mergeErr := errors.New("fatal: not a git repository")

	mock := &git.MockGitRunner{
		FetchFunc: func(r string) error {
			return nil
		},
		CountRangeFunc: behindBy(1),
		MergeFFOnlyFunc: func(branch string) error {
			return mergeErr
		},
	}

	_, err := PullWithRunner(mock, "origin", PullFFOnly, false)
	if err == nil {
		t.Error("Expected error when merge fails with non-conflict error")
	}
	if !strings.Contains(err.Error(), "not a git repository") {
		t.Errorf("Expected merge error to be propagated, got: %v", err)
	}
}

// TestPullTargetsTheCheckedOutBranchUpstream is the regression guard for the
// behaviour this command replaced: merging the remote's default branch
// (origin/HEAD) into whatever was checked out. The ref handed to the
// integration step must be the current branch's own upstream.
func TestPullTargetsTheCheckedOutBranchUpstream(t *testing.T) {
	var merged string

	mock := &git.MockGitRunner{
		CurrentBranchFunc: func() (string, error) { return "wip", nil },
		UpstreamFunc:      func() (string, error) { return "origin/wip", nil },
		FetchFunc:         func(r string) error { return nil },
		CountRangeFunc:    behindBy(3),
		MergeFFOnlyFunc: func(branch string) error {
			merged = branch
			return nil
		},
	}

	result, err := PullWithRunner(mock, "origin", PullFFOnly, false)
	if err != nil {
		t.Fatalf("PullWithRunner() error = %v", err)
	}
	if merged != "origin/wip" {
		t.Errorf("integrated %q, want the branch's own upstream %q", merged, "origin/wip")
	}
	if merged == "origin/HEAD" {
		t.Error("pull merged the remote's default branch instead of the branch upstream")
	}
	if result.Branch != "wip" || result.Upstream != "origin/wip" {
		t.Errorf("result reported branch=%q upstream=%q, want wip/origin/wip", result.Branch, result.Upstream)
	}
}

// TestPullWithoutUpstreamRefuses proves the pull stops with an actionable error
// instead of falling back to the remote's default branch.
func TestPullWithoutUpstreamRefuses(t *testing.T) {
	integrated := false

	mock := &git.MockGitRunner{
		CurrentBranchFunc: func() (string, error) { return "wip", nil },
		UpstreamFunc:      func() (string, error) { return "", git.ErrNoUpstream },
		MergeFFOnlyFunc: func(branch string) error {
			integrated = true
			return nil
		},
	}

	_, err := PullWithRunner(mock, "origin", PullFFOnly, false)
	if err == nil {
		t.Fatal("Expected an error when the branch has no upstream")
	}
	if !errors.Is(err, git.ErrNoUpstream) {
		t.Errorf("Expected ErrNoUpstream in the chain, got: %v", err)
	}
	if !strings.Contains(err.Error(), "wip") {
		t.Errorf("Expected the error to name the branch, got: %v", err)
	}
	if integrated {
		t.Error("pull integrated despite having no upstream")
	}
}

// TestPullDetachedHeadRefuses covers the other branchless state.
func TestPullDetachedHeadRefuses(t *testing.T) {
	mock := &git.MockGitRunner{
		CurrentBranchFunc: func() (string, error) { return "", git.ErrDetachedHead },
	}

	_, err := PullWithRunner(mock, "origin", PullFFOnly, false)
	if !errors.Is(err, git.ErrDetachedHead) {
		t.Errorf("Expected ErrDetachedHead, got: %v", err)
	}
}

// TestPullUpToDateReportsDistinctly proves an up-to-date pull is
// distinguishable from one that integrated commits — the defect that made the
// old command's output uninformative.
func TestPullUpToDateReportsDistinctly(t *testing.T) {
	integrated := false

	mock := &git.MockGitRunner{
		FetchFunc:      func(r string) error { return nil },
		CountRangeFunc: behindBy(0),
		MergeFFOnlyFunc: func(branch string) error {
			integrated = true
			return nil
		},
	}

	result, err := PullWithRunner(mock, "origin", PullFFOnly, false)
	if err != nil {
		t.Fatalf("PullWithRunner() error = %v", err)
	}
	if !result.UpToDate {
		t.Error("Expected UpToDate=true when the upstream is 0 commits ahead")
	}
	if result.CommitsPulled != 0 {
		t.Errorf("Expected CommitsPulled=0, got %d", result.CommitsPulled)
	}
	if integrated {
		t.Error("pull ran an integration step with nothing to integrate")
	}
	if !strings.Contains(result.Message, "up to date") {
		t.Errorf("Expected an up-to-date message, got: %q", result.Message)
	}
}

// TestPullReportsCommitCount proves CommitsPulled carries the real number
// rather than staying at the zero value, as the old SyncResult field did.
func TestPullReportsCommitCount(t *testing.T) {
	mock := &git.MockGitRunner{
		FetchFunc:       func(r string) error { return nil },
		CountRangeFunc:  behindBy(7),
		MergeFFOnlyFunc: func(branch string) error { return nil },
	}

	result, err := PullWithRunner(mock, "origin", PullFFOnly, false)
	if err != nil {
		t.Fatalf("PullWithRunner() error = %v", err)
	}
	if result.CommitsPulled != 7 {
		t.Errorf("CommitsPulled = %d, want 7", result.CommitsPulled)
	}
	if !strings.Contains(result.Message, "7 commits") {
		t.Errorf("Expected the message to state the count, got: %q", result.Message)
	}
}

// TestPullCountsAfterFetch guards the ordering: counting before the fetch would
// measure a stale remote-tracking ref.
func TestPullCountsAfterFetch(t *testing.T) {
	fetched := false
	countedAfterFetch := false

	mock := &git.MockGitRunner{
		FetchFunc: func(r string) error {
			fetched = true
			return nil
		},
		CountRangeFunc: func(from, to string) (int, error) {
			countedAfterFetch = fetched
			return 1, nil
		},
		MergeFFOnlyFunc: func(branch string) error { return nil },
	}

	if _, err := PullWithRunner(mock, "origin", PullFFOnly, false); err != nil {
		t.Fatalf("PullWithRunner() error = %v", err)
	}
	if !countedAfterFetch {
		t.Error("commits were counted before the fetch, against a stale ref")
	}
}

// TestPullDirtyWorktreeRefuses proves uncommitted work blocks the integration
// with the toolkit's own error rather than raw git output.
func TestPullDirtyWorktreeRefuses(t *testing.T) {
	integrated := false

	mock := &git.MockGitRunner{
		FetchFunc:      func(r string) error { return nil },
		CountRangeFunc: behindBy(1),
		StatusFunc: func() ([]git.StatusEntry, error) {
			return []git.StatusEntry{{Status: "M", FilePath: "app-misc/hello/hello-1.ebuild"}}, nil
		},
		MergeFFOnlyFunc: func(branch string) error {
			integrated = true
			return nil
		},
	}

	_, err := PullWithRunner(mock, "origin", PullFFOnly, false)
	if !errors.Is(err, ErrDirtyWorktree) {
		t.Errorf("Expected ErrDirtyWorktree, got: %v", err)
	}
	if integrated {
		t.Error("pull integrated over a dirty worktree")
	}
}

// TestPullUntrackedFilesDoNotBlock: git only refuses when a tracked path is in
// the way, so an overlay carrying untracked files still pulls.
func TestPullUntrackedFilesDoNotBlock(t *testing.T) {
	mock := &git.MockGitRunner{
		FetchFunc:      func(r string) error { return nil },
		CountRangeFunc: behindBy(1),
		StatusFunc: func() ([]git.StatusEntry, error) {
			return []git.StatusEntry{{Status: "??", FilePath: "scratch.txt"}}, nil
		},
		MergeFFOnlyFunc: func(branch string) error { return nil },
	}

	result, err := PullWithRunner(mock, "origin", PullFFOnly, false)
	if err != nil {
		t.Fatalf("PullWithRunner() error = %v", err)
	}
	if !result.Success {
		t.Error("Expected an untracked-only worktree to pull successfully")
	}
}

// TestPullUpToDateIgnoresDirtyWorktree: with nothing to integrate there is
// nothing for local work to collide with, so the pull must not complain.
func TestPullUpToDateIgnoresDirtyWorktree(t *testing.T) {
	mock := &git.MockGitRunner{
		FetchFunc:      func(r string) error { return nil },
		CountRangeFunc: behindBy(0),
		StatusFunc: func() ([]git.StatusEntry, error) {
			return []git.StatusEntry{{Status: "M", FilePath: "work-in-progress.ebuild"}}, nil
		},
	}

	result, err := PullWithRunner(mock, "origin", PullFFOnly, false)
	if err != nil {
		t.Fatalf("Expected no error with nothing to integrate, got: %v", err)
	}
	if !result.UpToDate {
		t.Error("Expected UpToDate=true")
	}
}

// TestPullDryRunDoesNotIntegrate proves --dry-run reports without writing.
func TestPullDryRunDoesNotIntegrate(t *testing.T) {
	integrated := false

	mock := &git.MockGitRunner{
		FetchFunc:      func(r string) error { return nil },
		CountRangeFunc: behindBy(4),
		MergeFFOnlyFunc: func(branch string) error {
			integrated = true
			return nil
		},
		MergeFunc:  func(branch string) error { integrated = true; return nil },
		RebaseFunc: func(branch string) error { integrated = true; return nil },
	}

	result, err := PullWithRunner(mock, "origin", PullFFOnly, true)
	if err != nil {
		t.Fatalf("PullWithRunner() error = %v", err)
	}
	if integrated {
		t.Error("dry-run integrated commits")
	}
	if result.CommitsPulled != 4 {
		t.Errorf("CommitsPulled = %d, want 4", result.CommitsPulled)
	}
	if !strings.Contains(result.Message, "Dry-run") {
		t.Errorf("Expected a dry-run message, got: %q", result.Message)
	}
}

// TestPullModeSelectsIntegration proves each mode reaches its own git verb —
// and, for the default, that it is the fast-forward-only one.
func TestPullModeSelectsIntegration(t *testing.T) {
	tests := []struct {
		name string
		mode PullMode
		want string
	}{
		{"default is fast-forward only", PullFFOnly, "ff-only"},
		{"merge mode merges", PullMerge, "merge"},
		{"rebase mode rebases", PullRebase, "rebase"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var called string
			mock := &git.MockGitRunner{
				FetchFunc:       func(r string) error { return nil },
				CountRangeFunc:  behindBy(1),
				MergeFFOnlyFunc: func(branch string) error { called = "ff-only"; return nil },
				MergeFunc:       func(branch string) error { called = "merge"; return nil },
				RebaseFunc:      func(branch string) error { called = "rebase"; return nil },
			}

			if _, err := PullWithRunner(mock, "origin", tc.mode, false); err != nil {
				t.Fatalf("PullWithRunner() error = %v", err)
			}
			if called != tc.want {
				t.Errorf("mode %v ran %q, want %q", tc.mode, called, tc.want)
			}
		})
	}
}

// TestPullRebaseConflictReported proves a rebase conflict is classified as a
// conflict, not propagated as an opaque error: git phrases it differently
// ("could not apply") than a merge conflict.
func TestPullRebaseConflictReported(t *testing.T) {
	rebaseErr := errors.New("error: could not apply 9a76df9... local work\n" +
		"CONFLICT (content): Merge conflict in app-misc/hello/hello-1.ebuild")

	mock := &git.MockGitRunner{
		FetchFunc:      func(r string) error { return nil },
		CountRangeFunc: behindBy(2),
		RebaseFunc:     func(branch string) error { return rebaseErr },
	}

	result, err := PullWithRunner(mock, "origin", PullRebase, false)
	if err != nil {
		t.Fatalf("Expected the conflict in the result, not as an error: %v", err)
	}
	if result.Success {
		t.Error("Expected Success=false on a rebase conflict")
	}
	if len(result.Conflicts) != 1 || result.Conflicts[0] != "app-misc/hello/hello-1.ebuild" {
		t.Errorf("Expected the conflicting ebuild to be reported, got: %v", result.Conflicts)
	}
}

// TestPullFFOnlyRefusesDivergence is the second half of the regression guard:
// on diverged history the default mode surfaces git's refusal instead of
// writing a merge commit.
func TestPullFFOnlyRefusesDivergence(t *testing.T) {
	divergeErr := errors.New("fatal: Not possible to fast-forward, aborting.")

	mock := &git.MockGitRunner{
		FetchFunc:       func(r string) error { return nil },
		CountRangeFunc:  behindBy(2),
		MergeFFOnlyFunc: func(branch string) error { return divergeErr },
	}

	_, err := PullWithRunner(mock, "origin", PullFFOnly, false)
	if err == nil {
		t.Fatal("Expected the fast-forward refusal to reach the caller")
	}
	if !strings.Contains(err.Error(), "fast-forward") {
		t.Errorf("Expected git's refusal to be propagated, got: %v", err)
	}
}

// TestPullUntrackedOverwriteIsTranslated: the preventive dirty check lets
// untracked files through, so git is the one that refuses when it is about to
// overwrite one. That refusal must reach the operator as a toolkit error
// naming the file, not as raw git output.
func TestPullUntrackedOverwriteIsTranslated(t *testing.T) {
	gitRefusal := errors.New("git command failed\n" +
		"Updating 98ffc53..9bdeaae\n\n" +
		"error: The following untracked working tree files would be overwritten by merge:\n" +
		"\tprofiles/categories\n" +
		"Please move or remove them before you merge.\n" +
		"Aborting")

	mock := &git.MockGitRunner{
		FetchFunc:      func(r string) error { return nil },
		CountRangeFunc: behindBy(1),
		StatusFunc: func() ([]git.StatusEntry, error) {
			// Untracked only: the preventive check does not stop this pull.
			return []git.StatusEntry{{Status: "??", FilePath: "profiles/categories"}}, nil
		},
		MergeFFOnlyFunc: func(branch string) error { return gitRefusal },
	}

	_, err := PullWithRunner(mock, "origin", PullFFOnly, false)
	if !errors.Is(err, ErrDirtyWorktree) {
		t.Fatalf("Expected ErrDirtyWorktree, got: %v", err)
	}
	if !strings.Contains(err.Error(), "profiles/categories") {
		t.Errorf("Expected the error to name the blocking file, got: %v", err)
	}
	if strings.Contains(err.Error(), "Aborting") {
		t.Errorf("Raw git output leaked into the error: %v", err)
	}
}

// TestParseOverwriteFiles covers the list parser directly, including the
// multi-file case and the boundary where git's indented run ends.
func TestParseOverwriteFiles(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want []string
	}{
		{
			name: "single file",
			in:   "error: The following untracked working tree files would be overwritten by merge:\n\tprofiles/categories\nPlease move or remove them before you merge.",
			want: []string{"profiles/categories"},
		},
		{
			name: "several files",
			in:   "error: Your local changes to the following files would be overwritten by merge:\n\ta/one.ebuild\n\tb/two.ebuild\nPlease commit your changes or stash them before you merge.",
			want: []string{"a/one.ebuild", "b/two.ebuild"},
		},
		{
			name: "unrelated error yields nothing",
			in:   "fatal: not a git repository",
			want: nil,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := parseOverwriteFiles(tc.in)
			if len(got) != len(tc.want) {
				t.Fatalf("parseOverwriteFiles() = %v, want %v", got, tc.want)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Errorf("file %d = %q, want %q", i, got[i], tc.want[i])
				}
			}
		})
	}
}

// TestPullModeString keeps the mode names stable: they appear in dry-run output.
func TestPullModeString(t *testing.T) {
	tests := map[PullMode]string{
		PullFFOnly: "fast-forward",
		PullMerge:  "merge",
		PullRebase: "rebase",
	}
	for mode, want := range tests {
		if got := mode.String(); got != want {
			t.Errorf("PullMode(%d).String() = %q, want %q", mode, got, want)
		}
	}
}
