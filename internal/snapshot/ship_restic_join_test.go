package snapshot

import (
	"errors"
	"testing"
)

// TestRunWithMount_JoinsFnAndCleanupErrors pins R4.6: a cleanup failure is
// never hidden behind the backup failure.
func TestRunWithMount_JoinsFnAndCleanupErrors(t *testing.T) {
	cleanupErr := errors.New("umount /tmp/bentoo-snap-ro-1: target is busy; left mounted")
	fm := &fakeMounter{path: "/mnt/snap.ro", cleanupErr: cleanupErr}
	r := &resticShipper{mount: fm}
	fnErr := errors.New("restic backup: repository locked")
	err := r.runWithMount(t.Context(), Snapshot{ID: "42", Subvolume: "/home", Path: "/p"}, func(string) error { return fnErr })
	if !errors.Is(err, fnErr) {
		t.Errorf("err = %v, want it to match the backup error", err)
	}
	if !errors.Is(err, cleanupErr) {
		t.Errorf("err = %v, want it to ALSO match the cleanup error", err)
	}
}
