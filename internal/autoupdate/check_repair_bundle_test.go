package autoupdate

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/obentoo/bentoolkit/internal/autoupdate/fixer"
	"github.com/obentoo/bentoolkit/internal/autoupdate/repair"
	"github.com/obentoo/bentoolkit/internal/autoupdate/validate"
)

// TestCheck_AHumanBumpLeavesARepairBundle: since apply only publishes, the
// check is where a bump fails, so it leaves the bundle --repair works from —
// with the stage that failed and the fixer session to resume.
func TestCheck_AHumanBumpLeavesARepairBundle(t *testing.T) {
	spy := &buildFixSpy{result: fixer.BuildFixResult{SessionID: "sess-42"}, err: errors.New("the agent gave up")}
	h := buildFixFixture(t, spy, buildRun{output: preparedThenConfigureFails, err: errors.New("exit status 1")})

	res := h.applier.Validate(t.Context(), h.pkg, validate.DepthNone)

	if worst := res.WorstOutcome(); worst != validate.OutcomeFailed {
		t.Fatalf("worst outcome %s, want a failed gate", worst)
	}
	bundles, _ := filepath.Glob(filepath.Join(h.applier.StagingRoot(), "*", "*", "*", repair.BundleFile))
	if len(bundles) != 1 {
		t.Fatalf("want one repair bundle beside the kept tree, found %v", bundles)
	}
	b, err := repair.ReadBundle(bundles[0])
	if err != nil {
		t.Fatal(err)
	}
	if b.Stage != "gate:"+validate.GateConfigure || b.FixerSession != "sess-42" || b.Candidate != "1.29.2" || b.Failure == "" {
		t.Errorf("bundle %+v; want stage gate:configure, the fixer session, the candidate and the failure", b)
	}
	if !b.RepairedAt.IsZero() {
		t.Error("a check's own bundle carries the repair mark; only a --repair session's does")
	}
	if !strings.HasPrefix(b.EbuildPath, filepath.Dir(bundles[0])) {
		t.Errorf("ebuild path %s is not inside the kept tree", b.EbuildPath)
	}
}

// TestCheck_AReadyBumpLeavesNoBundle: nothing to repair, nothing written.
func TestCheck_AReadyBumpLeavesNoBundle(t *testing.T) {
	h := buildFixFixture(t, &buildFixSpy{}, buildRun{output: cleanCompile})

	h.applier.Validate(t.Context(), h.pkg, validate.DepthNone)

	bundles, _ := filepath.Glob(filepath.Join(h.applier.StagingRoot(), "*", "*", "*", repair.BundleFile))
	if len(bundles) != 0 {
		t.Errorf("a passing check wrote %v", bundles)
	}
}
