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
	"go/ast"
	"go/parser"
	"go/token"
	"io"
	"os"
	"regexp"
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/obentoo/bentoolkit/internal/autoupdate"
	"github.com/obentoo/bentoolkit/internal/autoupdate/validate"
	"github.com/obentoo/bentoolkit/internal/common/config"
	"github.com/obentoo/bentoolkit/internal/common/logger"
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

// ---- story 045, sub-tasks 3.1 and 3.2: the structural claims ----
//
// These replace behavioural phrasings that were NOT expressible: runCheck and
// runPendingValidation need a Checker, a config dir and a network, and this
// package's tests build none — they compose the individual producers by hand
// (see renderCheckReport at :586). A rule stated where no test can reach it is
// a rule nobody enforces, which is how RC1 happened in the first place.

// callSites counts calls to name in file, excluding the declaration. A `func`
// line is a definition, not a call, and counting it turns "moved" into "still
// here" — the exact vacuity these guards exist to avoid.
func callSites(t *testing.T, file, name string) int {
	t.Helper()

	src, err := os.ReadFile(file)
	if err != nil {
		t.Fatalf("reading %s: %v", file, err)
	}
	n := 0
	lines := strings.Split(string(src), "\n")
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "func ") || strings.HasPrefix(trimmed, "//") {
			continue
		}
		n += strings.Count(line, name+"(")
	}
	if len(lines) < 2 {
		t.Fatalf("%s scanned as %d line(s) — the guard would pass vacuously", file, len(lines))
	}
	return n
}

// TestValidationDoesNotRenderTheReport is D2: the --llm gate keeps "do not
// validate" and loses "do not draw". After 3.1 nothing downstream of that gate
// draws, so the render call must not live in this file at all.
func TestValidationDoesNotRenderTheReport(t *testing.T) {
	if got := callSites(t, "overlay_autoupdate_check.go", "presentCheckReport"); got != 0 {
		t.Errorf("overlay_autoupdate_check.go still calls presentCheckReport %d time(s), want 0 — the render belongs to runCheck, not behind the --llm gate (D2, R3.1-R3.3)", got)
	}
}

// TestPendingValidationReturnsThePlanHalf is D2 stated as a return value rather
// than as a comment. The --llm gate keeps meaning "do not validate" and stops
// meaning "do not draw", and the way it stops meaning it is that the guarded
// arm now YIELDS something instead of ending the run's presentation.
//
// # Why the not-validating arm is the one a test can reach
//
// Every other path through runPendingValidation needs a Checker, a config
// directory and a network, and this package builds none of them — its tests
// compose the individual producers by hand (see renderCheckReport at :586). The
// `if !autoupdateLLM` arm returns before any of that, so it is the one arm
// callable with nothing but a context. That is a narrow claim and it is stated
// narrowly; the structural half above is what covers the rest.
//
// # The zero value is the assertion, not merely the absence of a crash
//
// D2 says the guard "stays exactly where it is and yields the zero value". A
// half-built report from this arm would be worse than none: runCheck merges
// this half into the report it already holds, so a Plan or a Tally invented
// here would be merged into a run that never validated anything and reported as
// if it had.
func TestPendingValidationReturnsThePlanHalf(t *testing.T) {
	restore := autoupdateLLM
	autoupdateLLM = false
	t.Cleanup(func() { autoupdateLLM = restore })

	got, printed := runPendingValidation(t.Context(), t.TempDir(), t.TempDir(),
		[]autoupdate.CheckResult{{Package: "app-misc/jq", CurrentVersion: "1.7.1", UpstreamVersion: "1.8.0", HasUpdate: true, Type: "source"}},
		config.LLMConfig{})

	if printed {
		t.Error("the gated arm reported the plan as printed, but it never reached printValidationPrice — SkipPlan would then omit a section nobody had shown (R2.3)")
	}
	if len(got.Scanned) != 0 || len(got.Plan) != 0 || len(got.Results) != 0 {
		t.Errorf("the gated arm returned a populated report (Scanned=%d, Plan=%d, Results=%d), want the zero value — it did not validate, so it has nothing to contribute (D2)",
			len(got.Scanned), len(got.Plan), len(got.Results))
	}
	if got.Complete || got.NotEvaluated != 0 || got.DistfilesToFetch != 0 || got.Tally != (report.Tally{}) {
		t.Errorf("the gated arm returned a non-zero report body (Complete=%v, NotEvaluated=%d, DistfilesToFetch=%d, Tally=%+v), want the zero value (D2)",
			got.Complete, got.NotEvaluated, got.DistfilesToFetch, got.Tally)
	}
	// Scanned is filled by runCheck from the scan it already holds, never here:
	// a report half that carried the scan would make the merge in 3.2 a question
	// of which copy wins.
	if got.Scanned != nil {
		t.Error("the gated arm returned a non-nil Scanned — the scan half belongs to runCheck (D1)")
	}
}

