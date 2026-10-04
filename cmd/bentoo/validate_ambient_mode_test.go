package main

// Authored for story 046, sub-task 13.2 — R3.7, R3.2.
//
// The rule, R3.7: "IF the render mode is requested through the environment or
// the configuration with a value outside the accepted set, THEN THE SYSTEM
// SHALL render the report in plain and state the source, the value it refused
// and the mode it used instead."
//
// 12.1 pinned that rule on the manifest and snapshot producers, 12.3 on the
// check producer. `overlay validate` is the FOURTH caller of the same envelope
// — presentValidateReport builds a report.Run at overlay_validate_report.go:176
// and hands it to the same export path — and it is the one where the rule is
// broken in the third way design.md names and neither earlier sub-task could
// show: not a sentence lost to logger.Debug, not a run killed by a gate, but a
// mode that is never resolved at all.
//
//	grep -n reportModeOrPlain cmd/bentoo/overlay_validate_report.go
//	→ nothing
//
// Measured on a binary built from HEAD, one fixture, an isolated HOME, the same
// unusable value in the same variable — "refusal stated" counted on stderr:
//
//	overlay manifest --dry-run   exit 0, refusal stated: 1
//	overlay validate             exit 0, refusal stated: 0
//	overlay validate --json      exit 0, refusal stated: 0
//
// The value is dropped and nothing is said. The operator's report is identical
// to the one they would have got with no BENTOO_UI at all, so there is nothing
// in front of them from which the typo could ever be inferred — on the ONE
// command whose flag semantics this story exists to change, which is also the
// command an operator is most likely to be running from a script whose profile
// they did not write.
//
// # What Green looks like, without this file naming it
//
// The assertions below are about what the operator can read, not about which
// function produces it: a source, a value, a mode used instead, said once, on a
// run that still exits the way it would have. The helper 12.1 built is the
// obvious route and 12.3's doc comment already miscounts the callers it has
// ("All THREE producers call it now" — there are four), but nothing here
// requires it. A producer that satisfied R3.7 by some other means would pass.
//
// # Which cases are Red on arrival and which are deliberately green
//
// Red on arrival:
//   - FromTheEnvironmentKeepsTheRunAndStatesItself — R3.7 whole, plain path,
//     against a measured control.
//   - FromTheConfigurationKeepsTheRunAndStatesItself — the wrongly-split half
//     by SOURCE: a fix written against the BENTOO_UI measurement above leaves
//     `ui.mode` exactly as silent as it is today.
//   - UnderJSONStatesItWithoutBreakingTheDocument — the wrongly-split half by
//     OUTPUT PATH. presentValidateReport forks on asJSON into two renderers,
//     and a fix placed in the human branch reaches neither the operator piping
//     to jq nor the CI job reading the document.
//
// Green on arrival, on purpose — these are the halves that must NOT move:
//   - LeavesTheRunsOwnStatusAlone. This is the defect 12.3 found on the third
//     producer, written down BEFORE it can be introduced on the fourth. There,
//     a mode resolved early and exited on turned "your shell profile has a
//     typo" into "your validation did not run", and answered it with the
//     status and the diagnostic the run's real fault was owed. The cheapest
//     way to make the three red cases above pass is a gate in runValidate, and
//     a gate is how the other three got it wrong.
//   - DoesNotSoftenAnExplicitFlag. R3.2 and R3.7 sit on the same value arriving
//     from two places and answer it oppositely: typed as --ui the run stops
//     before any work, inherited from a profile or a config it finishes and
//     says so. A fix that made the two agree would turn every red case here
//     green and break the promise root.go makes eleven lines above its own
//     check.
//
// # Reused rather than restated
//
// statesTheFallback and runBentoo come from report_mode_fallback_test.go,
// requireLoggerOutputIsReadable from autoupdate_ambient_mode_test.go,
// stubValidateRunner from overlay_validate_test.go, mixedReport from
// overlay_validate_render_test.go, buildBentoo from root_rejection_once_test.go
// and stubUIIsTerminal from overlay_manifest_test.go. A second spelling of the
// question "does this stream say which mode was used instead" would be a second
// answer to it the first time either was edited.
//
// report_mode_fallback_test.go's package-level init() pins logger.Default to the
// real stderr before any capture has moved it; without it the logger keeps the
// first captured run's pipe, which is closed moments later, and every later
// run's Warn is dropped in silence — under which a correct fix would leave this
// file red. That init covers this file, and requireLoggerOutputIsReadable is
// what makes the coverage OBSERVED here rather than assumed.

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/obentoo/bentoolkit/internal/autoupdate/validate"
)

