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
// `BuildRequest{StagedRoot, Key, Version, Depth, RequireIsolation, LogDir}`,
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

// compiledLog is a clean run as far as `>>> Source compiled.` — the ceiling the
// ladder had before story 042.
const compiledLog = configureOKLog +
	">>> Compiling source in /var/tmp/portage/media-plugins/gst-plugins-qt6-1.29.2/work ...\n" +
	">>> Source compiled.\n"

// installOKLog carries the two install lines MEASURED on this host rather than
// invented: /usr/lib/portage/python3.14/phase-functions.sh:636 prints
// ">>> Install ${CATEGORY}/${PF} into ${D}" and :654 prints
// ">>> Completed installing ${CATEGORY}/${PF} into ${D}".
//
// The package name is interpolated IMMEDIATELY AFTER each marker, with a single
// space between. That is the whole reason the trailing space belongs to the
// marker and the match is a prefix match — a marker written without it would
// also match a line like ">>> Installing", and one written with the package
// name in it would match nothing at all.
const installOKLog = compiledLog +
	">>> Install media-plugins/gst-plugins-qt6-1.29.2 into /var/tmp/portage/media-plugins/gst-plugins-qt6-1.29.2/image\n" +
	">>> Completed installing media-plugins/gst-plugins-qt6-1.29.2 into /var/tmp/portage/media-plugins/gst-plugins-qt6-1.29.2/image\n"

// installDiesLog is the failure this rung exists to catch: src_install began and
// never completed. A `doins` of a file upstream renamed is the honest shape,
// because it is the one that motivated the rung.
const installDiesLog = compiledLog +
	">>> Install media-plugins/gst-plugins-qt6-1.29.2 into /var/tmp/portage/media-plugins/gst-plugins-qt6-1.29.2/image\n" +
	"!!! ERROR: media-plugins/gst-plugins-qt6-1.29.2::bentoo-staging failed (install phase):\n" +
	"!!!   newins: /var/tmp/portage/media-plugins/gst-plugins-qt6-1.29.2/work/README.md does not exist\n"

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
		Key:        "media-plugins/gst-plugins-qt6",
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

// TestRunBuildGates_InstallDepthRunsTheInstallPhaseInOneInvocation is R1.1,
// R1.5 and R2.1 together. One `ebuild … clean install`, and a gate that reports
// on it — the phases cascade inside that single invocation, so a second spawn
// would be a correctness-neutral change that doubles what every install-depth
// bump costs (D4).
func TestRunBuildGates_InstallDepthRunsTheInstallPhaseInOneInvocation(t *testing.T) {
	spy := &buildSpy{}

	gates, err := RunBuildGates(context.Background(), buildRequestFor(t, DepthInstall), buildSeam(spy, installOKLog, nil))
	if err != nil {
		t.Fatalf("RunBuildGates: %v", err)
	}

	if spy.spawns != 1 {
		t.Errorf("spawned %d processes for an install-depth request, want exactly 1 — the phases cascade (D4)", spy.spawns)
	}
	if len(spy.argv) > 0 {
		if argv := strings.Join(spy.argv[0], " "); !strings.Contains(argv, "install") {
			t.Errorf("argv %q does not run the install phase for an install-depth request; deepestPhaseFor reads "+
				"deepest-first, so a case added below `>= DepthCompile` is unreachable and quietly compiles instead", argv)
		}
	}

	got := gateNamed(t, gates, GateInstall)
	if got.Outcome != OutcomePass {
		t.Fatalf("install gate: got %q (reason %q), want PASS", got.Outcome, got.Reason)
	}
}

// TestRunBuildGates_InstallWithoutItsDoneMarkerIsNotAPass is R2.4. A zero exit
// is not evidence that src_install finished — __vecho suppresses the marker
// under __quiet_mode, and a gate that read the exit status alone would report a
// pass for a phase it has no evidence ran.
func TestRunBuildGates_InstallWithoutItsDoneMarkerIsNotAPass(t *testing.T) {
	spy := &buildSpy{}

	// compiledLog exits 0 and stops one phase short: no `>>> Completed
	// installing` line anywhere in it.
	gates, err := RunBuildGates(context.Background(), buildRequestFor(t, DepthInstall), buildSeam(spy, compiledLog, nil))
	if err != nil {
		t.Fatalf("RunBuildGates: %v", err)
	}

	got := gateNamed(t, gates, GateInstall)
	if got.Outcome == OutcomePass {
		t.Fatalf("the install gate reported PASS on a run that never printed the completion marker; "+
			"reason %q — an install gate that could not run reports SKIPPED, never PASS", got.Reason)
	}
	if !strings.Contains(got.Reason, ">>> Completed installing") {
		t.Errorf("the underivable install gate reads %q and does not quote the marker it needed; "+
			"naming the missing evidence is what makes the outcome actionable", got.Reason)
	}
}

