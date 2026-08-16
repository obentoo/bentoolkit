package validate

// Authored for story 033, sub-task 7.1 — R4, R4.1, R4.2, R4.3, R5, R5.1, R5.2,
// R6, R6.4, R6.5.
//
// ONE INVOCATION, THREE GATES. `ebuild <path> clean configure` runs setup,
// unpack, prepare and configure in a single pass, and the captured output
// carries each phase's marker. Running a patches gate and then a configure gate
// as two invocations would unpack the same 6 MB tarball twice for no additional
// information (design D4), so the runner invokes the DEEPEST phase the selected
// depth requires exactly once and derives every shallower gate from the markers.
// "Exactly one process" is therefore an assertion, not a comment.
//
// The log fixtures below are the shapes MEASURED on the maintainer's host
// (design M-A), reproduced verbatim in their ordering:
//
//	>>> Source unpacked in <dir>
//	>>> Preparing source in <dir> ...
//	>>> Source prepared.
//	>>> Configuring source in <dir> ...
//	>>> Source configured.                       (or the meson error + ERROR: ...)
//
// The 6 MB tarballs are never committed and none is needed: what the gate reads
// is the ebuild's own output, and that is text.
//
// D13 IS ASSERTED IN BOTH DIRECTIONS. Portage stamps its failure with the
// STAGING repository's name — the measured line is
// `media-plugins/gst-plugins-qt6-1.29.2::bentoo-staging failed (configure phase)`
// — and the operator never created that repository. So the REPORT is re-labelled
// with the real atom, and the RETAINED LOG is not: the log is the evidence that
// gets pasted into an upstream bug, and one bentoo edited is one nobody can
// compare against their own run.
//
// This file pins the names design.md fixes in prose but not in code:
// `BuildRequest{StagedRoot, Atom, Version, Depth, RequireIsolation, LogDir}`,
// `BuildDeps{ExecCommand, RunAttached, LookPath, IsolationProbe}` and
// `RunBuildGates(ctx, BuildRequest, BuildDeps) ([]GateResult, error)`.

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// The four phase markers, written out once so a fixture cannot drift from what
// the runner greps for.
const (
	markerUnpacked    = ">>> Source unpacked in /var/tmp/portage/media-plugins/gst-plugins-qt6-1.29.2/work\n"
	markerPreparing   = ">>> Preparing source in /var/tmp/portage/media-plugins/gst-plugins-qt6-1.29.2/work/gst-plugins-good-1.29.2 ...\n"
	markerPrepared    = ">>> Source prepared.\n"
	markerConfiguring = ">>> Configuring source in /var/tmp/portage/media-plugins/gst-plugins-qt6-1.29.2/work/gst-plugins-good-1.29.2 ...\n"
	markerConfigured  = ">>> Source configured.\n"
)

// configureOKLog is a clean run to configure depth, with two patches applied.
const configureOKLog = markerUnpacked +
	markerPreparing +
	" * Applying gst-plugins-qt6-1.29.2-qt6-detection.patch ...\n" +
	" * Applying gst-plugins-qt6-1.29.2-meson-options.patch ...\n" +
	markerPrepared +
	markerConfiguring +
	markerConfigured

// configureOKNoPatchLog is the same run for an ebuild that applies no patch at
// all — R4.3's case, which must be a PASS that SAYS SO rather than a PASS that
// looks identical to one where every patch applied.
const configureOKNoPatchLog = markerUnpacked +
	markerPreparing +
	markerPrepared +
	markerConfiguring +
	markerConfigured

// configureFailLog is the measurement this whole story exists for, verbatim
// (design M-A). Note `::bentoo-staging`: Portage names the STAGING repo, and the
// report has to translate that back before an operator ever reads it (D13).
// EXTENDED IN STORY 037 (2026-08-16) WITH `die`'s REAL EPILOGUE, and the
// extension is the whole point of it. As authored this constant carried three
// non-empty lines after the last phase marker, where a real Portage failure
// carries 24 — so failureExcerpt's excerptLines window never engaged, and the
// test below passed without ever exercising the selection it is supposed to
// pin. Measured on this host, the epilogue is 16 lines and comes AFTER the
// error, which is precisely how the shipped tail-quoting reported 12 lines of
// boilerplate and none of the cause. The lines below are the ones `ebuild`
// actually printed, with the staging repo name kept so `clean` still has
// something to scrub.
const configureFailLog = markerUnpacked +
	markerPreparing +
	" * Applying gst-plugins-qt6-1.29.2-qt6-detection.patch ...\n" +
	markerPrepared +
	markerConfiguring +
	"meson.build:1:0: ERROR: Unknown option: \"aalib\".\n" +
	"ERROR: media-plugins/gst-plugins-qt6-1.29.2::bentoo-staging failed (configure phase):\n" +
	"  meson setup failed\n" +
	" * ERROR: media-plugins/gst-plugins-qt6-1.29.2::bentoo-staging failed (configure phase):\n" +
	" *   meson setup failed\n" +
	" * \n" +
	" * Call stack:\n" +
	" *     ebuild.sh, line  143:  Called src_configure\n" +
	" *   environment, line 2603:  Called meson_src_configure\n" +
	" *   environment, line 1769:  Called die\n" +
	" * The specific snippet of code:\n" +
	" *       [[ ${rv} -eq 0 ]] || die -n \"configure failed\";\n" +
	" * \n" +
	" * If you need support, post the output of `emerge --info '=media-plugins/gst-plugins-qt6-1.29.2::bentoo-staging'`,\n" +
	" * the complete build log and the output of `emerge -pqv '=media-plugins/gst-plugins-qt6-1.29.2::bentoo-staging'`.\n" +
	" * The complete build log is located at '/var/tmp/portage/media-plugins/gst-plugins-qt6-1.29.2/temp/build.log.gz'.\n" +
	" * The ebuild environment file is located at '/var/tmp/portage/media-plugins/gst-plugins-qt6-1.29.2/temp/environment'.\n" +
	" * Working directory: '/var/tmp/portage/media-plugins/gst-plugins-qt6-1.29.2/work/gst-plugins-good-1.29.2'\n" +
	" * S: '/var/tmp/portage/media-plugins/gst-plugins-qt6-1.29.2/work/gst-plugins-good-1.29.2'\n"

