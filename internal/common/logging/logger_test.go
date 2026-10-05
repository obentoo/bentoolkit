package logging

// Authored for story 062, sub-task 1.4 (R1.1–R1.3, R2.1–R2.5, R4.1, R5.1, R5.3).
//
// Contract, from the sub-task objective:
//
//	New(Options{Stderr, LogDir, Level, Resolved}) (*slog.Logger, func() error, error)
//
// builds the invocation logger — a slog.TextHandler on Stderr at Level with the
// top-level time attribute removed, plus, when LogDir != "", a slog.JSONHandler
// appending to LogDir/bentoo.log at FileLevel(Level), both behind one
// NewRedactingHandler; plus DefaultLogDir(), NewContext, FromContext (a
// discarding logger when none is stored) and OrDiscard.
//
// Standard scale, no design.md: where the objective leaves a signature open
// (DefaultLogDir's return shape, the Stderr field's type) the tests are written
// to accept either reasonable shape — Stderr is handed an *os.File, which
// satisfies both io.Writer and *os.File, and DefaultLogDir is called through
// reflection so both `func() string` and `func() (string, error)` pass.
//
// Isolation: every test sets HOME, XDG_STATE_HOME and XDG_CONFIG_HOME to temp
// dirs before anything opens a log file, and every LogDir is under t.TempDir().
// A run of this file must never create ~/.local/state/bentoo.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/obentoo/bentoolkit/internal/common/secrets"
)

// isolateLoggingEnv points every directory a logger could default to at temp
// dirs and returns the temp root.
func isolateLoggingEnv(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	t.Setenv("HOME", filepath.Join(root, "home"))
	t.Setenv("XDG_STATE_HOME", filepath.Join(root, "state"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(root, "config"))
	return root
}

// stderrFile is a real *os.File standing in for os.Stderr.
func stderrFile(t *testing.T) (*os.File, func() string) {
	t.Helper()
	f, err := os.CreateTemp(t.TempDir(), "stderr")
	if err != nil {
		t.Fatalf("creating the stderr stand-in: %v", err)
	}
	t.Cleanup(func() { _ = f.Close() })
	return f, func() string {
		data, err := os.ReadFile(f.Name())
		if err != nil {
			t.Fatalf("reading the stderr stand-in: %v", err)
		}
		return string(data)
	}
}

// buildLogger calls New and fails the test on an error.
func buildLogger(t *testing.T, opts Options) (*slog.Logger, func() error) {
	t.Helper()
	l, closeFn, err := New(opts)
	if err != nil {
		t.Fatalf("New(%+v) returned error %v", opts, err)
	}
	if l == nil {
		t.Fatal("New returned a nil logger with no error")
	}
	if closeFn == nil {
		t.Fatal("New returned a nil close func; callers must be able to close unconditionally")
	}
	return l, closeFn
}

func noSecrets() []string { return nil }

// fileRecords decodes every line of the log file; each must be one JSON object.
func fileRecords(t *testing.T, path string) []map[string]any {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading the log file %s: %v", path, err)
	}
	var recs []map[string]any
	for i, line := range strings.Split(strings.TrimRight(string(data), "\n"), "\n") {
		if line == "" {
			continue
		}
		var m map[string]any
		if err := json.Unmarshal([]byte(line), &m); err != nil {
			t.Fatalf("line %d of %s is not one JSON object: %v\n%s", i+1, path, err, line)
		}
		recs = append(recs, m)
	}
	return recs
}

func msgs(recs []map[string]any) []string {
	var out []string
	for _, r := range recs {
		out = append(out, fmt.Sprint(r["msg"]))
	}
	return out
}

func has(list []string, v string) bool {
	for _, x := range list {
		if x == v {
			return true
		}
	}
	return false
}

// withUmask pins the process umask for the test so file and directory modes
// are deterministic, restoring it afterwards.
func withUmask(t *testing.T, mask int) {
	t.Helper()
	old := syscall.Umask(mask)
	t.Cleanup(func() { syscall.Umask(old) })
}

// TestNewStderrLineIsSlogTextWithoutTime is R1.1 and R1.2: one line, slog text
// format, `level` and `msg` first, then the attributes, and no top-level
// `time`. The hostile half: an attribute named `time` inside a group is the
// diagnostic's own data and must survive the removal.
func TestNewStderrLineIsSlogTextWithoutTime(t *testing.T) {
	isolateLoggingEnv(t)
	stderr, read := stderrFile(t)
	l, closeFn := buildLogger(t, Options{Stderr: stderr, LogDir: "", Level: slog.LevelInfo, Resolved: noSecrets})

	l.Info("cache miss",
		slog.String("package", "app-misc/jq"),
		slog.Int("attempts", 3),
		slog.Group("build", slog.Duration("time", 3*time.Second)),
	)
	if err := closeFn(); err != nil {
		t.Fatalf("close: %v", err)
	}

	want := `level=INFO msg="cache miss" package=app-misc/jq attempts=3 build.time=3s` + "\n"
	if got := read(); got != want {
		t.Errorf("stderr =\n%q\nwant\n%q", got, want)
	}
}

