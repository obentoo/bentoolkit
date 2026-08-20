package autoupdate

// Authored for story 040, sub-task 2.3 — R2, R2.5.
//
// ONE WORD, TWO AMOUNTS OF EVIDENCE. `compileGateResult` hand-rolls its
// OutcomePass reason outside `buildRun.gateFor`, so a privileged `--compile`
// PASS carried neither the isolation label nor the distdir evidence that every
// `--depth compile` PASS has carried since stories 031 and 039. A reader could
// not tell an enforced build from an ambient one — which is exactly the claim
// isolationFidelityNote worries about in its own comment.
//
// THE SENTENCES ARE SPELLED OUT HERE LITERALLY, AND THAT IS NOT A LICENCE TO
// COPY THEM INTO THE PRODUCTION CODE. They are validate's, and R2.5 wants ONE
// copy of each: the gate must CALL the note-producing functions, so that a
// future rewording lands on both paths at once. Spelling them here is how the
// test states "byte-identical to the unprivileged path's for the same inputs"
// without the assertion being satisfiable by agreeing with itself.
//
// The gate deliberately does NOT route through `gateFor`: that function derives
// an outcome from phase markers in a transcript, and this path already knows its
// exit status, so routing it would re-derive an answer it was handed.

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/obentoo/bentoolkit/internal/autoupdate/validate"
)

// The three sentences validate produces, verbatim (validate/build.go:
// distdirEvidenceNote and isolationFidelityNote).
const (
	wantExportedDistdirNote = "; the gate exported DISTDIR=%s for this build, rather than inheriting one from the invoking shell"
	wantNoDistdirNote       = "; the gate exported no DISTDIR, so the build read whatever the host's own Portage configuration names"
	wantUnverifiedIsolation = "; this ran with unverified isolation, so the build could reach the network and the pass does not prove the sources came from DISTDIR alone"
)

// compileGateFixture builds the applier and the staged candidate the gate
// reports on. Nothing is spawned: compileGateResult only reads what the run
// already recorded.
func compileGateFixture(t *testing.T) (*Applier, candidatePaths) {
	t.Helper()
	tmp := t.TempDir()
	overlayDir := filepath.Join(tmp, "overlay")
	configDir := filepath.Join(tmp, "config")

	createTestEbuildFile(t, overlayDir, "media-plugins/gst-plugins-qt6", "1.28.6")

	applier, err := NewApplier(overlayDir, configDir,
		WithExecCommand(mockExecCommandSuccess),
		WithConfirmFunc(func(string) bool { return true }),
	)
	if err != nil {
		t.Fatalf("creating applier: %v", err)
	}

	stagedRoot := filepath.Join(tmp, "staging", "media-plugins", "gst-plugins-qt6", "1.29.2")
	return applier, candidatePaths{
		repoRoot:   stagedRoot,
		pkgDir:     filepath.Join(stagedRoot, "media-plugins", "gst-plugins-qt6"),
		ebuildPath: filepath.Join(stagedRoot, "media-plugins", "gst-plugins-qt6", "gst-plugins-qt6-1.29.2.ebuild"),
		staged:     true,
	}
}

// onlyGate returns the single gate the compile path reports, failing when the
// shape is anything else.
func onlyGate(t *testing.T, gates []validate.GateResult) validate.GateResult {
	t.Helper()
	if len(gates) != 1 {
		t.Fatalf("the compile path reported %d gates, want exactly one: %+v", len(gates), gates)
	}
	return gates[0]
}

// TestCompileGateResult_AnEnforcedPassNamesItsDistdirAndItsIsolation is R2.5:
// the privileged PASS carries the same two fidelity statements the unprivileged
// one carries, so one word cannot describe two different amounts of evidence.
func TestCompileGateResult_AnEnforcedPassNamesItsDistdirAndItsIsolation(t *testing.T) {
	applier, cand := compileGateFixture(t)
	const distdir = "/var/cache/distfiles-run"
	const probeReason = "unshare(CLONE_NEWNET): operation not permitted"

	gate := onlyGate(t, applier.compileGateResult(cand, "media-plugins/gst-plugins-qt6", "1.29.2", &ApplyResult{
		IsolationVerified:      false,
		IsolationReason:        probeReason,
		CompileDistdir:         distdir,
		CompileDistdirEnforced: true,
	}))

	if gate.Outcome != validate.OutcomePass {
		t.Fatalf("the gate reported %s, want PASS: %s", gate.Outcome, gate.Reason)
	}

	// The sentence the gate has always carried survives — this sub-task ADDS
	// fidelity, it does not reword the verdict.
	if !strings.Contains(gate.Reason, "a compile pass does not cover src_install") {
		t.Errorf("the PASS lost its own sentence: %q", gate.Reason)
	}

	wantDistdir := strings.Replace(wantExportedDistdirNote, "%s", distdir, 1)
	if !strings.Contains(gate.Reason, wantDistdir) {
		t.Errorf("the privileged PASS does not name the distdir it read.\n got: %q\nwant it to contain: %q\n"+
			"a pass that cannot say which directory it enforced cannot support the hermeticity its sibling claims",
			gate.Reason, wantDistdir)
	}
	if !strings.Contains(gate.Reason, wantUnverifiedIsolation) {
		t.Errorf("the privileged PASS does not state its isolation fidelity.\n got: %q\nwant it to contain: %q",
			gate.Reason, wantUnverifiedIsolation)
	}
	if !strings.Contains(gate.Reason, probeReason) {
		t.Errorf("the isolation note dropped the probe's own answer %q: %q", probeReason, gate.Reason)
	}
}

