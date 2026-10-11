package main

// Authored for story 062, sub-task 2.1 (R1.1, R2.1, R2.3–R2.6, R3.1, R3.3–R3.7,
// R5.1).
//
// Contract, from the sub-task objective: PersistentPreRunE resolves the level,
// builds the logger with logging.New (stderr = the os.Stderr of that moment,
// LogDir from DefaultLogDir, Resolved: secrets.Resolved), stores it with
// cmd.SetContext(logging.NewContext(cmd.Context(), l)), reports an invalid
// level and an unavailable file as one WARN each, emits the R3.7 startup debug
// record, and closes the file through registerExitCleanup and on normal return.
//
// # How the tests observe the invocation logger
//
// A probe sub-command is added to a freshly built root. It logs one diagnostic
// at each level through logging.FromContext(cmd.Context()) — the only route a
// command has to the invocation's logger (R5.1) — and prints a line to stdout
// so "the command ran" is observable. The root's PersistentPreRunE runs for it
// exactly as for any real command.
//
// # Isolation
//
// Every test sets HOME (newTestCLI), XDG_CONFIG_HOME (newTestCLI) and
// XDG_STATE_HOME (here) to temp dirs, and clears BENTOO_LOG_LEVEL, before any
// command runs; the re-exec child gets the same through its environment.

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"

	"github.com/fatih/color"
	"github.com/obentoo/bentoolkit/internal/common/logging"
	"github.com/spf13/cobra"
)

const (
	s062ProbeName    = "s062-log-probe"
	s062ProbeRan     = "s062 probe ran"
	s062ProbeFailure = "s062 probe failed on purpose"
	// s062BogusLevel is an invalid BENTOO_LOG_LEVEL. It is deliberately not a
	// word a test or subtest name contains: t.TempDir() paths carry the test's
	// name and appear in the startup record's log_file, so a value that were
	// part of the name would be "found" in stderr by its own path.
	s062BogusLevel = "zq-bogus-lvl"
)

// s062Env isolates the run and returns the harness and the XDG_STATE_HOME it set.
func s062Env(t *testing.T) (*testCLI, string) {
	t.Helper()
	c := newTestCLI(t)
	state := filepath.Join(c.Home(), "state")
	t.Setenv("XDG_STATE_HOME", state)
	t.Setenv("BENTOO_LOG_LEVEL", "")
	return c, state
}

// s062LogFile is where R2.1 puts the file for a given state root.
func s062LogFile(state string) string {
	return filepath.Join(state, "bentoo", "logs", "bentoo.log")
}

// s062Probe logs one diagnostic per level through the context's logger. With
// fail it returns an error after logging, so the run ends with status 1.
func s062Probe(fail bool) *cobra.Command {
	return &cobra.Command{
		Use: s062ProbeName,
		RunE: func(cmd *cobra.Command, _ []string) error {
			l := logging.FromContext(cmd.Context())
			l.Debug("s062_probe_debug", "probe", "debug")
			l.Info("s062_probe_info", "probe", "info")
			l.Warn("s062_probe_warn", "probe", "warn")
			l.Error("s062_probe_error", "probe", "error")
			fmt.Fprintln(os.Stdout, s062ProbeRan)
			if fail {
				return errors.New(s062ProbeFailure)
			}
			return nil
		},
	}
}

// s062Execute runs one invocation of a fresh root tree — with extra added as a
// sub-command when non-nil — the way main does, and returns both streams and
// the status. It goes through runMain, as testCLI.Run does, which returns the
// code main would pass to exitProcess.
func s062Execute(t *testing.T, extra *cobra.Command, args ...string) (stdout, stderr string, code int) {
	t.Helper()

	readOut := captureStream(t, 1, &os.Stdout)
	readErr := captureStream(t, 2, &os.Stderr)

	origColorOut, origNoColor := color.Output, color.NoColor
	color.Output, color.NoColor = os.Stdout, true

	code = func() int {
		defer func() { color.Output, color.NoColor = origColorOut, origNoColor }()
		// The tree's own deps, narrowed here because this harness does not
		// build its tree through newTestCLI: no s062 test resolves a secret of
		// its own, so a snapshot taken at the run equals one taken at setup.
		d := defaultDeps()
		isolateResolvedSecrets(t, d)
		root := newRootCmdWith(d)
		if extra != nil {
			root.AddCommand(extra)
		}
		root.SetArgs(args)
		root.SetOut(os.Stdout)
		root.SetErr(os.Stderr)
		return runMain(root)
	}()
	return readOut(), readErr(), code
}

