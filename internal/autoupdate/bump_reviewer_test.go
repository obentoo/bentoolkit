package autoupdate

// Authored for story 033, sub-task 10.1 — R7, R7.3, R7.4, R7.6, R7.7.
//
// A READING THAT CANNOT TOUCH ANYTHING. The reviewer's whole value is that it
// may raise scrutiny; its whole safety is that it may do nothing else. R7.4 says
// it gets no capability that can modify a file, and that is asserted against the
// PACKAGE-LEVEL tool list rather than a constructed reviewer — the same reason
// 11.1 does it for the fixer: NewClaudeCode* refuses to build without the
// `claude` CLI, so a constructor-based check would skip on every CI runner, and
// a security property that evaporates on the machine enforcing it is not
// enforced.
//
// R7.6 IS STRUCTURAL, NOT REMEMBERED. `Report.ExitCode` counts findings of
// severity `error` and nothing else. So if the reviewer can only ever emit
// `info` and `warning`, then "a reviewer's opinion never decides whether a bump
// passed" is a property of the type rather than a rule somebody has to keep in
// mind while editing the prompt. This is story 025's R4.5 — a finding never
// changes a verdict — restated for a model. The case below drives the reviewer's
// worst-case output through the report and asserts the severities that come out.
//
// THE REVIEWER READS TWO ARCHIVES, NOT THE NETWORK (design D8). The protocol
// listed G1.5 as needing network; it does not. Both versions' build-declaration
// files come out of the two distfiles with the selective `tar -xO` extraction
// story 031 already implements, and the diff is computed locally. Its INPUT is a
// prepared diff, not a capability to go and find one — which is what keeps the
// no-network property true for the reviewer as well.
//
// R7.7 SAYS "CANNOT RUN", NOT "HAS NO ARCHIVE". The requirement was widened in
// review for a concrete reason: handling only the missing-archive case leaves a
// provider failure with no stated outcome at all — the run would carry on and
// the report would be silent about the fact that nothing was reviewed — and it
// leaves `validate.timeout` with no consumer anywhere in the story. Each cause
// is named in the skip, because "the reviewer did not run" and "the reviewer
// timed out after an hour" ask different things of the operator.
//
// This file pins `bumpReviewAllowedTools`, the `BumpReviewer` interface,
// `BumpReviewRequest{Package, OldVersion, NewVersion, BuildFileDiff, OldArchive,
// NewArchive}`, `BumpReviewReport{Risks, ProposedDepth, Reason, Skipped,
// SkipReason}` and `WithBumpReviewerTimeout`, which is where
// `validate.timeout` (sub-task 9.1) is consumed.
//
// ONE UNCERTAIN HELPER: `stubLookPathFound` already exists in this package's
// tests and is used here to make `claudeAvailable()` answer true without the CLI
// on PATH. If its signature differs from `stubLookPathFound(t)`, adjust that one
// call — the assertions around it do not change.

import (
	"context"
	"go/parser"
	"go/token"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/obentoo/bentoolkit/internal/autoupdate/validate"
)

// TestBumpReviewAllowedTools_ContainsNothingThatWrites is R7.4. Read-only is not
// a description of intent here, it is the enumeration.
func TestBumpReviewAllowedTools_ContainsNothingThatWrites(t *testing.T) {
	for _, tool := range bumpReviewAllowedTools {
		switch {
		case tool == "Write":
			t.Error(`bumpReviewAllowedTools contains "Write"; the reviewer reads a prepared diff and reports on it (R7.4)`)
		case tool == "Edit":
			t.Error(`bumpReviewAllowedTools contains "Edit"; editing is the FIXER's capability, and the two are separate so that only one of them can change a file`)
		case tool == "Bash" || strings.HasPrefix(tool, "Bash("):
			t.Errorf("bumpReviewAllowedTools contains %q; a shell writes files, and no --add-dir scopes it", tool)
		case tool == "WebFetch" || tool == "WebSearch":
			t.Errorf("bumpReviewAllowedTools contains %q; the reviewer's input is a LOCALLY computed diff (D8), "+
				"and a fetch would put the package's no-network property back in question", tool)
		}
	}
}

