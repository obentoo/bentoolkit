package git

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/obentoo/bentoolkit/internal/common/procgroup"
	"github.com/obentoo/bentoolkit/internal/common/tui"
)

var (
	ErrFileNotFound       = errors.New("file not found")
	ErrPathOutsideOverlay = errors.New("path is outside overlay directory")
	ErrInvalidPath        = errors.New("invalid path")
	ErrGitCommand         = errors.New("git command failed")
	ErrNoUpstream         = errors.New("no upstream configured for the current branch")
	ErrDetachedHead       = errors.New("HEAD is detached; check out a branch first")
)

// Every git operation is bounded, on top of the context its caller passes.
// NetworkTimeout bounds the ones that talk to a remote: Push, PushDryRun and
// Fetch. LocalTimeout bounds every other one: status, add, commit,
// merge, rebase, rev-parse and rev-list. An operation that runs
// past its bound is stopped; its error wraps context.DeadlineExceeded and reads
// "git <op> timed out after <bound>".
const (
	NetworkTimeout = 5 * time.Minute
	LocalTimeout   = 1 * time.Minute
)

// networkTimeout and localTimeout are the bounds the runner applies. They are
// NetworkTimeout and LocalTimeout, except inside this package's own tests,
// which shorten them to watch a bound expire. A caller's short deadline cannot
// show that: then the caller's deadline expired, not the bound.
var (
	networkTimeout = NetworkTimeout
	localTimeout   = LocalTimeout
)

// GitRunner executes git commands in a specific working directory
type GitRunner struct {
	workDir string

	// reporter receives stage/done/tail events for mutating and streaming ops.
	// Defaults to tui.Noop() so callers that supply no reporter behave exactly
	// as before.
	reporter tui.Reporter
	// taskID identifies this runner's task in reporter events.
	taskID string
	// execCommand builds the underlying *exec.Cmd; it is a seam so tests can
	// substitute the subprocess. Defaults to exec.CommandContext. It
	// receives the bounded context of the run, and the command it returns must
	// be created by exec.CommandContext on that context: the runner sets the
	// command's Cancel (procgroup.Foreground), which os/exec refuses to start
	// on a command that has no context.
	execCommand func(ctx context.Context, name string, arg ...string) *exec.Cmd
}

// GitRunnerOption configures a GitRunner at construction time.
type GitRunnerOption func(*GitRunner)

// NewGitRunner creates a new GitRunner for the specified working directory.
// With no options it uses a no-op reporter and exec.CommandContext, so
// existing no-arg callers are unaffected.
func NewGitRunner(workDir string, opts ...GitRunnerOption) *GitRunner {
	g := &GitRunner{
		workDir:     workDir,
		reporter:    tui.Noop(),
		execCommand: exec.CommandContext,
	}
	for _, opt := range opts {
		opt(g)
	}
	return g
}

// WithGitReporter sets the reporter and task id used for stage/done/tail
// events. A nil reporter is normalized to tui.Noop().
func WithGitReporter(r tui.Reporter, id string) GitRunnerOption {
	return func(g *GitRunner) {
		if r == nil {
			r = tui.Noop()
		}
		g.reporter = r
		g.taskID = id
	}
}

// WithGitExecCommand overrides the subprocess seam. A nil fn leaves the
// default (exec.CommandContext) in place. fn receives the bounded context of
// the run and must build its command with exec.CommandContext on it, so a
// cancel or a timeout reaches the child.
func WithGitExecCommand(fn func(ctx context.Context, name string, arg ...string) *exec.Cmd) GitRunnerOption {
	return func(g *GitRunner) {
		if fn != nil {
			g.execCommand = fn
		}
	}
}

// staged runs fn between a TaskStage(op) and a TaskDone event, keeping the
// stage/done wiring DRY across mutating ops.
func (g *GitRunner) staged(op string, fn func() error) error {
	g.reporter.TaskStage(g.taskID, op)
	err := fn()
	g.reporter.TaskDone(g.taskID, err == nil, "", "")
	return err
}

// WorkDir returns the working directory of the GitRunner
func (g *GitRunner) WorkDir() string {
	return g.workDir
}

