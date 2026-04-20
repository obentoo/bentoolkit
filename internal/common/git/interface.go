package git

// GitExecutor defines the interface for git operations.
// This interface allows for mocking git operations in tests.
type GitExecutor interface {
	// Status returns the current git status as a list of StatusEntry
	Status() ([]StatusEntry, error)

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

	// WorkDir returns the working directory of the git repository
	WorkDir() string
}
