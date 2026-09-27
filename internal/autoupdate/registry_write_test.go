package autoupdate

// Authored for story 056, sub-task 3.1 (S056-R2.2, S056-R2.3, S056-R2.4).

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

func s056Analyzer(overlay string) *Analyzer {
	return &Analyzer{
		overlayPath: overlay,
		config: &PackagesConfig{Packages: map[string]PackageConfig{
			"app-misc/hello": {URL: "https://example.invalid/hello.json", Parser: "json", Path: "version"},
		}},
	}
}

// TestSavePackagesConfigKeepsRegistryMode: R2.3, with its converse. A 0644
// registry stays 0644 under umask 077 (the create mode would narrow it), and a
// 0600 registry stays 0600 under umask 000 (the create mode would widen it) —
// the registry's own mode wins in both directions.
func TestSavePackagesConfigKeepsRegistryMode(t *testing.T) {
	for _, tc := range []struct {
		name  string
		umask int
		mode  os.FileMode
	}{
		{"0644 under umask 077", 0o077, 0o644},
		{"0600 under umask 000", 0o000, 0o600},
		{"0664 under umask 077", 0o077, 0o664},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s056Umask(t, tc.umask)
			overlay := t.TempDir()
			cfgDir := filepath.Join(overlay, ".autoupdate")
			if err := os.MkdirAll(cfgDir, 0o755); err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(cfgDir, "packages.toml")
			if err := os.WriteFile(path, []byte("# old registry\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			if err := os.Chmod(path, tc.mode); err != nil {
				t.Fatal(err)
			}

			if err := s056Analyzer(overlay).savePackagesConfig(); err != nil {
				t.Fatalf("savePackagesConfig: %v", err)
			}
			info, err := os.Stat(path)
			if err != nil {
				t.Fatal(err)
			}
			if got := info.Mode().Perm(); got != tc.mode {
				t.Errorf("packages.toml is %#o after the save, want the %#o it had", got, tc.mode)
			}
			s056AssertEntries(t, cfgDir, "packages.toml")
		})
	}
}

// TestSavePackagesConfigNewRegistryIs0644: R2.4 — a first registry is 0644
// under a narrowing umask AND under a umask that would leave os.Create's 0666.
func TestSavePackagesConfigNewRegistryIs0644(t *testing.T) {
	for _, mask := range []int{0o077, 0o000} {
		t.Run("umask "+os.FileMode(mask).String(), func(t *testing.T) {
			s056Umask(t, mask)
			overlay := t.TempDir()
			if err := s056Analyzer(overlay).savePackagesConfig(); err != nil {
				t.Fatalf("savePackagesConfig: %v", err)
			}
			info, err := os.Stat(filepath.Join(overlay, ".autoupdate", "packages.toml"))
			if err != nil {
				t.Fatal(err)
			}
			if got := info.Mode().Perm(); got != 0o644 {
				t.Errorf("a new packages.toml is %#o, want 0644", got)
			}
		})
	}
}

// TestSavePackagesConfigIgnoresStaleFixedTemp and
// TestWritePackagesConfigAtomicallyIgnoresStaleFixedTemp: R2.2 — neither writer
// uses the fixed packages.toml.tmp any more, so a stale one (here a symlink to a
// victim) is never followed, and it is left exactly where it was.
func TestSavePackagesConfigIgnoresStaleFixedTemp(t *testing.T) {
	overlay := t.TempDir()
	cfgDir := filepath.Join(overlay, ".autoupdate")
	if err := os.MkdirAll(cfgDir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(cfgDir, "packages.toml")
	if err := os.WriteFile(path, []byte("# old\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	victim := s056PlantStaleTmp(t, cfgDir, "packages.toml")

	if err := s056Analyzer(overlay).savePackagesConfig(); err != nil {
		t.Fatalf("savePackagesConfig: %v", err)
	}
	if got, _ := os.ReadFile(victim); string(got) != "victim" {
		t.Errorf("savePackagesConfig wrote through the stale packages.toml.tmp symlink: victim holds %q", got)
	}
	if info, err := os.Lstat(path); err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0o644 {
		t.Errorf("packages.toml is %v (%v), want a regular 0644 file", info.Mode(), err)
	}
	s056AssertEntries(t, cfgDir, "packages.toml", "packages.toml.tmp")
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
