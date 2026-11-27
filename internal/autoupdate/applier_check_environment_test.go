package autoupdate

import (
	"fmt"
	"testing"

	"github.com/obentoo/bentoolkit/internal/autoupdate/validate"
	"github.com/obentoo/bentoolkit/internal/common/report"
)

// TestAnEnvironmentBuildFailureEndsWait: edk2 202611's configure died in
// pkg_setup on a signing key only root can read (2026-11-23, again in the
// repair batch of 2026-11-26). The check, and so a repair's re-validation,
// declines the build gates over the host, and the verdict is WAIT, not HUMAN:
// only this machine stands in the way.
func TestAnEnvironmentBuildFailureEndsWait(t *testing.T) {
	buildErr := fmt.Errorf("sys-firmware/edk2-202611: compile test failed: exit status 1 (%w: the build died before src_prepare began)", ErrBuildEnvironment)
	gates := appendBuildGates(nil, nil, buildErr, "sys-firmware/edk2", "202611", validate.DepthConfigure)
	if len(gates) == 0 {
		t.Fatal("no gate was reported for the failed build")
	}
	for _, g := range gates {
		if g.Outcome == validate.OutcomeFailed || g.Declined != validate.DeclineHost {
			t.Errorf("gate %s = %s declined %q, want SKIPPED over the host", g.Gate, g.Outcome, g.Declined)
		}
	}
	facts := validate.ReportGateFacts(gates, validate.DepthConfigure.String())
	if state := report.StateOf(report.Classify(false, facts), facts); state != report.Wait {
		t.Errorf("the verdict is %s, want %s", state, report.Wait)
	}
}
