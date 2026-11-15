package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/obentoo/bentoolkit/internal/autoupdate"
	"github.com/obentoo/bentoolkit/internal/autoupdate/validate"
)

// TestBuildBudget_ThePlanShowsTheCappedDepthAndWhy: the plan the operator
// confirms carries the budget's decision per package, and its distribution
// counts the depth that will run.
func TestBuildBudget_ThePlanShowsTheCappedDepthAndWhy(t *testing.T) {
	install := validate.DepthInstall
	updates := []autoupdate.PendingUpdate{
		{Package: "www-client/chromium", CurrentVersion: "140.0.1", NewVersion: "140.0.2"},
		{Package: "sys-libs/zlib", CurrentVersion: "1.3.1", NewVersion: "1.3.2"},
	}
	plan := planValidation(updates, validate.DefaultDepthPolicy(), &install, nil)
	plan.capBudget(validate.BuildBudget{Limit: 30 * time.Minute, History: map[string]validate.BuildEstimate{
		"www-client/chromium": {Average: 104 * time.Minute, Merges: 29},
		"sys-libs/zlib":       {Average: 7 * time.Second, Merges: 4},
	}})

	byPkg := map[string]validationPlanEntry{}
	for _, e := range plan.Entries {
		byPkg[e.Package] = e
	}
	if e := byPkg["www-client/chromium"]; e.Depth != "configure" || !strings.Contains(e.Reason, "capped at configure by the 30m0s build budget") {
		t.Errorf("chromium = %s %q; want configure, capped by the budget", e.Depth, e.Reason)
	}
	if e := byPkg["sys-libs/zlib"]; e.Depth != "install" || !strings.Contains(e.Reason, "within the 30m0s build budget") {
		t.Errorf("zlib = %s %q; want install, within the budget", e.Depth, e.Reason)
	}
	if plan.DepthDistribution["configure"] != 1 || plan.DepthDistribution["install"] != 1 {
		t.Errorf("distribution %v does not count the capped depths", plan.DepthDistribution)
	}
}

// TestBuildBudget_AMalformedBudgetStopsBeforeAnyWork: a typo in the key, or a
// negative flag, is refused by name instead of read as no budget.
func TestBuildBudget_AMalformedBudgetStopsBeforeAnyWork(t *testing.T) {
	c := newTestCLI(t)
	path := filepath.Join(c.Home(), ".config", "bentoo", "config.yaml")
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString("autoupdate:\n  build:\n    budget: soon\n"); err != nil {
		t.Fatal(err)
	}
	_ = f.Close()

	if _, stderr, code := c.Run("overlay", "autoupdate", "--check"); code == 0 || !strings.Contains(stderr, "autoupdate.build.budget") {
		t.Errorf("a malformed budget key exited %d:\n%s", code, stderr)
	}
	if _, stderr, code := c.Run("overlay", "autoupdate", "--check", "--build-budget=-5m"); code == 0 || !strings.Contains(stderr, "--build-budget") {
		t.Errorf("a negative --build-budget exited %d:\n%s", code, stderr)
	}
}
