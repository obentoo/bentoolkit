package main

// Authored for story 079, sub-task 5.1 — R3.4.
//
// design.md "Requirement state (check report)": report.PackageResult never
// imports autoupdate, so scannedFacts copies CheckResult.Requirements into
// report.PackageResult.Requirements field by field. A copy that dropped the
// slice, or swapped two fields of the same type (Package/Version/State are all
// strings), would leave the JSON and text reports silent or wrong.

import (
	"reflect"
	"testing"

	"github.com/obentoo/bentoolkit/internal/autoupdate"
	"github.com/obentoo/bentoolkit/internal/common/report"
)

func TestRequiresReportScannedFactsCopiesRequirements(t *testing.T) {
	results := []autoupdate.CheckResult{
		{
			Package: "dev-lang/flutter", CurrentVersion: "3.47.0", UpstreamVersion: "3.48.0", HasUpdate: true,
			Requirements: []autoupdate.RequirementState{
				{Package: "dev-lang/dart", Version: "3.14.0", State: "missing"},
				{Package: "dev-libs/engine", Version: "1.2", State: "present"},
			},
		},
		// Hostile neighbour: a package without requirements right after must
		// not inherit flutter's, and must carry none (omitted in JSON).
		{Package: "app-misc/jq", CurrentVersion: "1.7.1", UpstreamVersion: "1.8.0", HasUpdate: true},
	}

	facts := scannedFacts(results)

	if len(facts) != 2 {
		t.Fatalf("scannedFacts returned %d facts, want 2: %+v", len(facts), facts)
	}
	want := []report.Requirement{
		{Package: "dev-lang/dart", Version: "3.14.0", State: "missing"},
		{Package: "dev-libs/engine", Version: "1.2", State: "present"},
	}
	if facts[0].Package != "dev-lang/flutter" || !reflect.DeepEqual(facts[0].Requirements, want) {
		t.Errorf("flutter fact Requirements = %+v, want %+v", facts[0].Requirements, want)
	}
	if facts[1].Package != "app-misc/jq" || len(facts[1].Requirements) != 0 {
		t.Errorf("jq fact Requirements = %+v, want none", facts[1].Requirements)
	}
}
