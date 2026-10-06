package parse

import (
	"testing"

	"github.com/obentoo/bentoolkit/internal/autoupdate/registry"
)

// TestSelectVersionSeries pins that upstream selection stays inside the line:
// an index listing both series must not let the stable entry pick the testing
// release.
func TestSelectVersionSeries(t *testing.T) {
	cands := []string{"26.2.4.1", "26.2.5.2", "26.8.0.1"}

	stable := SelectVersion(nil, cands, &registry.PackageConfig{Select: "max", Series: `^26\.2\.`})
	if stable != "26.2.5.2" {
		t.Fatalf("stable entry selected %q, want %q", stable, "26.2.5.2")
	}

	testing_ := SelectVersion(nil, cands, &registry.PackageConfig{Select: "max", Series: `^26\.8\.`, Suffix: "_pre"})
	if testing_ != "26.8.0.1_pre" {
		t.Fatalf("testing entry selected %q, want %q", testing_, "26.8.0.1_pre")
	}
}
