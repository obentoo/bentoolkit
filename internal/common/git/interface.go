package git

// GitExecutor defines the interface for git operations.
// This interface allows for mocking git operations in tests.
type GitExecutor interface {
	// Status returns the current git status as a list of StatusEntry
	Status() ([]StatusEntry, error)

	// StagedStatus returns only the entries staged in the index (what a commit would include)
	StagedStatus() ([]StatusEntry, error)

	// Add stages files for commit
	Add(paths ...string) error

	// Commit creates a git commit with the specified message and author
	Commit(message, user, email string) error

	// Push pushes commits to the remote repository
	Push() error

	// PushDryRun shows what would be pushed without actually pushing
	PushDryRun() (string, error)

	// Fetch fetches changes from a remote repository
	Fetch(remote string) error

	// Merge merges a branch into the current branch
	Merge(branch string) error

	// MergeFFOnly merges a branch only if it can fast-forward. It refuses
	// rather than writing a merge commit when the branches have diverged.
	MergeFFOnly(branch string) error

	// Rebase replays the current branch's commits on top of the given branch
	Rebase(branch string) error

	// CurrentBranch returns the name of the checked-out branch. It reports an
	// error on a detached HEAD, which has no branch to pull into.
	CurrentBranch() (string, error)

	// Upstream returns the current branch's configured upstream ref
	// (for example "origin/master"), or an error when none is configured.
	Upstream() (string, error)

	// CountRange returns how many commits are reachable from `to` but not
	// from `from` — the size of the range `from..to`.
	CountRange(from, to string) (int, error)

	// WorkDir returns the working directory of the git repository
	WorkDir() string
}
