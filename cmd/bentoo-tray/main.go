// Command bentoo-tray is the session tray that announces bentoo notices.
// This file is the process boundary only: signals, the logger,
// the configuration, the wiring of the adapters and the exit code. Every
// decision lives in internal/tray.
package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math/rand/v2" // nosemgrep: go.lang.security.audit.crypto.math_random.math-random-used -- startup delay and interval jitter (Deps.Rand), not a secret
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"time"

	"github.com/obentoo/bentoolkit/internal/common/config"
	"github.com/obentoo/bentoolkit/internal/common/version"
	"github.com/obentoo/bentoolkit/internal/common/xdg"
	"github.com/obentoo/bentoolkit/internal/desktop/dbusx"
	"github.com/obentoo/bentoolkit/internal/desktop/netmon"
	"github.com/obentoo/bentoolkit/internal/desktop/notify"
	"github.com/obentoo/bentoolkit/internal/desktop/portal"
	"github.com/obentoo/bentoolkit/internal/desktop/sni"
	"github.com/obentoo/bentoolkit/internal/gentoo/news"
	"github.com/obentoo/bentoolkit/internal/gentoo/pkgdb"
	"github.com/obentoo/bentoolkit/internal/tray"
	"github.com/obentoo/bentoolkit/internal/tray/feed"
	"github.com/obentoo/bentoolkit/internal/tray/icons"
	"github.com/obentoo/bentoolkit/internal/tray/state"
	trayversion "github.com/obentoo/bentoolkit/internal/tray/version"
)

// Exit codes.
const (
	exitOK      = 0 // clean stop, or another instance already runs
	exitStartup = 1 // startup failure: bus unreachable, name request, unreadable state
	exitBusLost = 2 // the session bus went away while running
)

// Fixed system paths the tray reads; it never writes any of them.
const (
	reposConf  = "/etc/portage/repos.conf"
	repoName   = "bentoo"
	newsUnread = "/var/lib/gentoo/news/news-bentoo.unread"
)

// newsPoll is how often the news reader stats the unread list.
const newsPoll = time.Minute

// logLevelEnv selects the log level.
const logLevelEnv = "BENTOO_TRAY_LOG_LEVEL"

func main() {
	os.Exit(run(os.Args[1:], os.Stderr, os.Getenv))
}

// run is the whole program; its result is the process exit code.
func run(args []string, stderr io.Writer, getenv func(string) string) int {
	lvl, badLevel := parseLevel(getenv(logLevelEnv))
	log := slog.New(slog.NewTextHandler(stderr, &slog.HandlerOptions{Level: lvl}))
	if badLevel {
		log.Warn("unknown log level; using info", "env", logLevelEnv, "value", getenv(logLevelEnv),
			"known", "debug|info|warn|error")
	}

	switch {
	case len(args) == 1 && args[0] == "--version":
		fmt.Println(trayversion.Info())
		return exitOK
	case len(args) > 0:
		log.Error("unknown arguments; the only option is --version", "args", strings.Join(args, " "))
		return exitStartup
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM, syscall.SIGHUP)
	defer stop()

	return exitCode(log, start(ctx, log, getenv))
}

// parseLevel maps BENTOO_TRAY_LOG_LEVEL to a level. Empty is info; an unknown
// value is info too, reported by the caller (bad is true).
func parseLevel(v string) (lvl slog.Level, bad bool) {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "debug":
		return slog.LevelDebug, false
	case "", "info":
		return slog.LevelInfo, false
	case "warn":
		return slog.LevelWarn, false
	case "error":
		return slog.LevelError, false
	default:
		return slog.LevelInfo, true
	}
}

// exitCode logs how the App ended and maps it to the exit code.
func exitCode(log *slog.Logger, err error) int {
	switch {
	case err == nil:
		return exitOK // the App logged the stop
	case errors.Is(err, tray.ErrAlreadyRunning):
		log.Info("bentoo-tray is already running in this session; exiting", "bus_name", dbusx.BusName)
		return exitOK
	case errors.Is(err, tray.ErrBusLost):
		log.Error("the session bus was lost; exiting", "error", err)
		return exitBusLost
	default:
		log.Error("bentoo-tray could not start", "error", err)
		return exitStartup
	}
}

