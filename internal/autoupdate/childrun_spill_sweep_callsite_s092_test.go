package autoupdate

// Story 092, sub-task 7.1 (test-after): Apply runs the leftover-spill sweep
// once, on its first call, even when it then stops early on a done context.

import (
	"context"
	"testing"
	"time"
)

func TestChildRunSpillSweepRunsOnFirstApply(t *testing.T) {
	a, _ := s092v2SpillApplier(t)
	logs := a.LogsDir()
	stale := s092v2RealSpill(t, logs, 2*time.Hour)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := a.Apply(ctx, "dev-go/none", false); err == nil {
		t.Logf("Apply on a done context returned no error")
	}
	if s092v2Exists(t, stale) {
		t.Fatalf("the first Apply left %s, a spill older than one hour", stale)
	}

	again := s092v2RealSpill(t, logs, 2*time.Hour)
	if _, err := a.Apply(ctx, "dev-go/none", false); err == nil {
		t.Logf("Apply on a done context returned no error")
	}
	if !s092v2Exists(t, again) {
		t.Errorf("a second Apply swept again; the sweep must run once per Applier")
	}
}