// TestCompileGateResult_AVerifiedPassCarriesNoIsolationLabel keeps the label a
// signal rather than noise: a sentence printed under every gate on every host is
// one nobody reads. The distdir evidence still speaks, because it speaks in
// BOTH directions by design.
func TestCompileGateResult_AVerifiedPassCarriesNoIsolationLabel(t *testing.T) {
	applier, cand := compileGateFixture(t)
	const distdir = "/var/cache/distfiles-run"

	gate := onlyGate(t, applier.compileGateResult(cand, "media-plugins/gst-plugins-qt6", "1.29.2", &ApplyResult{
		IsolationVerified:      true,
		CompileDistdir:         distdir,
		CompileDistdirEnforced: true,
	}))

	if strings.Contains(gate.Reason, "unverified isolation") {
		t.Errorf("a verified run is labelled unverified: %q", gate.Reason)
	}
	if !strings.Contains(gate.Reason, strings.Replace(wantExportedDistdirNote, "%s", distdir, 1)) {
		t.Errorf("the verified PASS still has to name its distdir: %q", gate.Reason)
	}
}

// TestCompileGateResult_APassThatResolvedNoDistdirSaysSo is the empty
// direction, and it is the load-bearing half: an operator cannot read an
// absence, so a run that enforced nothing says out loud that the build answered
// from the host's own configuration.
func TestCompileGateResult_APassThatResolvedNoDistdirSaysSo(t *testing.T) {
	applier, cand := compileGateFixture(t)

	gate := onlyGate(t, applier.compileGateResult(cand, "media-plugins/gst-plugins-qt6", "1.29.2", &ApplyResult{
		IsolationVerified: true,
	}))

	if !strings.Contains(gate.Reason, wantNoDistdirNote) {
		t.Errorf("a run that resolved no distdir does not say so.\n got: %q\nwant it to contain: %q",
			gate.Reason, wantNoDistdirNote)
	}
}

// TestCompileGateResult_AnUnenforceableDistdirIsReportedAsSuch is R2.3, and it
// is the whole reason the mechanism was measured rather than assumed. On a doas
// host the resolved directory cannot be carried — doas has no argument form for
// an environment assignment — and a PASS that stayed silent would imply a
// hermeticity the run never had. It must NOT claim the export.
func TestCompileGateResult_AnUnenforceableDistdirIsReportedAsSuch(t *testing.T) {
	applier, cand := compileGateFixture(t)
	const distdir = "/var/cache/distfiles-run"

	gate := onlyGate(t, applier.compileGateResult(cand, "media-plugins/gst-plugins-qt6", "1.29.2", &ApplyResult{
		IsolationVerified:      true,
		CompileDistdir:         distdir,
		CompileDistdirEnforced: false,
	}))

	if gate.Outcome != validate.OutcomePass {
		t.Fatalf("an unenforced distdir turned the gate into %s; the build ran and passed — what changed is "+
			"only what the pass may claim: %s", gate.Outcome, gate.Reason)
	}
	if strings.Contains(gate.Reason, strings.Replace(wantExportedDistdirNote, "%s", distdir, 1)) {
		t.Errorf("the PASS claims it exported a distdir the privilege tool never carried: %q", gate.Reason)
	}
	if !strings.Contains(gate.Reason, "could not be enforced") {
		t.Errorf("the PASS does not say the distdir could not be enforced.\n got: %q\n"+
			"R2.3: a pass that cannot support its hermeticity claim must not imply one", gate.Reason)
	}
	if !strings.Contains(gate.Reason, distdir) {
		t.Errorf("the refusal does not name the directory that could not be carried: %q", gate.Reason)
	}
}

// TestCompileGateResult_TheIsolationRefusedSkipIsUnchanged is the guard. That
// branch is a SKIPPED because the compile never ran, and a fidelity note about
// a build that did not happen would be a claim about nothing.
func TestCompileGateResult_TheIsolationRefusedSkipIsUnchanged(t *testing.T) {
	applier, cand := compileGateFixture(t)
	applier.requireIsolation = true

	gate := onlyGate(t, applier.compileGateResult(cand, "media-plugins/gst-plugins-qt6", "1.29.2", &ApplyResult{
		IsolationVerified: false,
		IsolationReason:   "unshare(CLONE_NEWNET): operation not permitted",
	}))

	if gate.Outcome != validate.OutcomeSkipped {
		t.Fatalf("the refused compile reported %s, want SKIPPED: %s", gate.Outcome, gate.Reason)
	}
	const want = "isolation was required for media-plugins/gst-plugins-qt6-1.29.2 and this host could not provide it, " +
		"so the compile gate did not run: unshare(CLONE_NEWNET): operation not permitted"
	if gate.Reason != want {
		t.Errorf("the refusal's sentence changed.\n got: %q\nwant: %q", gate.Reason, want)
	}
}
