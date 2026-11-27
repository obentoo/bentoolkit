package autoupdate

import (
	"os"
	"testing"
)

// TestCheckAllWithoutRegistryWritesLeavesPackagesTOMLUntouched covers the
// option a check on a stale checkout runs with: the re-enable of a returned
// ebuild and the auto-disable of a vanished one are both skipped, so the file
// is byte-identical after the run.
func TestCheckAllWithoutRegistryWritesLeavesPackagesTOMLUntouched(t *testing.T) {
	srv := jsonVersionServer(t, "1.0.0")

	content := `["sci-ml/returned"]
enabled = false
disabled_by = "auto"
url = "` + srv.URL + `"
parser = "json"
path = "version"

["sci-ml/vanished"]
url = "` + srv.URL + `"
parser = "json"
path = "version"
`
	overlay, configPath := writePackagesTOML(t, content)
	createTestEbuild(t, overlay, "sci-ml/returned", "1.0.0")
	before, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}

	checker, err := NewChecker(overlay,
		WithConfigDir(t.TempDir()),
		WithRateLimiter(unlimitedRateLimiter()),
		WithoutRegistryWrites(),
	)
	if err != nil {
		t.Fatalf("NewChecker: %v", err)
	}

	checker.CheckAll(t.Context(), false)

	after, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != string(before) {
		t.Fatalf("packages.toml changed under WithoutRegistryWrites:\n--- before\n%s\n--- after\n%s", before, after)
	}
}