// ---- story 045, sub-task 3.2: one report, rendered once ----

// story045Scanned is the scan half of a run, in both shapes the two producers
// take: []autoupdate.CheckResult for the legacy printer and []report.PackageResult
// for the view model. They describe the SAME two packages on purpose — the
// defect is that both get printed, so a fixture where they disagree would be
// measuring something else.
func story045Scanned() ([]autoupdate.CheckResult, []report.PackageResult) {
	return []autoupdate.CheckResult{
		{Package: "app-misc/jq", CurrentVersion: "1.7.1", UpstreamVersion: "1.8.0", HasUpdate: true, Type: "source"},
		{Package: "app-editors/zed", CurrentVersion: "0.199.4", UpstreamVersion: "0.199.4", Type: "bin"},
	}, []report.PackageResult{
		{Package: "app-misc/jq", CurrentVersion: "1.7.1", CandidateVersion: "1.8.0", HasUpdate: true, Type: "source"},
		{Package: "app-editors/zed", CurrentVersion: "0.199.4", CandidateVersion: "0.199.4", Type: "bin"},
	}
}

// TestValidationPlanHeadingAppearsOnce is the same assertion for the half story
// 044's deferred quality gate did NOT name. printValidationPrice emits
// "Validation Plan" at overlay_autoupdate_check.go:320 before the confirmation,
// and the report emits its own at render/text.go:327 afterwards.
func TestValidationPlanHeadingAppearsOnce(t *testing.T) {
	_, scanned := story045Scanned()
	plan := buildValidationPlan(checkPlanUpdates(), checkPlanPolicy())
	r := report.Report{
		Scanned:  scanned,
		Plan:     []report.PlanEntry{{Package: "app-misc/jq", CurrentVersion: "1.7.1", CandidateVersion: "1.8.0", Depth: "configure"}},
		Complete: true,
	}

	out := captureStdout(t, func() {
		printValidationPrice(plan)
		presentCheckReport(r, true)
	})

	if got := strings.Count(out, "Validation Plan"); got != 1 {
		t.Errorf("the heading %q appears %d times, want exactly 1 — the pre-confirmation print is the ONE permitted producer (R2.3), so the report must omit its own section",
			"Validation Plan", got)
	}
}

// TestCheckOwnsTheRender is the other half, and it is why the pair cannot pass
// vacuously: moving the call out of one file proves nothing unless it arrived
// in the other. Both the batch path and the single-package path render from
// here, so this one claim covers what "renders without --llm" and "a single
// package renders" were each trying to say.
func TestCheckOwnsTheRender(t *testing.T) {
	if got := callSites(t, "overlay_autoupdate.go", "presentCheckReport"); got < 1 {
		t.Errorf("overlay_autoupdate.go calls presentCheckReport %d time(s), want at least 1 — runCheck owns the render on both entry paths (D1, R1.1, R1.2)", got)
	}
}

// TestSinglePackageDoesNotReconcile is D6. Only the batch path may reconcile the
// registry — overlay_autoupdate.go:793-794 states it — and the single-package
// path keeps its early return. Green on arrival: it guards a property that is
// already true and that this story could plausibly break while moving the
// render past that return.
func TestSinglePackageDoesNotReconcile(t *testing.T) {
	if got := callSites(t, "overlay_autoupdate.go", "reconcileRegistryAfterCheck"); got != 1 {
		t.Errorf("reconcileRegistryAfterCheck has %d call site(s), want exactly 1 — only the batch path reconciles (D6)", got)
	}
}

