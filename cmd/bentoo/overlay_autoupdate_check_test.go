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
	"bytes"
	"fmt"
	"io"
	"os"
	"regexp"
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/obentoo/bentoolkit/internal/autoupdate"
	"github.com/obentoo/bentoolkit/internal/autoupdate/validate"
	"github.com/obentoo/bentoolkit/internal/common/report"
	"github.com/obentoo/bentoolkit/internal/common/report/render"
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

	out := captureStdout(t, func() { printValidationPrice(plan) })

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

	var tally report.Tally
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
		}).Tally
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

	out := captureStdout(t, func() { printValidationPrice(plan) })

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

// ---------------------------------------------------------------------------
// Sub-task 6.5 — the check renders through the report. APPENDED.
// ---------------------------------------------------------------------------

// renderCheckReport renders the fixture report in one mode and returns what
// reached stdout. It is the seam between "the command built a report" and "the
// operator read something", which is the only place the two can disagree.
func renderCheckReport(t *testing.T, mode report.Mode) string {
	t.Helper()

	original := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("creating the capture pipe: %v", err)
	}
	os.Stdout = w

	captured := make(chan string, 1)
	go func() {
		var buf bytes.Buffer
		_, _ = io.Copy(&buf, r)
		captured <- buf.String()
	}()

	renderErr := func() error {
		defer func() {
			_ = w.Close()
			os.Stdout = original
		}()
		return renderCheckReportIn(mode, exportFixture(), render.Options{Width: 100})
	}()

	out := <-captured
	_ = r.Close()

	if renderErr != nil {
		t.Fatalf("rendering in %q: %v", mode, renderErr)
	}
	return out
}

// TestCheckRendersThroughReport is the end of the wire this story exists to
// build: the command's output comes from the view model rather than from
// Printf calls scattered across the moment each section prints.
func TestCheckRendersThroughReport(t *testing.T) {
	out := renderCheckReport(t, report.ModePlain)

	if strings.IndexByte(out, 0x1b) >= 0 {
		t.Error("the plain render carries an escape sequence — this is what cron captures (R2.1)")
	}

	fixture := exportFixture()
	if !fixture.Reconciles() {
		t.Errorf("the tally does not reconcile: %+v sums to %d over a plan of %d (R5.5)",
			fixture.Tally, fixture.Tally.Total(), len(fixture.Plan))
	}

	for _, column := range []string{"proved", "errored", "inconclusive", "skipped"} {
		if !strings.Contains(strings.ToLower(out), column) {
			t.Errorf("the rendered check does not name the %q column (R5.1)", column)
		}
	}
}

// TestModesAgreeOnContent is R2.4 measured at the command rather than at the
// renderer. Each mode is compared on ANSI-stripped content, not merely observed
// to run without error — a mode that dropped a section would pass the weaker
// check, and three table styles on one screen is exactly what came of nobody
// making the stronger one.
func TestModesAgreeOnContent(t *testing.T) {
	plain := stripForComparison(renderCheckReport(t, report.ModePlain))

	for _, mode := range []report.Mode{report.ModeInline} {
		got := stripForComparison(renderCheckReport(t, mode))
		if got == plain {
			continue
		}

		plainLines, gotLines := strings.Split(plain, "\n"), strings.Split(got, "\n")
		for i := 0; i < len(plainLines) || i < len(gotLines); i++ {
			var p, g string
			if i < len(plainLines) {
				p = plainLines[i]
			}
			if i < len(gotLines) {
				g = gotLines[i]
			}
			if p != g {
				t.Fatalf("%q and plain diverge at line %d (R2.4 — the modes differ in presentation, not in content)\n  plain: %q\n  %s: %q",
					mode, i+1, p, mode, g)
			}
		}
	}
}

// stripForComparison removes styling and the trailing padding a styled renderer
// leaves behind. Trailing spaces are invisible on a terminal and are exactly
// the "presentation" R2.4 permits the modes to differ in.
func stripForComparison(s string) string {
	lines := strings.Split(ansi.Strip(s), "\n")
	for i, line := range lines {
		lines[i] = strings.TrimRight(line, " \t")
	}
	return strings.Join(lines, "\n")
}

// TestNoLineExceedsWidth pins R6.3 on the command's real output. A line wider
// than the terminal wraps, and one wrapped line destroys the alignment of every
// column below it — which is the defect the four hand-typed %-45s produced and
// could not fix, because the correct width depends on the packages in THIS run.
func TestNoLineExceedsWidth(t *testing.T) {
	const width = 100

	for i, line := range strings.Split(renderCheckReport(t, report.ModePlain), "\n") {
		if w := lipgloss.Width(line); w > width {
			t.Errorf("line %d is %d cells wide, %d over the %d asked for: %q", i+1, w, w-width, width, line)
		}
	}
}

// TestCheckPathHasNoHardCodedWidths pins R6.3 mechanically, in the file the
// story names. The three %-45s sites OUTSIDE the check path are explicitly Out
// of Scope and must not be touched, so this is scoped to the one file.
func TestCheckPathHasNoHardCodedWidths(t *testing.T) {
	body, err := os.ReadFile("overlay_autoupdate_check.go")
	if err != nil {
		t.Fatalf("reading the check path: %v", err)
	}

	if matches := regexp.MustCompile(`%-\d+s`).FindAllString(string(body), -1); len(matches) > 0 {
		t.Errorf("overlay_autoupdate_check.go still declares %d hard-coded field width(s): %v (R6.3). "+
			"A width typed into a format string cannot be right: the correct one depends on the packages this run produced.",
			len(matches), matches)
	}
}

// TestOutOfScopeWidthsAreUntouched is the other half, and it guards the story's
// own boundary. Removing those three would be an improvement nobody asked for,
// in files this story does not test.
func TestOutOfScopeWidthsAreUntouched(t *testing.T) {
	for _, file := range []string{"overlay_prune.go", "overlay_autoupdate_sweep.go"} {
		body, err := os.ReadFile(file)
		if err != nil {
			t.Fatalf("reading %s: %v", file, err)
		}
		if !regexp.MustCompile(`%-\d+s`).MatchString(string(body)) {
			t.Errorf("%s no longer declares a hard-coded width — it is listed Out of Scope and should have been left alone", file)
		}
	}
}
