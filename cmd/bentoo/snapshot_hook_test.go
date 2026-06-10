package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/obentoo/bentoolkit/internal/snapshot"
)

// ---------------------------------------------------------------------------
// Story 007 T3.1 — `snapshot hook --install/--uninstall` verb (R4).
//
// The hook is OPT-IN: only this explicit command touches /etc/portage (R4.3).
// Tests run against a temp root via the snapshot.EmergeHookRoot seam
// (mirroring snapshot.StateDir) so no real system file is written.
// ---------------------------------------------------------------------------

// hookTOMLSnapper: snapper engine over root — the hook's supported setup.
const hookTOMLSnapper = `
[engine]
driver = "snapper"
subvolumes = ["/"]
`

// stubHookRoot points snapshot.EmergeHookRoot at a temp dir for the test.
func stubHookRoot(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	orig := snapshot.EmergeHookRoot
	t.Cleanup(func() { snapshot.EmergeHookRoot = orig })
	snapshot.EmergeHookRoot = dir
	return dir
}

// setHookFlags points the hook command's flags and restores them after.
func setHookFlags(t *testing.T, install, uninstall bool) {
	t.Helper()
	origI, origU := snapshotHookInstall, snapshotHookUninstall
	snapshotHookInstall, snapshotHookUninstall = install, uninstall
	t.Cleanup(func() { snapshotHookInstall, snapshotHookUninstall = origI, origU })
}

// hookPaths returns the hook script and bashrc paths under root.
func hookPaths(root string) (script, bashrc string) {
	return filepath.Join(root, "etc", "portage", "bashrc.d", "50-bentoo-snapshot.sh"),
		filepath.Join(root, "etc", "portage", "bashrc")
}

// TestRunSnapshotHook_InstallWritesHook: --install writes the Portage hook
// script (pre/post emerge snapshot pair via snapper, R4.1) and wires it into
// /etc/portage/bashrc through a managed block.
func TestRunSnapshotHook_InstallWritesHook(t *testing.T) {
	stubBinariesOnPath(t, "snapper")
	writeSnapshotConfig(t, hookTOMLSnapper)
	root := stubHookRoot(t)
	setHookFlags(t, true, false)

	var code int
	var exited bool
	_ = captureStdout(t, func() {
		code, exited = captureExit(t, func() {
			runSnapshotHook(snapshotHookCmd, nil)
		})
	})
	if exited {
		t.Fatalf("hook --install exited with code %d, want success", code)
	}

	script, bashrc := hookPaths(root)
	data, err := os.ReadFile(script)
	if err != nil {
		t.Fatalf("hook script not written: %v", err)
	}
	for _, want := range []string{"pre_pkg_setup", "post_pkg_postinst", "snapper"} {
		if !strings.Contains(string(data), want) {
			t.Errorf("hook script missing %q:\n%s", want, data)
		}
	}
	rc, err := os.ReadFile(bashrc)
	if err != nil {
		t.Fatalf("bashrc not written: %v", err)
	}
	if !strings.Contains(string(rc), "bentoo snapshot hook") {
		t.Errorf("bashrc missing managed block markers:\n%s", rc)
	}
}

// TestRunSnapshotHook_InstallIdempotent: a second --install leaves exactly one
// managed block — no duplicates (R4.1 idempotent).
func TestRunSnapshotHook_InstallIdempotent(t *testing.T) {
	stubBinariesOnPath(t, "snapper")
	writeSnapshotConfig(t, hookTOMLSnapper)
	root := stubHookRoot(t)
	setHookFlags(t, true, false)

	for i := 0; i < 2; i++ {
		_ = captureStdout(t, func() {
			if code, exited := captureExit(t, func() {
				runSnapshotHook(snapshotHookCmd, nil)
			}); exited {
				t.Fatalf("install #%d exited with code %d", i+1, code)
			}
		})
	}

	_, bashrc := hookPaths(root)
	rc, err := os.ReadFile(bashrc)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Count(string(rc), ">>> bentoo snapshot hook >>>"); got != 1 {
		t.Errorf("managed block begin-marker count = %d, want 1:\n%s", got, rc)
	}
}

