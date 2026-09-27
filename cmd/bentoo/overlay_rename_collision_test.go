package main

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// renameTestHome lays out a config and an overlay under a fresh HOME (t.Setenv,
// so the environment is restored even on failure) and writes each ebuild of
// files into app-misc/foo. It returns the package directory.
func renameTestHome(t *testing.T, files map[string]string) string {
	t.Helper()
	home := t.TempDir()
	configDir := filepath.Join(home, ".config", "bentoo")
	overlayDir := filepath.Join(home, "overlay")
	for _, dir := range []string{configDir, filepath.Join(overlayDir, "profiles"), filepath.Join(overlayDir, "metadata")} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", dir, err)
		}
	}
	config := "overlay:\n  path: " + overlayDir + "\n  remote: origin\ngit:\n  user: Test\n  email: test@test.com\n"
	if err := os.WriteFile(filepath.Join(configDir, "config.yaml"), []byte(config), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))

	pkgDir := filepath.Join(overlayDir, "app-misc", "foo")
	if err := os.MkdirAll(pkgDir, 0o755); err != nil {
		t.Fatalf("mkdir package: %v", err)
	}
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(pkgDir, name), []byte(content), 0o644); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}
	return pkgDir
}

// renameDirState maps each file name in dir to its content.
func renameDirState(t *testing.T, dir string) map[string]string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read dir: %v", err)
	}
	out := map[string]string{}
	for _, e := range entries {
		b, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			t.Fatalf("read %s: %v", e.Name(), err)
		}
		out[e.Name()] = string(b)
	}
	return out
}

// runRenameObserved runs runRename with the given flags, stdin and args, and
// returns the exit code (-1 when runRename returned without exiting), what was
// printed to stdout (where the confirmation prompt goes), and whatever stdin
// input was left unread.
func runRenameObserved(t *testing.T, flags renameFlagsSnapshot, stdin string, args []string) (code int, stdout, unread string) {
	t.Helper()
	orig := renameFlags
	renameFlags.DryRun = flags.dryRun
	renameFlags.Yes = flags.yes
	renameFlags.Force = flags.force
	renameFlags.NoManifest = true
	t.Cleanup(func() { renameFlags = orig })

	inR, inW, err := os.Pipe()
	if err != nil {
		t.Fatalf("stdin pipe: %v", err)
	}
	if _, err := inW.WriteString(stdin); err != nil {
		t.Fatalf("write stdin: %v", err)
	}
	if err := inW.Close(); err != nil {
		t.Fatalf("close stdin writer: %v", err)
	}
	outR, outW, err := os.Pipe()
	if err != nil {
		t.Fatalf("stdout pipe: %v", err)
	}
	origIn, origOut := os.Stdin, os.Stdout
	os.Stdin, os.Stdout = inR, outW
	func() {
		defer func() { os.Stdin, os.Stdout = origIn, origOut }()
		code = withExitIntercept(func() { runRename(renameCmd, args) })
	}()
	if err := outW.Close(); err != nil {
		t.Fatalf("close stdout writer: %v", err)
	}
	var out bytes.Buffer
	if _, err := io.Copy(&out, outR); err != nil {
		t.Fatalf("read stdout: %v", err)
	}
	rest, err := io.ReadAll(inR)
	if err != nil {
		t.Fatalf("read leftover stdin: %v", err)
	}
	_ = outR.Close()
	_ = inR.Close()
	return code, out.String(), string(rest)
}

// renameFlagsSnapshot names the rename flags a test sets.
type renameFlagsSnapshot struct {
	dryRun, yes, force bool
}

// The real A9 fixture: the revision carries the newer work, and os.ReadDir
// sorts it first, so today it is moved and then overwritten.
var collidingEbuilds = map[string]string{
	"foo-1.0.ebuild":    "REV0\n",
	"foo-1.0-r1.ebuild": "REV1 carries the fix\n",
}

// TestRunRenameCollisionExitsBeforePrompt pins S050-R5.5 in normal mode: a
// preview with a collision exits 1 without asking. The operator's "y" is fed on
// stdin, so a run that prompted would read it and go on to destroy foo-1.0-r1;
// the test checks the exit code, that no prompt was printed, that the "y" was
// never consumed, and that both ebuilds are byte-identical with no target
// written. --force is added in a second pass: it must not change any of that.
func TestRunRenameCollisionExitsBeforePrompt(t *testing.T) {
	for _, force := range []bool{false, true} {
		name := "without force"
		if force {
			name = "with force"
		}
		t.Run(name, func(t *testing.T) {
			pkgDir := renameTestHome(t, collidingEbuilds)
			before := renameDirState(t, pkgDir)

			code, stdout, unread := runRenameObserved(t, renameFlagsSnapshot{force: force}, "y\n",
				[]string{"app-misc:foo:1.0", "=>", "1.1"})

			if code != 1 {
				t.Errorf("exit code = %d, want 1 on a previewed collision", code)
			}
			if strings.Contains(stdout, "Proceed with rename?") {
				t.Errorf("the operator was prompted despite a collision; stdout:\n%s", stdout)
			}
			if unread != "y\n" {
				t.Errorf("stdin was consumed (left %q): the confirmation prompt ran", unread)
			}
			if after := renameDirState(t, pkgDir); !reflect.DeepEqual(before, after) {
				t.Errorf("the package directory changed\nbefore: %q\n after: %q", before, after)
			}
		})
	}
}

// TestRunRenameCollisionDryRunExitsNonZero pins S050-R5.5 in --dry-run mode: the
// plan it previews cannot run, so the dry run exits 1 instead of reporting
// success to a script, and touches nothing.
func TestRunRenameCollisionDryRunExitsNonZero(t *testing.T) {
	pkgDir := renameTestHome(t, collidingEbuilds)
	before := renameDirState(t, pkgDir)

	code, _, _ := runRenameObserved(t, renameFlagsSnapshot{dryRun: true, yes: true}, "",
		[]string{"app-misc:foo:1.0", "=>", "1.1"})

	if code != 1 {
		t.Errorf("exit code = %d, want 1 for a dry run whose plan collides", code)
	}
	if after := renameDirState(t, pkgDir); !reflect.DeepEqual(before, after) {
		t.Errorf("a dry run changed the package directory\nbefore: %q\n after: %q", before, after)
	}
}
