package main

// Authored for story 046, sub-task 4.2 — R8.4.
//
// Written from the contract: 4.2's objective — "A test can run one `bentoo`
// invocation end to end and read back what the operator would have seen" — and
// the harness contract fixed with the task list:
//
//	type testCLI struct{ ... }
//	func newTestCLI(t *testing.T) *testCLI
//	func (c *testCLI) Run(args ...string) (stdout, stderr string, code int)
//
// newTestCLI points config resolution at a t.TempDir() HOME and a temporary
// overlay, forces a non-TTY, and returns the exit status instead of calling
// os.Exit. Two Run calls must not share flag state.
//
// # One name added to that contract, and why
//
// func (c *testCLI) Overlay() string — the path of the temporary overlay.
//
// Every integration test in this story has to ARRANGE the run it observes:
// seed two packages so `--all` has something to hide, assert that a rejected
// `--ui` created no file, read back what a manifest run wrote. A harness that
// points config at an overlay the test cannot reach can only observe runs over
// an empty tree, and a report with no units cannot prove that a flag which
// changes which units are shown was honoured. It is the smallest affordance
// that makes the other six files possible.
//
// # This file is where the harness itself lives
//
// The tests below are its specification; the testCLI type and newTestCLI are
// added to this same file when 4.2 is implemented. That is the ordinary Go
// shape — a test helper beside the tests that prove it — and it is why this
// file is materialized BEFORE the harness is written rather than over it.
//
// Red on arrival: newTestCLI does not exist.

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"syscall"
	"testing"

	"github.com/fatih/color"
	"github.com/obentoo/bentoolkit/internal/autoupdate"
	"github.com/obentoo/bentoolkit/internal/common/distfiles"
	"github.com/spf13/cobra"
)

// TestTestCLIRunReturnsStdoutStderrAndStatus is the harness's own first
// promise. `version` is the probe: it is the one command that touches no
// config, no overlay and no network, so a failure here is the harness and
// never the command.
func TestTestCLIRunReturnsStdoutStderrAndStatus(t *testing.T) {
	c := newTestCLI(t)

	stdout, stderr, code := c.Run("version")

	if code != 0 {
		t.Errorf("`bentoo version` exited %d, want 0 (stderr: %q)", code, stderr)
	}
	if strings.TrimSpace(stdout) == "" {
		t.Error("the harness captured no stdout from `bentoo version` — a run whose output cannot be read back proves nothing about what the operator would have seen")
	}
	if strings.TrimSpace(stderr) != "" {
		t.Errorf("`bentoo version` wrote to stderr: %q", stderr)
	}
}

// TestTestCLIRejectedFlagExitsNonZeroOnStderr pins the other half: a run that
// fails must be distinguishable from one that succeeded, and the diagnostic
// must arrive on stderr where the operator sees it.
//
// stdout is asserted EMPTY of the command's own output. A harness that reported
// a non-zero status while the command had already run would make every "no work
// was done" assertion in this story unfalsifiable.
func TestTestCLIRejectedFlagExitsNonZeroOnStderr(t *testing.T) {
	c := newTestCLI(t)

	success, _, _ := c.Run("version")
	stdout, stderr, code := c.Run("version", "--not-a-real-flag")

	if code == 0 {
		t.Error("a rejected flag exited 0 — the harness cannot tell a failed run from a successful one")
	}
	if !strings.Contains(stderr, "not-a-real-flag") {
		t.Errorf("stderr does not name the rejected flag: %q", stderr)
	}
	if strings.TrimSpace(success) != "" && strings.Contains(stdout, strings.TrimSpace(success)) {
		t.Errorf("the command ran anyway: stdout carries the version output after the flag was rejected\n%q", stdout)
	}
}

