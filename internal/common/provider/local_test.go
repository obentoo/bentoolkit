package provider

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// TestNewLocalProvider_ReadsInPlace verifies a "local" provider points LocalPath
// at the configured on-disk tree (resolved to an absolute path) and reads
// package versions directly, without cloning.
func TestNewLocalProvider_ReadsInPlace(t *testing.T) {
	tmpDir := t.TempDir()
	pkgDir := filepath.Join(tmpDir, "sys-firmware", "edk2")
	if err := os.MkdirAll(pkgDir, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	for _, e := range []string{"edk2-202402.ebuild", "edk2-202405.ebuild"} {
		if err := os.WriteFile(filepath.Join(pkgDir, e), []byte("# mock"), 0o644); err != nil {
			t.Fatalf("WriteFile: %v", err)
		}
	}

	prov, err := NewLocalProvider(&RepositoryInfo{Name: "gentoo", Path: tmpDir})
	if err != nil {
		t.Fatalf("NewLocalProvider failed: %v", err)
	}
	if !prov.local {
		t.Fatal("expected local=true")
	}
	if !filepath.IsAbs(prov.LocalPath) {
		t.Errorf("LocalPath %q is not absolute", prov.LocalPath)
	}

	// LocalPackagePath must resolve the on-disk dir without any clone.
	got, err := prov.LocalPackagePath("sys-firmware", "edk2")
	if err != nil {
		t.Fatalf("LocalPackagePath failed: %v", err)
	}
	if got != pkgDir {
		t.Errorf("LocalPackagePath = %q, want %q", got, pkgDir)
	}

	versions, err := prov.GetPackageVersions("sys-firmware", "edk2")
	if err != nil {
		t.Fatalf("GetPackageVersions failed: %v", err)
	}
	if len(versions) != 2 {
		t.Errorf("expected 2 versions, got %d: %v", len(versions), versions)
	}
}

// TestNewLocalProvider_ImplementsPackageDirProvider guards the revive flow's
// type assertion: a local provider MUST satisfy PackageDirProvider.
func TestNewLocalProvider_ImplementsPackageDirProvider(t *testing.T) {
	prov, err := NewLocalProvider(&RepositoryInfo{Name: "gentoo", Path: t.TempDir()})
	if err != nil {
		t.Fatalf("NewLocalProvider failed: %v", err)
	}
	if _, ok := interface{}(prov).(PackageDirProvider); !ok {
		t.Fatal("local provider does not implement PackageDirProvider")
	}
}

// TestNewLocalProvider_Rejects covers three failure inputs: empty path, a
// missing directory, and a path that is a file.
func TestNewLocalProvider_Rejects(t *testing.T) {
	t.Run("empty path", func(t *testing.T) {
		_, err := NewLocalProvider(&RepositoryInfo{Name: "gentoo", Path: ""})
		if !errors.Is(err, ErrInvalidRepoURL) {
			t.Errorf("expected ErrInvalidRepoURL, got %v", err)
		}
	})

	t.Run("missing directory", func(t *testing.T) {
		_, err := NewLocalProvider(&RepositoryInfo{Name: "gentoo", Path: filepath.Join(t.TempDir(), "does-not-exist")})
		if !errors.Is(err, ErrInvalidRepoURL) {
			t.Errorf("expected ErrInvalidRepoURL, got %v", err)
		}
	})

	t.Run("path is a file", func(t *testing.T) {
		f := filepath.Join(t.TempDir(), "afile")
		if err := os.WriteFile(f, []byte("x"), 0o644); err != nil {
			t.Fatalf("WriteFile: %v", err)
		}
		_, err := NewLocalProvider(&RepositoryInfo{Name: "gentoo", Path: f})
		if !errors.Is(err, ErrInvalidRepoURL) {
			t.Errorf("expected ErrInvalidRepoURL, got %v", err)
		}
	})
}

// TestLocalProvider_DestructiveOpsAreNoOps ensures RemoveCache/ForceUpdate never
// touch a local in-place tree (which is the user's real /var/db/repos/gentoo,
// not a bentoo-managed cache).
func TestLocalProvider_DestructiveOpsAreNoOps(t *testing.T) {
	tmpDir := t.TempDir()
	sentinel := filepath.Join(tmpDir, "sentinel")
	if err := os.WriteFile(sentinel, []byte("keep"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	prov, err := NewLocalProvider(&RepositoryInfo{Name: "gentoo", Path: tmpDir})
	if err != nil {
		t.Fatalf("NewLocalProvider failed: %v", err)
	}

	if err := prov.RemoveCache(); err != nil {
		t.Fatalf("RemoveCache returned error: %v", err)
	}
	if err := prov.ForceUpdate(); err != nil {
		t.Fatalf("ForceUpdate returned error: %v", err)
	}

	if _, err := os.Stat(sentinel); err != nil {
		t.Fatalf("local tree was modified by a destructive op: %v", err)
	}
}

// TestNewProvider_LocalRouting checks the factory routes provider "local" to the
// in-place provider using Path.
func TestNewProvider_LocalRouting(t *testing.T) {
	prov, err := NewProvider(&RepositoryInfo{Name: "gentoo", Provider: "local", Path: t.TempDir()}, false)
	if err != nil {
		t.Fatalf("NewProvider(local) failed: %v", err)
	}
	gcp, ok := prov.(*GitCloneProvider)
	if !ok {
		t.Fatalf("expected *GitCloneProvider, got %T", prov)
	}
	if !gcp.local {
		t.Error("expected local=true from factory routing")
	}
}
