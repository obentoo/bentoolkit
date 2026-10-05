package logging

import (
	"fmt"
	"log/slog"
	"strings"
)

// levelNames are the values BENTOO_LOG_LEVEL accepts, matched after trimming
// and lower-casing. slog.Level's own text syntax ("warn+2") is deliberately not
// accepted.
var levelNames = map[string]slog.Level{
	"debug": slog.LevelDebug,
	"info":  slog.LevelInfo,
	"warn":  slog.LevelWarn,
	"error": slog.LevelError,
}

// ResolveLevel returns the terminal level: error under quiet, debug under
// verbose, else the level env names (BENTOO_LOG_LEVEL's value), else info.
// The flags are checked first, so an invalid env next to a flag is no error.
// An env value other than the four accepted words returns info and an error
// naming it; ResolveLevel itself never writes anything — the caller reports it.
func ResolveLevel(verbose, quiet bool, env string) (slog.Level, error) {
	switch {
	case quiet:
		return slog.LevelError, nil
	case verbose:
		return slog.LevelDebug, nil
	}
	name := strings.ToLower(strings.TrimSpace(env))
	if name == "" {
		return slog.LevelInfo, nil
	}
	if level, ok := levelNames[name]; ok {
		return level, nil
	}
	return slog.LevelInfo, fmt.Errorf("unknown log level %q: want one of debug, info, warn, error", env)
}

// FileLevel returns the log file's level for a terminal level: info, or debug
// when the terminal is debug, so --quiet and a raised BENTOO_LOG_LEVEL never
// remove info diagnostics from the file.
func FileLevel(terminal slog.Level) slog.Level {
	if terminal <= slog.LevelDebug {
		return slog.LevelDebug
	}
	return slog.LevelInfo
}