// TestTestCLITwoRunsDoNotShareFlagState is the requirement the whole harness
// turns on, and --help is the probe that makes it observable without depending
// on what any command does with a flag.
//
// cobra's `help` flag is an ordinary bool on the command. On a tree reused
// between runs it stays set, and the SECOND run prints usage instead of doing
// its work — a leak that turns every later assertion into a reading of the help
// text. A fresh tree per run is what stops it.
func TestTestCLITwoRunsDoNotShareFlagState(t *testing.T) {
	c := newTestCLI(t)

	helpOut, _, helpCode := c.Run("overlay", "manifest", "--help")
	if helpCode != 0 || !strings.Contains(helpOut, "manifest") {
		t.Fatalf("`overlay manifest --help` did not print usage (code %d):\n%s", helpCode, helpOut)
	}

	versionOut, _, versionCode := c.Run("version")
	if versionCode != 0 {
		t.Fatalf("`version` after a --help run exited %d", versionCode)
	}
	if strings.Contains(versionOut, "Usage:") {
		t.Errorf("the second run printed usage — the --help flag from the first run survived into it, so every run after a --help run reads back the wrong thing\n%s", versionOut)
	}

	fresh, _, _ := newTestCLI(t).Run("version")
	if strings.TrimSpace(versionOut) != strings.TrimSpace(fresh) {
		t.Errorf("a run after --help differs from the same run on a fresh harness — state crossed between runs\n--- after --help ---\n%s\n--- fresh ---\n%s", versionOut, fresh)
	}
}

// TestTestCLIReadsTheTempHome pins the isolation. Without it the suite reads
// the developer's own ~/.config/bentoo — so it passes on the machine that wrote
// it, fails in CI, and quietly runs commands against a real overlay.
//
// The config path and shape are this repository's own, established by
// setupTestHome in run_functions_test.go: $HOME/.config/bentoo/config.yaml with
// overlay.path naming the overlay.
func TestTestCLIReadsTheTempHome(t *testing.T) {
	c := newTestCLI(t)

	home := os.Getenv("HOME")
	if home == "" {
		t.Fatal("HOME is unset while the harness is alive")
	}
	if !strings.HasPrefix(home, os.TempDir()) {
		t.Errorf("HOME is %q, which is not under %q — config resolution is reading the developer's real home", home, os.TempDir())
	}

	configPath := filepath.Join(home, ".config", "bentoo", "config.yaml")
	data, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("the harness wrote no config at %s: %v", configPath, err)
	}

	overlay := c.Overlay()
	if overlay == "" {
		t.Fatal("the harness exposes no overlay path — a test cannot arrange the run it wants to observe")
	}
	if !strings.Contains(string(data), overlay) {
		t.Errorf("the config does not name the harness overlay %q:\n%s", overlay, data)
	}
	if info, err := os.Stat(overlay); err != nil || !info.IsDir() {
		t.Errorf("the overlay path %q is not a directory: %v", overlay, err)
	}
}

// TestTestCLIRunsWithoutATerminal pins the last clause of the contract. Mode
// resolution reads whether stdout is a terminal, and under `go test` it is a
// pipe — but the harness must force the answer rather than inherit it, or the
// suite renders one way on a developer's terminal and another in CI, and every
// output assertion in this story becomes environment-dependent.
func TestTestCLIRunsWithoutATerminal(t *testing.T) {
	stdout, _, _ := newTestCLI(t).Run("version")

	if strings.ContainsRune(stdout, 0x1b) {
		t.Errorf("the captured output carries an escape sequence — the harness did not force a non-TTY, so what a test reads back depends on where it ran\n%q", stdout)
	}
}

// ---------------------------------------------------------------------------
// The harness itself. Added by sub-task 4.2, in this file, for the reason the
// header gives: a test helper lives beside the tests that prove it.
// ---------------------------------------------------------------------------