// seedValidate builds a harness whose `overlay validate` run produces a real
// report without a real overlay, and returns it.
//
// mixedReport carries one of every outcome INCLUDING an error finding, so
// Report.ExitCode is 1 rather than 0 (internal/autoupdate/validate/report.go:274)
// — which is the point of using it here. Every status claim below is made
// against a control run rather than against a literal, and a fixture whose
// honest status is 0 would let a hard-coded 0 look like a measurement.
func seedValidate(t *testing.T, rep validate.Report) *testCLI {
	t.Helper()

	c := newTestCLI(t)
	stubValidateRunner(c.deps, rep)
	stubUIIsTerminal(c.deps, false)
	return c
}

// runValidateReport runs `overlay validate`, with --json when asJSON, and no
// --ui anywhere.
//
// The flag is deliberately absent: --ui is the OTHER source, answered before any
// work at the root, and passing it would settle the question this file asks
// before the run started.
func runValidateReport(t *testing.T, c *testCLI, asJSON bool) (stdout, stderr string, code int) {
	t.Helper()

	args := []string{"overlay", "validate"}
	if asJSON {
		args = append(args, "--json")
	}
	stdout, stderr, code = c.Run(args...)
	if strings.Contains(stderr, "unknown flag") {
		t.Fatalf("a flag was rejected: %s", stderr)
	}
	return stdout, stderr, code
}

// writeConfiguredUIMode appends `ui.mode: value` to the harness's own config.
//
// It appends to the file newTestCLI already wrote rather than replacing it,
// because the overlay path in it is what makes the run a run.
func writeConfiguredUIMode(t *testing.T, c *testCLI, value string) {
	t.Helper()

	configPath := filepath.Join(c.Home(), ".config", "bentoo", "config.yaml")
	existing, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("reading the harness config at %s: %v", configPath, err)
	}
	if err := os.WriteFile(configPath, append(existing, []byte("ui:\n  mode: "+value+"\n")...), 0o644); err != nil {
		t.Fatalf("writing ui.mode into the harness config: %v", err)
	}
}

// validateNotice is everything this run said that the control run did not.
//
// # Why it is a subtraction and not a stream
//
// R3.7 says "state", and says nothing about where. This command has TWO answers
// to that question already: in the default mode its human text goes to stdout
// (overlay_validate.go's header states the rule), and under --json stdout
// belongs to the document alone and every diagnostic is pointed at stderr. So a
// test that demanded one descriptor would be pinning a choice the requirement
// leaves open, and would fail a correct fix that made the other one.
//
// What the requirement DOES fix is that the sentence is something the operator
// did not have before. Subtracting the control's report from this run's stdout
// and adding stderr leaves exactly that: the extra. It also keeps the report's
// own prose out of the fallback check below, so a run whose packages happened to
// be named "plain-something" cannot be read as having stated a fallback.
func validateNotice(stdout, stderr, controlOut string) string {
	extra := stdout
	if controlOut != "" {
		extra = strings.Replace(extra, controlOut, "", 1)
	}
	return strings.TrimSpace(extra) + "\n" + stderr
}

