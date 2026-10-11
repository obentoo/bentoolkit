package vendor

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

const s090Modules = "# example.com/adep v1.0.0\n## explicit\nexample.com/adep\n"

// s090Pinned packs files (relative path -> body) into an xz tarball, the way
// a previously published vendor tarball looks.
func s090Pinned(t *testing.T, files map[string]string) string {
	t.Helper()
	root := t.TempDir()
	tree := filepath.Join(root, "tree")
	for rel, body := range files {
		p := filepath.Join(tree, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	out := filepath.Join(root, "pinned-vendor.tar.xz")
	if b, err := exec.Command("tar", "-C", tree, "-cJf", out, ".").CombinedOutput(); err != nil {
		t.Fatalf("packing the pinned tarball: %v\n%s", err, b)
	}
	return out
}

// R7.1, R7.2: only vendor/modules.txt decides, byte for byte. Hostile first:
// near-identical files that differ; then differently shaped tarballs whose
// modules.txt is the same.
func TestModulesTxtDiffers(t *testing.T) {
	s090NeedTools(t)
	cases := []struct {
		name   string
		pinned map[string]string
		want   bool
	}{
		{"one trailing byte more", map[string]string{"app-1.0/vendor/modules.txt": s090Modules + "\n"}, true},
		{"a moved module version", map[string]string{"app-1.0/vendor/modules.txt": "# example.com/adep v1.0.1\n## explicit\nexample.com/adep\n"}, true},
		{"pinned under the old version's top directory", map[string]string{"app-0.9/vendor/modules.txt": s090Modules}, false},
		{"other vendored files differ, modules.txt equal", map[string]string{
			"app-1.0/vendor/modules.txt":           s090Modules,
			"app-1.0/vendor/example.com/adep/a.go": "package adep // old\n",
		}, false},
		{"identical", map[string]string{"app-1.0/vendor/modules.txt": s090Modules}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			env := s090Setup(t, s090AppSource)
			env.runner.modulesTxt = s090Modules
			differs, err := ModulesTxtDiffers(t.Context(), env.runner, s090Pinned(t, tc.pinned), env.req)
			if err != nil {
				t.Fatalf("ModulesTxtDiffers: %v", err)
			}
			if differs != tc.want {
				t.Errorf("differs = %v, want %v", differs, tc.want)
			}
			if n := len(env.runner.callsNamed("go")); n != 1 {
				t.Errorf("go ran %d time(s), want 1 regeneration", n)
			}
			s090AssertWorkRootEmpty(t, env.workDir)
		})
	}
}

// R7.1: a pinned tarball without vendor/modules.txt cannot be compared, and
// "cannot compare" must not read as "same".
func TestModulesTxtDiffersWithoutModulesTxtIsAnError(t *testing.T) {
	s090NeedTools(t)
	env := s090Setup(t, s090AppSource)
	pinned := s090Pinned(t, map[string]string{"app-1.0/vendor/example.com/adep/a.go": "package adep\n"})
	differs, err := ModulesTxtDiffers(t.Context(), env.runner, pinned, env.req)
	if err == nil {
		t.Fatalf("ModulesTxtDiffers = (%v, nil), want an error", differs)
	}
	s090AssertWorkRootEmpty(t, env.workDir)
}
