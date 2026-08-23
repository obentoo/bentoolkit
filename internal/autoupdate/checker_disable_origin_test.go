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
)

// --- sub-task 1.1 — the field exists, parses, and the linter knows it --------

// R1.1 — the origin has to survive the round trip through the parser before any
// writer can be asked to produce it.
//
// The ABSENT row is the one that matters. Absent means deliberate (R1.3), which
// is the fail-safe direction this whole requirement turns on: a parser that
// turned an absent key into some non-empty default would re-arm the bug the
// story exists to remove, and it would do so invisibly.
func TestDisableOriginParsesFromTheRegistry(t *testing.T) {
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

	cfg, err := LoadPackagesConfig(dir)
	if err != nil {
		t.Fatalf("LoadPackagesConfig: %v", err)
	}
	if got := cfg.Packages["dev-libs/icu-compat"].DisabledBy; got != "auto" {
		t.Errorf("DisabledBy = %q, want %q", got, "auto")
	}
	if got := cfg.Packages["media-libs/libjxl-compat"].DisabledBy; got != "" {
		t.Errorf("an entry carrying no disabled_by parsed as %q, want empty — absent must stay absent", got)
	}
}

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

	issues, err := LintPackagesConfig(dir)
	if err != nil {
		t.Fatalf("LintPackagesConfig: %v", err)
	}
	if len(issues) != 0 {
		t.Fatalf("a registry carrying disabled_by reported %v, want nothing", rules(issues))
	}
}

// --- sub-task 1.2 — the two writers keep the pair consistent -----------------

// R1.1 — a disable the checker performs must say so, or the next scan cannot
// tell it from one a human wrote.
func TestDisableRecordsTheAutomaticOrigin(t *testing.T) {
	content := `["a/b"]
url = "https://x/y"
parser = "json"
path = "v"
`
	overlay, configPath := writePackagesTOML(t, content)
	if err := DisablePackagesInConfig(overlay, []string{"a/b"}); err != nil {
		t.Fatalf("DisablePackagesInConfig: %v", err)
	}
	got, _ := os.ReadFile(configPath)
	if !strings.Contains(string(got), "enabled = false") {
		t.Error("enabled = false was not written")
	}
	if !strings.Contains(string(got), `disabled_by = "auto"`) {
		t.Errorf("the origin was not recorded:\n%s", got)
	}

	cfg, err := LoadPackagesConfig(overlay)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if cfg.Packages["a/b"].DisabledBy != "auto" {
		t.Errorf("DisabledBy = %q, want \"auto\"", cfg.Packages["a/b"].DisabledBy)
	}
}

// R1.1 — the surgical editor must keep the hand-written prose. A full re-encode
// would drop it, which is why DisablePackagesInConfig edits raw text.
//
// GREEN ON ARRIVAL, and recorded as such in .draft/red-evidence.yaml: it is a
// regression guard over the editor 1.2 is about to change, not evidence for 1.2.
func TestDisableKeepsTheCommentsBlock(t *testing.T) {
	content := `["a/b"]
url = "https://x/y"
parser = "json"
path = "v"
comments = """
Pinned on purpose — do NOT enable.
"""
# END
`
	overlay, configPath := writePackagesTOML(t, content)
	if err := DisablePackagesInConfig(overlay, []string{"a/b"}); err != nil {
		t.Fatalf("DisablePackagesInConfig: %v", err)
	}
	got, _ := os.ReadFile(configPath)
	if !strings.Contains(string(got), "Pinned on purpose") || !strings.Contains(string(got), "# END") {
		t.Errorf("the surgical edit lost hand-written content:\n%s", got)
	}
}

// R1.2 — reviving must remove BOTH keys. Leaving disabled_by = "auto" on an
// enabled entry strands a claim about a state the entry is no longer in, and
// the linter reports it.
func TestEnableRemovesTheOriginToo(t *testing.T) {
	content := `["a/b"]
enabled = false
disabled_by = "auto"
url = "https://x/y"
parser = "json"
path = "v"
`
	overlay, configPath := writePackagesTOML(t, content)
	if err := EnablePackagesInConfig(overlay, []string{"a/b"}); err != nil {
		t.Fatalf("EnablePackagesInConfig: %v", err)
	}
	got, _ := os.ReadFile(configPath)
	if strings.Contains(string(got), "disabled_by") {
		t.Errorf("the origin survived the revive:\n%s", got)
	}
	if strings.Contains(string(got), "enabled") {
		t.Errorf("the enabled assignment survived the revive:\n%s", got)
	}
}