// testCLI runs one `bentoo` invocation end to end and hands back what the
// operator would have seen (R8.4).
//
// # It is deliberately not parallel-safe, and that is enforced rather than asked
//
// Isolation is per-process state — HOME, XDG_CONFIG_HOME, the render opt-outs —
// so two harnesses alive at once would read each other's configuration. Every
// one of them is set with t.Setenv, which panics if the test has called
// t.Parallel(). A rule the compiler cannot check is a rule someone eventually
// breaks; this one fails loudly on the first attempt instead.
type testCLI struct {
	t       *testing.T
	home    string
	overlay string
}

// newTestCLI prepares an isolated home, a real overlay directory and a config
// that names it, then forces the render mode's inputs so a run reads the same
// on a developer's terminal as in CI.
//
// Every failure below is a t.Fatal naming what could not be prepared. A harness
// that half-configures itself produces green tests that assert nothing, which is
// worse than a red one.
func newTestCLI(t *testing.T) *testCLI {
	t.Helper()

	home := t.TempDir()

	configDir := filepath.Join(home, ".config", "bentoo")
	if err := os.MkdirAll(configDir, 0o755); err != nil {
		t.Fatalf("newTestCLI: creating the config directory %s: %v", configDir, err)
	}

	// profiles/ and metadata/ are what makes the directory recognisable as an
	// overlay rather than an empty temp dir; setupTestHome established the shape.
	overlay := filepath.Join(home, "overlay")
	for _, sub := range []string{"profiles", "metadata"} {
		if err := os.MkdirAll(filepath.Join(overlay, sub), 0o755); err != nil {
			t.Fatalf("newTestCLI: creating the overlay subdirectory %s: %v", filepath.Join(overlay, sub), err)
		}
	}

	config := "overlay:\n  path: " + overlay + "\n  remote: origin\n" +
		"git:\n  user: Test\n  email: test@test.com\n"
	configPath := filepath.Join(configDir, "config.yaml")
	if err := os.WriteFile(configPath, []byte(config), 0o644); err != nil {
		t.Fatalf("newTestCLI: writing the config at %s: %v", configPath, err)
	}

	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))

	// Forced, not inherited. Under `go test` stdout is already a pipe, so the
	// answer would come out right by accident — and would come out differently
	// the day someone runs the suite under a pty. tui.Enabled reads both of
	// these, and either one alone is enough to mean "no TUI".
	t.Setenv("BENTOO_NO_TUI", "1")
	t.Setenv("NO_COLOR", "1")
	// Unset rather than empty-and-meaningful: per the convention ResolveMode
	// follows, an empty BENTOO_UI means "not set", so a stale value from the
	// developer's shell cannot reach the run.
	t.Setenv("BENTOO_UI", "")

	restoreReportFlags(t)

	return &testCLI{t: t, home: home, overlay: overlay}
}

// restoreReportFlags puts the report flags back the way it found them when the
// test ends, the way t.Setenv does for the environment above. Every other
// flag of `overlay autoupdate` binds a field of the autoupdateOptions its own
// tree allocates (story 060, R5.1), so no Run can leak one into a later test
// and there is nothing else to restore.
//
// # The flags are PACKAGE variables, and that is deliberate rather than an
// # oversight
//
// newRootCmd binds --verbose, --quiet and --no-color to locals and publishes
// them in PersistentPreRun, but binds --ui, --all and --export straight to the
// package variables of the same name — see the comment there for why (a publish
// line naming autoupdateAll would put that identifier outside a renderer
// Options, which TestAllDoesNotChangeActions forbids). newAutoupdateCmd
// binds every one of its own flags but --depth the same way. pflag writes the
// DEFAULT through that pointer at construction, so building a tree resets them
// all, and a harness that builds one per Run cannot leak flag state INTO the
// next run.
//
// # What it can leak into is a test that never builds a tree
//
// manifestUsesTUI and resolveAutoupdateUIMode read autoupdateUI directly, so a
// test calling autoupdateUsesTUI or manifestUsesTUI with no CLI at all reads
// whatever the last `Run(..., "--ui=plain")` in the package left behind. That
// makes such a test's result depend on which file sorts before it, which is not
// a property a suite may have: the two live-region guards in
// overlay_manifest_test.go passed for a year and turned red the day a new file
// landed between them and the previous harness user, having changed neither
// their assertions nor the code under them.
//
// The same hazard reopened once the harness ran handlers in-process through
// runMain (story 058). TestRunAutoupdate_SignalCancels calls runAutoupdate on
// the package-level autoupdate command variable (removed by story 060) and pins only --check, --force and
// --concurrency; run under -shuffle after the `--lint --fix --yes` row of
// TestS058AutoupdateRegistryModesKeepTheirExitCodes, it inherited
// autoupdateLint = true, took the lint path instead of the check, and failed
// "upstream check never started" (seed 1790717970495027171).
//
// Restoring on cleanup closes it at the source. It is not a fix for one
// ordering — it makes every ordering equivalent, which is the only version of
// this that stays true as files are added.
func restoreReportFlags(t *testing.T) {
	t.Helper()
	restoreFlagGlobals(t, reportFlagGlobals()...)
}

