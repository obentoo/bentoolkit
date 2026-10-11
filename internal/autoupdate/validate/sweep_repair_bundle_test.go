package validate

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestSweepStagedTrees_KeepsATreeCarryingARepairBundle: a recognised,
// out-of-scope tree whose record shows no FAILED gate is removed, unless it
// carries a repair bundle — then an operator repair is pending in it.
func TestSweepStagedTrees_KeepsATreeCarryingARepairBundle(t *testing.T) {
	overlay := t.TempDir()
	staging := t.TempDir()

	pending := sweepTree(t, staging, "dev-go/govulncheck", "1.9.0", OutcomePass)
	if err := os.WriteFile(filepath.Join(pending, RepairBundleFile), []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	control := sweepTree(t, staging, "media-libs/gst-plugins-base", "1.28.6", OutcomePass)
	unrecorded := sweepTree(t, staging, "dev-qt/qtbase", "6.9.1", "")
	if err := os.WriteFile(filepath.Join(unrecorded, RepairBundleFile), []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}

	report, err := SweepStagedTrees(SweepRequest{Overlay: overlay, StagingRoot: staging})
	if err != nil {
		t.Fatalf("SweepStagedTrees: %v", err)
	}
	if !sweepExists(t, pending) {
		t.Fatal("the sweep removed a tree carrying a repair bundle, and the pending repair with it")
	}
	if sweepExists(t, control) {
		t.Fatal("fixture: the out-of-scope tree without a bundle survived, so the bundle decided nothing")
	}
	for _, path := range []string{pending, unrecorded} {
		var reason string
		for _, k := range report.Kept {
			if k.Path == path {
				reason = k.Reason
			}
		}
		if !strings.Contains(reason, "repair bundle") {
			t.Errorf("kept %s with reason %q, want it to name the repair bundle", path, reason)
		}
	}
}