// run runs git with args in the work dir, writing its output to stdout and
// stderr. It returns nil when git succeeds, the error from interrupted when a
// context ended the run, and otherwise the run's own error, for the caller to
// explain with the output git printed.
//
// The run is bounded by timeout on top of ctx, and git stays in the terminal's
// foreground process group (procgroup.Foreground): it may prompt there for an
// ssh passphrase, https credentials or a gpg pinentry, and a child in a
// background group would be stopped by SIGTTIN at its first read. When either
// context is done git gets SIGTERM, and SIGKILL procgroup.GracePeriod later if
// it is still running.
func (g *GitRunner) run(ctx context.Context, timeout time.Duration, stdout, stderr io.Writer, args ...string) error {
	runCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	cmd := g.execCommand(runCtx, "git", args...)
	cmd.Dir = g.workDir
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	procgroup.Foreground(cmd)

	err := procgroup.Result(cmd, cmd.Run())
	if err == nil {
		return nil
	}
	if stopped := interrupted(ctx, runCtx, timeout, args[0]); stopped != nil {
		return stopped
	}
	return err
}

// interrupted returns the error for a git run, operation op, that ended with
// its context, or nil when neither context is done. parent is the caller's
// context; run is the one bounded by timeout that git ran under.
//
// The contexts decide, not the run's error: git stopped by the signal reports
// only "signal: terminated", whatever caused it. A done parent means the caller
// stopped the run, or the caller's own deadline did, which is not this
// operation's bound, so the parent's error is wrapped as it is. A done run
// context under a live parent hit the bound. run is read first:
// contexts never come back to life, so a parent that is live after that read
// was live before it, and a cancel landing between the reads is the parent's.
func interrupted(parent, run context.Context, timeout time.Duration, op string) error {
	runDone := run.Err() != nil
	if err := parent.Err(); err != nil {
		return fmt.Errorf("git %s: %w", op, err)
	}
	if runDone {
		return fmt.Errorf("git %s timed out after %s: %w", op, timeout, context.DeadlineExceeded)
	}
	return nil
}

// stoppedByContext reports whether err is run's report that a context ended
// the run (see interrupted) rather than a failure git reported itself. The ops
// that explain a failure with git's output, or map it to a sentinel, check it
// first so that neither hides a cancel or a timeout.
func stoppedByContext(err error) bool {
	return errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)
}

// runCommand runs a git command bounded by timeout (see run) and returns its
// stdout, its stderr and an error. A failure git reported itself carries
// ErrGitCommand and the last tui.MaxCapturedBytes of stderr, or is
// the bare run error when git wrote nothing to stderr.
func (g *GitRunner) runCommand(ctx context.Context, timeout time.Duration, args ...string) (stdout, stderr string, err error) {
	var stdoutBuf, stderrBuf bytes.Buffer
	err = g.run(ctx, timeout, &stdoutBuf, &stderrBuf, args...)
	stdout = stdoutBuf.String()
	stderr = stderrBuf.String()

	if err != nil && !stoppedByContext(err) && stderr != "" {
		// Wrap the error with stderr for context
		err = errors.Join(ErrGitCommand, errors.New(tui.Tail(strings.TrimSpace(stderr))))
	}
	return stdout, stderr, err
}

// runExplained runs a local git command that explains its failures on stdout
// as well as stderr (merge conflicts, a refused fast-forward, a stopped
// rebase) and folds both streams, cut to their last tui.MaxCapturedBytes,
// into the error, so the caller can read the explanation and
// detect a conflict.
func (g *GitRunner) runExplained(ctx context.Context, args ...string) error {
	stdout, stderr, err := g.runCommand(ctx, localTimeout, args...)
	if err != nil && !stoppedByContext(err) {
		if combined := strings.TrimSpace(stdout + "\n" + stderr); combined != "" {
			return errors.Join(ErrGitCommand, errors.New(tui.Tail(combined)))
		}
	}
	return err
}

// StatusEntry represents a single entry from git status --porcelain
type StatusEntry struct {
	Status   string // A, M, D, R, ??
	FilePath string
}

// Status returns the current git status as a list of StatusEntry
func (g *GitRunner) Status(ctx context.Context) ([]StatusEntry, error) {
	stdout, _, err := g.runCommand(ctx, localTimeout, "status", "--porcelain")
	if err != nil {
		return nil, err
	}

	return ParseStatusOutput(stdout), nil
}

