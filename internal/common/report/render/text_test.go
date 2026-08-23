package render

import (
	"bytes"
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"
	"github.com/obentoo/bentoolkit/internal/common/report"
)

var update = flag.Bool("update", false, "regenerate the golden files")

// golden compares got against testdata/<name>.golden, following the pattern
// internal/common/tui/testdata already establishes in this repository.
//
// Regenerating is `go test ./internal/common/report/render/ -update`. A golden
// regenerated without being READ freezes whatever the renderer happened to
// produce, including its defects — which is the risk this task group carries.
// Read the diff before accepting one.
func golden(t *testing.T, name string, got []byte) {
	t.Helper()

	path := filepath.Join("testdata", name+".golden")

	if *update {
		if err := os.MkdirAll("testdata", 0o755); err != nil {
			t.Fatalf("creating testdata: %v", err)
		}
		if err := os.WriteFile(path, got, 0o644); err != nil {
			t.Fatalf("writing %s: %v", path, err)
		}
		t.Logf("regenerated %s (%d bytes) — READ IT before committing", path, len(got))
		return
	}

	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading %s: %v\nRegenerate with: go test ./internal/common/report/render/ -update", path, err)
	}
	if !bytes.Equal(got, want) {
		t.Errorf("output does not match %s\n--- want ---\n%s\n--- got ---\n%s", path, want, got)
	}
}

// planReason is the ~230-character sentence the current check path prints
// TWICE for every skipped package — once from the plan and once from the
// result, because neither function knows the other ran. Its length is the
// point: it is what makes the duplication cost 230 cells rather than 20.
const planReason = "depth resolved to none because the package is configured with depth=none in the overlay registry, " +
	"and no override was supplied for this run; nothing about this ebuild or this host was consulted, " +
	"so no gate was planned and none declined"

// fixtureReport is the one Report every renderer in this package is tested
// against. Fixing it in one place is what lets the three modes be compared on
// CONTENT rather than on "it rendered without error" (R2.4).
//
// It is shaped to exercise the parts that are easy to get wrong:
//   - four planned packages, one per outcome column, so the tally has a
//     non-zero count in each and Reconciles() holds
//   - two scanned packages with no update, so ShowAll has something to reveal
//   - one row whose reason repeats its plan entry (SameReasonAsPlan) and one
//     whose reason differs, which is the R7.2/R7.3 pair
//   - an atom wider than the 45-cell column this story removes
func fixtureReport() report.Report {
	return report.Report{
		Scanned: []report.PackageResult{
			{Package: "app-misc/jq", Type: "source", CurrentVersion: "1.7.1", CandidateVersion: "1.8.0", HasUpdate: true},
			{Package: "dev-libs/libayatana-appindicator-glib", Type: "source", CurrentVersion: "0.5.92", CandidateVersion: "0.5.93", HasUpdate: true},
			{Package: "media-plugins/gst-plugins-adaptivedemux2", Type: "source", CurrentVersion: "1.28.0", CandidateVersion: "1.28.1", HasUpdate: true},
			{Package: "sys-apps/portage", Type: "source", CurrentVersion: "3.0.66", CandidateVersion: "3.0.67", HasUpdate: true},
			{Package: "app-editors/zed", Type: "bin", CurrentVersion: "0.199.5", CandidateVersion: "0.199.5"},
			{Package: "dev-lang/go", Type: "source", CurrentVersion: "1.26.0", CandidateVersion: "1.26.0"},
		},
		Plan: []report.PlanEntry{
			{Package: "app-misc/jq", CurrentVersion: "1.7.1", CandidateVersion: "1.8.0", Class: "minor", Depth: "compile", Reason: "minor bump earns compile"},
			{Package: "dev-libs/libayatana-appindicator-glib", CurrentVersion: "0.5.92", CandidateVersion: "0.5.93", Class: "patch", Depth: "configure", Reason: "patch bump earns configure"},
			{Package: "media-plugins/gst-plugins-adaptivedemux2", CurrentVersion: "1.28.0", CandidateVersion: "1.28.1", Class: "patch", Depth: "manifest", Reason: "patch bump earns manifest"},
			{Package: "sys-apps/portage", CurrentVersion: "3.0.66", CandidateVersion: "3.0.67", Class: "patch", Depth: "none", Reason: planReason, Skipped: true},
		},
		Results: []report.ValidationRow{
			{Package: "app-misc/jq", CandidateVersion: "1.8.0", Outcome: report.Proved, Depth: "compile", Reason: "every deciding gate passed"},
			{Package: "dev-libs/libayatana-appindicator-glib", CandidateVersion: "0.5.93", Outcome: report.Errored, Depth: "configure", Reason: "configure failed: missing dependency dev-libs/ayatana-ido"},
			// The reason DIFFERS from the plan's: the plan asked for manifest,
			// and the host could not produce one. That is new information (R7.3).
			{Package: "media-plugins/gst-plugins-adaptivedemux2", CandidateVersion: "1.28.1", Outcome: report.Inconclusive, Depth: "none", Reason: "no Manifest could be produced: distfile fetch returned 404"},
			// The reason REPEATS the plan's, word for word (R7.2).
			{Package: "sys-apps/portage", CandidateVersion: "3.0.67", Outcome: report.Skipped, Depth: "none", Reason: planReason, SameReasonAsPlan: true},
		},
		Tally:    report.Tally{Proved: 1, Errored: 1, Inconclusive: 1, Skipped: 1},
		Complete: true,
	}
}

