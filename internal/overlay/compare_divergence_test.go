package overlay

import "testing"

// TestCompareCarriesVerdict pins what one comparison reports per package once
// the caller supplies what it knows about divergence (R2.1, R2.2).
//
// Four packages, one per interesting cell of the policy table, compared in a
// single run because the four must coexist in one report:
//
//   - a package ::gentoo already delivers identically, with a registry entry
//     that declares nothing  -> Redundant, not patched;
//   - a package ::gentoo has moved past, carrying a declared divergence
//     -> NeedsRebase, patched, and the report names the DECLARING ENTRY (the
//     slot-suffixed key, not the bare atom) and its reason (R2.2);
//   - a package with no entry at all -> Unknown, and emphatically not Redundant
//     (R2.3);
//   - a patched package level with ::gentoo -> Keep.
//
// The map the caller passes is keyed by the bare "category/package" atom, and
// absence from it IS the unknown state — this package never reads packages.toml
// and has no notion of a registry key (R2.4).
//
// _Requirements: R2.1, R2.2_
func TestCompareCarriesVerdict(t *testing.T) {
	prov := &fakeProvider{versions: map[string][]string{
		"cat/redundant":  {"1.0"},
		"cat/rebase":     {"2.0"},
		"cat/unrecorded": {"1.0"},
		"cat/current":    {"2.0"},
	}}

	pkgs := []PackageInfo{
		{Category: "cat", Package: "redundant", LatestVersion: "1.0"},
		{Category: "cat", Package: "rebase", LatestVersion: "1.0"},
		{Category: "cat", Package: "unrecorded", LatestVersion: "1.0"},
		{Category: "cat", Package: "current", LatestVersion: "2.0"},
	}

	opts := CompareOptions{
		IncludeSynced: true,
		Divergence: map[string]Divergence{
			// present and silent: the registry knows this package and declares no
			// divergence. Distinct from absent, which is "nothing is known".
			"cat/redundant": {},
			"cat/rebase": {
				Patched: true,
				Reason:  "keeps our wayland-by-default patch",
				Entry:   "cat/rebase:2",
			},
			"cat/current": {
				Patched: true,
				Reason:  "extra USE flag we depend on",
				Entry:   "cat/current@stable",
			},
			// cat/unrecorded is deliberately absent.
		},
	}

	report, err := CompareWithProvider(pkgs, prov, opts)
	if err != nil {
		t.Fatalf("CompareWithProvider returned %v, want nil", err)
	}

	got := make(map[string]CompareResult, len(report.Results))
	for _, r := range report.Results {
		got[r.Category+"/"+r.Package] = r
	}
	if len(got) != len(pkgs) {
		t.Fatalf("report holds %d results (%v), want %d", len(got), got, len(pkgs))
	}

	want := map[string]CompareResult{
		"cat/redundant": {
			Status: StatusUpToDate, Verdict: VerdictRedundant,
			Patched: false, PatchedBy: "", PatchedReason: "",
		},
		"cat/rebase": {
			Status: StatusOutdated, Verdict: VerdictNeedsRebase,
			Patched: true, PatchedBy: "cat/rebase:2", PatchedReason: "keeps our wayland-by-default patch",
		},
		"cat/unrecorded": {
			Status: StatusUpToDate, Verdict: VerdictUnknown,
			Patched: false, PatchedBy: "", PatchedReason: "",
		},
		"cat/current": {
			Status: StatusUpToDate, Verdict: VerdictKeep,
			Patched: true, PatchedBy: "cat/current@stable", PatchedReason: "extra USE flag we depend on",
		},
	}

	for atom, w := range want {
		r, ok := got[atom]
		if !ok {
			t.Errorf("%s missing from the report", atom)
			continue
		}
		if r.Status != w.Status {
			t.Errorf("%s: Status = %v, want %v (the existing axis must be untouched)", atom, r.Status, w.Status)
		}
		if r.Verdict != w.Verdict {
			t.Errorf("%s: Verdict = %v, want %v", atom, r.Verdict, w.Verdict)
		}
		if r.Patched != w.Patched {
			t.Errorf("%s: Patched = %v, want %v", atom, r.Patched, w.Patched)
		}
		if r.PatchedBy != w.PatchedBy {
			t.Errorf("%s: PatchedBy = %q, want %q (the report must name the declaring entry)", atom, r.PatchedBy, w.PatchedBy)
		}
		if r.PatchedReason != w.PatchedReason {
			t.Errorf("%s: PatchedReason = %q, want %q", atom, r.PatchedReason, w.PatchedReason)
		}
	}

	// The unrecorded package is the one the story exists for: it is level with
	// ::gentoo, which today reads as "delete it", and nothing in the registry
	// supports that reading.
	if got["cat/unrecorded"].Verdict == VerdictRedundant {
		t.Error("a package with no registry entry was recommended for removal; absence of a record is not evidence of sameness")
	}
}