// TestNewStderrHonoursTheTerminalLevel is R1.3 with its converse: below the
// level nothing, at and above it one line each.
func TestNewStderrHonoursTheTerminalLevel(t *testing.T) {
	isolateLoggingEnv(t)
	stderr, read := stderrFile(t)
	l, closeFn := buildLogger(t, Options{Stderr: stderr, LogDir: "", Level: slog.LevelWarn, Resolved: noSecrets})

	l.Debug("below_debug")
	l.Info("below_info")
	l.Warn("at_warn")
	l.Error("above_error")
	_ = closeFn()

	got := read()
	for _, absent := range []string{"below_debug", "below_info"} {
		if strings.Contains(got, absent) {
			t.Errorf("a diagnostic below the terminal level reached stderr: %q in\n%s", absent, got)
		}
	}
	for _, present := range []string{"level=WARN msg=at_warn", "level=ERROR msg=above_error"} {
		if !strings.Contains(got, present) {
			t.Errorf("stderr lacks %q:\n%s", present, got)
		}
	}
	if n := strings.Count(got, "\n"); n != 2 {
		t.Errorf("stderr holds %d lines, want 2:\n%s", n, got)
	}
}

// TestNewCreatesTheLogDirectoryAndFileWithPrivateModes is R2.3, with the umask
// pinned to 022 so a directory created 0755 or a file created 0644 would show.
func TestNewCreatesTheLogDirectoryAndFileWithPrivateModes(t *testing.T) {
	root := isolateLoggingEnv(t)
	withUmask(t, 0o022)
	stderr, _ := stderrFile(t)
	dir := filepath.Join(root, "fresh", "bentoo", "logs")

	l, closeFn := buildLogger(t, Options{Stderr: stderr, LogDir: dir, Level: slog.LevelInfo, Resolved: noSecrets})
	l.Info("created")
	if err := closeFn(); err != nil {
		t.Fatalf("close: %v", err)
	}

	di, err := os.Stat(dir)
	if err != nil {
		t.Fatalf("the log directory was not created: %v", err)
	}
	if got := di.Mode().Perm(); got != 0o750 {
		t.Errorf("log directory mode = %#o, want 0750", got)
	}
	fi, err := os.Stat(filepath.Join(dir, "bentoo.log"))
	if err != nil {
		t.Fatalf("bentoo.log was not created: %v", err)
	}
	if got := fi.Mode().Perm(); got != 0o600 {
		t.Errorf("bentoo.log mode = %#o, want 0600", got)
	}
}

// TestNewFileRecordsAreJSONLinesWithTimeLevelMsgFirst is R2.1 and R2.2: an
// existing file is appended to, never truncated; each record is one JSON line
// whose first keys are time (RFC 3339, sub-second), level and msg, followed by
// the attributes.
func TestNewFileRecordsAreJSONLinesWithTimeLevelMsgFirst(t *testing.T) {
	root := isolateLoggingEnv(t)
	stderr, _ := stderrFile(t)
	dir := filepath.Join(root, "logs")
	if err := os.MkdirAll(dir, 0o750); err != nil {
		t.Fatal(err)
	}
	logFile := filepath.Join(dir, "bentoo.log")
	earlier := `{"time":"2026-09-27T10:00:00.5Z","level":"INFO","msg":"from an earlier run"}` + "\n"
	if err := os.WriteFile(logFile, []byte(earlier), 0o600); err != nil {
		t.Fatal(err)
	}

	l, closeFn := buildLogger(t, Options{Stderr: stderr, LogDir: dir, Level: slog.LevelInfo, Resolved: noSecrets})
	l.Warn("upstream slow", slog.String("package", "dev-lang/go"), slog.Int("ms", 1234))
	if err := closeFn(); err != nil {
		t.Fatalf("close: %v", err)
	}

	data, err := os.ReadFile(logFile)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(string(data), earlier) {
		t.Fatalf("the earlier run's record is gone — the file was not opened for appending:\n%s", data)
	}
	lines := strings.Split(strings.TrimRight(string(data), "\n"), "\n")
	if len(lines) != 2 {
		t.Fatalf("got %d lines, want 2 (earlier + this run):\n%s", len(lines), data)
	}
	line := lines[1]
	var rec map[string]any
	if err := json.Unmarshal([]byte(line), &rec); err != nil {
		t.Fatalf("the record is not one JSON object: %v\n%s", err, line)
	}
	if rec["level"] != "WARN" || rec["msg"] != "upstream slow" || rec["package"] != "dev-lang/go" || rec["ms"] != float64(1234) {
		t.Errorf("record = %v, want level WARN, msg %q, package and ms attributes", rec, "upstream slow")
	}
	ts, _ := rec["time"].(string)
	if _, err := time.Parse(time.RFC3339Nano, ts); err != nil {
		t.Errorf("time %q is not RFC 3339: %v", ts, err)
	}
	if !strings.Contains(ts, ".") {
		t.Errorf("time %q carries no sub-second precision", ts)
	}
	order := []string{`"time":`, `"level":`, `"msg":`, `"package":`}
	last := -1
	for _, k := range order {
		i := strings.Index(line, k)
		if i <= last {
			t.Errorf("key order in the record is not time, level, msg, then attributes:\n%s", line)
			break
		}
		last = i
	}
}

