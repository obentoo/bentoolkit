package main

// Authored for story 046, sub-task 12.1 — R3.7.
//
// The rule: "IF the render mode is requested through the environment or the
// configuration with a value outside the accepted set, THEN THE SYSTEM SHALL
// render the report in plain and state the source, the value it refused and the
// mode it used instead."
//
// Three facts, and the run survives. The run surviving is already true; the
// three facts are stated to nobody. Measured on a binary built from HEAD, in an
// isolated home with a seeded overlay:
//
//	BENTOO_UI=bogus bentoo overlay manifest --dry-run
//	  → exit 0, the report renders in plain, and stderr carries only
//	    "Regenerating Manifest for 2 package(s)"
//	bentoo overlay manifest --dry-run --ui=bogus
//	  → exit 1, no report, and stderr carries the sentence
//
// The message R3.7 asks for is not missing — it is BUILT and then thrown away.
// parseMode already produces `BENTOO_UI: "bogus" is not a UI mode; the accepted
// values are auto, plain, inline or fullscreen`, naming two of the three facts,
// and both presenters hand it to logger.Debug. The default level is LevelInfo,
// so the operator never sees it. The third fact — the mode used instead — is
// the one no existing string carries.
//
// # Why these tests run in-process and 11.1's had to build a binary
//
// 11.1 was about a sentence printed TWICE, once by cobra and once by main.go,
// and main() is the seam testCLI cannot enter. This is about a sentence written
// to file descriptor 2 by the logger, and captureStream redirects that
// descriptor — testcli_test.go documents exactly this, and the logger's
// once-captured os.Stderr is the case it was written for. In-process is also
// the only way to reach the SECOND producer: `snapshot run` needs btrbk and a
// subvolume from a real binary, and a MockRunner from here.
//
// The one claim that is genuinely about a process — that an explicit --ui still
// stops the run before it does any work — is asserted through the real binary,
// with 11.1's buildBentoo, because an exit status read from the harness's
// error path is not the same evidence as an exit status read from a process.
//
// # What is Red here, and what is deliberately green
//
// Red on arrival: the four tests that ask for the sentence — over the manifest
// producer, over the snapshot producer, over the configured source, and against
// the downgrade line it must not be mistaken for.
//
// Green on arrival, on purpose: TestAmbientModeRefusalDoesNotSoftenAnExplicitFlag.
// It pins the half that must NOT move. R3.7 and R3.2 sit on the same value
// ("bogus") arriving from two places, and the two answers are opposite: refused
// from --ui the run stops, refused from the environment the run finishes. A fix
// that reached R3.7 by rejecting ambient values at the root would satisfy every
// other test in this file and break `bentoo version` on a host whose shell
// profile has a typo — which is the exact thing root.go:66 says it will not do.

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/obentoo/bentoolkit/internal/common/logger"
	"github.com/obentoo/bentoolkit/internal/snapshot"
)

// init pins the default logger to the real stderr before any test has moved it.
//
// # Without it these tests read an empty stream and would stay red after the fix
//
// logger.Default builds its logger once, under a sync.Once, and keeps whatever
// os.Stderr WAS at that moment. captureStream does two things per run: it
// redirects file descriptor 2 into a pipe, and it swaps the os.Stderr variable
// to that pipe's writer. Both are undone when the run ends, and the pipe is
// closed.
//
// So the once firing inside a captured run is a trap: the logger keeps that
// run's writer, whose descriptor is closed moments later, and every subsequent
// run's logger output is written to a closed pipe and dropped without an error
// anyone sees. Measured before this line existed — two identical runs in one
// test, the second one silent:
//
//	run1 stderr="Regenerating Manifest for 2 package(s)\n"
//	run2 stderr=""
//
// Firing it here leaves the logger holding descriptor 2, which captureStream
// redirects per run — the arrangement testcli_test.go's own comment describes,
// and the only one under which logger output is observable from more than the
// first captured run in the binary.
//
// It is init() and not a line in each test on purpose: the once is process-wide,
// so a test that pins it after some other file's captured run has already fired
// it is pinning nothing. Package initialisation is the only point that is
// guaranteed to come first.
func init() { logger.Default() }

