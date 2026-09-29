package main

// Authored for story 046, sub-task 12.3 — R3.7, R3.6.
//
// The rule, R3.7: "IF the render mode is requested through the environment or
// the configuration with a value outside the accepted set, THEN THE SYSTEM
// SHALL render the report in plain and state the source, the value it refused
// and the mode it used instead."
//
// 12.1 pinned that rule on the two producers that grew a report in this story.
// This is the THIRD producer, and it fails the rule in a way neither of those
// two could: `overlay autoupdate --check` does not merely lose the sentence, it
// loses the run. Measured through the harness over a seeded overlay, on HEAD:
//
//	bentoo overlay autoupdate --check --force
//	  → exit 0, the full Version Check Results report on stdout, stderr empty
//	BENTOO_UI=bogus bentoo overlay autoupdate --check --force
//	  → exit 1, NO report at all, and stderr carrying only
//	    `BENTOO_UI: "bogus" is not a UI mode; the accepted values are auto,
//	     plain, inline or fullscreen`
//
// So the difference an unusable shell variable makes to this command is the
// whole answer. R3.7 asks for a degraded RENDER; what happens is a failed RUN.
//
// # Where it comes from, and why the citation is narrower than the code
//
// overlay_autoupdate.go:617 resolves the mode before any package work and exits
// 1 on an error. It cites S044-R3.9, which reads: "IF --ui is given a value
// outside the accepted set THEN THE SYSTEM SHALL reject it naming the accepted
// values, and SHALL NOT run the check." The flag, and nothing else — but
// resolveAutoupdateUIMode resolves the whole chain (flag, BENTOO_UI, ui.mode,
// terminal), so the gate fires on all four while claiming the authority of one.
//
// Task 4 of this story made that gap total. root.go's PersistentPreRunE now
// validates --ui for all 30 commands before any run function, so by the time
// line 617 runs the flag has already been judged. Every rejection left at 617
// is an AMBIENT one — the exact case R3.9 never covered and R3.7 answers the
// other way.
//
// Two comments state the guarantee the citation does not give them:
// overlay_autoupdate_ui.go:306 ("R3.9 stops an unusable --ui, BENTOO_UI or
// ui.mode in runAutoupdate") and overlay_autoupdate_check.go:639 ("An error is
// unreachable in a real run"). Both are load-bearing: they are why the third
// producer, presentCheckReport, hands its error to logger.Debug — below the
// default LevelInfo, and therefore to nobody.
//
// # What each test here holds, and which are green on arrival
//
// Red on arrival:
//   - FromTheEnvironmentKeepsTheRunAndStatesItself — R3.7 whole, against a
//     measured control run.
//   - FromTheConfigurationKeepsTheRunAndStatesItself — the wrongly-split half
//     by source: a fix driven by the BENTOO_UI measurement leaves ui.mode as
//     fatal as it is today.
//   - StatesTheRefusalExactlyOnce — R3.6 over the two voices this command has.
//   - LeavesAnUnrelatedFailureItsOwnStatusAndMessage — the half that says the
//     fix must not swallow the failures that are real.
//
// Green on arrival, deliberately: DoesNotSoftenAnExplicitFlag. It pins the half
// that must NOT move. R3.2 and R3.7 sit on the same value arriving from two
// places and give opposite answers, and a fix that made them agree — by
// loosening the root — would turn every other test in this file green and break
// the rule the root states eleven lines above its own check.
//
// # Why the explicit half is a test of its own here, where 12.1 paired them
//
// 12.1 could assert both answers in one function because both were already
// true of `overlay manifest`: the ambient run finished, the explicit one did
// not. On this command the ambient half is the defect, so pairing them would
// bury a deliberate green inside a red test and leave "which half moved?"
// unanswerable from the output. They are split so that each failure names one
// direction.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// seedForAmbientCheck builds a harness whose overlay a check run can actually
// work over, and returns it.
//
// seedCheckOverlay (report_fixture_test.go) is this story's hermetic check
// fixture: an ebuild per package, one more already at the upstream version, and
// a registry pointing all of them at an httptest server. Nothing here reaches
// the network, and the run produces a real report — which is what makes "the
// operator still got their report" an assertion about a report rather than
// about an exit status.
func seedForAmbientCheck(t *testing.T) *testCLI {
	t.Helper()

	c := newTestCLI(t)
	seedCheckOverlay(t, c, "1.0.0", "2.0.0", "app-misc/jq", "dev-lang/go")
	t.Cleanup(stubUIIsTerminal(false))
	return c
}

