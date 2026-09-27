package snapshot

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// s053MountScript scripts mount/umount for transientMounter. umount behaves as
// execRunner does on a dead ctx: it never starts and returns ctx.Err().
type s053MountScript struct {
	bindErr, remountErr, umountErr error
	onBind                         func(dir string)
	umounts                        [][]string
	umountCtxErr                   error
	umountDeadline                 time.Duration
	umountHasDeadline              bool
}

func (s *s053MountScript) runner() *MockRunner {
	return &MockRunner{RunFunc: func(ctx context.Context, name string, args []string, _ []byte) ([]byte, error) {
		switch name {
		case "mount":
			if len(args) > 0 && args[0] == "--bind" {
				if s.onBind != nil {
					s.onBind(args[len(args)-1])
				}
				return nil, s.bindErr
			}
			return nil, s.remountErr
		case "umount":
			s.umounts = append(s.umounts, append([]string(nil), args...))
			s.umountCtxErr = ctx.Err()
			if dl, ok := ctx.Deadline(); ok {
				s.umountHasDeadline, s.umountDeadline = true, time.Until(dl)
			}
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			return nil, s.umountErr
		}
		return nil, nil
	}}
}

func s053Tmp(t *testing.T) string {
	t.Helper()
	tmp := t.TempDir()
	t.Setenv("TMPDIR", tmp)
	return tmp
}

