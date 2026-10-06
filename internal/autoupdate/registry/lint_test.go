package registry

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// writeRegistry creates an overlay whose .autoupdate/packages.toml holds content
// and returns the overlay path.
func writeRegistry(t *testing.T, content string) string {
	t.Helper()
	dir := t.TempDir()
	autoDir := filepath.Join(dir, ".autoupdate")
	if err := os.MkdirAll(autoDir, 0o750); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(autoDir, "packages.toml"), []byte(content), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	return dir
}

// A note on why this stops at lintRecordModel rather than running the whole of
// LintPackagesConfig: a record setting all 38 fields is necessarily invalid
// SEMANTICALLY, because several of them are mutually exclusive by design — this
// one pairs suffix with track = "commit", which ValidatePackageConfig rejects
// because a snapshot suffix comes from the current ebuild. That is a real rule
// working correctly. The scan above covers every rule this sub-task can break
// (layout, field order, the legacy fields); semantic validity belongs to a
// fixture that is semantically coherent, and TestSavePackagesConfigRecordModel
// above is one.

// TestRenderRecordNeverEmitsRedundantEnabled pins the one value a writer must
// swallow. An absent `enabled` already means enabled, so `enabled = true` is the
// redundancy the linter reports — a writer emitting it would generate the finding
// the record was just checked against. `enabled = false` must survive: it is the
// only way to say disabled.
func TestRenderRecordNeverEmitsRedundantEnabled(t *testing.T) {
	base := PackageConfig{
		URL: "https://e.com", Parser: "json", Path: "v",
		Comments: "x — doc.\n",
	}

	on, off := true, false

	withOn := base
	withOn.Enabled = &on
	if got := RenderRecord("dev-util/x", &withOn); strings.Contains(got, "enabled") {
		t.Errorf("enabled = true was emitted:\n%s", got)
	}

	withOff := base
	withOff.Enabled = &off
	if got := RenderRecord("dev-util/x", &withOff); !strings.Contains(got, "enabled = false") {
		t.Errorf("enabled = false was dropped:\n%s", got)
	}
}

// TestFormatCommentsFieldEscaping covers the two ways a doc string could break
// the file it is written into.
func TestFormatCommentsFieldEscaping(t *testing.T) {
	t.Run("a triple quote cannot close the string early", func(t *testing.T) {
		got := formatCommentsField(`x — upstream writes """ in its changelog.`)
		if strings.Count(got, `"""`) != 2 {
			t.Fatalf("unescaped triple quote in:\n%s", got)
		}
	})

	t.Run("a bracket line is indented out of header shape", func(t *testing.T) {
		got := formatCommentsField("x — the path is\n[0].version\nfor this API.")
		if !strings.Contains(got, "\n [0].version\n") {
			t.Fatalf("bracket line not indented:\n%s", got)
		}
	})

	t.Run("a backslash survives the round trip", func(t *testing.T) {
		got := formatCommentsField(`x — the pattern is '\d+'.`)
		if !strings.Contains(got, `'\\d+'`) {
			t.Fatalf("backslash not escaped:\n%s", got)
		}
	})
}

// TestLintIssueString covers the two shapes a reported issue takes: one anchored
// to a record and line, and one that belongs to no record.
func TestLintIssueString(t *testing.T) {
	withPkg := LintIssue{Line: 42, Package: "app-office/libreoffice", Rule: LintMissingEnd, Message: "not closed"}
	want := `packages.toml:42: [app-office/libreoffice] missing-end: not closed`
	if got := withPkg.String(); got != want {
		t.Fatalf("got %q, want %q", got, want)
	}

	stray := LintIssue{Line: 3, Rule: LintStrayComment, Message: "floating"}
	want = `packages.toml:3: stray-comment: floating`
	if got := stray.String(); got != want {
		t.Fatalf("got %q, want %q", got, want)
	}

	noLine := LintIssue{Package: "dev-util/x", Rule: LintInvalidConfig, Message: "bad suffix"}
	want = `packages.toml: [dev-util/x] invalid-config: bad suffix`
	if got := noLine.String(); got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

// TestCanonicalFieldOrderCoversPackageConfig is the drift guard the
// CanonicalFieldOrder doc comment points at. The order rule ranks a field by
// looking it up in that slice and SKIPS what it cannot rank, so a field added to
// PackageConfig and forgotten here would not be reported as misplaced — it would
// stop being ordered at all, silently. Reflection over the toml tags is what
// makes forgetting impossible.
func TestCanonicalFieldOrderCoversPackageConfig(t *testing.T) {
	rt := reflect.TypeOf(PackageConfig{})

	tagged := make(map[string]bool, rt.NumField())
	for i := 0; i < rt.NumField(); i++ {
		f := rt.Field(i)
		tag := f.Tag.Get("toml")
		if tag == "" || tag == "-" {
			t.Fatalf("PackageConfig.%s has no toml tag; it can never be ordered", f.Name)
		}
		name, _, _ := strings.Cut(tag, ",")
		tagged[name] = true
	}

	seen := make(map[string]int, len(CanonicalFieldOrder))
	for _, field := range CanonicalFieldOrder {
		seen[field]++
		if !tagged[field] {
			t.Errorf("CanonicalFieldOrder lists %q, which no PackageConfig field claims", field)
		}
	}
	for field := range tagged {
		if seen[field] != 1 {
			t.Errorf("field %q appears %d time(s) in CanonicalFieldOrder, want exactly 1", field, seen[field])
		}
	}
	if len(CanonicalFieldOrder) != len(tagged) {
		t.Errorf("CanonicalFieldOrder has %d entries, PackageConfig has %d toml tags",
			len(CanonicalFieldOrder), len(tagged))
	}
	// Explicit, because its absence is a decision rather than an oversight: the
	// classifier is `type`, and `binary` only survives as a retired key the
	// repair migrates away (R1.1).
	if seen["binary"] != 0 {
		t.Error("CanonicalFieldOrder lists the retired key binary; type is the classifier")
	}
}
