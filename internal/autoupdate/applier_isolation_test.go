package autoupdate

// Authored for story 031, sub-task 7.2 — R7, R7.2, R7.3, R7.4, R7.5.
//
// This is the honesty fix, and it is a change to SHIPPED behaviour, so the last
// test in this file matters as much as the first: the compile gate's existing
// conditions for success and failure must not move (R7.5). Everything this
// sub-task adds is a label and a skip.
//
// The defect being corrected was measured, not guessed. A synthetic ebuild whose
// src_compile probes outbound HTTP, run the way runCompile runs it, reported
// `network-sandbox` as effective INSIDE the phase while the network stayed
// reachable — unshare(CLONE_NEWNET) needs privilege, so the namespace is never
// created, and Portage says nothing. The gate then printed a plain green.
//
// This file pins three names design.md implies but does not spell out:
// the options `WithApplierIsolationProbe` and `WithApplierRequireIsolation`,
// and the result fields `ApplyResult.IsolationVerified` / `.IsolationReason`.
//
// createTestEbuildFile and mockExecCommandSuccess come from applier_test.go.
//
// Red is DEFERRED to Run mode: none of the three names exist yet.

import (
	"path/filepath"
	"testing"
)

// isolationFixture lays out an overlay and a pending bump ready to apply, and
// returns the applier built from opts plus the package atom.
func isolationFixture(t *testing.T, opts ...ApplierOption) (*Applier, string) {
	t.Helper()
	tmp := t.TempDir()
	overlayDir := filepath.Join(tmp, "overlay")
	configDir := filepath.Join(tmp, "config")
	const pkg = "media-plugins/gst-plugins-qt6"

	createTestEbuildFile(t, overlayDir, pkg, "1.28.6")

	pending, err := NewPendingList(configDir)
	if err != nil {
		t.Fatalf("creating pending list: %v", err)
	}
	pending.Add(PendingUpdate{
		Package:        pkg,
		CurrentVersion: "1.28.6",
		NewVersion:     "1.29.2",
		Status:         StatusPending,
	})

	base := []ApplierOption{
		WithApplierPendingList(pending),
		WithExecCommand(mockExecCommandSuccess),
	}
	applier, err := NewApplier(overlayDir, configDir, append(base, opts...)...)
	if err != nil {
		t.Fatalf("creating applier: %v", err)
	}
	return applier, pkg
}

// TestRunCompile_ProbeIsSkippedWhenNotCompiling pins that an apply without the
// compile step pays nothing for this change.
func TestRunCompile_ProbeIsSkippedWhenNotCompiling(t *testing.T) {
	var probed bool
	applier, pkg := isolationFixture(t,
		WithApplierIsolationProbe(func() (bool, string) { probed = true; return true, "" }),
	)

	if _, err := applier.Apply(t.Context(), pkg); err != nil {
		t.Fatalf("Apply: %v", err)
	}

	if probed {
		t.Error("the isolation probe ran on an apply that never compiles")
	}
}

func containsArg(args []string, want string) bool {
	for _, a := range args {
		if a == want {
			return true
		}
	}
	return false
}
