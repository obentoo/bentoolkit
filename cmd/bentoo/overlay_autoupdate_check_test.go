package main

// Authored for story 033, sub-task 12.3 — R9, R9.1, R9.2, R9.3, R9.4, R9.5.
//
// WHAT `--check --llm` PROMISES: it tells the operator which pending updates
// would actually survive being applied, and it applies none of them. So the two
// halves that can go wrong are the cost (hours of build the operator did not
// expect) and the reach (something published by a command documented as
// read-only). Both are asserted here, and the second one is asserted on EVERY
// path rather than only the happy one.
//
// THE PLAN COMES FIRST, AND THE ORDERING IS THE ASSERTION. "A plan is printed"
// is satisfied by printing it at the end, next to the results, which is worth
// nothing: by then the hours are spent. So the gate runner writes a marker to
// stdout as it runs, and the test requires every plan line to appear BEFORE the
// first marker. This is `confirmSweep`'s established shape
// (overlay_autoupdate_sweep.go:241) — the full plan, then the single question.
//
// NAMES. Where design.md fixes a name this file uses it. Where it does not, the
// name is invented HERE and listed in the evidence entry rather than chosen
// silently: `validationPlan`, `validationPlanEntry`, `buildValidationPlan`,
// `printValidationPlan`, `confirmValidationRun`, `runValidationCheck`,
// `validationTally`, plus `validationPlan.DistfilesToFetch` and
// `validationPlan.DepthDistribution` (R9.3's two additions). The BEHAVIOURS
// below are the contract; those identifiers are the negotiable part.

import (
	"fmt"
	"strings"
	"testing"

	"github.com/obentoo/bentoolkit/internal/autoupdate"
	"github.com/obentoo/bentoolkit/internal/autoupdate/validate"
)

// checkPlanUpdates is the pending list these cases plan against: one series
// crossing that earns a build, one patch bump that does not, and one binary
// record that resolves to depth none.
func checkPlanUpdates() []autoupdate.PendingUpdate {
	return []autoupdate.PendingUpdate{
		{Package: "media-plugins/gst-plugins-qt6", CurrentVersion: "1.28.6", NewVersion: "1.29.2"},
		{Package: "dev-libs/quiet", CurrentVersion: "1.28.5", NewVersion: "1.28.6"},
		{Package: "app-editors/vscode-bin", CurrentVersion: "1.90.0", NewVersion: "1.91.0"},
	}
}

func checkPlanPolicy() validate.DepthPolicy {
	return validate.DepthPolicy{
		ByClass: map[validate.Class]validate.Depth{
			validate.ClassRevision: validate.DepthOptions,
			validate.ClassPatch:    validate.DepthOptions,
			validate.ClassSeries:   validate.DepthConfigure,
			validate.ClassMajor:    validate.DepthCompile,
		},
		Overrides: map[string]validate.DepthOverride{},
	}
}

// TestCheckPlan_NamesEveryPackageItsDepthAndWhy is R9.3. The plan is what makes
// the cost visible BEFORE it is paid, so a plan that omits the reason leaves the
// operator approving a number they cannot check.
func TestCheckPlan_NamesEveryPackageItsDepthAndWhy(t *testing.T) {
	plan := buildValidationPlan(checkPlanUpdates(), checkPlanPolicy())

	if len(plan.Entries) != len(checkPlanUpdates()) {
		t.Fatalf("the plan holds %d entries for %d pending updates; every pending update is planned for, including the skipped ones",
			len(plan.Entries), len(checkPlanUpdates()))
	}

	out := captureStdout(t, func() { printValidationPlan(plan) })

	for _, want := range []string{"media-plugins/gst-plugins-qt6", "dev-libs/quiet", "app-editors/vscode-bin"} {
		if !strings.Contains(out, want) {
			t.Errorf("the plan does not name %s:\n%s", want, out)
		}
	}
	if !strings.Contains(out, "configure") {
		t.Errorf("the plan does not state the depth selected for the series crossing:\n%s", out)
	}
	// R9.3's "which are skipped with the reason": a package that will not be
	// validated has to say why, or the operator reads the shorter list as
	// progress.
	if !strings.Contains(out, "none") {
		t.Errorf("the plan does not show the binary record resolving to depth none:\n%s", out)
	}
	for _, entry := range plan.Entries {
		if entry.Reason == "" {
			t.Errorf("the plan entry for %s carries no reason; the operator cannot tell policy from an override", entry.Package)
		}
	}
	// R9.3 also asks for the count, so the operator knows the size of what they
	// are approving without counting lines.
	if !strings.Contains(out, "3") {
		t.Errorf("the plan does not state how many packages will be evaluated:\n%s", out)
	}
}