// renderPlain is the shorthand every test below uses. It fails the test on a
// render error rather than returning one, because no test here is about the
// error path.
func renderPlain(t *testing.T, opts Options) string {
	t.Helper()

	var buf bytes.Buffer
	if err := Plain(&buf, fixtureReport(), opts); err != nil {
		t.Fatalf("Plain returned an error: %v", err)
	}
	return buf.String()
}

// TestPlainGoldenWithoutShowAll pins the default rendering: the version-check
// section reports the up-to-date packages as a COUNT (R8.3).
func TestPlainGoldenWithoutShowAll(t *testing.T) {
	golden(t, "TestPlainGoldenWithoutShowAll", []byte(renderPlain(t, Options{Width: 100})))
}

// TestPlainGoldenWithShowAll pins the --all rendering: the same run, with the
// packages behind that count listed (R8.2).
func TestPlainGoldenWithShowAll(t *testing.T) {
	golden(t, "TestPlainGoldenWithShowAll", []byte(renderPlain(t, Options{Width: 100, ShowAll: true})))
}

// TestPlainGoldensDifferGuards the two goldens above against being written from
// the same output. Identical goldens would make both tests pass while ShowAll
// did nothing at all.
func TestPlainGoldensDiffer(t *testing.T) {
	if renderPlain(t, Options{Width: 100}) == renderPlain(t, Options{Width: 100, ShowAll: true}) {
		t.Fatal("ShowAll changed nothing in the output")
	}
}

// TestPlainHasNoEscapes pins R2.1. Plain is what cron captures and what an
// operator pipes into a file; an escape sequence there is noise in a log
// nobody will read with a terminal.
func TestPlainHasNoEscapes(t *testing.T) {
	for _, showAll := range []bool{false, true} {
		out := renderPlain(t, Options{Width: 100, ShowAll: showAll})

		if i := strings.IndexByte(out, 0x1b); i >= 0 {
			t.Errorf("plain output (ShowAll=%v) carries an escape sequence at byte %d: %q",
				showAll, i, out[i:min(i+20, len(out))])
		}
	}
}