func s053MountDirs(t *testing.T, tmp string) []string {
	t.Helper()
	m, err := filepath.Glob(filepath.Join(tmp, "bentoo-snap-ro-*"))
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func s053Plant(t *testing.T, dir string) string {
	t.Helper()
	f := filepath.Join(dir, "still-mounted-data")
	if err := os.WriteFile(f, []byte("snapshot content"), 0o600); err != nil {
		t.Fatal(err)
	}
	return f
}

var s053Snap = Snapshot{ID: "42", Subvolume: "/home", Path: "/home/.snapshots/42/snapshot"}

func TestTransientMounter_UmountUnderCancelledCtx(t *testing.T) {
	s053Tmp(t)
	s := &s053MountScript{}
	m := &transientMounter{run: s.runner()}
	ctx, cancel := context.WithCancel(t.Context())
	dir, cleanup, err := m.Mount(ctx, s053Snap)
	if err != nil {
		t.Fatalf("Mount: %v", err)
	}
	cancel() // the Send was cancelled mid-backup
	cerr := cleanup()
	if len(s.umounts) != 1 || len(s.umounts[0]) != 1 || s.umounts[0][0] != dir {
		t.Fatalf("umount calls = %q, want exactly [[%s]]", s.umounts, dir)
	}
	if s.umountCtxErr != nil {
		t.Errorf("umount ran under a cancelled ctx (%v): it would never start", s.umountCtxErr)
	}
	if !s.umountHasDeadline || s.umountDeadline <= 0 || s.umountDeadline > 30*time.Second {
		t.Errorf("umount ctx deadline = (%v, set=%v), want bounded by 30s", s.umountDeadline, s.umountHasDeadline)
	}
	if cerr != nil {
		t.Errorf("cleanup = %v, want nil after a successful umount", cerr)
	}
}

func TestTransientMounter_UmountFailureLeavesDir(t *testing.T) {
	s053Tmp(t)
	busy := errors.New("umount: target is busy")
	s := &s053MountScript{umountErr: busy}
	m := &transientMounter{run: s.runner()}
	dir, cleanup, err := m.Mount(t.Context(), s053Snap)
	if err != nil {
		t.Fatalf("Mount: %v", err)
	}
	planted := s053Plant(t, dir) // what a still-mounted snapshot looks like
	cerr := cleanup()
	if !errors.Is(cerr, busy) {
		t.Fatalf("cleanup = %v, want it to wrap the umount error", cerr)
	}
	if !strings.Contains(cerr.Error(), dir) || !strings.Contains(strings.ToLower(cerr.Error()), "mounted") {
		t.Errorf("cleanup error %q must name %s and say it was left mounted", cerr, dir)
	}
	if _, err := os.Stat(planted); err != nil {
		t.Errorf("content under a still-mounted dir was deleted: %v", err)
	}
}

func TestTransientMounter_RemovesDirAfterUmount(t *testing.T) {
	t.Run("empty dir removed", func(t *testing.T) {
		tmp := s053Tmp(t)
		m := &transientMounter{run: (&s053MountScript{}).runner()}
		_, cleanup, err := m.Mount(t.Context(), s053Snap)
		if err != nil {
			t.Fatalf("Mount: %v", err)
		}
		if cerr := cleanup(); cerr != nil {
			t.Errorf("cleanup = %v, want nil", cerr)
		}
		if left := s053MountDirs(t, tmp); len(left) != 0 {
			t.Errorf("mountpoints left behind: %q", left)
		}
	})
	t.Run("non-empty dir is never removed recursively", func(t *testing.T) {
		s053Tmp(t)
		m := &transientMounter{run: (&s053MountScript{}).runner()}
		dir, cleanup, err := m.Mount(t.Context(), s053Snap)
		if err != nil {
			t.Fatalf("Mount: %v", err)
		}
		planted := s053Plant(t, dir)
		cerr := cleanup()
		if cerr == nil || !strings.Contains(cerr.Error(), dir) {
			t.Errorf("cleanup = %v, want an error naming %s", cerr, dir)
		}
		if _, err := os.Stat(planted); err != nil {
			t.Errorf("recursive delete reached the snapshot content: %v", err)
		}
	})
}

func TestTransientMounter_BindFailureRemovesDir(t *testing.T) {
	bindErr := errors.New("mount: permission denied")
	t.Run("empty dir removed", func(t *testing.T) {
		tmp := s053Tmp(t)
		m := &transientMounter{run: (&s053MountScript{bindErr: bindErr}).runner()}
		if _, _, err := m.Mount(t.Context(), s053Snap); !errors.Is(err, bindErr) {
			t.Fatalf("Mount = %v, want the bind error", err)
		}
		if left := s053MountDirs(t, tmp); len(left) != 0 {
			t.Errorf("unused mountpoint left behind: %q", left)
		}
	})
	t.Run("non-empty dir is never removed recursively", func(t *testing.T) {
		s053Tmp(t)
		var planted string
		s := &s053MountScript{bindErr: bindErr, onBind: func(dir string) { planted = s053Plant(t, dir) }}
		m := &transientMounter{run: s.runner()}
		if _, _, err := m.Mount(t.Context(), s053Snap); err == nil {
			t.Fatalf("Mount = nil, want the bind error")
		}
		if _, err := os.Stat(planted); err != nil {
			t.Errorf("bind-failure cleanup deleted recursively: %v", err)
		}
	})
}

func TestTransientMounter_RemountFailureCleansUp(t *testing.T) {
	remountErr := errors.New("mount: cannot remount read-only")
	t.Run("umount also fails: both errors, dir kept", func(t *testing.T) {
		tmp := s053Tmp(t)
		busy := errors.New("umount: target is busy")
		m := &transientMounter{run: (&s053MountScript{remountErr: remountErr, umountErr: busy}).runner()}
		_, _, err := m.Mount(t.Context(), s053Snap)
		if !errors.Is(err, remountErr) || !errors.Is(err, busy) {
			t.Errorf("Mount = %v, want the remount AND the umount error joined", err)
		}
		if left := s053MountDirs(t, tmp); len(left) != 1 {
			t.Errorf("mountpoints = %q, want the still-mounted dir kept", left)
		}
	})
	t.Run("umount succeeds: remount error, dir removed", func(t *testing.T) {
		tmp := s053Tmp(t)
		s := &s053MountScript{remountErr: remountErr}
		m := &transientMounter{run: s.runner()}
		if _, _, err := m.Mount(t.Context(), s053Snap); !errors.Is(err, remountErr) {
			t.Errorf("Mount = %v, want the remount error", err)
		}
		if len(s.umounts) != 1 {
			t.Errorf("umount calls = %q, want exactly one", s.umounts)
		}
		if left := s053MountDirs(t, tmp); len(left) != 0 {
			t.Errorf("mountpoints left behind: %q", left)
		}
	})
}