// linesNaming returns the lines of text that carry value.
//
// Lines rather than occurrences: "once" is a claim about how many times the
// operator is TOLD, and a single sentence that quotes the value twice is one
// telling, while the same sentence from two speakers is two.
func linesNaming(text, value string) []string {
	var found []string
	for _, line := range strings.Split(text, "\n") {
		if strings.Contains(line, value) {
			found = append(found, line)
		}
	}
	return found
}

// requireStatesTheRefusal is R3.7's three facts plus R3.6's count, over one
// notice.
//
// The three are checked separately so a failure names WHICH fact is missing.
// They fail differently in practice: parseMode's message already carries the
// source and the value, so a fix that merely raises it from Debug to Warn
// answers two of them — while the third, the mode used INSTEAD, is the one no
// existing string in this program carries.
func requireStatesTheRefusal(t *testing.T, notice, source, value string) {
	t.Helper()

	if !strings.Contains(notice, source) {
		t.Errorf("the run does not name %s as the SOURCE of the refused value (R3.7).\n"+
			"    An operator told only that \"a UI mode\" was refused has three places to search:\n"+
			"    the flag they did not pass, their shell profile, and their config file.\n"+
			"    Observed notice: %q", source, notice)
	}
	if !strings.Contains(notice, value) {
		t.Errorf("the run does not name the VALUE it refused (R3.7).\n"+
			"    Remedy: check that `func presentValidateReport` in overlay_validate_report.go\n"+
			"    still calls reportModeOrPlain before it renders. That call is what states the\n"+
			"    refusal; this producer keeps no other use for the mode it gets back, so it is\n"+
			"    easy to delete as dead and lose the sentence with it.\n"+
			"    (This remedy previously read \"grep for reportModeOrPlain in that file returns\n"+
			"    nothing\", which was true when 12.1 was written and false from the moment 12.1\n"+
			"    landed the call — it now returns three hits. Corrected at 15.5, alongside the\n"+
			"    line-number anchor it also carried.)\n"+
			"    Observed notice: %q", notice)
	} else if got := len(linesNaming(notice, value)); got != 1 {
		t.Errorf("the refusal is stated on %d lines; it is owed exactly once (R3.6).\n"+
			"    One typo answered in two voices is the duplication 11.1 already paid for at\n"+
			"    the root, and this run has two candidate speakers the moment a mode is\n"+
			"    resolved anywhere before the presenter.\n"+
			"    Observed notice:\n%s", got, notice)
	}
	if !statesTheFallback(notice, value) {
		t.Errorf("the run does not say which mode it used INSTEAD (R3.7).\n"+
			"    This is the fact no existing string carries: parseMode's message lists plain\n"+
			"    among the values it WOULD have accepted, which is not the same as telling the\n"+
			"    reader that the report in front of them was rendered in it.\n"+
			"    Observed notice: %q", notice)
	}
}

// TestValidateAmbientModeFromTheEnvironmentKeepsTheRunAndStatesItself is R3.7
// whole, on the fourth producer, in the mode a human reads.
//
// # One harness, two runs, one variable changed
//
// The control is not decoration. "Renders the report in plain" and "the run
// survives" are both claims about SAMENESS: what has to hold is that the run
// with the unusable value produced the same status and the same report as the
// run without it, and that the only difference is a sentence gained. Both runs
// go over the same home, the same config and the same stubbed report, so the
// environment is literally the only thing that differs between them.
//
// The report is asserted to be CONTAINED in the second run's stdout rather than
// equal to it, because where the sentence lands is the implementer's to choose
// (see validateNotice): a notice printed above or below the report on stdout is
// as correct as one on stderr, and only losing or changing the report is not.
func TestValidateAmbientModeFromTheEnvironmentKeepsTheRunAndStatesItself(t *testing.T) {
	requireLoggerOutputIsReadable(t)

	c := seedValidate(t, mixedReport())

	controlOut, controlErr, controlCode := runValidateReport(t, c, false)
	if strings.TrimSpace(controlOut) == "" {
		t.Fatalf("the control run produced no report — the fixture is not driving the producer this test is about.\n    exit %d, stderr %q", controlCode, controlErr)
	}

	t.Setenv("BENTOO_UI", "bogus")
	stdout, stderr, code := runValidateReport(t, c, false)

	if code != controlCode {
		t.Errorf("`overlay validate` exited %d with BENTOO_UI=bogus and %d without it — R3.7 degrades the RENDER, it does not change what the run DECIDED (the status is what a CI job branches on).\n    Observed stderr: %q", code, controlCode, stderr)
	}
	if !strings.Contains(stdout, controlOut) {
		t.Fatalf("the report is not the one the same run produces with no BENTOO_UI set — R3.7 asks for the report in plain, not for a different report or for none.\n--- with BENTOO_UI=bogus ---\n%s\n--- control ---\n%s", stdout, controlOut)
	}

	requireStatesTheRefusal(t, validateNotice(stdout, stderr, controlOut), "BENTOO_UI", "bogus")
}

