package snapshot

import (
	"context"
	"errors"
	"testing"
)

// TestArchiveShipper_DeleteRunsAfterCancel pins R5.5 on its likeliest path: a
// Send cancelled mid-pipe is what leaves a truncated object, so the deletion
// must run on a context the cancellation did not reach, and Send must still
// report the cancellation (R5.6).
func TestArchiveShipper_DeleteRunsAfterCancel(t *testing.T) {
	_ = captureWarn(t)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	deleted, deleteCtxErr := false, error(nil)
	mr := &MockRunner{RunFunc: func(ctx context.Context, name string, args []string, _ []byte) ([]byte, error) {
		switch {
		case name == "btrfs":
			return nil, ctx.Err()
		case name == "rclone" && len(args) > 0 && args[0] == "deletefile":
			deleted, deleteCtxErr = true, ctx.Err()
		}
		return nil, nil
	}}
	a := &archiveShipper{name: "offsite", remote: "r:bkt", mode: "full", compress: "zstd", run: mr, parents: &fakeParentStore{}}
	_, err := a.Send(ctx, Snapshot{ID: "42", Subvolume: "/home", Path: "/p"})
	if !errors.Is(err, context.Canceled) {
		t.Errorf("Send = %v, want errors.Is(err, context.Canceled)", err)
	}
	if !deleted {
		t.Fatal("no rclone deletefile after a cancelled Send")
	}
	if deleteCtxErr != nil {
		t.Errorf("deletefile ran on a cancelled context (%v); it would never start on the real runner", deleteCtxErr)
	}
}
