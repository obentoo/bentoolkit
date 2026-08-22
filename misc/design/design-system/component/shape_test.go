package component_test

import (
	"strings"
	"testing"

	"github.com/obentoo/bentoolkit/misc/design/design-system/component"
	"github.com/obentoo/bentoolkit/misc/design/design-system/theme"
)

func plain() theme.Theme { return theme.New(theme.Plain) }

// TestConfirm_DefaultsToNo is a SAFETY property, not a style one. Every caller
// of this shape in bentoo guards something that writes — a bump being applied,
// a package being removed, an overlay that auto-commits and pushes within
// minutes. A bare Enter must never be the answer that acts.
func TestConfirm_DefaultsToNo(t *testing.T) {
	for _, p := range []component.Prompt{
		component.Confirm("Apply?"),
		component.ConfirmAll("Apply?"),
	} {
		got, ok := p.Default()
		if !ok {
			t.Fatalf("%q offers no default; a bare Enter would have no defined meaning", p.Question)
		}
		if got != 'n' {
			t.Errorf("the default answer is %q, want 'n' — Enter must not be the answer that writes", got)
		}
	}
}

// TestPrompt_RenderUpperCasesTheDefault pins the convention every hand-written
// prompt in the tree already follows, so a reader can tell what Enter does
// without consulting the source.
func TestPrompt_RenderUpperCasesTheDefault(t *testing.T) {
	if got := component.Confirm("Apply?").Render(plain()); !strings.HasSuffix(got, "[y/N]") {
		t.Errorf("Confirm rendered %q, want it to end in [y/N]", got)
	}
	if got := component.ConfirmAll("Apply?").Render(plain()); !strings.HasSuffix(got, "[y/N/a/q]") {
		t.Errorf("ConfirmAll rendered %q, want it to end in [y/N/a/q]", got)
	}
}