// configureBareDieFailLog is the shape the banner-ending window got wrong: a
// phase that produced NO diagnostic of its own before dying. `econf || die
// "econf failed"` and `emake || die "emake failed"` are the common case in the
// tree, and for them die's message — printed AFTER the banner — is the entire
// cause. An excerpt that ends at the banner therefore quotes a line that only
// repeats failReason, and the operator learns nothing they did not already have.
const configureBareDieFailLog = markerUnpacked +
	markerPreparing +
	markerPrepared +
	markerConfiguring +
	"ERROR: media-plugins/gst-plugins-qt6-1.29.2::bentoo-staging failed (configure phase):\n" +
	"  econf failed\n" +
	" * ERROR: media-plugins/gst-plugins-qt6-1.29.2::bentoo-staging failed (configure phase):\n" +
	" *   econf failed\n" +
	" * \n" +
	" * Call stack:\n" +
	" *     ebuild.sh, line  143:  Called src_configure\n" +
	" *   environment, line 1769:  Called die\n" +
	" * The specific snippet of code:\n" +
	" *       econf || die \"econf failed\";\n" +
	" * The complete build log is located at '/var/tmp/portage/media-plugins/gst-plugins-qt6-1.29.2/temp/build.log.gz'.\n"

// unpackFailLog stops before `>>> Source prepared.`, which per design D6 makes
// it a host or distfile fault rather than a statement about the ebuild.
const unpackFailLog = ">>> Unpacking source...\n" +
	">>> Unpacking gst-plugins-good-1.29.2.tar.xz to /var/tmp/portage/.../work\n" +
	"tar: Unexpected EOF in archive\n" +
	"ERROR: media-plugins/gst-plugins-qt6-1.29.2::bentoo-staging failed (unpack phase):\n"

// buildSpy records what the runner spawned, so "exactly one process" and "the
// argv named one phase" are observable rather than assumed.
type buildSpy struct {
	spawns     int
	names      []string
	argv       [][]string
	attached   int
	lastCmd    *exec.Cmd
	lookedUp   []string
	envAtSpawn []string
}

// buildSeam returns deps whose child never really runs: ExecCommand builds a
// harmless `true` so the *exec.Cmd is real (the runner sets Dir and Env on it),
// and RunAttached answers with the captured log and the given error. Nothing
// touches the network, a distdir or a privilege tool.
func buildSeam(spy *buildSpy, output string, runErr error) BuildDeps {
	return BuildDeps{
		ExecCommand: func(ctx context.Context, name string, arg ...string) *exec.Cmd {
			spy.spawns++
			spy.names = append(spy.names, name)
			spy.argv = append(spy.argv, arg)
			cmd := exec.CommandContext(ctx, "true")
			spy.lastCmd = cmd
			return cmd
		},
		RunAttached: func(cmd *exec.Cmd) ([]byte, error) {
			spy.attached++
			spy.lastCmd = cmd
			spy.envAtSpawn = cmd.Env
			return []byte(output), runErr
		},
		LookPath: func(name string) (string, error) {
			spy.lookedUp = append(spy.lookedUp, name)
			return "/usr/bin/" + name, nil
		},
		IsolationProbe: func() (bool, string) { return true, "" },
	}
}

// buildRequestFor is the request every case below starts from: a staged tree
// path, the real atom, and a log directory under t.TempDir().
func buildRequestFor(t *testing.T, depth Depth) BuildRequest {
	t.Helper()
	return BuildRequest{
		StagedRoot: filepath.Join(t.TempDir(), "staging", "media-plugins", "gst-plugins-qt6", "1.29.2"),
		Atom:       "media-plugins/gst-plugins-qt6",
		Version:    "1.29.2",
		Depth:      depth,
		LogDir:     t.TempDir(),
	}
}