// runAmbientCheck runs `overlay autoupdate --check --force` over c.
//
// --force skips the cache, so a second run in the same test observes the same
// scan as the first rather than a replay of it. --ui is deliberately absent:
// the flag is the OTHER source, and passing it would answer the question before
// the run started.
func runAmbientCheck(t *testing.T, c *testCLI) (stdout, stderr string, code int) {
	t.Helper()

	stdout, stderr, code = c.Run("overlay", "autoupdate", "--check", "--force")
	if strings.Contains(stderr, "unknown flag") {
		t.Fatalf("a flag was rejected: %s", stderr)
	}
	return stdout, stderr, code
}

// checkReportBody is stdout from the report's first heading onward.
//
// The progress line above it redraws in place — "Checking: [ 33%] 1/3" and its
// carriage returns — and which fraction lands between two writes is a property
// of how fast three goroutines finished, not of the render. Comparing whole
// streams would make a sameness assertion depend on that. From the heading down
// the two runs are byte-identical, measured twice on the same fixture before
// this helper was written.
func checkReportBody(stdout string) string {
	if i := strings.Index(stdout, "Version Check Results"); i >= 0 {
		return stdout[i:]
	}
	return ""
}

// requireLoggerOutputIsReadable proves this test can SEE a logger write before
// the absence of one is allowed to mean anything.
//
// The R3.7 sentence arrives at logger.Warn on stderr. On a run that renders its
// report successfully nothing else is written there — the control run's stderr
// is empty — so there is no line in the same capture to serve as the control
// 12.1 used. This is that control, run through the same code path: a check over
// an overlay with no registry fails at logger.Error, on the same stream, by the
// same route.
//
// It matters because of a trap testcli_test.go documents and 12.1 measured:
// logger.Default builds its logger once, under a sync.Once, and keeps whatever
// os.Stderr WAS at that moment, while captureStream swaps that variable per run
// and closes the pipe afterwards. If the once ever fires inside a captured run,
// every later run's logger output is dropped in silence — and every assertion
// in this file would stay red after a perfectly correct fix.
// report_mode_fallback_test.go's package-level init() pins it, which covers
// this file too; this function is what makes that coverage OBSERVED here rather
// than assumed from another file.
//
// It runs FIRST in each test, before the seeded harness is built: newTestCLI
// repoints HOME with t.Setenv, so the last one built is the one the run reads.
func requireLoggerOutputIsReadable(t *testing.T) {
	t.Helper()

	probe := newTestCLI(t)
	_, stderr, code := probe.Run("overlay", "autoupdate", "--check")
	if code == 0 || !strings.Contains(stderr, "failed to initialize checker") {
		t.Fatalf("the harness read no logger output from a run that must produce it — a missing sentence below would prove nothing about the code.\n"+
			"    Probe: `overlay autoupdate --check` over an overlay with no registry, which\n"+
			"    logs at logger.Error on the same stream the R3.7 sentence uses.\n"+
			"    Observed: exit %d, stderr %q", code, stderr)
	}
}

