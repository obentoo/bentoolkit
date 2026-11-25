package autoupdate

import (
	"errors"
	"testing"

	"github.com/obentoo/bentoolkit/internal/autoupdate/validate"
)

// TestValidate_AMachineFaultIsDeclinedOverTheHost is edk2 202611 on
// 2026-11-23: its build died before src_prepare (a signing key the portage
// user could not read), the attribution said so, and the check still reported
// a FAILED gate with no cause, so the bump read HUMAN. The check now declines
// the build gates over the host, which reads WAIT.
func TestValidate_AMachineFaultIsDeclinedOverTheHost(t *testing.T) {
	spy := &buildFixSpy{}
	h := buildFixFixture(t, spy, buildRun{output: failsDuringUnpack, err: errors.New("exit status 1")})
	WithApplierDepth(validate.DepthCompile)(h.applier)

	result := h.applier.Validate(t.Context(), h.pkg, validate.DepthNone)

	declined := 0
	for _, gate := range result.Gates {
		if gate.Outcome == validate.OutcomeFailed {
			t.Errorf("the %s gate reported FAILED for a fault the attribution put on the machine: %s", gate.Gate, gate.Reason)
		}
		if gate.Outcome == validate.OutcomeSkipped && gate.Declined == validate.DeclineHost {
			declined++
		}
	}
	if declined == 0 {
		t.Fatalf("no build gate was declined over the host: %+v", result.Gates)
	}
	if len(spy.calls) != 0 {
		t.Errorf("the fixer was invoked %d time(s) for a machine fault", len(spy.calls))
	}
}