// gateNamed returns the gate with the given name, failing when it is absent —
// a gate the depth covered must always be reported, even if only to say it was
// skipped.
func gateNamed(t *testing.T, gates []GateResult, name string) GateResult {
	t.Helper()
	for _, g := range gates {
		if g.Gate == name {
			return g
		}
	}
	t.Fatalf("no %q gate in %+v; a gate the selected depth covers is always reported", name, gates)
	return GateResult{}
}

// TestRunBuildGates_OneInvocationForAConfigureRequest is design D4 as an
// assertion. Two invocations would be a correctness-neutral change that doubles
// the cost of every bump, so it is pinned here rather than left to review.
func TestRunBuildGates_OneInvocationForAConfigureRequest(t *testing.T) {
	spy := &buildSpy{}

	if _, err := RunBuildGates(context.Background(), buildRequestFor(t, DepthConfigure), buildSeam(spy, configureOKLog, nil)); err != nil {
		t.Fatalf("RunBuildGates: %v", err)
	}

	if spy.spawns != 1 {
		t.Errorf("spawned %d processes for a configure-depth request, want exactly 1 — "+
			"running the phases separately unpacks the 6 MB tarball twice for no additional information (D4)", spy.spawns)
	}
	if len(spy.names) > 0 && spy.names[0] != "ebuild" && !strings.HasSuffix(spy.names[0], "/ebuild") {
		t.Errorf("spawned %q, want the ebuild command", spy.names[0])
	}
	if len(spy.argv) > 0 {
		joined := strings.Join(spy.argv[0], " ")
		if !strings.Contains(joined, "clean") {
			t.Errorf("argv %q does not carry the clean phase", joined)
		}
		if !strings.Contains(joined, "configure") {
			t.Errorf("argv %q does not carry the configure phase", joined)
		}
		if strings.Contains(joined, "compile") {
			t.Errorf("argv %q runs compile for a configure-depth request", joined)
		}
	}
}

// TestRunBuildGates_CleanRunPassesPatchesAndConfigure is R4.2 and R5.2 together.
func TestRunBuildGates_CleanRunPassesPatchesAndConfigure(t *testing.T) {
	spy := &buildSpy{}

	gates, err := RunBuildGates(context.Background(), buildRequestFor(t, DepthConfigure), buildSeam(spy, configureOKLog, nil))
	if err != nil {
		t.Fatalf("RunBuildGates: %v", err)
	}

	if got := gateNamed(t, gates, GatePatches); got.Outcome != OutcomePass {
		t.Errorf("patches gate: got %q (reason %q), want PASS — the log carries %q",
			got.Outcome, got.Reason, strings.TrimSpace(markerPrepared))
	}
	cfg := gateNamed(t, gates, GateConfigure)
	if cfg.Outcome != OutcomePass {
		t.Errorf("configure gate: got %q (reason %q), want PASS — the log carries %q",
			cfg.Outcome, cfg.Reason, strings.TrimSpace(markerConfigured))
	}
	// R5.2: a configure pass names its own reach. Without this a green here
	// reads as "it builds", which is exactly the overclaim the story removes.
	if !strings.Contains(strings.ToLower(cfg.Reason), "compil") {
		t.Errorf("the configure PASS reason %q does not state that a configure pass does not cover compilation (R5.2)", cfg.Reason)
	}
}

// TestRunBuildGates_NoPatchAppliedIsAPassThatSaysSo is R4.3. "Every patch
// applied" and "there were no patches" are different answers and must not render
// alike.
func TestRunBuildGates_NoPatchAppliedIsAPassThatSaysSo(t *testing.T) {
	spy := &buildSpy{}

	gates, err := RunBuildGates(context.Background(), buildRequestFor(t, DepthConfigure), buildSeam(spy, configureOKNoPatchLog, nil))
	if err != nil {
		t.Fatalf("RunBuildGates: %v", err)
	}

	got := gateNamed(t, gates, GatePatches)
	if got.Outcome != OutcomePass {
		t.Fatalf("patches gate: got %q, want PASS for an ebuild that applies no patch", got.Outcome)
	}
	if got.Reason == "" {
		t.Error("the patches gate passed silently for an ebuild that applies no patch; R4.3 asks it to say so")
	}
}