// TestSinglePackageReturnsBeforeTheBatchPath closes a gap in the guard above,
// and the gap was found by running the mutation .draft/red-evidence.yaml
// prescribes rather than by reading it.
//
// That entry justifies TestSinglePackageDoesNotReconcile like this: "3.2 moves
// the render past the single-package early return, and a restructure that
// dropped that return would start reconciling the registry from a one-package
// run. Mutation after 3.2: remove the early return and confirm this test
// fails." It does not fail. Deleting the return leaves the CALL SITE COUNT at
// one, so a test that counts call sites cannot see it — the failure mode is
// about REACHABILITY, and counting is blind to reachability by construction.
//
// Measured on 2026-08-24, both mutations run against the finished 3.2:
//   - add a second reconcileRegistryAfterCheck call site -> the count guard FAILS (correct)
//   - delete the single-package `return`              -> the count guard PASSES (the gap)
//
// This test covers the second. Falling through from the single-package branch
// reaches CheckAll, the registry fixer prompt and the reconcile — so a run the
// operator scoped to one package would scan the whole overlay and offer to
// publish pins. That is D6's whole subject, and it was the half nothing watched.
func TestSinglePackageReturnsBeforeTheBatchPath(t *testing.T) {
	const file = "overlay_autoupdate.go"

	parsed, err := parser.ParseFile(token.NewFileSet(), file, nil, 0)
	if err != nil {
		t.Fatalf("parsing %s: %v", file, err)
	}

	var branch *ast.IfStmt
	ast.Inspect(parsed, func(n ast.Node) bool {
		fn, ok := n.(*ast.FuncDecl)
		if !ok || fn.Name.Name != "runCheck" {
			return true
		}
		ast.Inspect(fn, func(inner ast.Node) bool {
			stmt, ok := inner.(*ast.IfStmt)
			if !ok {
				return true
			}
			// `if len(args) > 0` — the single-package arm.
			cmp, ok := stmt.Cond.(*ast.BinaryExpr)
			if !ok || cmp.Op != token.GTR {
				return true
			}
			call, ok := cmp.X.(*ast.CallExpr)
			if !ok || len(call.Args) != 1 {
				return true
			}
			if name, ok := call.Fun.(*ast.Ident); ok && name.Name == "len" {
				if arg, ok := call.Args[0].(*ast.Ident); ok && arg.Name == "args" {
					branch = stmt
					return false
				}
			}
			return true
		})
		return false
	})

	// Vacuity guard: a rename of `args`, or a restructure that replaced the
	// branch with something else, must fail loudly rather than silently stop
	// measuring anything.
	if branch == nil {
		t.Fatalf("could not find `if len(args) > 0` inside runCheck in %s — the single-package arm was renamed or restructured, and this guard now measures nothing (D6)", file)
	}
	if len(branch.Body.List) == 0 {
		t.Fatalf("the single-package arm is empty in %s", file)
	}

	if _, ok := branch.Body.List[len(branch.Body.List)-1].(*ast.ReturnStmt); !ok {
		t.Errorf("the single-package arm of runCheck does not end in a return. " +
			"Falling through reaches CheckAll, the registry fixer prompt and reconcileRegistryAfterCheck, " +
			"so a run scoped to one package would scan the whole overlay and offer to publish pins (D6, S045-R1.2)")
	}
}

// ---- story 045, sub-tasks 3.3 and 3.4: the empty scan, and the hint ----

// noPlanPrinted names the second argument presentCheckReport gained in
// sub-task 3.2. None of these fixtures ran printValidationPrice, so none of
// them may ask the report to omit a plan section nobody has seen — a bare
// `false` at four call sites would say nothing about which of the two it is.
const noPlanPrinted = false

