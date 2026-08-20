package main

// Regression for the plan/run classification split observed on 2026-08-20: a
// pending entry carrying the raw upstream tag ("v3.2.3", written by an older
// binary) was classified by the PLAN as major — "version cannot be read as a
// Gentoo version" — and priced at configure, while Validate stripped the prefix
// and ran the same bump as patch at options. The operator confirmed a cost the
// run never spends. The plan must classify the SAME value the run classifies.

import (
	"strings"
	"testing"

	"github.com/obentoo/bentoolkit/internal/autoupdate"
	"github.com/obentoo/bentoolkit/internal/autoupdate/validate"
)

func TestCheckPlan_ClassifiesAPrefixedPendingVersionAsTheRunWill(t *testing.T) {
	updates := []autoupdate.PendingUpdate{
		// dev-libs/imath as observed: a patch bump wearing its GitHub tag.
		{Package: "dev-libs/imath", CurrentVersion: "3.2.2", NewVersion: "v3.2.3"},
	}

	plan := buildValidationPlan(updates, checkPlanPolicy())

	if len(plan.Entries) != 1 {
		t.Fatalf("expected 1 plan entry, got %d", len(plan.Entries))
	}
	entry := plan.Entries[0]

	if entry.Class != validate.ClassPatch.String() {
		t.Errorf("the plan classified %q over %q as %s; the run strips the prefix and executes a %s bump",
			updates[0].NewVersion, updates[0].CurrentVersion, entry.Class, validate.ClassPatch)
	}
	if entry.Depth != validate.DepthOptions.String() {
		t.Errorf("the plan priced the bump at depth %s; the policy prices a patch bump at %s",
			entry.Depth, validate.DepthOptions)
	}
	if strings.Contains(entry.Reason, "could not be classified") {
		t.Errorf("the plan still carries the misclassification note for a readable version:\n%s", entry.Reason)
	}
}