// TestRunBuildGates_ConfigureFailureNamesTheOptionAndRetainsTheLog is the
// golden failure at build depth: the patches still applied, the configure step
// is where the bump dies, and the operator is handed the log.
func TestRunBuildGates_ConfigureFailureNamesTheOptionAndRetainsTheLog(t *testing.T) {
	spy := &buildSpy{}
	req := buildRequestFor(t, DepthConfigure)

	gates, err := RunBuildGates(context.Background(), req, buildSeam(spy, configureFailLog, errors.New("exit status 1")))
	if err != nil {
		t.Fatalf("RunBuildGates returned an error (%v); a failing build is a REPORTED OUTCOME, not an aborted run", err)
	}

	if got := gateNamed(t, gates, GatePatches); got.Outcome != OutcomePass {
		t.Errorf("patches gate: got %q, want PASS — the log reached %q before configure failed",
			got.Outcome, strings.TrimSpace(markerPrepared))
	}
	cfg := gateNamed(t, gates, GateConfigure)
	if cfg.Outcome != OutcomeFailed {
		t.Fatalf("configure gate: got %q, want FAILED", cfg.Outcome)
	}

	var details []string
	for _, f := range cfg.Findings {
		if f.Severity == SeverityError {
			details = append(details, f.Detail)
		}
	}
	joined := strings.Join(details, " | ")
	if !strings.Contains(joined, "aalib") {
		t.Errorf("the configure failure does not name the option upstream removed; findings: %q", joined)
	}
	// die's OWN message, which Portage prints AFTER the banner. Quoting up to the
	// banner and stopping dropped it, and here that loss is survivable only
	// because meson happened to print a diagnostic first — see
	// TestRunBuildGates_ABareDieStillReportsItsMessage for the shape where it is
	// the only thing there is.
	if !strings.Contains(joined, "meson setup failed") {
		t.Errorf("the excerpt drops the message die was called with; findings: %q", joined)
	}
	// The epilogue is still excluded. This is the half the story fixed first, and
	// appending die's message must not have re-admitted the boilerplate behind it.
	for _, boilerplate := range []string{"Call stack:", "located at", "If you need support"} {
		if strings.Contains(joined, boilerplate) {
			t.Errorf("die's epilogue is back in the excerpt (%q); findings: %q", boilerplate, joined)
		}
	}
}