// TestCheckRun_ThePlanIsPrintedBeforeTheFirstGateRuns is the ordering assertion,
// and it is the one that makes R9.3 worth anything. A plan printed beside the
// results is a receipt, not a plan.
func TestCheckRun_ThePlanIsPrintedBeforeTheFirstGateRuns(t *testing.T) {
	plan := buildValidationPlan(checkPlanUpdates(), checkPlanPolicy())

	out := captureStdout(t, func() {
		runValidationCheck(plan, func(entry validationPlanEntry) validate.EbuildResult {
			// The marker lands in the same stream as the plan, so their ORDER
			// is a property of one captured string rather than of two clocks.
			fmt.Printf("GATE-RAN %s\n", entry.Package)
			return validate.EbuildResult{Package: entry.Package, Version: entry.Version}
		})
	})

	firstGate := strings.Index(out, "GATE-RAN")
	if firstGate == -1 {
		t.Fatalf("no gate ran during a --check --llm run; R9.1 evaluates every pending update:\n%s", out)
	}
	head := out[:firstGate]
	for _, want := range []string{"media-plugins/gst-plugins-qt6", "dev-libs/quiet", "app-editors/vscode-bin"} {
		if !strings.Contains(head, want) {
			t.Errorf("%s is missing from everything printed before the first gate ran; the whole plan comes first:\n%s", want, out)
		}
	}
}

// TestCheckRun_NonInteractiveWithoutYesChangesNothingAndSaysHow is
// `confirmSweep`'s middle gate. A scripted run that silently did the expensive
// thing would be the worst outcome; one that silently did nothing would be the
// second worst, because the operator would not know why.
func TestCheckRun_NonInteractiveWithoutYesChangesNothingAndSaysHow(t *testing.T) {
	origYes := autoupdateYes
	autoupdateYes = false
	t.Cleanup(func() { autoupdateYes = origYes })

	plan := buildValidationPlan(checkPlanUpdates(), checkPlanPolicy())

	var approved bool
	out := captureStdout(t, func() { approved = confirmValidationRun(plan) })

	if approved {
		t.Fatal("a non-interactive run without --yes approved a plan containing a build; the test requires BOTH stdin and stdout to be a TTY, " +
			"and `go test` gives neither")
	}
	if !strings.Contains(out, "--yes") {
		t.Errorf("the refusal does not say how to proceed:\n%s", out)
	}
}

// TestCheckRun_PipedYesCannotAnswerForTheOperator pins the interactivity
// predicate itself. `yes | bentoo …` must not be able to approve hours of build:
// stdout being a pipe is enough to refuse, which is why the check is on both
// streams rather than on stdin alone.
func TestCheckRun_PipedYesCannotAnswerForTheOperator(t *testing.T) {
	origYes := autoupdateYes
	autoupdateYes = false
	t.Cleanup(func() { autoupdateYes = origYes })

	// captureStdout replaces os.Stdout with a pipe, which is exactly the shape
	// `yes | bentoo …` produces.
	var approved bool
	_ = captureStdout(t, func() { approved = confirmValidationRun(buildValidationPlan(checkPlanUpdates(), checkPlanPolicy())) })

	if approved {
		t.Error("a piped stdout was treated as an interactive terminal; the predicate requires both stdin and stdout to be a TTY " +
			"so a pipe cannot answer for a human")
	}
}

// TestCheckRun_AnOptionsOnlyRunAsksNothing is R9.4 read precisely: the single
// confirmation exists because a gate above `options` costs build time. A run
// where nothing does must not prompt, or the operator learns to answer without
// reading.
func TestCheckRun_AnOptionsOnlyRunAsksNothing(t *testing.T) {
	origYes := autoupdateYes
	autoupdateYes = false
	t.Cleanup(func() { autoupdateYes = origYes })

	cheap := []autoupdate.PendingUpdate{
		{Package: "dev-libs/quiet", CurrentVersion: "1.28.5", NewVersion: "1.28.6"},
		{Package: "dev-libs/quieter", CurrentVersion: "2.0.0", NewVersion: "2.0.0-r1"},
	}
	plan := buildValidationPlan(cheap, checkPlanPolicy())

	var approved bool
	out := captureStdout(t, func() { approved = confirmValidationRun(plan) })

	if !approved {
		t.Errorf("a run whose depths are all `options` was refused for want of a confirmation; nothing here starts a build:\n%s", out)
	}
}

