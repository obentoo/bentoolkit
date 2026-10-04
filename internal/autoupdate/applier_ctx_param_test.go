package autoupdate

// Story 059, sub-task 4.2 (R1.1, R4.1, R4.2, R4.5): Apply takes its context as
// a parameter. A context that is already done stops Apply before it creates or
// modifies anything under the overlay, the error is that context's own error,
// and the pending entry stays. One Applier serves many Apply calls, each bounded
// by its own context, and every child it spawns is spawned from that context.

import (
	"context"
	"crypto/sha256"
	"errors"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

type applyCtx059Key struct{}

// applyCtx059Seam is the exec seam: it records how many children were spawned
// and the context label each was spawned with, and every child succeeds.
type applyCtx059Seam struct {
	mu     sync.Mutex
	labels []any
}

func (s *applyCtx059Seam) exec(ctx context.Context, name string, arg ...string) *exec.Cmd {
	s.mu.Lock()
	s.labels = append(s.labels, ctx.Value(applyCtx059Key{}))
	s.mu.Unlock()
	return exec.CommandContext(ctx, "true")
}

func (s *applyCtx059Seam) take() []any {
	s.mu.Lock()
	defer s.mu.Unlock()
	got := s.labels
	s.labels = nil
	return got
}

type applyCtx059Fixture struct {
	overlayDir string
	pending    *PendingList
	applier    *Applier
	seam       *applyCtx059Seam
}

// newApplyCtx059Fixture builds an overlay holding each package at 1.0.0, a
// pending 1.0.0 -> 2.0.0 update for each, and one Applier over them.
func newApplyCtx059Fixture(t *testing.T, pkgs ...string) *applyCtx059Fixture {
	t.Helper()
	tmp := t.TempDir()
	f := &applyCtx059Fixture{overlayDir: filepath.Join(tmp, "overlay"), seam: &applyCtx059Seam{}}
	configDir := filepath.Join(tmp, "config")
	pending, err := NewPendingList(configDir)
	if err != nil {
		t.Fatalf("NewPendingList: %v", err)
	}
	for _, pkg := range pkgs {
		createTestEbuildFile(t, f.overlayDir, pkg, "1.0.0")
		if err := pending.Add(PendingUpdate{Package: pkg, CurrentVersion: "1.0.0", NewVersion: "2.0.0", Status: StatusPending}); err != nil {
			t.Fatalf("pending.Add(%s): %v", pkg, err)
		}
	}
	f.pending = pending
	f.applier, err = NewApplier(f.overlayDir, configDir,
		WithApplierPendingList(pending),
		WithExecCommand(f.seam.exec),
	)
	if err != nil {
		t.Fatalf("NewApplier: %v", err)
	}
	return f
}

type applyCtx059Entry struct {
	mode fs.FileMode
	sum  [32]byte
	mod  time.Time
}

// snapshotApplyCtx059 records every path under dir with its mode, content hash
// and modification time, so any creation, removal or rewrite shows up.
func snapshotApplyCtx059(t *testing.T, dir string) map[string]applyCtx059Entry {
	t.Helper()
	out := map[string]applyCtx059Entry{}
	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		e := applyCtx059Entry{mode: info.Mode(), mod: info.ModTime()}
		if info.Mode().IsRegular() {
			data, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			e.sum = sha256.Sum256(data)
		}
		out[path] = e
		return nil
	})
	if err != nil {
		t.Fatalf("snapshot %s: %v", dir, err)
	}
	return out
}

func diffApplyCtx059(before, after map[string]applyCtx059Entry) []string {
	var diffs []string
	for p, b := range before {
		a, ok := after[p]
		switch {
		case !ok:
			diffs = append(diffs, "removed "+p)
		case a != b:
			diffs = append(diffs, "modified "+p)
		}
	}
	for p := range after {
		if _, ok := before[p]; !ok {
			diffs = append(diffs, "created "+p)
		}
	}
	return diffs
}