// TestPrompt_KeysMatchesWhatWasAdvertised is why Keys exists: the code that
// reads the answer validates against the question's own set instead of keeping
// a second copy that can drift.
func TestPrompt_KeysMatchesWhatWasAdvertised(t *testing.T) {
	p := component.ConfirmAll("Apply?")
	got := p.Keys()
	want := []rune{'y', 'n', 'a', 'q'}
	if len(got) != len(want) {
		t.Fatalf("Keys() = %q, want %q", string(got), string(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("Keys() = %q, want %q", string(got), string(want))
		}
	}

	rendered := p.Render(plain())
	for _, k := range got {
		if !strings.ContainsAny(rendered, string(k)+strings.ToUpper(string(k))) {
			t.Errorf("Keys() offers %q but the rendered prompt %q never shows it", k, rendered)
		}
	}
}

func TestPrompt_NoDefaultIsReported(t *testing.T) {
	p := component.Choose("Pick", component.Option{Key: 'a', Label: "alpha"})
	if _, ok := p.Default(); ok {
		t.Error("a prompt with no default option reported one")
	}
}

// TestBox_TopAndBottomEdgesAreTheSameWidth is the defect this component was
// written to fix: output.Box closes with a fixed sixteen dashes regardless of
// how wide it opened, so the panel almost never squares up.
func TestBox_TopAndBottomEdgesAreTheSameWidth(t *testing.T) {
	const width = 40
	lines := strings.Split(component.Box{
		Title: "Staged tree", Width: width, Lines: []string{"one", "two"},
	}.Render(plain()), "\n")

	top, bottom := lines[0], lines[len(lines)-1]
	if theme.Width(top) != theme.Width(bottom) {
		t.Errorf("top edge is %d cells and bottom edge is %d:\n%s\n%s",
			theme.Width(top), theme.Width(bottom), top, bottom)
	}
	if theme.Width(top) != width {
		t.Errorf("the box rendered %d cells wide, want the requested %d", theme.Width(top), width)
	}
}

// TestTable_MeasuresInsteadOfAssumingAWidth is the defect behind "%-45s": a
// hand-picked width misaligns the moment a cell exceeds it.
func TestTable_MeasuresInsteadOfAssumingAWidth(t *testing.T) {
	long := strings.Repeat("x", 60)
	out := component.Table{Rows: [][]string{
		{long, "first"},
		{"short", "second"},
	}}.Render(plain())

	lines := strings.Split(out, "\n")
	if len(lines) != 2 {
		t.Fatalf("want 2 rows, got %d:\n%s", len(lines), out)
	}
	firstCol := strings.Index(lines[0], "first")
	secondCol := strings.Index(lines[1], "second")
	if firstCol != secondCol {
		t.Errorf("the second column starts at %d on row 1 and %d on row 2; a 60-cell "+
			"cell was not measured:\n%s", firstCol, secondCol, out)
	}
}

func TestTable_LastColumnIsNotPadded(t *testing.T) {
	out := component.Table{Rows: [][]string{{"a", "short"}, {"b", "much longer"}}}.Render(plain())
	for _, ln := range strings.Split(out, "\n") {
		if strings.HasSuffix(ln, " ") {
			t.Errorf("row %q ends in padding; captured output would carry trailing whitespace", ln)
		}
	}
}

func TestKV_AlignsValuesOnOneColumn(t *testing.T) {
	out := component.KV{Pairs: []component.Pair{
		{Key: "depth", Value: "install"},
		{Key: "staged at", Value: "/tmp/x"},
	}}.Render(plain())

	lines := strings.Split(out, "\n")
	if a, b := strings.Index(lines[0], "install"), strings.Index(lines[1], "/tmp/x"); a != b {
		t.Errorf("values start at columns %d and %d, want one column:\n%s", a, b, out)
	}
}

// TestTally_ShowsZeroBucketsByDefault: "0 failed" is information. A summary
// that silently omits the bucket a reader was looking for reads as if the run
// never checked it.
func TestTally_ShowsZeroBucketsByDefault(t *testing.T) {
	ty := component.Tally{Noun: "ebuilds", Total: 4, Parts: []component.TallyPart{
		{Label: "failed", Count: 0, Role: theme.Fail},
		{Label: "passed", Count: 4, Role: theme.OK},
	}}
	if got := ty.Render(plain()); !strings.Contains(got, "0 failed") {
		t.Errorf("rendered %q, which drops the empty bucket", got)
	}

	ty.HideZero = true
	if got := ty.Render(plain()); strings.Contains(got, "0 failed") {
		t.Errorf("HideZero rendered %q, which kept the empty bucket", got)
	}
}

func TestTally_WithNoPartsIsStillALine(t *testing.T) {
	got := component.Tally{Noun: "ebuilds", Total: 0}.Render(plain())
	if got != "0 ebuilds" {
		t.Errorf("rendered %q, want a bare count rather than a dangling colon", got)
	}
}

// TestProgress_IndeterminateDropsThePercentage. Work whose size is not known
// yet has no percentage, and inventing one — 0%, or 100% of nothing — is the
// dishonest render.
func TestProgress_IndeterminateDropsThePercentage(t *testing.T) {
	p := component.Progress{Label: "Scanning", Done: 137}
	if got := p.Percent(); got != -1 {
		t.Errorf("Percent() = %d for an unknown total, want -1", got)
	}
	got := p.Render(plain())
	if strings.Contains(got, "%") {
		t.Errorf("rendered %q, which invents a percentage", got)
	}
	if !strings.Contains(got, "137") {
		t.Errorf("rendered %q, which lost the count it does know", got)
	}
}

func TestProgress_PercentColumnDoesNotJog(t *testing.T) {
	var widths []int
	for _, done := range []int{1, 15, 100} {
		out := component.Progress{Label: "Checking", Done: done, Total: 100}.Render(plain())
		widths = append(widths, strings.Index(out, "]"))
	}
	for i := 1; i < len(widths); i++ {
		if widths[i] != widths[0] {
			t.Errorf("the closing bracket sits at columns %v across 1%%, 15%% and 100%%; "+
				"the percentage is not padded", widths)
			break
		}
	}
}

func TestProgress_BarNeverOverflowsItsWidth(t *testing.T) {
	for _, done := range []int{0, 50, 100, 200} {
		out := component.Progress{Done: done, Total: 100, BarWidth: 10}.Render(plain())
		bar := out[strings.LastIndex(out, " ")+1:]
		if theme.Width(bar) != 10 {
			t.Errorf("done=%d drew a %d-cell bar, want 10: %q", done, theme.Width(bar), bar)
		}
	}
}

// TestGroup_EmptySaysSo. A group that prints nothing is indistinguishable from
// a group that failed to run.
func TestGroup_EmptySaysSo(t *testing.T) {
	got := component.Group{Title: "files"}.Render(plain())
	if !strings.Contains(got, "(none)") {
		t.Errorf("rendered %q, which says nothing about being empty", got)
	}
	if !strings.Contains(got, "(0)") {
		t.Errorf("rendered %q, which drops the count", got)
	}
}

func TestGroup_HideCountSuppressesOnlyTheCount(t *testing.T) {
	got := component.Group{Title: "files", Items: []string{"a"}, HideCount: true}.Render(plain())
	if strings.Contains(got, "(1)") {
		t.Errorf("rendered %q with the count HideCount asked to drop", got)
	}
	if !strings.Contains(got, "files") || !strings.Contains(got, "a") {
		t.Errorf("rendered %q, which dropped more than the count", got)
	}
}

// TestTree_IndentsEveryLevel is the structural property: overlay_prune.go
// carries the hierarchy in its format strings, so a level can be reordered
// without anything noticing. Here the data carries it.
func TestTree_IndentsEveryLevel(t *testing.T) {
	out := component.Tree{Roots: []component.Node{{
		Label: "L0", Children: []component.Node{{
			Label: "L1", Children: []component.Node{{Label: "L2"}},
		}},
	}}}.Render(plain())

	lines := strings.Split(out, "\n")
	if len(lines) != 3 {
		t.Fatalf("want 3 lines, got %d:\n%s", len(lines), out)
	}
	for i, want := range []int{0, 2, 4} {
		if got := len(lines[i]) - len(strings.TrimLeft(lines[i], " ")); got != want {
			t.Errorf("line %d is indented %d spaces, want %d:\n%s", i, got, want, out)
		}
	}
}

func TestStatus_AlignPadsTheTokenColumn(t *testing.T) {
	var cols []int
	for _, r := range []theme.Role{theme.OK, theme.Fail, theme.Warn} {
		out := component.Status{Role: r, Label: "pkg", Align: true}.Render(plain())
		cols = append(cols, strings.Index(out, "pkg"))
	}
	for i := 1; i < len(cols); i++ {
		if cols[i] != cols[0] {
			t.Errorf("labels start at columns %v across OK/FAIL/WARN; Align did not pad", cols)
			break
		}
	}
}

func TestStatus_WithoutADetailDrawsNoDash(t *testing.T) {
	got := component.Status{Role: theme.OK, Label: "pkg"}.Render(plain())
	if strings.Contains(got, "--") {
		t.Errorf("rendered %q with a dangling separator and nothing after it", got)
	}
}

func TestOutcome_StringUsesTheSpellingTheReportsAlreadyUse(t *testing.T) {
	for _, tt := range []struct {
		o    component.Outcome
		want string
	}{
		{component.Passed, "PASS"},
		{component.Failed, "FAIL"},
		{component.Skipped, "SKIP"},
		{component.Outcome(99), "SKIP"},
	} {
		if got := tt.o.String(); got != tt.want {
			t.Errorf("Outcome(%d).String() = %q, want %q", tt.o, got, tt.want)
		}
	}
}

// TestOutcome_ZeroValueIsSkippedNotPassed. The ladder's central rule is that a
// gate which could not run reports SKIPPED and never PASSED; a zero value that
// meant "passed" would break it by default.
func TestOutcome_ZeroValueIsSkippedNotPassed(t *testing.T) {
	var zero component.Outcome
	if zero != component.Skipped {
		t.Fatalf("the zero Outcome is %v; a gate nobody decided must not read as a pass", zero)
	}
	if got := (component.Gate{Name: "x"}).Outcome.Role(); got != theme.Skip {
		t.Errorf("the zero Gate paints as %v, want the Skip role", got)
	}
}

func TestGates_AlignsNamesAndKeepsReasons(t *testing.T) {
	out := component.Gates{Gates: []component.Gate{
		{Name: "patches", Outcome: component.Passed, Reason: "done"},
		{Name: "configure", Outcome: component.Failed, Reason: "boom"},
	}}.Render(plain())

	lines := strings.Split(out, "\n")
	if a, b := strings.Index(lines[0], "done"), strings.Index(lines[1], "boom"); a != b {
		t.Errorf("reasons start at columns %d and %d, want one column:\n%s", a, b, out)
	}
}

func TestRule_HonoursItsWidth(t *testing.T) {
	for _, label := range []string{"", "Validation"} {
		got := component.Rule{Label: label, Width: 30}.Render(plain())
		if theme.Width(got) != 30 {
			t.Errorf("Rule{Label:%q} drew %d cells, want 30: %q", label, theme.Width(got), got)
		}
	}
}

func TestEmpty_DefaultsToNone(t *testing.T) {
	if got := (component.Empty{}).Render(plain()); got != "(none)" {
		t.Errorf("rendered %q, want the default (none)", got)
	}
}

func TestTransition_WithoutALabelDrawsNoColon(t *testing.T) {
	got := component.Transition{From: "1.0", To: "1.1"}.Render(plain())
	if strings.Contains(got, ":") {
		t.Errorf("rendered %q with a colon and no label before it", got)
	}
	if !strings.Contains(got, "->") {
		t.Errorf("rendered %q without the ASCII arrow", got)
	}
}
