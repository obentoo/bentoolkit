package overlay

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	"github.com/obentoo/bentoolkit/internal/common/git"
	"github.com/obentoo/bentoolkit/internal/common/logging"
)

// ErrCheckoutBehind reports that the overlay checkout lacks commits its
// upstream already has, so anything written from it lands on a stale base.
var ErrCheckoutBehind = errors.New("the overlay checkout is behind its upstream")

// DefaultUpstreamFetchTimeout bounds the optional fetch CheckUpstream runs
// before counting. It is shorter than git.NetworkTimeout on purpose: the
// fetch only refreshes a ref the check can do without, so a slow remote must
// not hold the whole command.
const DefaultUpstreamFetchTimeout = 60 * time.Second

// UpstreamCheckOptions configures CheckUpstream.
type UpstreamCheckOptions struct {
	// Fetch refreshes the remote-tracking ref before counting. Off, the count
	// is taken against the ref as the last fetch or pull left it, which may
	// itself be stale; that is the default because the check must work
	// offline and must not touch the network unasked.
	Fetch bool
	// Remote is the remote Fetch refreshes; empty means "origin", as in Pull.
	Remote string
	// FetchTimeout bounds the fetch; zero means DefaultUpstreamFetchTimeout.
	FetchTimeout time.Duration
	// Logger receives the skip and fetch diagnostics; nil discards them.
	Logger *slog.Logger
}

// UpstreamStatus is what CheckUpstream found.
type UpstreamStatus struct {
	// Checked is false when the check was skipped: not a git checkout, a
	// detached HEAD, no upstream configured, or a count git could not take.
	Checked  bool
	Branch   string
	Upstream string
	// Behind counts the commits in HEAD..upstream, Ahead those in
	// upstream..HEAD. A diverged checkout has both and counts as behind.
	Behind int
	Ahead  int
	// Fetched reports that the remote-tracking ref was refreshed by this
	// check, so Behind is current rather than as of the last fetch.
	Fetched bool
}

// IsBehind reports whether the checkout lacks commits its upstream has.
func (s UpstreamStatus) IsBehind() bool {
	return s.Checked && s.Behind > 0
}

// Refusal returns nil when the checkout is not behind, and otherwise an error
// wrapping ErrCheckoutBehind that names the count and the command that fixes
// it. A caller about to write calls it before the first write.
func (s UpstreamStatus) Refusal() error {
	if !s.IsBehind() {
		return nil
	}
	diverged := ""
	if s.Ahead > 0 {
		diverged = fmt.Sprintf(" and %d ahead (diverged)", s.Ahead)
	}
	freshness := "as of the last fetch"
	if s.Fetched {
		freshness = "just fetched"
	}
	return fmt.Errorf("%w: branch %s is %d commit(s) behind %s (%s)%s; run 'bentoo overlay pull' first",
		ErrCheckoutBehind, s.Branch, s.Behind, s.Upstream, freshness, diverged)
}

// IsCheckoutRoot reports whether path is the top of a git checkout: it holds
// a .git directory, or the .git file a worktree carries. The upstream check
// runs only there, so an overlay that merely sits inside some other
// repository is not measured against that repository's branch.
func IsCheckoutRoot(path string) bool {
	_, err := os.Lstat(filepath.Join(path, ".git"))
	return err == nil
}

// CheckUpstream counts how far the checked-out branch is behind its
// configured upstream, the same way Pull does (CountRange HEAD..upstream).
//
// It never fails the caller. Every way of not knowing degrades instead: no
// branch or no upstream skips at DEBUG, a failed fetch warns once and counts
// against the local remote-tracking ref, and a count git refuses warns and
// skips. Only the caller decides what a behind checkout means.
func CheckUpstream(ctx context.Context, runner git.GitExecutor, opts UpstreamCheckOptions) UpstreamStatus {
	log := logging.OrDiscard(opts.Logger)

	branch, err := runner.CurrentBranch(ctx)
	if err != nil {
		log.Debug("upstream check skipped: no branch checked out", "path", runner.WorkDir(), "err", err)
		return UpstreamStatus{}
	}
	upstream, err := runner.Upstream(ctx)
	if err != nil {
		log.Debug("upstream check skipped: the branch has no upstream", "path", runner.WorkDir(), "branch", branch, "err", err)
		return UpstreamStatus{}
	}

	status := UpstreamStatus{Branch: branch, Upstream: upstream}

	if opts.Fetch {
		remote := opts.Remote
		if remote == "" {
			remote = "origin"
		}
		timeout := opts.FetchTimeout
		if timeout <= 0 {
			timeout = DefaultUpstreamFetchTimeout
		}
		fetchCtx, cancel := context.WithTimeout(ctx, timeout)
		err := runner.Fetch(fetchCtx, remote)
		cancel()
		if err != nil {
			log.Warn("fetching the overlay remote failed; counting against the local remote-tracking ref, which may be stale",
				"path", runner.WorkDir(), "remote", remote, "upstream", upstream, "err", err)
		} else {
			status.Fetched = true
		}
	}

	behind, err := runner.CountRange(ctx, "HEAD", upstream)
	if err != nil {
		log.Warn("upstream check skipped: counting commits behind the upstream failed",
			"path", runner.WorkDir(), "upstream", upstream, "err", err)
		return UpstreamStatus{}
	}
	ahead, err := runner.CountRange(ctx, upstream, "HEAD")
	if err != nil {
		// The behind count is the one that matters; ahead only words the error.
		log.Debug("counting commits ahead of the upstream failed", "path", runner.WorkDir(), "upstream", upstream, "err", err)
		ahead = 0
	}

	status.Checked = true
	status.Behind = behind
	status.Ahead = ahead
	log.Debug("upstream check", "path", runner.WorkDir(), "branch", branch, "upstream", upstream,
		"behind", behind, "ahead", ahead, "fetched", status.Fetched)
	return status
}