// TestAutoupdateAmbientModeFromTheEnvironmentKeepsTheRunAndStatesItself is R3.7
// whole, on the third producer.
//
// The control run is not decoration. "Renders the report in plain" and "the run
// survives" are both claims about SAMENESS: what has to hold is that the run
// with the unusable value produced the same status and the same report as the
// run without it, and that only stderr differs — by gaining a sentence.
func TestAutoupdateAmbientModeFromTheEnvironmentKeepsTheRunAndStatesItself(t *testing.T) {
	requireLoggerOutputIsReadable(t)

	control := seedForAmbientCheck(t)
	controlOut, controlErr, controlCode := runAmbientCheck(t, control)
	if checkReportBody(controlOut) == "" {
		t.Fatalf("the control run produced no report — the fixture is not driving the producer this test is about.\n    exit %d, stderr %q\n%s", controlCode, controlErr, controlOut)
	}

	c := seedForAmbientCheck(t)
	t.Setenv("BENTOO_UI", "bogus")
	stdout, stderr, code := runAmbientCheck(t, c)

	if code != controlCode {
		t.Errorf("`overlay autoupdate --check` exited %d with BENTOO_UI=bogus and %d without it — R3.7 degrades the RENDER, it does not fail the run.\n"+
			"    Remedy: func runAutoupdate resolves the whole precedence chain and returns\n"+
			"    exit status 1 on any error, citing S044-R3.9 — which covers the --ui FLAG alone.\n"+
			"    Since Task 4 the root already rejects that flag, so this gate now fires only on\n"+
			"    the two ambient sources, which R3.7 answers the opposite way.\n"+
			"    Observed stderr: %q", code, controlCode, stderr)
	}
	if checkReportBody(stdout) == "" {
		t.Fatalf("BENTOO_UI=bogus cost the operator the report entirely (R3.7): a value inherited from a shell profile decided whether the check ran at all.\n    Observed stdout: %q", stdout)
	}
	if checkReportBody(stdout) != checkReportBody(controlOut) {
		t.Errorf("the report differs from the one the same run produces with no BENTOO_UI set — R3.7 asks for the report in plain, not for a different report.\n--- with BENTOO_UI=bogus ---\n%s\n--- control ---\n%s", checkReportBody(stdout), checkReportBody(controlOut))
	}

	if !strings.Contains(stderr, "BENTOO_UI") {
		t.Errorf("stderr does not name the SOURCE the refused value came from (R3.7).\n"+
			"    Remedy: parseMode already names it. presentCheckReport\n"+
			"    (overlay_autoupdate_check.go:736) hands that error to logger.Debug, which sits\n"+
			"    below the default LevelInfo and reaches no one; reportModeOrPlain is the rung\n"+
			"    that turns it into a mode and a sentence.\n"+
			"    Observed stderr: %q", stderr)
	}
	if !strings.Contains(stderr, "bogus") {
		t.Errorf("stderr does not name the VALUE that was refused (R3.7).\n    Observed stderr: %q", stderr)
	}
	if !statesTheFallback(stderr, "bogus") {
		t.Errorf("stderr does not say which mode the run used INSTEAD (R3.7).\n"+
			"    This is the fact no existing string carries: parseMode's message lists plain\n"+
			"    among the values it would have accepted, which is not the same as saying the\n"+
			"    report above it was rendered in it.\n"+
			"    Observed stderr: %q", stderr)
	}
}

// TestAutoupdateAmbientModeFromTheConfigurationKeepsTheRunAndStatesItself is
// the wrongly-split half by source.
//
// R3.7 names two ambient sources — "the environment or the configuration" — and
// the measurement that motivated this sub-task used only the first. A fix
// written against that measurement (special-casing BENTOO_UI, or reading the
// environment before the gate) satisfies every test above while `ui.mode` stays
// exactly as fatal as it is today: measured on HEAD, a config carrying
// `ui: {mode: bogus}` exits 1 with `ui.mode: "bogus" is not a UI mode`.
//
// The two sources must come out of the resolution together. What must NOT come
// out together is WHICH one is named, because "which of my three places is
// wrong" is the only question the operator has left.
func TestAutoupdateAmbientModeFromTheConfigurationKeepsTheRunAndStatesItself(t *testing.T) {
	requireLoggerOutputIsReadable(t)

	control := seedForAmbientCheck(t)
	controlOut, controlErr, controlCode := runAmbientCheck(t, control)
	if checkReportBody(controlOut) == "" {
		t.Fatalf("the control run produced no report — the fixture is not driving the producer this test is about.\n    exit %d, stderr %q", controlCode, controlErr)
	}

	c := seedForAmbientCheck(t)
	configPath := filepath.Join(c.Home(), ".config", "bentoo", "config.yaml")
	existing, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("reading the harness config at %s: %v", configPath, err)
	}
	if err := os.WriteFile(configPath, append(existing, []byte("ui:\n  mode: bogus\n")...), 0o644); err != nil {
		t.Fatalf("writing ui.mode into the harness config: %v", err)
	}

	stdout, stderr, code := runAmbientCheck(t, c)

	if code != controlCode {
		t.Errorf("`overlay autoupdate --check` exited %d with an unusable ui.mode and %d without it — a configured typo degrades the render, it does not fail the run (R3.7).\n"+
			"    Observed stderr: %q", code, controlCode, stderr)
	}
	if checkReportBody(stdout) == "" {
		t.Fatalf("an unusable ui.mode cost the operator the report entirely (R3.7).\n    Observed stdout: %q", stdout)
	}
	if checkReportBody(stdout) != checkReportBody(controlOut) {
		t.Errorf("the report differs from the one the same run produces with no ui.mode set (R3.7).\n--- with ui.mode: bogus ---\n%s\n--- control ---\n%s", checkReportBody(stdout), checkReportBody(controlOut))
	}

	if !strings.Contains(stderr, "ui.mode") {
		t.Errorf("stderr does not name `ui.mode` as the source of the refused value (R3.7).\n"+
			"    The environment is not the only ambient source, and an operator told only that\n"+
			"    \"a UI mode\" was refused has three places to search.\n"+
			"    Observed stderr: %q", stderr)
	}
	if !strings.Contains(stderr, "bogus") {
		t.Errorf("stderr does not name the value refused from the configuration (R3.7).\n    Observed stderr: %q", stderr)
	}
	if !statesTheFallback(stderr, "bogus") {
		t.Errorf("stderr does not say which mode the run used instead of the configured one (R3.7).\n    Observed stderr: %q", stderr)
	}
}

