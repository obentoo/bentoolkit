package snapshot

import (
	"reflect"
	"testing"
)

// TestMockRunner_PipeRecordsStages pins the mockRunner half of the Piper seam:
// each pipe is recorded by stage names, and every stage still lands in Calls
// chained through stdin so argv assertions keep working.
func TestMockRunner_PipeRecordsStages(t *testing.T) {
	mr := &mockRunner{}
	markerRunner(t, mr, map[string][]byte{"btrfs": []byte("B"), "zstd": []byte("Z"), "rclone": []byte("R")})
	stages := archivePipeStages(Snapshot{ID: "42", Subvolume: "/home", Path: "/home/.snapshots/42/snapshot"}, "", "r:bkt", "")
	for i := 0; i < 2; i++ {
		if _, err := runPipe(t.Context(), mr, stages); err != nil {
			t.Fatalf("runPipe #%d: %v", i+1, err)
		}
	}
	want := [][]string{{"btrfs", "zstd", "rclone"}, {"btrfs", "zstd", "rclone"}}
	if !reflect.DeepEqual(mr.PipeCalls, want) {
		t.Errorf("PipeCalls = %q, want %q", mr.PipeCalls, want)
	}
	if len(mr.Calls) != 6 {
		t.Fatalf("Calls = %d, want 6 (3 stages x 2 pipes)", len(mr.Calls))
	}
	if string(mr.Calls[1].Stdin) != "B" || string(mr.Calls[2].Stdin) != "Z" {
		t.Errorf("stage stdin not chained: %q, %q", mr.Calls[1].Stdin, mr.Calls[2].Stdin)
	}
}
