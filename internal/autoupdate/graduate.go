package autoupdate

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/obentoo/bentoolkit/internal/autoupdate/cooldown"
	"github.com/obentoo/bentoolkit/internal/autoupdate/ebuilds"
	"github.com/obentoo/bentoolkit/internal/autoupdate/registry"
	"github.com/obentoo/bentoolkit/internal/common/ebuild"
	"github.com/obentoo/bentoolkit/internal/common/tui"
)

// graduationURL returns rawURL asked for the newest commit no later than
// until, or false when the endpoint cannot be asked that.
//
// Both commit-list APIs the registry's track = "commit" records use take the
// same parameter: GitHub's /repos/:owner/:repo/commits and GitLab's
// /projects/:id/repository/commits both accept until=<ISO 8601>, and both
// still answer newest-first, so path "[0]..." reads the snapshot the cooldown
// would have let through. A single-commit endpoint (GitHub /commits/<ref>)
// has no such parameter and is refused.
func graduationURL(rawURL string, until time.Time) (string, bool) {
	u, err := url.Parse(rawURL)
	if err != nil || u.Host == "" {
		return "", false
	}
	path := strings.TrimSuffix(u.EscapedPath(), "/")
	if !strings.HasSuffix(path, "/commits") {
		return "", false
	}
	q := u.Query()
	q.Set("until", until.UTC().Format(time.RFC3339))
	u.RawQuery = q.Encode()
	return u.String(), true
}

// checkerCooldown is the overlay's cooldown block as the checker reads it:
// loaded once per Checker, read-only. A block that cannot be read disables
// graduation for the run (the apply then fails closed on its own).
type checkerCooldown struct {
	once  sync.Once
	block *cooldown.Block
}

// cooldownDays returns the release delay the cooldown applies to the record,
// or 0 when the record is not under the cooldown: no block, an exempt record.
func (c *Checker) cooldownDays(cfg *registry.PackageConfig) int {
	c.cooldownState.once.Do(func() {
		c.cooldownState.block, _ = cooldown.Load(c.overlayPath)
	})
	b := c.cooldownState.block
	if b == nil || cfg.CooldownExempt != nil {
		return 0
	}
	if cfg.CooldownDays > 0 {
		return cfg.CooldownDays
	}
	return b.Days
}

// graduationTarget returns the snapshot version and commit a commit-tracked
// line under the cooldown graduates to: the newest upstream commit at least
// the cooldown's days old, versioned on the same base and suffix as the
// candidate. Empty when the line is not under the cooldown, its URL cannot be
// asked for an older commit, or the lookup fails (logged; the candidate is
// still masked, only the graduation waits for the next check).
func (c *Checker) graduationTarget(ctx context.Context, pkg string, cfg *registry.PackageConfig, currentVersion string) (version, sha string) {
	days := c.cooldownDays(cfg)
	if days == 0 {
		return "", ""
	}
	until := c.now().AddDate(0, 0, -days)
	gURL, ok := graduationURL(cfg.URL, until)
	if !ok {
		return "", ""
	}
	gCfg := *cfg
	gCfg.URL = gURL
	info, err := c.fetchCommitInfo(ctx, &gCfg)
	if err != nil {
		c.logger().Warn("cooldown: graduation lookup failed; the candidate is masked, the installable snapshot waits for the next check",
			"package", pkg, "err", fmt.Errorf("asking %s for a commit before %s: %w", pkg, until.Format(time.RFC3339), err))
		return "", ""
	}
	version, _ = commitTrackedVersion(currentVersion, info)
	return version, info.SHA
}

// liberatedVersion returns the newest version of the record's release line
// that the cooldown block does not mask: what a user installs today. Empty
// when every version of the line is masked.
func (a *Applier) liberatedVersion(key string) (string, error) {
	category, name, ok := ebuilds.SplitPkgAtom(key)
	if !ok {
		return "", fmt.Errorf("%q is not a category/package atom", key)
	}
	line, err := ebuilds.LineVersions(a.logger(), a.overlayPath, key, a.configs[key].Series)
	if err != nil {
		return "", err
	}
	a.cooldown.mu.Lock()
	block, berr := a.cooldownLocked()
	masked := map[string]bool{}
	if block != nil {
		for _, e := range block.Entries {
			if e.Package == category+"/"+name {
				masked[e.Version] = true
			}
		}
	}
	a.cooldown.mu.Unlock()
	if berr != nil {
		return "", berr
	}
	best := ""
	for _, v := range line {
		if masked[v] {
			continue
		}
		if best == "" || ebuild.CompareVersions(v, best) > 0 {
			best = v
		}
	}
	return best, nil
}

// applyGraduation bumps the line's newest unmasked version to the graduation
// target the check queued (PendingUpdate.GraduateVersion), through the same
// apply as any bump — staging, gates, promotion, md5-cache — but unmasked: the
// target is already the cooldown's days old upstream.
//
// It runs on a view of the Applier with its own throwaway pending list, so the
// candidate's pending entry, its status and the registry pin are never
// touched: the candidate apply that follows owns those. Nothing is done when
// the line has no unmasked version or is already at or past the target. The
// returned result is nil when nothing was attempted.
func (a *Applier) applyGraduation(ctx context.Context, key string, upd PendingUpdate) *ApplyResult {
	current, err := a.liberatedVersion(key)
	if err != nil {
		a.logger().Warn("cooldown: graduation skipped: the line's unmasked version cannot be read", "package", key, "err", err)
		return nil
	}
	if current == "" || ebuild.CompareVersions(upd.GraduateVersion, current) <= 0 {
		return nil
	}
	dir, err := os.MkdirTemp("", "bentoo-graduate-")
	if err != nil {
		a.logger().Warn("cooldown: graduation skipped", "package", key, "err", fmt.Errorf("creating its pending list: %w", err))
		return nil
	}
	defer func() {
		if err := os.RemoveAll(dir); err != nil {
			a.logger().Warn("cooldown: graduation left its temporary pending list behind", "dir", dir, "err", err)
		}
	}()
	pl, err := NewPendingList(dir)
	if err == nil {
		err = pl.Add(PendingUpdate{
			Package:        key,
			CurrentVersion: current,
			NewVersion:     upd.GraduateVersion,
			CommitHash:     upd.GraduateCommit,
			Status:         StatusPending,
		})
	}
	if err != nil {
		a.logger().Warn("cooldown: graduation skipped", "package", key, "err", fmt.Errorf("queueing %s: %w", upd.GraduateVersion, err))
		return nil
	}

	view := *a
	view.pending = pl
	view.pendingDeleteFn = pl.Delete
	view.setVersionsFn = func(string, map[string]string) error { return nil }
	view.graduating = true
	view.reporter = tui.Noop()
	// The error is also on res.Error, logged below; a failed graduation never
	// fails the candidate, so it is not returned.
	res, _ := view.Apply(ctx, key)
	if res != nil {
		res.Graduation = true
		if res.Success {
			a.logger().Info("cooldown: graduated", "package", key, "from", current, "version", res.NewVersion)
		} else if res.Error != nil {
			a.logger().Warn("cooldown: graduation failed; the installable snapshot stays where it was",
				"package", key, "version", upd.GraduateVersion, "err", res.Error)
		}
	}
	return res
}