// s062LinesWith returns the stderr lines containing sub.
func s062LinesWith(stderr, sub string) []string {
	var out []string
	for _, line := range strings.Split(stderr, "\n") {
		if strings.Contains(line, sub) {
			out = append(out, line)
		}
	}
	return out
}

// s062Fields parses one slog text line into its key=value pairs.
func s062Fields(line string) map[string]string {
	out := map[string]string{}
	for {
		line = strings.TrimLeft(line, " ")
		eq := strings.IndexByte(line, '=')
		if eq < 0 {
			return out
		}
		key, rest := line[:eq], line[eq+1:]
		var val string
		if strings.HasPrefix(rest, `"`) {
			i := 1
			for i < len(rest) && rest[i] != '"' {
				if rest[i] == '\\' {
					i++
				}
				i++
			}
			end := min(i+1, len(rest))
			if v, err := strconv.Unquote(rest[:end]); err == nil {
				val = v
			} else {
				val = rest[:end]
			}
			rest = rest[end:]
		} else if sp := strings.IndexByte(rest, ' '); sp >= 0 {
			val, rest = rest[:sp], rest[sp:]
		} else {
			val, rest = rest, ""
		}
		out[key] = val
		line = rest
	}
}

// s062FileRecords decodes the log file; every line must be one JSON object.
func s062FileRecords(t *testing.T, path string) []map[string]any {
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

func s062FileMsgs(t *testing.T, path string) map[string]bool {
	t.Helper()
	out := map[string]bool{}
	for _, r := range s062FileRecords(t, path) {
		out[fmt.Sprint(r["msg"])] = true
	}
	return out
}

// TestLoggingWiringInvalidLevelWarnsOnceAndTheCommandRuns is R3.3: one WARN on
// stderr naming the value and the four accepted ones, terminal level info, and
// the command runs — on the probe and on a real command.
func TestLoggingWiringInvalidLevelWarnsOnceAndTheCommandRuns(t *testing.T) {
	s062Env(t)
	t.Setenv("BENTOO_LOG_LEVEL", s062BogusLevel)

	stdout, stderr, code := s062Execute(t, s062Probe(false), s062ProbeName)
	if code != 0 || !strings.Contains(stdout, s062ProbeRan) {
		t.Fatalf("the command did not run to success (code %d)\nstdout: %s\nstderr: %s", code, stdout, stderr)
	}
	warns := s062LinesWith(stderr, s062BogusLevel)
	if len(warns) != 1 {
		t.Fatalf("stderr names the invalid value on %d lines, want exactly 1:\n%s", len(warns), stderr)
	}
	if !strings.HasPrefix(warns[0], "level=WARN ") {
		t.Errorf("the invalid-level line is not a slog WARN line: %q", warns[0])
	}
	lower := strings.ToLower(warns[0])
	for _, accepted := range []string{"debug", "info", "warn", "error"} {
		if !strings.Contains(lower, accepted) {
			t.Errorf("the warning does not name the accepted value %q: %q", accepted, warns[0])
		}
	}
	if !strings.Contains(stderr, "msg=s062_probe_info") {
		t.Errorf("terminal level is not info: the probe's info diagnostic is missing\n%s", stderr)
	}
	if strings.Contains(stderr, "s062_probe_debug") {
		t.Errorf("terminal level is below info: the probe's debug diagnostic reached stderr\n%s", stderr)
	}

	_, stderr, code = s062Execute(t, nil, "version")
	if code != 0 {
		t.Errorf("`version` with an invalid BENTOO_LOG_LEVEL exited %d, want 0\n%s", code, stderr)
	}
	if n := len(s062LinesWith(stderr, s062BogusLevel)); n != 1 {
		t.Errorf("`version`: stderr names the invalid value on %d lines, want exactly 1:\n%s", n, stderr)
	}
}

// s062StartupRecord returns the one stderr line carrying log_level_source.
func s062StartupRecord(t *testing.T, stderr string) (map[string]string, int) {
	t.Helper()
	lines := s062LinesWith(stderr, "log_level_source=")
	if len(lines) != 1 {
		t.Fatalf("stderr carries %d startup records (lines with log_level_source=), want 1:\n%s", len(lines), stderr)
	}
	return s062Fields(lines[0]), strings.Index(stderr, lines[0])
}

// TestLoggingWiringEnvDebugEmitsTheStartupRecord is R3.1 and R3.7: the
// environment alone (any case, padded) raises the terminal level to debug, and
// one debug record naming the level, its source and the log file is emitted
// before the command runs, on stderr and in the file.
func TestLoggingWiringEnvDebugEmitsTheStartupRecord(t *testing.T) {
	_, state := s062Env(t)
	t.Setenv("BENTOO_LOG_LEVEL", " Debug ")

	_, stderr, code := s062Execute(t, s062Probe(false), s062ProbeName)
	if code != 0 {
		t.Fatalf("exit %d\n%s", code, stderr)
	}
	if !strings.Contains(stderr, "msg=s062_probe_debug") {
		t.Errorf("BENTOO_LOG_LEVEL=debug did not reach the terminal level: no debug line\n%s", stderr)
	}
	fields, at := s062StartupRecord(t, stderr)
	if fields["level"] != "DEBUG" {
		t.Errorf("startup record level = %q, want DEBUG", fields["level"])
	}
	if !strings.EqualFold(fields["log_level"], "debug") {
		t.Errorf("log_level = %q, want debug", fields["log_level"])
	}
	if fields["log_level_source"] != "env" {
		t.Errorf("log_level_source = %q, want env", fields["log_level_source"])
	}
	if want := s062LogFile(state); fields["log_file"] != want {
		t.Errorf("log_file = %q, want %q", fields["log_file"], want)
	}
	if probeAt := strings.Index(stderr, "s062_probe_"); probeAt >= 0 && at > probeAt {
		t.Errorf("the startup record came after the command started logging\n%s", stderr)
	}

	var inFile bool
	for _, r := range s062FileRecords(t, s062LogFile(state)) {
		if r["log_level_source"] == "env" {
			inFile = true
		}
	}
	if !inFile {
		t.Errorf("the startup record is not in the log file (file level is debug when the terminal is)")
	}
}

// TestLoggingWiringVerboseBeatsTheEnvironment is R3.4: --verbose gives debug
// whatever BENTOO_LOG_LEVEL holds — a lower level, or a value that would
// otherwise be warned about.
func TestLoggingWiringVerboseBeatsTheEnvironment(t *testing.T) {
	for _, tc := range []struct{ name, env string }{
		{"lower level in the environment", "error"},
		{"invalid value in the environment", s062BogusLevel},
	} {
		env := tc.env
		t.Run(tc.name, func(t *testing.T) {
			s062Env(t)
			t.Setenv("BENTOO_LOG_LEVEL", env)

			_, stderr, code := s062Execute(t, s062Probe(false), "--verbose", s062ProbeName)
			if code != 0 {
				t.Fatalf("exit %d\n%s", code, stderr)
			}
			if !strings.Contains(stderr, "msg=s062_probe_debug") {
				t.Errorf("--verbose with BENTOO_LOG_LEVEL=%s: no debug line\n%s", env, stderr)
			}
			fields, _ := s062StartupRecord(t, stderr)
			if fields["log_level_source"] != "flag" {
				t.Errorf("log_level_source = %q, want flag", fields["log_level_source"])
			}
			if env == s062BogusLevel && len(s062LinesWith(stderr, s062BogusLevel)) != 0 {
				t.Errorf("--verbose makes BENTOO_LOG_LEVEL irrelevant, yet its value was warned about\n%s", stderr)
			}
		})
	}
}

// TestLoggingWiringQuietBeatsTheEnvironment is R3.5 and R2.4: --quiet gives
// error whatever --verbose and BENTOO_LOG_LEVEL hold, and info still reaches the
// file.
func TestLoggingWiringQuietBeatsTheEnvironment(t *testing.T) {
	for _, args := range [][]string{
		{"--quiet", s062ProbeName},
		{"--quiet", "--verbose", s062ProbeName},
	} {
		t.Run(strings.Join(args[:len(args)-1], " "), func(t *testing.T) {
			_, state := s062Env(t)
			t.Setenv("BENTOO_LOG_LEVEL", "debug")

			_, stderr, code := s062Execute(t, s062Probe(false), args...)
			if code != 0 {
				t.Fatalf("exit %d\n%s", code, stderr)
			}
			if !strings.Contains(stderr, "msg=s062_probe_error") {
				t.Errorf("--quiet hid the error diagnostic\n%s", stderr)
			}
			for _, below := range []string{"s062_probe_warn", "s062_probe_info", "s062_probe_debug", "log_level_source="} {
				if strings.Contains(stderr, below) {
					t.Errorf("--quiet let %q reach stderr\n%s", below, stderr)
				}
			}
			msgs := s062FileMsgs(t, s062LogFile(state))
			if !msgs["s062_probe_info"] {
				t.Errorf("--quiet removed the info diagnostic from the file (file has %v)", msgs)
			}
			if msgs["s062_probe_debug"] {
				t.Errorf("the file holds a debug diagnostic at terminal level error")
			}
		})
	}
}

// TestLoggingWiringCreatesTheLogFileWithPrivateModes is R2.1 and R2.3 on a real
// command: `version` creates the directory 0750 and the file 0600 in each of
// the three XDG_STATE_HOME cases, and a logging command's records are JSON.
func TestLoggingWiringCreatesTheLogFileWithPrivateModes(t *testing.T) {
	old := syscall.Umask(0o022)
	t.Cleanup(func() { syscall.Umask(old) })

	for _, tc := range []string{"set", "empty", "unset"} {
		t.Run(tc, func(t *testing.T) {
			c, state := s062Env(t)
			switch tc {
			case "empty":
				t.Setenv("XDG_STATE_HOME", "")
				state = filepath.Join(c.Home(), ".local", "state")
			case "unset":
				if err := os.Unsetenv("XDG_STATE_HOME"); err != nil {
					t.Fatal(err)
				}
				state = filepath.Join(c.Home(), ".local", "state")
			}
			logFile := s062LogFile(state)

			if _, stderr, code := s062Execute(t, nil, "version"); code != 0 {
				t.Fatalf("`version` exited %d\n%s", code, stderr)
			}
			di, err := os.Stat(filepath.Dir(logFile))
			if err != nil {
				t.Fatalf("`version` did not create the log directory: %v", err)
			}
			if got := di.Mode().Perm(); got != 0o750 {
				t.Errorf("log directory mode = %#o, want 0750", got)
			}
			fi, err := os.Stat(logFile)
			if err != nil {
				t.Fatalf("`version` did not create %s: %v", logFile, err)
			}
			if got := fi.Mode().Perm(); got != 0o600 {
				t.Errorf("bentoo.log mode = %#o, want 0600", got)
			}

			if _, stderr, code := s062Execute(t, s062Probe(false), s062ProbeName); code != 0 {
				t.Fatalf("probe exited %d\n%s", code, stderr)
			}
			var found bool
			for _, r := range s062FileRecords(t, logFile) {
				if r["msg"] == "s062_probe_info" {
					found = true
					if r["level"] != "INFO" || r["time"] == nil || r["probe"] != "info" {
						t.Errorf("file record = %v, want time, level INFO, msg and the probe attribute", r)
					}
				}
			}
			if !found {
				t.Errorf("the probe's info diagnostic is not in %s", logFile)
			}
		})
	}
}

// TestLoggingWiringUnavailableFileWarnsAndTheCommandRuns is R2.5 and R3.7's
// empty log_file: one WARN naming the path and the cause, stderr diagnostics
// still flow, and the status is what it would have been with the file.
func TestLoggingWiringUnavailableFileWarnsAndTheCommandRuns(t *testing.T) {
	for _, fail := range []bool{false, true} {
		t.Run(fmt.Sprintf("probe fails=%v", fail), func(t *testing.T) {
			c, _ := s062Env(t)
			_, _, codeWithFile := s062Execute(t, s062Probe(fail), s062ProbeName)

			blocker := filepath.Join(c.Home(), "state-is-a-file")
			if err := os.WriteFile(blocker, []byte("x"), 0o600); err != nil {
				t.Fatal(err)
			}
			t.Setenv("XDG_STATE_HOME", blocker)

			stdout, stderr, code := s062Execute(t, s062Probe(fail), s062ProbeName)
			if code != codeWithFile {
				t.Errorf("exit %d with the file unavailable, %d with it available — the status must not change", code, codeWithFile)
			}
			if !strings.Contains(stdout, s062ProbeRan) {
				t.Errorf("the command did not run\nstdout: %s\nstderr: %s", stdout, stderr)
			}
			warns := s062LinesWith(stderr, blocker)
			if len(warns) != 1 {
				t.Fatalf("stderr names the unavailable path on %d lines, want exactly 1:\n%s", len(warns), stderr)
			}
			if !strings.HasPrefix(warns[0], "level=WARN ") {
				t.Errorf("the unavailable-file line is not a slog WARN line: %q", warns[0])
			}
			if !strings.Contains(warns[0], "not a directory") {
				t.Errorf("the warning does not carry the cause: %q", warns[0])
			}
			if !strings.Contains(stderr, "msg=s062_probe_info") {
				t.Errorf("stderr diagnostics stopped with the file unavailable\n%s", stderr)
			}
		})
	}

	t.Run("startup record names no file", func(t *testing.T) {
		c, _ := s062Env(t)
		blocker := filepath.Join(c.Home(), "state-is-a-file")
		if err := os.WriteFile(blocker, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
		t.Setenv("XDG_STATE_HOME", blocker)
		_, stderr, _ := s062Execute(t, s062Probe(false), "--verbose", s062ProbeName)
		fields, _ := s062StartupRecord(t, stderr)
		if v, ok := fields["log_file"]; !ok || v != "" {
			t.Errorf("log_file = %q (present %v), want present and empty when the file is unavailable", v, ok)
		}
	})
}

// TestLoggingWiringVerboseHelpNamesTheEnvironmentVariable is R3.6.
func TestLoggingWiringVerboseHelpNamesTheEnvironmentVariable(t *testing.T) {
	flag := newRootCmd().PersistentFlags().Lookup("verbose")
	if flag == nil {
		t.Fatal("the root has no --verbose flag")
	}
	usage := flag.Usage
	for _, want := range []string{"BENTOO_LOG_LEVEL", "debug", "info", "warn", "error", "quiet"} {
		if !strings.Contains(usage, want) {
			t.Errorf("--verbose help does not name %q: %q", want, usage)
		}
	}

	s062Env(t)
	stdout, _, _ := s062Execute(t, nil, "--help")
	if !strings.Contains(stdout, "BENTOO_LOG_LEVEL") {
		t.Errorf("`bentoo --help` does not show BENTOO_LOG_LEVEL:\n%s", stdout)
	}
}

const (
	s062ExitMarkerEnv = "BENTOO_TEST_S062_EXIT_MARKER"
	s062ExitCode      = 5
	s062ExitReturned  = 97
	s062ExitRecords   = 20
)

// TestHelperS062ProductionExitChild is not a test: in the child role it runs a
// probe that logs and then ends through the PRODUCTION exit, exitProcess,
// which runs the registered cleanups and skips deferred calls.
func TestHelperS062ProductionExitChild(t *testing.T) {
	marker := os.Getenv(s062ExitMarkerEnv)
	if marker == "" {
		t.Skip("child role only")
	}
	root := newRootCmd()
	root.AddCommand(&cobra.Command{
		Use: "s062-exit-probe",
		Run: func(cmd *cobra.Command, _ []string) {
			l := logging.FromContext(cmd.Context())
			for i := range s062ExitRecords {
				l.Info("s062_before_exit", "marker", marker, "i", i)
			}
			exitProcess(s062ExitCode)
		},
	})
	root.SetArgs([]string{"s062-exit-probe"})
	_ = root.Execute()
	os.Exit(s062ExitReturned)
}

// TestLoggingWiringFileHoldsWhatWasLoggedBeforeTheProductionExit is R2.6, in a
// re-exec'd child with exitProcess exactly as the binary ships it (an
// in-process run never reaches it, and so never shows what it skips).
func TestLoggingWiringFileHoldsWhatWasLoggedBeforeTheProductionExit(t *testing.T) {
	root := t.TempDir()
	state := filepath.Join(root, "state")
	marker := "s062-exit-" + strconv.Itoa(os.Getpid())

	cmd := exec.Command(os.Args[0], "-test.run=^TestHelperS062ProductionExitChild$", "-test.count=1")
	cmd.Env = append(os.Environ(),
		"HOME="+filepath.Join(root, "home"),
		"XDG_CONFIG_HOME="+filepath.Join(root, "config"),
		"XDG_STATE_HOME="+state,
		"BENTOO_LOG_LEVEL=",
		"BENTOO_NO_TUI=1",
		"NO_COLOR=1",
		"BENTOO_UI=",
		s062ExitMarkerEnv+"="+marker,
	)
	out, err := cmd.CombinedOutput()
	code := 0
	var exitErr *exec.ExitError
	switch {
	case err == nil:
	case errors.As(err, &exitErr):
		code = exitErr.ExitCode()
	default:
		t.Fatalf("running the child: %v", err)
	}
	if code == s062ExitReturned {
		t.Fatalf("the probe returned instead of ending through osExit; the exit path was not exercised:\n%s", out)
	}
	if code != s062ExitCode {
		t.Fatalf("the child exited %d, want %d:\n%s", code, s062ExitCode, out)
	}

	n := 0
	for _, r := range s062FileRecords(t, s062LogFile(state)) {
		if r["msg"] == "s062_before_exit" && r["marker"] == marker {
			n++
		}
	}
	if n != s062ExitRecords {
		t.Errorf("the log file holds %d of the %d diagnostics emitted before the production exit\nchild output:\n%s", n, s062ExitRecords, out)
	}
}
