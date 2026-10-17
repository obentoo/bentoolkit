package main

import (
	"context"
	"errors"
	"log/slog"
	"os/exec"
	"slices"
	"time"

	"github.com/obentoo/bentoolkit/internal/common/config"
	"github.com/obentoo/bentoolkit/internal/gentoo/news"
	"github.com/obentoo/bentoolkit/internal/gentoo/pending"
)

// repoRevisions is the stamp Portage rewrites after every repository sync;
// the App triggers an update check when its modification time changes.
const repoRevisions = "/var/lib/portage/repo_revisions"

// The update-check commands, their fixed arguments and their time limits.
const (
	emergeCmd      = "emerge"
	glsaCheckCmd   = "glsa-check"
	updatesLimit   = 10 * time.Minute
	glsaLimit      = 2 * time.Minute
	monitorUpdates = "updates"
	monitorGLSA    = "glsa"
	monitorSync    = "sync"
)

// The state a start-up line reports: off when it turns a monitor off or
// leaves it nothing to watch, ok when the monitor runs and its conditions
// start at the App's starting level.
const (
	stateOff = "off"
	stateOK  = "ok"
)

// updatesArgs lists the pending @world updates. --ask=n overrides an --ask
// in EMERGE_DEFAULT_OPTS, which would otherwise wait on a prompt.
var updatesArgs = []string{"--pretend", "--update", "--deep", "--newuse", "--ask=n", "--color=n", "--quiet", "@world"}

// glsaArgs lists the advisories affecting the host. --nocolor is the only
// switch that keeps glsa-check's colour codes out of its output.
var glsaArgs = []string{"--list", "affected", "--nocolor"}

// priorityWrappers run every update check at niceness 19 and in the idle
// I/O scheduling class, so the child never runs a moment at normal priority.
// ionice -t still runs the command when the kernel refuses the class (a
// seccomp or LSM filter on ioprio_set): without it ionice exits 1 and the
// check would read as failed.
var priorityWrappers = []struct {
	name string
	args []string
}{
	{"nice", []string{"-n", "19"}},
	{"ionice", []string{"-t", "-c", "3"}},
}

// resolveUpdates fills the update-check fields of w: the @world and GLSA
// commands, the sync stamp, the emerge scan and the watched repositories.
// A disabled monitor looks nothing up and reads nothing; a nil capability of
// h is one the host lacks, and nothing is looked up or logged for it.
func resolveUpdates(ctx context.Context, log *slog.Logger, m config.Monitors, h hostHealth, w *healthWiring) {
	if m.Updates.Enabled || m.GLSA.Enabled {
		w.Updates, w.GLSA = updateCommands(ctx, log, m, h.LookPath)
		w.RepoRevisions = repoRevisions
		if h.Reader != nil {
			w.EmergeRunning = h.Reader.EmergeRunning
		}
	}
	if m.Sync.Enabled {
		w.Repos = syncedRepos(ctx, log, h.SyncedRepos, h.SyncTime)
	}
}

// updateCommands looks up the commands of the enabled update monitors and,
// when at least one was found, the priority wrappers they run behind. A
// missing emerge is one WARN naming it and the cause (the updates monitor is
// off); a missing glsa-check is one INFO saying gentoolkit is absent, and any
// other glsa-check lookup failure one WARN (the GLSA monitor is off). A
// missing wrapper is dropped with one WARN and never takes a command with it.
func updateCommands(ctx context.Context, log *slog.Logger, m config.Monitors, lookPath func(string) (string, error)) (updates, glsa *pending.Command) {
	if lookPath == nil {
		return nil, nil
	}
	if m.Updates.Enabled {
		if path, err := lookPath(emergeCmd); err != nil {
			log.WarnContext(ctx, "cannot find or run emerge; the updates monitor is off",
				"monitor", monitorUpdates, "target", "", "state", stateOff, "command", emergeCmd, "error", err)
		} else {
			updates = &pending.Command{Path: path, Args: slices.Clone(updatesArgs), Timeout: updatesLimit}
		}
	}
	if m.GLSA.Enabled {
		switch path, err := lookPath(glsaCheckCmd); {
		case errors.Is(err, exec.ErrNotFound):
			log.InfoContext(ctx, "glsa-check is not installed (app-portage/gentoolkit is absent); the GLSA monitor is off",
				"monitor", monitorGLSA, "target", "", "state", stateOff, "command", glsaCheckCmd)
		case err != nil:
			log.WarnContext(ctx, "cannot run glsa-check; the GLSA monitor is off",
				"monitor", monitorGLSA, "target", "", "state", stateOff, "command", glsaCheckCmd, "error", err)
		default:
			glsa = &pending.Command{Path: path, Args: slices.Clone(glsaArgs), Timeout: glsaLimit}
		}
	}
	if updates == nil && glsa == nil {
		return nil, nil
	}
	monitor := monitorUpdates
	if updates == nil {
		monitor = monitorGLSA
	}
	wrap := priorityWrap(ctx, log, monitor, lookPath)
	for _, c := range []*pending.Command{updates, glsa} {
		if c != nil {
			c.Wrap = slices.Clone(wrap)
		}
	}
	return updates, glsa
}

// priorityWrap looks up each priority wrapper once and returns the prefix
// the found ones form. A wrapper that cannot be found is left out with one
// WARN naming it, logged under monitor, the first command it would wrap.
func priorityWrap(ctx context.Context, log *slog.Logger, monitor string, lookPath func(string) (string, error)) []string {
	var wrap []string
	for _, pw := range priorityWrappers {
		path, err := lookPath(pw.name)
		if err != nil {
			log.WarnContext(ctx, "a priority wrapper is missing; update checks run without it",
				"monitor", monitor, "target", "", "state", stateOK, "command", pw.name, "error", err)
			continue
		}
		wrap = append(wrap, path)
		wrap = append(wrap, pw.args...)
	}
	return wrap
}

// syncedRepos reads repos.conf once and returns the repositories whose sync
// age is watched. An unreadable repos.conf is one WARN naming the path and
// the cause, and no repository is watched. A repository with no sync time
// yet is one INFO naming it and a failed read one WARN naming it and its
// location; both stay watched, so a later sync is still seen.
func syncedRepos(ctx context.Context, log *slog.Logger, list func(string) ([]news.Repo, error), syncTime func(string) (time.Time, bool, error)) []news.Repo {
	if list == nil {
		return nil
	}
	repos, err := list(reposConf)
	if err != nil {
		log.WarnContext(ctx, "cannot read the portage repositories configuration; no repository sync age is watched",
			"monitor", monitorSync, "target", "", "state", stateOff, "path", reposConf, "error", err)
		return nil
	}
	if syncTime == nil {
		return repos
	}
	for _, r := range repos {
		_, ok, err := syncTime(r.Location)
		switch {
		case err != nil:
			log.WarnContext(ctx, "cannot read when the repository was last synced; it stays watched",
				"monitor", monitorSync, "target", r.Name, "state", stateOK, "repository", r.Name, "path", r.Location, "error", err)
		case !ok:
			log.InfoContext(ctx, "the repository has no sync time yet; its sync age is skipped until it has one",
				"monitor", monitorSync, "target", r.Name, "state", stateOK, "repository", r.Name, "path", r.Location)
		}
	}
	return repos
}
