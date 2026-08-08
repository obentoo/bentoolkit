package overlay

import (
	"strings"
	"testing"
)

// verdictReportFixture is one report holding every verdict at once, with two
// packages that share a verdict but NOT a status (app-misc/ours is
// not-in-remote, dev-lang/rust is up-to-date, both Keep). That pair is what
// makes the grouping assertion meaningful: under the old status grouping they
// land in different sections, under verdict grouping they land together.
func verdictReportFixture() *CompareReport {
	return &CompareReport{
		TotalPackages:    6,
		ComparedPackages: 6,
		OutdatedCount:    1,
		NewerCount:       1,
		UpToDateCount:    3,
		NotInRemoteCount: 1,
		Results: []CompareResult{
			{
				Category: "app-editors", Package: "zed",
				LocalVersion: "1.0", RemoteVersion: "2.0", Status: StatusOutdated,
				Verdict: VerdictNeedsRebase, Patched: true,
				PatchedBy: "app-editors/zed@stable", PatchedReason: "keeps our wayland patch",
			},
			{
				Category: "www-client", Package: "firefox",
				LocalVersion: "129.0", RemoteVersion: "129.0", Status: StatusUpToDate,
				Verdict: VerdictRedundant,
			},
			{
				Category: "sys-devel", Package: "gcc",
				LocalVersion: "14.2", RemoteVersion: "14.2", Status: StatusUpToDate,
				Verdict: VerdictRedundant,
			},
			{
				Category: "app-misc", Package: "ours",
				LocalVersion: "0.3", RemoteVersion: "", Status: StatusNotInRemote,
				Verdict: VerdictKeep,
			},
			{
				Category: "dev-lang", Package: "rust",
				LocalVersion: "1.90", RemoteVersion: "1.90", Status: StatusUpToDate,
				Verdict: VerdictKeep, Patched: true,
				PatchedBy: "dev-lang/rust", PatchedReason: "extra USE flag we depend on",
			},
			{
				Category: "sys-devel", Package: "binutils",
				LocalVersion: "2.47", RemoteVersion: "2.46.1", Status: StatusNewer,
				Verdict: VerdictUnknown,
			},
		},
	}
}

