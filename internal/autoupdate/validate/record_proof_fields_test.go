package validate

import (
	"testing"
	"time"
)

// TestStageRecord_CarriesItsProvenance: backend, environment, time and state
// survive the trip through the file, so a later apply can judge the proof.
func TestStageRecord_CarriesItsProvenance(t *testing.T) {
	root := t.TempDir()
	at := time.Date(2026, 11, 13, 18, 0, 0, 0, time.UTC)
	want := StageRecord{
		Package: "app-misc/demo", Version: "2.0", Depth: DepthConfigure,
		Backend: BackendHost, Environment: "::gentoo synced Fri, 13 Nov 2026 18:31:03 +0000",
		RecordedAt: at, State: "WAIT",
	}
	if err := WriteStageRecord(root, want); err != nil {
		t.Fatalf("WriteStageRecord: %v", err)
	}

	got, err := ReadStageRecord(root)
	if err != nil {
		t.Fatalf("ReadStageRecord: %v", err)
	}
	if got.Backend != want.Backend || got.Environment != want.Environment || !got.RecordedAt.Equal(at) ||
		got.State != want.State || got.FreshEnvironment {
		t.Errorf("read back %+v, want the provenance of %+v", got, want)
	}
}
