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
	"context"
	"errors"
	"os/exec"
	"path/filepath"
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

// buildFixSpy records every invocation so "the fixer was never reached" is
// observable rather than inferred from the outcome.
type buildFixSpy struct {
	calls  []BuildFixRequest
	result BuildFixResult
	err    error
}

func (s *buildFixSpy) FixBuild(_ context.Context, req BuildFixRequest) (BuildFixResult, error) {
	s.calls = append(s.calls, req)
	return s.result, s.err
}

// buildRun is one scripted invocation of the build child: what it printed and
// whether it failed. A slice of them is a run and its re-run, in order.
type buildRun struct {
	output string
	err    error
}

// buildFixFixture wires an applier whose build child is scripted. Each
// runAttached call consumes the next buildRun; the argv of every invocation is
// recorded so "the SAME phase was re-run" is checkable.
type buildFixHarness struct {
	applier *Applier
	pkg     string
	spy     *buildFixSpy
	argv    [][]string
	runs    int
}

func buildFixFixture(t *testing.T, spy *buildFixSpy, script ...buildRun) *buildFixHarness {
	t.Helper()
	tmp := t.TempDir()
	overlayDir := filepath.Join(tmp, "overlay")
	configDir := filepath.Join(tmp, "config")
	const pkg = "media-plugins/gst-plugins-qt6"

	createTestEbuildFile(t, overlayDir, pkg, "1.28.6")

	pending, err := NewPendingList(configDir)
	if err != nil {
		t.Fatalf("creating pending list: %v", err)
	}
	pending.Add(PendingUpdate{
		Package:        pkg,
		CurrentVersion: "1.28.6",
		NewVersion:     "1.29.2",
		Status:         StatusPending,
	})

	h := &buildFixHarness{pkg: pkg, spy: spy}

	seam := func(ctx context.Context, name string, arg ...string) *exec.Cmd {
		// Only the build child is scripted; `pkgdev manifest` and friends
		// succeed so the run reaches the gates.
		if name == "ebuild" || containsArg(arg, "ebuild") {
			h.argv = append(h.argv, append([]string{name}, arg...))
		}
		return exec.CommandContext(ctx, "true")
	}
	attached := func(cmd *exec.Cmd) ([]byte, error) {
		idx := h.runs
		h.runs++
		if idx >= len(script) {
			t.Fatalf("the build child ran %d times; the script has %d entries", h.runs, len(script))
		}
		return []byte(script[idx].output), script[idx].err
	}

	applier, err := NewApplier(overlayDir, configDir,
		WithApplierPendingList(pending),
		WithExecCommand(seam),
		WithApplierRunAttached(attached),
		WithConfirmFunc(func(string) bool { return true }),
		WithApplierStagingRoot(filepath.Join(tmp, "staging")),
		WithApplierBuildFixer(spy),
		WithApplierIsolationProbe(func() (bool, string) { return true, "" }),
	)
	if err != nil {
		t.Fatalf("creating applier: %v", err)
	}
	h.applier = applier
	return h
}

// The three build logs this sub-task classifies. They reuse the phase markers
// design M-A measured; the strings live here rather than in the validate package
// because this file asserts about ATTRIBUTION, not about parsing.
const (
	preparedThenConfigureFails = ">>> Source unpacked in /var/tmp/portage/work\n" +
		">>> Preparing source in /var/tmp/portage/work/src ...\n" +
		">>> Source prepared.\n" +
		">>> Configuring source in /var/tmp/portage/work/src ...\n" +
		"meson.build:1:0: ERROR: Unknown option: \"aalib\".\n" +
		"ERROR: media-plugins/gst-plugins-qt6-1.29.2::bentoo-staging failed (configure phase):\n"

	failsDuringUnpack = ">>> Unpacking source...\n" +
		">>> Unpacking gst-plugins-good-1.29.2.tar.xz to /var/tmp/portage/work\n" +
		"tar: Unexpected EOF in archive\n" +
		"ERROR: media-plugins/gst-plugins-qt6-1.29.2::bentoo-staging failed (unpack phase):\n"

	failsOutOfDisk = ">>> Source unpacked in /var/tmp/portage/work\n" +
		">>> Preparing source in /var/tmp/portage/work/src ...\n" +
		">>> Source prepared.\n" +
		">>> Configuring source in /var/tmp/portage/work/src ...\n" +
		"cc: error: unable to write output: No space left on device\n" +
		"ERROR: media-plugins/gst-plugins-qt6-1.29.2::bentoo-staging failed (configure phase):\n"

	cleanCompile = ">>> Source unpacked in /var/tmp/portage/work\n" +
		">>> Preparing source in /var/tmp/portage/work/src ...\n" +
		">>> Source prepared.\n" +
		">>> Configuring source in /var/tmp/portage/work/src ...\n" +
		">>> Source configured.\n" +
		">>> Compiling source in /var/tmp/portage/work/src ...\n" +
		">>> Source compiled.\n"
)