// TestBumpReviewAllowedTools_IsNarrowerThanTheBuildFixer states the ordering the
// three agentic capabilities keep: the manifest fixer is the widest, the build
// fixer holds Read and Edit, and the reviewer holds strictly less than that.
func TestBumpReviewAllowedTools_IsNarrowerThanTheBuildFixer(t *testing.T) {
	fixer := map[string]bool{}
	for _, tool := range buildFixAllowedTools {
		fixer[tool] = true
	}
	for _, tool := range bumpReviewAllowedTools {
		if !fixer[tool] {
			t.Errorf("the reviewer holds %q, which the build fixer does not; the reviewer is the narrowest capability in the codebase", tool)
		}
	}
	if len(bumpReviewAllowedTools) >= len(buildFixAllowedTools) {
		t.Errorf("bumpReviewAllowedTools (%d) is not narrower than buildFixAllowedTools (%d)",
			len(bumpReviewAllowedTools), len(buildFixAllowedTools))
	}
}

// reviewSeam scripts the `claude` child: it prints the given envelope body and
// exits 0, and records the argv and the child environment.
type reviewSeam struct {
	spawns int
	args   []string
	cmd    *exec.Cmd
}

func newReviewSeam(spy *reviewSeam, stdout string) func(ctx context.Context, name string, arg ...string) *exec.Cmd {
	return func(ctx context.Context, name string, arg ...string) *exec.Cmd {
		spy.spawns++
		spy.args = arg
		cmd := exec.CommandContext(ctx, "printf", "%s", stdout)
		spy.cmd = cmd
		return cmd
	}
}

// TestBumpReviewer_MissingPreviousArchiveIsSkippedNamingIt is R7.7. The reviewer
// diffs two versions, so with only one archive on disk it has nothing to read —
// and a reviewer that could not run must not look like a reviewer that found
// nothing. The depth is unchanged, so the run continues at the policy depth.
func TestBumpReviewer_MissingPreviousArchiveIsSkippedNamingIt(t *testing.T) {
	stubLookPathFound(t)
	spy := &reviewSeam{}
	reviewer, err := NewClaudeCodeBumpReviewer(LLMConfig{Provider: "claude-code"},
		WithBumpReviewerExecCommand(newReviewSeam(spy, "{}")))
	if err != nil {
		t.Fatalf("constructing the reviewer: %v", err)
	}

	const missing = "gst-plugins-good-1.28.6.tar.xz"
	got, err := reviewer.ReviewBump(context.Background(), BumpReviewRequest{
		Package:    "media-plugins/gst-plugins-qt6",
		OldVersion: "1.28.6",
		NewVersion: "1.29.2",
		OldArchive: "", // not on disk
		NewArchive: "/var/cache/distfiles/gst-plugins-good-1.29.2.tar.xz",
	})
	if err != nil {
		t.Fatalf("ReviewBump: %v — a missing archive is a reported outcome, not a failed run (R7.7)", err)
	}

	if !got.Skipped {
		t.Fatal("the reviewer ran without the previous version's archive; it has nothing to diff against")
	}
	if got.SkipReason == "" {
		t.Fatal("the skip carries no reason; a skip nobody can read is a pass")
	}
	if !strings.Contains(got.SkipReason, "1.28.6") && !strings.Contains(got.SkipReason, missing) {
		t.Errorf("the skip reason %q does not name the archive that was absent", got.SkipReason)
	}
	if got.ProposedDepth != nil {
		t.Errorf("a skipped reviewer proposed a depth (%v); the run continues at the depth policy selected (R7.7)", *got.ProposedDepth)
	}
	if spy.spawns != 0 {
		t.Errorf("the agent was invoked %d time(s) with nothing to review", spy.spawns)
	}
}

