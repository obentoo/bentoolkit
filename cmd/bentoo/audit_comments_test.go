package main

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// The audit-comments Makefile target is a repository guard: it scans the
// non-test Go files of the directories named in AUDIT_COMMENTS_DIRS (default
// "cmd internal") and fails on two defects -- a line matching one of six
// tracker-ID regexes, and a run of 20 or more consecutive "//" lines that is
// not a package doc comment. These tests drive the real target through make
// over fixture trees built in a temporary directory, so the repository's own
// source never decides the outcome.

// auditRepoRoot returns the absolute path of the repository root; tests in this
// package run from cmd/bentoo.
func auditRepoRoot(t *testing.T) string {
	t.Helper()
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatalf("resolving repository root: %v", err)
	}
	return root
}

// acRequireMake skips the test when GNU make is not installed.
func acRequireMake(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("make"); err != nil {
		t.Skip("make is not on PATH")
	}
}

// acMakeEnv is the test process environment with every variable that could leak
// a make invocation's state into the child removed (a parent make exports
// MAKEFLAGS, which can carry command-line variable overrides), and with the C
// locale so make's own diagnostics are in English.
func acMakeEnv() []string {
	drop := map[string]bool{
		"MAKEFLAGS": true, "MFLAGS": true, "MAKELEVEL": true,
		"MAKEOVERRIDES": true, "GNUMAKEFLAGS": true, "AUDIT_COMMENTS_DIRS": true,
		"LC_ALL": true, "LANG": true, "LANGUAGE": true, "LC_MESSAGES": true,
	}
	var env []string
	for _, kv := range os.Environ() {
		name, _, _ := strings.Cut(kv, "=")
		if !drop[name] {
			env = append(env, kv)
		}
	}
	return append(env, "LC_ALL=C")
}

// acRunMake runs make with args and returns combined output and exit status. A
// make that could not find the audit-comments rule fails the test outright, so
// no "exit non-zero" assertion below can be satisfied by a missing target.
func acRunMake(t *testing.T, dir string, args ...string) (string, int) {
	t.Helper()
	full := append([]string{"-s", "--no-print-directory"}, args...)
	cmd := exec.Command("make", full...)
	cmd.Dir = dir
	cmd.Env = acMakeEnv()
	out, err := cmd.CombinedOutput()
	code := 0
	if err != nil {
		var exitErr *exec.ExitError
		if !errors.As(err, &exitErr) {
			t.Fatalf("running make %v: %v", full, err)
		}
		code = exitErr.ExitCode()
	}
	text := string(out)
	if strings.Contains(text, "No rule to make target") {
		t.Fatalf("the Makefile has no audit-comments target:\n%s", text)
	}
	return text, code
}

// acAuditDirs runs `make audit-comments` from the repository root over the given
// absolute directories.
func acAuditDirs(t *testing.T, dirs ...string) (string, int) {
	t.Helper()
	return acRunMake(t, auditRepoRoot(t), "-C", auditRepoRoot(t), "audit-comments",
		"AUDIT_COMMENTS_DIRS="+strings.Join(dirs, " "))
}

// acWriteTree creates each relative path under root with its content.
func acWriteTree(t *testing.T, root string, files map[string]string) {
	t.Helper()
	for rel, content := range files {
		path := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatalf("creating directory for %s: %v", rel, err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatalf("writing %s: %v", rel, err)
		}
	}
}

// acGoFile joins lines into a file body; line N of the file is lines[N-1].
func acGoFile(lines ...string) string {
	return strings.Join(lines, "\n") + "\n"
}

// acCommentBlock returns n consecutive comment lines, each prefixed by indent.
// Every fifth line is a bare "//", which is still a comment line.
func acCommentBlock(n int, indent string) []string {
	out := make([]string, 0, n)
	for i := 1; i <= n; i++ {
		if i%5 == 0 {
			out = append(out, indent+"//")
			continue
		}
		out = append(out, indent+"// explanation line "+strconv.Itoa(i)+" of a long note")
	}
	return out
}

// acLines returns its arguments as a slice of file lines.
func acLines(l ...string) []string { return l }

// acCat concatenates line slices into a new slice.
func acCat(parts ...[]string) []string {
	var out []string
	for _, p := range parts {
		out = append(out, p...)
	}
	return out
}