// StagedStatus returns only the entries staged in the index, i.e. exactly what
// a commit would include. It runs git status --porcelain and keeps entries whose
// index column (X) is set, dropping unstaged-only and untracked files.
func (g *GitRunner) StagedStatus(ctx context.Context) ([]StatusEntry, error) {
	stdout, _, err := g.runCommand(ctx, localTimeout, "status", "--porcelain")
	if err != nil {
		return nil, err
	}

	return ParseStagedStatusOutput(stdout), nil
}

// ParseStagedStatusOutput parses git status --porcelain output and keeps only
// entries staged in the index. In the "XY filename" format, X is the index
// (staged) status and Y is the worktree status; an entry is staged when X is
// neither a space (unstaged-only) nor '?' (untracked).
func ParseStagedStatusOutput(output string) []StatusEntry {
	var entries []StatusEntry

	lines := strings.Split(output, "\n")
	for _, line := range lines {
		if len(line) < 3 {
			continue
		}

		// X = index status, Y = worktree status. Keep only staged (X set).
		indexStatus := line[0]
		if indexStatus == ' ' || indexStatus == '?' {
			continue
		}

		status := string(indexStatus)
		filePath := line[3:]

		// Staged rename: "R  old -> new" — split into delete + add, mirroring
		// ParseStatusOutput so version-bump detection keeps working.
		if status == "R" {
			parts := strings.Split(filePath, " -> ")
			if len(parts) == 2 {
				entries = append(entries, StatusEntry{
					Status:   "D",
					FilePath: strings.TrimSpace(parts[0]),
				})
				entries = append(entries, StatusEntry{
					Status:   "A",
					FilePath: strings.TrimSpace(parts[1]),
				})
				continue
			}
		}

		entries = append(entries, StatusEntry{
			Status:   status,
			FilePath: filePath,
		})
	}

	return entries
}

// ParseStatusOutput parses git status --porcelain output into StatusEntry slice
func ParseStatusOutput(output string) []StatusEntry {
	var entries []StatusEntry

	lines := strings.Split(output, "\n")
	for _, line := range lines {
		if len(line) < 3 {
			continue
		}

		// Git status --porcelain format: XY filename
		// X = index status, Y = worktree status
		// We use the index status (X) for staged files, worktree status (Y) for unstaged
		status := strings.TrimSpace(line[:2])
		filePath := line[3:]

		// Handle renamed files: R  old -> new
		// Convert rename to delete + add for proper version bump detection
		if strings.HasPrefix(status, "R") {
			parts := strings.Split(filePath, " -> ")
			if len(parts) == 2 {
				oldPath := strings.TrimSpace(parts[0])
				newPath := strings.TrimSpace(parts[1])
				// Add delete entry for old file
				entries = append(entries, StatusEntry{
					Status:   "D",
					FilePath: oldPath,
				})
				// Add entry for new file
				entries = append(entries, StatusEntry{
					Status:   "A",
					FilePath: newPath,
				})
				continue
			}
		}

		entries = append(entries, StatusEntry{
			Status:   status,
			FilePath: filePath,
		})
	}

	return entries
}

// bentooScratchExclude is a pathspec that keeps bentoo's scratch files out of
// every `git add` this package runs: the temporary files of an atomic write
// (".<name>.bentoo-<pid>-<random>"), the realign and promote temporaries, and
// the lock files (".autoupdate.bentoo-lock", ".state.bentoo-lock"). A killed
// run can leave one of them in the overlay, and staging it would commit and
// publish it.
const bentooScratchExclude = ":(exclude,glob)**/.*.bentoo-*"

// Add stages files for commit with path validation. Each path follows "--",
// so a file whose name begins with "-" is staged as a file, never read as an
// option; bentooScratchExclude keeps bentoo's scratch files out.
func (g *GitRunner) Add(ctx context.Context, paths ...string) error {
	return g.staged("add", func() error {
		if len(paths) == 0 {
			// Default to adding all changes
			_, _, err := g.runCommand(ctx, localTimeout, "add", "--", ".", bentooScratchExclude)
			return err
		}

		for _, path := range paths {
			if err := g.validateAndAddPath(ctx, path); err != nil {
				return err
			}
		}

		return nil
	})
}