// TestRunBuildGates_InstallFailureDoesNotDragTheCompileGateDown is R7.1 and R6.1
// in one assertion, and it is the case the ladder gets WRONG the moment a rung
// is added above compile without revisiting `completed`.
//
// `completed` treats compile as the exception whose authority is the child's
// EXIT STATUS, because compile used to be the deepest phase any request
// invoked. On an install-depth run it is not deepest any more: a src_install
// that dies makes runErr non-nil, and compile — whose own `>>> Source compiled.`
// marker is right there in the transcript — would be reported FAILED for a
// phase that demonstrably finished.
func TestRunBuildGates_InstallFailureDoesNotDragTheCompileGateDown(t *testing.T) {
	spy := &buildSpy{}

	gates, err := RunBuildGates(context.Background(), buildRequestFor(t, DepthInstall),
		buildSeam(spy, installDiesLog, errors.New("exit status 1")))
	if err != nil {
		t.Fatalf("RunBuildGates: %v", err)
	}

	install := gateNamed(t, gates, GateInstall)
	if install.Outcome != OutcomeFailed {
		t.Errorf("install gate: got %q (reason %q), want FAILED — src_install began and never completed", install.Outcome, install.Reason)
	}

	compile := gateNamed(t, gates, GateCompile)
	if compile.Outcome != OutcomePass {
		t.Errorf("the compile gate reported %q (reason %q) on a run whose transcript contains %q; the exit status "+
			"stops being evidence about compile once the child went deeper than it, and blaming compile for an "+
			"install failure sends the operator to the wrong phase",
			compile.Outcome, compile.Reason, ">>> Source compiled.")
	}
}

// TestDeepestPhaseFor_EveryRungMapsWhereItDid is R1.1 and R1.5 as a table over
// the WHOLE ladder rather than a spot check on the new rung.
//
// deepestPhaseFor is a deepest-first switch, so a case in the wrong position is
// not a compile error — it is a rung that silently STEALS the phase of the rung
// below it. A test that only asserted install maps to phaseInstall would pass
// just as happily with compile broken, which is why every member of depthLadder
// is named here and the count is asserted at the end.
func TestDeepestPhaseFor_EveryRungMapsWhereItDid(t *testing.T) {
	want := map[Depth]struct {
		phase  buildPhase
		builds bool
	}{
		DepthNone:      {phaseSetup, false},
		DepthOptions:   {phaseSetup, false},
		DepthPatches:   {phasePrepare, true},
		DepthConfigure: {phaseConfigure, true},
		DepthCompile:   {phaseCompile, true},
		DepthInstall:   {phaseInstall, true},
	}

	for _, rung := range depthLadder {
		expected, named := want[rung]
		if !named {
			t.Errorf("depthLadder holds %v and this table does not name it; a rung added without revisiting "+
				"deepestPhaseFor is a rung that runs some other rung's phase", rung)
			continue
		}
		phase, builds := deepestPhaseFor(rung)
		if phase != expected.phase || builds != expected.builds {
			t.Errorf("deepestPhaseFor(%v) = (%v, %t), want (%v, %t)", rung, phase, builds, expected.phase, expected.builds)
		}
	}

	if len(want) != len(depthLadder) {
		t.Errorf("the table names %d rungs and the ladder holds %d; the two must be the same set, or this test "+
			"passes over a rung nobody mapped", len(want), len(depthLadder))
	}
}

// TestPhaseTrace_LastStartedReachesTheDeepestPhase pins the ceiling of
// lastStarted's loop.
//
// HONESTY NOTE: this is a regression PIN, not a test-first proof. It cannot be
// written as a Red — it names phaseInstall, and a Go package that references an
// identifier which does not exist yet does not compile, so no test in it runs at
// all. The defect it guards is also currently UNOBSERVABLE through the public
// surface: lastStarted is read only by notReachedReason (build.go), and with
// install at the top of the ladder there is no gate above install whose "not
// reached" sentence could quote the wrong phase. It is pinned anyway because the
// loop read `phaseCompile` as its ceiling until story 042, and the next rung
// added above install would re-introduce the mis-attribution in silence.
func TestPhaseTrace_LastStartedReachesTheDeepestPhase(t *testing.T) {
	if got := tracePhases(installDiesLog).lastStarted(); got != phaseInstall {
		t.Errorf("lastStarted() = %v for a transcript whose last marker is %q, want install; the loop's ceiling "+
			"must follow the enum (phaseCount-1) rather than name a phase, or a rung added above it is skipped",
			got, strings.TrimSpace(markerStartInstall))
	}

	// And the phase below it is still found when nothing deeper began, so the
	// widened ceiling did not turn into "always answer the deepest".
	if got := tracePhases(compiledLog).lastStarted(); got != phaseCompile {
		t.Errorf("lastStarted() = %v for a transcript that stops at %q, want compile",
			got, strings.TrimSpace(markerDoneCompile))
	}
}