// TestEmptyScanRendersNoReport is D4 held at the render seam rather than
// scattered through runCheck. A run that scanned nothing renders nothing at all:
// the sentence is logger.Info's (suppressible by --quiet, cmd/bentoo/main.go:30)
// and the report's own "No package is configured for autoupdate." lead
// (render/text.go:272) must never be reached on that path.
//
// Putting the decision here rather than in runCheck is what makes it testable —
// runCheck needs a checker, a config dir and a network, and this package's tests
// do not build one.
func TestEmptyScanRendersNoReport(t *testing.T) {
	out := captureStdout(t, func() {
		presentCheckReport(report.Report{Complete: true}, noPlanPrinted)
	})

	if strings.TrimSpace(out) != "" {
		t.Errorf("an empty scan rendered %d bytes; it must render nothing so --quiet keeps the one silence it has today (R5.3)\n%s",
			len(out), out)
	}
}

// TestNonEmptyScanStillRenders is the hostile half of the rule above, and it is
// the half that matters: "render nothing when empty" is satisfied perfectly by
// an implementation that renders nothing ever. This is the fixture that would
// make that implementation fail.
func TestNonEmptyScanStillRenders(t *testing.T) {
	r := report.Report{Complete: true, Scanned: []report.PackageResult{
		{Package: "app-misc/jq", Type: "source", CurrentVersion: "1.7.1", CandidateVersion: "1.8.0", HasUpdate: true},
	}}

	out := captureStdout(t, func() { presentCheckReport(r, noPlanPrinted) })

	if !strings.Contains(out, "Version Check Results") {
		t.Errorf("a scan with one package rendered no version check — the empty-scan rule must not swallow real runs (R4.1)\n%s", out)
	}
}

// TestEmptyScanUnderQuietIsSilent is the half of D4 that states WHY the empty
// scan is special, and it is the reason D4 exists at all.
//
// # --quiet reaches one of the three channels this command writes on
//
// Its whole effect is logger.SetQuiet(true) (cmd/bentoo/main.go:30-32). It never
// reaches the output package and never reaches os.Stdout. So today a
// `--check --quiet` over an empty registry prints NOTHING — the sentence is
// logger.Info's and is suppressed — while a `--check --quiet` with packages
// prints the whole table anyway. That asymmetry already exists, and D4's rule is
// to keep it rather than to invent a new one.
//
// Routing the empty-scan sentence through the report would have taken the one
// silence a quiet run has. This asserts it was not taken.
//
// # Both halves, because either alone is satisfiable by the wrong code
//
// "Silent under quiet" alone is satisfied by a command that renders nothing
// ever. "Still renders under quiet" alone is satisfied by one that ignores the
// empty case. Only together do they pin the asymmetry: the silence comes from
// the scan being empty, never from quiet, and --quiet did not become a report
// suppressor. The logger's own half is covered where it belongs, by
// TestQuietModeSuppressesInfoMessages in internal/common/logger.
func TestEmptyScanUnderQuietIsSilent(t *testing.T) {
	logger.SetQuiet(true)
	t.Cleanup(func() { logger.Default().SetLevel(logger.LevelInfo) })

	empty := captureStdout(t, func() {
		presentCheckReport(report.Report{Complete: true}, noPlanPrinted)
	})
	if strings.TrimSpace(empty) != "" {
		t.Errorf("a quiet run over an empty scan put %d bytes on stdout, which --quiet cannot reach — the one silence a quiet run has today would be gone (R5.3, D4)\n%s",
			len(empty), empty)
	}

	scanned := captureStdout(t, func() {
		presentCheckReport(report.Report{Complete: true, Scanned: []report.PackageResult{
			{Package: "app-misc/jq", Type: "source", CurrentVersion: "1.7.1", CandidateVersion: "1.8.0", HasUpdate: true},
		}}, noPlanPrinted)
	})
	if !strings.Contains(scanned, "Version Check Results") {
		t.Errorf("a quiet run over a NON-empty scan rendered nothing — --quiet must not become a report suppressor, and the silence above must come from the empty scan alone (D4)\n%s", scanned)
	}
}

