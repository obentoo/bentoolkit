package snapshot

import (
	"errors"
	"strings"
	"testing"
)

var s053Unidentified = map[string]Snapshot{
	"no path":       {ID: "42", Subvolume: "/home"},
	"no id":         {Subvolume: "/home", Path: "/home/.snapshots/42/snapshot"},
	"no id or path": {Subvolume: "/home"},
}

func s053AssertUnidentifiedErr(t *testing.T, err error, ship string) {
	t.Helper()
	if err == nil {
		t.Fatalf("Send returned nil, want an ErrSnapshotUnidentified error")
	}
	if !errors.Is(err, ErrSnapshotUnidentified) {
		t.Errorf("err = %v, want it to wrap ErrSnapshotUnidentified", err)
	}
	if !strings.Contains(err.Error(), ship) || !strings.Contains(err.Error(), "/home") {
		t.Errorf("err %q must name the ship %q and the subvolume /home", err, ship)
	}
}

// TestArchiveShipper_Send_RefusesUnidentified pins R2.1: no subprocess, no
// parent recorded; and its converse, a non-empty identity (ID "0") still ships.
func TestArchiveShipper_Send_RefusesUnidentified(t *testing.T) {
	for name, snap := range s053Unidentified {
		t.Run(name, func(t *testing.T) {
			mr := &MockRunner{}
			ps := &fakeParentStore{last: Snapshot{ID: "41", Subvolume: "/home", Path: "/home/.snapshots/41/snapshot"}, ok: true}
			a := &archiveShipper{name: "offsite", remote: "r:bkt", mode: "incremental", compress: "zstd", run: mr, parents: ps}
			_, err := a.Send(t.Context(), snap)
			s053AssertUnidentifiedErr(t, err, "offsite")
			if len(mr.Calls) != 0 {
				t.Errorf("ran %d subprocesses %+v, want none", len(mr.Calls), mr.Calls)
			}
			if len(ps.recorded) != 0 {
				t.Errorf("recorded parent %+v for an unidentified snapshot", ps.recorded)
			}
		})
	}
	t.Run("identified snapshot still ships", func(t *testing.T) {
		mr := &MockRunner{}
		a := &archiveShipper{name: "offsite", remote: "r:bkt", mode: "full", compress: "zstd", run: mr, parents: &fakeParentStore{}}
		if _, err := a.Send(t.Context(), Snapshot{ID: "0", Subvolume: "/home", Path: "/home/.snapshots/0/snapshot"}); err != nil {
			t.Fatalf("Send(identified) = %v, want nil", err)
		}
		if len(mr.Calls) == 0 {
			t.Errorf("identified snapshot ran no subprocess")
		}
	})
}

// TestResticShipper_Send_RefusesUnidentified pins R2.2: nothing is mounted.
func TestResticShipper_Send_RefusesUnidentified(t *testing.T) {
	for name, snap := range s053Unidentified {
		t.Run(name, func(t *testing.T) {
			mr := &MockRunner{}
			fm := &fakeMounter{path: "/mnt/snap.ro"}
			r := &resticShipper{name: "vault", repo: "/srv/restic", passwordFile: "/etc/bentoo/restic.pw", mount: fm, run: mr}
			_, err := r.Send(t.Context(), snap)
			s053AssertUnidentifiedErr(t, err, "vault")
			if fm.mounted {
				t.Errorf("mounted an unidentified snapshot")
			}
			if len(mr.Calls) != 0 {
				t.Errorf("ran %d subprocesses %+v, want none", len(mr.Calls), mr.Calls)
			}
		})
	}
	t.Run("identified snapshot still mounts", func(t *testing.T) {
		fm := &fakeMounter{path: "/mnt/snap.ro"}
		r := &resticShipper{name: "vault", repo: "/srv/restic", passwordFile: "/etc/bentoo/restic.pw", mount: fm, run: &MockRunner{}}
		if _, err := r.Send(t.Context(), Snapshot{ID: "0", Subvolume: "/home", Path: "/home/.snapshots/0/snapshot"}); err != nil {
			t.Fatalf("Send(identified) = %v, want nil", err)
		}
		if !fm.mounted {
			t.Errorf("identified snapshot was not mounted")
		}
	})
}
