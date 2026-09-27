package main

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"syscall"
	"testing"
)

// runRenameOutput runs runRename like runRenameObserved does, and returns the
// exit code (-1 when runRename returned without exiting) and everything the run
// printed on stdout and stderr together.
//
// Stderr is captured at the file-descriptor level, not by swapping os.Stderr:
// the logger keeps the *os.File it saw on first use, so a swapped variable would
// miss every logger.Error line — which is exactly the output these tests read.
func runRenameOutput(t *testing.T, flags renameFlagsSnapshot, args []string) (int, string) {
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
	if err := inW.Close(); err != nil {
		t.Fatalf("close stdin writer: %v", err)
	}
	outR, outW, err := os.Pipe()
	if err != nil {
		t.Fatalf("output pipe: %v", err)
	}
	collected := make(chan string, 1)
	go func() {
		var buf bytes.Buffer
		_, _ = io.Copy(&buf, outR)
		collected <- buf.String()
	}()

	savedErrFD, err := syscall.Dup(2)
	if err != nil {
		t.Fatalf("dup stderr: %v", err)
	}
	if err := syscall.Dup2(int(outW.Fd()), 2); err != nil {
		t.Fatalf("redirect stderr: %v", err)
	}
	origIn, origOut := os.Stdin, os.Stdout
	os.Stdin, os.Stdout = inR, outW
	var code int
	func() {
		defer func() {
			os.Stdin, os.Stdout = origIn, origOut
			_ = syscall.Dup2(savedErrFD, 2)
			_ = syscall.Close(savedErrFD)
		}()
		code = withExitIntercept(func() { runRename(renameCmd, args) })
	}()
	if err := outW.Close(); err != nil {
		t.Fatalf("close output writer: %v", err)
	}
	out := <-collected
	_ = outR.Close()
	_ = inR.Close()
	return code, out
}

// brokenConfigHome points HOME and XDG_CONFIG_HOME at a config whose overlay
// path does not exist, so any configuration load fails and says "loading config".
func brokenConfigHome(t *testing.T) {
	t.Helper()
	home := t.TempDir()
	configDir := filepath.Join(home, ".config", "bentoo")
	if err := os.MkdirAll(configDir, 0o755); err != nil {
		t.Fatalf("mkdir config: %v", err)
	}
	missing := filepath.Join(home, "no-such-overlay")
	config := "overlay:\n  path: " + missing + "\n  remote: origin\n" +
		"git:\n  user: Test\n  email: test@test.com\n"
	if err := os.WriteFile(filepath.Join(configDir, "config.yaml"), []byte(config), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
}

// renamePreviewMarker is the line every rename preview starts its match list
// with.
const renamePreviewMarker = "ebuild(s) to rename"

// TestRunRenameInvalidNewVersionNamesValueWithoutPreview pins S050-R4.2 and
// R4.3 on the command's output: a rejected new version exits 1, prints the
// %q-quoted value, prints no rename preview, and is refused before the
// configuration is read.
//
// Two setups per value. With a working config and an ebuild the spec would
// match, a run that got past the argument check would print a preview. With a
// config whose overlay does not exist, a run that read the configuration first
// would print "loading config" — the fixture check at the top proves it does.
func TestRunRenameInvalidNewVersionNamesValueWithoutPreview(t *testing.T) {
	t.Run("fixture: a config load is observable", func(t *testing.T) {
		brokenConfigHome(t)
		code, out := runRenameOutput(t, renameFlagsSnapshot{dryRun: true, yes: true}, []string{"app-misc:foo:1.0", "=>", "1.1"})
		if code != 1 || !strings.Contains(out, "loading config") {
			t.Fatalf("fixture: a valid spec over the broken config should fail on loading config; exit %d, output:\n%s", code, out)
		}
	})

	for _, v := range []string{"1.2/../../../x", "latest"} {
		quoted := fmt.Sprintf("%q", v)

		t.Run(v+"/matching overlay", func(t *testing.T) {
			pkgDir := renameTestHome(t, map[string]string{"foo-1.1.ebuild": "V11\n"})
			before := renameDirState(t, pkgDir)

			code, out := runRenameOutput(t, renameFlagsSnapshot{dryRun: true, yes: true, force: true},
				[]string{"app-misc:foo:1.1", "=>", v})

			if code != 1 {
				t.Errorf("exit code = %d, want 1 for new version %q", code, v)
			}
			if !strings.Contains(out, quoted) {
				t.Errorf("output does not name the rejected value %s:\n%s", quoted, out)
			}
			if strings.Contains(out, renamePreviewMarker) {
				t.Errorf("a rename preview was printed for a rejected version:\n%s", out)
			}
			if after := renameDirState(t, pkgDir); !reflect.DeepEqual(before, after) {
				t.Errorf("the package directory changed\nbefore: %q\n after: %q", before, after)
			}
		})

		t.Run(v+"/before config", func(t *testing.T) {
			brokenConfigHome(t)

			code, out := runRenameOutput(t, renameFlagsSnapshot{dryRun: true, yes: true, force: true},
				[]string{"app-misc:foo:1.1", "=>", v})

			if code != 1 {
				t.Errorf("exit code = %d, want 1 for new version %q", code, v)
			}
			if !strings.Contains(out, quoted) {
				t.Errorf("output does not name the rejected value %s:\n%s", quoted, out)
			}
			if strings.Contains(out, "loading config") {
				t.Errorf("the configuration was read before the version was rejected:\n%s", out)
			}
		})
	}
}

// TestRunRenameCollisionDryRunNamesPair pins S050-R5.5 and R5.6 on the dry-run
// output: two ebuilds that would share one target exit 1, and the output
// states the collision in one line naming the package, the target and BOTH
// sources. The match list alone does not satisfy this — each of its lines names
// one source and the target, so the pair is never stated as a pair there.
func TestRunRenameCollisionDryRunNamesPair(t *testing.T) {
	pkgDir := renameTestHome(t, collidingEbuilds)
	before := renameDirState(t, pkgDir)

	code, out := runRenameOutput(t, renameFlagsSnapshot{dryRun: true}, []string{"app-misc:foo:1.0", "=>", "1.1"})

	if code != 1 {
		t.Errorf("exit code = %d, want 1 for a dry run whose plan collides", code)
	}
	want := []string{"app-misc/foo", "foo-1.1.ebuild", "foo-1.0.ebuild", "foo-1.0-r1.ebuild"}
	named := false
	for _, line := range strings.Split(out, "\n") {
		all := true
		for _, w := range want {
			if !strings.Contains(line, w) {
				all = false
				break
			}
		}
		if all {
			named = true
			break
		}
	}
	if !named {
		t.Errorf("no output line names the collision (%q together):\n%s", want, out)
	}
	if after := renameDirState(t, pkgDir); !reflect.DeepEqual(before, after) {
		t.Errorf("a dry run changed the package directory\nbefore: %q\n after: %q", before, after)
	}
}
