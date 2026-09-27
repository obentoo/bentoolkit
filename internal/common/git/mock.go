package git

import "context"

// MockGitRunner implements GitExecutor for testing.
// Each method can be configured with a custom function to control behavior.
// Each method hands the context it received to that function unchanged, so a
// test sees the very context the code under test passed, and can stand in for
// a git child that never answers by blocking on ctx.Done().
type MockGitRunner struct {
	StatusFunc        func(ctx context.Context) ([]StatusEntry, error)
	StagedStatusFunc  func(ctx context.Context) ([]StatusEntry, error)
	AddFunc           func(ctx context.Context, paths ...string) error
	CommitFunc        func(ctx context.Context, message, user, email string) error
	PushFunc          func(ctx context.Context) error
	PushDryRunFunc    func(ctx context.Context) (string, error)
	FetchFunc         func(ctx context.Context, remote string) error
	MergeFunc         func(ctx context.Context, branch string) error
	MergeFFOnlyFunc   func(ctx context.Context, branch string) error
	RebaseFunc        func(ctx context.Context, branch string) error
	CurrentBranchFunc func(ctx context.Context) (string, error)
	UpstreamFunc      func(ctx context.Context) (string, error)
	CountRangeFunc    func(ctx context.Context, from, to string) (int, error)
	workDir           string
}

// NewMockGitRunner creates a new MockGitRunner with the specified working directory
func NewMockGitRunner(workDir string) *MockGitRunner {
	return &MockGitRunner{
		workDir: workDir,
	}
}

// Status returns the current git status as a list of StatusEntry
func (m *MockGitRunner) Status(ctx context.Context) ([]StatusEntry, error) {
	if m.StatusFunc != nil {
		return m.StatusFunc(ctx)
	}
	return nil, nil
}

// StagedStatus returns only the entries staged in the index
func (m *MockGitRunner) StagedStatus(ctx context.Context) ([]StatusEntry, error) {
	if m.StagedStatusFunc != nil {
		return m.StagedStatusFunc(ctx)
	}
	return nil, nil
}

// Add stages files for commit
func (m *MockGitRunner) Add(ctx context.Context, paths ...string) error {
	if m.AddFunc != nil {
		return m.AddFunc(ctx, paths...)
	}
	return nil
}

// Commit creates a git commit with the specified message and author
func (m *MockGitRunner) Commit(ctx context.Context, message, user, email string) error {
	if m.CommitFunc != nil {
		return m.CommitFunc(ctx, message, user, email)
	}
	return nil
}

// Push pushes commits to the remote repository
func (m *MockGitRunner) Push(ctx context.Context) error {
	if m.PushFunc != nil {
		return m.PushFunc(ctx)
	}
	return nil
}

// PushDryRun shows what would be pushed without actually pushing
func (m *MockGitRunner) PushDryRun(ctx context.Context) (string, error) {
	if m.PushDryRunFunc != nil {
		return m.PushDryRunFunc(ctx)
	}
	return "", nil
}

// Fetch fetches changes from a remote repository
func (m *MockGitRunner) Fetch(ctx context.Context, remote string) error {
	if m.FetchFunc != nil {
		return m.FetchFunc(ctx, remote)
	}
	return nil
}

// Merge merges a branch into the current branch
func (m *MockGitRunner) Merge(ctx context.Context, branch string) error {
	if m.MergeFunc != nil {
		return m.MergeFunc(ctx, branch)
	}
	return nil
}

// MergeFFOnly merges a branch only if it can fast-forward
func (m *MockGitRunner) MergeFFOnly(ctx context.Context, branch string) error {
	if m.MergeFFOnlyFunc != nil {
		return m.MergeFFOnlyFunc(ctx, branch)
	}
	return nil
}

// Rebase replays the current branch on top of the given branch
func (m *MockGitRunner) Rebase(ctx context.Context, branch string) error {
	if m.RebaseFunc != nil {
		return m.RebaseFunc(ctx, branch)
	}
	return nil
}

// CurrentBranch returns the checked-out branch name
func (m *MockGitRunner) CurrentBranch(ctx context.Context) (string, error) {
	if m.CurrentBranchFunc != nil {
		return m.CurrentBranchFunc(ctx)
	}
	return "master", nil
}

// Upstream returns the current branch's upstream ref
func (m *MockGitRunner) Upstream(ctx context.Context) (string, error) {
	if m.UpstreamFunc != nil {
		return m.UpstreamFunc(ctx)
	}
	return "origin/master", nil
}

// CountRange returns the number of commits in the range from..to
func (m *MockGitRunner) CountRange(ctx context.Context, from, to string) (int, error) {
	if m.CountRangeFunc != nil {
		return m.CountRangeFunc(ctx, from, to)
	}
	return 0, nil
}

// WorkDir returns the working directory of the git repository
func (m *MockGitRunner) WorkDir() string {
	return m.workDir
}

// Ensure MockGitRunner implements GitExecutor interface
var _ GitExecutor = (*MockGitRunner)(nil)
