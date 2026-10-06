package autoupdate

// Authored for story 056, sub-task 3.2 (S056-R2.5, S056-R2.6). Moved here from
// cmd/bentoo by story 060, sub-task 1.3: the snapshot restore it pins is now
// (*RegistryFixAttempt).Revert in registry_fixer.go, so the assertions follow it
// unchanged and the source check reads that file.

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"testing"
)

func s056Names(t *testing.T, dir string) string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		names = append(names, e.Name())
	}
	sort.Strings(names)
	return strings.Join(names, ",")
}

// s056Revert restores snapshot at mode into configPath through Revert, the way a
// still-failing attempt the operator declines is settled.
func s056Revert(configPath string, snapshot []byte, mode os.FileMode) error {
	a := &RegistryFixAttempt{Status: RegistryFixStillFailing, configPath: configPath, snapshot: snapshot, mode: mode}
	return a.Revert()
}

// TestRegistryFixRevert_KeepsModeDespiteStaleTmp: the restored registry
// carries the captured mode even when a stale packages.toml.tmp at another mode
// sits beside it and the umask would narrow a fresh create; and a stale tmp
// that is a symlink is never written through. Either way the stale entry is
// left exactly where it was.
func TestRegistryFixRevert_KeepsModeDespiteStaleTmp(t *testing.T) {
	old := syscall.Umask(0o077)
	t.Cleanup(func() { syscall.Umask(old) })

	snapshot := []byte("[\"app-misc/hello\"]\nurl = \"https://example.invalid\"\n")

	t.Run("stale regular tmp at 0600", func(t *testing.T) {
		dir := t.TempDir()
		configPath := filepath.Join(dir, "packages.toml")
		if err := os.WriteFile(configPath, []byte("# edited by the fixer\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		stale := configPath + ".tmp"
		if err := os.WriteFile(stale, []byte("stale"), 0o600); err != nil {
			t.Fatal(err)
		}

		if err := s056Revert(configPath, snapshot, 0o644); err != nil {
			t.Fatalf("Revert: %v", err)
		}
		if got, _ := os.ReadFile(configPath); string(got) != string(snapshot) {
			t.Errorf("registry holds %q, want the snapshot", got)
		}
		info, err := os.Lstat(configPath)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != 0o644 {
			t.Errorf("restored registry is %#o, want the captured 0644", info.Mode().Perm())
		}
		if got, _ := os.ReadFile(stale); string(got) != "stale" {
			t.Errorf("the stale packages.toml.tmp was used: it now holds %q", got)
		}
		if got := s056Names(t, dir); got != "packages.toml,packages.toml.tmp" {
			t.Errorf("directory holds %s after the restore", got)
		}
	})

	t.Run("stale tmp is a symlink", func(t *testing.T) {
		dir := t.TempDir()
		configPath := filepath.Join(dir, "packages.toml")
		if err := os.WriteFile(configPath, []byte("# edited\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		victim := filepath.Join(t.TempDir(), "victim")
		if err := os.WriteFile(victim, []byte("victim"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(victim, configPath+".tmp"); err != nil {
			t.Fatal(err)
		}
		if err := s056Revert(configPath, snapshot, 0o644); err != nil {
			t.Fatalf("Revert: %v", err)
		}
		if got, _ := os.ReadFile(victim); string(got) != "victim" {
			t.Errorf("the restore wrote through the stale tmp symlink: victim holds %q", got)
		}
		if info, err := os.Lstat(configPath); err != nil || !info.Mode().IsRegular() {
			t.Errorf("packages.toml is not a regular file after the restore (%v)", err)
		}
	})
}

// TestRegistryFixRevert_HasNoTempRenameOfItsOwn: R2.6 is a statement about the
// source, so it is checked there: registry_fixer.go, home of the restore, calls
// no os.Rename, os.WriteFile, os.Create or os.CreateTemp, spells no ".tmp"
// literal, and calls fileutil.WriteFileAtomic.
func TestRegistryFixRevert_HasNoTempRenameOfItsOwn(t *testing.T) {
	const src = "registry_fix_attempt.go"
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, src, nil, parser.SkipObjectResolution)
	if err != nil {
		t.Fatalf("parsing %s: %v", src, err)
	}
	forbidden := map[string]bool{"os.Rename": true, "os.WriteFile": true, "os.Create": true, "os.CreateTemp": true}
	calls, helper := 0, false
	ast.Inspect(file, func(n ast.Node) bool {
		switch n := n.(type) {
		case *ast.CallExpr:
			sel, ok := n.Fun.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			pkg, ok := sel.X.(*ast.Ident)
			if !ok {
				return true
			}
			calls++
			name := pkg.Name + "." + sel.Sel.Name
			if forbidden[name] {
				t.Errorf("%s: %s calls %s; the restore must go through the shared helper", fset.Position(n.Pos()), src, name)
			}
			if name == "fileutil.WriteFileAtomic" {
				helper = true
			}
		case *ast.BasicLit:
			if n.Kind == token.STRING {
				if s, err := strconv.Unquote(n.Value); err == nil && strings.Contains(s, ".tmp") {
					t.Errorf("%s: %s spells a %q temp name", fset.Position(n.Pos()), src, s)
				}
			}
		}
		return true
	})
	t.Logf("swept %d qualified calls in %s", calls, src)
	if calls == 0 {
		t.Fatalf("swept 0 calls in %s; the check passed over nothing", src)
	}
	if !helper {
		t.Errorf("%s never calls fileutil.WriteFileAtomic", src)
	}
}
