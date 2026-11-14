package autoupdate

import (
	"strings"
	"testing"
	"time"

	"github.com/obentoo/bentoolkit/internal/autoupdate/validate"
)

// TestBuildBudget_TheRunCapsTheDepthThePlanCapped: the applier resolves the
// same capped depth the plan shows, for the check and for the apply alike.
func TestBuildBudget_TheRunCapsTheDepthThePlanCapped(t *testing.T) {
	install := validate.DepthInstall
	a := &Applier{validatePolicy: validate.DefaultDepthPolicy(), flagDepth: &install}
	WithApplierBuildBudget(validate.BuildBudget{Limit: time.Hour, History: map[string]validate.BuildEstimate{
		"www-client/chromium": {Average: 2 * time.Hour, Merges: 3},
	}})(a)

	if d := a.depthFor("www-client/chromium", "140.0.1", "140.0.2"); d.Depth != validate.DepthConfigure || !strings.Contains(d.Reason, "build budget") {
		t.Errorf("chromium = %s %q; want configure, capped by the budget", d.Depth, d.Reason)
	}
	if d := a.depthFor("app-misc/new", "1.0", "1.1"); d.Depth != validate.DepthConfigure || !strings.Contains(d.Reason, "no build history") {
		t.Errorf("a package with no history = %s %q; want configure, counted as expensive", d.Depth, d.Reason)
	}
	if d := (&Applier{validatePolicy: validate.DefaultDepthPolicy(), flagDepth: &install}).depthFor("app-misc/new", "1.0", "1.1"); d.Depth != validate.DepthInstall {
		t.Errorf("no budget capped the depth: %s %q", d.Depth, d.Reason)
	}
}
