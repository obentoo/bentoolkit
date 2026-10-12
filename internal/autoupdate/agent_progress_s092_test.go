package autoupdate

// Authored for story 092 (autoupdate-apply-observable), sub-task 3.2, test-after:
// the applier's three agent call sites hand each agent a Progress callback that
// lands in the package's task (R4.2, R4.3) and a LogPath under the logs
// directory, <logs>/<category>/<package>/<version>/<stage>-agent.jsonl (R4.7).

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/obentoo/bentoolkit/internal/autoupdate/fixer"
	"github.com/obentoo/bentoolkit/internal/autoupdate/llm"
	"github.com/obentoo/bentoolkit/internal/autoupdate/validate"
)

// agentProgressStream is a stream-json run: model text, two tool calls, and a
// successful result that lists one refused WebFetch.
const agentProgressStream = `{"type":"system","subtype":"init","tools":["Read","Bash","WebFetch"]}
{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"MODEL-PROSE-S092 thinking"}]}}
{"type":"assistant","message":{"role":"assistant","content":[{"type":"tool_use","id":"t1","name":"Read","input":{"file_path":"/stage/godot-4.7.ebuild"}}]}}
{"type":"assistant","message":{"role":"assistant","content":[{"type":"tool_use","id":"t2","name":"Bash","input":{"command":"pkgdev manifest --distdir /d"}}]}}
{"type":"result","subtype":"success","is_error":false,"result":"edited SRC_URI","permission_denials":[{"tool_name":"WebFetch","tool_input":{"url":"https://blocked.example.org/rel?SENTINEL-INPUT-S092"}}]}
`

// agentProgressSeam is a `claude` that prints body from a file and exits 0.
func agentProgressSeam(t *testing.T, body string) func(ctx context.Context, name string, arg ...string) *exec.Cmd {
	t.Helper()
	p := filepath.Join(t.TempDir(), "stream.jsonl")
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return func(ctx context.Context, _ string, _ ...string) *exec.Cmd {
		return exec.CommandContext(ctx, "/bin/sh", "-c", `cat "$0"`, p)
	}
}

func taskLines(events []string, pkg string) []string {
	var out []string
	for _, e := range events {
		if rest, ok := strings.CutPrefix(e, "TaskLine:"+pkg+":"); ok {
			out = append(out, rest)
		}
	}
	return out
}

// The manifest fixer, end to end through Apply: each tool call and the refusal
// is one line of the package's task, prefixed with the stage the live region
// shows for the fix, and the stream is kept 0600 under the logs directory.
func TestAgentProgressManifestFixStreamsIntoTheTask(t *testing.T) {
	isolateSecretsPaths(t)
	tmpDir := t.TempDir()
	overlayDir := filepath.Join(tmpDir, "overlay")
	configDir := filepath.Join(tmpDir, "config")
	pkg, oldVersion, newVersion := "dev-games/godot", "4.7_rc3", "4.7"
	createTestEbuildFile(t, overlayDir, pkg, oldVersion)
	pending, err := NewPendingList(configDir)
	if err != nil {
		t.Fatal(err)
	}
	pending.Add(PendingUpdate{Package: pkg, CurrentVersion: oldVersion, NewVersion: newVersion, Status: StatusPending})

	rec := &recordingReporter{}
	f := newTestFixer(t, llm.LLMConfig{Provider: "claude-code", Bare: "false"},
		fixer.WithFixerExecCommand(agentProgressSeam(t, agentProgressStream)))
	applier, err := NewApplier(overlayDir, configDir,
		WithApplierPendingList(pending),
		WithExecCommand(pkgdevFailsPrinting("SRC_URI is unreachable: 404 Not Found")),
		WithApplierFixer(f),
		WithApplierReporter(rec),
	)
	if err != nil {
		t.Fatalf("NewApplier: %v", err)
	}
	if _, err := applier.Apply(t.Context(), pkg, false); err == nil {
		t.Fatal("fixture: the re-check was scripted to fail")
	}

	events := rec.snapshot()
	lines := taskLines(events, pkg)
	for _, want := range []string{
		fixer.StageManifestFix + ": Read /stage/godot-4.7.ebuild",
		fixer.StageManifestFix + ": Bash pkgdev",
		fixer.StageManifestFix + ": WebFetch(blocked.example.org) denied",
	} {
		if !slices.Contains(lines, want) {
			t.Errorf("the task has no line %q; its lines: %q", want, lines)
		}
	}
	for _, l := range lines {
		if strings.Contains(l, "MODEL-PROSE-S092") || strings.Contains(l, "SENTINEL-INPUT-S092") {
			t.Errorf("a task line carries model text or a refused input: %q", l)
		}
	}
	assertOrder(t, events,
		"TaskStage:"+pkg+":"+fixer.StageManifestFix,
		"TaskLine:"+pkg+":"+fixer.StageManifestFix+": Read /stage/godot-4.7.ebuild")

	logPath := filepath.Join(applier.LogsDir(), "dev-games", "godot", newVersion, fixer.StageManifestFix+"-agent.jsonl")
	body, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("the manifest fixer's stream was not kept at %s: %v", logPath, err)
	}
	if !strings.Contains(string(body), `"file_path":"/stage/godot-4.7.ebuild"`) || !strings.Contains(string(body), `"type":"result"`) {
		t.Errorf("the agent log does not hold the stream:\n%s", body)
	}
	if fi, err := os.Stat(logPath); err != nil || fi.Mode().Perm() != 0o600 {
		t.Errorf("agent log mode: %v (err %v), want 0600", fi.Mode().Perm(), err)
	}
}