// TestNewFileLevelIsIndependentOfTheTerminal is R2.1's "whatever the terminal
// level" and R2.4. Hostile first: at terminal error (--quiet) and warn, info
// still reaches the file while stderr stays quiet; at info, debug does not;
// at debug, debug does.
func TestNewFileLevelIsIndependentOfTheTerminal(t *testing.T) {
	for _, tc := range []struct {
		terminal  slog.Level
		wantFile  []string
		notInFile []string
	}{
		{slog.LevelError, []string{"probe_info", "probe_warn", "probe_error"}, []string{"probe_debug"}},
		{slog.LevelWarn, []string{"probe_info", "probe_warn", "probe_error"}, []string{"probe_debug"}},
		{slog.LevelInfo, []string{"probe_info", "probe_warn", "probe_error"}, []string{"probe_debug"}},
		{slog.LevelDebug, []string{"probe_debug", "probe_info", "probe_warn", "probe_error"}, nil},
	} {
		t.Run(tc.terminal.String(), func(t *testing.T) {
			root := isolateLoggingEnv(t)
			stderr, read := stderrFile(t)
			dir := filepath.Join(root, "logs")
			l, closeFn := buildLogger(t, Options{Stderr: stderr, LogDir: dir, Level: tc.terminal, Resolved: noSecrets})
			l.Debug("probe_debug")
			l.Info("probe_info")
			l.Warn("probe_warn")
			l.Error("probe_error")
			if err := closeFn(); err != nil {
				t.Fatalf("close: %v", err)
			}

			got := msgs(fileRecords(t, filepath.Join(dir, "bentoo.log")))
			for _, m := range tc.wantFile {
				if !has(got, m) {
					t.Errorf("terminal %v: the file lacks %q (file has %v)", tc.terminal, m, got)
				}
			}
			for _, m := range tc.notInFile {
				if has(got, m) {
					t.Errorf("terminal %v: the file holds %q, below the file level", tc.terminal, m)
				}
			}
			if tc.terminal == slog.LevelError && strings.Contains(read(), "probe_info") {
				t.Errorf("terminal error: info reached stderr:\n%s", read())
			}
		})
	}
}

// TestNewWithoutALogDirWritesNoFile: LogDir "" means stderr only — no file is
// created anywhere, including the default location.
func TestNewWithoutALogDirWritesNoFile(t *testing.T) {
	root := isolateLoggingEnv(t)
	stderr, read := stderrFile(t)
	l, closeFn := buildLogger(t, Options{Stderr: stderr, LogDir: "", Level: slog.LevelInfo, Resolved: noSecrets})
	l.Info("stderr_only")
	if err := closeFn(); err != nil {
		t.Errorf("close with no file returned %v", err)
	}
	if !strings.Contains(read(), "stderr_only") {
		t.Errorf("stderr lacks the diagnostic:\n%s", read())
	}
	for _, p := range []string{filepath.Join(root, "state", "bentoo"), filepath.Join(root, "home", ".local", "state", "bentoo")} {
		if _, err := os.Stat(p); err == nil {
			t.Errorf("%s exists: an empty LogDir must not fall back to a default file", p)
		}
	}
}

