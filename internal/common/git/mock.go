package git

// MockGitRunner implements GitExecutor for testing.
// Each method can be configured with a custom function to control behavior.
type MockGitRunner struct {
	StatusFunc        func() ([]StatusEntry, error)
	StagedStatusFunc  func() ([]StatusEntry, error)
	AddFunc           func(paths ...string) error
	CommitFunc        func(message, user, email string) error
	PushFunc          func() error
	PushDryRunFunc    func() (string, error)
	FetchFunc         func(remote string) error
	MergeFunc         func(branch string) error
	MergeFFOnlyFunc   func(branch string) error
	RebaseFunc        func(branch string) error
	CurrentBranchFunc func() (string, error)
	UpstreamFunc      func() (string, error)
	CountRangeFunc    func(from, to string) (int, error)
	workDir           string
}

// NewMockGitRunner creates a new MockGitRunner with the specified working directory
func NewMockGitRunner(workDir string) *MockGitRunner {
	return &MockGitRunner{
		workDir: workDir,
	}
}

// Status returns the current git status as a list of StatusEntry
func (m *MockGitRunner) Status() ([]StatusEntry, error) {
	if m.StatusFunc != nil {
		return m.StatusFunc()
	}
	return nil, nil
}

// StagedStatus returns only the entries staged in the index
func (m *MockGitRunner) StagedStatus() ([]StatusEntry, error) {
	if m.StagedStatusFunc != nil {
		return m.StagedStatusFunc()
	}
	return nil, nil
}

// Add stages files for commit
func (m *MockGitRunner) Add(paths ...string) error {
	if m.AddFunc != nil {
		return m.AddFunc(paths...)
	}
	return nil
}

// Commit creates a git commit with the specified message and author
func (m *MockGitRunner) Commit(message, user, email string) error {
	if m.CommitFunc != nil {
		return m.CommitFunc(message, user, email)
	}
	return nil
}

// Push pushes commits to the remote repository
func (m *MockGitRunner) Push() error {
	if m.PushFunc != nil {
		return m.PushFunc()
	}
	return nil
}

// PushDryRun shows what would be pushed without actually pushing
func (m *MockGitRunner) PushDryRun() (string, error) {
	if m.PushDryRunFunc != nil {
		return m.PushDryRunFunc()
	}
	return "", nil
}

// Fetch fetches changes from a remote repository
func (m *MockGitRunner) Fetch(remote string) error {
	if m.FetchFunc != nil {
		return m.FetchFunc(remote)
	}
	return nil
}

// Merge merges a branch into the current branch
func (m *MockGitRunner) Merge(branch string) error {
	if m.MergeFunc != nil {
		return m.MergeFunc(branch)
	}
	return nil
}

// MergeFFOnly merges a branch only if it can fast-forward
func (m *MockGitRunner) MergeFFOnly(branch string) error {
	if m.MergeFFOnlyFunc != nil {
		return m.MergeFFOnlyFunc(branch)
	}
	return nil
}

// Rebase replays the current branch on top of the given branch
func (m *MockGitRunner) Rebase(branch string) error {
	if m.RebaseFunc != nil {
		return m.RebaseFunc(branch)
	}
	return nil
}

// CurrentBranch returns the checked-out branch name
func (m *MockGitRunner) CurrentBranch() (string, error) {
	if m.CurrentBranchFunc != nil {
		return m.CurrentBranchFunc()
	}
	return "master", nil
}

// Upstream returns the current branch's upstream ref
func (m *MockGitRunner) Upstream() (string, error) {
	if m.UpstreamFunc != nil {
		return m.UpstreamFunc()
	}
	return "origin/master", nil
}

// CountRange returns the number of commits in the range from..to
func (m *MockGitRunner) CountRange(from, to string) (int, error) {
	if m.CountRangeFunc != nil {
		return m.CountRangeFunc(from, to)
	}
	return 0, nil
}

// WorkDir returns the working directory of the git repository
func (m *MockGitRunner) WorkDir() string {
	return m.workDir
}

// Ensure MockGitRunner implements GitExecutor interface
var _ GitExecutor = (*MockGitRunner)(nil)