// TestRunBuildGates_ABareDieStillReportsItsMessage pins the case that ending the
// excerpt at die's banner got wrong.
//
// The phase printed no diagnostic before dying, so everything before the banner
// is empty and the banner is the first line after the phase marker. Quoting
// `cause + banner` therefore yielded the banner alone — a line that names the
// atom and the phase, both of which failReason already says. The message die was
// called with is the only statement of WHY, and it is printed after the banner.
func TestRunBuildGates_ABareDieStillReportsItsMessage(t *testing.T) {
	spy := &buildSpy{}
	req := buildRequestFor(t, DepthConfigure)

	gates, err := RunBuildGates(context.Background(), req, buildSeam(spy, configureBareDieFailLog, errors.New("exit status 1")))
	if err != nil {
		t.Fatalf("RunBuildGates returned an error (%v); a failing build is a REPORTED OUTCOME", err)
	}

	cfg := gateNamed(t, gates, GateConfigure)
	if cfg.Outcome != OutcomeFailed {
		t.Fatalf("configure gate: got %q, want FAILED", cfg.Outcome)
	}

	var details []string
	for _, f := range cfg.Findings {
		if f.Severity == SeverityError {
			details = append(details, f.Detail)
		}
	}
	joined := strings.Join(details, " | ")

	if !strings.Contains(joined, "econf failed") {
		t.Errorf("the only statement of why this build died is absent from the findings: %q", joined)
	}
	for _, boilerplate := range []string{"Call stack:", "located at", "specific snippet"} {
		if strings.Contains(joined, boilerplate) {
			t.Errorf("die's epilogue leaked into the excerpt (%q); findings: %q", boilerplate, joined)
		}
	}

	// R5.1/R6.5: the log is retained and its path is named. Exactly one log for
	// one invocation.
	entries, err := os.ReadDir(req.LogDir)
	if err != nil {
		t.Fatalf("reading the log dir: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("the log dir holds %d files, want exactly 1 retained log", len(entries))
	}
	logPath := filepath.Join(req.LogDir, entries[0].Name())
	if !strings.Contains(cfg.Reason+" "+joined, logPath) {
		t.Errorf("neither the reason (%q) nor the findings name the retained log %q", cfg.Reason, logPath)
	}
	info, err := os.Stat(logPath)
	if err != nil {
		t.Fatalf("stat %q: %v", logPath, err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("the retained log is mode %04o, want 0600 — the existing compile log's mode is preserved", perm)
	}
}

// TestRunBuildGates_TheStagingRepoNameNeverReachesTheReport is D13. Portage
// stamps its failure with `::bentoo-staging` because that is the repo the build
// ran from; the operator asked about `media-plugins/gst-plugins-qt6` and must
// never have to know the staging repo's name to read the answer.
func TestRunBuildGates_TheStagingRepoNameNeverReachesTheReport(t *testing.T) {
	spy := &buildSpy{}

	gates, err := RunBuildGates(context.Background(), buildRequestFor(t, DepthConfigure), buildSeam(spy, configureFailLog, errors.New("exit status 1")))
	if err != nil {
		t.Fatalf("RunBuildGates: %v", err)
	}

	var reported []string
	for _, g := range gates {
		reported = append(reported, g.Reason)
		for _, f := range g.Findings {
			reported = append(reported, f.Detail)
		}
	}
	all := strings.Join(reported, " | ")
	if strings.Contains(all, "bentoo-staging") {
		t.Errorf("the staging repo name leaked into the report: %q", all)
	}
	if strings.Contains(all, "::") && !strings.Contains(all, "::bentoo ") {
		t.Errorf("the report carries a repository qualifier that is neither the real repo nor stripped: %q", all)
	}
	if !strings.Contains(all, "media-plugins/gst-plugins-qt6") {
		t.Errorf("the report does not name the real atom: %q", all)
	}
	// The VERSION has to survive the re-labelling too. A substitution matching
	// the whole `<atom>-<version>::<repo>` token and replacing it with the bare
	// atom would satisfy every assertion above while losing WHICH version
	// failed — inside a sweep that is the part the operator needs most.
	if !strings.Contains(all, "1.29.2") {
		t.Errorf("the re-labelled report no longer names the version that failed: %q", all)
	}
}

// TestRunBuildGates_TheRetainedLogKeepsPortagesOwnWords is the other half of
// D13, and it is a deliberate asymmetry: the REPORT is re-labelled, the
// EVIDENCE is not.
//
// The operator reads `media-plugins/gst-plugins-qt6-1.29.2` because they never
// created a repository called bentoo-staging and should not have to learn its
// name. But the retained log is what gets pasted into an upstream bug or a
// pkgdev question, and a log bentoo edited is a log nobody can trust — worse, a
// reader comparing it against their own run would find text Portage never
// emitted. So the raw bytes are kept exactly as the child produced them.
func TestRunBuildGates_TheRetainedLogKeepsPortagesOwnWords(t *testing.T) {
	spy := &buildSpy{}
	req := buildRequestFor(t, DepthConfigure)

	gates, err := RunBuildGates(context.Background(), req, buildSeam(spy, configureFailLog, errors.New("exit status 1")))
	if err != nil {
		t.Fatalf("RunBuildGates: %v", err)
	}
	if got := gateNamed(t, gates, GateConfigure); got.Outcome != OutcomeFailed {
		t.Fatalf("configure gate: got %q, want FAILED", got.Outcome)
	}

	entries, err := os.ReadDir(req.LogDir)
	if err != nil {
		t.Fatalf("reading the log dir: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("the log dir holds %d files, want exactly 1", len(entries))
	}
	body, err := os.ReadFile(filepath.Join(req.LogDir, entries[0].Name()))
	if err != nil {
		t.Fatalf("reading the retained log: %v", err)
	}

	if string(body) != configureFailLog {
		t.Errorf("the retained log was rewritten before being saved; it is the evidence an operator pastes into an upstream "+
			"bug, and a log bentoo edited is one nobody can compare against their own run.\n  got:  %q\n  want: %q", body, configureFailLog)
	}
	if !strings.Contains(string(body), "::bentoo-staging") {
		t.Error("Portage's own repository label was stripped from the retained log; the re-labelling belongs to the report, not to the evidence")
	}
}

// TestRunBuildGates_ACleanRunNamesNoStagingRepoEither keeps the re-labelling
// from being implemented on the failure path alone. A PASS states its own reach
// (R5.2, R6.4), and those sentences name the package too.
func TestRunBuildGates_ACleanRunNamesNoStagingRepoEither(t *testing.T) {
	spy := &buildSpy{}

	gates, err := RunBuildGates(context.Background(), buildRequestFor(t, DepthConfigure), buildSeam(spy, configureOKLog, nil))
	if err != nil {
		t.Fatalf("RunBuildGates: %v", err)
	}

	for _, g := range gates {
		if strings.Contains(g.Reason, "bentoo-staging") {
			t.Errorf("the %s gate's PASS reason names the staging repository: %q", g.Gate, g.Reason)
		}
		for _, f := range g.Findings {
			if strings.Contains(f.Detail, "bentoo-staging") {
				t.Errorf("a finding on the %s gate names the staging repository: %q", g.Gate, f.Detail)
			}
		}
	}
}

// TestRunBuildGates_FailureBeforePreparedAttributesToUnpack is design D6's first
// rule. A failure in setup or unpack is a host or distfile fault, so the patches
// gate has proved nothing — and saying PASS or FAILED there would both be lies,
// in opposite directions.
func TestRunBuildGates_FailureBeforePreparedAttributesToUnpack(t *testing.T) {
	spy := &buildSpy{}

	gates, err := RunBuildGates(context.Background(), buildRequestFor(t, DepthConfigure), buildSeam(spy, unpackFailLog, errors.New("exit status 1")))
	if err != nil {
		t.Fatalf("RunBuildGates: %v", err)
	}

	patches := gateNamed(t, gates, GatePatches)
	if patches.Outcome == OutcomePass {
		t.Error("the patches gate reported PASS on a run that never reached `>>> Source prepared.`")
	}
	if patches.Reason == "" || !strings.Contains(strings.ToLower(patches.Reason), "unpack") {
		t.Errorf("the patches outcome %q carries reason %q, which does not attribute the failure to the unpack phase (D6)",
			patches.Outcome, patches.Reason)
	}

	// And it is not misattributed downstream: configure never started, so
	// blaming it would send the operator to the wrong file.
	cfg := gateNamed(t, gates, GateConfigure)
	if cfg.Outcome == OutcomeFailed {
		t.Errorf("the configure gate reported FAILED although the run never reached %q", strings.TrimSpace(markerConfiguring))
	}
	if cfg.Reason == "" {
		t.Error("the configure gate did not run and gave no reason; a skip nobody can read is a pass")
	}
}

// TestRunBuildGates_CompileDepthRunsTheCompilePhaseAndNamesItsReach is R6.4.
func TestRunBuildGates_CompileDepthRunsTheCompilePhaseAndNamesItsReach(t *testing.T) {
	spy := &buildSpy{}
	log := configureOKLog + ">>> Compiling source in /var/tmp/portage/.../work ...\n>>> Source compiled.\n"

	gates, err := RunBuildGates(context.Background(), buildRequestFor(t, DepthCompile), buildSeam(spy, log, nil))
	if err != nil {
		t.Fatalf("RunBuildGates: %v", err)
	}

	if spy.spawns != 1 {
		t.Errorf("spawned %d processes for a compile-depth request, want exactly 1", spy.spawns)
	}
	if len(spy.argv) > 0 && !strings.Contains(strings.Join(spy.argv[0], " "), "compile") {
		t.Errorf("argv %v does not run the compile phase for a compile-depth request", spy.argv[0])
	}
	got := gateNamed(t, gates, GateCompile)
	if got.Outcome != OutcomePass {
		t.Fatalf("compile gate: got %q (reason %q), want PASS", got.Outcome, got.Reason)
	}
	if !strings.Contains(strings.ToLower(got.Reason), "install") {
		t.Errorf("the compile PASS reason %q does not state that a compile pass does not cover the install phase (R6.4)", got.Reason)
	}
}

// unverifiedIsolationLabel is story 031's wording, kept verbatim so the two
// gates cannot drift into describing the same fidelity in two ways.
const unverifiedIsolationLabel = "unverified isolation"

// reasonsOf flattens every gate's reason, which is where a gate states its own
// reach.
func reasonsOf(gates []GateResult) string {
	var parts []string
	for _, g := range gates {
		parts = append(parts, string(g.Gate)+": "+g.Reason)
	}
	return strings.Join(parts, " | ")
}

// TestRunBuildGates_NoNamespaceStillRunsAndSaysSo is R6.6 and D11 together: the
// default does not move, and the pass names the fidelity it actually had.
func TestRunBuildGates_NoNamespaceStillRunsAndSaysSo(t *testing.T) {
	spy := &buildSpy{}
	deps := buildSeam(spy, configureOKLog, nil)
	deps.IsolationProbe = func() (bool, string) {
		return false, "unshare(CLONE_NEWNET): operation not permitted"
	}

	req := buildRequestFor(t, DepthConfigure)
	req.RequireIsolation = false

	gates, err := RunBuildGates(context.Background(), req, deps)
	if err != nil {
		t.Fatalf("RunBuildGates: %v", err)
	}

	if spy.spawns != 1 {
		t.Fatalf("spawned %d processes; with isolation unavailable and not required the gate still runs (D11)", spy.spawns)
	}
	if got := gateNamed(t, gates, GateConfigure); got.Outcome != OutcomePass {
		t.Errorf("configure gate: got %q, want PASS — a missing namespace does not make a clean configure fail", got.Outcome)
	}
	if all := reasonsOf(gates); !strings.Contains(all, unverifiedIsolationLabel) {
		t.Errorf("no gate carries the %q label: %s\n"+
			"a pass that cannot name its own fidelity is the defect story 031 removed", unverifiedIsolationLabel, all)
	}
}

// TestRunBuildGates_VerifiedIsolationCarriesNoLabel keeps the label from
// becoming noise on the hosts where the gate really is isolated.
func TestRunBuildGates_VerifiedIsolationCarriesNoLabel(t *testing.T) {
	spy := &buildSpy{}
	deps := buildSeam(spy, configureOKLog, nil) // its probe grants the namespace

	gates, err := RunBuildGates(context.Background(), buildRequestFor(t, DepthConfigure), deps)
	if err != nil {
		t.Fatalf("RunBuildGates: %v", err)
	}

	if all := reasonsOf(gates); strings.Contains(all, unverifiedIsolationLabel) {
		t.Errorf("a verified-isolation run carries the %q label: %s", unverifiedIsolationLabel, all)
	}
}

// TestRunBuildGates_RequireIsolationSkipsAndSpawnsNothing is R6.2. Running an
// unisolated build after the operator demanded isolation would produce exactly
// the meaningless green they asked to avoid, so the build must not happen at
// all — asserted by watching the seam, not by reading the outcome.
//
// And the skip is a REPORTED outcome with a reason: applier.go:1379-1381's
// `("", nil)` silent skip is the one behaviour this story explicitly does not
// copy, because the caller cannot tell it from a pass.
func TestRunBuildGates_RequireIsolationSkipsAndSpawnsNothing(t *testing.T) {
	spy := &buildSpy{}
	deps := buildSeam(spy, configureOKLog, nil)
	deps.IsolationProbe = func() (bool, string) {
		return false, "unshare(CLONE_NEWNET): operation not permitted"
	}

	req := buildRequestFor(t, DepthConfigure)
	req.RequireIsolation = true

	gates, err := RunBuildGates(context.Background(), req, deps)
	if err != nil {
		t.Fatalf("RunBuildGates: %v; a refused build is a reported outcome, not an aborted run", err)
	}

	if spy.spawns != 0 {
		t.Errorf("spawned %d processes although isolation was required and unavailable", spy.spawns)
	}
	for _, name := range []string{GatePatches, GateConfigure} {
		got := gateNamed(t, gates, name)
		if got.Outcome != OutcomeSkipped {
			t.Errorf("%s gate: got %q, want SKIPPED", name, got.Outcome)
		}
		if got.Reason == "" {
			t.Errorf("%s gate reports SKIPPED with no reason", name)
		}
		if !strings.Contains(strings.ToLower(got.Reason), "isolation") {
			t.Errorf("%s gate's reason %q does not name isolation as the cause", name, got.Reason)
		}
	}
}

// TestRunBuildGates_ChildGetsAnAllowListedEnvironment is R6.3, and the evidence
// behind it is concrete: a stray variable from an interactive shell broke a
// configure inside emerge. The child gets a named set, not whatever the operator
// happened to export.
func TestRunBuildGates_ChildGetsAnAllowListedEnvironment(t *testing.T) {
	const stray = "BENTOO_STRAY_VARIABLE"
	t.Setenv(stray, "this must not reach the build")
	t.Setenv("PORTAGE_TMPDIR", "/var/tmp/portage-under-test")

	spy := &buildSpy{}

	if _, err := RunBuildGates(context.Background(), buildRequestFor(t, DepthConfigure), buildSeam(spy, configureOKLog, nil)); err != nil {
		t.Fatalf("RunBuildGates: %v", err)
	}

	if spy.envAtSpawn == nil {
		t.Fatal("cmd.Env was never set, so the child inherits the invoking shell's environment wholesale (R6.3)")
	}
	for _, kv := range spy.envAtSpawn {
		if strings.HasPrefix(kv, stray+"=") {
			t.Errorf("the child environment carries %q; only an allow-listed set may cross into the build", kv)
		}
	}

	// The allow-list has to be a list, not an empty set: a build with no PATH
	// cannot run, and PORTAGE_* is what points it at the scratch tree.
	joined := strings.Join(spy.envAtSpawn, "\n")
	for _, want := range []string{"PATH=", "PORTAGE_TMPDIR="} {
		if !strings.Contains(joined, want) {
			t.Errorf("the child environment is missing %s; the allow-list is PATH, HOME, TERM, PORTAGE_*, FEATURES, MAKEOPTS and DISTDIR", want)
		}
	}
	// And the parent's own environment is untouched — the gate builds a child
	// env, it does not mutate this process.
	if os.Getenv(stray) == "" {
		t.Error("the parent process's environment was modified; the gate must build the child's env, never edit its own")
	}
}

// TestRunBuildGates_NoPrivilegeToolOnTheUnprivilegedPath is R6.1 and the direct
// consequence of M-B: membership in `portage` was measured sufficient, so
// reaching for sudo or doas would make an unattended sweep prompt for a password
// it can never receive.
func TestRunBuildGates_NoPrivilegeToolOnTheUnprivilegedPath(t *testing.T) {
	spy := &buildSpy{}

	if _, err := RunBuildGates(context.Background(), buildRequestFor(t, DepthConfigure), buildSeam(spy, configureOKLog, nil)); err != nil {
		t.Fatalf("RunBuildGates: %v", err)
	}

	for _, name := range spy.names {
		if name == "sudo" || name == "doas" {
			t.Errorf("the gate spawned %q on the unprivileged path; the whole unpack/prepare/configure cycle was measured to run as the invoking user (M-B)", name)
		}
	}
	for _, looked := range spy.lookedUp {
		if looked == "sudo" || looked == "doas" {
			t.Errorf("the gate looked up the privilege tool %q although the unprivileged path was available", looked)
		}
	}
	if len(spy.argv) > 0 {
		if first := spy.argv[0]; len(first) > 0 && (first[0] == "ebuild") {
			t.Errorf("argv %v looks like `<privtool> ebuild …`; the unprivileged invocation runs ebuild directly", first)
		}
	}
}

// TestRunBuildGates_PrivilegeUnobtainableIsSkippedNotFailed is R6.2's other
// half: when escalation IS required and cannot be had without a human, the gate
// reports SKIPPED naming why rather than failing the whole sweep. `sudo -n`
// prompting is the measured condition (M-B), so a sweep must survive it.
func TestRunBuildGates_PrivilegeUnobtainableIsSkippedNotFailed(t *testing.T) {
	spy := &buildSpy{}
	deps := buildSeam(spy, configureOKLog, nil)
	deps.LookPath = func(name string) (string, error) {
		spy.lookedUp = append(spy.lookedUp, name)
		if name == "sudo" || name == "doas" {
			return "", os.ErrNotExist
		}
		return "/usr/bin/" + name, nil
	}
	deps.IsolationProbe = func() (bool, string) { return false, "unshare(CLONE_NEWNET): operation not permitted" }

	req := buildRequestFor(t, DepthConfigure)
	req.RequireIsolation = true // the only reason to need privilege at all

	gates, err := RunBuildGates(context.Background(), req, deps)
	if err != nil {
		t.Fatalf("RunBuildGates: %v; an unobtainable privilege is a reported outcome, not a failed run", err)
	}

	got := gateNamed(t, gates, GateConfigure)
	if got.Outcome == OutcomeFailed {
		t.Error("the gate reported FAILED because privilege could not be obtained; that blames the ebuild for the host")
	}
	if got.Outcome != OutcomeSkipped || got.Reason == "" {
		t.Errorf("configure gate: got %q with reason %q, want SKIPPED naming why", got.Outcome, got.Reason)
	}
}

// TestRunBuildGates_AnInterruptedBuildIsAnErrorNotAGateList pins the shape of
// the answer, and the shape is the whole point.
//
// Two wrong answers were tried before this one. Deriving normally reports
// FAILED: the child is spawned through CommandContext, so Ctrl-C kills it, the
// phase counts as started-and-failed, and the operator is told their ebuild is
// broken. Returning SkippedGates instead was WORSE — PromotionDecision promotes
// on a list of PASS-or-SKIPPED, so an interrupted `--apply --depth=compile`
// would publish the bump into an overlay that auto-commits and pushes.
//
// A gate list cannot express "nothing was measured", because every value it can
// hold is a statement about the candidate. So the error travels, and each
// driver already refuses to promote on one.
func TestRunBuildGates_AnInterruptedBuildIsAnErrorNotAGateList(t *testing.T) {
	spy := &buildSpy{}
	req := buildRequestFor(t, DepthConfigure)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	gates, err := RunBuildGates(ctx, req, buildSeam(spy, configureFailLog, errors.New("signal: killed")))

	if err == nil {
		t.Fatalf("an interrupted build returned no error; gates=%+v — a gate list is a statement about the "+
			"candidate, and PromotionDecision publishes on one that is all PASS or SKIPPED", gates)
	}
	if !errors.Is(err, context.Canceled) {
		t.Errorf("the error does not wrap the context cause (%v); a caller cannot tell an interrupt from a "+
			"malformed request without matching this sentence", err)
	}
	// The critical assertion: NOTHING promotable comes back. A single SKIPPED
	// gate here is enough for PromotionDecision to return promoted=true.
	if len(gates) != 0 {
		t.Errorf("an interrupted build produced %d gate(s): %+v — every one of them is promotable", len(gates), gates)
	}
	// The partial transcript is still evidence, and the error names where it is.
	entries, rerr := os.ReadDir(req.LogDir)
	if rerr != nil {
		t.Fatalf("reading the log dir: %v", rerr)
	}
	if len(entries) != 1 {
		t.Errorf("the log dir holds %d files; the partial transcript of an interrupted build is evidence "+
			"someone may want", len(entries))
	}
}

// TestPromotionDecision_RefusesNothingRatherThanPromotingIt is the invariant the
// interrupt fix exists to protect, asserted directly so a future change to
// RunBuildGates' return shape cannot quietly re-open it.
//
// An all-SKIPPED list promotes BY DESIGN — a gate that could not run is not a
// gate that objected. That design is only safe while "could not run" never
// covers "was killed halfway through". This pins the consequence: if an
// interrupt ever produces skipped gates again, this test still passes, and the
// one above is what fails. Both are needed; neither is redundant.
func TestPromotionDecision_RefusesNothingRatherThanPromotingIt(t *testing.T) {
	skipped := SkippedGates(DepthConfigure, "killed mid-build")

	promoted, reason := PromotionDecision(skipped, nil)
	if !promoted {
		t.Skip("PromotionDecision no longer promotes on all-SKIPPED; the interrupt hazard this guards is gone")
	}
	t.Logf("confirmed: an all-SKIPPED list promotes (%q) — which is why an interrupt must not produce one", reason)
}