// TestCheckRun_YesApprovesWithAWarning is confirmSweep's first gate, kept
// identical so the two commands read alike.
func TestCheckRun_YesApprovesWithAWarning(t *testing.T) {
	origYes := autoupdateYes
	autoupdateYes = true
	t.Cleanup(func() { autoupdateYes = origYes })

	plan := buildValidationPlan(checkPlanUpdates(), checkPlanPolicy())

	var approved bool
	out := captureStdout(t, func() { approved = confirmValidationRun(plan) })

	if !approved {
		t.Fatal("--yes did not approve the run")
	}
	if out == "" {
		t.Error("--yes approved silently; the operator gets a warning saying what is about to be spent")
	}
}

// TestCheckRun_PublishesNothingOnAnyPath is R9.2, and it is asserted over every
// route through the function rather than the happy one: a gate that errors, a
// gate that skips, and a gate that passes. `--check` is documented read-only,
// and the overlay it would write to auto-commits and pushes.
func TestCheckRun_PublishesNothingOnAnyPath(t *testing.T) {
	plan := buildValidationPlan(checkPlanUpdates(), checkPlanPolicy())

	var promoted []string
	origSet := setVersionsForCheck
	setVersionsForCheck = func(overlayPath string, pins map[string]string) error {
		for pkg := range pins {
			promoted = append(promoted, pkg)
		}
		return nil
	}
	t.Cleanup(func() { setVersionsForCheck = origSet })

	outcomes := map[string]validate.Outcome{
		"media-plugins/gst-plugins-qt6": validate.OutcomeFailed,
		"dev-libs/quiet":                validate.OutcomePass,
		"app-editors/vscode-bin":        validate.OutcomeSkipped,
	}

	_ = captureStdout(t, func() {
		runValidationCheck(plan, func(entry validationPlanEntry) validate.EbuildResult {
			return validate.EbuildResult{
				Package: entry.Package,
				Version: entry.Version,
				Gates: []validate.GateResult{{
					Gate:    validate.GateOptions,
					Outcome: outcomes[entry.Package],
					Reason:  "fixture",
				}},
			}
		})
	})

	if len(promoted) != 0 {
		t.Errorf("a --check run promoted %v; --check publishes nothing, on any path (R9.2)", promoted)
	}
}

// TestCheckRun_TallyCountsEachOutcomeExactlyOnce is R9.5. The tally is the
// number the operator reads and acts on, so a package counted twice — or in two
// columns — is worse than no tally at all.
func TestCheckRun_TallyCountsEachOutcomeExactlyOnce(t *testing.T) {
	plan := buildValidationPlan(checkPlanUpdates(), checkPlanPolicy())

	outcomes := map[string]validate.Outcome{
		"media-plugins/gst-plugins-qt6": validate.OutcomeFailed,
		"dev-libs/quiet":                validate.OutcomePass,
		"app-editors/vscode-bin":        validate.OutcomeSkipped,
	}

	var tally validationTally
	_ = captureStdout(t, func() {
		tally = runValidationCheck(plan, func(entry validationPlanEntry) validate.EbuildResult {
			outcome := outcomes[entry.Package]
			res := validate.EbuildResult{
				Package: entry.Package,
				Version: entry.Version,
				Gates:   []validate.GateResult{{Gate: validate.GateOptions, Outcome: outcome, Reason: "fixture"}},
			}
			if outcome == validate.OutcomeFailed {
				res.Gates[0].Findings = []validate.Finding{{
					Gate: validate.GateOptions, Severity: validate.SeverityError, Detail: "-Daalib= is undeclared upstream",
				}}
			}
			return res
		})
	})

	if tally.Proved != 1 {
		t.Errorf("Proved = %d, want 1", tally.Proved)
	}
	if tally.Errored != 1 {
		t.Errorf("Errored = %d, want 1", tally.Errored)
	}
	if tally.Skipped != 1 {
		t.Errorf("Skipped = %d, want 1", tally.Skipped)
	}
	if got := tally.Proved + tally.Errored + tally.Skipped; got != len(plan.Entries) {
		t.Errorf("the tally accounts for %d outcomes across %d planned packages; each package lands in exactly one column",
			got, len(plan.Entries))
	}
}

// TestCheckPlan_EveryPendingUpdateIsEvaluated is R9.1. A plan that quietly
// dropped the expensive packages would produce a reassuring tally about the easy
// ones, which is the failure mode with the longest half-life.
func TestCheckPlan_EveryPendingUpdateIsEvaluated(t *testing.T) {
	plan := buildValidationPlan(checkPlanUpdates(), checkPlanPolicy())

	var evaluated []string
	_ = captureStdout(t, func() {
		runValidationCheck(plan, func(entry validationPlanEntry) validate.EbuildResult {
			evaluated = append(evaluated, entry.Package)
			return validate.EbuildResult{Package: entry.Package, Version: entry.Version}
		})
	})

	for _, update := range checkPlanUpdates() {
		var seen bool
		for _, got := range evaluated {
			if got == update.Package {
				seen = true
			}
		}
		if !seen {
			t.Errorf("%s was planned but never evaluated; every pending update goes through the gates at its resolved depth (R9.1)",
				update.Package)
		}
	}
}

