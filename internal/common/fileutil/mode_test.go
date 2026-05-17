package fileutil

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

// mockLogger records every Warn call so tests can assert on logging behavior.
type mockLogger struct {
	warnCalls []string
}

func (m *mockLogger) Warn(format string, args ...interface{}) {
	m.warnCalls = append(m.warnCalls, format)
}

// TestCacheFileMode_IsRestrictive asserts the shared cache file mode is
// owner-only read/write (0600).
func TestCacheFileMode_IsRestrictive(t *testing.T) {
	if CacheFileMode != 0600 {
		t.Errorf("CacheFileMode = %#o, want %#o", CacheFileMode, os.FileMode(0600))
	}
}

// TestSafeChmod_NormalFS verifies SafeChmod applies the mode to a real file
// on a filesystem that supports chmod.
func TestSafeChmod_NormalFS(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cachefile")
	if err := os.WriteFile(path, []byte("data"), 0o644); err != nil {
		t.Fatalf("failed to create test file: %v", err)
	}

	log := &mockLogger{}
	if err := SafeChmod(path, CacheFileMode, log); err != nil {
		t.Fatalf("SafeChmod returned error: %v", err)
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("os.Stat failed: %v", err)
	}
	if got := info.Mode().Perm(); got != CacheFileMode {
		t.Errorf("file mode = %#o, want %#o", got, CacheFileMode)
	}
	if len(log.warnCalls) != 0 {
		t.Errorf("expected no Warn calls on normal FS, got %d: %v", len(log.warnCalls), log.warnCalls)
	}
}

// TestSafeChmod_UnsupportedFS verifies that when the filesystem does not
// support chmod (errno EOPNOTSUPP), SafeChmod swallows the error, returns
// nil, and emits exactly one Warn line.
func TestSafeChmod_UnsupportedFS(t *testing.T) {
	orig := chmodFunc
	t.Cleanup(func() { chmodFunc = orig })
	chmodFunc = func(string, os.FileMode) error {
		return &os.PathError{Op: "chmod", Path: "fake", Err: syscall.EOPNOTSUPP}
	}

	log := &mockLogger{}
	if err := SafeChmod("fake", CacheFileMode, log); err != nil {
		t.Fatalf("SafeChmod should swallow unsupported-FS error, got: %v", err)
	}
	if len(log.warnCalls) != 1 {
		t.Fatalf("expected exactly 1 Warn call, got %d: %v", len(log.warnCalls), log.warnCalls)
	}
}