// R1.2 — the delete must be driven by the key being THERE, not by an assumption
// that the pair always travels together. Every one of the ~90 records disabled
// before this story carries `enabled` and no origin, so the revive path meets
// that shape far more often than the complete one, and a delete written as
// "remove the line after enabled" would eat the url.
func TestEnableWithoutAnOriginIsANoOp(t *testing.T) {
	content := `["a/b"]
enabled = false
url = "https://x/y"
parser = "json"
path = "v"
`
	overlay, configPath := writePackagesTOML(t, content)
	if err := EnablePackagesInConfig(overlay, []string{"a/b"}); err != nil {
		t.Fatalf("EnablePackagesInConfig: %v", err)
	}
	got := string(mustRead(t, configPath))
	if strings.Contains(got, "enabled") {
		t.Errorf("the enabled assignment survived the revive:\n%s", got)
	}
	for _, keep := range []string{`url = "https://x/y"`, `parser = "json"`, `path = "v"`} {
		if !strings.Contains(got, keep) {
			t.Errorf("the revive removed %s, which it was never asked to touch:\n%s", keep, got)
		}
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

// --- orchestrator-authored, Run mode ----------------------------------------
//
// The two guards below close a gap the sub-task 1.2 mutation proof exposed, and
// they are recorded in .draft/deviations.yaml rather than smuggled in.
//
// TestDisableKeepsTheCommentsBlock above is a DELETION guard: it asserts the
// hand-written prose survived. It says nothing about WHERE the new keys landed.
// A mutant that inserts them immediately after the `comments = """` opener
// leaves every byte of prose in place — and passes — while producing a record
// that is still ENABLED with no origin, because both keys were swallowed into
// the doc string. The whole disable is voided and the guard is green.
//
// A second mutation showed the `inComments` mask is not exercised at all: with
// it bypassed entirely, all 226 tests of the package still passed, because no
// fixture anywhere puts an `enabled =` line inside a doc body.
//
// R5 is the requirement that forbids leaving it there: a guard that cannot fail
// for the defect it exists for is what this story is about.

// R1.1 — the disable must be READABLE BACK, which is the property the prose
// guard cannot see. Asserting on the reloaded config rather than on the file's
// bytes is deliberate: it is the only formulation that fails when the keys are
// written somewhere the parser will not look.
func TestDisableIsReadableBackThroughACommentsBlock(t *testing.T) {
	content := `["a/b"]
url = "https://x/y"
parser = "json"
path = "v"
comments = """
Pinned on purpose — do NOT enable.
"""
# END
`
	overlay, _ := writePackagesTOML(t, content)
	if err := DisablePackagesInConfig(overlay, []string{"a/b"}); err != nil {
		t.Fatalf("DisablePackagesInConfig: %v", err)
	}

	cfg, err := LoadPackagesConfig(overlay)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	pkg := cfg.Packages["a/b"]
	if pkg.IsEnabled() {
		t.Error("the record is still enabled after a disable: the keys were written where the parser cannot see them")
	}
	if pkg.DisabledBy != "auto" {
		t.Errorf("DisabledBy = %q, want \"auto\" — the origin did not reach the parser either", pkg.DisabledBy)
	}
}

// R1.1, Constraint — the inComments mask, exercised. A doc body may legally
// quote the very keys the editor rewrites; PackageConfig.Comments documents that
// hazard and editPackagesConfigSections carries the mask for it, but until now
// no fixture in the package put such a line inside a doc string, so the mask
// could be deleted outright without turning a single test red.
func TestDisableDoesNotEditKeysQuotedInsideTheDocBody(t *testing.T) {
	// The two quoted lines START with the keys, which is the only shape the
	// anchored regexes (^\s*enabled\s*= and ^\s*disabled_by\s*=) can match. A
	// doc body that merely MENTIONS the keys mid-sentence exercises nothing:
	// the first draft of this test did exactly that and survived the mask being
	// deleted, which is how the flaw was found.
	content := `["a/b"]
url = "https://x/y"
parser = "json"
path = "v"
comments = """
Do not undo this by hand. What the checker writes into a disabled record is
enabled = true
disabled_by = "manual"
quoted verbatim above so a maintainer knows the exact two lines to look for.
"""
# END
`
	overlay, configPath := writePackagesTOML(t, content)
	if err := DisablePackagesInConfig(overlay, []string{"a/b"}); err != nil {
		t.Fatalf("DisablePackagesInConfig: %v", err)
	}

	got := string(mustRead(t, configPath))
	for _, quoted := range []string{
		"enabled = true",
		`disabled_by = "manual"`,
	} {
		if !strings.Contains(got, quoted) {
			t.Errorf("the editor rewrote a line quoted inside the doc body; %q is gone:\n%s", quoted, got)
		}
	}

	// And the real keys still landed, so this is not passing by the editor
	// having simply declined to do anything.
	cfg, err := LoadPackagesConfig(overlay)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	pkg := cfg.Packages["a/b"]
	if pkg.IsEnabled() || pkg.DisabledBy != "auto" {
		t.Error("the disable did not take effect; this guard cannot fail for its own reason")
	}
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
		pkg  PackageConfig
		want bool
	}{
		{"auto-disabled, ebuild back", PackageConfig{Enabled: &no, DisabledBy: "auto"}, true},
		{"legacy disable, no origin", PackageConfig{Enabled: &no}, false},
		{"human origin", PackageConfig{Enabled: &no, DisabledBy: "maintainer"}, false},
		{"held and disabled", PackageConfig{Enabled: &no, DisabledBy: "auto", Hold: true}, false},
		{"held, no origin", PackageConfig{Enabled: &no, Hold: true}, false},
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
// The notice is asserted on the builder rather than on stderr because
// logger.Logger.output is unexported with no setter, and because this is the
// convention the codebase already uses for message text (validate/build.go's
// passReason, skipReason, notReachedReason are pure builders tested directly).
func TestLegacyDisableIsNamedInTheOutput(t *testing.T) {
	notice := frozenDisableNotice([]string{"dev-libs/icu-compat", "media-libs/libjxl-compat"})

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
	if frozenDisableNotice(nil) != "" {
		t.Errorf("an empty frozen set produced a notice: %q — a run with nothing to report must say nothing", frozenDisableNotice(nil))
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

			res := checker.CheckAll(false)

			if got := checker.Config().Packages[pkg]; got.IsEnabled() != tc.wantEnabled {
				t.Errorf("in memory: enabled = %v, want %v", got.IsEnabled(), tc.wantEnabled)
			}
			cfg, err := LoadPackagesConfig(overlay)
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

			res := checker.CheckAll(false)

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
// Recorded in .draft/deviations.yaml. The seam it asserts through
// (reportFrozenDisables) exists for this test and says so at its definition.

// R1.4 — the run must NAME the entry it froze. Asserting the call happened, with
// the atom in it, is the only formulation that fails when the reporting is
// dropped while the notice builder survives.
func TestCheckAllReportsTheEntryItFroze(t *testing.T) {
	var reported []string
	original := reportFrozenDisables
	reportFrozenDisables = func(notice string) { reported = append(reported, notice) }
	t.Cleanup(func() { reportFrozenDisables = original })

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
	)
	if err != nil {
		t.Fatalf("NewChecker: %v", err)
	}
	checker.CheckAll(false)

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
	var reported []string
	original := reportFrozenDisables
	reportFrozenDisables = func(notice string) { reported = append(reported, notice) }
	t.Cleanup(func() { reportFrozenDisables = original })

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
	)
	if err != nil {
		t.Fatalf("NewChecker: %v", err)
	}
	checker.CheckAll(false)

	if len(reported) != 0 {
		t.Errorf("a run that froze nothing still reported: %v", reported)
	}
}

// --- sub-task 1.4 — the legacy migration ------------------------------------
//
// Authored by the orchestrator: .draft/authored-tests/ carries no fragment for
// 1.4, and tasks.md states the contract instead. The name matches the sub-task's
// stated -run selector, 'DisableOriginMigration'.

// R1.5 — absent-means-deliberate is the fail-safe direction, and its price is
// that every entry disabled before the field existed also stops reconciling.
// This is the tool that pays it, and each row is a way the tool could be wrong.
//
// The `except` row is the whole point of the exclusion list: dev-libs/icu-compat
// and media-libs/libjxl-compat were disabled ON PURPOSE and are exactly the two
// records a blanket migration would re-arm, undoing the story it belongs to.
func TestDisableOriginMigrationMarksOnlyWhatItShould(t *testing.T) {
	content := `["dev-libs/icu-compat"]
enabled = false
url = "https://x/1"
parser = "json"
path = "v"
comments = "pinned on purpose — carries the ICU 77 ABI."
# END
["net-misc/gone"]
enabled = false
url = "https://x/2"
parser = "json"
path = "v"
comments = "the ebuild vanished from the overlay."
# END
["app-misc/already-stamped"]
enabled = false
disabled_by = "auto"
url = "https://x/3"
parser = "json"
path = "v"
comments = "already migrated by an earlier run."
# END
["sci-ml/held-and-disabled"]
hold = true
enabled = false
url = "https://x/4"
parser = "json"
path = "v"
comments = "held by a maintainer; hold already protects it."
# END
["dev-util/live"]
url = "https://x/5"
parser = "json"
path = "v"
comments = "enabled, and none of this concerns it."
# END
`
	overlay, _ := writePackagesTOML(t, content)

	marked, err := MarkAutoDisabled(overlay, []string{"dev-libs/icu-compat"})
	if err != nil {
		t.Fatalf("MarkAutoDisabled: %v", err)
	}
	if len(marked) != 1 || marked[0] != "net-misc/gone" {
		t.Errorf("marked = %v, want exactly [net-misc/gone]", marked)
	}

	cfg, err := LoadPackagesConfig(overlay)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	for _, tc := range []struct {
		pkg  string
		want string
		why  string
	}{
		{"net-misc/gone", "auto", "a plain legacy disable is what the migration exists for"},
		{"dev-libs/icu-compat", "", "an excluded entry must survive: marking it re-arms the bug this story fixes"},
		{"app-misc/already-stamped", "auto", "an entry already stamped is left as it is, so the run is idempotent"},
		{"sci-ml/held-and-disabled", "", "a held entry was not disabled by the checker; stamping it would be a false claim"},
		{"dev-util/live", "", "an enabled entry has no disable to explain"},
	} {
		if got := cfg.Packages[tc.pkg].DisabledBy; got != tc.want {
			t.Errorf("%s: DisabledBy = %q, want %q — %s", tc.pkg, got, tc.want, tc.why)
		}
	}

	// Nothing may have been ENABLED by a migration whose only job is to stamp.
	for _, pkg := range []string{"dev-libs/icu-compat", "net-misc/gone", "app-misc/already-stamped", "sci-ml/held-and-disabled"} {
		entry := cfg.Packages[pkg]
		if entry.IsEnabled() {
			t.Errorf("%s was re-enabled by the migration, which only ever stamps", pkg)
		}
	}
}

// R1.5 — a second run must change nothing. Without this the migration could be
// safe once and destructive on the re-run an operator does after adding an
// entry, and the registry auto-commits and pushes, so a spurious rewrite reaches
// origin within minutes.
func TestDisableOriginMigrationIsIdempotent(t *testing.T) {
	content := `["net-misc/gone"]
enabled = false
url = "https://x/2"
parser = "json"
path = "v"
comments = "the ebuild vanished from the overlay."
# END
`
	overlay, configPath := writePackagesTOML(t, content)

	if _, err := MarkAutoDisabled(overlay, nil); err != nil {
		t.Fatalf("first pass: %v", err)
	}
	first := string(mustRead(t, configPath))

	marked, err := MarkAutoDisabled(overlay, nil)
	if err != nil {
		t.Fatalf("second pass: %v", err)
	}
	if len(marked) != 0 {
		t.Errorf("the second pass marked %v, want nothing", marked)
	}
	if second := string(mustRead(t, configPath)); second != first {
		t.Errorf("the second pass rewrote the file:\n--- first ---\n%s\n--- second ---\n%s", first, second)
	}
	if !strings.Contains(first, `disabled_by = "auto"`) {
		t.Fatalf("the first pass never stamped anything; this test cannot fail for its own reason:\n%s", first)
	}
}

// R1.5, Constraint — the migration decides from the PARSED registry, never from
// a text scan. The header of the real packages.toml records what grep does to
// this question: 98 hits unanchored, 95 anchored, 92 real. The doc body below
// reproduces that trap, and the comments field is the documented place a record
// explains itself, so this shape is ordinary rather than contrived.
func TestDisableOriginMigrationIgnoresTheDocBody(t *testing.T) {
	content := `["dev-util/live"]
url = "https://x/5"
parser = "json"
path = "v"
comments = """
This entry is ENABLED. The line below is quoted documentation, not configuration:
enabled = false
Anything deciding by text scan will read it as a disabled record.
"""
# END
`
	overlay, _ := writePackagesTOML(t, content)

	marked, err := MarkAutoDisabled(overlay, nil)
	if err != nil {
		t.Fatalf("MarkAutoDisabled: %v", err)
	}
	if len(marked) != 0 {
		t.Errorf("marked %v — an enabled record was read as disabled from its own documentation", marked)
	}
	cfg, err := LoadPackagesConfig(overlay)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if cfg.Packages["dev-util/live"].DisabledBy != "" {
		t.Error("an enabled record was stamped")
	}
}