// TestCheckPlan_NamesTheDistfilesToBeFetchedAndTheDepthDistribution is R9.3's
// two additions, and they answer the two questions an operator actually has
// before approving a run.
//
// HOW MANY DISTFILES. A gate above `options` needs an unpacked tarball, so this
// run STAGES and MANIFESTS every pending update — which means fetching every
// distfile the host does not already hold. On a metered connection, or a laptop,
// forty tarballs is the number that decides whether the answer is yes. It is
// nowhere else in the output.
//
// THE DEPTH DISTRIBUTION. This is the story's "reach is measured, never claimed"
// metric: how many packages resolve to none, options, patches, configure and
// compile, WITH ITS DENOMINATOR. `--check --llm` is the only place it is
// produced, so a plan that omits it leaves the success metric unmeasurable — and
// "most bumps are cheap" stays an assertion nobody can check.
func TestCheckPlan_NamesTheDistfilesToBeFetchedAndTheDepthDistribution(t *testing.T) {
	plan := buildValidationPlan(checkPlanUpdates(), checkPlanPolicy())

	out := captureStdout(t, func() { printValidationPlan(plan) })

	// The fetch count is a number the operator can act on, so it is carried on
	// the plan rather than only printed: the confirmation prompt reads it too.
	if plan.DistfilesToFetch < 0 {
		t.Fatalf("DistfilesToFetch = %d", plan.DistfilesToFetch)
	}
	if !strings.Contains(strings.ToLower(out), "distfile") {
		t.Errorf("the plan does not say how many distfiles the run will fetch; a gate above options unpacks a tarball, and on a "+
			"metered connection that number is what decides the answer (R9.3):\n%s", out)
	}

	// The distribution, with its denominator. Every depth the run selected has
	// to appear with its count; a depth nothing resolved to may be omitted, but
	// the total may not.
	dist := plan.DepthDistribution
	if len(dist) == 0 {
		t.Fatalf("the plan carries no depth distribution; this is the only place the story's \"reported with its denominator\" "+
			"metric is produced (R9.3):\n%s", out)
	}
	total := 0
	for _, n := range dist {
		total += n
	}
	if total != len(plan.Entries) {
		t.Errorf("the depth distribution accounts for %d packages across a plan of %d; each package lands in exactly one bucket, "+
			"and a distribution without its denominator measures nothing", total, len(plan.Entries))
	}
	for depth, n := range dist {
		if n == 0 {
			continue
		}
		if !strings.Contains(out, depth) {
			t.Errorf("the printed plan does not name the depth %q that %d package(s) resolved to:\n%s", depth, n, out)
		}
	}

	// The fixture is one configure, one options and one none, so the
	// distribution is not trivially one bucket.
	if len(dist) < 2 {
		t.Errorf("the distribution collapsed to %d bucket(s) for a plan spanning three different depths: %v", len(dist), dist)
	}
}