// reportFlagGlobals is every package variable restoreReportFlags puts back:
// the three report flags newRootCmd declares on the root. --no-tui, which
// outranks --ui, is no longer one of them: it is a field of autoupdateOptions
// and reaches resolveAutoupdateUIMode as a parameter (story 060).
func reportFlagGlobals() []any {
	return []any{&autoupdateUI, &autoupdateAll, &autoupdateExport}
}

// testAutoupdateOptions returns an autoupdateOptions holding every flag's
// default, as a freshly built `overlay autoupdate` would bind it. A test that
// calls runAutoupdate or a mode method directly starts from it and sets only
// the fields it means to exercise.
func testAutoupdateOptions() *autoupdateOptions {
	return &autoupdateOptions{
		concurrency:    autoupdate.DefaultConcurrency,
		distfilesCache: distfiles.DefaultCache,
	}
}

// testAutoupdateRun wraps o in the run runAutoupdate would hand a mode, with
// every resolved value at its zero (nothing configured).
func testAutoupdateRun(o *autoupdateOptions) *autoupdateRun {
	return &autoupdateRun{opts: o}
}

// restoreFlagGlobals snapshots the variable behind each pointer now and writes
// the snapshot back on cleanup. A slice is copied, not aliased: pflag's
// StringSlice appends to the slice it holds on a repeated flag, which could
// write into the backing array of a snapshot that merely shared it.
//
// Every argument must be a non-nil pointer; anything else is a harness defect
// and fails the test that asked for it rather than restoring nothing.
func restoreFlagGlobals(t *testing.T, ptrs ...any) {
	t.Helper()

	type saved struct{ target, value reflect.Value }
	snapshot := make([]saved, 0, len(ptrs))
	for _, p := range ptrs {
		ptr := reflect.ValueOf(p)
		if ptr.Kind() != reflect.Pointer || ptr.IsNil() {
			t.Fatalf("restoreFlagGlobals: %T is not a non-nil pointer to a flag variable", p)
		}
		target := ptr.Elem()
		value := reflect.New(target.Type()).Elem()
		if target.Kind() == reflect.Slice && !target.IsNil() {
			clone := reflect.MakeSlice(target.Type(), target.Len(), target.Len())
			reflect.Copy(clone, target)
			value.Set(clone)
		} else {
			value.Set(target)
		}
		snapshot = append(snapshot, saved{target: target, value: value})
	}
	t.Cleanup(func() {
		for _, s := range snapshot {
			s.target.Set(s.value)
		}
	})
}

// Overlay is the path of the temporary overlay, so a test can arrange the run
// it wants to observe: seed two packages so --all has something to hide, assert
// a rejected flag created no file, read back what a manifest run wrote.
func (c *testCLI) Overlay() string { return c.overlay }

// Home is the temporary HOME, for the same reason Overlay exists.
func (c *testCLI) Home() string { return c.home }

