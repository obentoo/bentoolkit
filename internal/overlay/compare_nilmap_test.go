package overlay

import (
	"strings"
	"testing"
)

// nilMapPackages is the mixed set every assertion below runs against: one
// package per pre-existing Status value, so nothing about the old axis can
// change unnoticed.
func nilMapPackages() ([]PackageInfo, *fakeProvider) {
	prov := &fakeProvider{versions: map[string][]string{
		"cat/behind": {"2.0"},
		"cat/ahead":  {"2.0"},
		"cat/level":  {"1.0"},
		// cat/only is absent upstream on purpose.
	}}
	pkgs := []PackageInfo{
		{Category: "cat", Package: "behind", LatestVersion: "1.0"},
		{Category: "cat", Package: "ahead", LatestVersion: "3.0"},
		{Category: "cat", Package: "level", LatestVersion: "1.0"},
		{Category: "cat", Package: "only", LatestVersion: "1.0"},
	}
	return pkgs, prov
}

// TestNilDivergenceMapIsAdditive is the single assertion that this story adds an
// axis instead of redefining one (UB6, R6.1).
//
// A nil Divergence map is not an edge case: it is the degraded mode R2.5 asks
// for when packages.toml cannot be read, and it is also what every run looked
// like before this story. With nothing known about any package, the report's
// Status column, its counts and the `--only-outdated` selection must be exactly
// what they are today — and every Verdict must be `unknown`, because a lookup
// that finds nothing is not a lookup that found "not patched".
//
// It asserts on OUTPUT, not on internal state: the statuses and counts a caller
// reads back, the selection `--only-outdated` produces, and the rendered table's
// pre-existing columns and rows. Whatever the Verdict grouping does to the
// sections around them, those must survive intact.
//
// _Requirements: R6.1, UB1, UB2, UB3, UB5, UB6_
func TestNilDivergenceMapIsAdditive(t *testing.T) {
	t.Run("statuses and counts are what they are today", func(t *testing.T) {
		pkgs, prov := nilMapPackages()

		report, err := CompareWithProvider(pkgs, prov, CompareOptions{
			IncludeSynced:      true,
			IncludeNotInRemote: true,
			Divergence:         nil, // nothing is known about any package
		})
		if err != nil {
			t.Fatalf("CompareWithProvider returned %v, want nil", err)
		}

		wantStatus := map[string]CompareStatus{
			"cat/behind": StatusOutdated,
			"cat/ahead":  StatusNewer,
			"cat/level":  StatusUpToDate,
			"cat/only":   StatusNotInRemote,
		}
		got := make(map[string]CompareResult, len(report.Results))
		for _, r := range report.Results {
			got[r.Category+"/"+r.Package] = r
		}
		if len(got) != len(wantStatus) {
			t.Fatalf("report holds %d results, want %d", len(got), len(wantStatus))
		}
		for atom, want := range wantStatus {
			r, ok := got[atom]
			if !ok {
				t.Errorf("%s missing from the report", atom)
				continue
			}
			if r.Status != want {
				t.Errorf("%s: Status = %v, want %v", atom, r.Status, want)
			}
			// UB6 in the other direction: nothing known means nothing recommended.
			if r.Verdict != VerdictUnknown {
				t.Errorf("%s: Verdict = %v, want VerdictUnknown with a nil map", atom, r.Verdict)
			}
			if r.Patched || r.PatchedBy != "" || r.PatchedReason != "" {
				t.Errorf("%s: reports a divergence (%v, %q, %q) the caller never supplied", atom, r.Patched, r.PatchedBy, r.PatchedReason)
			}
			if r.Verified != NotVerified {
				t.Errorf("%s: Verified = %v, want NotVerified (no local content in this run)", atom, r.Verified)
			}
		}

		// The existing summary counts, unchanged (UB3, UB5).
		for _, c := range []struct {
			name string
			got  int
			want int
		}{
			{"TotalPackages", report.TotalPackages, 4},
			{"ComparedPackages", report.ComparedPackages, 4},
			{"OutdatedCount", report.OutdatedCount, 1},
			{"NewerCount", report.NewerCount, 1},
			{"UpToDateCount", report.UpToDateCount, 1},
			{"NotInRemoteCount", report.NotInRemoteCount, 1},
			{"ErrorCount", report.ErrorCount, 0},
		} {
			if c.got != c.want {
				t.Errorf("%s = %d, want %d", c.name, c.got, c.want)
			}
		}
	})

	t.Run("--only-outdated selects exactly what it selects today", func(t *testing.T) {
		pkgs, prov := nilMapPackages()

		// The flag combination runCompare builds for `--only-outdated`.
		report, err := CompareWithProvider(pkgs, prov, CompareOptions{
			OnlyOutdated:  true,
			IncludeSynced: false,
			Divergence:    nil,
		})
		if err != nil {
			t.Fatalf("CompareWithProvider returned %v, want nil", err)
		}

		var selected []string
		for _, r := range report.Results {
			selected = append(selected, r.Category+"/"+r.Package)
		}
		if len(selected) != 1 || selected[0] != "cat/behind" {
			t.Errorf("--only-outdated selected %v, want [cat/behind]; the filter must select on Status alone (UB2)", selected)
		}
		// The counts are computed over every package, not only the selected ones.
		if report.ComparedPackages != 4 || report.UpToDateCount != 1 || report.NotInRemoteCount != 1 {
			t.Errorf("counts changed under --only-outdated: compared=%d up-to-date=%d only-in-bentoo=%d, want 4/1/1",
				report.ComparedPackages, report.UpToDateCount, report.NotInRemoteCount)
		}
	})

	t.Run("the rendered table keeps its existing columns and rows", func(t *testing.T) {
		pkgs, prov := nilMapPackages()

		report, err := CompareWithProvider(pkgs, prov, CompareOptions{
			IncludeSynced:      true,
			IncludeNotInRemote: true,
			Divergence:         nil,
		})
		if err != nil {
			t.Fatalf("CompareWithProvider returned %v, want nil", err)
		}
		out := FormatReport(report)

		for _, header := range []string{"Package", "Category", "Bentoo Version", "Gentoo Version", "Status"} {
			if !strings.Contains(out, header) {
				t.Errorf("column header %q disappeared from the report.\n--- report ---\n%s", header, out)
			}
		}

		// Each package still reads its own Status on its own row.
		wantRow := map[string]string{
			"behind": "outdated",
			"ahead":  "newer",
			"level":  "up-to-date",
			"only":   "not-in-remote",
		}
		for pkg, status := range wantRow {
			line := nilMapLineContaining(out, pkg)
			if line == "" {
				t.Errorf("no row for %s.\n--- report ---\n%s", pkg, out)
				continue
			}
			if !strings.Contains(line, status) {
				t.Errorf("row for %s reads %q, want it to state Status %q", pkg, strings.TrimSpace(line), status)
			}
		}

		// The pre-existing summary line, verbatim.
		if !strings.Contains(out, "Total: 4") {
			t.Errorf("summary line \"Total: 4\" missing.\n--- report ---\n%s", out)
		}

		// FormatSummary is the other pre-existing output surface (UB3).
		summary := FormatSummary(report)
		for _, want := range []string{"Scanned: 4 packages", "Compared: 3 packages"} {
			if !strings.Contains(summary, want) {
				t.Errorf("summary line %q missing.\n--- summary ---\n%s", want, summary)
			}
		}
	})
}

// nilMapLineContaining returns the first line of s containing sub, or "".
func nilMapLineContaining(s, sub string) string {
	for _, line := range strings.Split(s, "\n") {
		if strings.Contains(line, sub) {
			return line
		}
	}
	return ""
}