// start loads the configuration, connects the buses, builds the App and runs
// it until ctx ends. The connections are closed only after Run returns: the
// bus owner reports a closed connection as a lost bus.
func start(ctx context.Context, log *slog.Logger, getenv func(string) string) error {
	statePath, err := statePath(getenv)
	if err != nil {
		return err
	}
	trayCfg := loadTrayConfig(log)

	session, err := dbusx.SessionBus(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = session.Close() }()

	var netMon tray.NetworkMonitor
	if system, err := dbusx.SystemBus(ctx); err != nil {
		log.Warn("the system bus is unreachable; network checks are off", "error", err)
		netMon = netmon.Absent()
	} else {
		defer func() { _ = system.Close() }()
		netMon = netmon.New(system, log)
	}

	notifier, err := notify.New(session, log) //nolint:contextcheck // notify.New takes no ctx by design; it bounds its one call with its own timeout
	if err != nil {
		return fmt.Errorf("starting notifications on the session bus: %w", err)
	}

	feedURL := trayCfg.GetFeedURL()
	fetcher, feedHost, allowedHost := newFeed(log, feedURL)

	repoPath, err := news.RepoLocation(reposConf, repoName)
	if err != nil {
		log.Warn("cannot read the portage repositories configuration; using the default location",
			"repos_conf", reposConf, "repo", repoName, "location", repoPath, "error", err)
	}
	ticks, stopTicks := minuteTicks()
	defer stopTicks()

	log.Info("bentoo-tray starting", "version", trayversion.Version(), "bentoolkit", version.Short(), "feed_url", feedURL, "state", statePath)

	app := tray.New(tray.Deps{
		Log:    log,
		Clock:  tray.SystemClock{},
		Rand:   rand.Float64,
		Config: trayCfg,
		Feed:   fetcher,
		News:   news.NewReader(newsUnread, repoPath, feedHost, ticks),
		Pkgs:   pkgdb.Reader{Root: pkgdb.DefaultRoot},
		Notify: notifier,
		Icon: sni.New(session, log, sni.IconSet{
			Plain:    icons.Pixmaps(icons.Plain),
			Unread:   icons.Pixmaps(icons.Unread),
			Critical: icons.Pixmaps(icons.Critical),
		}),
		Open:  portal.New(session, allowedHost, log, portal.ExecRun),
		Net:   netMon,
		Store: state.Open(statePath),
		Bus:   dbusx.NewOwner(session),
	})
	return app.Run(ctx)
}

// newFeed builds the feed fetcher for feedURL and the hosts derived from it:
// feedHost for the news notices' URLs, allowedHost for the URLs the portal
// may open. A refused URL yields a nil interface, so the App runs on
// the offline news alone, and an empty allowedHost, so nothing is opened.
func newFeed(log *slog.Logger, feedURL string) (fetcher tray.FeedFetcher, feedHost, allowedHost string) {
	if u, err := url.Parse(feedURL); err == nil {
		feedHost = u.Host
	}
	f, err := feed.New(feedURL, nil, "")
	if err != nil {
		log.Error("the feed URL is refused; checking the offline news only", "feed_url", feedURL, "error", err)
		return nil, feedHost, ""
	}
	return f, feedHost, feedHost
}

// statePath is $XDG_STATE_HOME/bentoo-notices/state.json.
func statePath(getenv func(string) string) (string, error) {
	dir := xdg.StateHome(getenv, getenv("HOME"))
	if !filepath.IsAbs(dir) {
		return "", errors.New("locating the state directory: XDG_STATE_HOME and HOME are both unset or relative")
	}
	return filepath.Join(dir, "bentoo-notices", "state.json"), nil
}

// loadTrayConfig returns the tray block of the configuration and logs its
// warnings. A configuration that cannot be loaded leaves the defaults, so a
// broken unrelated key does not stop security notices.
func loadTrayConfig(log *slog.Logger) config.TrayConfig {
	cfg, err := config.Load()
	if err != nil {
		log.Warn("cannot load the configuration; using the tray defaults", "error", err)
		return config.TrayConfig{}
	}
	_, intervalWarnings := cfg.Tray.GetInterval()
	_, muteWarnings := cfg.Tray.GetMute()
	for _, w := range slices.Concat(intervalWarnings, muteWarnings) {
		log.Warn("configuration", "warning", w)
	}
	return cfg.Tray
}

// minuteTicks returns a channel of newsPoll ticks and a stop func that closes
// it, which is what ends the news reader's poll.
func minuteTicks() (<-chan time.Time, func()) {
	out := make(chan time.Time)
	done := make(chan struct{})
	ticker := time.NewTicker(newsPoll)
	go func() {
		defer close(out)
		defer ticker.Stop()
		for {
			select {
			case <-done:
				return
			case t := <-ticker.C:
				select {
				case out <- t:
				case <-done:
					return
				}
			}
		}
	}()
	return out, func() { close(done) }
}
