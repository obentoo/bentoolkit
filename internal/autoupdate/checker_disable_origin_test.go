// Story 043 — R1. The origin of a disable, and the reconciliation that reads it.
//
// MATERIALISED PER SUB-TASK, NOT ALL AT ONCE. In Go a test naming a symbol that
// does not exist yet stops the WHOLE package compiling, and a package that does
// not compile runs no tests at all — so a fragment landed early does not fail,
// it silences every other test in internal/autoupdate. Each sub-task of task 1
// appends its own tests to this file and confirms Red before implementing.
// See .draft/red-evidence.yaml for the deferral and its reason.

package autoupdate

import (
	"os"
	"strings"
	"testing"

	"github.com/obentoo/bentoolkit/internal/autoupdate/registry"
)

// R1.1, Constraint — `--lint` reads the same file, and an unknown key does not
// merely produce a finding: it fails the load outright (UnknownKeysError), so
// the semantic checks never run. A field the linter does not know would report
// every migrated entry AND stop the registry building at all.
//
// The redundant-enabled row is deliberate rather than filler: that rule fires on
// `enabled = true`, and disabled_by only ever rides beside `enabled = false`.
// Pinning it here says the new key did not widen the rule by accident.
func TestDisableOriginIsNotAnUnknownKeyToTheLinter(t *testing.T) {
	dir := writeRegistry(t, `["dev-libs/icu-compat"]
enabled = false
disabled_by = "auto"
url = "https://example.com"
parser = "json"
path = "version"
comments = "icu-compat — carries the ICU 77 ABI for orion-bin."
# END
["media-libs/libjxl-compat"]
enabled = false
url = "https://example.com"
parser = "json"
path = "version"
comments = "libjxl-compat — pinned by hand, no origin recorded."
# END
`)

	issues, err := registry.LintPackagesConfig(nil, dir)
	if err != nil {
		t.Fatalf("LintPackagesConfig: %v", err)
	}
	if len(issues) != 0 {
		t.Fatalf("a registry carrying disabled_by reported %v, want nothing", rules(issues))
	}
}

// mustRead is a read that fails the test rather than returning a nil body that
// every assertion below would then read as "the key is absent" — the exact
// shape of a guard that passes for the wrong reason.
func mustRead(t *testing.T, path string) []byte {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return b
}

// --- sub-task 1.3 — reconcile only what the checker itself disabled ---------

// R1.2 + R1.3 — the whole decision, as a table. This is the predicate task 1.3
// extracts from CheckAll; testing it directly keeps the rule readable and lets
// the CheckAll test assert wiring rather than policy.
//
// The legacy row is the one this story exists for: dev-libs/icu-compat was
// disabled with no origin and its ebuild WAS present, and that combination is
// what the old code read as "stale bookkeeping, clear it".
func TestReconcilesOnlyWhatTheCheckerDisabled(t *testing.T) {
	no := false
	cases := []struct {
		name string
		pkg  registry.PackageConfig
		want bool
	}{
		{"auto-disabled, ebuild back", registry.PackageConfig{Enabled: &no, DisabledBy: "auto"}, true},
		{"legacy disable, no origin", registry.PackageConfig{Enabled: &no}, false},
		{"human origin", registry.PackageConfig{Enabled: &no, DisabledBy: "maintainer"}, false},
		{"held and disabled", registry.PackageConfig{Enabled: &no, DisabledBy: "auto", Hold: true}, false},
		{"held, no origin", registry.PackageConfig{Enabled: &no, Hold: true}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := reconcilesAutomatically(tc.pkg); got != tc.want {
				t.Errorf("reconcilesAutomatically = %v, want %v", got, tc.want)
			}
		})
	}
}

