package snapshot

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
)

func s053Deletes(mr *mockRunner) [][]string {
	var out [][]string
	for _, c := range mr.Calls {
		if c.Name == "rclone" && len(c.Args) > 0 && c.Args[0] == "deletefile" {
			out = append(out, c.Args)
		}
	}
	return out
}

// TestArchiveShipper_Send_DeletesObjectOnPipeFailure pins R5.5 and its converse.
func TestArchiveShipper_Send_DeletesObjectOnPipeFailure(t *testing.T) {
	snap := Snapshot{ID: "42", Subvolume: "/home", Path: "/home/.snapshots/42/snapshot"}
	dest := "r:bkt/" + ArchiveObjectName("/home", "42")
	sendErr := errors.New("btrfs send: stream truncated")
	newShip := func(mr *mockRunner, ps *fakeParentStore) *archiveShipper {
		return &archiveShipper{name: "offsite", remote: "r:bkt", mode: "full", compress: "zstd", run: mr, parents: ps}
	}

	t.Run("failed pipe deletes its object and returns the pipe error", func(t *testing.T) {
		mr := &mockRunner{RunFunc: func(_ context.Context, name string, _ []string, _ []byte) ([]byte, error) {
			if name == "btrfs" {
				return nil, sendErr
			}
			return nil, nil
		}}
		ps := &fakeParentStore{}
		_, err := newShip(mr, ps).Send(t.Context(), snap)
		if !errors.Is(err, sendErr) {
			t.Errorf("Send = %v, want the pipe error", err)
		}
		if got, want := s053Deletes(mr), [][]string{{"deletefile", dest}}; !slices.EqualFunc(got, want, slices.Equal) {
			t.Errorf("deletefile calls = %q, want %q", got, want)
		}
		if len(ps.recorded) != 0 {
			t.Errorf("recorded parent %+v after a failed pipe", ps.recorded)
		}
	})
	t.Run("failed delete warns; the pipe error stays the returned error", func(t *testing.T) {
		lc := &logCapture{}
		warns := lc.all
		delErr := errors.New("rclone deletefile: 403")
		mr := &mockRunner{RunFunc: func(_ context.Context, name string, args []string, _ []byte) ([]byte, error) {
			switch {
			case name == "btrfs":
				return nil, sendErr
			case name == "rclone" && len(args) > 0 && args[0] == "deletefile":
				return nil, delErr
			}
			return nil, nil
		}}
		sh := newShip(mr, &fakeParentStore{})
		sh.log = lc.logger()
		_, err := sh.Send(t.Context(), snap)
		if !errors.Is(err, sendErr) || errors.Is(err, delErr) {
			t.Errorf("Send = %v, want the pipe error and not the delete error", err)
		}
		found := false
		for _, w := range warns() {
			if strings.Contains(w, dest) {
				found = true
			}
		}
		if !found {
			t.Errorf("warnings %q do not report the failed deletion of %s", warns(), dest)
		}
	})
	t.Run("successful pipe deletes nothing", func(t *testing.T) {
		mr := &mockRunner{}
		if _, err := newShip(mr, &fakeParentStore{}).Send(t.Context(), snap); err != nil {
			t.Fatalf("Send = %v, want nil", err)
		}
		if d := s053Deletes(mr); len(d) != 0 {
			t.Errorf("deletefile calls %q after a successful ship", d)
		}
	})
}

type s053RunOnlyRunner struct{ calls int }

func (r *s053RunOnlyRunner) Run(context.Context, string, []string, []byte) ([]byte, error) {
	r.calls++
	return nil, nil
}

// TestRunPipe_RefusesNonPiper pins R5.7: no buffered fallback.
func TestRunPipe_RefusesNonPiper(t *testing.T) {
	r := &s053RunOnlyRunner{}
	stages := archivePipeStages(Snapshot{ID: "42", Subvolume: "/home", Path: "/p"}, "", "r:bkt", "")
	_, err := runPipe(t.Context(), r, stages)
	if err == nil || !strings.Contains(err.Error(), "s053RunOnlyRunner") {
		t.Errorf("runPipe = %v, want an error naming the runner type s053RunOnlyRunner", err)
	}
	if r.calls != 0 {
		t.Errorf("ran %d stages through Run: fell back to buffering", r.calls)
	}
}