// TestBumpReviewer_RisksAreNeverEmittedAtSeverityError is R7.6, driven through
// the worst case the reviewer can produce: an agent that reports a breaking
// change in the strongest terms it has. ExitCode counts `error` alone, so as
// long as none comes out, the model cannot fail a bump even if it tries to.
func TestBumpReviewer_RisksAreNeverEmittedAtSeverityError(t *testing.T) {
	stubLookPathFound(t)
	// The agent's own words, at maximum alarm — including the literal word
	// "error", which must not become a SEVERITY.
	envelope := `{"result":"{\"risks\":[` +
		`{\"severity\":\"error\",\"detail\":\"upstream removed the plugin registration API; this bump WILL fail to build\"},` +
		`{\"severity\":\"critical\",\"detail\":\"meson.build dropped two options the ebuild passes\"}` +
		`],\"proposed_depth\":\"compile\",\"reason\":\"the build files moved substantially\"}"}`

	spy := &reviewSeam{}
	reviewer, err := NewClaudeCodeBumpReviewer(LLMConfig{Provider: "claude-code"},
		WithBumpReviewerExecCommand(newReviewSeam(spy, envelope)))
	if err != nil {
		t.Fatalf("constructing the reviewer: %v", err)
	}

	got, err := reviewer.ReviewBump(context.Background(), BumpReviewRequest{
		Package:       "media-plugins/gst-plugins-qt6",
		OldVersion:    "1.28.6",
		NewVersion:    "1.29.2",
		BuildFileDiff: "-option('aalib')\n-option('libcaca')\n",
	})
	if err != nil {
		t.Fatalf("ReviewBump: %v", err)
	}

	if len(got.Risks) == 0 {
		t.Fatal("the reviewer reported no risks from a report naming two; R7.3 requires the risks it names to be reported")
	}
	for _, risk := range got.Risks {
		if risk.Severity == validate.SeverityError {
			t.Errorf("a reviewer risk was emitted at severity error (%q); ExitCode counts error findings, "+
				"so this lets a model's opinion decide whether a bump passed (R7.6)", risk.Detail)
		}
		if risk.Severity != validate.SeverityInfo && risk.Severity != validate.SeverityWarning {
			t.Errorf("risk %q carries severity %q; the reviewer emits info or warning and nothing else", risk.Detail, risk.Severity)
		}
		if risk.Gate == "" {
			t.Errorf("risk %q carries no gate, so the report cannot attribute it", risk.Detail)
		}
	}

	// R7.3: what it named is reported, verbatim enough to act on.
	var joined []string
	for _, r := range got.Risks {
		joined = append(joined, r.Detail)
	}
	if !strings.Contains(strings.Join(joined, " | "), "registration API") {
		t.Errorf("the reviewer's risks were not carried through: %v", joined)
	}
}

// TestBumpReviewer_ReadsThePreparedDiffAndReachesNoNetwork is D8. The input is a
// diff computed locally from two archives already on disk; the reviewer is given
// text, not a capability to go and find text.
func TestBumpReviewer_ReadsThePreparedDiffAndReachesNoNetwork(t *testing.T) {
	stubLookPathFound(t)
	spy := &reviewSeam{}
	reviewer, err := NewClaudeCodeBumpReviewer(LLMConfig{Provider: "claude-code"},
		WithBumpReviewerExecCommand(newReviewSeam(spy, `{"result":"{\"risks\":[]}"}`)))
	if err != nil {
		t.Fatalf("constructing the reviewer: %v", err)
	}

	const diff = "-option('aalib')\n-option('libcaca')\n+option('vulkan')\n"
	if _, err := reviewer.ReviewBump(context.Background(), BumpReviewRequest{
		Package:       "media-plugins/gst-plugins-qt6",
		OldVersion:    "1.28.6",
		NewVersion:    "1.29.2",
		BuildFileDiff: diff,
	}); err != nil {
		t.Fatalf("ReviewBump: %v", err)
	}

	if spy.spawns != 1 {
		t.Fatalf("spawned %d agents, want exactly 1", spy.spawns)
	}
	argv := strings.Join(spy.args, " ")
	if !strings.Contains(argv, "aalib") {
		t.Error("the prepared diff did not reach the agent's instruction; its input is the diff, not a URL")
	}
	// The tool list travels on the argv, so the security property is also
	// observable at the call site rather than only in the package variable.
	if !strings.Contains(argv, "--allowedTools") {
		t.Errorf("the invocation sets no --allowedTools: %s", argv)
	}
	for _, forbidden := range []string{"Write", "Edit", "Bash", "WebFetch"} {
		if strings.Contains(argv, forbidden) {
			t.Errorf("the invocation grants %q: %s", forbidden, argv)
		}
	}
	// --add-dir would give the agent a directory to work in. It has nothing to
	// work on: its input is already in the instruction.
	if strings.Contains(argv, "--add-dir") {
		t.Errorf("the reviewer is scoped to a directory with --add-dir; it reads a prepared diff and writes nothing: %s", argv)
	}
}