// The build fixer on the --compile path: its request carries the task's
// callback and the stage's log path.
func TestAgentProgressBuildFixCompilePath(t *testing.T) {
	spy := &progressBuildFixSpy{stage: fixer.StageBuildFix, buildFixSpy: buildFixSpy{err: errors.New("stop after the request is recorded")}}
	h := buildFixFixture(t, &spy.buildFixSpy, buildRun{output: preparedThenConfigureFails, err: errors.New("exit status 1")})
	WithApplierBuildFixer(spy)(h.applier)
	rec := &recordingReporter{}
	WithApplierReporter(rec)(h.applier)

	if result, _ := h.applier.Apply(t.Context(), h.pkg, true); result.Success {
		t.Fatal("fixture: the fix was scripted to fail")
	}
	if len(spy.calls) != 1 {
		t.Fatalf("the build fixer ran %d time(s), want 1", len(spy.calls))
	}
	assertAgentWiring(t, h.applier, rec, spy.calls[0].Progress, spy.calls[0].LogPath,
		h.pkg, "1.29.2", fixer.StageBuildFix)
}

// The build fixer on the depth-driven gates path.
func TestAgentProgressBuildFixGatesPath(t *testing.T) {
	tmp := t.TempDir()
	spy := &progressBuildFixSpy{stage: fixer.StageBuildFix, buildFixSpy: buildFixSpy{err: errors.New("stop after the request is recorded")}}
	rec := &recordingReporter{}
	a, err := NewApplier(filepath.Join(tmp, "overlay"), filepath.Join(tmp, "config"),
		WithExecCommand(func(ctx context.Context, _ string, _ ...string) *exec.Cmd {
			return exec.CommandContext(ctx, "true")
		}),
		WithApplierDistdir(t.TempDir(), ""),
		WithApplierBuildFixer(spy),
		WithApplierReporter(rec),
	)
	if err != nil {
		t.Fatal(err)
	}
	const pkg, version = "media-plugins/gst-plugins-qt6", "1.29.2"
	staged := filepath.Join(tmp, "staged")
	cand := candidatePaths{
		staged:     true,
		repoRoot:   staged,
		pkgDir:     filepath.Join(staged, pkg),
		ebuildPath: filepath.Join(staged, pkg, "gst-plugins-qt6-1.29.2.ebuild"),
	}
	first := buildAttempt{transcript: preparedThenConfigureFails, err: errors.New("exit status 1")}
	if _, err := a.repairBuildGatesAndRerun(t.Context(), cand, pkg, version,
		validate.BuildRequest{Depth: validate.DepthConfigure}, validate.BuildDeps{}, first, &ApplyResult{}); err == nil {
		t.Fatal("fixture: the fix was scripted to fail")
	}
	if len(spy.calls) != 1 {
		t.Fatalf("the build fixer ran %d time(s), want 1", len(spy.calls))
	}
	assertAgentWiring(t, a, rec, spy.calls[0].Progress, spy.calls[0].LogPath, pkg, version, fixer.StageBuildFix)
}