// fallbackMarkers are the ways a sentence can say "and so I used this one
// instead". The set is generous on wording and strict on substance: naming the
// mode is not enough, because the message that exists TODAY already contains
// the word "plain" — it lists it among the accepted values — while saying
// nothing about what this run actually rendered in.
var fallbackMarkers = []string{
	"instead", "fall back", "falling back", "fell back",
	"renders in plain", "rendering in plain", "rendered in plain", "using plain",
}

// statesTheFallback answers R3.7's third fact: does this stream say which mode
// the run used, as opposed to which values it would have accepted?
//
// The refused value is REMOVED before the question is asked. A value like
// "plainish" contains the fallback's own name, so a message that merely echoed
// what it refused would otherwise be read as naming the mode it chose — the
// test passing on a sentence that answers two of the three facts.
func statesTheFallback(stderr, refusedValue string) bool {
	stripped := stderr
	if refusedValue != "" {
		stripped = strings.ReplaceAll(stripped, refusedValue, "")
	}
	if !strings.Contains(stripped, "plain") {
		return false
	}
	for _, marker := range fallbackMarkers {
		if strings.Contains(stripped, marker) {
			return true
		}
	}
	return false
}

// manifestUnderAmbientMode runs `overlay manifest --dry-run` over a seeded
// overlay with BENTOO_UI set to uiEnv, and no --ui flag anywhere.
//
// --dry-run is what makes this reachable without pkgdev or the network, and
// --ui is deliberately absent: the flag is the OTHER source, and passing it
// would answer the question before the run started.
func manifestUnderAmbientMode(t *testing.T, uiEnv string) (stdout, stderr string, code int) {
	t.Helper()

	c := newTestCLI(t)
	for _, pkg := range []string{"app-misc/jq", "dev-lang/go"} {
		writeExitTestEbuild(t, c.Overlay(), pkg, "1.0.0")
	}
	t.Setenv("BENTOO_UI", uiEnv)
	stubUIIsTerminal(c.deps, false)

	stdout, stderr, code = c.Run("overlay", "manifest", "--dry-run")
	if strings.Contains(stderr, "unknown flag") {
		t.Fatalf("a flag was rejected: %s", stderr)
	}
	return stdout, stderr, code
}

// TestAmbientModeRefusalKeepsTheRunAndStatesItself is R3.7 whole, on the first
// of the two producers.
//
// The control run is not decoration. "Exits with the status it would have had"
// and "renders in plain" are both claims about SAMENESS, and a hard-coded 0 or
// an eyeballed absence of escape sequences would assert neither: what has to
// hold is that the run with the bad value produced the same status and the same
// report as the run without it. Only stderr may differ, and only by gaining a
// sentence.
func TestAmbientModeRefusalKeepsTheRunAndStatesItself(t *testing.T) {
	controlOut, controlErr, controlCode := manifestUnderAmbientMode(t, "")

	// The harness must be able to SEE a logger write before the absence of one
	// means anything. logger.Info goes to the same stream by the same route as
	// the logger.Warn this test is waiting for, so if this line is missing the
	// failure below would be the capture and not the code.
	if !strings.Contains(controlErr, "Regenerating Manifest") {
		t.Fatalf("the harness captured no logger output at all on the control run — a missing sentence here would prove nothing about the code.\n    stderr: %q", controlErr)
	}

	stdout, stderr, code := manifestUnderAmbientMode(t, "bogus")

	if code != controlCode {
		t.Errorf("the run exited %d with BENTOO_UI=bogus and %d without it — R3.7 degrades the render, it does not fail the run", code, controlCode)
	}
	if strings.TrimSpace(stdout) != strings.TrimSpace(controlOut) {
		t.Errorf("the report differs from the one the same run produces with no BENTOO_UI set — R3.7 asks for the report in plain, not for a different report.\n--- with BENTOO_UI=bogus ---\n%s\n--- control ---\n%s", stdout, controlOut)
	}
	if strings.TrimSpace(stdout) == "" {
		t.Fatal("the run produced no report at all (R3.7)")
	}

	if !strings.Contains(stderr, "BENTOO_UI") {
		t.Errorf("stderr does not name the SOURCE the refused value came from (R3.7).\n"+
			"    Remedy: parseMode already names it; presentManifestReport hands its error to\n"+
			"    logger.Debug, which is below the default LevelInfo and therefore invisible.\n"+
			"    Observed stderr: %q", stderr)
	}
	if !strings.Contains(stderr, "bogus") {
		t.Errorf("stderr does not name the VALUE that was refused (R3.7).\n    Observed stderr: %q", stderr)
	}
	if !statesTheFallback(stderr, "bogus") {
		t.Errorf("stderr does not say which mode the run used INSTEAD (R3.7).\n"+
			"    This is the fact no existing string carries: parseMode's message lists plain\n"+
			"    among the values it would have accepted, which is not the same as saying the\n"+
			"    report in front of you was rendered in it.\n"+
			"    Observed stderr: %q", stderr)
	}
}