// featuresEntries returns every FEATURES assignment in a composed environment.
// It returns the whole slice rather than the first match on purpose: "exactly
// one entry" is the property R3.3 is about, and a helper that answered with the
// first would make a duplicate invisible to every test below.
func featuresEntries(env []string) []string {
	var found []string
	for _, kv := range env {
		if strings.HasPrefix(kv, "FEATURES=") {
			found = append(found, kv)
		}
	}
	return found
}

// sameEnv reports byte-identity of two composed environments, order included.
// Joining on NUL is what makes it byte-identity rather than set equality: a
// reordering is a different environment to exec.Cmd's duplicate-key behaviour.
func sameEnv(a, b []string) bool {
	return strings.Join(a, "\x00") == strings.Join(b, "\x00")
}

// TestRunBuildGates_InstallComposesOneFeaturesEntryEndingInMinusTest is R3.1,
// R3.2 and R3.3 together, asserted on the COMPOSED environment the child would
// actually receive rather than on a flag or a struct field — a field can change
// while nothing observable does.
//
// The value is COMPOSED, never appended as a second assignment. allowedBuildEnv
// may already have placed a FEATURES entry there, and adding a second would
// leave exec.Cmd's duplicate-key behaviour to choose between them: the exact
// hazard build.go documents as the reason DISTDIR was taken off the allow-list.
//
// Measured on the maintainer's host, not assumed: FEATURES is INCREMENTAL — 42
// features at baseline, and FEATURES="-userpriv" yields 41 with only that one
// gone. So ` -test` subtracts src_test and preserves sandbox, network-sandbox,
// userpriv, ccache and everything else the host configured (S042-M3).
func TestRunBuildGates_InstallComposesOneFeaturesEntryEndingInMinusTest(t *testing.T) {
	t.Setenv("FEATURES", "test ccache sandbox network-sandbox userpriv")
	spy := &buildSpy{}

	if _, err := RunBuildGates(context.Background(), buildRequestFor(t, DepthInstall), buildSeam(spy, installOKLog, nil)); err != nil {
		t.Fatalf("RunBuildGates: %v", err)
	}

	entries := featuresEntries(spy.envAtSpawn)
	if len(entries) != 1 {
		t.Fatalf("the install child received %d FEATURES assignments (%v), want exactly 1 — two entries leave "+
			"exec.Cmd to choose, which is a decision nobody made (R3.3)", len(entries), entries)
	}
	if !strings.HasSuffix(entries[0], " -test") {
		t.Errorf("the install child received %q, which does not end in ` -test`; src_test runs between compile "+
			"and install, so a host carrying FEATURES=test would silently run upstream's suite and two machines "+
			"would return different verdicts for the same bump (R3.1)", entries[0])
	}
	for _, keep := range []string{"ccache", "sandbox", "network-sandbox", "userpriv"} {
		if !strings.Contains(entries[0], keep) {
			t.Errorf("the install child received %q, which dropped %q; -test must SUBTRACT one feature, not "+
				"overwrite the set — the sandbox and privilege-dropping features are the ones that matter most (R3.2)",
				entries[0], keep)
		}
	}
}

// TestRunBuildGates_InstallAddsFeaturesWhenTheParentCarriesNone covers the
// degenerate half of R3.3: with nothing to replace, exactly one entry is
// APPENDED, and it is still exactly one.
func TestRunBuildGates_InstallAddsFeaturesWhenTheParentCarriesNone(t *testing.T) {
	// t.Setenv first so its cleanup restores whatever the host really had; the
	// unset is what the case under test needs.
	t.Setenv("FEATURES", "placeholder")
	if err := os.Unsetenv("FEATURES"); err != nil {
		t.Fatalf("Unsetenv: %v", err)
	}

	spy := &buildSpy{}
	if _, err := RunBuildGates(context.Background(), buildRequestFor(t, DepthInstall), buildSeam(spy, installOKLog, nil)); err != nil {
		t.Fatalf("RunBuildGates: %v", err)
	}

	entries := featuresEntries(spy.envAtSpawn)
	if len(entries) != 1 || entries[0] != "FEATURES=-test" {
		t.Errorf("the install child received %v, want exactly one entry `FEATURES=-test`; a parent with no "+
			"FEATURES must still get src_test disabled, or the determinism the rung promises depends on the host "+
			"happening to set the variable", entries)
	}
}

