package main

import (
	"testing"

	"github.com/obentoo/bentoolkit/internal/overlay"
)

// The seam this file pins:
//
//	func filterCompareResults(results []overlay.CompareResult, onlyRedundant, onlyPatched bool) []overlay.CompareResult
//
// A pure narrowing of an already-computed report, applied in cmd/ where the two
// new flags live. `--only-outdated` is NOT one of its parameters on purpose: it
// selects on Status and is already applied inside CompareWithProvider, so
// combining the filters is an intersection by construction — this function runs
// on the set that filter left behind. (The design assigns the flags to
// cmd/bentoo/overlay_compare.go but names no function, so the signature is this
// test's contract rather than a copy of the design's.)

// filterFixture is a report holding one package per verdict, plus a patched
// package that is also outdated — the only combination in which `--only-patched`
// and `--only-outdated` overlap.
func filterFixture() []overlay.CompareResult {
	return []overlay.CompareResult{
		{
			Category: "app-editors", Package: "zed", Status: overlay.StatusOutdated,
			Verdict: overlay.VerdictNeedsRebase, Patched: true, PatchedBy: "app-editors/zed@stable",
		},
		{
			Category: "www-client", Package: "firefox", Status: overlay.StatusUpToDate,
			Verdict: overlay.VerdictRedundant,
		},
		{
			Category: "sys-devel", Package: "gcc", Status: overlay.StatusOutdated,
			Verdict: overlay.VerdictRedundant,
		},
		{
			Category: "dev-lang", Package: "rust", Status: overlay.StatusUpToDate,
			Verdict: overlay.VerdictKeep, Patched: true, PatchedBy: "dev-lang/rust",
		},
		{
			Category: "sys-devel", Package: "binutils", Status: overlay.StatusNewer,
			Verdict: overlay.VerdictUnknown,
		},
	}
}

func filterAtoms(results []overlay.CompareResult) []string {
	out := make([]string, 0, len(results))
	for _, r := range results {
		out = append(out, r.Category+"/"+r.Package)
	}
	return out
}

func sameAtoms(got []overlay.CompareResult, want []string) bool {
	atoms := filterAtoms(got)
	if len(atoms) != len(want) {
		return false
	}
	for i := range atoms {
		if atoms[i] != want[i] {
			return false
		}
	}
	return true
}

// TestCompareFilters covers the two flags R5 adds and the intersection R5.3
// requires.
//
// _Requirements: R5.1, R5.2, R5.3_
func TestCompareFilters(t *testing.T) {
	t.Run("both flags are registered", func(t *testing.T) {
		for _, name := range []string{"only-redundant", "only-patched"} {
			flag := compareCmd.Flags().Lookup(name)
			if flag == nil {
				t.Errorf("compare command should have a --%s flag", name)
				continue
			}
			if flag.Value.Type() != "bool" {
				t.Errorf("--%s should be bool, got %s", name, flag.Value.Type())
			}
			if flag.DefValue != "false" {
				t.Errorf("--%s default should be false, got %q", name, flag.DefValue)
			}
		}
		// The pre-existing filter is untouched (UB2).
		if flag := compareCmd.Flags().Lookup("only-outdated"); flag == nil {
			t.Error("--only-outdated disappeared")
		}
	})

	t.Run("no flag narrows nothing", func(t *testing.T) {
		got := filterCompareResults(filterFixture(), false, false)
		want := []string{"app-editors/zed", "www-client/firefox", "sys-devel/gcc", "dev-lang/rust", "sys-devel/binutils"}
		if !sameAtoms(got, want) {
			t.Errorf("filterCompareResults(_, false, false) = %v, want %v (order preserved)", filterAtoms(got), want)
		}
	})

	t.Run("--only-redundant lists only redundant packages (R5.1)", func(t *testing.T) {
		got := filterCompareResults(filterFixture(), true, false)
		want := []string{"www-client/firefox", "sys-devel/gcc"}
		if !sameAtoms(got, want) {
			t.Errorf("--only-redundant selected %v, want %v", filterAtoms(got), want)
		}
		for _, r := range got {
			if r.Verdict != overlay.VerdictRedundant {
				t.Errorf("%s/%s has Verdict %v; --only-redundant selects on the verdict alone", r.Category, r.Package, r.Verdict)
			}
		}
	})

	t.Run("--only-patched lists only patched packages (R5.2)", func(t *testing.T) {
		got := filterCompareResults(filterFixture(), false, true)
		want := []string{"app-editors/zed", "dev-lang/rust"}
		if !sameAtoms(got, want) {
			t.Errorf("--only-patched selected %v, want %v", filterAtoms(got), want)
		}
		for _, r := range got {
			if !r.Patched {
				t.Errorf("%s/%s is not patched; --only-patched selects on the declaration, not on the verdict", r.Category, r.Package)
			}
		}
	})

	t.Run("the two new filters intersect (R5.3)", func(t *testing.T) {
		// Redundant means "we changed nothing", patched means "we did", so the
		// intersection is empty by construction — and an implementation that
		// unioned the flags instead would return four rows here.
		got := filterCompareResults(filterFixture(), true, true)
		if len(got) != 0 {
			t.Errorf("--only-redundant --only-patched selected %v, want nothing: the two conditions cannot both hold", filterAtoms(got))
		}
	})

	t.Run("a new filter intersects with --only-outdated (R5.3)", func(t *testing.T) {
		// What `--only-outdated` leaves behind: Status-selected rows only. The
		// new filter narrows that set rather than replacing it.
		var outdatedOnly []overlay.CompareResult
		for _, r := range filterFixture() {
			if r.Status == overlay.StatusOutdated {
				outdatedOnly = append(outdatedOnly, r)
			}
		}
		if len(outdatedOnly) != 2 {
			t.Fatalf("fixture check: %d outdated rows, want 2", len(outdatedOnly))
		}

		got := filterCompareResults(outdatedOnly, false, true)
		if want := []string{"app-editors/zed"}; !sameAtoms(got, want) {
			t.Errorf("--only-outdated --only-patched selected %v, want %v", filterAtoms(got), want)
		}

		got = filterCompareResults(outdatedOnly, true, false)
		if want := []string{"sys-devel/gcc"}; !sameAtoms(got, want) {
			t.Errorf("--only-outdated --only-redundant selected %v, want %v", filterAtoms(got), want)
		}
	})
}