// acNonEmptyLines returns the output lines that carry text.
func acNonEmptyLines(out string) []string {
	var lines []string
	for _, l := range strings.Split(out, "\n") {
		if strings.TrimSpace(l) != "" {
			lines = append(lines, l)
		}
	}
	return lines
}

// acRequireLocation fails unless out names base:line (the file's base name
// followed by exactly that line number).
func acRequireLocation(t *testing.T, out, base string, line int) {
	t.Helper()
	re := regexp.MustCompile(`(^|[\s/])` + regexp.QuoteMeta(base) + `:` + strconv.Itoa(line) + `($|\D)`)
	for _, l := range strings.Split(out, "\n") {
		if re.MatchString(l) {
			return
		}
	}
	t.Errorf("output does not name %s:%d\n--- output ---\n%s", base, line, out)
}

// acRequireNoLocation fails when out names base at any line.
func acRequireNoLocation(t *testing.T, out, base string) {
	t.Helper()
	re := regexp.MustCompile(`(^|[\s/])` + regexp.QuoteMeta(base) + `:[0-9]+`)
	if re.MatchString(out) {
		t.Errorf("output names %s, which must not be reported\n--- output ---\n%s", base, out)
	}
}

// acCleanSummary runs the guard over a tree with no defect and returns the one
// summary line it prints, so other cases can check they never print it.
func acCleanSummary(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	acWriteTree(t, root, map[string]string{"clean.go": acGoFile(
		"// Package fixture is a clean fixture.",
		"package fixture",
		"",
		"// Add returns the sum of a and b.",
		"func Add(a, b int) int { return a + b }",
	)})
	out, code := acAuditDirs(t, root)
	lines := acNonEmptyLines(out)
	if code != 0 || len(lines) != 1 {
		t.Fatalf("clean tree: want exit 0 and one summary line, got exit %d and %d lines\n%s", code, len(lines), out)
	}
	return strings.TrimSpace(lines[0])
}

// acRequireFailure checks a defect run: non-zero exit and no clean summary.
func acRequireFailure(t *testing.T, out string, code int) {
	t.Helper()
	if code == 0 {
		t.Errorf("want a non-zero exit, got 0\n--- output ---\n%s", out)
	}
	summary := acCleanSummary(t)
	if strings.Contains(out, summary) {
		t.Errorf("a failing run printed the clean summary %q\n--- output ---\n%s", summary, out)
	}
}

// acRequireClean checks a run that must pass: exit 0 and exactly one line.
func acRequireClean(t *testing.T, out string, code int) {
	t.Helper()
	if code != 0 {
		t.Errorf("want exit 0, got %d\n--- output ---\n%s", code, out)
	}
	if n := len(acNonEmptyLines(out)); n != 1 {
		t.Errorf("want exactly one summary line, got %d\n--- output ---\n%s", n, out)
	}
}

// TestAuditCommentsFlagsEachTrackerIDForm puts one tracker-ID form on a known
// line of an otherwise clean file and expects the guard to fail and name it.
// Each sample matches exactly one of the six regexes, except the
// letter-suffixed form, which by construction also matches the bare form.
func TestAuditCommentsFlagsEachTrackerIDForm(t *testing.T) {
	t.Parallel()
	acRequireMake(t)
	samples := []struct{ name, text string }{
		{"bare_S_code", "the retry rule from S123 still applies"},
		{"S_code_with_requirement_suffix", "the retry rule from S123-RX9 still applies"},
		{"dotted_requirement_number", "the retry rule from R9.9 still applies"},
		{"parenthesised_letter_number", "the retry rule (D9) still applies"},
		{"parenthesised_letter_number_mid_phrase", "the retry rule (see note, T3 here) still applies"},
		{"hyphenated_sub_task_number", "the retry rule from sub-task 13.4 still applies"},
		{"capitalised_plural_subtasks", "the retry rule from Subtasks 7 still applies"},
		{"lowercase_story_number", "the retry rule from story 123 still applies"},
		{"capitalised_plural_stories", "the retry rule from Stories 123 still applies"},
	}
	for _, s := range samples {
		t.Run(s.name, func(t *testing.T) {
			t.Parallel()
			root := t.TempDir()
			acWriteTree(t, root, map[string]string{"form.go": acGoFile(
				"package fixture",
				"",
				"func f() {}",
				"",
				"// "+s.text,
				"func g() {}",
			)})
			out, code := acAuditDirs(t, root)
			acRequireFailure(t, out, code)
			acRequireLocation(t, out, "form.go", 5)
		})
	}
}