// TestBumpReviewer_ChildEnvironmentFollowsTheStrippingDiscipline pins that the
// reviewer builds its child environment through childEnv rather than handing the
// agent os.Environ(). In non-bare mode the auth variables are stripped, which is
// the discipline every other claude-code capability in this package already
// keeps.
func TestBumpReviewer_ChildEnvironmentFollowsTheStrippingDiscipline(t *testing.T) {
	stubLookPathFound(t)
	t.Setenv("ANTHROPIC_API_KEY", "sk-must-not-reach-the-child")

	spy := &reviewSeam{}
	reviewer, err := NewClaudeCodeBumpReviewer(
		LLMConfig{Provider: "claude-code", Bare: "false"},
		WithBumpReviewerExecCommand(newReviewSeam(spy, `{"result":"{\"risks\":[]}"}`)))
	if err != nil {
		t.Fatalf("constructing the reviewer: %v", err)
	}

	if _, err := reviewer.ReviewBump(context.Background(), BumpReviewRequest{
		Package:       "media-plugins/gst-plugins-qt6",
		OldVersion:    "1.28.6",
		NewVersion:    "1.29.2",
		BuildFileDiff: "-option('aalib')\n",
	}); err != nil {
		t.Fatalf("ReviewBump: %v", err)
	}

	if spy.cmd == nil {
		t.Fatal("no command was captured")
	}
	if spy.cmd.Env == nil {
		t.Fatal("cmd.Env was never set; the agent inherits the operator's whole environment, auth variables included")
	}
	for _, kv := range spy.cmd.Env {
		if strings.HasPrefix(kv, "ANTHROPIC_API_KEY=") {
			t.Errorf("the child carries %q in non-bare mode; childEnv strips it and the reviewer must go through childEnv", strings.SplitN(kv, "=", 2)[0])
		}
	}
	// The secret must never appear in argv either — argv is world-readable in
	// /proc on this platform.
	if strings.Contains(strings.Join(spy.args, " "), "sk-must-not-reach-the-child") {
		t.Error("the API key value appears in the agent's argv")
	}
}

// TestBumpReviewer_SourceReachesNoNetworkPackage is the structural half of D8,
// mirroring validate's own import assertion: behaviour cannot prove a negative,
// so the reviewer's source is read for anything that can open a socket.
//
// It is scoped to this one file rather than the whole package, because
// internal/autoupdate legitimately speaks HTTP everywhere else — the reviewer is
// the exception that has to stay one.
func TestBumpReviewer_SourceReachesNoNetworkPackage(t *testing.T) {
	const source = "bump_reviewer.go"
	if _, err := os.Stat(source); err != nil {
		t.Fatalf("%s is missing: %v", source, err)
	}

	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, source, nil, parser.ImportsOnly)
	if err != nil {
		t.Fatalf("parsing %s: %v", source, err)
	}

	banned := map[string]bool{
		`"net"`:        true,
		`"net/http"`:   true,
		`"crypto/tls"`: true,
	}
	for _, imp := range file.Imports {
		if banned[imp.Path.Value] {
			t.Errorf("%s imports %s; the reviewer's input is a locally computed diff and it reaches no network (D8)", source, imp.Path.Value)
		}
	}
}