// TestAutoupdateAmbientModeStatesTheRefusalExactlyOnce is R3.6 over the two
// voices this command has, and it is the third element rather than either half
// of R3.7.
//
// R3.7's own text can be satisfied by a sentence, and says nothing about how
// many. This command is the one place in the toolkit where a refused ambient
// mode passes TWO potential speakers on a single run: the gate at
// overlay_autoupdate.go:617, which resolves the mode before any package work,
// and presentCheckReport, which resolves it again to choose a renderer. The
// intended fix keeps the first — it is also where warnUIDowngrade's line is
// emitted early — and adds a warning to the second. A fix that leaves the
// logger.Error at the first while adding the sentence to the second answers
// R3.7 perfectly and tells the operator the same thing twice, in two voices,
// about one typo.
//
// Which is why the count alone would not do: today the sentence appears exactly
// once, from a gate that then kills the run. The count only means something
// about a run that SURVIVED, so survival is asserted first and fatally.
func TestAutoupdateAmbientModeStatesTheRefusalExactlyOnce(t *testing.T) {
	requireLoggerOutputIsReadable(t)

	c := seedForAmbientCheck(t)
	t.Setenv("BENTOO_UI", "bogus")
	stdout, stderr, code := runAmbientCheck(t, c)

	if code != 0 || checkReportBody(stdout) == "" {
		t.Fatalf("the run did not survive its refused BENTOO_UI, so how many times the refusal was stated says nothing yet (exit %d).\n"+
			"    See TestAutoupdateAmbientModeFromTheEnvironmentKeepsTheRunAndStatesItself for\n"+
			"    the survival half; this test measures the count on a run that finished.\n"+
			"    Observed stderr: %q", code, stderr)
	}

	const reason = "is not a UI mode"
	if got := strings.Count(stderr, reason); got != 1 {
		t.Errorf("the refusal is stated %d times; R3.6 requires exactly once.\n"+
			"    Remedy: this run resolves the mode twice — once at overlay_autoupdate.go:617,\n"+
			"    before any package work, and once in presentCheckReport to pick a renderer.\n"+
			"    Exactly one of them speaks. One typo answered in two voices is the duplication\n"+
			"    R3.6 exists to forbid, and 11.1 already paid for it once at the root.\n"+
			"    Observed stderr:\n%s", got, stderr)
	}
}