// TestRunSnapshotHook_UninstallRemovesPreservingUserBashrc: --uninstall removes
// the script and the managed block while user bashrc content survives (R4.2).
func TestRunSnapshotHook_UninstallRemovesPreservingUserBashrc(t *testing.T) {
	stubBinariesOnPath(t, "snapper")
	writeSnapshotConfig(t, hookTOMLSnapper)
	root := stubHookRoot(t)
	script, bashrc := hookPaths(root)

	// Pre-existing user bashrc content that must survive install+uninstall.
	if err := os.MkdirAll(filepath.Dir(bashrc), 0o755); err != nil {
		t.Fatal(err)
	}
	userLine := `export MAKEOPTS="-j8"`
	if err := os.WriteFile(bashrc, []byte(userLine+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	setHookFlags(t, true, false)
	_ = captureStdout(t, func() {
		if code, exited := captureExit(t, func() {
			runSnapshotHook(snapshotHookCmd, nil)
		}); exited {
			t.Fatalf("install exited with code %d", code)
		}
	})
	rc, _ := os.ReadFile(bashrc)
	if !strings.Contains(string(rc), userLine) {
		t.Fatalf("install clobbered user bashrc:\n%s", rc)
	}

	setHookFlags(t, false, true)
	_ = captureStdout(t, func() {
		if code, exited := captureExit(t, func() {
			runSnapshotHook(snapshotHookCmd, nil)
		}); exited {
			t.Fatalf("uninstall exited with code %d", code)
		}
	})

	if _, err := os.Stat(script); !os.IsNotExist(err) {
		t.Errorf("hook script still present after uninstall (err=%v)", err)
	}
	rc, err := os.ReadFile(bashrc)
	if err != nil {
		t.Fatalf("user bashrc removed by uninstall: %v", err)
	}
	if strings.Contains(string(rc), "bentoo snapshot hook") {
		t.Errorf("managed block still present after uninstall:\n%s", rc)
	}
	if !strings.Contains(string(rc), userLine) {
		t.Errorf("user bashrc content lost on uninstall:\n%s", rc)
	}
}

// TestRunSnapshotHook_UninstallNoopWhenAbsent: --uninstall on a clean system
// is a successful no-op (R4.2).
func TestRunSnapshotHook_UninstallNoopWhenAbsent(t *testing.T) {
	writeSnapshotConfig(t, hookTOMLSnapper)
	stubHookRoot(t)
	setHookFlags(t, false, true)

	var code int
	var exited bool
	_ = captureStdout(t, func() {
		code, exited = captureExit(t, func() {
			runSnapshotHook(snapshotHookCmd, nil)
		})
	})
	if exited {
		t.Errorf("uninstall on clean root exited with code %d, want clean no-op", code)
	}
}

// TestRunSnapshotHook_InstallRefusedNonSnapper: the hook shells out to snapper,
// so --install with a non-snapper engine is refused — osExit(1) and nothing is
// written under the root.
func TestRunSnapshotHook_InstallRefusedNonSnapper(t *testing.T) {
	stubBinariesOnPath(t, "btrbk", "ssh")
	writeSnapshotConfig(t, validSnapshotTOML)
	root := stubHookRoot(t)
	setHookFlags(t, true, false)

	var code int
	var exited bool
	_ = captureStdout(t, func() {
		code, exited = captureExit(t, func() {
			runSnapshotHook(snapshotHookCmd, nil)
		})
	})
	if !exited || code != 1 {
		t.Errorf("non-snapper install exit = (%d, %v), want (1, true)", code, exited)
	}
	if _, err := os.Stat(filepath.Join(root, "etc")); !os.IsNotExist(err) {
		t.Errorf("refused install wrote files under root (err=%v)", err)
	}
}

// TestRunSnapshotHook_RequiresExactlyOneFlag: neither or both of
// --install/--uninstall is an argument error → osExit(1), nothing written.
func TestRunSnapshotHook_RequiresExactlyOneFlag(t *testing.T) {
	writeSnapshotConfig(t, hookTOMLSnapper)
	root := stubHookRoot(t)

	for name, flags := range map[string][2]bool{
		"neither": {false, false},
		"both":    {true, true},
	} {
		t.Run(name, func(t *testing.T) {
			setHookFlags(t, flags[0], flags[1])
			var code int
			var exited bool
			_ = captureStdout(t, func() {
				code, exited = captureExit(t, func() {
					runSnapshotHook(snapshotHookCmd, nil)
				})
			})
			if !exited || code != 1 {
				t.Errorf("exit = (%d, %v), want (1, true)", code, exited)
			}
			if _, err := os.Stat(filepath.Join(root, "etc")); !os.IsNotExist(err) {
				t.Errorf("invalid flags wrote files under root (err=%v)", err)
			}
		})
	}
}
