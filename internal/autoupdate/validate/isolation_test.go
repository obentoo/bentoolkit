package validate

// Authored for story 031, sub-task 7.1 — R7, R7.1.
//
// Written from the contract: design.md D6 fixes
// `ProbeIsolation() (ok bool, reason string)` and, more importantly, fixes WHY
// it is a probe at all. Inferring the answer from euid or capabilities is the
// defect this task removes: the shipped compile gate already believes it is
// isolated because Portage says `network-sandbox` is in FEATURES, while the
// namespace was never created — unshare(CLONE_NEWNET) needs privilege, and
// Portage emits no warning when it cannot.
//
// This file pins the seam name `probeNetNS`, so both branches are reachable in
// a test without privilege and without root.
//
// Red is DEFERRED to Run mode: the package does not exist yet.

import (
	"errors"
	"testing"
)

// stubNetNS makes the namespace attempt answer with err for the rest of the
// test. A nil err means the kernel granted the namespace.
func stubNetNS(t *testing.T, err error) {
	t.Helper()
	orig := probeNetNS
	probeNetNS = func() error { return err }
	t.Cleanup(func() { probeNetNS = orig })
}

// TestProbeIsolation_NamespaceGranted is the only case that may report a plain
// pass downstream.
func TestProbeIsolation_NamespaceGranted(t *testing.T) {
	stubNetNS(t, nil)

	ok, reason := ProbeIsolation()

	if !ok {
		t.Errorf("ProbeIsolation: got ok=false (%q), want true when the namespace is granted", reason)
	}
}

// TestProbeIsolation_NamespaceDenied is the measured case from the source
// report: `unshare --net` answering "Operation not permitted" as a normal user.
func TestProbeIsolation_NamespaceDenied(t *testing.T) {
	stubNetNS(t, errors.New("operation not permitted"))

	ok, reason := ProbeIsolation()

	if ok {
		t.Fatal("ProbeIsolation: got ok=true when the kernel denied the namespace")
	}
	if reason == "" {
		t.Error("a denied namespace carries an empty reason; the label printed downstream needs to say why")
	}
}

// TestProbeIsolation_ProbeCouldNotRunIsNotAPass is the rule that keeps the
// whole change honest. A probe that failed for its own reasons has proved
// NOTHING, so it must behave exactly like a denial — never like a success.
func TestProbeIsolation_ProbeCouldNotRunIsNotAPass(t *testing.T) {
	stubNetNS(t, errors.New("exec: no such file or directory"))

	ok, reason := ProbeIsolation()

	if ok {
		t.Fatal("ProbeIsolation: got ok=true from a probe that could not run; undetermined is not verified")
	}
	if reason == "" {
		t.Error("an undetermined probe carries an empty reason")
	}
}

// TestProbeIsolation_ReasonIsEmptyOnlyWhenVerified pins the pairing between the
// two return values, so a caller can rely on one to render the other.
func TestProbeIsolation_ReasonIsEmptyOnlyWhenVerified(t *testing.T) {
	stubNetNS(t, nil)
	if ok, reason := ProbeIsolation(); ok && reason != "" {
		t.Errorf("a verified probe carries reason %q; there is nothing to explain", reason)
	}

	stubNetNS(t, errors.New("operation not permitted"))
	if ok, reason := ProbeIsolation(); !ok && reason == "" {
		t.Error("an unverified probe carries no reason")
	}
}
