package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/obentoo/bentoolkit/internal/autoupdate/llm"
)

// Authored for story 048, sub-task 3.3 — S048-R3.1, S048-R4.1, S048-R5.1.
//
// deps.newClaudeAsker (default newClaudeCodeAsker) is the ONE construction seam
// both review paths are built through, and it passes no timeout option at all —
// so every review this program has ever run has run on
// llm.DefaultClaudeCodeTimeout, whatever the operator configured.
// S048-R3.1 makes the configured budget the one in force, and Success Metric 5
// says that must be shown "end to end rather than at the getter".
//
// SO THIS GUARD MEASURES THE DEADLINE, NOT THE PLUMBING. It writes a real
// config file carrying `autoupdate.review.timeout`, runs the real command over
// a real (temporary) overlay, and puts a real `claude` on PATH that never
// answers — a script that execs sleep for ten minutes. Nothing here is stubbed:
// the value travels config file → loadAppContext → runCompare → the seam →
// ClaudeCodeClient, and what is asserted at the end is WALL-CLOCK TIME, the one
// observation a getter cannot fake. A run whose invocation is killed one second
// after it started was running under a one-second budget; nothing else it could
// have been reading produces that number.
//
// WHY TWO BUDGETS AND A DIFFERENCE BETWEEN THEM. A single case would pass
// against an implementation that hardcoded any small constant — two seconds
// satisfies "not the 120s default" and "at least most of the second I asked
// for" equally well. The two cases are therefore compared against EACH OTHER:
// the measured per-invocation deadline must GROW when the key grows, which is
// the property "the configured value is the one in force" actually means, and
// the property a constant of any size fails.
//
// WHY THE SCRIPT EXECS RATHER THAN CALLS. `sh -c 'sleep 600'` leaves sleep
// holding the CLI's stdout pipe after the shell is killed, and cmd.Wait would
// then block for the full ten minutes rather than for the budget. `exec` makes
// the process the deadline kills the one holding the pipe. The absolute path to
// sleep is resolved by the test, because PATH inside the fixture holds nothing
// but the fake `claude`.
//
// It lives in a file of its own so it can be materialised on its own; in Go a
// test sharing a file with another sub-task's test cannot be taken to Green
// without that sub-task's symbols existing too.

// budgetRun is one measured run: how long it took, how many CLI invocations it
// made, and what it exited with.
type budgetRun struct {
	elapsed time.Duration
	calls   int
	code    int
}

// runCompareUnderConfiguredBudget writes a world whose config carries
// `autoupdate.review.timeout: <seconds>`, puts a never-answering `claude` on
// PATH, runs the shipped command, and reports how long it took and how many
// invocations it made.
func runCompareUnderConfiguredBudget(t *testing.T, seconds int) budgetRun {
	t.Helper()

	home := t.TempDir()
	overlayPath := filepath.Join(home, "overlay")
	gentooPath := filepath.Join(home, "gentoo")
	for _, sub := range []string{"profiles", "metadata"} {
		if err := os.MkdirAll(filepath.Join(overlayPath, sub), 0o750); err != nil {
			t.Fatalf("mkdir overlay/%s: %v", sub, err)
		}
	}
	if err := os.MkdirAll(filepath.Join(gentooPath, "profiles"), 0o750); err != nil {
		t.Fatalf("mkdir gentoo/profiles: %v", err)
	}
	if err := os.WriteFile(filepath.Join(gentooPath, "profiles", "repo_name"), []byte("gentoo\n"), 0o600); err != nil {
		t.Fatalf("write repo_name: %v", err)
	}

	// One package, carried at the same version by both trees with different
	// content: an UNDECLARED DIVERGENCE, which is the only shape AnnotateReviews
	// submits to a model. One is deliberate — the cost of this guard is one
	// budget per invocation, and one invocation is enough to measure a deadline.
	realignWriteEbuild(t, overlayPath, "media-libs", "gst-plugins-qt6", "1.29.2", realignOursEbuild)
	realignWriteEbuild(t, gentooPath, "media-libs", "gst-plugins-qt6", "1.29.2", realignBaselineEbuild)

	configDir := filepath.Join(home, ".config", "bentoo")
	if err := os.MkdirAll(configDir, 0o750); err != nil {
		t.Fatalf("mkdir config: %v", err)
	}
	cfg := "overlay:\n  path: " + overlayPath + "\n  remote: origin\n" +
		"git:\n  user: Test\n  email: test@test.com\n" +
		// The key under test, in the unit S048-R3.3 chose (integer seconds) and
		// the block S048-R3.4 chose (nested, so probeConfig needs no entry).
		"autoupdate:\n  review:\n    timeout: " + fmt.Sprint(seconds) + "\n" +
		"repositories:\n" +
		"  gentoo:\n    provider: local\n    path: " + gentooPath + "\n"
	if err := os.WriteFile(filepath.Join(configDir, "config.yaml"), []byte(cfg), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))

	sleepBin, err := exec.LookPath("sleep")
	if err != nil {
		t.Skipf("no sleep(1) on this machine (%v); this guard measures a deadline by letting a CLI hang, and cannot without one", err)
	}

	binDir := t.TempDir()
	logPath := filepath.Join(home, "claude-calls.log")
	// Records the invocation, then hangs for far longer than any budget this
	// program could be running under — including the 120s default, which is what
	// makes "it died after one second" a statement about the configured value.
	script := "#!/bin/sh\nprintf 'call\\n' >> " + logPath + "\nexec " + sleepBin + " 600\n"
	if err := os.WriteFile(filepath.Join(binDir, "claude"), []byte(script), 0o700); err != nil {
		t.Fatalf("write fake claude: %v", err)
	}
	// PATH holds the fake and nothing else, so a real `claude` installed on the
	// machine running this suite can never be the one spawned.
	t.Setenv("PATH", binDir)
	if _, err := exec.LookPath("claude"); err != nil {
		t.Fatalf("the fake claude is not resolvable on PATH (%v); the run would build no reviewer and this guard would measure nothing", err)
	}

	realignFlags(t, false, false)

	start := time.Now()
	_, code := realignRun(t, nil)
	elapsed := time.Since(start)

	calls := 0
	if data, err := os.ReadFile(logPath); err == nil {
		calls = len(strings.Split(strings.TrimSpace(string(data)), "\n"))
	}
	return budgetRun{elapsed: elapsed, calls: calls, code: code}
}

