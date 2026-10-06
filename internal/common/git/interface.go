package git

import "context"

// GitExecutor defines the interface for git operations.
// This interface allows for mocking git operations in tests.
//
// Every method that runs git takes, first, the context that stops it: when ctx
// is done the git child is stopped and the method returns an error wrapping
// ctx.Err().
type GitExecutor interface {
	// Status returns the current git status as a list of StatusEntry
	Status(ctx context.Context) ([]StatusEntry, error)

	// StagedStatus returns only the entries staged in the index (what a commit would include)
	StagedStatus(ctx context.Context) ([]StatusEntry, error)

	// Add stages files for commit
	Add(ctx context.Context, paths ...string) error

	// Commit creates a git commit with the specified message and author
	Commit(ctx context.Context, message, user, email string) error

	// Push pushes commits to the remote repository
	Push(ctx context.Context) error

	// PushDryRun shows what would be pushed without actually pushing
	PushDryRun(ctx context.Context) (string, error)

	// Fetch fetches changes from a remote repository
	Fetch(ctx context.Context, remote string) error

	// Merge merges a branch into the current branch
	Merge(ctx context.Context, branch string) error

	// MergeFFOnly merges a branch only if it can fast-forward. It refuses
	// rather than writing a merge commit when the branches have diverged.
	MergeFFOnly(ctx context.Context, branch string) error

	// Rebase replays the current branch's commits on top of the given branch
	Rebase(ctx context.Context, branch string) error

	// CurrentBranch returns the name of the checked-out branch. It reports an
	// error on a detached HEAD, which has no branch to pull into.
	CurrentBranch(ctx context.Context) (string, error)

	// Upstream returns the current branch's configured upstream ref
	// (for example "origin/master"), or an error when none is configured.
	Upstream(ctx context.Context) (string, error)

	// CountRange returns how many commits are reachable from `to` but not
	// from `from` — the size of the range `from..to`.
	CountRange(ctx context.Context, from, to string) (int, error)

	// WorkDir returns the working directory of the git repository
	WorkDir() string
}
