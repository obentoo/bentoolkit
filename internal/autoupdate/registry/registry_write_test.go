package registry

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

func s056Umask(t *testing.T, mask int) {
	t.Helper()
	old := syscall.Umask(mask)
	t.Cleanup(func() { syscall.Umask(old) })
}

func TestWritePackagesConfigAtomicallyIgnoresStaleFixedTemp(t *testing.T) {
	s056Umask(t, 0o077)
	dir := t.TempDir()
	path := filepath.Join(dir, "packages.toml")
	if err := os.WriteFile(path, []byte("# old\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatal(err)
	}
	victim := s056PlantStaleTmp(t, dir, "packages.toml")
	data := []byte("[\"app-misc/hello\"]\nurl = \"https://example.invalid\"\n")

	if err := writePackagesConfigAtomically(path, data); err != nil {
		t.Fatalf("writePackagesConfigAtomically: %v", err)
	}
	if got, _ := os.ReadFile(victim); string(got) != "victim" {
		t.Errorf("the registry write went through the stale packages.toml.tmp symlink: victim holds %q", got)
	}
	got, _ := os.ReadFile(path)
	if string(got) != string(data) {
		t.Errorf("packages.toml holds %q, want %q", got, data)
	}
	if info, err := os.Lstat(path); err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0o644 {
		t.Errorf("packages.toml is %v (%v), want a regular 0644 file", info.Mode(), err)
	}
	s056AssertEntries(t, dir, "packages.toml", "packages.toml.tmp")
}
