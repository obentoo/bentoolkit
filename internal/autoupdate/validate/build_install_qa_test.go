package validate

import (
	"context"
	"errors"
	"testing"
)

// installQAFatalLog is src_install finishing and Portage's post-install QA
// then failing the phase: `>>> Completed installing` is printed by
// __dyn_install BEFORE install_qa_check runs, so the marker is present on a
// run that failed.
const installQAFatalLog = installOKLog +
	" * QA Notice: The following files contain writable and executable sections\n" +
	" * ERROR: media-plugins/gst-plugins-qt6-1.29.2::bentoo-staging failed (install phase):\n" +
	" *   install_qa_check failed\n"

// TestRunBuildGates_AFatalPostInstallQAIsNotAPass: a run that exits non-zero
// after the install marker did not pass the install gate, whatever marker it
// printed on the way.
func TestRunBuildGates_AFatalPostInstallQAIsNotAPass(t *testing.T) {
	spy := &buildSpy{}

	gates, err := RunBuildGates(context.Background(), buildRequestFor(t, DepthInstall),
		buildSeam(spy, installQAFatalLog, errors.New("exit status 1")))
	if err != nil {
		t.Fatalf("RunBuildGates: %v", err)
	}

	if got := gateNamed(t, gates, GateInstall); got.Outcome == OutcomePass {
		t.Fatalf("the install gate reported PASS on a run whose post-install QA failed the phase; reason %q", got.Reason)
	}
}
