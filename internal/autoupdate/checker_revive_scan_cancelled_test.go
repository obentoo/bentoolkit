package autoupdate

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/obentoo/bentoolkit/internal/autoupdate/registry"
)

// TestFindRevivableOrphans_ACancelledScanSaysSo reproduces the 2026-11-13 run:
// an interrupted --check handed the revive scan a dead context, each lookup
// failed with "context canceled", the failures came back as 171 soft errors and
// the caller printed "Nothing to revive". A cancelled scan must report the
// cancellation itself, so the caller can stay quiet.
func TestFindRevivableOrphans_ACancelledScanSaysSo(t *testing.T) {
	upstream := checkerCtx059Upstream(t)
	disabled := false
	c, err := NewChecker(t.TempDir(),
		WithConfigDir(t.TempDir()),
		WithPackagesConfig(&registry.PackagesConfig{Packages: map[string]registry.PackageConfig{
			"app-editors/orphan-a": {URL: upstream.URL, Parser: "json", Path: "version", Enabled: &disabled},
			"app-editors/orphan-b": {URL: upstream.URL, Parser: "json", Path: "version", Enabled: &disabled},
		}}),
		WithRateLimiter(unlimitedRateLimiter()),
	)
	if err != nil {
		t.Fatalf("NewChecker: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	got, err := c.FindRevivableOrphans(ctx, &checkerCtx059Provider{})

	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want one wrapping context.Canceled", err)
	}
	if strings.Contains(err.Error(), "soft error") {
		t.Errorf("a cancelled scan was reported as soft errors: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("a cancelled scan returned %d candidate(s)", len(got))
	}
}