// TestCheckRun_AReviewerEscalationPastTheConfirmedDepthDoesNotRunUnasked is
// R9.6, and it closes a hole the confirmation gate opened by being correct.
//
// A plan that resolves entirely to `options` asks for nothing — rightly, since
// nothing in it starts a build (R9.4). The reviewer then runs, finds a
// build-system change, and raises a bump to `configure` or `compile`. Those are
// hours the operator never approved, in a run whose whole promise was that it
// would tell them the cost first.
//
// Either answer is acceptable and the choice is the implementer's: re-prompt for
// the raised depth, or hold that bump at the depth that WAS confirmed. What is
// not acceptable is doing the expensive thing silently, so the report has to say
// which happened.
func TestCheckRun_AReviewerEscalationPastTheConfirmedDepthDoesNotRunUnasked(t *testing.T) {
	origYes := autoupdateYes
	autoupdateYes = false // nothing may be approved without an explicit ask
	t.Cleanup(func() { autoupdateYes = origYes })

	// A plan whose every entry is `options`: by R9.4 this asks for no
	// confirmation at all.
	cheap := []autoupdate.PendingUpdate{
		{Package: "dev-libs/quiet", CurrentVersion: "1.28.5", NewVersion: "1.28.6"},
		{Package: "dev-libs/quieter", CurrentVersion: "2.0.0", NewVersion: "2.0.0-r1"},
	}
	plan := buildValidationPlan(cheap, checkPlanPolicy())
	for _, entry := range plan.Entries {
		if entry.Depth != "options" {
			t.Fatalf("fixture drift: %s resolved to %q, want options — this case is about a run that asked for nothing",
				entry.Package, entry.Depth)
		}
	}

	// The reviewer raises the first package to compile.
	escalations := map[string]string{"dev-libs/quiet": "compile"}

	var ranAt []string
	out := captureStdout(t, func() {
		runValidationCheck(plan, func(entry validationPlanEntry) validate.EbuildResult {
			depth := entry.Depth
			if raised, ok := escalations[entry.Package]; ok {
				depth = raised
			}
			ranAt = append(ranAt, entry.Package+"@"+depth)
			fmt.Printf("GATE-RAN %s at %s\n", entry.Package, depth)
			return validate.EbuildResult{
				Package: entry.Package, Version: entry.Version,
				Depth: depth, DepthRequested: entry.Depth,
			}
		})
	})

	// The hole: a compile that nobody was asked about.
	for _, at := range ranAt {
		if strings.HasSuffix(at, "@compile") && !strings.Contains(out, "compile") {
			t.Fatalf("a bump ran at compile depth in a run that asked for no confirmation, and the output never mentions it: %v", ranAt)
		}
	}

	lower := strings.ToLower(out)
	reprompted := strings.Contains(lower, "confirm") || strings.Contains(lower, "--yes") || strings.Contains(lower, "proceed")
	held := strings.Contains(lower, "held") || strings.Contains(lower, "not raised") || strings.Contains(lower, "confirmed depth")

	if !reprompted && !held {
		t.Errorf("the reviewer raised a bump past the depth the operator confirmed and the report says neither that it "+
			"re-prompted nor that it held at the confirmed depth; hours nobody approved would be spent in silence (R9.6):\n%s", out)
	}

	// If it HELD, the gate must actually have run at the confirmed depth — a
	// report that says "held" while running the compile anyway is worse than
	// either honest answer.
	if held && !reprompted {
		for _, at := range ranAt {
			if strings.HasSuffix(at, "@compile") {
				t.Errorf("the report says the escalation was held, but the gate ran at compile depth anyway: %v", ranAt)
			}
		}
	}
}

// TestCheckRun_AnEscalationInsideTheConfirmedDepthRunsWithoutFuss keeps R9.6
// from being over-applied. The operator already approved a run containing a
// configure, so a reviewer raising a DIFFERENT package from options to configure
// asks for nothing new — re-prompting there would train them to approve without
// reading, which is the failure mode every confirmation gate dies of.
func TestCheckRun_AnEscalationInsideTheConfirmedDepthRunsWithoutFuss(t *testing.T) {
	origYes := autoupdateYes
	autoupdateYes = true // the whole run, including its configure, was approved
	t.Cleanup(func() { autoupdateYes = origYes })

	plan := buildValidationPlan(checkPlanUpdates(), checkPlanPolicy())

	var prompts int
	origConfirm := confirmSweepFn
	confirmSweepFn = func(string) bool { prompts++; return true }
	t.Cleanup(func() { confirmSweepFn = origConfirm })

	_ = captureStdout(t, func() {
		if !confirmValidationRun(plan) {
			t.Fatal("--yes did not approve the run")
		}
		runValidationCheck(plan, func(entry validationPlanEntry) validate.EbuildResult {
			depth := entry.Depth
			if entry.Package == "dev-libs/quiet" {
				depth = "configure" // raised, but no deeper than the run already covers
			}
			return validate.EbuildResult{Package: entry.Package, Version: entry.Version, Depth: depth, DepthRequested: entry.Depth}
		})
	})

	if prompts != 0 {
		t.Errorf("the operator was prompted %d extra time(s) for an escalation no deeper than the depth already confirmed; "+
			"a gate that asks twice for the same permission gets answered without being read", prompts)
	}
}