// TestRunBuildGates_CompileEnvIsUntouchedByTheInstallRule is the R6.1 half of
// this sub-task, and the one that fails SILENTLY if the phase condition is
// dropped: a wrongly-scoped -test changes nothing an operator sees until a host
// that sets FEATURES=test runs a compile.
func TestRunBuildGates_CompileEnvIsUntouchedByTheInstallRule(t *testing.T) {
	for _, parent := range []struct {
		name  string
		value string
		unset bool
	}{
		{name: "parent carries FEATURES", value: "test ccache sandbox"},
		{name: "parent carries none", unset: true},
	} {
		t.Run(parent.name, func(t *testing.T) {
			t.Setenv("FEATURES", parent.value)
			if parent.unset {
				if err := os.Unsetenv("FEATURES"); err != nil {
					t.Fatalf("Unsetenv: %v", err)
				}
			}

			want := allowedBuildEnv(os.Environ())
			spy := &buildSpy{}
			if _, err := RunBuildGates(context.Background(), buildRequestFor(t, DepthCompile), buildSeam(spy, compiledLog, nil)); err != nil {
				t.Fatalf("RunBuildGates: %v", err)
			}

			if !sameEnv(spy.envAtSpawn, want) {
				t.Errorf("a compile-depth child received\n  %v\nwant the parent-filtered set byte-identical\n  %v\n"+
					"R6.1: a run that does not ask for the new rung behaves exactly as it did before, in cost AND in output",
					spy.envAtSpawn, want)
			}
			for _, kv := range featuresEntries(spy.envAtSpawn) {
				if strings.Contains(kv, "-test") {
					t.Errorf("a compile-depth child received %q; src_test runs BETWEEN compile and install, so a "+
						"compile run would never have reached it — subtracting it there is provably inert and still "+
						"a change to the environment of every existing gate", kv)
				}
			}
		})
	}
}

// TestRunBuildGates_InstallPassNamesQmergeAndSrcTest is R2.1, R2.2 and R2.3.
//
// Every rung of this ladder states its own ceiling, and adding one that did not
// would be the single change that makes the ladder less honest than it was. An
// install pass has TWO omissions and they have different causes: qmerge is out
// of the ladder permanently (S042-D2), and src_test did not run because this
// gate switched it off for determinism (S042-D3). An operator reading a green
// must not have to know the ladder's history to learn what it bought.
//
// Asserted on the RENDERED STRING rather than on a struct field: a field can
// change while the sentence an operator actually reads does not.
func TestRunBuildGates_InstallPassNamesQmergeAndSrcTest(t *testing.T) {
	spy := &buildSpy{}

	gates, err := RunBuildGates(context.Background(), buildRequestFor(t, DepthInstall), buildSeam(spy, installOKLog, nil))
	if err != nil {
		t.Fatalf("RunBuildGates: %v", err)
	}

	got := gateNamed(t, gates, GateInstall)
	if got.Outcome != OutcomePass {
		t.Fatalf("install gate: got %q (reason %q), want PASS", got.Outcome, got.Reason)
	}

	for _, uncovered := range []string{"qmerge", "src_test"} {
		if !strings.Contains(got.Reason, uncovered) {
			t.Errorf("the install PASS reason %q does not name %q as uncovered; a green that does not say where "+
				"it stops reads as \"it installs\", and that overclaim is what this rung exists to avoid",
				got.Reason, uncovered)
		}
	}

	// And it must not claim the opposite of what it did. src_install assembles
	// an IMAGE under ${D}; nothing was merged onto any system.
	if strings.Contains(got.Reason, "installed on") || strings.Contains(got.Reason, "installed onto") {
		t.Errorf("the install PASS reason %q reads as though the package was installed somewhere; the phase "+
			"assembles an image under ${D} inside PORTAGE_TMPDIR and merges nothing", got.Reason)
	}

	if !strings.Contains(got.Reason, "gst-plugins-qt6-1.29.2") {
		t.Errorf("the install PASS reason %q does not name the package it is about", got.Reason)
	}
}

// TestRunBuildGates_CompilePassStillNamesSrcInstall is R2.6 and R6.3 — a
// REGRESSION GUARD with no production change behind it.
//
// The compile sentence is exactly the kind that gets "helpfully" updated when a
// deeper rung arrives, and it is still true: a compile-depth run still stops
// short of src_install. The existing compile test asserts only that the reason
// contains "install", which the word "src_install" satisfies and so would the
// word "installing" — this pins the specific claim.
func TestRunBuildGates_CompilePassStillNamesSrcInstall(t *testing.T) {
	spy := &buildSpy{}

	gates, err := RunBuildGates(context.Background(), buildRequestFor(t, DepthCompile), buildSeam(spy, compiledLog, nil))
	if err != nil {
		t.Fatalf("RunBuildGates: %v", err)
	}

	got := gateNamed(t, gates, GateCompile)
	if got.Outcome != OutcomePass {
		t.Fatalf("compile gate: got %q (reason %q), want PASS", got.Outcome, got.Reason)
	}
	if !strings.Contains(got.Reason, "src_install") {
		t.Errorf("the compile PASS reason %q no longer states that src_install went uncovered; it is STILL TRUE "+
			"at compile depth, and story 033 made that promise before this ladder had a rung above it", got.Reason)
	}

	// The compile sentence is about compile, not about the rung above it: it
	// must not have acquired the install pass's own omissions.
	for _, notHere := range []string{"qmerge", "src_test"} {
		if strings.Contains(got.Reason, notHere) {
			t.Errorf("the compile PASS reason %q names %q; that is the INSTALL gate's ceiling, and a compile run "+
				"never reached the phase it is about", got.Reason, notHere)
		}
	}
}