// TestConfiguredReviewBudgetIsTheDeadlineInForce is S048-R3.1 and S048-R4.1
// measured at the only place they are observable: how long an invocation is
// allowed to run.
//
// _Requirements: S048-R3.1, S048-R4.1, S048-R5.1_
func TestConfiguredReviewBudgetIsTheDeadlineInForce(t *testing.T) {
	budgets := []int{1, 3}
	perCall := make([]time.Duration, len(budgets))

	for i, seconds := range budgets {
		t.Run(fmt.Sprintf("timeout: %d", seconds), func(t *testing.T) {
			run := runCompareUnderConfiguredBudget(t, seconds)

			// The vacuity guard. Every bound below is on elapsed/calls, and a run
			// that never reached the CLI would divide by zero — or, with a
			// forgiving bound, report a clean guard for a run that measured
			// nothing at all.
			if run.calls < 1 {
				t.Fatalf("the run spawned the `claude` CLI %d times, so no deadline was measured. This guard is about the "+
					"budget an invocation runs under, and a fixture that reaches no invocation proves nothing about it.", run.calls)
			}
			if run.code != 0 {
				t.Errorf("the run exited %d; a review that does not return is a warning and a report printed without "+
					"commentary, never a failed run", run.code)
			}

			want := time.Duration(seconds) * time.Second
			got := run.elapsed / time.Duration(run.calls)
			perCall[i] = got
			t.Logf("configured %s, %d invocation(s), %s of wall clock, %s per invocation", want, run.calls, run.elapsed.Round(time.Millisecond), got.Round(time.Millisecond))

			if got < time.Duration(float64(want)*0.6) {
				t.Errorf("an invocation configured with a %s budget was over after %s.\n"+
					"S048-R3.1 makes the configured value the budget in force; a deadline shorter than the operator asked "+
					"for kills reviews they paid for.", want, got.Round(time.Millisecond))
			}
			if got > want+10*time.Second {
				t.Errorf("an invocation configured with a %s budget ran for %s before it was killed.\n"+
					"The configured key did not reach the invocation: newClaudeAsker "+
					"(overlay_compare_review.go:64) is the one seam both review paths are built through, and until it "+
					"passes autoupdate.WithClaudeCodeTimeout every review runs on llm.DefaultClaudeCodeTimeout "+
					"(%s) no matter what the config says (S048-R3.1, S048-R4.1).", want, got.Round(time.Millisecond), llm.DefaultClaudeCodeTimeout)
			}
		})
	}

	// The hostile half of the pair above. Each case on its own is satisfied by an
	// implementation that ignores the key and hardcodes any small constant; only
	// the DIFFERENCE between them says the deadline follows the value. Success
	// Metric 5 asks for exactly this: "setting the key changes the budget an
	// invocation is given".
	if perCall[0] == 0 || perCall[1] == 0 {
		t.Fatal("one of the two cases measured nothing, so the two budgets cannot be told apart; the assertion below is " +
			"the one that distinguishes a configured budget from a constant, and it must not be skipped silently")
	}
	grew := perCall[1] - perCall[0]
	if grew < time.Second {
		t.Errorf("raising `autoupdate.review.timeout` from %ds to %ds moved the measured deadline by %s.\n"+
			"Each case alone is satisfied by a hardcoded constant of roughly the right size; only the growth shows the "+
			"budget is READ from the configuration. A deadline that does not move when the key moves is not the "+
			"operator's value in force (S048-R3.1).", budgets[0], budgets[1], grew.Round(time.Millisecond))
	}
}