// wiringLine is the progress line the wiring spies deliver during the call.
func wiringLine(stage string) string { return stage + ": Edit /staged/x.ebuild" }

// progressBuildFixSpy is a buildFixSpy that delivers wiringLine through the
// request's Progress while the call runs, as a real agent does.
type progressBuildFixSpy struct {
	buildFixSpy
	stage string
}

func (s *progressBuildFixSpy) FixBuild(ctx context.Context, req fixer.BuildFixRequest) (fixer.BuildFixResult, error) {
	if req.Progress != nil {
		req.Progress(wiringLine(s.stage))
	}
	return s.buildFixSpy.FixBuild(ctx, req)
}

// bumpReviewSpy records the request, delivers wiringLine during the call and
// skips.
type bumpReviewSpy struct{ calls []fixer.BumpReviewRequest }

func (s *bumpReviewSpy) ReviewBump(_ context.Context, req fixer.BumpReviewRequest) (fixer.BumpReviewReport, error) {
	s.calls = append(s.calls, req)
	if req.Progress != nil {
		req.Progress(wiringLine(fixer.StageBumpReview))
	}
	return fixer.BumpReviewReport{Skipped: true, SkipReason: "spy"}, nil
}

// The bump reviewer: its log is keyed by the candidate version.
func TestAgentProgressBumpReview(t *testing.T) {
	tmp := t.TempDir()
	spy := &bumpReviewSpy{}
	rec := &recordingReporter{}
	a, err := NewApplier(filepath.Join(tmp, "overlay"), filepath.Join(tmp, "config"),
		WithApplierDistdir(t.TempDir(), ""),
		WithApplierBumpReviewer(spy),
		WithApplierReporter(rec),
	)
	if err != nil {
		t.Fatal(err)
	}
	const pkg = "media-plugins/gst-plugins-qt6"
	var gates []validate.GateResult
	a.reviewBump(t.Context(), candidatePaths{staged: true, pkgDir: filepath.Join(tmp, "staged", pkg)},
		pkg, "1.28.6", "1.29.2", validate.DepthDecision{Depth: validate.DepthOptions, Reason: "policy"}, &gates)
	if len(spy.calls) != 1 {
		t.Fatalf("the reviewer ran %d time(s), want 1", len(spy.calls))
	}
	assertAgentWiring(t, a, rec, spy.calls[0].Progress, spy.calls[0].LogPath, pkg, "1.29.2", fixer.StageBumpReview)
}

// assertAgentWiring checks a call site's request: the line the spy delivered
// through Progress during the call joined pkg's task after the stage was
// entered, a call made once the agent call returned adds nothing, and LogPath
// is the stage's file.
func assertAgentWiring(t *testing.T, a *Applier, rec *recordingReporter, progress func(string), logPath, pkg, version, stage string) {
	t.Helper()
	cat, name, _ := strings.Cut(pkg, "/")
	if want := filepath.Join(a.LogsDir(), cat, name, version, stage+"-agent.jsonl"); logPath != want {
		t.Errorf("LogPath = %q, want %q", logPath, want)
	}
	if progress == nil {
		t.Fatal("the request carries no Progress callback")
	}
	assertOrder(t, rec.snapshot(), "TaskStage:"+pkg+":"+stage, "TaskLine:"+pkg+":"+wiringLine(stage))

	late := stage + ": Edit /staged/late.ebuild"
	progress(late)
	if lines := taskLines(rec.snapshot(), pkg); slices.Contains(lines, late) {
		t.Errorf("a Progress call after the agent call returned reached the task: %q", late)
	}
}

// No logs directory, or a name that cannot be one: the agent runs unrecorded.
func TestAgentProgressLogPathEmptyWhenItCannotBeKept(t *testing.T) {
	a := &Applier{}
	if got := a.agentLogPath("dev-games/godot", "4.7", fixer.StageManifestFix); got != "" {
		t.Errorf("no logs directory: LogPath = %q, want empty", got)
	}
	a.logsDir = t.TempDir()
	for _, bad := range [][2]string{{"no-category", "1"}, {"dev-games/godot", ".."}, {"dev-games/godot", "a/b"}} {
		if got := a.agentLogPath(bad[0], bad[1], fixer.StageManifestFix); got != "" {
			t.Errorf("agentLogPath(%q, %q) = %q, want empty", bad[0], bad[1], got)
		}
	}
}