// TestNewReportsAnUnavailableLogFile is R2.5 at the constructor: when the
// directory or file cannot be created or opened, New returns an error naming
// the path and the cause. If it also returns a logger, that logger still writes
// to stderr.
func TestNewReportsAnUnavailableLogFile(t *testing.T) {
	root := isolateLoggingEnv(t)

	blocker := filepath.Join(root, "not-a-dir")
	if err := os.WriteFile(blocker, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	readOnlyDir := filepath.Join(root, "ro-logs")
	if err := os.MkdirAll(readOnlyDir, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(readOnlyDir, "bentoo.log"), nil, 0o400); err != nil {
		t.Fatal(err)
	}

	for _, tc := range []struct {
		name, dir, cause string
	}{
		{"directory under a regular file", filepath.Join(blocker, "logs"), "not a directory"},
		{"file not writable", readOnlyDir, "permission denied"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			stderr, read := stderrFile(t)
			l, closeFn, err := New(Options{Stderr: stderr, LogDir: tc.dir, Level: slog.LevelInfo, Resolved: noSecrets})
			if err == nil {
				t.Fatalf("New with LogDir %q returned no error", tc.dir)
			}
			if !strings.Contains(err.Error(), tc.dir) {
				t.Errorf("the error does not name the path %q: %v", tc.dir, err)
			}
			if !strings.Contains(err.Error(), tc.cause) {
				t.Errorf("the error does not carry the cause %q: %v", tc.cause, err)
			}
			if l != nil {
				l.Warn("still_on_stderr")
				if closeFn != nil {
					_ = closeFn()
				}
				if !strings.Contains(read(), "still_on_stderr") {
					t.Errorf("the logger New returned alongside the error does not write to stderr:\n%s", read())
				}
			}
		})
	}
}

// TestNewRedactsAResolvedSecretInBothSinks is R4.1 end to end, and the story's
// success metric: a secret resolved through secrets.Lookup — even AFTER the
// logger was built — is `***` in the message, a string attribute, an error
// attribute and a grouped attribute, on stderr and in the file.
func TestNewRedactsAResolvedSecretInBothSinks(t *testing.T) {
	root := isolateLoggingEnv(t)
	stderr, read := stderrFile(t)
	dir := filepath.Join(root, "logs")

	l, closeFn := buildLogger(t, Options{Stderr: stderr, LogDir: dir, Level: slog.LevelInfo, Resolved: secrets.Resolved})

	const name = "S062_LOGGER_TEST_FAKE_TOKEN"
	secret := fmt.Sprintf("s062fakeTok%d", time.Now().UnixNano())
	t.Setenv(name, secret)
	if got, found, err := secrets.Lookup(name); err != nil || !found || got != secret {
		t.Fatalf("secrets.Lookup(%q) = (%q, %v, %v)", name, got, found, err)
	}

	l.Warn("request to "+secret+" failed",
		slog.String("url", "https://example.invalid/"+secret),
		slog.Any("err", errors.New("echoed "+secret)),
		slog.Group("req", slog.String("header", "Bearer "+secret)),
	)
	if err := closeFn(); err != nil {
		t.Fatalf("close: %v", err)
	}

	text := read()
	file, err := os.ReadFile(filepath.Join(dir, "bentoo.log"))
	if err != nil {
		t.Fatal(err)
	}
	for sink, out := range map[string]string{"stderr": text, "file": string(file)} {
		if strings.Contains(out, secret) {
			t.Errorf("%s carries the resolved secret:\n%s", sink, out)
		}
		if n := strings.Count(out, "***"); n < 4 {
			t.Errorf("%s holds %d `***`, want one in each of the 4 positions:\n%s", sink, n, out)
		}
	}
	recs := fileRecords(t, filepath.Join(dir, "bentoo.log"))
	if len(recs) != 1 {
		t.Fatalf("file holds %d records, want 1", len(recs))
	}
	rec := recs[0]
	req, _ := rec["req"].(map[string]any)
	if rec["msg"] != "request to *** failed" || rec["url"] != "https://example.invalid/***" || rec["err"] != "echoed ***" || req["header"] != "Bearer ***" {
		t.Errorf("file record = %v, want `***` in msg, url, err and req.header", rec)
	}
}

// TestNewIsSafeForConcurrentUse: many goroutines on one logger (under -race);
// every file line is still exactly one JSON object.
func TestNewIsSafeForConcurrentUse(t *testing.T) {
	root := isolateLoggingEnv(t)
	stderr, _ := stderrFile(t)
	dir := filepath.Join(root, "logs")
	l, closeFn := buildLogger(t, Options{Stderr: stderr, LogDir: dir, Level: slog.LevelInfo, Resolved: noSecrets})

	var wg sync.WaitGroup
	for i := range 64 {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			l.Info("concurrent", slog.Int("i", i), slog.String("pad", strings.Repeat("x", 512)))
		}(i)
	}
	wg.Wait()
	if err := closeFn(); err != nil {
		t.Fatalf("close: %v", err)
	}
	if n := len(fileRecords(t, filepath.Join(dir, "bentoo.log"))); n != 64 {
		t.Errorf("file holds %d records, want 64", n)
	}
}