// TestAuditCommentsIgnoresNearMissIDs holds text that resembles a tracker ID
// but breaks one constraint of every regex (digit count, case, word boundary,
// letter range). A guard looser than the six patterns would flag it.
func TestAuditCommentsIgnoresNearMissIDs(t *testing.T) {
	acRequireMake(t)
	root := t.TempDir()
	acWriteTree(t, root, map[string]string{"near.go": acGoFile(
		"package fixture",
		"",
		"// S1234 rows and S12 rows and TS123 and S123x are not codes.",
		"// story 12, a history 123 entries long, and a substory 123.",
		"// (K9) and (D123) and (d9) and (go1.26) are not codes either.",
		"// r9.9, v1.2.3, R10, sha256 and RFC 3339 are plain prose.",
		"// sub-tasks are fine when no number follows.",
		`const label = "S12 and story 12 are fine in strings"`,
	)})
	out, code := acAuditDirs(t, root)
	acRequireClean(t, out, code)
	acRequireNoLocation(t, out, "near.go")
}

// TestAuditCommentsFlagsIDsOutsideLineComments: the pattern is matched against
// every line, so an ID in a string literal, a raw string, a block comment or a
// trailing comment after code is as much a defect as one in a "//" line.
func TestAuditCommentsFlagsIDsOutsideLineComments(t *testing.T) {
	t.Parallel()
	acRequireMake(t)
	cases := []struct {
		name  string
		lines []string
		line  int
	}{
		{"interpreted_string_literal", []string{"package fixture", "", "import \"errors\"", "", `var errBad = errors.New("refused by rule R9.9")`}, 5},
		{"raw_string_literal", []string{"package fixture", "", "const help = `", "usage: tool [flags]", "see story 123 for details", "`"}, 5},
		{"block_comment", []string{"package fixture", "", "/*", "  the cache rule from S123", "*/", "func f() {}"}, 4},
		{"trailing_comment_after_code", []string{"package fixture", "", "func f() int {", "\treturn 1 // keeps sub-task 4.2 happy", "}"}, 4},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			root := t.TempDir()
			acWriteTree(t, root, map[string]string{"where.go": acGoFile(c.lines...)})
			out, code := acAuditDirs(t, root)
			acRequireFailure(t, out, code)
			acRequireLocation(t, out, "where.go", c.line)
		})
	}
}

// TestAuditCommentsReportsEveryHit: every offending line is printed, across
// several files, nested directories and every directory listed.
func TestAuditCommentsReportsEveryHit(t *testing.T) {
	acRequireMake(t)
	first, second := t.TempDir(), t.TempDir()
	acWriteTree(t, first, map[string]string{
		"one.go": acGoFile("package fixture", "", "// from S123", "func f() {}", "", "", "// from story 123"),
	})
	acWriteTree(t, second, map[string]string{
		"deep/nested/two.go": acGoFile("package nested", "", "", `const s = "rule (D9) applies"`),
	})
	out, code := acAuditDirs(t, first, second)
	acRequireFailure(t, out, code)
	acRequireLocation(t, out, "one.go", 3)
	acRequireLocation(t, out, "one.go", 7)
	acRequireLocation(t, out, "two.go", 4)
}