// TestEmptyScanWithValidationStillRenders is the third hostile half of D4, and
// it is the one nobody had written: the empty-scan guard must not swallow a run
// that VALIDATED something.
//
// # The state is reachable, which is why this is a test and not a note
//
// CheckAll skips disabled and held entries, and DisableOrphans auto-disables an
// entry whose ebuild has vanished — so "every registry entry disabled, and a
// pending.json still on disk from an earlier run" produces exactly this shape:
// an empty Scanned beside a plan that was built, confirmed and evaluated. Before
// this story runPendingValidation drew that report itself; keying the silence on
// Scanned alone would discard it, and what is discarded is potentially hours of
// gate work the operator waited for and approved.
//
// # R5.3's subject is an empty REGISTRY, not an empty Scanned slice
//
// "WHEN no package is configured for autoupdate" describes a run with nothing to
// say. A run holding results has something to say however its scan came out, so
// the guard is a conjunction: silent only when BOTH halves are empty.
func TestEmptyScanWithValidationStillRenders(t *testing.T) {
	r := report.Report{
		Complete: true,
		Plan:     []report.PlanEntry{{Package: "app-misc/jq", CurrentVersion: "1.7.1", CandidateVersion: "1.8.0", Depth: "configure"}},
		Results:  []report.ValidationRow{{Package: "app-misc/jq", Outcome: "proved"}},
		Tally:    report.Tally{Proved: 1},
	}

	out := captureStdout(t, func() { presentCheckReport(r, noPlanPrinted) })

	if !strings.Contains(out, "app-misc/jq") {
		t.Errorf("a run that evaluated a package rendered nothing because its scan came out empty — the gates ran and their answer was discarded (D4, R5.3)\n%s", out)
	}
}

// TestListHintAppearsOnce is R5.2. The hint names a bentoo subcommand, so D5
// puts it here, in the caller, and NOT in versionCheckSection — a renderer under
// internal/common must not know one binary's command names. R1.4 permits it
// because a hint is not a package name, a version, a plan entry or a tally.
func TestListHintAppearsOnce(t *testing.T) {
	r := report.Report{Complete: true, Scanned: []report.PackageResult{
		{Package: "app-misc/jq", Type: "source", CurrentVersion: "1.7.1", CandidateVersion: "1.8.0", HasUpdate: true},
		{Package: "app-misc/yq", Type: "source", CurrentVersion: "4.44.1", CandidateVersion: "4.45.0", HasUpdate: true},
	}}

	out := captureStdout(t, func() { presentCheckReport(r, noPlanPrinted) })

	// Two pending updates, one hint — not one per package, and not one per section.
	if got := strings.Count(out, "--list"); got != 1 {
		t.Errorf("the pending-updates hint appears %d times over a 2-update scan, want exactly 1 (R5.2)\n%s", got, out)
	}
}

// TestListHintAbsentWithNoUpdates keeps the hint from becoming unconditional
// noise: a run that found nothing to update has nothing to list.
func TestListHintAbsentWithNoUpdates(t *testing.T) {
	r := report.Report{Complete: true, Scanned: []report.PackageResult{
		{Package: "app-editors/zed", Type: "bin", CurrentVersion: "0.199.4", CandidateVersion: "0.199.4"},
	}}

	out := captureStdout(t, func() { presentCheckReport(r, noPlanPrinted) })

	if strings.Contains(out, "--list") {
		t.Errorf("the hint was printed for a scan with no pending update (R5.2)\n%s", out)
	}
}

// ---- story 045, sub-task 4.1: the guard that outlives the fix ----

// TestCheckPathHasOneProducerPerHeading is the durable half of R1.4, and it
// exists because the OTHER half stopped working the moment it passed.
//
// That other half (3.2) counted what two producers wrote to a terminal. It was
// retired in 4.1 alongside the legacy check printer it called: with one producer
// left there is nothing to call twice, so it could no longer fail however badly
// a future change reintroduced a second one. Counting OUTPUT needs the whole
// command, and this package's tests do not run the whole command — they compose
// the producers by hand.
//
// So this counts PRODUCERS instead, in the source, which is the thing RC1 says
// nobody was watching. It was red until 4.1 removed the second emitter of the
// version-check heading from cmd/bentoo, and it is what goes red again if one
// comes back — that heading belongs to the report.
//
// "Validation Plan" is the deliberate asymmetry: printValidationPrice keeps it
// (R2.3), so exactly one emitter is correct and zero would be a regression.
func TestCheckPathHasOneProducerPerHeading(t *testing.T) {
	for _, want := range []struct {
		heading string
		count   int
		why     string
	}{
		{"Version Check Results", 0, "this heading belongs to the report; cmd/bentoo must not emit it (R1.4)"},
		{"Validation Plan", 1, "printValidationPrice is the ONE permitted producer, kept for the pre-confirmation print (R2.3)"},
	} {
		got := countHeadingEmitters(t, want.heading)
		if got != want.count {
			t.Errorf("cmd/bentoo has %d producer(s) of %q, want %d — %s",
				got, want.heading, want.count, want.why)
		}
	}
}