// TestValidateAmbientModeFromTheConfigurationKeepsTheRunAndStatesItself is the
// wrongly-split half by SOURCE.
//
// R3.7 names two ambient sources — "the environment or the configuration" — and
// the measurement that motivated this sub-task used only the first. A fix
// written against that measurement satisfies the case above while `ui.mode`
// stays exactly as silent as it is today, and the configured source is the one
// that survives a new shell, a cron entry and a reboot: an operator can unset a
// variable to find out whether it was the problem, and cannot unset a file they
// have not thought to suspect.
//
// The two sources must come out of the resolution together. What must NOT come
// out together is WHICH one is named, because "which of my three places is
// wrong" is the only question the operator has left — which is why the source is
// asserted by name rather than by "some source was mentioned".
func TestValidateAmbientModeFromTheConfigurationKeepsTheRunAndStatesItself(t *testing.T) {
	requireLoggerOutputIsReadable(t)

	c := seedValidate(t, mixedReport())

	controlOut, controlErr, controlCode := runValidateReport(t, c, false)
	if strings.TrimSpace(controlOut) == "" {
		t.Fatalf("the control run produced no report — the fixture is not driving the producer this test is about.\n    exit %d, stderr %q", controlCode, controlErr)
	}

	writeConfiguredUIMode(t, c, "bogus")
	stdout, stderr, code := runValidateReport(t, c, false)

	if code != controlCode {
		t.Errorf("`overlay validate` exited %d with an unusable ui.mode and %d without it — a typo in a config file degrades the render, it does not change the run's answer (R3.7).\n    Observed stderr: %q", code, controlCode, stderr)
	}
	if !strings.Contains(stdout, controlOut) {
		t.Fatalf("an unusable ui.mode changed or cost the operator the report (R3.7).\n--- with ui.mode: bogus ---\n%s\n--- control ---\n%s", stdout, controlOut)
	}

	requireStatesTheRefusal(t, validateNotice(stdout, stderr, controlOut), "ui.mode", "bogus")
}