// TestAutoupdateAmbientModeDoesNotSoftenAnExplicitFlag is the half that must
// NOT move, and it is EXPECTED TO PASS before 12.3 is implemented.
//
// One value, two sources, opposite answers: from --ui the run is refused before
// any work (R3.2, R3.6), from the environment or the config it finishes and
// says so (R3.7). The cheapest way to make the other four tests here pass is to
// stop rejecting the value at all — and that fix would be indistinguishable
// from a correct one to every test that only checks the ambient direction.
//
// Asserted through the real binary, with 11.1's buildBentoo, because "exits 1
// before doing any work" is a claim about a process: an exit status recovered
// from the harness's panic sentinel is the harness's reading of one, and R8.4
// asks for the real entry point where the claim is end to end.
func TestAutoupdateAmbientModeDoesNotSoftenAnExplicitFlag(t *testing.T) {
	bentoo := buildBentoo(t)
	seedForAmbientCheck(t)

	stdout, stderr, code := runBentoo(t, bentoo, "", "overlay", "autoupdate", "--check", "--force", "--ui=bogus")

	if code != 1 {
		t.Errorf("`overlay autoupdate --check --ui=bogus` exited %d, want 1 — R3.2 refuses an explicit flag before the run does any work (stderr: %q)", code, stderr)
	}
	if strings.TrimSpace(stdout) != "" {
		t.Errorf("`--ui=bogus` produced output; R3.2's refusal happens BEFORE any package work, and a report on stdout means the check ran:\n%s", stdout)
	}
	if !strings.Contains(stderr, "--ui") {
		t.Errorf("the explicit refusal no longer names the flag it refused (R3.2).\n"+
			"    The source is what separates this fatal answer from R3.7's survivable one, so\n"+
			"    an operator who cannot see which of the two happened cannot know whether their\n"+
			"    check ran.\n    Observed stderr: %q", stderr)
	}
	for _, mode := range []string{"auto", "plain", "inline", "fullscreen"} {
		if !strings.Contains(stderr, mode) {
			t.Errorf("the explicit refusal no longer names %q; R3.2 requires the accepted set.\n    Observed stderr: %q", mode, stderr)
		}
	}
}

// TestAutoupdateAmbientModeLeavesAnUnrelatedFailureItsOwnStatusAndMessage is
// the converse of the survival half: R3.7 makes a refused ambient mode stop
// COSTING the run, not stop the run from failing on its own account.
//
// It is also the plainest demonstration of what is wrong today. The same
// command over an overlay with no registry fails for a reason that has nothing
// to do with rendering, and the two runs are not the same failure:
//
//	exit 1, "failed to initialize checker: ... packages.toml not found in overlay"
//	exit 1, `BENTOO_UI: "bogus" is not a UI mode; ...`
//
// Identical statuses, different deaths. The display key kills the run EARLIER
// than the fault the operator actually has, so the diagnostic they need is
// replaced by one about a variable that could not have caused it — and fixing
// the typo only reveals the real error on the next run.
//
// The R3.7 sentence is deliberately NOT required here. This run renders no
// report, so a fix that warns only where a report is produced is correct, and
// demanding the sentence would pin an implementation instead of the rule.
func TestAutoupdateAmbientModeLeavesAnUnrelatedFailureItsOwnStatusAndMessage(t *testing.T) {
	requireLoggerOutputIsReadable(t)

	control := newTestCLI(t)
	t.Cleanup(stubUIIsTerminal(false))
	_, controlErr, controlCode := control.Run("overlay", "autoupdate", "--check")
	if controlCode == 0 {
		t.Fatalf("the control run succeeded over an overlay with no registry — this fixture exists to produce a failure that has nothing to do with rendering (stderr: %q)", controlErr)
	}
	if !strings.Contains(controlErr, "failed to initialize checker") {
		t.Fatalf("the control run did not fail the way this test assumes, so there is nothing to preserve.\n    Observed stderr: %q", controlErr)
	}

	c := newTestCLI(t)
	t.Cleanup(stubUIIsTerminal(false))
	t.Setenv("BENTOO_UI", "bogus")
	_, stderr, code := c.Run("overlay", "autoupdate", "--check")

	if code != controlCode {
		t.Errorf("the run exited %d with BENTOO_UI=bogus and %d without it, on a failure that has nothing to do with rendering — a refused display value must not change what a broken run reports (R3.7).\n    Observed stderr: %q", code, controlCode, stderr)
	}
	if !strings.Contains(stderr, "failed to initialize checker") {
		t.Errorf("BENTOO_UI=bogus replaced the run's own diagnostic with one about the display.\n"+
			"    Remedy: the gate at overlay_autoupdate.go:617 exits before the checker is built,\n"+
			"    so the operator is told about a shell variable that could not have caused their\n"+
			"    failure — and learns the real one only on the run after they fix the typo.\n"+
			"    --- with BENTOO_UI=bogus ---\n%q\n    --- control ---\n%q", stderr, controlErr)
	}
}