// TestPlainTallyShowsFourCounts pins R5.1 at the surface an operator reads.
// The column being added is the whole point of the story: a count of three
// cannot tell a toolkit limitation from the operator's own policy.
//
// It asserts on the TALLY LINE, not on the whole output, and that scoping is
// load-bearing rather than tidy. The first version of this test lowercased the
// entire render and grepped for the four words — and a mutation that folded
// inconclusive back into skipped ("2 not validated") still PASSED it, because
// "inconclusive" and "skipped" also appear in the results table's OUTCOME
// column. It was a guard for R5.1 that could not fail for R5.1's defect.
// Measured, not suspected: the mutation was run.
//
// The line is found by content rather than by section title, so a renamed
// heading does not turn this into a false red. Exactly one line names both
// "proved" and "errored" — a results row carries one outcome, never two.
func TestPlainTallyShowsFourCounts(t *testing.T) {
	out := renderPlain(t, Options{Width: 100})

	var tally string
	for _, line := range strings.Split(out, "\n") {
		lower := strings.ToLower(line)
		if strings.Contains(lower, "proved") && strings.Contains(lower, "errored") {
			tally = lower
			break
		}
	}
	if tally == "" {
		t.Fatalf("no line names both proved and errored, so there is no tally line\n--- output ---\n%s", out)
	}

	for _, column := range []string{"proved", "errored", "inconclusive", "skipped"} {
		if !strings.Contains(tally, column) {
			t.Errorf("the tally does not name the %q column\ntally line: %q", column, tally)
		}
	}
}

// TestPlainNoLineExceedsTheWidth pins R6.3. A line wider than the terminal
// wraps, and one wrapped line destroys the alignment of every column below it.
func TestPlainNoLineExceedsTheWidth(t *testing.T) {
	const width = 100

	for _, showAll := range []bool{false, true} {
		for i, line := range strings.Split(renderPlain(t, Options{Width: width, ShowAll: showAll}), "\n") {
			if w := lipgloss.Width(line); w > width {
				t.Errorf("ShowAll=%v line %d is %d cells wide, %d over the %d asked for: %q",
					showAll, i+1, w, w-width, width, line)
			}
		}
	}
}

// ---------------------------------------------------------------------------
// Sub-task 2.3 — a reason is printed once. APPENDED to text_test.go.
// ---------------------------------------------------------------------------

// TestReasonPrintedOnce is the regression guard for RC3. The current check path
// prints entry.Reason from the plan and then prints it AGAIN through
// skipReason's last fallback, because neither function knows the other ran. For
// every skipped package the operator reads the same ~230 characters twice.
//
// The count is of the SHORTENED form as it appears on screen — the plan prints
// its reason shortened to the available width (R7.1), so counting the full
// string would find zero occurrences and pass vacuously.
func TestReasonPrintedOnce(t *testing.T) {
	out := renderPlain(t, Options{Width: 100})

	// The first 40 cells of the sentence are enough to identify it and short
	// enough to survive any sensible shortening.
	needle := planReason[:40]

	if n := strings.Count(out, needle); n != 1 {
		t.Fatalf("the plan's reason appears %d times, want exactly 1 (R7.2)\nneedle: %q\n--- output ---\n%s", n, needle, out)
	}
}

// TestDifferingReasonIsPrinted is the other half of the rule, and the half that
// keeps the fix from becoming a new defect. Suppressing every result reason
// would satisfy the test above and silently drop the case where the result
// carries NEW information — the plan asked for manifest, the host could not
// produce one (R7.3).
func TestDifferingReasonIsPrinted(t *testing.T) {
	out := renderPlain(t, Options{Width: 100})

	const differing = "no Manifest could be produced"
	if !strings.Contains(out, differing) {
		t.Fatalf("a result reason that differs from its plan entry was not printed (R7.3)\nwanted: %q\n--- output ---\n%s", differing, out)
	}
}

// TestReasonSurvivesShortening pins R7.4: shortening is a rendering decision
// and never a loss of data. The screen may show 60 cells of a 230-character
// sentence; the model still holds all 230, which is what lets the Markdown and
// JSON exports carry the whole thing.
func TestReasonSurvivesShortening(t *testing.T) {
	r := fixtureReport()

	// The model's copy is whole.
	var found bool
	for _, entry := range r.Plan {
		if entry.Package == "sys-apps/portage" {
			found = true
			if entry.Reason != planReason {
				t.Fatalf("the model's reason was modified: %d chars, want %d", len(entry.Reason), len(planReason))
			}
		}
	}
	if !found {
		t.Fatal("the fixture no longer holds the package this test is about")
	}

	// The screen's copy is not, at a width that cannot hold it.
	out := renderPlain(t, Options{Width: 80})
	if strings.Contains(out, planReason) {
		t.Errorf("the full %d-character reason was printed at width 80 — it cannot fit, so it was not shortened", len(planReason))
	}
	if !strings.Contains(out, planReason[:40]) {
		t.Errorf("the reason was shortened past the point of being identifiable")
	}
}