// validateAndAddPath validates a single path and adds it to staging
func (g *GitRunner) validateAndAddPath(ctx context.Context, path string) error {
	// Resolve the path relative to workDir
	var absPath string
	if filepath.IsAbs(path) {
		absPath = path
	} else {
		absPath = filepath.Join(g.workDir, path)
	}

	// Clean the path to resolve any .. or . components
	absPath = filepath.Clean(absPath)
	workDirAbs := filepath.Clean(g.workDir)

	// Check if path is inside the overlay directory
	relPath, err := filepath.Rel(workDirAbs, absPath)
	if err != nil {
		return errors.Join(ErrInvalidPath, err)
	}

	// If the relative path starts with "..", it's outside the overlay
	if strings.HasPrefix(relPath, "..") {
		return ErrPathOutsideOverlay
	}

	// Check if the path exists (use Lstat to detect symlinks without following them)
	linfo, lstatErr := os.Lstat(absPath)
	if lstatErr != nil {
		return ErrFileNotFound
	}

	// Resolve symlinks to prevent traversal attacks.
	// For symlinks: EvalSymlinks follows the link — broken symlinks return ErrInvalidPath.
	// For regular files/dirs: EvalSymlinks resolves any symlink components in the path.
	var realPath string
	if linfo.Mode()&os.ModeSymlink != 0 {
		// It's a symlink — resolve it; broken symlink → ErrInvalidPath
		resolved, err := filepath.EvalSymlinks(absPath)
		if err != nil {
			return fmt.Errorf("%w: cannot resolve symlink: %w", ErrInvalidPath, err)
		}
		realPath = resolved
	} else {
		// Regular file or directory — resolve any symlink components in the path itself
		resolved, err := filepath.EvalSymlinks(absPath)
		if err != nil {
			return fmt.Errorf("%w: cannot resolve path: %w", ErrInvalidPath, err)
		}
		realPath = resolved
	}

	realWorkDir, err := filepath.EvalSymlinks(workDirAbs)
	if err != nil {
		return fmt.Errorf("cannot resolve overlay path: %w", err)
	}

	// Re-validate containment with real (symlink-resolved) paths
	realRelPath, err := filepath.Rel(realWorkDir, realPath)
	if err != nil {
		return errors.Join(ErrInvalidPath, err)
	}

	if strings.HasPrefix(realRelPath, "..") {
		return ErrPathOutsideOverlay
	}

	// Add the file to staging; "--" ends the options, so the path is a path.
	_, _, err = g.runCommand(ctx, localTimeout, "add", "--", path, bentooScratchExclude)
	return err
}

// Commit creates a git commit with the specified message and author
func (g *GitRunner) Commit(ctx context.Context, message, user, email string) error {
	return g.staged("commit", func() error {
		args := []string{"commit", "-m", message}

		// Set author if provided
		if user != "" && email != "" {
			author := user + " <" + email + ">"
			args = append(args, "--author", author)
		}

		_, _, err := g.runCommand(ctx, localTimeout, args...)
		return err
	})
}

// Push pushes commits to the remote repository, bounded by NetworkTimeout.
func (g *GitRunner) Push(ctx context.Context) error {
	return g.staged("push", func() error {
		_, _, err := g.runCommand(ctx, networkTimeout, "push")
		return err
	})
}

// PushDryRun shows what would be pushed without actually pushing. It asks the
// remote, so it is bounded by NetworkTimeout.
func (g *GitRunner) PushDryRun(ctx context.Context) (string, error) {
	stdout, _, err := g.runCommand(ctx, networkTimeout, "push", "--dry-run", "-v")
	if err != nil {
		return "", err
	}
	if stdout == "" {
		return "Nothing to push (up-to-date with remote)", nil
	}
	return strings.TrimSpace(stdout), nil
}

