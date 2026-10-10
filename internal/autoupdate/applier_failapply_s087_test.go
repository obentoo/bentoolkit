package autoupdate

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

const s087Pkg = "app-misc/s087-demo"

// s087Route is one way a pending store's SetStatus can fail.
type s087Route struct {
	name string
	// withEntry adds s087Pkg to the store; without it SetStatus returns
	// ErrPackageNotInPending.
	withEntry bool
	// breakSave replaces pending.json with a directory, so every later save
	// fails on read (EISDIR) after the in-memory update.
	breakSave bool
	// inner is a sentinel inside the SetStatus error's chain that the error
	// returned by failApply must NOT expose.
	inner error
}

var s087Routes = []s087Route{
	{name: "package absent from the store", inner: ErrPackageNotInPending},
	{name: "store cannot be saved", withEntry: true, breakSave: true, inner: syscall.EISDIR},
}

// s087Applier builds an Applier over a pending store kept in a temp config dir.
// It never touches the host state dir.
func s087Applier(t *testing.T, withEntry, breakSave bool) (*Applier, *PendingList) {
	t.Helper()
	configDir := t.TempDir()
	pending, err := NewPendingList(configDir)
	if err != nil {
		t.Fatalf("NewPendingList: %v", err)
	}
	if withEntry {
		if err := pending.Add(PendingUpdate{Package: s087Pkg, CurrentVersion: "1.0", NewVersion: "1.1", Status: StatusPending}); err != nil {
			t.Fatalf("pending.Add: %v", err)
		}
	}
	if breakSave {
		path := filepath.Join(configDir, "pending.json")
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			t.Fatalf("removing %s: %v", path, err)
		}
		if err := os.Mkdir(path, 0o750); err != nil {
			t.Fatalf("replacing %s with a directory: %v", path, err)
		}
	}
	a, err := NewApplier(t.TempDir(), configDir,
		WithApplierPendingList(pending),
		WithApplierDistdir(t.TempDir(), ""),
	)
	if err != nil {
		t.Fatalf("NewApplier: %v", err)
	}
	return a, pending
}

// s087SetStatusErr returns the error the store gives for a StatusFailed update
// on this route, and checks that the fixture really fails the way it claims.
func s087SetStatusErr(t *testing.T, pending *PendingList, r s087Route) error {
	t.Helper()
	serr := pending.SetStatus(s087Pkg, StatusFailed, "probe")
	if serr == nil {
		t.Fatalf("fixture %q: SetStatus succeeded, want a failure", r.name)
	}
	if !errors.Is(serr, r.inner) {
		t.Fatalf("fixture %q: SetStatus error %v does not carry %v", r.name, serr, r.inner)
	}
	return serr
}

// s087Original is the error failApply is asked to record. It wraps a sentinel
// of its own, so the chain behind the original must survive too.
func s087Original() error {
	return fmt.Errorf("%w: s087 original failure", ErrManifestFailed)
}

// TestS087_2_1_FailApplyDoesNotMatchTheSetStatusError pins R3.2: when the
// status update also fails, the returned error is not identified with the
// SetStatus error, nor with any sentinel inside it.
func TestS087_2_1_FailApplyDoesNotMatchTheSetStatusError(t *testing.T) {
	for _, r := range s087Routes {
		t.Run(r.name, func(t *testing.T) {
			a, pending := s087Applier(t, r.withEntry, r.breakSave)
			serr := s087SetStatusErr(t, pending, r)

			result, err := a.failApply(s087Pkg, &ApplyResult{}, s087Original())
			if err == nil {
				t.Fatal("failApply returned nil, want an error")
			}
			if errors.Is(err, serr) {
				t.Errorf("errors.Is(err, SetStatus error) = true, want false; err = %v", err)
			}
			if errors.Is(err, r.inner) {
				t.Errorf("errors.Is(err, %v) = true, want false: the SetStatus chain leaked; err = %v", r.inner, err)
			}
			if result == nil || result.Error == nil {
				t.Fatalf("result.Error is not set; result = %+v", result)
			}
			if errors.Is(result.Error, serr) || errors.Is(result.Error, r.inner) {
				t.Errorf("result.Error matches the SetStatus error, want no match; result.Error = %v", result.Error)
			}
		})
	}
}

// TestS087_2_1_FailApplyKeepsTheOriginalErrorWhenSetStatusFails pins R3.1:
// the returned error still matches the original (and the sentinel the original
// wraps), and its text ends with "(also failed to update status: <err>)".
func TestS087_2_1_FailApplyKeepsTheOriginalErrorWhenSetStatusFails(t *testing.T) {
	for _, r := range s087Routes {
		t.Run(r.name, func(t *testing.T) {
			a, pending := s087Applier(t, r.withEntry, r.breakSave)
			serr := s087SetStatusErr(t, pending, r)
			original := s087Original()

			result, err := a.failApply(s087Pkg, &ApplyResult{}, original)
			if err == nil {
				t.Fatal("failApply returned nil, want an error")
			}
			if !errors.Is(err, original) {
				t.Errorf("errors.Is(err, original) = false, want true; err = %v", err)
			}
			if !errors.Is(err, ErrManifestFailed) {
				t.Errorf("errors.Is(err, ErrManifestFailed) = false, want true: the original's chain was lost; err = %v", err)
			}
			wantSuffix := " (also failed to update status: " + serr.Error() + ")"
			if !strings.HasSuffix(err.Error(), wantSuffix) {
				t.Errorf("err text = %q, want it to end with %q", err.Error(), wantSuffix)
			}
			if !strings.HasPrefix(err.Error(), original.Error()) {
				t.Errorf("err text = %q, want it to start with the original %q", err.Error(), original.Error())
			}
			if result == nil || result.Error == nil {
				t.Fatalf("result.Error is not set; result = %+v", result)
			}
			if result.Error.Error() != err.Error() || !errors.Is(result.Error, original) {
				t.Errorf("result.Error = %v, want the returned error %v", result.Error, err)
			}
		})
	}
}

// TestS087_2_1_FailApplyReturnsTheOriginalWhenSetStatusSucceeds is the benign
// half: with a working store the original comes back untouched and the entry
// records the failure.
func TestS087_2_1_FailApplyReturnsTheOriginalWhenSetStatusSucceeds(t *testing.T) {
	a, pending := s087Applier(t, true, false)
	original := s087Original()

	result, err := a.failApply(s087Pkg, &ApplyResult{}, original)
	if !errors.Is(err, original) || err.Error() != original.Error() {
		t.Errorf("err = %v, want the original %v unchanged", err, original)
	}
	if strings.Contains(fmt.Sprint(err), "also failed to update status") {
		t.Errorf("err = %v carries the status-failure suffix with a working store", err)
	}
	if result == nil || !errors.Is(result.Error, original) {
		t.Errorf("result.Error = %v, want the original", result)
	}
	got, ok := pending.Get(s087Pkg)
	if !ok {
		t.Fatalf("pending entry %s vanished", s087Pkg)
	}
	if got.Status != StatusFailed || got.Error != original.Error() {
		t.Errorf("pending entry = {Status: %q, Error: %q}, want {Status: %q, Error: %q}",
			got.Status, got.Error, StatusFailed, original.Error())
	}
}