// TestFormatReportVerdict pins what the operator actually reads (R3.1, R3.7):
// a Verdict per package next to the Status it does not replace, packages grouped
// by Verdict, an explicit removal recommendation for the redundant ones — and
// every existing summary line still there, still counting by Status.
//
// The two axes answer the two different questions the operator has: *how do the
// versions compare* and *what should I do about it*. Renaming `up-to-date` to
// `redundant` would have been the smaller diff and the larger break, so the
// summary counts below are asserted verbatim as they read today.
//
// _Requirements: R3.1, R3.7_
func TestFormatReportVerdict(t *testing.T) {
	report := verdictReportFixture()
	out := FormatReport(report)
	low := strings.ToLower(out)

	t.Run("the table carries a Verdict column", func(t *testing.T) {
		if !strings.Contains(out, "Verdict") {
			t.Errorf("no Verdict column header in the report.\n--- report ---\n%s", out)
		}
		// The Status column and its four values keep their meaning: the new
		// column is additive.
		if !strings.Contains(out, "Status") {
			t.Errorf("the Status column header disappeared.\n--- report ---\n%s", out)
		}
	})

	t.Run("every package's row states its verdict", func(t *testing.T) {
		wantVerdictWord := map[string]string{
			"zed":      "needs-rebase",
			"firefox":  "redundant",
			"gcc":      "redundant",
			"ours":     "keep",
			"rust":     "keep",
			"binutils": "unknown",
		}
		for pkg, word := range wantVerdictWord {
			// The package's TABLE ROW, not the first line mentioning its name.
			// The two were the same thing until the report's prose grew: a note
			// saying a copy holds "work of ours" is the first line containing
			// "ours", and this assertion then read a sentence and demanded a
			// verdict of it. tableRows matches the first column of a row, so it
			// cannot pick up prose, a finding, or a longer package name.
			rows := tableRows(low, pkg)
			if len(rows) != 1 {
				t.Errorf("the report holds %d table rows for %s, want exactly 1.\n--- report ---\n%s", len(rows), pkg, out)
				continue
			}
			if !strings.Contains(rows[0], word) {
				t.Errorf("row for %s reads %q; it must state the verdict %q", pkg, strings.TrimSpace(rows[0]), word)
			}
		}
	})

	t.Run("packages are grouped by verdict", func(t *testing.T) {
		// The order packages appear in, mapped to their verdict. Equal verdicts
		// must be contiguous — that is what "grouped" means, and it is checked
		// without pinning which group comes first.
		order := []struct {
			pkg     string
			verdict Verdict
		}{
			{"zed", VerdictNeedsRebase},
			{"firefox", VerdictRedundant},
			{"gcc", VerdictRedundant},
			{"ours", VerdictKeep},
			{"rust", VerdictKeep},
			{"binutils", VerdictUnknown},
		}
		positions := make(map[Verdict][]int)
		for _, o := range order {
			// Positioned by the package's ROW, for the reason given above: the
			// first occurrence of a package's NAME can now be a sentence in
			// another section's note, and a span measured from that would report
			// a grouping failure that the table does not have.
			rows := tableRows(low, o.pkg)
			if len(rows) != 1 {
				t.Fatalf("the report holds %d table rows for %s, want exactly 1.\n--- report ---\n%s", len(rows), o.pkg, out)
			}
			positions[o.verdict] = append(positions[o.verdict], strings.Index(low, rows[0]))
		}
		// A group is contiguous when no OTHER verdict's row sits between its
		// first and last row.
		for v, idxs := range positions {
			lo, hi := idxs[0], idxs[0]
			for _, i := range idxs {
				lo, hi = min(lo, i), max(hi, i)
			}
			for other, otherIdxs := range positions {
				if other == v {
					continue
				}
				for _, i := range otherIdxs {
					if i > lo && i < hi {
						t.Errorf("verdict %v is not grouped: a %v row sits inside its span.\n--- report ---\n%s", v, other, out)
					}
				}
			}
		}
	})

	t.Run("a redundant package is named a removal candidate", func(t *testing.T) {
		if !strings.Contains(low, "remov") {
			t.Errorf("the report never says a redundant package can be removed from the overlay.\n--- report ---\n%s", out)
		}
	})

	t.Run("no removal is suggested when nothing is redundant", func(t *testing.T) {
		clean := verdictReportFixture()
		kept := clean.Results[:0]
		for _, r := range clean.Results {
			if r.Verdict != VerdictRedundant {
				kept = append(kept, r)
			}
		}
		clean.Results = kept
		clean.UpToDateCount = 1

		cleanOut := strings.ToLower(FormatReport(clean))
		if strings.Contains(cleanOut, "remov") {
			t.Errorf("the report recommends a removal with no redundant package in it.\n--- report ---\n%s", FormatReport(clean))
		}
	})

	t.Run("every existing summary line survives", func(t *testing.T) {
		// Counted by Status, exactly as today: 1 outdated, 3 up-to-date, 2 other,
		// 6 in total. The verdict grouping must not recompute them.
		for _, want := range []string{"Outdated: 1", "Up-to-date: 3", "Other: 2", "Total: 6"} {
			if !strings.Contains(out, want) {
				t.Errorf("summary line %q missing; the existing counts must be untouched.\n--- report ---\n%s", want, out)
			}
		}
	})
}

// lineContaining is deliberately GONE. It returned the first line of the report
// containing a package's name, which stopped being that package's row the moment
// a section note contained an ordinary English word that is also a fixture's
// package name ("...whether a copy holds work of ours" and app-misc/ours). Both
// callers now use tableRows (compare_redundant_split_test.go), which matches the
// first column of a table row and therefore cannot match prose.
