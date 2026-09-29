package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestRunSnapshotHook_UninstallRefusesBrokenBlock: the refusal reaches the
// operator on stderr with the fix, exit 1, and nothing on disk changes.
func TestRunSnapshotHook_UninstallRefusesBrokenBlock(t *testing.T) {
	writeSnapshotConfig(t, hookTOMLSnapper)
	root := stubHookRoot(t)
	script, bashrc := hookPaths(root)
	if err := os.MkdirAll(filepath.Dir(script), 0o755); err != nil {
		t.Fatal(err)
	}
	broken := "export A=1\n# >>> bentoo snapshot hook >>>\nexport B=2\n"
	if err := os.WriteFile(bashrc, []byte(broken), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(script, []byte("SCRIPT\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	setHookFlags(t, false, true)

	var code int
	var exited bool
	stop := captureStream(t, 2, &os.Stderr)
	_ = captureStdout(t, func() {
		code, exited = exitOf(runSnapshotHook(snapshotHookCmd, nil))
	})
	stderr := stop()

	if !exited || code != 1 {
		t.Errorf("exit = (%d, %v), want (1, true)", code, exited)
	}
	for _, want := range []string{bashrc, "# <<< bentoo snapshot hook <<<"} {
		if !strings.Contains(stderr, want) {
			t.Errorf("stderr %q must contain %q", stderr, want)
		}
	}
	if got, _ := os.ReadFile(bashrc); string(got) != broken {
		t.Errorf("bashrc changed to %q", got)
	}
	if got, err := os.ReadFile(script); err != nil || string(got) != "SCRIPT\n" {
		t.Errorf("hook script removed or changed: (%q, %v)", got, err)
	}
}