// snapshotUnderAmbientMode runs `snapshot run` over the mocked pipeline with
// BENTOO_UI set to uiEnv and no --ui flag.
//
// The seam is snapshot_run_test.go's: a MockRunner in place of btrbk, stubbed
// binaries on PATH and a redirected state directory, so no subvolume is touched.
func snapshotUnderAmbientMode(t *testing.T, uiEnv string) (stdout, stderr string, code int) {
	t.Helper()

	c := newTestCLI(t)
	stubBinariesOnPath(t, "btrbk", "ssh")
	_, configPath := writeSnapshotConfig(t, validSnapshotTOML)
	redirectStateDir(t)

	c.deps.snapshotRunner = &snapshot.MockRunner{}

	t.Setenv("BENTOO_UI", uiEnv)
	stubUIIsTerminal(c.deps, false)

	stdout, stderr, code = c.Run("snapshot", "--config="+configPath, "run")
	if strings.Contains(stderr, "unknown flag") {
		t.Fatalf("a flag was rejected: %s", stderr)
	}
	return stdout, stderr, code
}

// TestAmbientModeRefusalReachesTheSnapshotProducerToo is the half a fix applied
// to one file would leave open.
//
// presentManifestReport and presentSnapshotReport carry the same four lines and
// the same doc comment, so the cheapest fix touches one of them — and the
// operator whose typo cost them the fullscreen report on `snapshot run` gets the
// same silence as before, from code that now claims to state its refusals.
// R3.7 is a rule about reports, not about one command's file.
func TestAmbientModeRefusalReachesTheSnapshotProducerToo(t *testing.T) {
	controlOut, _, controlCode := snapshotUnderAmbientMode(t, "")
	if strings.TrimSpace(controlOut) == "" {
		t.Fatal("the control `snapshot run` produced no report — the fixture is not driving the producer this test is about")
	}

	stdout, stderr, code := snapshotUnderAmbientMode(t, "bogus")

	if code != controlCode {
		t.Errorf("`snapshot run` exited %d with BENTOO_UI=bogus and %d without it — a refused ambient mode degrades the render, it does not fail the run", code, controlCode)
	}
	if strings.TrimSpace(stdout) == "" {
		t.Fatal("`snapshot run` produced no report with BENTOO_UI=bogus (R3.7)")
	}
	if !strings.Contains(stderr, "BENTOO_UI") || !strings.Contains(stderr, "bogus") {
		t.Errorf("the second producer does not state the source and the value it refused (R3.7).\n"+
			"    snapshot_report.go:288 is overlay_manifest_report.go:226 with one word changed;\n"+
			"    a fix that reaches only the first leaves this run as silent as it is today.\n"+
			"    Observed stderr: %q", stderr)
	}
	if !statesTheFallback(stderr, "bogus") {
		t.Errorf("the second producer does not say which mode it used instead (R3.7).\n    Observed stderr: %q", stderr)
	}
}

