package llm

// Authored for story 051 (llm-agent-least-privilege), sub-task 2.1 — one
// permission-argv builder (S051-R2.3, R2.5..R2.8, R4.1, R4.2).
//
// Name pinned by tasks.md: agentPermissionArgs(p agentPermissions)
// ([]string, error).
//
// Names pinned by THIS file, because no document fixes them: the three
// agentPermissions fields it constructs — agent (the name R2.8's error must
// carry), dir (the agent's own directory) and tools (the --tools set). An
// implementation that spells them differently renames them here; the
// assertions do not change.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func mustPermArgs(t *testing.T, p AgentPermissions) []string {
	t.Helper()
	args, err := AgentPermissionArgs(p)
	if err != nil {
		t.Fatalf("agentPermissionArgs(%+v): %v", p, err)
	}
	return args
}

// TestAgentPermissionArgs_OneElementPerRule is R2.7 (and the R2.3 shape): each
// rule is its own argv element, and the directory rules are the exact
// `//`-anchored `/**` form — never a prefix a sibling directory also matches.
func TestAgentPermissionArgs_OneElementPerRule(t *testing.T) {
	isolateSecretsPaths(t)
	dir := t.TempDir()
	args := mustPermArgs(t, AgentPermissions{Agent: "manifest fixer", Dir: dir, Tools: []string{"Read", "Edit", "Write", "Bash", "WebFetch"}})

	allow := flagValues(args, "--allowedTools")
	if len(allow) == 0 {
		t.Fatalf("no --allowedTools values in %q", args)
	}
	for _, flag := range []string{"--allowedTools", "--disallowedTools"} {
		for _, v := range flagValues(args, flag) {
			if !oneRule.MatchString(v) {
				t.Errorf("%s element %q is not exactly one rule (R2.7)", flag, v)
			}
		}
	}
	for _, want := range []string{dirRule("Read", dir), dirRule("Edit", dir)} {
		if countOf(allow, want) != 1 {
			t.Errorf("--allowedTools holds %q %d times, want exactly once (R2.3); allow = %q", want, countOf(allow, want), allow)
		}
	}
	for _, bare := range []string{"Read", "Edit", "Write"} {
		if containsRule(allow, bare) {
			t.Errorf("--allowedTools grants bare %q, which reaches every path (R2.3)", bare)
		}
	}
	want := map[string]bool{"Read": true, "Edit": true, "Write": true, "Bash": true, "WebFetch": true}
	if got := toolSet(args); len(got) != len(want) {
		t.Errorf("--tools = %v, want exactly %v", got, want)
	} else {
		for tool := range want {
			if !got[tool] {
				t.Errorf("--tools lacks %q", tool)
			}
		}
	}
}