// skipReason's contract is the one its own doc comment states: a skip ALWAYS
// carries a reason. The cascade has three sources and none of them is a field
// anything forces to be populated, so "always" is only true if the function
// answers for the case where all three are blank.
//
// The empty case is asserted on the RENDERED line, not just on the return
// value, because the defect is what an operator reads: printValidationResults
// wraps this in parentheses, so an empty return prints "not validated ()" and
// the empty parenthesis reads as "checked, nothing to say".
func TestSkipReason_NeverReportsASkipWithoutAReason(t *testing.T) {
	tests := []struct {
		name   string
		result validate.EbuildResult
		entry  validationPlanEntry
		want   string
	}{
		{
			name: "a skipped gate's own reason wins over both fallbacks",
			result: validate.EbuildResult{
				Gates: []validate.GateResult{
					{Gate: "build", Outcome: validate.OutcomeSkipped, Reason: "gate said so"},
				},
				DepthReason: "depth said so",
			},
			entry: validationPlanEntry{Reason: "plan said so"},
			want:  "gate said so",
		},
		{
			name:   "the depth reason is next when no gate carries one",
			result: validate.EbuildResult{DepthReason: "depth said so"},
			entry:  validationPlanEntry{Reason: "plan said so"},
			want:   "depth said so",
		},
		{
			name:   "the plan's reason is last",
			result: validate.EbuildResult{},
			entry:  validationPlanEntry{Reason: "plan said so"},
			want:   "plan said so",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := skipReason(tc.result, tc.entry); got != tc.want {
				t.Errorf("skipReason() = %q, want %q", got, tc.want)
			}
		})
	}

	// The case the cascade used to fall out of: nothing populated anywhere.
	t.Run("every source blank still names the silence", func(t *testing.T) {
		got := skipReason(validate.EbuildResult{
			// A skipped gate that states no reason must not be mistaken for a
			// reason: this is the shape skippedBuildGates produces when a depth
			// reason was expected and never supplied.
			Gates: []validate.GateResult{{Gate: "build", Outcome: validate.OutcomeSkipped}},
		}, validationPlanEntry{})

		if got == "" {
			t.Fatal("skipReason() returned \"\": the caller prints it as \"not validated ()\", " +
				"and an empty parenthesis reads as a result rather than as an unexplained skip")
		}
		if strings.TrimSpace(got) == "" {
			t.Fatalf("skipReason() = %q: whitespace reads the same as empty once parenthesised", got)
		}
	})
}

// --- story 043, sub-task 4.2 ------------------------------------------------
//
// THE ANSWER, RECORDED SO THE NEXT READER DOES NOT RE-DERIVE IT: the tally does
// NOT feed this command's exit code, and cannot, because nothing reads it.
//
// runValidationCheck returns a validationTally. Its ONE production caller
// (overlay_autoupdate_check.go:474) discards the return value, and the whole
// check path contains no call to osExit — verified by grep over
// overlay_autoupdate_check.go, which has none. The counts are printed by
// printValidationTally and go nowhere else. So `bentoo overlay autoupdate
// --check` exits 0 whether it proved everything or nothing.
//
// That is what closes the design's open risk ("Changing the check's outcome may
// change its exit code") by MEASUREMENT rather than by assumption, and it is why
// sub-task 4.1 is free to move packages between the three columns.
//
// This test is the pin. It is written and passing BEFORE 4.1 lands, deliberately:
// written afterwards it would pin the NEW behaviour and prove nothing about
// whether the change moved the exit code.

// TestCheckExitCodeUnchanged is that pin. A run containing one proved, one
// failed and one not-validated package must reach its end without the process
// being exited — including the FAILED one, which is the row a future author is
// most likely to reach for osExit over.
func TestCheckExitCodeUnchanged(t *testing.T) {
	var exits []int
	origExit := osExit
	osExit = func(code int) { exits = append(exits, code) }
	t.Cleanup(func() { osExit = origExit })

	plan := buildValidationPlan(checkPlanUpdates(), checkPlanPolicy())

	outcomes := map[string]validate.Outcome{
		"media-plugins/gst-plugins-qt6": validate.OutcomeFailed,
		"dev-libs/quiet":                validate.OutcomePass,
		"app-editors/vscode-bin":        validate.OutcomeSkipped,
	}

	var tally validationTally
	_ = captureStdout(t, func() {
		tally = runValidationCheck(plan, func(entry validationPlanEntry) validate.EbuildResult {
			outcome := outcomes[entry.Package]
			res := validate.EbuildResult{
				Package: entry.Package,
				Version: entry.Version,
				Depth:   entry.Depth,
				Gates:   []validate.GateResult{{Gate: validate.GateOptions, Outcome: outcome, Reason: "fixture"}},
			}
			if outcome == validate.OutcomeFailed {
				res.Gates[0].Findings = []validate.Finding{{
					Gate: validate.GateOptions, Severity: validate.SeverityError, Detail: "-Daalib= is undeclared upstream",
				}}
			}
			return res
		})
	})

	if len(exits) != 0 {
		t.Errorf("the check exited with %v; today it exits 0 whatever the tally says, and a change that moves "+
			"the exit code is a CI-visible behaviour change needing its own Unchanged-Behavior assertion", exits)
	}

	// The fixture has to have exercised all three columns, or the assertion above
	// is about a run that never met the interesting case.
	if tally.Proved+tally.Errored+tally.Skipped != len(plan.Entries) {
		t.Fatalf("the tally accounts for %d of %d planned packages; this test cannot fail for its own reason",
			tally.Proved+tally.Errored+tally.Skipped, len(plan.Entries))
	}
	if tally.Errored == 0 {
		t.Fatal("no package errored in this run, so the row most likely to justify a non-zero exit was never reached")
	}
}