// Fetch fetches changes from a remote repository. It is a streaming op: git's
// progress output (which it writes to stderr) is tailed live via a
// tui.StreamCapture and also captured, so a failing fetch preserves its output
// in the returned error — the last tui.MaxCapturedBytes of it, which is
// what Captured keeps. Under the default Noop reporter and
// exec.CommandContext this is behavior-equivalent to a buffered fetch.
// It is bounded by NetworkTimeout, and a cancel or a timeout is reported as
// runCommand reports it (see run).
//
// The remote comes from the overlay config, so "--end-of-options" precedes it:
// read as an option, a value such as "--upload-pack=<cmd>" would run <cmd>.
func (g *GitRunner) Fetch(ctx context.Context, remote string) error {
	g.reporter.TaskStage(g.taskID, "fetch")

	// One capture for both streams (combined), so git's stderr progress is tailed
	// and preserved alongside any stdout.
	sc := tui.NewStreamCapture(g.reporter, g.taskID, tui.StreamStdout)
	runErr := g.run(ctx, networkTimeout, sc, sc, "fetch", "--end-of-options", remote)
	_ = sc.Close()

	g.reporter.TaskDone(g.taskID, runErr == nil, "", "")

	if runErr == nil || stoppedByContext(runErr) {
		return runErr
	}
	// Preserve the captured output in the error, mirroring runCommand's
	// wrapping.
	if captured := strings.TrimSpace(sc.Captured()); captured != "" {
		return errors.Join(ErrGitCommand, errors.New(captured))
	}
	return errors.Join(ErrGitCommand, runErr)
}

// Merge merges a branch into the current branch.
// If there are conflicts, the error message includes the conflict details from
// stdout. "--end-of-options" precedes the branch, so a name beginning with "-"
// is read as a revision, never as an option.
func (g *GitRunner) Merge(ctx context.Context, branch string) error {
	return g.staged("merge", func() error {
		return g.runExplained(ctx, "merge", "--end-of-options", branch)
	})
}

// MergeFFOnly merges a branch only when the merge can fast-forward. Where
// Merge writes a merge commit over diverged history, this refuses and leaves
// the branch untouched, so the caller can report the divergence instead of
// burying it in a commit nobody asked for. Git reports "Not possible to
// fast-forward" on stdout, so both streams reach the caller. The branch
// follows "--end-of-options", as in Merge.
func (g *GitRunner) MergeFFOnly(ctx context.Context, branch string) error {
	return g.staged("merge", func() error {
		return g.runExplained(ctx, "merge", "--ff-only", "--end-of-options", branch)
	})
}

// Rebase replays the current branch's commits on top of branch. As with
// Merge, conflict details arrive on stdout and are folded into the error, and
// the branch follows "--end-of-options".
func (g *GitRunner) Rebase(ctx context.Context, branch string) error {
	return g.staged("rebase", func() error {
		return g.runExplained(ctx, "rebase", "--end-of-options", branch)
	})
}

// CurrentBranch returns the checked-out branch name. A detached HEAD yields
// the literal "HEAD" from git, which is not a branch anything can be pulled
// into, so it is reported as an error.
func (g *GitRunner) CurrentBranch(ctx context.Context) (string, error) {
	stdout, _, err := g.runCommand(ctx, localTimeout, "rev-parse", "--abbrev-ref", "HEAD")
	if err != nil {
		return "", err
	}
	branch := strings.TrimSpace(stdout)
	if branch == "" || branch == "HEAD" {
		return "", ErrDetachedHead
	}
	return branch, nil
}

// Upstream returns the current branch's upstream ref, such as
// "origin/master". A branch with no upstream yields ErrNoUpstream rather
// than git's raw "no upstream configured" text. A cancel or a timeout is not
// a missing upstream and is returned as it is.
func (g *GitRunner) Upstream(ctx context.Context) (string, error) {
	stdout, _, err := g.runCommand(ctx, localTimeout, "rev-parse", "--abbrev-ref", "--symbolic-full-name", "@{u}")
	if err != nil {
		if stoppedByContext(err) {
			return "", err
		}
		return "", ErrNoUpstream
	}
	upstream := strings.TrimSpace(stdout)
	if upstream == "" {
		return "", ErrNoUpstream
	}
	return upstream, nil
}

// CountRange returns the number of commits in the range from..to — that is,
// commits reachable from `to` but not from `from`.
// "--end-of-options" precedes the range, so neither end is read as an option.
func (g *GitRunner) CountRange(ctx context.Context, from, to string) (int, error) {
	stdout, _, err := g.runCommand(ctx, localTimeout, "rev-list", "--count", "--end-of-options", from+".."+to)
	if err != nil {
		return 0, err
	}
	count, err := strconv.Atoi(strings.TrimSpace(stdout))
	if err != nil {
		return 0, errors.Join(ErrGitCommand, err)
	}
	return count, nil
}

// Ensure GitRunner implements GitExecutor interface
var _ GitExecutor = (*GitRunner)(nil)