// R1.4 — a frozen legacy entry that nobody is told about is the silent
// regression this fail-safe would otherwise introduce. The run must name it.
//
// The notice is asserted on the line logFrozenDisables writes to an injected
// logger: the message with its attributes rendered after it.
func TestLegacyDisableIsNamedInTheOutput(t *testing.T) {
	lc := captureInfoLogs(t)
	logFrozenDisables(lc.logger(), []string{"dev-libs/icu-compat", "media-libs/libjxl-compat"})
	notice := strings.Join(lc.all(), "\n")

	for _, atom := range []string{"dev-libs/icu-compat", "media-libs/libjxl-compat"} {
		if !strings.Contains(notice, atom) {
			t.Errorf("the notice does not name %s, so a frozen entry is invisible:\n%s", atom, notice)
		}
	}
	// Naming them is half of R1.4. Saying WHY is the half that tells a maintainer
	// the entry is repairable rather than broken.
	if !strings.Contains(notice, "disabled_by") {
		t.Errorf("the notice does not say what is missing, so nobody can act on it:\n%s", notice)
	}
	empty := captureInfoLogs(t)
	logFrozenDisables(empty.logger(), nil)
	if empty.count() != 0 {
		t.Errorf("an empty frozen set produced a notice: %q — a run with nothing to report must say nothing", empty.all())
	}
}

// R1.3 + R1.4 + R5.2 — the defect end to end, in the exact shape R5.2 demands:
// disabled, NO origin recorded, and the ebuild PRESENT in the overlay. That
// last condition is what made the old reconciliation act; a fixture whose ebuild
// is absent would leave the entry disabled for an entirely different reason and
// could not fail for this one.
//
// The `auto` row beside it is not decoration: without it a predicate hardcoded
// to `return false` would pass this test, and the reconciliation would be dead
// rather than correct.
func TestCheckAllLeavesALegacyDisableAlone(t *testing.T) {
	for _, tc := range []struct {
		name        string
		origin      string
		wantEnabled bool
	}{
		{"legacy disable, no origin recorded", "", false},
		{"the checker's own bookkeeping", "\ndisabled_by = \"auto\"", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			pkg := "sci-ml/reappeared"
			srv := jsonVersionServer(t, "1.0.0")

			content := `["sci-ml/reappeared"]
enabled = false` + tc.origin + `
url = "` + srv.URL + `"
parser = "json"
path = "version"
`
			overlay, configPath := writePackagesTOML(t, content)
			createTestEbuild(t, overlay, pkg, "1.0.0") // the ebuild IS present — R5.2

			checker, err := NewChecker(overlay,
				WithConfigDir(t.TempDir()),
				WithRateLimiter(unlimitedRateLimiter()),
			)
			if err != nil {
				t.Fatalf("NewChecker: %v", err)
			}

			res := checker.CheckAll(t.Context(), false)

			if got := checker.Config().Packages[pkg]; got.IsEnabled() != tc.wantEnabled {
				t.Errorf("in memory: enabled = %v, want %v", got.IsEnabled(), tc.wantEnabled)
			}
			cfg, err := registry.LoadPackagesConfig(overlay)
			if err != nil {
				t.Fatalf("reload: %v", err)
			}
			onDisk := cfg.Packages[pkg]
			if onDisk.IsEnabled() != tc.wantEnabled {
				body := string(mustRead(t, configPath))
				t.Errorf("on disk: enabled = %v, want %v:\n%s", onDisk.IsEnabled(), tc.wantEnabled, body)
			}
			// A package left disabled must also stay OUT of the run, or it was
			// bumped anyway and the registry state was cosmetic.
			if hasItem(res, pkg) != tc.wantEnabled {
				t.Errorf("present in batch = %v, want %v", hasItem(res, pkg), tc.wantEnabled)
			}
		})
	}
}

