package registry

import (
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func cdIntPtr(n int) *int { return &n }

// cdApply plans and writes one edit, failing the test on any error.
func cdApply(t *testing.T, overlay string, req CooldownRequest) *CooldownEdit {
	t.Helper()
	edit, err := PlanCooldownEdit(overlay, req)
	if err != nil {
		t.Fatalf("PlanCooldownEdit(%v): %v", req.Keys, err)
	}
	if err := edit.Write(); err != nil {
		t.Fatalf("Write(%v): %v", req.Keys, err)
	}
	return edit
}

func cdRegistry(t *testing.T, overlay string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(overlay, ".autoupdate", "packages.toml"))
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

// TestCooldownEditGolden: set on a record without an override (inserted in
// canonical position), set on one with an override (rewritten in place),
// set on one release line of two, set on a record whose only later field is
// comments, and unset — the file must equal the golden byte for byte.
func TestCooldownEditGolden(t *testing.T) {
	overlay := writeRegistry(t, readFixture(t, "cooldown_set_input.toml"))
	cdApply(t, overlay, CooldownRequest{
		Keys: []string{"app-misc/plain", "app-misc/override", "app-editors/multi@stable", "dev-libs/docs-only"},
		Days: cdIntPtr(3), Reason: `upstream ships "hotfix" releases`,
	})
	cdApply(t, overlay, CooldownRequest{Keys: []string{"app-misc/dropme"}})

	got := cdRegistry(t, overlay)
	if want := readFixture(t, "cooldown_set_golden.toml"); got != want {
		t.Fatalf("edited fixture does not match testdata/cooldown_set_golden.toml: %s", describeFirstDifference(want, got))
	}
	if issues, err := LintPackagesConfig(slog.Default(), overlay); err != nil || len(issues) > 0 {
		t.Errorf("the edited registry does not lint clean: %v %v", err, issues)
	}
}

func TestCooldownEditSetReportsOldAndNew(t *testing.T) {
	overlay := writeRegistry(t, readFixture(t, "cooldown_set_input.toml"))
	edit := cdApply(t, overlay, CooldownRequest{Keys: []string{"app-misc/override", "app-misc/plain"}, Days: cdIntPtr(0), Reason: "first-party"})
	if len(edit.Changes) != 2 {
		t.Fatalf("changes = %+v, want 2", edit.Changes)
	}
	ov, pl := edit.Changes[0], edit.Changes[1]
	if ov.Key != "app-misc/override" || *ov.OldDays != 5 || *ov.NewDays != 0 || *ov.OldReason != `old reason with a "quote"` {
		t.Errorf("override change = %+v", ov)
	}
	if pl.Key != "app-misc/plain" || pl.OldDays != nil || pl.OldReason != nil || *pl.NewReason != "first-party" {
		t.Errorf("plain change = %+v", pl)
	}
	cfg, err := LoadPackagesConfig(overlay)
	if err != nil {
		t.Fatal(err)
	}
	if rec := cfg.Packages["app-misc/plain"]; !rec.CooldownOff() {
		t.Errorf("app-misc/plain is not out of the cooldown after --days 0: %+v", rec.CooldownDays)
	}
}

func TestCooldownEditNoOpWritesNothing(t *testing.T) {
	overlay := writeRegistry(t, readFixture(t, "cooldown_set_input.toml"))
	path := filepath.Join(overlay, ".autoupdate", "packages.toml")
	before, _ := os.Stat(path)
	edit, err := PlanCooldownEdit(overlay, CooldownRequest{Keys: []string{"app-misc/plain"}})
	if err != nil {
		t.Fatal(err)
	}
	if edit.Changed() || len(edit.Changes) != 0 {
		t.Fatalf("unset on a record without an override changed something: %+v", edit.Changes)
	}
	if err := edit.Write(); err != nil {
		t.Fatal(err)
	}
	if after, _ := os.Stat(path); !after.ModTime().Equal(before.ModTime()) {
		t.Error("a no-op edit rewrote the file")
	}
}

func TestCooldownEditKeyResolution(t *testing.T) {
	overlay := writeRegistry(t, readFixture(t, "cooldown_set_input.toml"))
	days := cdIntPtr(2)

	t.Run("ambiguous bare key", func(t *testing.T) {
		_, err := PlanCooldownEdit(overlay, CooldownRequest{Keys: []string{"app-editors/multi"}, Days: days, Reason: "r"})
		var amb *AmbiguousKeyError
		if !errors.As(err, &amb) {
			t.Fatalf("err = %v, want *AmbiguousKeyError", err)
		}
		if !slices.Equal(amb.Matches, []string{"app-editors/multi@preview", "app-editors/multi@stable"}) {
			t.Errorf("matches = %v", amb.Matches)
		}
		if !strings.Contains(err.Error(), "app-editors/multi@preview") || !strings.Contains(err.Error(), "app-editors/multi@stable") {
			t.Errorf("error does not list the records: %v", err)
		}
	})
	t.Run("all lines", func(t *testing.T) {
		edit, err := PlanCooldownEdit(overlay, CooldownRequest{Keys: []string{"app-editors/multi"}, AllLines: true, Days: days, Reason: "r"})
		if err != nil {
			t.Fatal(err)
		}
		if !slices.Equal(edit.Targets, []string{"app-editors/multi@preview", "app-editors/multi@stable"}) {
			t.Errorf("targets = %v", edit.Targets)
		}
	})
	t.Run("exact key names its siblings", func(t *testing.T) {
		edit, err := PlanCooldownEdit(overlay, CooldownRequest{Keys: []string{"app-editors/multi@stable"}, Days: days, Reason: "r"})
		if err != nil {
			t.Fatal(err)
		}
		if got := edit.Siblings["app-editors/multi@stable"]; !slices.Equal(got, []string{"app-editors/multi@preview"}) {
			t.Errorf("siblings = %v", edit.Siblings)
		}
	})
	t.Run("unknown key", func(t *testing.T) {
		_, err := PlanCooldownEdit(overlay, CooldownRequest{Keys: []string{"app-misc/nope"}, Days: days, Reason: "r"})
		if !errors.Is(err, ErrUnknownRecord) || !strings.Contains(err.Error(), "app-misc/nope") {
			t.Errorf("err = %v, want ErrUnknownRecord naming the key", err)
		}
	})
}

const cdGroupRegistry = `["dev-libs/grp-a"]
url = "https://example.invalid/a.json"
parser = "json"
path = "version"
cooldown_with = ["dev-libs/grp-b"]
cooldown_days = 2
cooldown_reason = "released together"
comments = """
a
"""
# END

["dev-libs/grp-b"]
url = "https://example.invalid/b.json"
parser = "json"
path = "version"
cooldown_with = ["dev-libs/grp-a"]
cooldown_days = 2
cooldown_reason = "released together"
comments = """
b
"""
# END
`

func TestCooldownEditGroups(t *testing.T) {
	t.Run("setting one member sets the group", func(t *testing.T) {
		overlay := writeRegistry(t, cdGroupRegistry)
		edit := cdApply(t, overlay, CooldownRequest{Keys: []string{"dev-libs/grp-a"}, Days: cdIntPtr(4), Reason: "slower"})
		if !slices.Equal(edit.GroupAdded, []string{"dev-libs/grp-b"}) || len(edit.Changes) != 2 {
			t.Fatalf("group added = %v, changes = %+v", edit.GroupAdded, edit.Changes)
		}
		cfg, err := LoadPackagesConfig(overlay)
		if err != nil {
			t.Fatal(err)
		}
		if err := cfg.ValidateAll(slog.Default()); err != nil {
			t.Fatalf("registry no longer validates: %v", err)
		}
		for _, k := range []string{"dev-libs/grp-a", "dev-libs/grp-b"} {
			rec := cfg.Packages[k]
			if d := rec.CooldownDaysOverride(); d != 4 {
				t.Errorf("%s cooldown_days = %d, want 4", k, d)
			}
		}
	})
	t.Run("unsetting one member unsets the group", func(t *testing.T) {
		overlay := writeRegistry(t, cdGroupRegistry)
		cdApply(t, overlay, CooldownRequest{Keys: []string{"dev-libs/grp-b"}})
		if got := cdRegistry(t, overlay); strings.Contains(got, "cooldown_days") || strings.Contains(got, "cooldown_reason") {
			t.Errorf("an override survived:\n%s", got)
		}
	})
	t.Run("zero is refused naming the members", func(t *testing.T) {
		overlay := writeRegistry(t, cdGroupRegistry)
		_, err := PlanCooldownEdit(overlay, CooldownRequest{Keys: []string{"dev-libs/grp-a"}, Days: cdIntPtr(0), Reason: "r"})
		if err == nil || !strings.Contains(err.Error(), "dev-libs/grp-a, dev-libs/grp-b") {
			t.Errorf("err = %v, want a refusal naming both members", err)
		}
		if got := cdRegistry(t, overlay); got != cdGroupRegistry {
			t.Error("a refused edit changed the file")
		}
	})
}

func TestCooldownEditValidatesValues(t *testing.T) {
	overlay := writeRegistry(t, readFixture(t, "cooldown_set_input.toml"))
	for name, req := range map[string]CooldownRequest{
		"negative days":     {Days: cdIntPtr(-1), Reason: "r"},
		"days over bound":   {Days: cdIntPtr(3651), Reason: "r"},
		"missing reason":    {Days: cdIntPtr(3)},
		"blank reason":      {Days: cdIntPtr(3), Reason: "  "},
		"multi-line reason": {Days: cdIntPtr(3), Reason: "a\nb"},
		"no key":            {},
	} {
		t.Run(name, func(t *testing.T) {
			if name != "no key" {
				req.Keys = []string{"app-misc/plain"}
			}
			if _, err := PlanCooldownEdit(overlay, req); err == nil {
				t.Error("accepted")
			}
		})
	}
	if _, err := PlanCooldownEdit(overlay, CooldownRequest{Keys: []string{"app-misc/plain"}, Days: cdIntPtr(3650), Reason: "r"}); err != nil {
		t.Errorf("the bound itself was refused: %v", err)
	}
}

// A line inside a multi-line string other than comments that looks like an
// assignment must not be edited as one: the decode gate refuses the edit.
func TestCooldownEditRefusesToTouchAMultiLineValue(t *testing.T) {
	const reg = `["app-misc/trap"]
url = "https://example.invalid/t.json"
parser = "json"
path = "version"
patched = """
cooldown_days = 9
"""
comments = """
t
"""
# END
`
	overlay := writeRegistry(t, reg)
	if _, err := PlanCooldownEdit(overlay, CooldownRequest{Keys: []string{"app-misc/trap"}, Days: cdIntPtr(3), Reason: "r"}); err == nil {
		t.Fatal("an edit that rewrote a line of patched was accepted")
	}
	if got := cdRegistry(t, overlay); got != reg {
		t.Error("the refused edit changed the file")
	}
}

func TestCooldownEditRollsBackWhenThePostWriteLintFails(t *testing.T) {
	overlay := writeRegistry(t, readFixture(t, "cooldown_set_input.toml"))
	original := cdRegistry(t, overlay)
	old := cooldownPostWriteLint
	cooldownPostWriteLint = func(*slog.Logger, string) ([]LintIssue, error) { return nil, errors.New("injected") }
	t.Cleanup(func() { cooldownPostWriteLint = old })

	edit, err := PlanCooldownEdit(overlay, CooldownRequest{Keys: []string{"app-misc/plain"}, Days: cdIntPtr(3), Reason: "r"})
	if err != nil {
		t.Fatal(err)
	}
	err = edit.Write()
	if err == nil || !strings.Contains(err.Error(), "original restored") {
		t.Fatalf("Write = %v, want a rollback error", err)
	}
	if got := cdRegistry(t, overlay); got != original {
		t.Error("the original was not restored")
	}
}

func TestCooldownEditRefusesAFileChangedSinceThePlan(t *testing.T) {
	overlay := writeRegistry(t, readFixture(t, "cooldown_set_input.toml"))
	edit, err := PlanCooldownEdit(overlay, CooldownRequest{Keys: []string{"app-misc/plain"}, Days: cdIntPtr(3), Reason: "r"})
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(overlay, ".autoupdate", "packages.toml")
	changed := cdRegistry(t, overlay) + "\n"
	if err := os.WriteFile(path, []byte(changed), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := edit.Write(); err == nil {
		t.Fatal("wrote over a file that changed since the plan")
	}
	if got := cdRegistry(t, overlay); got != changed {
		t.Error("the changed file was overwritten")
	}
}

// A finding the original already had is not new because an insertion moved
// its line; a second copy of it, or a different finding, is.
func TestNewLintIssuesIgnoresShiftedLines(t *testing.T) {
	old := LintIssue{Package: "a/b", Rule: LintInvalidConfig, Message: "m", Line: 10}
	moved := old
	moved.Line = 12
	other := LintIssue{Package: "a/c", Rule: LintInvalidConfig, Message: "m"}
	fresh := newLintIssues([]LintIssue{old}, []LintIssue{moved, moved, other})
	if len(fresh) != 2 || fresh[0].Package != "a/b" || fresh[1].Package != "a/c" {
		t.Errorf("fresh = %+v, want the duplicate and a/c", fresh)
	}
}