// TestValidateAmbientModeUnderJSONStatesItWithoutBreakingTheDocument is the
// wrongly-split half by OUTPUT PATH, and it carries one claim the plain cases
// cannot make.
//
// presentValidateReport forks on asJSON into two renderers. A fix placed in the
// human branch — or one that writes its sentence through the `diag` writer the
// presenter is already handed — passes both cases above and leaves the machine
// path exactly as it is today. That path is where a silent refusal costs most:
// nobody is watching a CI job's terminal, and a document that renders the same
// whatever the profile says gives the operator nothing to notice.
//
// # The document is asserted BYTE-IDENTICAL here, where the plain cases were not
//
// The two are not the same promise. In the default mode stdout is prose and a
// sentence added to it is a legitimate place to state a refusal. Under --json
// stdout is one document and nothing else may land in it — the whole reason
// runValidate points diag at stderr for the run (overlay_validate.go:162) — so a
// notice on stdout would break the `| jq` the flag exists for. That is a fact
// about the requirement's own reading of this path, not about which function
// emits the line, and it is why this case reads the notice from stderr alone.
//
// Both ambient sources are run through it, because the fork is BELOW them: a fix
// that reached --json for the environment and not for the config would be the
// two halves of this file's split intersecting, and neither other test would see
// it.
func TestValidateAmbientModeUnderJSONStatesItWithoutBreakingTheDocument(t *testing.T) {
	cases := []struct {
		name    string
		source  string
		arrange func(t *testing.T, c *testCLI)
	}{
		{
			name:    "BENTOO_UI",
			source:  "BENTOO_UI",
			arrange: func(t *testing.T, _ *testCLI) { t.Setenv("BENTOO_UI", "bogus") },
		},
		{
			name:    "ui.mode",
			source:  "ui.mode",
			arrange: func(t *testing.T, c *testCLI) { writeConfiguredUIMode(t, c, "bogus") },
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			requireLoggerOutputIsReadable(t)

			c := seedValidate(t, mixedReport())

			controlOut, controlErr, controlCode := runValidateReport(t, c, true)
			var control map[string]any
			if err := json.Unmarshal([]byte(controlOut), &control); err != nil {
				t.Fatalf("the control `overlay validate --json` did not write one JSON document: %v\n    exit %d, stderr %q\n%s", err, controlCode, controlErr, controlOut)
			}

			tc.arrange(t, c)
			stdout, stderr, code := runValidateReport(t, c, true)

			if code != controlCode {
				t.Errorf("`overlay validate --json` exited %d with an unusable %s and %d without it — a refused display value must not change what the run decided (R3.7).\n    Observed stderr: %q", code, tc.source, controlCode, stderr)
			}
			if stdout != controlOut {
				t.Errorf("the JSON document changed when %s was unusable — stdout under --json is the document and nothing else, so a notice written there is a broken pipeline rather than a stated refusal (R4.3, D8).\n--- with %s: bogus ---\n%s\n--- control ---\n%s", tc.source, tc.source, stdout, controlOut)
			}
			if err := json.Unmarshal([]byte(stdout), new(map[string]any)); err != nil {
				t.Fatalf("stdout is no longer one JSON document with %s unusable: %v\n%s", tc.source, err, stdout)
			}

			// stderr alone: under --json it is the only stream a diagnostic may
			// use, and the assertion above has already fixed stdout as the
			// document.
			requireStatesTheRefusal(t, stderr, tc.source, "bogus")
		})
	}
}

