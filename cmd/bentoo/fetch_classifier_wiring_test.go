package main

// Story 011 — Phase-3 authored (RED-first) test for sub-task 7.1:
//   newConfiguredFetchClassifier(...) + WithApplierFetchClassifier wiring.
//
// Target: cmd/bentoo/overlay_autoupdate_test.go. RED until the executor adds
// newConfiguredFetchClassifier to cmd/bentoo (mirroring newConfiguredManifestFixer
// / applierFixerOption). Go compile error "undefined: newConfiguredFetchClassifier"
// is valid RED.
//
// Behavior-level: the constructor returns a non-nil classifier when GitHub access
// is configured (a token is present) and nil when unconfigured (degrade to current
// behavior, AD6/UB2). The exact parameter list is part of the executor's audited
// surface; this test pins the OUTCOME (nil vs non-nil) via a token string input,
// matching the design note "constructs httpFetchClassifier from the GitHub
// token/config". If the executor settles on a different single input, the surface
// exception absorbs it; the nil/non-nil contract is the load-bearing behavior.

import "testing"

func TestNewConfiguredFetchClassifier_Configured(t *testing.T) {
	c := newConfiguredFetchClassifier("ghp_exampletoken")
	if c == nil {
		t.Fatal("expected a non-nil FetchClassifier when GitHub access is configured")
	}
}

func TestNewConfiguredFetchClassifier_Unconfigured(t *testing.T) {
	// No token configured → nil classifier so --apply degrades to current behavior
	// (AD6/UB2). A nil here is intentionally fed to WithApplierFetchClassifier(nil).
	if c := newConfiguredFetchClassifier(""); c != nil {
		t.Errorf("expected nil classifier when unconfigured, got %T", c)
	}
}