// TestBuildFixer_OutOfDiskIsReportedAndNeverReachesTheModel is R8.5 and the
// reason environmentVerdict is not reused: it would call this repairable, and
// the agent's only available repair is to rewrite an ebuild that is fine.
func TestBuildFixer_OutOfDiskIsReportedAndNeverReachesTheModel(t *testing.T) {
	spy := &buildFixSpy{result: BuildFixResult{Summary: "should never be called"}}
	h := buildFixFixture(t, spy, buildRun{output: failsOutOfDisk, err: errors.New("exit status 1")})

	result, _ := h.applier.Apply(h.pkg, true)

	if len(spy.calls) != 0 {
		t.Errorf("the fixer was invoked %d time(s) for an out-of-disk failure; a machine fault is never handed to a model (R8.5)", len(spy.calls))
	}
	if result.Success {
		t.Fatal("an out-of-disk build reported success")
	}
	if result.Error == nil {
		t.Fatal("an out-of-disk build carried no error")
	}
	// R8.5 asks that the failure be REPORTED, and reported as the machine's.
	msg := result.Error.Error()
	if !strings.Contains(strings.ToLower(msg), "space") {
		t.Errorf("the error %q does not say what the machine ran out of; the operator cannot act on it", msg)
	}
}

// TestBuildFixer_UnpackFailureNeverReachesTheModel is design D6's phase rule.
// A failure before `>>> Source prepared.` happened in setup or unpack, which is
// a host or distfile fault — story 030's recorded lesson is to mark the phase
// rather than enumerate causes.
func TestBuildFixer_UnpackFailureNeverReachesTheModel(t *testing.T) {
	spy := &buildFixSpy{}
	h := buildFixFixture(t, spy, buildRun{output: failsDuringUnpack, err: errors.New("exit status 1")})

	result, _ := h.applier.Apply(h.pkg, true)

	if len(spy.calls) != 0 {
		t.Errorf("the fixer was invoked for a failure that happened before %q; nothing about the ebuild had been exercised yet",
			">>> Source prepared.")
	}
	if result.Success {
		t.Error("a build that failed during unpack reported success")
	}
}

// TestBuildFixer_ConfigureFailureIsHandedToTheModel is R8.1 — the positive
// case, without which the three refusals above could be satisfied by never
// invoking the fixer at all.
func TestBuildFixer_ConfigureFailureIsHandedToTheModel(t *testing.T) {
	spy := &buildFixSpy{result: BuildFixResult{Summary: "dropped -Daalib= and -Dlibcaca="}}
	h := buildFixFixture(t, spy,
		buildRun{output: preparedThenConfigureFails, err: errors.New("exit status 1")},
		buildRun{output: cleanCompile}, // the authoritative re-run succeeds
	)

	result, _ := h.applier.Apply(h.pkg, true)

	if len(spy.calls) != 1 {
		t.Fatalf("the fixer was invoked %d time(s) for a configure failure, want exactly 1 (R8.1)", len(spy.calls))
	}
	req := spy.calls[0]
	if req.Package != h.pkg || req.Version != "1.29.2" {
		t.Errorf("the fix request names %s-%s, want %s-1.29.2", req.Package, req.Version, h.pkg)
	}
	if !strings.Contains(req.BuildLog, "aalib") {
		t.Error("the fix request carries no build log naming the failure; the agent would be guessing")
	}
	// R8.3: everything the fixer may touch is inside the staged tree.
	if req.StagedDir == "" {
		t.Error("the fix request carries no staged directory to scope the agent to")
	}
	if req.EbuildPath != "" && !strings.HasPrefix(filepath.Clean(req.EbuildPath), filepath.Clean(req.StagedDir)) {
		t.Errorf("the ebuild handed to the fixer (%q) is outside the staged tree (%q)", req.EbuildPath, req.StagedDir)
	}
	if !result.Success {
		t.Errorf("the re-run succeeded and the apply still failed: %v", result.Error)
	}
}

