package registry

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