// TestAgentPermissionArgs_WriteIsScopedAsEdit is R2.3: the CLI never consults a
// Write(path) rule, so Write is scoped through Edit. Holding Write without Edit
// still yields the Edit scope; holding both yields it ONCE; holding neither
// (the reviewer) leaves no Edit token anywhere in argv.
func TestAgentPermissionArgs_WriteIsScopedAsEdit(t *testing.T) {
	isolateSecretsPaths(t)
	dir := t.TempDir()

	for _, tc := range []struct {
		name  string
		tools []string
	}{
		{"write-only", []string{"Read", "Write"}},
		{"edit-and-write", []string{"Read", "Edit", "Write"}},
	} {
		args := mustPermArgs(t, AgentPermissions{Agent: "registry fixer", Dir: dir, Tools: tc.tools})
		allow := flagValues(args, "--allowedTools")
		if n := countOf(allow, dirRule("Edit", dir)); n != 1 {
			t.Errorf("%s: %q appears %d times in --allowedTools, want 1 (R2.3); allow = %q", tc.name, dirRule("Edit", dir), n, allow)
		}
		for _, v := range allow {
			if v == "Write" || strings.HasPrefix(v, "Write(") {
				t.Errorf("%s: --allowedTools carries %q; writes are scoped as Edit(//dir/**) (R2.3)", tc.name, v)
			}
		}
	}

	// The word check below reads the whole argv, paths included, and t.TempDir()
	// names its directories after this test — whose name holds both "Write" and
	// "Edit". A neutral directory and secrets home keep the check about the
	// argv's rules rather than about the fixture's path (story 051 run, recorded
	// in the deviation register as a surface adjustment).
	neutral, err := os.MkdirTemp("", "bentoo051-") //nolint:usetesting // t.TempDir would name the path after this test, putting "Write" and "Edit" back into the argv

	if err != nil {
		t.Fatalf("creating a neutral directory: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(neutral) })
	t.Setenv("HOME", neutral)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(neutral, ".config"))
	dir = neutral

	args := mustPermArgs(t, AgentPermissions{Agent: "bump reviewer", Dir: dir, Tools: []string{"Read"}})
	joined := strings.Join(args, " ")
	for _, word := range []string{"Edit", "Write", "Bash", "WebFetch", "--add-dir"} {
		if strings.Contains(joined, word) {
			t.Errorf("a Read-only agent's argv contains %q: %s", word, joined)
		}
	}
	if !containsRule(flagValues(args, "--allowedTools"), dirRule("Read", dir)) {
		t.Errorf("a Read-only agent is not granted %q (R2.3)", dirRule("Read", dir))
	}
}

// TestAgentPermissionArgs_DeniesSecretsPaths is R2.5, including the value the
// rule DERIVES: secrets.Paths() re-reads HOME/XDG_CONFIG_HOME on every call, so
// a builder that cached the paths once would deny yesterday's file. The second
// call runs under a different XDG_CONFIG_HOME and must deny the NEW path.
func TestAgentPermissionArgs_DeniesSecretsPaths(t *testing.T) {
	home := isolateSecretsPaths(t)
	dir := t.TempDir()

	args := mustPermArgs(t, AgentPermissions{Agent: "build fixer", Dir: dir, Tools: []string{"Read", "Edit"}})
	assertPinnedPermissions(t, "build fixer", args, true)
	deny := ruleList(t, "build fixer", args, "deny")
	for _, want := range []string{
		fileRule("Read", "/etc/bentoo/secrets"),
		fileRule("Edit", "/etc/bentoo/secrets"),
		fileRule("Read", filepath.Join(home, ".config", "bentoo", "secrets")),
		fileRule("Edit", filepath.Join(home, ".config", "bentoo", "secrets")),
	} {
		if !containsRule(deny, want) {
			t.Errorf("deny rules lack %q (R2.5); deny = %q", want, deny)
		}
	}

	moved := filepath.Join(t.TempDir(), "xdg")
	t.Setenv("XDG_CONFIG_HOME", moved)
	args = mustPermArgs(t, AgentPermissions{Agent: "build fixer", Dir: dir, Tools: []string{"Read", "Edit"}})
	deny = ruleList(t, "build fixer", args, "deny")
	if want := fileRule("Read", filepath.Join(moved, "bentoo", "secrets")); !containsRule(deny, want) {
		t.Errorf("after XDG_CONFIG_HOME moved, deny rules lack %q; the secrets paths were cached (R2.5)", want)
	}
}

// TestAgentPermissionArgs_PinsSettings is R2.6, R4.1 and R4.2 for both a
// Read-only and an editing agent.
func TestAgentPermissionArgs_PinsSettings(t *testing.T) {
	isolateSecretsPaths(t)
	dir := t.TempDir()
	assertPinnedPermissions(t, "read-only agent", mustPermArgs(t, AgentPermissions{Agent: "bump reviewer", Dir: dir, Tools: []string{"Read"}}), false)
	assertPinnedPermissions(t, "editing agent", mustPermArgs(t, AgentPermissions{Agent: "manifest fixer", Dir: dir, Tools: []string{"Read", "Edit", "Write", "Bash", "WebFetch"}}), true)
}

// TestAgentPermissionArgs_RejectsUnsafeDir is R2.8 in both directions: a
// relative path or any character outside [A-Za-z0-9._+@/-] is refused with an
// error naming the agent and the path; a path using EVERY allowed punctuation
// mark is accepted, so the check cannot pass by refusing everything unusual.
func TestAgentPermissionArgs_RejectsUnsafeDir(t *testing.T) {
	isolateSecretsPaths(t)
	for _, dir := range []string{
		"relative/pkg",
		"/var/db/repos/my overlay/dev-libs/foo",
		"/tmp/pkg$(id)",
		"/tmp/pkg*",
		"/tmp/pkg)(",
		"/tmp/pkg,other",
		"/tmp/pkg\nRead(//etc/**)",
		"",
	} {
		_, err := AgentPermissionArgs(AgentPermissions{Agent: "manifest fixer", Dir: dir, Tools: []string{"Read", "Edit"}})
		if err == nil {
			t.Errorf("dir %q was accepted; it is not absolute or carries a character outside [A-Za-z0-9._+@/-] (R2.8)", dir)
			continue
		}
		if !strings.Contains(err.Error(), "manifest fixer") {
			t.Errorf("error for dir %q does not name the fixer: %v (R2.8)", dir, err)
		}
		if dir != "" && !strings.Contains(err.Error(), strings.SplitN(dir, "\n", 2)[0]) {
			t.Errorf("error for dir %q does not name the rejected path: %v (R2.8)", dir, err)
		}
	}

	ok := "/var/db/repos/bentoo_overlay/dev-libs/libfoo+bar@2.0-r1"
	args, err := AgentPermissionArgs(AgentPermissions{Agent: "manifest fixer", Dir: ok, Tools: []string{"Read", "Edit"}})
	if err != nil {
		t.Fatalf("dir %q uses only allowed characters and was refused: %v (R2.8)", ok, err)
	}
	if !containsRule(flagValues(args, "--allowedTools"), dirRule("Read", ok)) {
		t.Errorf("accepted dir %q is not the Read scope (R2.3)", ok)
	}
}
