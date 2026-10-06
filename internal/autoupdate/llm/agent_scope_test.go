package llm

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// privateDirScript records, from INSIDE the child, the directory it runs in and
// that directory's mode into capture, then prints body.
func privateDirScript(capture, body string) string {
	return "pwd -P > '" + capture + "'; stat -c %a . >> '" + capture + "'; " + printEnvelopeScript(body)
}

// assertPrivateDirRun checks what one invocation left behind: a cmd.Dir that is
// absolute and not bentoo's cwd, which the CHILD saw as its cwd with mode 0700,
// and which no longer exists after the call returned (R2.1, R2.4).
func assertPrivateDirRun(t *testing.T, who, capture string, cmdDir string) {
	t.Helper()
	cwd, _ := os.Getwd()
	if cmdDir == "" || !filepath.IsAbs(cmdDir) || cmdDir == cwd {
		t.Fatalf("%s: cmd.Dir = %q; the agent runs in bentoo's cwd %q instead of a private directory (R2.1, R2.4)", who, cmdDir, cwd)
	}
	raw, err := os.ReadFile(capture)
	if err != nil {
		t.Fatalf("%s: reading what the child recorded: %v", who, err)
	}
	lines := strings.Split(strings.TrimSpace(string(raw)), "\n")
	if len(lines) != 2 {
		t.Fatalf("%s: child record = %q, want pwd and mode", who, raw)
	}
	if resolved, err := filepath.EvalSymlinks(filepath.Dir(cmdDir)); err == nil {
		if lines[0] != filepath.Join(resolved, filepath.Base(cmdDir)) {
			t.Errorf("%s: the child ran in %q, not in cmd.Dir %q", who, lines[0], cmdDir)
		}
	}
	if lines[1] != "700" {
		t.Errorf("%s: the private directory had mode %s while the agent ran, want 700 (R2.1, R2.4)", who, lines[1])
	}
	if _, err := os.Stat(cmdDir); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("%s: the private directory %s still exists after the call (stat err = %v); it must be removed (R2.1, R2.4)", who, cmdDir, err)
	}
}

// ---------------------------------------------------------------------------
// 3.1 — the text-only client
// ---------------------------------------------------------------------------

// TestRun_RunsInAPrivateDirectoryItRemoves is R2.1, R2.9 and R3.7 for the
// text-only client, in four parts: the tool-free argv; a fresh 0700 directory
// per invocation (two calls never share one) that is gone afterwards; a
// directory that cannot be created is an error naming it and wrapping the
// cause; a directory that cannot be removed is one warning naming it.
func TestRun_RunsInAPrivateDirectoryItRemoves(t *testing.T) {
	isolateSecretsPaths(t)

	var dirs []string
	for i := 0; i < 2; i++ {
		capture := filepath.Join(t.TempDir(), "child.txt")
		seam, spy := agentSeam(privateDirScript(capture, okEnvelope))
		c := newTestClient(t, LLMConfig{Bare: "false"}, WithClaudeCodeExecCommand(seam))
		if _, err := c.run(t.Context(), "instr", []byte("content"), ""); err != nil {
			t.Fatalf("run %d: %v", i, err)
		}
		if got := flagValues(spy.args, "--tools"); len(got) != 1 || got[0] != "" {
			t.Errorf("--tools values = %q, want exactly one empty value (R2.1)", got)
		}
		if strings.Contains(strings.Join(spy.args, " "), "WebFetch") {
			t.Errorf("the text client's argv names WebFetch (R3.7): %q", spy.args)
		}
		assertPinnedPermissions(t, "text client", spy.args, false)
		assertPrivateDirRun(t, "text client", capture, spy.last().Dir)
		dirs = append(dirs, spy.last().Dir)
	}
	if dirs[0] == dirs[1] {
		t.Errorf("two invocations shared the private directory %s; it is per invocation (R2.1)", dirs[0])
	}

	// Removal failure: the child leaves a directory it cannot delete from. The
	// guard line keeps the script inert if the client still runs in the test's
	// own cwd, so nothing is ever created inside the source tree.
	lc := captureWarnLogs(t)
	cwd, _ := os.Getwd()
	stuck := `[ "$(pwd -P)" = "` + cwd + `" ] || { mkdir -p locked/inner && : > locked/inner/f && chmod 0500 locked; }; ` + printEnvelopeScript(okEnvelope)
	seam, spy := agentSeam(stuck)
	c := newTestClient(t, LLMConfig{Bare: "false"}, WithClaudeCodeExecCommand(seam), WithClaudeCodeLogger(lc.logger()))
	_, runErr := c.run(t.Context(), "instr", []byte("content"), "")
	dir := spy.last().Dir
	if dir != "" && dir != cwd {
		t.Cleanup(func() {
			_ = os.Chmod(filepath.Join(dir, "locked"), 0o700)
			_ = os.RemoveAll(dir)
		})
	}
	if runErr != nil {
		t.Errorf("a failed removal must be logged, not returned: %v (R2.9)", runErr)
	}
	named := 0
	for _, line := range lc.all() {
		if dir != "" && strings.Contains(line, dir) {
			named++
		}
	}
	if dir == "" || named != 1 {
		t.Errorf("removal failure of %q produced %d warnings naming it, want 1 (R2.9); lines = %q", dir, named, lc.all())
	}

	// Create failure: the temp root does not exist.
	absent := filepath.Join(t.TempDir(), "absent-root")
	t.Setenv("TMPDIR", absent)
	seam, spy = agentSeam(printEnvelopeScript(okEnvelope))
	c = newTestClient(t, LLMConfig{Bare: "false"}, WithClaudeCodeExecCommand(seam))
	_, err := c.run(t.Context(), "instr", []byte("content"), "")
	if err == nil {
		t.Fatal("run succeeded although its private directory could not be created (R2.9)")
	}
	if !strings.Contains(err.Error(), absent) {
		t.Errorf("create error does not name the directory %s: %v (R2.9)", absent, err)
	}
	if !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("create error does not wrap its cause with %%w: %v (R2.9)", err)
	}
	if spy.spawns() != 0 {
		t.Errorf("claude was spawned %d times although no private directory existed", spy.spawns())
	}
}
