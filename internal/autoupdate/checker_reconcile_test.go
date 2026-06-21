package autoupdate

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// hasItem reports whether the batch result contains a CheckResult for pkg.
func hasItem(res BatchResult[CheckResult], pkg string) bool {
	for _, it := range res.Items {
		if it.Package == pkg {
			return true
		}
	}
	return false
}

// TestCheckAllReconcilesReappearedEbuild covers the overlay-as-source-of-truth
// rule: a package auto-disabled (enabled = false) whose ebuild is present in the
// overlay again is re-enabled by CheckAll — both on disk and in memory — and is
// included in the batch rather than skipped forever.
func TestCheckAllReconcilesReappearedEbuild(t *testing.T) {
	pkg := "sci-ml/reappeared"
	srv := jsonVersionServer(t, "1.0.0") // equal to the ebuild: no pending churn

	content := `["sci-ml/reappeared"]
enabled = false
url = "` + srv.URL + `"
parser = "json"
path = "version"
`
	overlay, configPath := writePackagesTOML(t, content)
	createTestEbuild(t, overlay, pkg, "1.0.0") // ebuild is back in the overlay

	checker, err := NewChecker(overlay,
		WithConfigDir(t.TempDir()),
		WithRateLimiter(unlimitedRateLimiter()),
	)
	if err != nil {
		t.Fatalf("NewChecker: %v", err)
	}

	res := checker.CheckAll(false)

	// In-memory config reconciled to enabled.
	if pc := checker.Config().Packages[pkg]; !pc.IsEnabled() {
		t.Errorf("expected %s enabled in memory after reconcile", pkg)
	}
	// packages.toml rewritten to enabled = true (comment-preserving edit).
	got, _ := os.ReadFile(configPath)
	if !strings.Contains(string(got), "enabled = true") {
		t.Errorf("expected packages.toml to carry enabled = true, got:\n%s", got)
	}
	// And the package is actually processed, not skipped.
	if !hasItem(res, pkg) {
		t.Errorf("expected %s in batch results after reconcile; failures=%v", pkg, res.Failures)
	}
}

// TestCheckAllKeepsHeldPackageDisabled guards the hold semantics: a held package
// (hold = true) whose ebuild is present is NOT processed and its status is never
// touched by reconciliation — hold is a deliberate maintainer decision, not stale
// bookkeeping the overlay should override.
func TestCheckAllKeepsHeldPackageDisabled(t *testing.T) {
	pkg := "sci-ml/held"
	srv := jsonVersionServer(t, "2.0.0") // newer: would pend if it were checked

	content := `["sci-ml/held"]
hold = true
url = "` + srv.URL + `"
parser = "json"
path = "version"
`
	overlay, configPath := writePackagesTOML(t, content)
	createTestEbuild(t, overlay, pkg, "1.0.0")

	checker, err := NewChecker(overlay,
		WithConfigDir(t.TempDir()),
		WithRateLimiter(unlimitedRateLimiter()),
	)
	if err != nil {
		t.Fatalf("NewChecker: %v", err)
	}

	res := checker.CheckAll(false)

	if hasItem(res, pkg) {
		t.Errorf("held package %s must not be processed", pkg)
	}
	if _, failed := res.Failures[pkg]; failed {
		t.Errorf("held package %s must not appear as a failure: %v", pkg, res.Failures[pkg])
	}
	// The held flag must survive and reconciliation must not have churned an
	// `enabled = true` into the file.
	if pc := checker.Config().Packages[pkg]; !pc.IsHeld() {
		t.Errorf("expected %s to remain held", pkg)
	}
	got, _ := os.ReadFile(configPath)
	if strings.Contains(string(got), "enabled = true") {
		t.Errorf("reconciliation must not enable a held package, got:\n%s", got)
	}
}

// TestCheckAllKeepsOrphanDisabled guards the inverse of reconciliation: a
// disabled package whose ebuild is genuinely ABSENT from the overlay stays
// disabled and out of the batch.
func TestCheckAllKeepsOrphanDisabled(t *testing.T) {
	pkg := "sci-ml/orphan"
	srv := jsonVersionServer(t, "2.0.0")

	content := `["sci-ml/orphan"]
enabled = false
url = "` + srv.URL + `"
parser = "json"
path = "version"
`
	overlay, configPath := writePackagesTOML(t, content)
	// Deliberately do NOT create an ebuild: the package is a true orphan.

	checker, err := NewChecker(overlay,
		WithConfigDir(t.TempDir()),
		WithRateLimiter(unlimitedRateLimiter()),
	)
	if err != nil {
		t.Fatalf("NewChecker: %v", err)
	}

	res := checker.CheckAll(false)

	if hasItem(res, pkg) {
		t.Errorf("absent orphan %s must stay out of the batch", pkg)
	}
	if pc := checker.Config().Packages[pkg]; pc.IsEnabled() {
		t.Errorf("absent orphan %s must remain disabled", pkg)
	}
	got, _ := os.ReadFile(configPath)
	if !strings.Contains(string(got), "enabled = false") {
		t.Errorf("packages.toml must keep enabled = false for an absent orphan, got:\n%s", filepath.Base(configPath))
	}
}
