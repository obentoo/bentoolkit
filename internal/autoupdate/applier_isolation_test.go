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
	"context"
	"os/exec"
	"path/filepath"
	"strings"
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
		WithConfirmFunc(func(string) bool { return true }),
	}
	applier, err := NewApplier(overlayDir, configDir, append(base, opts...)...)
	if err != nil {
		t.Fatalf("creating applier: %v", err)
	}
	return applier, pkg
}

// TestRunCompile_UnverifiedIsolationIsNeverAPlainPass is R7.3, and it is the
// whole point. The compile succeeds; what changes is that the result can no
// longer claim isolation it never had.
func TestRunCompile_UnverifiedIsolationIsNeverAPlainPass(t *testing.T) {
	applier, pkg := isolationFixture(t,
		WithApplierIsolationProbe(func() (bool, string) {
			return false, "unshare(CLONE_NEWNET): operation not permitted"
		}),
	)

	result, err := applier.Apply(pkg, true)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}

	if !result.Success {
		t.Fatalf("Apply was not successful: %v", result.Error)
	}
	if result.IsolationVerified {
		t.Error("IsolationVerified is true although the probe denied the namespace")
	}
	if result.IsolationReason == "" {
		t.Error("IsolationReason is empty; a pass that cannot name its own fidelity is the bug this story removes")
	}
	if !strings.Contains(result.IsolationReason, "not permitted") {
		t.Errorf("IsolationReason %q does not carry the kernel's answer", result.IsolationReason)
	}
}

// TestRunCompile_VerifiedIsolationIsAPlainPass keeps the label from becoming
// noise on the hosts where the gate really is isolated.
func TestRunCompile_VerifiedIsolationIsAPlainPass(t *testing.T) {
	applier, pkg := isolationFixture(t,
		WithApplierIsolationProbe(func() (bool, string) { return true, "" }),
	)

	result, err := applier.Apply(pkg, true)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}

	if !result.IsolationVerified {
		t.Error("IsolationVerified is false although the probe granted the namespace")
	}
	if result.IsolationReason != "" {
		t.Errorf("IsolationReason is %q on a verified run; there is nothing to explain", result.IsolationReason)
	}
}

// TestRunCompile_RequireIsolationLeavesTheCompileUnrun is R7.4. Running an
// unisolated compile after the operator demanded isolation would produce
// exactly the meaningless green they asked to avoid, so the compile must not
// happen at all — asserted by watching the exec seam.
func TestRunCompile_RequireIsolationLeavesTheCompileUnrun(t *testing.T) {
	var ranEbuild bool
	spy := func(ctx context.Context, name string, arg ...string) *exec.Cmd {
		if name == "ebuild" || containsArg(arg, "compile") {
			ranEbuild = true
		}
		return mockExecCommandSuccess(ctx, name, arg...)
	}

	applier, pkg := isolationFixture(t,
		WithExecCommand(spy),
		WithApplierRequireIsolation(true),
		WithApplierIsolationProbe(func() (bool, string) {
			return false, "unshare(CLONE_NEWNET): operation not permitted"
		}),
	)

	result, err := applier.Apply(pkg, true)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}

	if ranEbuild {
		t.Error("the compile ran although isolation was required and unavailable")
	}
	if result.IsolationReason == "" {
		t.Error("the skip carries no reason")
	}
	if result.IsolationVerified {
		t.Error("IsolationVerified is true on a run that was skipped for lack of isolation")
	}
}

// TestRunCompile_ExistingFailureStillFails is R7.5, the Unchanged Behaviour
// guard. A story that adds a label must not quietly become a story that changes
// when the gate fails.
func TestRunCompile_ExistingFailureStillFails(t *testing.T) {
	failing := func(ctx context.Context, name string, arg ...string) *exec.Cmd {
		if name == "ebuild" || containsArg(arg, "compile") {
			return exec.CommandContext(ctx, "false")
		}
		return mockExecCommandSuccess(ctx, name, arg...)
	}

	applier, pkg := isolationFixture(t,
		WithExecCommand(failing),
		WithApplierIsolationProbe(func() (bool, string) { return true, "" }),
	)

	result, _ := applier.Apply(pkg, true)

	if result.Success {
		t.Error("a failing compile reported success; the gate's failure condition moved")
	}
	if result.Error == nil {
		t.Error("a failing compile carried no error")
	}
}

// TestRunCompile_ProbeIsSkippedWhenNotCompiling pins that an apply without the
// compile step pays nothing for this change.
func TestRunCompile_ProbeIsSkippedWhenNotCompiling(t *testing.T) {
	var probed bool
	applier, pkg := isolationFixture(t,
		WithApplierIsolationProbe(func() (bool, string) { probed = true; return true, "" }),
	)

	if _, err := applier.Apply(pkg, false); err != nil {
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
