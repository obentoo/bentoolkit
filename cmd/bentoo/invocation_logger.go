package main

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/obentoo/bentoolkit/internal/common/logging"
	"github.com/obentoo/bentoolkit/internal/common/secrets"
	"github.com/spf13/cobra"
)

// logLevelEnv names the environment variable that sets the terminal level when
// neither --verbose nor --quiet is given.
const logLevelEnv = "BENTOO_LOG_LEVEL"

// Values of the startup record's log_level_source attribute.
const (
	logLevelSourceFlag    = "flag"
	logLevelSourceEnv     = "env"
	logLevelSourceDefault = "default"
)

// setUpInvocationLogger builds the invocation's one logger and stores it
// in the context of cmd — the command about to run — and of the root, so that
// func execute can reach it after ExecuteContext returns. It returns the func
// that closes the log file; that func is also registered with
// registerExitCleanup, it is safe to call more than once, and calling it
// cancels the registration.
//
// Problems with the level or the file are reported on the logger itself, as one
// WARN each, and never fail the command.
//
// os.Stderr is read here, at call time, never captured at package init: the
// test harness swaps it per run, and a logger holding the first run's stream
// would write into a pipe a later run has already closed.
func setUpInvocationLogger(cmd *cobra.Command, verboseFlag, quietFlag bool) (closeLog func()) {
	env := os.Getenv(logLevelEnv)
	level, levelErr := logging.ResolveLevel(verboseFlag, quietFlag, env)

	opts := logging.Options{Stderr: os.Stderr, Level: level, Resolved: secrets.Resolved}
	dir, dirErr := logging.DefaultLogDir()
	if dirErr == nil {
		opts.LogDir = dir
	}
	l, closeFile, fileErr := logging.New(opts)

	logFile := ""
	switch {
	case dirErr != nil:
		l.Warn("log file unavailable", "err", dirErr)
	case fileErr != nil:
		l.Warn("log file unavailable", "err", fileErr)
	default:
		logFile = filepath.Join(dir, logging.FileName)
	}
	if levelErr != nil {
		l.Warn("invalid "+logLevelEnv, "err", levelErr)
	}
	l.Debug("logging configured",
		"log_level", strings.ToLower(level.String()),
		"log_level_source", logLevelSource(verboseFlag, quietFlag, env, levelErr),
		"log_file", logFile,
	)

	// A close failure has no exit status to change: it is reported on
	// stderr, which the logger still reaches once its file is gone.
	closeOnce := sync.OnceFunc(func() {
		if err := closeFile(); err != nil {
			l.Warn("closing the log file failed", "err", err)
		}
	})
	unregister := registerExitCleanup(closeOnce)
	closeLog = func() {
		closeOnce()
		unregister()
	}

	cmd.SetContext(withInvocationLog(contextOrBackground(cmd.Context()), l, closeLog))
	if root := cmd.Root(); root != cmd {
		root.SetContext(withInvocationLog(contextOrBackground(root.Context()), l, closeLog))
	}
	return closeLog
}

// logCloserKey is the context key under which setUpInvocationLogger stores the
// func that closes the invocation's log file.
type logCloserKey struct{}

// withInvocationLog returns a copy of ctx carrying the invocation logger l and
// closeLog, the func that closes its file.
func withInvocationLog(ctx context.Context, l *slog.Logger, closeLog func()) context.Context {
	return context.WithValue(logging.NewContext(ctx, l), logCloserKey{}, closeLog)
}

// closeInvocationLog closes the log file of the invocation whose logger ctx
// carries, and does nothing when it carries none. Closing twice is harmless.
//
// func execute calls it once ExecuteContext has returned and the failWith cause
// is logged: cobra skips PersistentPostRunE on an error return, and without
// this an in-process run that fails keeps its file open, and its exit cleanup
// registered, until the process ends.
func closeInvocationLog(ctx context.Context) {
	if closeLog, ok := ctx.Value(logCloserKey{}).(func()); ok && closeLog != nil {
		closeLog()
	}
}

// logLevelSource names what decided the terminal level: a flag when --verbose
// or --quiet was given, the environment when a valid non-blank
// BENTOO_LOG_LEVEL did, and the default otherwise.
func logLevelSource(verboseFlag, quietFlag bool, env string, levelErr error) string {
	switch {
	case verboseFlag || quietFlag:
		return logLevelSourceFlag
	case levelErr == nil && strings.TrimSpace(env) != "":
		return logLevelSourceEnv
	default:
		return logLevelSourceDefault
	}
}

// contextOrBackground returns ctx, or context.Background() when ctx is nil — a
// command that was never executed carries no context.
func contextOrBackground(ctx context.Context) context.Context {
	if ctx == nil {
		return context.Background()
	}
	return ctx
}