// --- story 043, sub-task 4.1 ------------------------------------------------
//
// APPENDED to this existing file, not replacing it: the tests above are the
// check path's own suite and stay exactly as they are.

// R4.1 + R4.4 — dev-libs/icu-compat, verbatim from the 2026-08-22 run: two
// SKIPPED gates and two PASS, one of which is the depth the policy selected.
// The old rule collapsed all four into "not validated" and then printed the
// FIRST skip reason, so the operator was told about a missing distfile and
// never told the package had configured.
func TestCheckOutcomeNamesTheDepthReached(t *testing.T) {
	res := validate.EbuildResult{
		Depth: "configure",
		Gates: []validate.GateResult{
			{Gate: validate.GateOptions, Outcome: validate.OutcomeSkipped, Reason: "no distfile named by the Manifest is present"},
			{Gate: validate.GateReview, Outcome: validate.OutcomeSkipped, Reason: "no archive on disk for the previous version"},
			{Gate: validate.GatePatches, Outcome: validate.OutcomePass},
			{Gate: validate.GateConfigure, Outcome: validate.OutcomePass},
		},
	}
	entry := validationPlanEntry{Package: "dev-libs/icu-compat", Version: "78.3", Depth: "configure"}

	outcome, detail := checkOutcome(res, entry)
	if outcome != validate.OutcomePass {
		t.Errorf("outcome = %v, want PASS: the configure gate is the depth the policy selected and it passed", outcome)
	}
	if !contains(detail, "configure") || !contains(detail, "patches") {
		t.Errorf("detail %q does not name the gates that passed (R4.4)", detail)
	}
}

// R4.2 — nothing here relaxes failure. A FAILED gate outranks every PASS,
// exactly as today.
func TestCheckOutcomeStillFailsOnAFailedGate(t *testing.T) {
	res := validate.EbuildResult{
		Depth: "configure",
		Gates: []validate.GateResult{
			{Gate: validate.GatePatches, Outcome: validate.OutcomePass},
			{Gate: validate.GateConfigure, Outcome: validate.OutcomeFailed, Reason: "configure died"},
		},
	}
	outcome, _ := checkOutcome(res, validationPlanEntry{Depth: "configure"})
	if outcome != validate.OutcomeFailed {
		t.Errorf("outcome = %v, want FAILED", outcome)
	}
}

// R4.3 — the case the strict rule exists for is preserved: when the selected
// depth's gate did not pass, nothing was proved and the reason is named. A
// vacuous `patches` PASS must NOT be enough (this is why RC4-c was rejected).
func TestCheckOutcomeWithNoDepthGatePass(t *testing.T) {
	cases := []struct {
		name  string
		gates []validate.GateResult
	}{
		{"all skipped", []validate.GateResult{
			{Gate: validate.GateOptions, Outcome: validate.OutcomeSkipped, Reason: "binary record"},
		}},
		{"only a vacuous patches pass", []validate.GateResult{
			{Gate: validate.GatePatches, Outcome: validate.OutcomePass},
			{Gate: validate.GateConfigure, Outcome: validate.OutcomeSkipped, Reason: "build system undetermined"},
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			res := validate.EbuildResult{Depth: "configure", Gates: tc.gates}
			outcome, detail := checkOutcome(res, validationPlanEntry{Depth: "configure"})
			if outcome == validate.OutcomePass {
				t.Error("nothing measured the selected depth, so nothing was proved")
			}
			if detail == "" {
				t.Error("a skip with no reason reads as a result (R4.3)")
			}
		})
	}
}

// R4.2, Unchanged Behavior — an unparseable depth must fall through to
// not-validated, never guess a depth to compare against.
func TestCheckOutcomeWithUnparseableDepth(t *testing.T) {
	res := validate.EbuildResult{
		Depth: "",
		Gates: []validate.GateResult{{Gate: validate.GatePatches, Outcome: validate.OutcomePass}},
	}
	if outcome, _ := checkOutcome(res, validationPlanEntry{Depth: "nonsense"}); outcome == validate.OutcomePass {
		t.Error("a depth nobody could parse must not produce a proved verdict")
	}
}