// TestBumpReviewer_AProviderErrorIsSkippedNamingIt is R7.7 for the cause the
// original requirement did not mention. The agent could not be run — the CLI is
// gone, the budget cap was reached, the process died — and the run continues at
// the depth policy selected, with the report saying so.
//
// SKIPPED and not an error: a reviewer is advisory (R7.6), so its own failure
// must not fail a bump that the deterministic gates would have passed.
func TestBumpReviewer_AProviderErrorIsSkippedNamingIt(t *testing.T) {
	stubLookPathFound(t)
	spy := &reviewSeam{}
	reviewer, err := NewClaudeCodeBumpReviewer(LLMConfig{Provider: "claude-code"},
		WithBumpReviewerExecCommand(func(ctx context.Context, _ string, arg ...string) *exec.Cmd {
			spy.spawns++
			spy.args = arg
			// Exits non-zero having printed nothing, the way an agent that
			// could not start does.
			return exec.CommandContext(ctx, "false")
		}))
	if err != nil {
		t.Fatalf("constructing the reviewer: %v", err)
	}

	got, revErr := reviewer.ReviewBump(context.Background(), BumpReviewRequest{
		Package:       "media-plugins/gst-plugins-qt6",
		OldVersion:    "1.28.6",
		NewVersion:    "1.29.2",
		BuildFileDiff: "-option('aalib')\n",
	})
	if revErr != nil {
		t.Fatalf("ReviewBump returned an error (%v); a reviewer that could not run is a REPORTED outcome — it is advisory, "+
			"and failing the bump on its own failure would let a model decide after all (R7.6)", revErr)
	}

	if !got.Skipped {
		t.Fatal("a provider failure did not produce a SKIPPED review; the report would then be silent about the fact that nothing was reviewed")
	}
	if got.SkipReason == "" {
		t.Fatal("the skip carries no reason")
	}
	if strings.Contains(strings.ToLower(got.SkipReason), "archive") {
		t.Errorf("the skip reason %q blames a missing archive for a provider failure; each cause is named as itself, "+
			"because they ask different things of the operator", got.SkipReason)
	}
	if len(got.Risks) != 0 {
		t.Errorf("a reviewer that never ran produced %d risk(s)", len(got.Risks))
	}
	if got.ProposedDepth != nil {
		t.Errorf("a skipped reviewer proposed depth %v; the run continues at the policy floor (R7.7)", *got.ProposedDepth)
	}
}

// TestBumpReviewer_AnElapsedTimeoutIsSkippedNamingIt is R7.7 for the cause that
// gives `validate.timeout` (9.1) its only consumer on the reviewer side.
//
// An agent that hangs is worse than one that fails: an unattended sweep over
// forty packages stops on the first, and nothing in the output says why. The
// budget is bounded and its expiry is reported as itself.
func TestBumpReviewer_AnElapsedTimeoutIsSkippedNamingIt(t *testing.T) {
	stubLookPathFound(t)
	spy := &reviewSeam{}
	reviewer, err := NewClaudeCodeBumpReviewer(LLMConfig{Provider: "claude-code"},
		WithBumpReviewerTimeout(50*time.Millisecond),
		WithBumpReviewerExecCommand(func(ctx context.Context, _ string, arg ...string) *exec.Cmd {
			spy.spawns++
			spy.args = arg
			// Outlives the budget. Bound to ctx, so the timeout really is what
			// ends it rather than the test waiting the whole time.
			return exec.CommandContext(ctx, "sleep", "30")
		}))
	if err != nil {
		t.Fatalf("constructing the reviewer: %v", err)
	}

	start := time.Now()
	got, revErr := reviewer.ReviewBump(context.Background(), BumpReviewRequest{
		Package:       "media-plugins/gst-plugins-qt6",
		OldVersion:    "1.28.6",
		NewVersion:    "1.29.2",
		BuildFileDiff: "-option('aalib')\n",
	})
	elapsed := time.Since(start)

	if revErr != nil {
		t.Fatalf("ReviewBump returned an error on timeout (%v); an advisory capability that ran out of time must not fail the bump", revErr)
	}
	if elapsed > 10*time.Second {
		t.Errorf("ReviewBump took %v against a 50ms budget; the timeout is not bounding the child", elapsed)
	}
	if !got.Skipped {
		t.Fatal("an elapsed timeout did not produce a SKIPPED review")
	}
	if !strings.Contains(strings.ToLower(got.SkipReason), "time") {
		t.Errorf("the skip reason %q does not say the budget elapsed; 'the reviewer did not run' and 'the reviewer ran out of "+
			"time' ask different things of the operator — one is a broken host, the other is a budget to raise", got.SkipReason)
	}
	if got.ProposedDepth != nil {
		t.Errorf("a timed-out reviewer proposed depth %v; the floor stands (R7.7)", *got.ProposedDepth)
	}
}

