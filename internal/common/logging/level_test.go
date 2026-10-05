package logging

// Authored for story 062, sub-task 1.3 (R2.4, R3.1–R3.5).
//
// Contract, from the sub-task objective:
//
//	func ResolveLevel(verbose, quiet bool, env string) (slog.Level, error)
//	func FileLevel(terminal slog.Level) slog.Level
//
// ResolveLevel gives the terminal level with the precedence
// --quiet > --verbose > BENTOO_LOG_LEVEL > info; FileLevel gives info, or debug
// when the terminal is debug.
//
// Uses only ResolveLevel and FileLevel, so it can be materialized after
// redact_test.go (1.2) and before logger_test.go (1.4).

import (
	"log/slog"
	"strings"
	"testing"
)

// TestResolveLevelPrecedence is R3.1, R3.2, R3.4 and R3.5 as one table.
func TestResolveLevelPrecedence(t *testing.T) {
	for _, tc := range []struct {
		name           string
		verbose, quiet bool
		env            string
		want           slog.Level
	}{
		// R3.2: nothing set.
		{"no flag, env unset", false, false, "", slog.LevelInfo},
		{"no flag, env whitespace only", false, false, "  \t ", slog.LevelInfo},
		// R3.1: the four words, any case, surrounding whitespace ignored.
		{"env debug", false, false, "debug", slog.LevelDebug},
		{"env INFO", false, false, "INFO", slog.LevelInfo},
		{"env Warn padded", false, false, "  Warn\t", slog.LevelWarn},
		{"env eRrOr", false, false, "eRrOr", slog.LevelError},
		// R3.4: --verbose beats the environment in both directions.
		{"verbose over env error", true, false, "error", slog.LevelDebug},
		{"verbose over env unset", true, false, "", slog.LevelDebug},
		// R3.5: --quiet beats everything.
		{"quiet over env debug", false, true, "debug", slog.LevelError},
		{"quiet over verbose", true, true, "", slog.LevelError},
		{"quiet over verbose and env debug", true, true, "debug", slog.LevelError},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ResolveLevel(tc.verbose, tc.quiet, tc.env)
			if err != nil {
				t.Fatalf("ResolveLevel(%v, %v, %q) returned error %v", tc.verbose, tc.quiet, tc.env, err)
			}
			if got != tc.want {
				t.Errorf("ResolveLevel(%v, %v, %q) = %v, want %v", tc.verbose, tc.quiet, tc.env, got, tc.want)
			}
		})
	}
}

// TestResolveLevelRejectsAnUnknownValue is R3.3. The first values are the
// hostile ones: slog.Level's own text syntax accepts "warn+2", "INFO-4" and
// "error+1", so a resolver built on Level.UnmarshalText would take them — the
// requirement accepts exactly four words.
func TestResolveLevelRejectsAnUnknownValue(t *testing.T) {
	for _, value := range []string{
		"warn+2", "INFO-4", "error+1", "debug-1",
		"warning", "trace", "verbose", "4", "-4", "debugx", "info,debug",
	} {
		t.Run(value, func(t *testing.T) {
			got, err := ResolveLevel(false, false, value)
			if err == nil {
				t.Fatalf("ResolveLevel(false, false, %q) = %v with no error, want an error naming the value", value, got)
			}
			if got != slog.LevelInfo {
				t.Errorf("ResolveLevel(false, false, %q) level = %v, want info alongside the error", value, got)
			}
			msg := err.Error()
			if !strings.Contains(msg, value) {
				t.Errorf("the error does not name the value %q: %q", value, msg)
			}
			lower := strings.ToLower(msg)
			for _, accepted := range []string{"debug", "info", "warn", "error"} {
				if !strings.Contains(lower, accepted) {
					t.Errorf("the error does not name the accepted value %q: %q", accepted, msg)
				}
			}
		})
	}
}

// TestResolveLevelFlagMakesAnInvalidEnvIrrelevant: with --verbose or --quiet
// given, BENTOO_LOG_LEVEL is not consulted, so a bad value in it is not an
// error (R3.4, R3.5 — "whatever BENTOO_LOG_LEVEL holds").
func TestResolveLevelFlagMakesAnInvalidEnvIrrelevant(t *testing.T) {
	for _, tc := range []struct {
		name           string
		verbose, quiet bool
		want           slog.Level
	}{
		{"verbose", true, false, slog.LevelDebug},
		{"quiet", false, true, slog.LevelError},
		{"both", true, true, slog.LevelError},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ResolveLevel(tc.verbose, tc.quiet, "loud")
			if err != nil {
				t.Errorf("ResolveLevel(%v, %v, \"loud\") returned %v; a flag makes the variable irrelevant", tc.verbose, tc.quiet, err)
			}
			if got != tc.want {
				t.Errorf("ResolveLevel(%v, %v, \"loud\") = %v, want %v", tc.verbose, tc.quiet, got, tc.want)
			}
		})
	}
}

// TestFileLevel is R2.4. The hostile rows are warn and error: a file level that
// followed the terminal would drop info diagnostics from the file exactly when
// --quiet or a raised BENTOO_LOG_LEVEL is in use.
func TestFileLevel(t *testing.T) {
	for _, tc := range []struct {
		terminal, want slog.Level
	}{
		{slog.LevelError, slog.LevelInfo},
		{slog.LevelWarn, slog.LevelInfo},
		{slog.LevelInfo, slog.LevelInfo},
		{slog.LevelDebug, slog.LevelDebug},
	} {
		if got := FileLevel(tc.terminal); got != tc.want {
			t.Errorf("FileLevel(%v) = %v, want %v", tc.terminal, got, tc.want)
		}
	}
}