// R1.3, Unchanged Behavior 5 — hold keeps meaning what it meant. A held entry is
// untouched whether or not it carries an origin, so the new field cannot become
// a back door into a package the maintainer held.
func TestCheckAllStillLeavesHeldPackagesAlone(t *testing.T) {
	for _, origin := range []string{"", "\ndisabled_by = \"auto\""} {
		t.Run("origin="+origin, func(t *testing.T) {
			pkg := "sci-ml/held"
			srv := jsonVersionServer(t, "2.0.0") // newer: would pend if it were checked

			content := `["sci-ml/held"]
hold = true
enabled = false` + origin + `
url = "` + srv.URL + `"
parser = "json"
path = "version"
`
			overlay, _ := writePackagesTOML(t, content)
			createTestEbuild(t, overlay, pkg, "1.0.0")

			checker, err := NewChecker(overlay,
				WithConfigDir(t.TempDir()),
				WithRateLimiter(unlimitedRateLimiter()),
			)
			if err != nil {
				t.Fatalf("NewChecker: %v", err)
			}

			res := checker.CheckAll(t.Context(), false)

			held := checker.Config().Packages[pkg]
			if held.IsEnabled() {
				t.Error("a held package was re-enabled by reconciliation")
			}
			if hasItem(res, pkg) {
				t.Error("a held package was processed")
			}
		})
	}
}

// --- orchestrator-authored, Run mode ----------------------------------------
//
// R1.4's WIRING, which the sub-task 1.3 mutation proof found uncovered: deleting
// CheckAll's report call turned nothing red, because TestLegacyDisableIsNamedInTheOutput
// Output pins the notice's wording and TestCheckAllLeavesALegacyDisableAlone pins
// the behaviour, and neither pins that the run joins the two.
//
// Recorded in .draft/deviations.yaml. Since story 062 it asserts through the
// logger injected with WithLogger, which receives the notice's record.

// R1.4 — the run must NAME the entry it froze. Asserting the call happened, with
// the atom in it, is the only formulation that fails when the reporting is
// dropped while the notice builder survives.
func TestCheckAllReportsTheEntryItFroze(t *testing.T) {
	frozenLog := captureInfoLogs(t)

	pkg := "sci-ml/reappeared"
	srv := jsonVersionServer(t, "1.0.0")
	content := `["sci-ml/reappeared"]
enabled = false
url = "` + srv.URL + `"
parser = "json"
path = "version"
`
	overlay, _ := writePackagesTOML(t, content)
	createTestEbuild(t, overlay, pkg, "1.0.0") // present — this is the R5.2 shape

	checker, err := NewChecker(overlay,
		WithConfigDir(t.TempDir()),
		WithRateLimiter(unlimitedRateLimiter()),
		WithLogger(frozenLog.logger()),
	)
	if err != nil {
		t.Fatalf("NewChecker: %v", err)
	}
	checker.CheckAll(t.Context(), false)
	var reported []string
	for _, line := range frozenLog.all() {
		if strings.HasPrefix(line, frozenDisableMessage) {
			reported = append(reported, line)
		}
	}

	if len(reported) != 1 {
		t.Fatalf("the run reported %d times, want exactly 1 — R1.4 asks for one line per run: %v", len(reported), reported)
	}
	if !strings.Contains(reported[0], pkg) {
		t.Errorf("the reported line does not name %s:\n%s", pkg, reported[0])
	}
}

// R1.4, the other half — a run with nothing frozen must say nothing. Without
// this row the test above is satisfied by a reporter that fires unconditionally,
// which on a healthy registry would print a line about an empty list on every
// single scan.
func TestCheckAllReportsNothingWhenNothingIsFrozen(t *testing.T) {
	frozenLog := captureInfoLogs(t)

	pkg := "sci-ml/reappeared"
	srv := jsonVersionServer(t, "1.0.0")
	content := `["sci-ml/reappeared"]
enabled = false
disabled_by = "auto"
url = "` + srv.URL + `"
parser = "json"
path = "version"
`
	overlay, _ := writePackagesTOML(t, content)
	createTestEbuild(t, overlay, pkg, "1.0.0")

	checker, err := NewChecker(overlay,
		WithConfigDir(t.TempDir()),
		WithRateLimiter(unlimitedRateLimiter()),
		WithLogger(frozenLog.logger()),
	)
	if err != nil {
		t.Fatalf("NewChecker: %v", err)
	}
	checker.CheckAll(t.Context(), false)
	var reported []string
	for _, line := range frozenLog.all() {
		if strings.HasPrefix(line, frozenDisableMessage) {
			reported = append(reported, line)
		}
	}

	if len(reported) != 0 {
		t.Errorf("a run that froze nothing still reported: %v", reported)
	}
}

