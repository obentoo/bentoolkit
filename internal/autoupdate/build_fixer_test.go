package autoupdate

// Authored for story 033, sub-task 11.1 — R8, R8.3, R8.4, R8.6.
//
// D7 IS A SECURITY PROPERTY, SO IT GETS AN ASSERTION RATHER THAN A COMMENT.
// `--add-dir` scopes the agent's FILE writes; it does not scope `Bash`. The
// manifest fixer may hold `Write` and `Bash(pkgdev *)` because the manifest step
// is its whole job (manifest_fixer.go:53). The build fixer may not: the staged
// tree is the only thing standing between a bad agent edit and an overlay that
// auto-commits and pushes, and a shell steps straight over it. The fixer only
// ever modifies an ebuild that already exists, so it needs no `Write` either.
//
// The assertions are deliberately written against the PACKAGE-LEVEL allow-list
// rather than against a constructed fixer. NewClaudeCodeFixer refuses to build
// without the `claude` CLI on PATH, so a constructor-based assertion would skip
// on every CI runner — and a security property that evaporates on the machine
// that enforces it is not enforced. Reaching the value directly also means the
// check cannot be satisfied by a code path that merely happens not to be taken.
//
// This file pins four names design.md fixes in prose but not in code:
// `buildFixAllowedTools`, `buildLogBudget`, `buildFixMaxAttempts`, and
// `buildFixInstruction(BuildFixRequest) string`.
//
// truncateMiddle comes from manifest_fixer.go:349 and is reused, never
// reimplemented.

import (
	"strings"
	"testing"
)

// TestBuildFixAllowedTools_HasNoShellAndNoWrite is D7, and it is the assertion
// this sub-task exists for. Written as two explicit scans rather than a set
// comparison so the failure message names the offending entry.
func TestBuildFixAllowedTools_HasNoShellAndNoWrite(t *testing.T) {
	for _, tool := range buildFixAllowedTools {
		if tool == "Write" {
			t.Error(`buildFixAllowedTools contains "Write"; the build fixer only ever modifies an ebuild that already exists`)
		}
		// Any form of Bash, scoped or not: `Bash`, `Bash(pkgdev *)`,
		// `Bash(ls *)`. --add-dir does not scope a shell, so a scoped pattern
		// is not a smaller version of the same permission — it is the same
		// hole with a narrower doorway.
		if tool == "Bash" || strings.HasPrefix(tool, "Bash(") {
			t.Errorf("buildFixAllowedTools contains %q; --add-dir scopes file writes but NOT Bash, "+
				"so any shell lets the agent write outside the staged tree and defeat the staging boundary (D7)", tool)
		}
	}
}

// TestBuildFixAllowedTools_IsExactlyReadAndEdit pins the whole list, not just
// the two forbidden entries. Without this, a future `WebFetch` or `Task` could
// be added and the negative assertions above would still pass.
func TestBuildFixAllowedTools_IsExactlyReadAndEdit(t *testing.T) {
	want := map[string]bool{"Read": true, "Edit": true}

	if len(buildFixAllowedTools) != len(want) {
		t.Errorf("buildFixAllowedTools = %v, want exactly %v — the narrowest allow-list in the codebase, by design",
			buildFixAllowedTools, []string{"Read", "Edit"})
	}
	seen := map[string]bool{}
	for _, tool := range buildFixAllowedTools {
		if !want[tool] {
			t.Errorf("buildFixAllowedTools contains %q, which is outside {Read, Edit}", tool)
		}
		if seen[tool] {
			t.Errorf("buildFixAllowedTools lists %q twice", tool)
		}
		seen[tool] = true
	}
	for tool := range want {
		if !seen[tool] {
			t.Errorf("buildFixAllowedTools is missing %q; the fixer cannot repair an ebuild it may not read or edit", tool)
		}
	}
}

// TestBuildFixAllowedTools_IsStrictlyNarrowerThanTheManifestFixer states the
// relationship the two lists must keep. The manifest fixer's list is defensible
// where it lives and indefensible here, and the two are easy to copy between.
func TestBuildFixAllowedTools_IsStrictlyNarrowerThanTheManifestFixer(t *testing.T) {
	manifest := map[string]bool{}
	for _, tool := range manifestFixAllowedTools {
		manifest[tool] = true
	}
	for _, tool := range buildFixAllowedTools {
		if !manifest[tool] {
			t.Errorf("buildFixAllowedTools grants %q, which the manifest fixer does not; the build fixer is the narrower of the two", tool)
		}
	}
	if len(buildFixAllowedTools) >= len(manifestFixAllowedTools) {
		t.Errorf("buildFixAllowedTools (%d entries) is not narrower than manifestFixAllowedTools (%d entries)",
			len(buildFixAllowedTools), len(manifestFixAllowedTools))
	}
}

