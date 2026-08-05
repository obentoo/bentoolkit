package main

import (
	"strings"
	"testing"

	"github.com/obentoo/bentoolkit/internal/overlay"
)

// This file carries two jobs, and the first one is inherited rather than new.
//
// UB3 guaranteed the user-visible summary — "Total packages scanned / Found in
// both / Only in Bentoo" — by saying that no task modifies printComparisonSummary.
// That was the only guarantee available in v1: the function lives in package
// main, and compare_nilmap_test.go lives in package overlay, which cannot reach
// it. Task 7.2 modifies it, so the guarantee is spent and this test replaces it
// with a direct pin of the three lines.
//
// The second job is R3.9: the four per-Verdict counters existed on CompareReport
// from task 2.2 and were read by nothing at all.
//
// Both assert on the pure line builders rather than on captured log output.
// logger binds its io.Writer once at first use and exposes no setter
// (logger.go:44-52), so capturing it would mean either adding production API for
// a test or swapping os.Stderr at the fd level. Splitting the decision from the
// emission is cheaper and leaves the emission trivial enough to read.

func summaryReport() *overlay.CompareReport {
	return &overlay.CompareReport{
		TotalPackages:    10,
		ComparedPackages: 8,
		NotInRemoteCount: 2,
		ErrorCount:       1,

		VerdictKeepCount:        4,
		VerdictRedundantCount:   2,
		VerdictNeedsRebaseCount: 1,
		VerdictUnknownCount:     3,
	}
}

// _Requirements: R3.9, UB3_
func TestPrintComparisonSummary(t *testing.T) {
	t.Run("the three pre-existing lines are unchanged", func(t *testing.T) {
		got := comparisonSummaryLines(summaryReport())

		// Verbatim, including the leading blank line and the two-space indent.
		// These are the bytes UB3 promises an operator's habits can rely on.
		want := []string{
			"\nSummary:",
			"  Total packages scanned: 10",
			"  Found in both repos: 5",
			"  Only in Bentoo: 2",
		}
		if len(got) != len(want) {
			t.Fatalf("summary has %d lines, want %d:\ngot  %q\nwant %q", len(got), len(want), got, want)
		}
		for i := range want {
			if got[i] != want[i] {
				t.Errorf("summary line %d is %q, want %q — UB3 promises these are untouched", i, got[i], want[i])
			}
		}
	})

	t.Run("each non-zero verdict count is printed", func(t *testing.T) {
		lines := verdictSummaryLines(summaryReport())
		if len(lines) != 1 {
			t.Fatalf("verdict summary has %d lines, want exactly 1: %q", len(lines), lines)
		}
		line := lines[0]

		for _, want := range []string{"keep 4", "redundant 2", "needs-rebase 1", "unknown 3"} {
			if !strings.Contains(line, want) {
				t.Errorf("verdict summary %q is missing %q", line, want)
			}
		}
	})

	t.Run("the verdict line cannot be read as a status count", func(t *testing.T) {
		line := verdictSummaryLines(summaryReport())[0]

		// The counter FIELDS carry a Verdict prefix so that a bare UnknownCount
		// beside the existing ErrorCount does not read as a Status count. The
		// printed line sits directly under "Errors (API issues)" and needs the
		// same protection, or UB3's separation of the two axes survives in the
		// struct and dies on screen.
		if !strings.Contains(strings.ToLower(line), "verdict") {
			t.Errorf("verdict summary %q never says which axis it counts; under the Errors line it reads as a Status count", line)
		}
	})

	t.Run("a zero count prints nothing", func(t *testing.T) {
		report := summaryReport()
		report.VerdictNeedsRebaseCount = 0
		report.VerdictUnknownCount = 0

		line := verdictSummaryLines(report)[0]

		if strings.Contains(line, "needs-rebase") {
			t.Errorf("verdict summary %q lists needs-rebase with a count of zero", line)
		}
		if strings.Contains(line, "unknown") {
			t.Errorf("verdict summary %q lists unknown with a count of zero", line)
		}
	})

	t.Run("no verdict line at all when every count is zero", func(t *testing.T) {
		report := &overlay.CompareReport{TotalPackages: 3}

		if lines := verdictSummaryLines(report); len(lines) != 0 {
			t.Errorf("verdict summary printed %q for a report with no verdicts at all", lines)
		}
	})

	t.Run("the counts come from the report, not from a filtered Results", func(t *testing.T) {
		// runCompare assigns the filtered slice back to report.Results before
		// rendering (task 5.2), deliberately leaving the counts as computed over
		// the whole scan: the summary answers "what is in the overlay", not
		// "what did you ask to see". Counting Results here would silently make
		// --only-redundant rewrite the overlay's own totals.
		report := summaryReport()
		report.Results = nil

		line := verdictSummaryLines(report)[0]

		if !strings.Contains(line, "keep 4") {
			t.Errorf("verdict summary %q lost its counts when Results was emptied — it is counting the filtered view instead of the scan", line)
		}
		if got := comparisonSummaryLines(report); !strings.Contains(strings.Join(got, "\n"), "Total packages scanned: 10") {
			t.Errorf("the scanned total followed the filtered Results: %q", got)
		}
	})
}