// R1.2 + R1.3 — the writers must not manufacture the origin they are supposed
// to read. `disabled_by` exists so a scan can tell its own bookkeeping from a
// human decision; a re-disable that stamps "auto" over a stated origin hands the
// entry straight back to the reconciliation the origin exists to keep it away
// from, which is the exact defect this story removed — reached through the field
// that removed it.
//
// reconcilesAutomatically already refuses a human origin
// (TestReconcilesOnlyWhatTheCheckerDisabled, row "human origin"), so the policy
// was never the gap. Both halves below are about the WRITERS, and both must be
// asserted: the file is what survives the run, the in-memory mirror is what the
// same run reads back on its next entry.
//
// Reachable through `bentoo overlay autoupdate <pkg>`: CheckPackage does not
// consult enabled/hold, so an explicitly named pin whose ebuild has been removed
// reaches DisableOrphans (cmd/bentoo/overlay_autoupdate.go). CheckAll cannot
// reach it — it filters !IsEnabled() || IsHeld() before checking.
func TestARedisableKeepsAHumanOrigin(t *testing.T) {
	const origin = "maintainer"

	content := `["a/b"]
enabled = false
disabled_by = "` + origin + `"
url = "https://x/y"
parser = "json"
path = "v"
comments = """
Pinned on purpose — do NOT enable.
"""
`

	t.Run("the file writer", func(t *testing.T) {
		overlay, configPath := writePackagesTOML(t, content)
		if err := registry.DisablePackagesInConfig(overlay, []string{"a/b"}); err != nil {
			t.Fatalf("DisablePackagesInConfig: %v", err)
		}
		cfg, err := registry.LoadPackagesConfig(overlay)
		if err != nil {
			t.Fatalf("reload: %v", err)
		}
		got := cfg.Packages["a/b"]
		if got.DisabledBy != origin {
			t.Errorf("DisabledBy = %q, want %q — the disable overwrote a stated origin:\n%s",
				got.DisabledBy, origin, mustRead(t, configPath))
		}
		if reconcilesAutomatically(got) {
			t.Error("the entry is now revivable, so the next scan may publish the bump the origin forbade")
		}
	})

	t.Run("the in-memory mirror", func(t *testing.T) {
		overlay, _ := writePackagesTOML(t, content)
		checker, err := NewChecker(overlay,
			WithConfigDir(t.TempDir()),
			WithRateLimiter(unlimitedRateLimiter()),
		)
		if err != nil {
			t.Fatalf("NewChecker: %v", err)
		}
		if err := checker.DisableOrphans([]string{"a/b"}); err != nil {
			t.Fatalf("DisableOrphans: %v", err)
		}
		got := checker.Config().Packages["a/b"]
		if got.DisabledBy != origin {
			t.Errorf("in memory: DisabledBy = %q, want %q", got.DisabledBy, origin)
		}
		if reconcilesAutomatically(got) {
			t.Error("in memory: the entry is now revivable within this very run")
		}
	})

	// The other direction, so the fix cannot be a predicate that never writes:
	// a record stating NO origin must still be stamped, or DisableOrphans stops
	// recording its own bookkeeping and every auto-disable freezes under R1.3.
	t.Run("a record with no origin is still stamped", func(t *testing.T) {
		overlay, _ := writePackagesTOML(t, `["a/b"]
enabled = false
url = "https://x/y"
parser = "json"
path = "v"
`)
		if err := registry.DisablePackagesInConfig(overlay, []string{"a/b"}); err != nil {
			t.Fatalf("DisablePackagesInConfig: %v", err)
		}
		cfg, err := registry.LoadPackagesConfig(overlay)
		if err != nil {
			t.Fatalf("reload: %v", err)
		}
		if got := cfg.Packages["a/b"]; got.DisabledBy != registry.DisabledByAuto {
			t.Errorf("DisabledBy = %q, want %q", got.DisabledBy, registry.DisabledByAuto)
		}
	})
}