// TestAuditCommentsSkipsTestFilesOnly: _test.go files and non-Go files are out
// of scope, but a production file is not exempted because its offending line
// mentions a test file, nor because its name merely contains "test".
func TestAuditCommentsSkipsTestFilesOnly(t *testing.T) {
	t.Parallel()
	acRequireMake(t)

	t.Run("ids_only_in_test_and_non_go_files", func(t *testing.T) {
		t.Parallel()
		root := t.TempDir()
		acWriteTree(t, root, map[string]string{
			"clean.go":          acGoFile("package fixture", "", "func f() {}"),
			"clean_test.go":     acGoFile("package fixture", "", "// pinned by S123 and story 123", `const name = "R9.9 (D9)"`),
			"notes.md":          "Context lives in S123 and sub-task 4.2.\n",
			"testdata/data.txt": "story 123\n",
		})
		long := acGoFile(acCat(acLines("package fixture", ""), acCommentBlock(30, ""), acLines("func g() {}"))...)
		acWriteTree(t, root, map[string]string{"long_test.go": long})
		out, code := acAuditDirs(t, root)
		acRequireClean(t, out, code)
	})

	t.Run("production_line_naming_a_test_file", func(t *testing.T) {
		t.Parallel()
		root := t.TempDir()
		acWriteTree(t, root, map[string]string{
			"mention.go": acGoFile("package fixture", "", "// rule S123 is pinned by mention_test.go", "func f() {}"),
		})
		out, code := acAuditDirs(t, root)
		acRequireFailure(t, out, code)
		acRequireLocation(t, out, "mention.go", 3)
	})

	t.Run("production_file_named_like_a_test", func(t *testing.T) {
		t.Parallel()
		root := t.TempDir()
		acWriteTree(t, root, map[string]string{
			"contest.go": acGoFile("package fixture", "", "// rule from story 123", "func f() {}"),
			"test.go":    acGoFile("package fixture", "", "// rule from S123", "func g() {}"),
		})
		out, code := acAuditDirs(t, root)
		acRequireFailure(t, out, code)
		acRequireLocation(t, out, "contest.go", 3)
		acRequireLocation(t, out, "test.go", 3)
	})
}

// TestAuditCommentsFlagsLongCommentBlocks covers the block-length rule: 20 or
// more consecutive comment lines that are not a package doc comment fail and
// are reported at the line where the block starts.
func TestAuditCommentsFlagsLongCommentBlocks(t *testing.T) {
	t.Parallel()
	acRequireMake(t)
	head := acLines("package fixture", "", "func f() {}", "") // a block after it starts on line 5
	cases := []struct {
		name  string
		lines []string
		start []int
	}{
		{"twenty_lines_above_a_func", acCat(head, acCommentBlock(20, ""), acLines("func g() {}")), []int{5}},
		{"indented_block_inside_a_func", acCat(acLines("package fixture", "", "func f() {"), acCommentBlock(20, "\t"), acLines("\t_ = 1", "}")), []int{4}},
		{"block_ending_at_end_of_file", acCat(head, acCommentBlock(20, "")), []int{5}},
		{"package_doc_separated_by_a_blank_line", acCat(acCommentBlock(25, ""), acLines("", "package fixture")), []int{1}},
		{"two_long_blocks_in_one_file", acCat(head, acCommentBlock(21, ""), acLines("func g() {}", ""), acCommentBlock(22, ""), acLines("func h() {}")), []int{5, 28}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			root := t.TempDir()
			acWriteTree(t, root, map[string]string{"block.go": acGoFile(c.lines...)})
			out, code := acAuditDirs(t, root)
			acRequireFailure(t, out, code)
			for _, s := range c.start {
				acRequireLocation(t, out, "block.go", s)
			}
		})
	}
}

// TestAuditCommentsAcceptsShortAndPackageDocBlocks is the other side of the
// block rule: what is not one block of 20 or more must pass.
func TestAuditCommentsAcceptsShortAndPackageDocBlocks(t *testing.T) {
	t.Parallel()
	acRequireMake(t)
	pkg := acLines("package fixture", "")
	trailing := make([]string, 0, 25)
	for i := 0; i < 25; i++ {
		trailing = append(trailing, "var v"+strconv.Itoa(i)+" = 1 // a trailing note")
	}
	cases := []struct {
		name  string
		lines []string
	}{
		{"nineteen_lines", acCat(pkg, acCommentBlock(19, ""), acLines("func g() {}"))},
		{"nineteen_then_blank_then_nineteen", acCat(pkg, acCommentBlock(19, ""), acLines(""), acCommentBlock(19, ""), acLines("func g() {}"))},
		{"nineteen_then_code_then_nineteen", acCat(pkg, acCommentBlock(19, ""), acLines("var x = 1"), acCommentBlock(19, ""), acLines("func g() {}"))},
		{"package_doc_of_twenty_five_lines", acCat(acCommentBlock(25, ""), acLines("package fixture", "", "func f() {}"))},
		{"package_doc_after_build_constraint", acCat(acLines("//go:build linux", ""), acCommentBlock(25, ""), acLines("package fixture"))},
		{"trailing_comments_after_code", acCat(pkg, trailing)},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			root := t.TempDir()
			acWriteTree(t, root, map[string]string{"short.go": acGoFile(c.lines...)})
			out, code := acAuditDirs(t, root)
			acRequireClean(t, out, code)
			acRequireNoLocation(t, out, "short.go")
		})
	}
}