// TestValidateAmbientModeLeavesTheRunsOwnStatusAlone is the half that must NOT
// move, and it is EXPECTED TO PASS before 13.2 is implemented.
//
// It is 12.3's finding written down one producer early. On `overlay autoupdate
// --check` a mode resolved before the work and exited on turned every unusable
// ambient value into a failed run, and — worse — into the WRONG diagnostic:
// a command with a real fault reported the display key instead, so the operator
// learned their actual error only on the run after they fixed a typo that could
// not have caused it.
//
// `overlay validate` has the same shape waiting for it. Its statuses are load
// bearing and documented as such: 1 means a gate found an error, 2 means the
// selector matched nothing, 130 means the run was interrupted, and CI branches
// on all three. The cheapest way to satisfy the three red cases above is a
// resolution near the top of runValidate that exits on error — and that fix
// would answer a selector typo with a message about a shell variable.
//
// The fixture is a run that FAILS ON ITS OWN ACCOUNT: an unmatched selector,
// whose status is 2 and whose report is one line. R3.7's sentence is
// deliberately not required of it — the requirement is about a report that was
// rendered, and this one renders through the same presenter, so demanding or
// forbidding the sentence here would pin an implementation instead of the rule.
// What is required is that the run's OWN answer survives the display key.
func TestValidateAmbientModeLeavesTheRunsOwnStatusAlone(t *testing.T) {
	requireLoggerOutputIsReadable(t)

	const selector = "app-misc/does-not-exist"
	c := seedValidate(t, validate.Report{UnmatchedSelector: selector})

	controlOut, controlErr, controlCode := c.Run("overlay", "validate", selector)
	if controlCode != 2 {
		t.Fatalf("the control run exited %d, want 2 — this fixture exists to produce a failure that has nothing to do with rendering, and it is not producing one.\n    stdout %q, stderr %q", controlCode, controlOut, controlErr)
	}
	if !strings.Contains(controlOut+controlErr, selector) {
		t.Fatalf("the control run does not name the selector it could not match, so there is nothing here to preserve.\n    stdout %q, stderr %q", controlOut, controlErr)
	}

	t.Setenv("BENTOO_UI", "bogus")
	stdout, stderr, code := c.Run("overlay", "validate", selector)

	if code != controlCode {
		t.Errorf("the run exited %d with BENTOO_UI=bogus and %d without it, on a failure that has nothing to do with rendering — a refused display value must not change what a broken run reports (R3.7).\n"+
			"    Remedy: resolve the mode where the report is PRODUCED, not at the top of the\n"+
			"    run. overlay_autoupdate.go:617 is the gate this warns against; it cost the\n"+
			"    third producer both its report and its exit status.\n"+
			"    Observed stdout %q, stderr %q", code, controlCode, stdout, stderr)
	}
	if !strings.Contains(stdout+stderr, selector) {
		t.Errorf("BENTOO_UI=bogus replaced the run's own diagnostic with one about the display — the operator is told about a shell variable that could not have caused their failure, and learns the real one only on the run after they fix the typo.\n"+
			"    --- with BENTOO_UI=bogus ---\n    stdout %q\n    stderr %q\n    --- control ---\n    stdout %q\n    stderr %q", stdout, stderr, controlOut, controlErr)
	}
}

// TestValidateAmbientModeDoesNotSoftenAnExplicitFlag is the other half that must
// NOT move, and it too is EXPECTED TO PASS before 13.2 is implemented.
//
// One value, two sources, opposite answers: typed as --ui the run is refused
// before any work and told so once (R3.2, R3.6), inherited from the environment
// or the config it finishes and says so (R3.7). The cheapest way to make the
// three red cases pass is to stop refusing the value anywhere — and to every
// test that only looks at the ambient direction, that fix is indistinguishable
// from a correct one.
//
// Asserted through the real binary, with 11.1's buildBentoo, because "exits 1
// before doing any work" is a claim about a PROCESS: a status recovered from the
// harness's panic sentinel is the harness's reading of one. No runner stub is
// installed and none is needed — the claim is precisely that the run never
// reaches the runner, and an isolated HOME is what keeps the attempt off any
// real overlay if it ever does.
func TestValidateAmbientModeDoesNotSoftenAnExplicitFlag(t *testing.T) {
	bentoo := buildBentoo(t)
	newTestCLI(t)

	stdout, stderr, code := runBentoo(t, bentoo, "", "overlay", "validate", "--ui=bogus")

	if code != 1 {
		t.Errorf("`overlay validate --ui=bogus` exited %d, want 1 — R3.2 refuses an explicit flag before the run does any work (stderr: %q)", code, stderr)
	}
	if strings.TrimSpace(stdout) != "" {
		t.Errorf("`--ui=bogus` produced output; R3.2's refusal happens BEFORE any work, and anything on stdout means the validation ran:\n%s", stdout)
	}
	if !strings.Contains(stderr, "--ui") {
		t.Errorf("the explicit refusal no longer names the flag it refused (R3.2).\n"+
			"    The source is what separates this fatal answer from R3.7's survivable one, so\n"+
			"    an operator who cannot see which of the two happened cannot know whether their\n"+
			"    overlay was validated.\n    Observed stderr: %q", stderr)
	}
	for _, mode := range []string{"auto", "plain", "inline", "fullscreen"} {
		if !strings.Contains(stderr, mode) {
			t.Errorf("the explicit refusal no longer names %q; R3.2 requires the accepted set.\n    Observed stderr: %q", mode, stderr)
		}
	}
}