// ---------------------------------------------------------------------------
// Sub-task 3.1 — Markdown shares the text renderer. APPENDED to text_test.go.
// ---------------------------------------------------------------------------

// renderMarkdown mirrors renderPlain. Note what it CANNOT take: Markdown has no
// Options parameter, so there is no width to shorten to and no ShowAll to
// honour. R9.3 is enforced by the signature rather than by a branch somebody
// has to remember — an export that mirrored screen truncation would be a
// useless record, and this is why it cannot.
func renderMarkdown(t *testing.T) string {
	t.Helper()

	var buf bytes.Buffer
	if err := Markdown(&buf, fixtureReport()); err != nil {
		t.Fatalf("Markdown returned an error: %v", err)
	}
	return buf.String()
}

// TestMarkdownGolden pins the export over the same fixture the plain goldens
// use. Comparing the two goldens is how "differ by table syntax and nothing
// else" stops being a claim.
func TestMarkdownGolden(t *testing.T) {
	golden(t, "TestMarkdownGolden", []byte(renderMarkdown(t)))
}

// TestExportIsCompleteKeepsEveryReasonWhole pins the first half of R9.3. The
// terminal shows 96 cells of the 232-character reason; the export shows all
// 232. Shortening is a rendering decision, and an export is not a rendering of
// a terminal.
func TestExportIsCompleteKeepsEveryReasonWhole(t *testing.T) {
	out := renderMarkdown(t)

	if !strings.Contains(out, planReason) {
		t.Errorf("the %d-character reason was not exported whole (R9.3)\n--- export ---\n%s", len(planReason), out)
	}
	if strings.Contains(out, "…") {
		t.Errorf("the export carries an ellipsis, so something was shortened (R9.3)")
	}
}

// TestExportIsCompleteListsEveryPackage pins the other half of R9.3: every
// package, regardless of --all. The two up-to-date packages that the plain
// render hides behind a count must be named here.
func TestExportIsCompleteListsEveryPackage(t *testing.T) {
	out := renderMarkdown(t)

	for _, pkg := range fixtureReport().Scanned {
		if !strings.Contains(out, pkg.Package) {
			t.Errorf("the export does not name %q — an export that honoured --all would be an incomplete record (R9.3)", pkg.Package)
		}
	}
}

// TestExportIsCompleteRegardlessOfPlainShowAll is the guard that makes the two
// tests above mean something. Markdown takes no Options, so nothing a caller
// does to the terminal rendering may change the export: identical bytes either
// way, by construction.
func TestExportIsCompleteRegardlessOfPlainShowAll(t *testing.T) {
	first := renderMarkdown(t)

	// Render plain both ways in between — if any shared state leaked from the
	// screen renderer into the export, this is where it would show.
	renderPlain(t, Options{Width: 40})
	renderPlain(t, Options{Width: 200, ShowAll: true})

	if second := renderMarkdown(t); first != second {
		t.Error("the Markdown export changed after the plain renderer ran — the export is not independent of the screen")
	}
}

// TestMarkdownIsNotPlain guards against the "shares the renderer" refactor
// collapsing into "is the renderer". They differ by table syntax; if the two
// outputs were identical, one of the two styles would not be reaching the
// writer at all.
func TestMarkdownIsNotPlain(t *testing.T) {
	if renderMarkdown(t) == renderPlain(t, Options{Width: 100}) {
		t.Fatal("the Markdown export is byte-identical to the plain render — the style parameter is not being applied")
	}
}
