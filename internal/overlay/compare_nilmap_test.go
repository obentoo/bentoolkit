package overlay

import (
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

		report, err := CompareWithProvider(t.Context(), pkgs, prov, CompareOptions{
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
		report, err := CompareWithProvider(t.Context(), pkgs, prov, CompareOptions{
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

	// Story 047, sub-task 5.5 (S047-R8.2): "the rendered table keeps its existing
	// columns and rows" stood here and was RETIRED. It asserted the five headers
	// "Package", "Category", "Bentoo Version", "Gentoo Version", "Status" and one
	// row per package carrying its status word, all over FormatReport's text.
	//
	// The table is now internal/common/report's, and it is a different table on
	// purpose: PACKAGE (the whole atom, so CATEGORY is no longer a column of its
	// own), BENTOO, GENTOO, STATE, DIFF, REASON. Every header is pinned by
	// TestCompareRunTablesNameTheirColumns and by every plain and markdown
	// golden; the per-package row and its status word by
	// testdata/TestCompareGoldenPlain.golden and TestCompareJSONGolden; and the
	// routing of a package to its row by TestBuildCompareReport/"each verdict
	// lands in its own list" in cmd/bentoo.
	//
	// What this file is actually about survives untouched above and below: a nil
	// Divergence map must change no count, no status and no verdict. That claim
	// never needed a renderer.

}