// TestAuditCommentsCleanTreePrintsOneSummaryLine: a tree with neither defect
// exits 0 with one summary line, and the summary is not printed by a run that
// found a defect.
func TestAuditCommentsCleanTreePrintsOneSummaryLine(t *testing.T) {
	acRequireMake(t)
	summary := acCleanSummary(t)
	root := t.TempDir()
	acWriteTree(t, root, map[string]string{"bad.go": acGoFile("package fixture", "", "// from S123")})
	out, _ := acAuditDirs(t, root)
	if strings.Contains(out, summary) {
		t.Errorf("a run with a defect printed the clean summary %q\n%s", summary, out)
	}
}

// TestAuditCommentsFailsOnMissingDirectory: a directory that does not exist is
// an error, never a clean pass -- also when it is listed after a clean one.
func TestAuditCommentsFailsOnMissingDirectory(t *testing.T) {
	acRequireMake(t)
	clean := t.TempDir()
	acWriteTree(t, clean, map[string]string{"ok.go": acGoFile("package fixture", "", "func f() {}")})
	missing := filepath.Join(t.TempDir(), "does-not-exist")

	t.Run("only_the_missing_directory", func(t *testing.T) {
		out, code := acAuditDirs(t, missing)
		acRequireFailure(t, out, code)
	})
	t.Run("missing_directory_after_a_clean_one", func(t *testing.T) {
		out, code := acAuditDirs(t, clean, missing)
		acRequireFailure(t, out, code)
	})
}

// TestAuditCommentsDefaultScansOnlyCmdAndInternal runs the target with no
// AUDIT_COMMENTS_DIRS from a fixture repository root: defects under cmd/ and
// internal/ are reported, defects anywhere else (agent worktrees, the
// workflow directory, other top-level trees) are not.
func TestAuditCommentsDefaultScansOnlyCmdAndInternal(t *testing.T) {
	t.Parallel()
	acRequireMake(t)
	makefile := filepath.Join(auditRepoRoot(t), "Makefile")
	goMod := "module example.com/fixture\n\ngo 1.26.0\n\ntoolchain go1.26.8\n"
	outside := map[string]string{
		".claude/worktrees/agent/internal/wt.go": acGoFile("package wt", "", "// from S123"),
		".epic/stories/x/epicnote.go":            acGoFile("package x", "", "// from story 123"),
		"misc/tool/misc.go":                      acGoFile("package tool", "", "// from R9.9"),
		"rootfile.go":                            acGoFile("package fixture", "", "// from sub-task 4.2"),
	}

	t.Run("defects_outside_are_ignored", func(t *testing.T) {
		t.Parallel()
		root := t.TempDir()
		acWriteTree(t, root, outside)
		acWriteTree(t, root, map[string]string{
			"go.mod":              goMod,
			"cmd/app/main.go":     acGoFile("package main", "", "func main() {}"),
			"internal/lib/lib.go": acGoFile("package lib", "", "// Lib is fine.", "func Lib() {}"),
		})
		out, code := acRunMake(t, root, "-f", makefile, "-C", root, "audit-comments")
		acRequireClean(t, out, code)
		for _, base := range []string{"wt.go", "epicnote.go", "misc.go", "rootfile.go"} {
			acRequireNoLocation(t, out, base)
		}
	})

	t.Run("defects_inside_are_reported", func(t *testing.T) {
		t.Parallel()
		root := t.TempDir()
		acWriteTree(t, root, outside)
		acWriteTree(t, root, map[string]string{
			"go.mod":                   goMod,
			"cmd/app/main.go":          acGoFile("package main", "", "// from S123", "func main() {}"),
			"internal/deep/pkg/lib.go": acGoFile("package pkg", "", "", "// from story 123"),
		})
		out, code := acRunMake(t, root, "-f", makefile, "-C", root, "audit-comments")
		acRequireFailure(t, out, code)
		acRequireLocation(t, out, "main.go", 3)
		acRequireLocation(t, out, "lib.go", 4)
		for _, base := range []string{"wt.go", "epicnote.go", "misc.go", "rootfile.go"} {
			acRequireNoLocation(t, out, base)
		}
	})
}