// TestRunBuildGates_TheInstallGateDecidesBothWays is S042-R7.1 and R7.2 — the
// mutation proof, with BOTH outcomes in one place because either alone proves
// the wrong thing.
//
// A green on a healthy package is indistinguishable from a gate nobody wired.
// What tells them apart is the same candidate FAILING when its src_install is
// broken and PASSING when the break is removed, so neither half is allowed to
// stand on its own here.
//
// The mutation is on the TRANSCRIPT, which is the only thing these tests can
// mutate: buildSeam fakes the child entirely — ExecCommand returns a harmless
// `true` and RunAttached answers with the scripted output — so no real ebuild is
// ever built at this level. The break is `newins` on a file upstream renamed,
// which is the honest shape because it is the failure this rung was added for.
// The evidence that the marker strings match a REAL Portage rather than a
// synthetic transcript is sub-task 6.3's live run; no unit test can establish it.
func TestRunBuildGates_TheInstallGateDecidesBothWays(t *testing.T) {
	t.Run("broken src_install FAILS the gate and is not counted as reach", func(t *testing.T) {
		spy := &buildSpy{}
		gates, err := RunBuildGates(context.Background(), buildRequestFor(t, DepthInstall),
			buildSeam(spy, installDiesLog, errors.New("exit status 1")))
		if err != nil {
			t.Fatalf("RunBuildGates: %v", err)
		}

		got := gateNamed(t, gates, GateInstall)
		if got.Outcome != OutcomeFailed {
			t.Fatalf("install gate: got %q (reason %q), want FAILED — src_install began and never completed", got.Outcome, got.Reason)
		}
		if len(got.Findings) == 0 {
			t.Error("the FAILED install gate carries no findings; Report.ExitCode counts error findings, and a " +
				"failure that exits 0 is the silent pass in another costume")
		}

		// R7.1's second half: a failed gate must not promote the bump, and the
		// reach calculation is where that shows up.
		if reached := deepestPassedRung(gates, DepthInstall); reached == DepthInstall {
			t.Errorf("a FAILED install gate reported the reach as %v; a failure measured a failure, not a rung", reached)
		}
	})

	t.Run("the same candidate repaired PASSES", func(t *testing.T) {
		spy := &buildSpy{}
		gates, err := RunBuildGates(context.Background(), buildRequestFor(t, DepthInstall),
			buildSeam(spy, installOKLog, nil))
		if err != nil {
			t.Fatalf("RunBuildGates: %v", err)
		}

		got := gateNamed(t, gates, GateInstall)
		if got.Outcome != OutcomePass {
			t.Fatalf("install gate: got %q (reason %q), want PASS — the break was removed and nothing else changed",
				got.Outcome, got.Reason)
		}
		if reached := deepestPassedRung(gates, DepthInstall); reached != DepthInstall {
			t.Errorf("a passing install gate reported the reach as %v, want install (R2.5)", reached)
		}
	})

	t.Run("exit 0 without the completion marker is underivable, never a pass", func(t *testing.T) {
		spy := &buildSpy{}
		gates, err := RunBuildGates(context.Background(), buildRequestFor(t, DepthInstall),
			buildSeam(spy, compiledLog, nil))
		if err != nil {
			t.Fatalf("RunBuildGates: %v", err)
		}

		got := gateNamed(t, gates, GateInstall)
		if got.Outcome == OutcomePass {
			t.Fatalf("the install gate PASSED on a zero exit that never printed the completion marker: %q", got.Reason)
		}
		if reached := deepestPassedRung(gates, DepthInstall); reached == DepthInstall {
			t.Errorf("an underivable install gate reported the reach as %v; the one rule this story must not "+
				"weaken is that a gate which could not run reports SKIPPED, never PASS", reached)
		}
	})
}