// TestAmbientModeRefusalCoversTheConfiguredSourceToo is the other converse.
//
// R3.7 names two ambient sources — "the environment or the configuration" — and
// a fix driven by the measurement that motivated it (`BENTOO_UI=bogus`) can
// satisfy every environment-shaped test while `ui.mode` stays silent. The two
// sources reach parseMode through the same call and must come out of it
// together; what must NOT come out together is which one is named, because
// "which of my three places is wrong" is the only question the operator has.
func TestAmbientModeRefusalCoversTheConfiguredSourceToo(t *testing.T) {
	c := newTestCLI(t)
	for _, pkg := range []string{"app-misc/jq", "dev-lang/go"} {
		writeExitTestEbuild(t, c.Overlay(), pkg, "1.0.0")
	}
	stubUIIsTerminal(c.deps, false)

	configPath := filepath.Join(c.Home(), ".config", "bentoo", "config.yaml")
	existing, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("reading the harness config at %s: %v", configPath, err)
	}
	if err := os.WriteFile(configPath, append(existing, []byte("ui:\n  mode: bogus\n")...), 0o644); err != nil {
		t.Fatalf("writing ui.mode into the harness config: %v", err)
	}

	stdout, stderr, code := c.Run("overlay", "manifest", "--dry-run")

	if code != 0 {
		t.Errorf("`overlay manifest --dry-run` exited %d with an unusable ui.mode — a configured typo degrades the render, it does not fail the run (stderr: %q)", code, stderr)
	}
	if strings.TrimSpace(stdout) == "" {
		t.Fatal("the run produced no report at all with an unusable ui.mode (R3.7)")
	}
	if !strings.Contains(stderr, "ui.mode") {
		t.Errorf("stderr does not name `ui.mode` as the source of the refused value (R3.7).\n"+
			"    The environment is not the only ambient source, and an operator told only that\n"+
			"    \"a UI mode\" was refused has three places to search.\n"+
			"    Observed stderr: %q", stderr)
	}
	if !strings.Contains(stderr, "bogus") {
		t.Errorf("stderr does not name the value refused from the configuration (R3.7).\n    Observed stderr: %q", stderr)
	}
	if !statesTheFallback(stderr, "bogus") {
		t.Errorf("stderr does not say which mode the run used instead of the configured one (R3.7).\n    Observed stderr: %q", stderr)
	}
}

// TestAmbientModeRefusalStaysApartFromTheDowngradeSentence is the third element.
//
// The two halves above are derived from R3.7's own text. This one cannot be:
// the sentence 12.1 writes is a string the FIX invents, and the requirement can
// only name the things it forbids. What it collides with is already on stderr.
//
// warnUIDowngrade has been logging, at Warn, on the same stream, for as long as
// --ui has existed: "fullscreen output needs an interactive terminal and stdout
// is not one, so this run renders in plain". A refusal worded as "the UI mode
// did not resolve, rendering in plain" reads identically to it — and the two
// mean opposite things to the person reading. One says the terminal cannot do
// what was asked and there is nothing to fix; the other says a value they typed
// is wrong and will keep being wrong on every run until they find it.
//
// The distinguishing facts are precisely the two R3.7 asks for and the downgrade
// line structurally cannot carry: a source and a value. So the assertion is not
// "the refusal mentions plain" — the downgrade does too — but that the refusal
// carries something no downgrade ever could.
func TestAmbientModeRefusalStaysApartFromTheDowngradeSentence(t *testing.T) {
	downgrade := manifestUnderDowngrade(t)
	if !strings.Contains(downgrade, "renders in plain") {
		t.Fatalf("the downgrade fixture did not produce the sentence it exists to produce — without it there is nothing to be told apart FROM.\n    Observed stderr: %q", downgrade)
	}
	if strings.Contains(downgrade, "BENTOO_UI") || strings.Contains(downgrade, "bogus") {
		t.Fatalf("the downgrade sentence already names a source or a value, so it is not the neighbour this test assumed.\n    Observed stderr: %q", downgrade)
	}

	_, refusal, _ := manifestUnderAmbientMode(t, "bogus")

	if refusal == downgrade {
		t.Fatalf("a refused BENTOO_UI and a terminal that cannot carry fullscreen produce byte-identical stderr — the operator cannot tell a typo they must fix from a device limit they cannot.\n    Observed: %q", refusal)
	}
	if !strings.Contains(refusal, "BENTOO_UI") || !strings.Contains(refusal, "bogus") {
		t.Errorf("the refusal carries nothing the downgrade line does not (R3.7).\n"+
			"    Both end in plain and both arrive at Warn on stderr; the source and the value\n"+
			"    are the only two facts that separate \"your profile has a typo\" from \"your\n"+
			"    terminal is a pipe\".\n"+
			"    --- refusal ---\n%q\n    --- downgrade ---\n%q", refusal, downgrade)
	}
}