// countHeadingEmitters counts string literals equal to heading in this package's
// non-test .go files. A literal is what a producer needs, so the literal is what
// is counted — no call graph, nothing to keep in sync with a refactor.
// fmt.Sprintf(%q) rather than strconv.Quote: this package's test file does not
// import strconv, and a fragment is APPENDED to that file, not written with its
// own import block.
func countHeadingEmitters(t *testing.T, heading string) int {
	t.Helper()

	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("reading the package directory: %v", err)
	}

	total := 0
	scanned := 0
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		scanned++
		src, err := os.ReadFile(name)
		if err != nil {
			t.Fatalf("reading %s: %v", name, err)
		}
		total += strings.Count(string(src), fmt.Sprintf("%q", heading))
	}

	// Anti-vacuity: a guard that scanned nothing reports 0 and looks satisfied.
	if scanned == 0 {
		t.Fatal("scanned no non-test .go file — the guard would pass vacuously")
	}
	return total
}

// TestOrphanDisableIsAnnounced closes the last R5.4 parity gap, and it is the
// one that was not about display at all.
//
// # A silent write to a hand-maintained file
//
// CheckAll auto-disables entries whose ebuild has vanished
// (internal/autoupdate/checker.go:2020) and warns only when that write FAILS,
// so a successful one says nothing. displayCheckResults used to be the thing
// that said it — "N package(s) had no ebuild and were disabled (enabled =
// false)" — and sub-task 3.2 removed its last caller. Between then and now, a
// batch check edited packages.toml without telling anyone.
//
// The report states the orphan COUNT already ("N package(s) have no ebuild in
// the overlay.", render/text.go:357). What it cannot state is that the registry
// was WRITTEN: internal/common/report/render must not know that packages.toml
// exists, which is the same boundary D5 draws for the --list hint. So the
// sentence belongs to the caller, exactly as the hint does.
//
// # The asymmetry this removes
//
// The single-package path never lost it: overlay_autoupdate.go:717 says
// "disabled in packages.toml" and returns before presentCheckReport, so there
// is no risk of saying it twice. Only the batch path went quiet.
func TestOrphanDisableIsAnnounced(t *testing.T) {
	orphaned := report.Report{Complete: true, Scanned: []report.PackageResult{
		{Package: "app-misc/gone", Type: "source", Orphaned: true},
		{Package: "app-misc/alsogone", Type: "source", Orphaned: true},
		{Package: "app-misc/jq", Type: "source", CurrentVersion: "1.7.1", CandidateVersion: "1.7.1"},
	}}

	out := captureStdout(t, func() { presentCheckReport(orphaned, noPlanPrinted) })

	// One sentence for the run, not one per package: it reports a single
	// batched write, which is what DisableOrphans performs.
	if got := strings.Count(out, "packages.toml"); got != 1 {
		t.Errorf("the auto-disable notice names packages.toml %d times over a 2-orphan scan, want exactly 1 — a batch check must not edit a hand-maintained file silently (R5.4)\n%s", got, out)
	}

	clean := report.Report{Complete: true, Scanned: []report.PackageResult{
		{Package: "app-misc/jq", Type: "source", CurrentVersion: "1.7.1", CandidateVersion: "1.7.1"},
	}}

	quiet := captureStdout(t, func() { presentCheckReport(clean, noPlanPrinted) })
	if strings.Contains(quiet, "packages.toml") {
		t.Errorf("a run that disabled nothing announced a registry write — the notice must not become unconditional noise (R5.4)\n%s", quiet)
	}
}