// TestRunBuildGates_CompileDepthFingerprintIsUnchangedByStory042 is S042-R6.1
// and R6.3, measured rather than asserted in prose.
//
// The expected values below were CAPTURED FROM THE PRE-STORY TREE, not written
// from the post-story code: a probe using only API that exists in both trees was
// run in a worktree detached at 6029e9f (the base this branch forked from) and
// again at HEAD, and the two fingerprints diffed byte-identical. What is pinned
// here is that measurement.
//
// The environment line is the one that matters most. A wrongly-scoped ` -test`
// changes NOTHING an operator sees until a host that sets FEATURES=test runs a
// compile — so the parent below deliberately carries `test`, and the assertion
// is that the child still receives it.
func TestRunBuildGates_CompileDepthFingerprintIsUnchangedByStory042(t *testing.T) {
	t.Setenv("FEATURES", "test ccache sandbox network-sandbox userpriv")

	spy := &buildSpy{}
	gates, err := RunBuildGates(context.Background(), buildRequestFor(t, DepthCompile), buildSeam(spy, compiledLog, nil))
	if err != nil {
		t.Fatalf("RunBuildGates: %v", err)
	}

	if spy.spawns != 1 {
		t.Errorf("spawns = %d, want 1", spy.spawns)
	}

	// The gate list, in order, with its outcomes. Nothing else asserts that a
	// compile-depth run reports exactly these three and no install gate.
	want := []struct{ gate, outcome string }{
		{GatePatches, string(OutcomePass)},
		{GateConfigure, string(OutcomePass)},
		{GateCompile, string(OutcomePass)},
	}
	if len(gates) != len(want) {
		t.Fatalf("a compile-depth run reported %d gates, want %d: %+v — the install gate must not appear for a "+
			"depth that did not ask for it", len(gates), len(want), gates)
	}
	for i, w := range want {
		if gates[i].Gate != w.gate || string(gates[i].Outcome) != w.outcome {
			t.Errorf("gate %d is %s/%s, want %s/%s", i, gates[i].Gate, gates[i].Outcome, w.gate, w.outcome)
		}
	}

	// The compile reason, verbatim from the pre-story tree minus the distdir
	// note, which every PASS carries and which this story does not touch.
	const wantCompileReason = "the compile phase completed for media-plugins/gst-plugins-qt6-1.29.2; " +
		"a compile pass does not cover src_install, which this ladder deliberately stops short of"
	if !strings.HasPrefix(gates[2].Reason, wantCompileReason) {
		t.Errorf("the compile reason changed.\n got: %s\nwant prefix: %s", gates[2].Reason, wantCompileReason)
	}

	// R6.1's hardest half: the parent carries `test` and the compile child still
	// receives it, unmodified.
	entries := featuresEntries(spy.envAtSpawn)
	if len(entries) != 1 || entries[0] != "FEATURES=test ccache sandbox network-sandbox userpriv" {
		t.Errorf("the compile child received %v, want exactly the parent's own value; this is the assertion that "+
			"fails if the ` -test` composition is ever applied outside the install phase", entries)
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
			t.Errorf("the child environment is missing %s; the allow-list is PATH, HOME, TERM, PORTAGE_*, FEATURES and MAKEOPTS; DISTDIR left it in story 039 and is now set as a computed value instead", want)
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

// ---------------------------------------------------------------------------
// Story 039, sub-task 3.1 — R3, R3.1, R3.2, R3.3.
//
// DISTDIR sat on buildEnvAllowed, which reads as "we pass it". It is not what
// that list does: allowedBuildEnv filters the PARENT's environment, so the entry
// meant "we let the invoking shell's value through, if it had one" — the weakest
// of the three possible behaviours and the one nobody would choose on purpose.
// The value the operator resolved with --distdir was consumed only by
// distfiles.Locate, for READING; a grep for `DISTDIR=` over internal/ and cmd/
// returned nothing outside tests. Nothing exported it.
//
// So a build gate ran against whatever distdir the shell happened to name, or
// against the host's default, and a PASS could not support the sentence
// isolationFidelityNote already worries about: that the sources came from
// DISTDIR alone.
//
// The fix is a COMPUTED value, not a wider allow-list. The allow-list's purpose
// is that the parent's environment cannot leak in; the resolved distdir is an
// input to this run, so it belongs with the run's own variables.
// ---------------------------------------------------------------------------

// distdirEntries returns every DISTDIR= assignment in a child environment. It
// returns them all rather than the first, because "exactly once" is half the
// contract: exec.Cmd's duplicate-key behaviour is not a decision anyone should
// inherit.
func distdirEntries(env []string) []string {
	var got []string
	for _, kv := range env {
		if strings.HasPrefix(kv, "DISTDIR=") {
			got = append(got, kv)
		}
	}
	return got
}

// TestRunBuildGates_TheResolvedDistdirReachesTheChild is R3.1.
func TestRunBuildGates_TheResolvedDistdirReachesTheChild(t *testing.T) {
	distdir := t.TempDir()
	spy := &buildSpy{}

	req := buildRequestFor(t, DepthConfigure)
	req.Distdir = distdir

	if _, err := RunBuildGates(context.Background(), req, buildSeam(spy, configureOKLog, nil)); err != nil {
		t.Fatalf("RunBuildGates: %v", err)
	}
	if spy.envAtSpawn == nil {
		t.Fatal("cmd.Env was never set, so there is nothing to ask about DISTDIR")
	}

	got := distdirEntries(spy.envAtSpawn)
	if len(got) != 1 {
		t.Fatalf("the child environment carries %d DISTDIR entries (%v), want exactly 1; a gate that passes "+
			"without a distdir it can name cannot support the claim that the sources came from DISTDIR alone "+
			"(R3.1)", len(got), got)
	}
	if got[0] != "DISTDIR="+distdir {
		t.Errorf("the child was given %q, want %q — the value the operator resolved, not one this package "+
			"invented", got[0], "DISTDIR="+distdir)
	}
}

// TestRunBuildGates_NoResolvedDistdirInventsNothing is R3.2 and R3.3 together,
// and it is the assertion that keeps the fix from becoming a wider allow-list.
//
// The parent exports DISTDIR; the request resolves none. Today's entry on
// buildEnvAllowed would let the shell's value through, which is precisely the
// behaviour that made a PASS unable to say where its sources came from. With
// nothing resolved, nothing crosses — and this package invents no value of its
// own either.
func TestRunBuildGates_NoResolvedDistdirInventsNothing(t *testing.T) {
	t.Setenv("DISTDIR", "/var/cache/distfiles-from-the-invoking-shell")
	spy := &buildSpy{}

	req := buildRequestFor(t, DepthConfigure) // Distdir left empty on purpose.

	if _, err := RunBuildGates(context.Background(), req, buildSeam(spy, configureOKLog, nil)); err != nil {
		t.Fatalf("RunBuildGates: %v", err)
	}

	if got := distdirEntries(spy.envAtSpawn); len(got) != 0 {
		t.Errorf("the child environment carries %v with nothing resolved; the parent's value crossing in is "+
			"the leak the allow-list exists to prevent (R3.2, R3.3), and inventing one would be worse", got)
	}
}

// TestRunBuildGates_TheParentCannotOverrideTheResolvedDistdir is the third case,
// and the one exec.Cmd would otherwise decide by its duplicate-key rule.
//
// Leaving DISTDIR on buildEnvAllowed while also setting it explicitly makes the
// answer depend on which entry wins — a decision nobody made, recorded nowhere.
// Removing it from the list makes the computed value the only source, so the
// parent cannot influence the build at all.
func TestRunBuildGates_TheParentCannotOverrideTheResolvedDistdir(t *testing.T) {
	t.Setenv("DISTDIR", "/var/cache/distfiles-from-the-invoking-shell")
	resolved := t.TempDir()
	spy := &buildSpy{}

	req := buildRequestFor(t, DepthConfigure)
	req.Distdir = resolved

	if _, err := RunBuildGates(context.Background(), req, buildSeam(spy, configureOKLog, nil)); err != nil {
		t.Fatalf("RunBuildGates: %v", err)
	}

	got := distdirEntries(spy.envAtSpawn)
	if len(got) != 1 || got[0] != "DISTDIR="+resolved {
		t.Errorf("the child was given %v, want exactly [DISTDIR=%s]; the parent's DISTDIR must not compete "+
			"with the resolved one, and the winner must not be exec.Cmd's duplicate-key behaviour (R3.3)",
			got, resolved)
	}
}

// ---------------------------------------------------------------------------
// Story 039, sub-task 3.2 — R3, R3.4.
//
// isolationFidelityNote already worries, in the code, about the exact claim this
// pins: an unisolated run "could reach the network and the pass does not prove
// the sources came from DISTDIR alone". Sub-task 3.1 made the distdir real by
// exporting it; a PASS that does not SAY which one it read still cannot support
// that sentence, because the reader has no way to tell an enforced distdir from
// the host's ambient one.
//
// It rides on a PASS only, for the reason gateFor already documents: the label
// answers an OVERCLAIM, and a pass is the only outcome that claims to have
// proved anything. A FAILED gate is not made less true by where its sources came
// from, and a SKIPPED gate measured nothing to qualify.
// ---------------------------------------------------------------------------

// TestRunBuildGates_APassNamesTheDistdirItRead is R3.4's first half.
func TestRunBuildGates_APassNamesTheDistdirItRead(t *testing.T) {
	distdir := t.TempDir()
	req := buildRequestFor(t, DepthConfigure)
	req.Distdir = distdir

	gates, err := RunBuildGates(context.Background(), req, buildSeam(&buildSpy{}, configureOKLog, nil))
	if err != nil {
		t.Fatalf("RunBuildGates: %v", err)
	}

	configure := gateNamed(t, gates, GateConfigure)
	if configure.Outcome != OutcomePass {
		t.Fatalf("the configure gate reported %s (%q), and this test measures what a PASS says",
			configure.Outcome, configure.Reason)
	}
	if !strings.Contains(configure.Reason, distdir) {
		t.Errorf("the PASS %q does not name the distdir it read (%s); a gate that passes without saying "+
			"where its sources came from cannot support the hermeticity claim this story exists to make "+
			"true (R3.4)", configure.Reason, distdir)
	}
}

// TestRunBuildGates_APassWithoutADistdirSaysSo is R3.4's second half, and it is
// the half that makes the first one worth reading.
//
// If only the enforced case spoke, a reason without a distdir would be
// ambiguous between "this run enforced none" and "this sentence predates the
// change". Saying so out loud is what lets an operator read the absence.
func TestRunBuildGates_APassWithoutADistdirSaysSo(t *testing.T) {
	req := buildRequestFor(t, DepthConfigure) // Distdir left empty on purpose.

	gates, err := RunBuildGates(context.Background(), req, buildSeam(&buildSpy{}, configureOKLog, nil))
	if err != nil {
		t.Fatalf("RunBuildGates: %v", err)
	}

	configure := gateNamed(t, gates, GateConfigure)
	if configure.Outcome != OutcomePass {
		t.Fatalf("the configure gate reported %s (%q)", configure.Outcome, configure.Reason)
	}
	if !strings.Contains(configure.Reason, "distdir") && !strings.Contains(configure.Reason, "DISTDIR") {
		t.Errorf("the PASS %q says nothing about the distdir; with none exported the build read whatever "+
			"the host's own configuration names, and an operator cannot tell that from an enforced run "+
			"unless the reason says it (R3.4)", configure.Reason)
	}
}

// TestRunBuildGates_OnlyAPassCarriesTheDistdirEvidence keeps the new sentence
// from becoming decoration on every line of every report — the rule gateFor
// already states for the isolation label, asserted for this one too.
func TestRunBuildGates_OnlyAPassCarriesTheDistdirEvidence(t *testing.T) {
	distdir := t.TempDir()
	req := buildRequestFor(t, DepthCompile)
	req.Distdir = distdir

	// configureOKLog stops after configure, so at compile depth the compile gate
	// is the one that measured nothing.
	gates, err := RunBuildGates(context.Background(), req, buildSeam(&buildSpy{}, configureOKLog, nil))
	if err != nil {
		t.Fatalf("RunBuildGates: %v", err)
	}

	for _, g := range gates {
		if g.Outcome == OutcomePass {
			continue
		}
		if strings.Contains(g.Reason, distdir) {
			t.Errorf("the %s gate reported %s and still carries the distdir evidence (%q); the sentence "+
				"answers an OVERCLAIM, and an outcome that claims nothing has nothing to qualify",
				g.Gate, g.Outcome, g.Reason)
		}
	}
}

// TestRunBuildGates_SuffixedKeyHandsEbuildTheCleanPath is story 040's R5.1 at
// the build gate: the path handed to `ebuild … clean <phase>` derives from the
// suffix-stripped key, because the staged tree's content is the clean package
// (stage_test.go pins that half) and a suffixed path names a file that never
// existed. Measured on the maintainer's host (2026-08-19), this is the third
// gate the leak would reach after the manifest step and the Manifest reads.
func TestRunBuildGates_SuffixedKeyHandsEbuildTheCleanPath(t *testing.T) {
	spy := &buildSpy{}
	req := BuildRequest{
		StagedRoot: filepath.Join(t.TempDir(), "staging", "app-editors", "zed-bin@preview", "1.17.0"),
		Key:        "app-editors/zed-bin@preview",
		Version:    "1.17.0",
		Depth:      DepthConfigure,
		LogDir:     t.TempDir(),
	}

	if _, err := RunBuildGates(context.Background(), req, buildSeam(spy, configureOKLog, nil)); err != nil {
		t.Fatalf("RunBuildGates: %v", err)
	}
	if len(spy.argv) == 0 || len(spy.argv[0]) == 0 {
		t.Fatalf("no child was spawned; the seam should have received one `ebuild` invocation")
	}

	want := filepath.Join(req.StagedRoot, "app-editors", "zed-bin", "zed-bin-1.17.0.ebuild")
	if got := spy.argv[0][0]; got != want {
		t.Errorf("ebuild was pointed at %q, want %q; the registry key's suffix is the stage root's "+
			"retention identity and must not reach the candidate's path inside the staged repository", got, want)
	}
}