// Run executes one invocation and returns what reached the two streams plus the
// status the process would have exited with.
//
// # A fresh tree per call, not per harness
//
// cobra stores flag state on the command, and its own `help` flag is an
// ordinary bool: a tree reused between runs prints usage on the second run
// instead of doing its work. newRootCmd (sub-task 4.1) is what makes a fresh,
// fully-wired tree cheap enough to build per call.
//
// # The status is returned, never exited
//
// The run goes through runMain, which returns the code func main would pass to
// exitProcess, so a failing run is an assertion rather than a dead test binary.
// A code of 0 means the command succeeded.
func (c *testCLI) Run(args ...string) (stdout, stderr string, code int) {
	c.t.Helper()

	readOut := captureStream(c.t, 1, &os.Stdout)
	readErr := captureStream(c.t, 2, &os.Stderr)

	// fatih/color caches its writer at package init, so redirecting the file
	// descriptor is not enough to stop it reaching the real terminal.
	origColorOut, origNoColor := color.Output, color.NoColor
	color.Output, color.NoColor = os.Stdout, true

	code = func() int {
		defer func() { color.Output, color.NoColor = origColorOut, origNoColor }()

		cmd := newRootCmd()
		cmd.SetArgs(args)
		cmd.SetOut(os.Stdout)
		cmd.SetErr(os.Stderr)
		// The same entry point func main uses: one exit-code mapping, one
		// printer, one signal context for the harness and the binary.
		return runMain(cmd)
	}()

	return readOut(), readErr(), code
}

// captureStream redirects one file descriptor into a pipe and returns a
// function that restores it and yields everything written.
//
// # Why the file descriptor and not just the os.Stdout variable
//
// Two writers in this program cannot be reached by swapping the Go variable.
// internal/common/logger builds its default logger once, under a sync.Once, and
// captures os.Stderr at that moment with no exported way to change it — so a
// per-run variable swap would leave it writing into the FIRST run's pipe, which
// is closed by the second, and the writes would vanish silently. And commands
// like `overlay manifest` and `snapshot run` spawn subprocesses that inherit
// descriptors, not Go variables.
//
// Redirecting the descriptor catches every one of them, which is what "read
// back what the operator would have seen" has to mean.
//
// A goroutine drains the pipe concurrently: a pipe buffer is 64 KiB and a full
// report is allowed to be longer, so a run that filled it would otherwise
// deadlock against its own reader.
func captureStream(t *testing.T, fd int, std **os.File) func() string {
	t.Helper()

	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("captureStream: opening a pipe for fd %d: %v", fd, err)
	}
	saved, err := syscall.Dup(fd)
	if err != nil {
		t.Fatalf("captureStream: saving fd %d: %v", fd, err)
	}
	if err := syscall.Dup2(int(w.Fd()), fd); err != nil {
		t.Fatalf("captureStream: redirecting fd %d: %v", fd, err)
	}

	orig := *std
	*std = w

	drained := make(chan string, 1)
	go func() {
		var buf bytes.Buffer
		_, _ = io.Copy(&buf, r)
		drained <- buf.String()
	}()

	return func() string {
		// Restore the descriptor BEFORE closing w: until then fd and w.Fd()
		// both hold the pipe's write end, and the reader would never see EOF.
		if err := syscall.Dup2(saved, fd); err != nil {
			t.Fatalf("captureStream: restoring fd %d: %v", fd, err)
		}
		_ = syscall.Close(saved)
		*std = orig
		_ = w.Close()
		out := <-drained
		_ = r.Close()
		return out
	}
}

// testAutoupdateCmd builds a fresh tree and returns its `overlay autoupdate`
// command. It replaces the package-level command variable story 060 removed: a
// test that needs the *cobra.Command — to set a signal context on it, or to
// read its flags — gets one no other test shares.
func testAutoupdateCmd() *cobra.Command {
	return subCommand(subCommand(newRootCmd(), "overlay"), "autoupdate")
}