// TestBuildFixer_TheRerunDecidesNotTheAgentsSelfReport is R8.2, and it is the
// invariant the manifest fixer already proves. The agent says it repaired the
// ebuild; the re-run says otherwise; FAILED is the answer.
func TestBuildFixer_TheRerunDecidesNotTheAgentsSelfReport(t *testing.T) {
	spy := &buildFixSpy{result: BuildFixResult{Summary: "removed the unknown meson options"}}
	h := buildFixFixture(t, spy,
		buildRun{output: preparedThenConfigureFails, err: errors.New("exit status 1")},
		buildRun{output: preparedThenConfigureFails, err: errors.New("exit status 1")}, // still broken
	)

	result, _ := h.applier.Apply(h.pkg, true)

	if len(spy.calls) == 0 {
		t.Fatal("the fixer was never invoked, so this case proves nothing about its self-report")
	}
	if result.Success {
		t.Fatal("the apply succeeded on the agent's word alone; the re-run is the verdict, never the model (R8.2)")
	}
	if result.Error == nil {
		t.Error("a bump whose re-run still failed carries no error")
	}
	// The claim must not survive into the report either: a "fixed" flag on a
	// bump that is still broken is worse than no flag.
	if result.Fixed {
		t.Error("result.Fixed is true although the re-run still failed")
	}
}

// TestBuildFixer_TheSameGateIsRerun pins which phase the authoritative re-run
// runs. Re-running a SHALLOWER phase would let a configure failure be cleared by
// a successful prepare — a green that proves nothing about the failure it claims
// to have repaired.
func TestBuildFixer_TheSameGateIsRerun(t *testing.T) {
	spy := &buildFixSpy{result: BuildFixResult{Summary: "edited the staged ebuild"}}
	h := buildFixFixture(t, spy,
		buildRun{output: preparedThenConfigureFails, err: errors.New("exit status 1")},
		buildRun{output: cleanCompile},
	)

	if _, err := h.applier.Apply(h.pkg, true); err != nil {
		t.Fatalf("Apply: %v", err)
	}

	if h.runs != 2 {
		t.Fatalf("the build child ran %d time(s); one failing run plus one authoritative re-run is 2", h.runs)
	}
	if len(h.argv) < 2 {
		t.Fatalf("only %d build invocation(s) were recorded", len(h.argv))
	}
	first, second := strings.Join(h.argv[0], " "), strings.Join(h.argv[1], " ")
	if phaseOf(first) != phaseOf(second) {
		t.Errorf("the re-run ran phase %q after a failure in phase %q; the same phase must decide (R8.2)\nfirst:  %s\nsecond: %s",
			phaseOf(second), phaseOf(first), first, second)
	}
	if spy.calls[0].Gate == "" {
		t.Error("the fix request does not name the gate that failed, so the re-run cannot be matched to it")
	}
}

// phaseOf returns the deepest ebuild phase named in an argv string. It is a test
// helper and deliberately dumb: the phases are a closed set.
func phaseOf(argv string) string {
	for _, phase := range []string{"compile", "configure", "prepare", "unpack"} {
		if strings.Contains(argv, phase) {
			return phase
		}
	}
	return ""
}

// TestBuildFixer_NoFixerWiredKeepsTheFailFastBehaviour is the Unchanged
// Behaviour guard: without --llm the gates still decide, and a failed build is
// still a failed apply. A story that adds a repair path must not change what
// happens when nobody asked for one.
func TestBuildFixer_NoFixerWiredKeepsTheFailFastBehaviour(t *testing.T) {
	spy := &buildFixSpy{}
	h := buildFixFixture(t, spy, buildRun{output: preparedThenConfigureFails, err: errors.New("exit status 1")})
	// Re-wire without the fixer: a nil BuildFixer must be ignored exactly as
	// WithApplierFixer ignores one (applier.go:365).
	WithApplierBuildFixer(nil)(h.applier)

	result, _ := h.applier.Apply(h.pkg, true)

	if result.Success {
		t.Error("a failing configure reported success with no fixer wired")
	}
	if h.runs != 1 {
		t.Errorf("the build child ran %d time(s) with no fixer wired; there is nothing to re-run", h.runs)
	}
}