// TestBumpReviewer_EverySkipCauseIsDistinguishable sweeps the three causes side
// by side. Each is a different next action — fetch the archive, fix the host,
// raise the budget — and a shared "the reviewer was skipped" string would make
// all three unactionable at once.
func TestBumpReviewer_EverySkipCauseIsDistinguishable(t *testing.T) {
	stubLookPathFound(t)

	seams := map[string]func(ctx context.Context, name string, arg ...string) *exec.Cmd{
		"provider error": func(ctx context.Context, _ string, _ ...string) *exec.Cmd {
			return exec.CommandContext(ctx, "false")
		},
		"timeout": func(ctx context.Context, _ string, _ ...string) *exec.Cmd {
			return exec.CommandContext(ctx, "sleep", "30")
		},
	}

	reasons := map[string]string{}
	for name, seam := range seams {
		reviewer, err := NewClaudeCodeBumpReviewer(LLMConfig{Provider: "claude-code"},
			WithBumpReviewerTimeout(50*time.Millisecond),
			WithBumpReviewerExecCommand(seam))
		if err != nil {
			t.Fatalf("constructing the reviewer for %s: %v", name, err)
		}
		got, err := reviewer.ReviewBump(context.Background(), BumpReviewRequest{
			Package: "media-plugins/gst-plugins-qt6", OldVersion: "1.28.6", NewVersion: "1.29.2",
			BuildFileDiff: "-option('aalib')\n",
		})
		if err != nil {
			t.Fatalf("%s: ReviewBump returned %v", name, err)
		}
		if !got.Skipped {
			t.Fatalf("%s did not produce a SKIPPED review", name)
		}
		reasons[name] = got.SkipReason
	}

	// The missing-archive cause, for comparison.
	reviewer, err := NewClaudeCodeBumpReviewer(LLMConfig{Provider: "claude-code"},
		WithBumpReviewerExecCommand(newReviewSeam(&reviewSeam{}, "{}")))
	if err != nil {
		t.Fatalf("constructing the reviewer: %v", err)
	}
	missing, err := reviewer.ReviewBump(context.Background(), BumpReviewRequest{
		Package: "media-plugins/gst-plugins-qt6", OldVersion: "1.28.6", NewVersion: "1.29.2",
		NewArchive: "/var/cache/distfiles/gst-plugins-good-1.29.2.tar.xz",
	})
	if err != nil {
		t.Fatalf("ReviewBump: %v", err)
	}
	reasons["missing archive"] = missing.SkipReason

	seen := map[string]string{}
	for cause, reason := range reasons {
		if reason == "" {
			t.Errorf("%s produced an empty skip reason", cause)
			continue
		}
		if other, dup := seen[reason]; dup {
			t.Errorf("%s and %s report the identical reason %q; three causes with three different remedies are rendered as one",
				cause, other, reason)
		}
		seen[reason] = cause
	}
}