// manifestUnderDowngrade produces the pre-existing plain-fallback sentence: a
// mode that IS accepted, on a stdout that cannot carry it.
//
// The two opt-out variables are cleared because they outrank everything —
// newTestCLI sets both, and with either one set ResolveMode returns plain
// before it ever decides that anything was downgraded. uiDowngradeReported is
// reset because it is process-wide and holds "at most once" across the whole
// test binary, so an earlier test in a full-suite run would otherwise swallow
// the line this fixture exists to produce.
func manifestUnderDowngrade(t *testing.T) (stderr string) {
	t.Helper()

	c := newTestCLI(t)
	for _, pkg := range []string{"app-misc/jq", "dev-lang/go"} {
		writeExitTestEbuild(t, c.Overlay(), pkg, "1.0.0")
	}
	t.Setenv("BENTOO_UI", "fullscreen")
	t.Setenv("BENTOO_NO_TUI", "")
	t.Setenv("NO_COLOR", "")
	stubUIIsTerminal(c.deps, false)

	was := uiDowngradeReported.Swap(false)
	t.Cleanup(func() { uiDowngradeReported.Store(was) })

	_, stderr, _ = c.Run("overlay", "manifest", "--dry-run")
	return stderr
}

// TestAmbientModeRefusalDoesNotSoftenAnExplicitFlag is the half that must not
// move, and it is expected to pass before 12.1 is implemented.
//
// One value, two sources, opposite answers: from --ui the run is refused before
// any work (R3.2), from the environment the run finishes and says so (R3.7). A
// fix that made the two agree would satisfy this file's other four tests — the
// sentence would be on stderr either way — and would break the promise root.go
// makes eleven lines above the check: a typo in a shell profile must not stop
// `bentoo version`. So the pair is asserted together, through the real binary,
// where an exit status is a process's and not a harness's reading of one.
func TestAmbientModeRefusalDoesNotSoftenAnExplicitFlag(t *testing.T) {
	bentoo := buildBentoo(t)

	c := newTestCLI(t)
	for _, pkg := range []string{"app-misc/jq", "dev-lang/go"} {
		writeExitTestEbuild(t, c.Overlay(), pkg, "1.0.0")
	}

	ambientOut, ambientErr, ambientCode := runBentoo(t, bentoo, "bogus", "overlay", "manifest", "--dry-run")
	if ambientCode != 0 {
		t.Errorf("BENTOO_UI=bogus made the run exit %d; an ambient value is refused, the run is not (stderr: %q)", ambientCode, ambientErr)
	}
	if !strings.Contains(ambientOut, "app-misc/jq") {
		t.Errorf("BENTOO_UI=bogus cost the operator the report (R3.7):\n%s", ambientOut)
	}

	explicitOut, explicitErr, explicitCode := runBentoo(t, bentoo, "", "overlay", "manifest", "--dry-run", "--ui=bogus")
	if explicitCode != 1 {
		t.Errorf("`--ui=bogus` exited %d, want 1 — R3.2 refuses the run before it does any work (stderr: %q)", explicitCode, explicitErr)
	}
	if strings.TrimSpace(explicitOut) != "" {
		t.Errorf("`--ui=bogus` produced output; R3.2's refusal happens BEFORE any work:\n%s", explicitOut)
	}
	for _, mode := range []string{"auto", "plain", "inline", "fullscreen"} {
		if !strings.Contains(explicitErr, mode) {
			t.Errorf("the explicit refusal no longer names %q; R3.2 requires the accepted set.\n    Observed: %s", mode, explicitErr)
		}
	}
}

// runBentoo executes the built binary with BENTOO_UI set to uiEnv, inheriting
// the isolated HOME the harness has already put in this process's environment.
//
// The variable is filtered out of the inherited environment before being added
// back, rather than appended and left to os/exec's de-duplication: a test whose
// meaning depends on which of two identical keys the runtime prefers is a test
// that reads differently on a runtime that changes its mind.
func runBentoo(t *testing.T, bin, uiEnv string, args ...string) (stdout, stderr string, code int) {
	t.Helper()

	env := make([]string, 0, len(os.Environ())+1)
	for _, entry := range os.Environ() {
		if strings.HasPrefix(entry, "BENTOO_UI=") {
			continue
		}
		env = append(env, entry)
	}
	env = append(env, "BENTOO_UI="+uiEnv)

	var out, errBuf strings.Builder
	cmd := exec.Command(bin, args...)
	cmd.Env = env
	cmd.Stdout = &out
	cmd.Stderr = &errBuf

	if err := cmd.Run(); err != nil {
		if !errors.As(err, new(*exec.ExitError)) {
			t.Fatalf("running %s %v: %v", bin, args, err)
		}
	}
	return out.String(), errBuf.String(), cmd.ProcessState.ExitCode()
}