// Unchanged Behavior 6 — `bentoo overlay validate` shares WorstOutcome and is
// deliberately left byte-identical, so the new rule must not have been smuggled
// into it. The four gate sets below are the ones whose readings DIVERGE between
// the two rules; if WorstOutcome ever agreed with checkOutcome on all of them,
// one of the two had been changed to be the other.
func TestWorstOutcomeIsUnchangedByTheCheckPathRule(t *testing.T) {
	for _, tc := range []struct {
		name  string
		gates []validate.GateResult
		want  validate.Outcome
	}{
		{"the icu-compat shape: two skips outrank two passes", []validate.GateResult{
			{Gate: validate.GateOptions, Outcome: validate.OutcomeSkipped, Reason: "no distfile"},
			{Gate: validate.GateReview, Outcome: validate.OutcomeSkipped, Reason: "no archive"},
			{Gate: validate.GatePatches, Outcome: validate.OutcomePass},
			{Gate: validate.GateConfigure, Outcome: validate.OutcomePass},
		}, validate.OutcomeSkipped},
		{"every deciding gate passed", []validate.GateResult{
			{Gate: validate.GatePatches, Outcome: validate.OutcomePass},
			{Gate: validate.GateConfigure, Outcome: validate.OutcomePass},
		}, validate.OutcomePass},
		{"a failure outranks everything", []validate.GateResult{
			{Gate: validate.GatePatches, Outcome: validate.OutcomePass},
			{Gate: validate.GateConfigure, Outcome: validate.OutcomeFailed, Reason: "died"},
		}, validate.OutcomeFailed},
		{"no deciding gate at all", nil, validate.OutcomeSkipped},
	} {
		t.Run(tc.name, func(t *testing.T) {
			res := validate.EbuildResult{Depth: "configure", Gates: tc.gates}
			if got := res.WorstOutcome(); got != tc.want {
				t.Errorf("WorstOutcome = %v, want %v — `overlay validate` consumes this and is out of scope (Unchanged Behavior 6)", got, tc.want)
			}
		})
	}
}

// --- orchestrator-authored, Run mode ----------------------------------------
//
// Two guards closing gaps the sub-task 4.1 mutation proof measured. Recorded in
// .draft/deviations.yaml.

// R4.2, Unchanged Behavior — the anti-guess guard, widened.
//
// TestCheckOutcomeWithUnparseableDepth above carries only a `patches` PASS, so a
// mutant that GUESSES a depth is caught only when the guess resolves to
// `patches`: guessing `configure` lands on a gate the fixture does not have, so
// the verdict is not-validated anyway and the assertion is never reached. That
// was measured, not suspected — mutating the fallback to guess DepthConfigure
// left the suite green.
//
// A gate set holding a PASS at every rung a guess could name closes it: whatever
// the mutant guesses, it finds a PASS, and only refusing to guess at all keeps
// the verdict honest.
func TestCheckOutcomeWithUnparseableDepthRefusesEveryGuess(t *testing.T) {
	res := validate.EbuildResult{
		Depth: "",
		Gates: []validate.GateResult{
			{Gate: validate.GateOptions, Outcome: validate.OutcomePass},
			{Gate: validate.GatePatches, Outcome: validate.OutcomePass},
			{Gate: validate.GateConfigure, Outcome: validate.OutcomePass},
			{Gate: validate.GateInstall, Outcome: validate.OutcomePass},
		},
	}
	outcome, detail := checkOutcome(res, validationPlanEntry{Depth: "nonsense"})
	if outcome == validate.OutcomePass {
		t.Errorf("a depth nobody could parse produced a proved verdict (%q); every rung passed here, "+
			"so ANY guess finds a PASS and only refusing to guess keeps the answer honest", detail)
	}
	if detail == "" {
		t.Error("a verdict with no reason reads as a result (R4.3)")
	}
}

// R4.1 — the fallback ORDER, which nothing else pins.
//
// checkOutcome takes the selected depth from entry.Depth and falls back to
// result.Depth only when the entry names none. The inverse order is circular:
// result.Depth is the depth the run REACHED, so the gate for it passed by
// definition and every result would read as proved — the exact over-report this
// story exists to remove, arrived at from the other side.
//
// Both orders pass every other test in this file, because the only fixture that
// exercises the fallback has result.Depth == "". This one separates them: the
// plan asked for `compile`, the run only reached `patches`, and `patches` passed.
// Read entry-first that is not validated; read result-first it is proved.
func TestCheckOutcomePrefersThePlannedDepthOverTheDepthReached(t *testing.T) {
	res := validate.EbuildResult{
		Depth: "patches",
		Gates: []validate.GateResult{
			{Gate: validate.GatePatches, Outcome: validate.OutcomePass},
			{Gate: validate.GateConfigure, Outcome: validate.OutcomeSkipped, Reason: "the build did not get this far"},
		},
	}
	outcome, detail := checkOutcome(res, validationPlanEntry{Package: "dev-libs/x", Version: "2.0", Depth: "compile"})
	if outcome == validate.OutcomePass {
		t.Errorf("a bump asked for `compile` that only reached `patches` reads as proved (%q); "+
			"taking the depth from the RESULT is circular — the reached depth's gate passed by definition", detail)
	}
	if detail == "" {
		t.Error("a verdict with no reason reads as a result (R4.3)")
	}
}
