package autoupdate

import (
	"context"
	"errors"
	"os/exec"
	"path/filepath"
	"reflect"
	"testing"
)

func TestMissingDistfiles(t *testing.T) {
	out := []byte(`{"__class__": "MissingManifest", "category": "c", "package": "p", "version": "2.0.0", "files": ["p-2.0.0-extra.tar.xz", "p-2.0.0-a.tar.gz"]}
{"__class__": "MissingManifest", "category": "c", "package": "p", "version": "1.0.0", "files": ["old.tar.gz"]}
`)
	got, err := missingDistfiles(out, "2.0.0")
	if err != nil {
		t.Fatalf("missingDistfiles: %v", err)
	}
	want := []string{"p-2.0.0-a.tar.gz", "p-2.0.0-extra.tar.xz"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v (only the bumped version, sorted)", got, want)
	}

	if _, err := missingDistfiles([]byte("Traceback (most recent call last):\n"), "2.0.0"); err == nil {
		t.Error("a non-JsonStream line was read as a clean scan")
	}
	if got, err := missingDistfiles(nil, "2.0.0"); err != nil || len(got) != 0 {
		t.Errorf("empty output: got %v, %v; want none, nil", got, err)
	}
}

// TestApplyRefusesManifestMissingDist pins the guard end to end: the manifest
// step "succeeds", pkgcheck reports a distfile of the new version with no DIST
// line, and the apply fails instead of shipping an ebuild that cannot fetch.
func TestApplyRefusesManifestMissingDist(t *testing.T) {
	tmpDir := t.TempDir()
	overlayDir := filepath.Join(tmpDir, "overlay")
	configDir := filepath.Join(tmpDir, "config")

	pkg := "test-cat/test-pkg"
	createTestEbuildFile(t, overlayDir, pkg, "1.0.0")

	pending, _ := NewPendingList(configDir)
	if err := pending.Add(PendingUpdate{
		Package:        pkg,
		CurrentVersion: "1.0.0",
		NewVersion:     "2.0.0",
		Status:         StatusPending,
	}); err != nil {
		t.Fatalf("pending.Add: %v", err)
	}

	record := `{"__class__": "MissingManifest", "category": "test-cat", "package": "test-pkg", "version": "2.0.0", "files": ["test-pkg-2.0.0.tar.gz"]}`
	execFn := func(ctx context.Context, name string, arg ...string) *exec.Cmd {
		if name == "pkgcheck" {
			return exec.CommandContext(ctx, "printf", "%s\n", record)
		}
		return exec.CommandContext(ctx, "true")
	}

	applier, err := NewApplier(overlayDir, configDir,
		WithApplierPendingList(pending),
		WithExecCommand(execFn),
	)
	if err != nil {
		t.Fatalf("NewApplier: %v", err)
	}

	result, err := applier.Apply(t.Context(), pkg, false)
	if !errors.Is(err, ErrManifestIncomplete) {
		t.Fatalf("Apply error = %v, want ErrManifestIncomplete", err)
	}
	if result.Success {
		t.Error("an apply with a missing DIST line reported success")
	}
	if !pending.Has(pkg) {
		t.Error("the failed bump's pending entry was removed; it must stay for a retry")
	}
}