// defaultLogDir calls DefaultLogDir whether it is `func() string` or
// `func() (string, error)`; the objective names it without fixing its shape.
func defaultLogDir(t *testing.T) string {
	t.Helper()
	out := reflect.ValueOf(DefaultLogDir).Call(nil)
	if len(out) == 0 || out[0].Kind() != reflect.String {
		t.Fatalf("DefaultLogDir returns %d values; want a string first", len(out))
	}
	if len(out) == 2 && !out[1].IsNil() {
		t.Fatalf("DefaultLogDir returned error %v", out[1].Interface())
	}
	return out[0].String()
}

// TestDefaultLogDirFollowsXDGStateHome is R2.1's location rule: set →
// $XDG_STATE_HOME/bentoo/logs; set but empty, or unset → ~/.local/state/bentoo/logs.
func TestDefaultLogDirFollowsXDGStateHome(t *testing.T) {
	root := isolateLoggingEnv(t)
	home := filepath.Join(root, "home")

	t.Run("set", func(t *testing.T) {
		t.Setenv("XDG_STATE_HOME", filepath.Join(root, "xdg-state"))
		if got, want := defaultLogDir(t), filepath.Join(root, "xdg-state", "bentoo", "logs"); got != want {
			t.Errorf("DefaultLogDir() = %q, want %q", got, want)
		}
	})
	t.Run("empty", func(t *testing.T) {
		t.Setenv("XDG_STATE_HOME", "")
		if got, want := defaultLogDir(t), filepath.Join(home, ".local", "state", "bentoo", "logs"); got != want {
			t.Errorf("DefaultLogDir() = %q, want %q", got, want)
		}
	})
	t.Run("unset", func(t *testing.T) {
		t.Setenv("XDG_STATE_HOME", "placeholder")
		if err := os.Unsetenv("XDG_STATE_HOME"); err != nil {
			t.Fatal(err)
		}
		if got, want := defaultLogDir(t), filepath.Join(home, ".local", "state", "bentoo", "logs"); got != want {
			t.Errorf("DefaultLogDir() = %q, want %q", got, want)
		}
	})
}

// TestContextCarriesTheInvocationLogger is R5.1's context half and R5.3's
// fallback: FromContext returns exactly the logger NewContext stored, and a
// logger that discards — never slog.Default(), which would write to stderr —
// when none was stored.
func TestContextCarriesTheInvocationLogger(t *testing.T) {
	isolateLoggingEnv(t)
	stderr, _ := stderrFile(t)
	l, closeFn := buildLogger(t, Options{Stderr: stderr, LogDir: "", Level: slog.LevelInfo, Resolved: noSecrets})
	defer func() { _ = closeFn() }()

	ctx := NewContext(context.Background(), l)
	if got := FromContext(ctx); got != l {
		t.Errorf("FromContext(NewContext(ctx, l)) = %p, want the stored logger %p", got, l)
	}

	empty := FromContext(context.Background())
	assertDiscards(t, "FromContext(context.Background())", empty)
}

// TestOrDiscardReturnsTheGivenLoggerOrADiscardingOne is R5.3: a component built
// without a logger discards its diagnostics.
func TestOrDiscardReturnsTheGivenLoggerOrADiscardingOne(t *testing.T) {
	l := slog.New(slog.NewTextHandler(&strings.Builder{}, nil))
	if got := OrDiscard(l); got != l {
		t.Errorf("OrDiscard(l) = %p, want l itself %p", got, l)
	}
	assertDiscards(t, "OrDiscard(nil)", OrDiscard(nil))
}

func assertDiscards(t *testing.T, what string, l *slog.Logger) {
	t.Helper()
	if l == nil {
		t.Fatalf("%s returned nil; callers must be able to log without a nil check", what)
	}
	if l.Handler() == slog.Default().Handler() {
		t.Errorf("%s returned slog.Default(), which writes to stderr, not a discarding logger", what)
	}
	for _, lvl := range []slog.Level{slog.LevelDebug, slog.LevelInfo, slog.LevelWarn, slog.LevelError} {
		if l.Enabled(context.Background(), lvl) {
			t.Errorf("%s is enabled at %v; it must discard everything", what, lvl)
		}
	}
	l.Error("must_not_panic")
}