// TestBuildFixInstruction_OversizedLogIsTruncatedInTheMiddle guards execve. A
// compile log dwarfs a manifest error, the instruction travels as a SINGLE argv
// element, and Linux's MAX_ARG_STRLEN is 128 KiB — over it, the child never
// starts and the failure reads as "argument list too long" rather than as
// anything about the bump.
func TestBuildFixInstruction_OversizedLogIsTruncatedInTheMiddle(t *testing.T) {
	const maxArgStrLen = 128 * 1024
	// A head and a tail that must both survive: the actionable part of a build
	// log lives at its two ends.
	head := "meson.build:1:0: ERROR: Unknown option: \"aalib\".\n"
	tail := "ERROR: media-plugins/gst-plugins-qt6-1.29.2::bentoo-staging failed (configure phase)\n"
	huge := head + strings.Repeat("compiling something irrelevant\n", 40000) + tail

	got := buildFixInstruction(BuildFixRequest{
		Package:    "media-plugins/gst-plugins-qt6",
		Version:    "1.29.2",
		Gate:       "configure",
		StagedDir:  "/tmp/staging/media-plugins/gst-plugins-qt6/1.29.2",
		EbuildPath: "/tmp/staging/media-plugins/gst-plugins-qt6/1.29.2/media-plugins/gst-plugins-qt6/gst-plugins-qt6-1.29.2.ebuild",
		BuildLog:   huge,
	})

	if len(got) >= maxArgStrLen {
		t.Errorf("the instruction is %d bytes, at or above MAX_ARG_STRLEN (%d); execve fails with E2BIG before the agent starts",
			len(got), maxArgStrLen)
	}
	if !strings.Contains(got, "aalib") {
		t.Error("the head of the build log was dropped; the first error is the one that says what broke")
	}
	if !strings.Contains(got, "failed (configure phase)") {
		t.Error("the tail of the build log was dropped; the last lines name the phase that failed")
	}
	if len(huge) <= len(got) {
		t.Errorf("nothing was elided: a %d-byte log produced a %d-byte instruction", len(huge), len(got))
	}
}

// TestBuildLogBudget_LeavesRoomForTheRestOfTheInstruction pins the budget as a
// value rather than as an emergent property of the truncation call, so a later
// edit to the prompt text cannot silently push the argv over the limit.
func TestBuildLogBudget_LeavesRoomForTheRestOfTheInstruction(t *testing.T) {
	const maxArgStrLen = 128 * 1024
	if buildLogBudget <= 0 {
		t.Fatalf("buildLogBudget = %d; a non-positive budget disables the guard", buildLogBudget)
	}
	if buildLogBudget >= maxArgStrLen {
		t.Errorf("buildLogBudget = %d, which alone reaches MAX_ARG_STRLEN (%d) with no room for the prompt around it",
			buildLogBudget, maxArgStrLen)
	}
	// truncateMiddle is the existing mechanism (manifest_fixer.go:349) and is
	// reused rather than reimplemented; this asserts the budget is honoured.
	got := truncateMiddle(strings.Repeat("x", buildLogBudget*4), buildLogBudget, "build log")
	if len(got) > buildLogBudget*2 {
		t.Errorf("truncateMiddle returned %d bytes for a budget of %d", len(got), buildLogBudget)
	}
}

// TestBuildFixMaxAttempts_IsBounded is R8.4. An unbounded repair loop against a
// bump that cannot be repaired spends money until somebody notices.
func TestBuildFixMaxAttempts_IsBounded(t *testing.T) {
	if buildFixMaxAttempts < 1 {
		t.Errorf("buildFixMaxAttempts = %d; the fixer would never run", buildFixMaxAttempts)
	}
	if buildFixMaxAttempts > 3 {
		t.Errorf("buildFixMaxAttempts = %d; each attempt is a full agent invocation plus an authoritative re-run of the gate",
			buildFixMaxAttempts)
	}
}