// TestApplyOnADoneContextTouchesNothing pins R4.1 and R4.5 for both ways a
// context can already be done. The deadline case is the hostile half of
// "errors.Is(err, ctx.Err())": an Apply that hard-codes context.Canceled
// passes the cancelled case and fails this one.
func TestApplyOnADoneContextTouchesNothing(t *testing.T) {
	const pkg = "test-cat/ctx-apply"
	for _, tc := range []struct {
		name string
		ctx  func() (context.Context, context.CancelFunc)
	}{
		{"cancelled", func() (context.Context, context.CancelFunc) {
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			return ctx, cancel
		}},
		{"deadline exceeded", func() (context.Context, context.CancelFunc) {
			return context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newApplyCtx059Fixture(t, pkg)
			ctx, cancel := tc.ctx()
			defer cancel()

			before := snapshotApplyCtx059(t, f.overlayDir)
			res, err := f.applier.Apply(ctx, pkg, false)
			after := snapshotApplyCtx059(t, f.overlayDir)

			if err == nil {
				t.Fatal("Apply with a done context returned nil error (R4.1)")
			}
			if !errors.Is(err, ctx.Err()) {
				t.Errorf("Apply error = %v; want errors.Is(err, %v) (R4.1)", err, ctx.Err())
			}
			if !strings.Contains(err.Error(), pkg) {
				t.Errorf("Apply error = %q; want it to name the package %s (Q4)", err, pkg)
			}
			if res != nil && res.Success {
				t.Error("Apply with a done context reported Success")
			}
			if d := diffApplyCtx059(before, after); len(d) != 0 {
				t.Errorf("Apply with a done context changed the overlay: %v (R4.1)", d)
			}
			if spawned := f.seam.take(); len(spawned) != 0 {
				t.Errorf("Apply with a done context spawned %d child process(es); want 0 (R4.1)", len(spawned))
			}
			if !f.pending.Has(pkg) {
				t.Error("Apply with a done context removed the pending entry (R4.5)")
			}
		})
	}
}

// TestApplyContextDoesNotLeakIntoTheNextApply pins R4.2 and its converse on
// ONE Applier, and that every child a live Apply spawns is spawned from that
// Apply's own context.
func TestApplyContextDoesNotLeakIntoTheNextApply(t *testing.T) {
	const pkgA, pkgB = "test-cat/ctx-a", "test-cat/ctx-b"
	cancelled := func() context.Context {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		return ctx
	}
	assertApplied := func(t *testing.T, f *applyCtx059Fixture, pkg, label string) {
		t.Helper()
		ctx := context.WithValue(context.Background(), applyCtx059Key{}, label)
		res, err := f.applier.Apply(ctx, pkg, false)
		if err != nil || res == nil || !res.Success {
			t.Fatalf("Apply(%s) with a live context = (%+v, %v); want success", pkg, res, err)
		}
		if _, err := os.Stat(f.applier.EbuildPath(pkg, "2.0.0")); err != nil {
			t.Errorf("Apply(%s) succeeded but the new ebuild is missing: %v", pkg, err)
		}
		if f.pending.Has(pkg) {
			t.Errorf("Apply(%s) succeeded but its pending entry remains", pkg)
		}
		spawned := f.seam.take()
		if len(spawned) == 0 {
			t.Fatalf("Apply(%s) spawned no child (the manifest step runs one)", pkg)
		}
		for i, got := range spawned {
			if got != label {
				t.Errorf("Apply(%s): child %d was spawned with context label %v; want %q (every child must come from the call's context)", pkg, i, got, label)
			}
		}
	}
	assertRefused := func(t *testing.T, f *applyCtx059Fixture, pkg string) {
		t.Helper()
		if _, err := f.applier.Apply(cancelled(), pkg, false); !errors.Is(err, context.Canceled) {
			t.Fatalf("Apply(%s) with a cancelled context: err = %v; want errors.Is(err, context.Canceled)", pkg, err)
		}
		if _, err := os.Stat(f.applier.EbuildPath(pkg, "2.0.0")); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("Apply(%s) with a cancelled context left a 2.0.0 ebuild (stat err = %v)", pkg, err)
		}
		if !f.pending.Has(pkg) {
			t.Errorf("Apply(%s) with a cancelled context removed its pending entry", pkg)
		}
		f.seam.take()
	}

	t.Run("cancelled Apply, then live Apply of another package", func(t *testing.T) {
		f := newApplyCtx059Fixture(t, pkgA, pkgB)
		assertRefused(t, f, pkgA)
		assertApplied(t, f, pkgB, "apply-b")
	})
	t.Run("live Apply, then cancelled Apply of another package", func(t *testing.T) {
		f := newApplyCtx059Fixture(t, pkgA, pkgB)
		assertApplied(t, f, pkgA, "apply-a")
		assertRefused(t, f, pkgB)
	})
}
