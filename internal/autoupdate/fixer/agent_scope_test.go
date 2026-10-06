package fixer

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/obentoo/bentoolkit/internal/autoupdate/llm"
)

// assertExactRuleSet compares the allow rules to want as sets, and reports any
// rule given twice.
func assertExactRuleSet(t *testing.T, who string, got []string, want []string) {
	t.Helper()
	wantSet := map[string]bool{}
	for _, w := range want {
		wantSet[w] = true
	}
	seen := map[string]int{}
	for _, g := range got {
		seen[g]++
		if !wantSet[g] {
			t.Errorf("%s: allow rule %q is not one the requirements grant", who, g)
		}
	}
	for g, n := range seen {
		if n > 1 {
			t.Errorf("%s: allow rule %q given %d times", who, g, n)
		}
	}
	for _, w := range want {
		if seen[w] == 0 {
			t.Errorf("%s: allow rule %q is missing; allow = %q", who, w, got)
		}
	}
}

// assertExactTools compares the --tools set to want.
func assertExactTools(t *testing.T, who string, args []string, want ...string) {
	t.Helper()
	got := toolSet(args)
	if len(got) != len(want) {
		t.Errorf("%s: --tools = %v, want exactly %v (R2.2)", who, got, want)
		return
	}
	for _, w := range want {
		if !got[w] {
			t.Errorf("%s: --tools = %v lacks %q (R2.2)", who, got, w)
		}
	}
}

func webFetchRules(hosts ...string) []string {
	out := make([]string, 0, len(hosts))
	for _, h := range hosts {
		out = append(out, "WebFetch(domain:"+h+")")
	}
	return out
}

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
// 3.2 — the manifest fixer
// ---------------------------------------------------------------------------

// TestFixManifest_PermissionArgvIsScoped is R2.2, R2.3, R3.1..R3.3, R3.5 (the
// error-text half) and R3.8 for the manifest fixer. The pkgdev error names two
// http(s) hosts and one ftp:// and one file:// URL: only the first two become
// WebFetch rules. A PkgDir with a space spawns nothing (R2.8).
func TestFixManifest_PermissionArgvIsScoped(t *testing.T) {
	isolateSecretsPaths(t)
	seam, spy := agentSeam(printEnvelopeScript(okEnvelope))
	f := newTestFixer(t, llm.LLMConfig{Provider: "claude-code", Bare: "false"}, WithFixerExecCommand(seam))

	req := sampleFixRequest(t)
	req.ManifestError = "!!! Couldn't download 'godot-4.7.tar.xz'. Aborting.\n" +
		"404 Not Found: https://dl.example.org/godot/4.7/godot-4.7.tar.xz\n" +
		"retried http://mirror.example.net:8080/godot-4.7.tar.xz\n" +
		"also tried ftp://ftp.example.edu/pub/godot-4.7.tar.xz and file:///etc/passwd"
	if _, err := f.FixManifest(context.Background(), req); err != nil {
		t.Fatalf("FixManifest: %v", err)
	}
	args := spy.args
	assertExactTools(t, "manifest fixer", args, "Read", "Edit", "Write", "Bash", "WebFetch")
	want := append([]string{dirRule("Read", req.PkgDir), dirRule("Edit", req.PkgDir), "Bash(pkgdev *)"},
		webFetchRules("dl.example.org", "mirror.example.net", "github.com", "codeload.github.com", "objects.githubusercontent.com")...)
	assertExactRuleSet(t, "manifest fixer", ruleList(t, "manifest fixer", args, "allow"), want)
	assertPinnedPermissions(t, "manifest fixer", args, true)

	for _, flag := range []string{"-p", "--append-system-prompt"} {
		if v, _ := flagValue(args, flag); shellWord.MatchString(v) {
			t.Errorf("manifest fixer %s mentions %q as a usable command (R3.8)", flag, shellWord.FindString(v))
		}
	}
	if v := buildManifestFixInstruction(sampleFixRequest(t)); shellWord.MatchString(v) {
		t.Errorf("buildManifestFixInstruction mentions %q (R3.8)", shellWord.FindString(v))
	}

	unsafe := sampleFixRequest(t)
	unsafe.PkgDir = filepath.Join(t.TempDir(), "my overlay")
	if err := os.MkdirAll(unsafe.PkgDir, 0o755); err != nil {
		t.Fatal(err)
	}
	seam2, spy2 := agentSeam(printEnvelopeScript(okEnvelope))
	f2 := newTestFixer(t, llm.LLMConfig{Provider: "claude-code", Bare: "false"}, WithFixerExecCommand(seam2))
	_, err := f2.FixManifest(context.Background(), unsafe)
	if err == nil || spy2.spawns() != 0 {
		t.Fatalf("PkgDir %q (a space) was accepted: err=%v spawns=%d (R2.8)", unsafe.PkgDir, err, spy2.spawns())
	}
	if !strings.Contains(err.Error(), unsafe.PkgDir) || !strings.Contains(strings.ToLower(err.Error()), "manifest") {
		t.Errorf("error does not name the manifest fixer and the rejected path: %v (R2.8)", err)
	}
}

// ---------------------------------------------------------------------------
// 3.3 — the registry fixer
// ---------------------------------------------------------------------------

// TestFixRegistry_PermissionArgvIsScoped is R2.2, R2.3, R3.1, R3.3 and R3.4 for
// the registry fixer. Hostile half: a URL inside FetchError is NOT a registry
// host (R3.4 takes only URL and FallbackURL). A nil Config leaves the GitHub
// hosts alone.
func TestFixRegistry_PermissionArgvIsScoped(t *testing.T) {
	isolateSecretsPaths(t)
	stubLookPathFound(t)

	seam, spy := agentSeam(printEnvelopeScript(okEnvelope))
	f, err := NewClaudeCodeRegistryFixer(llm.LLMConfig{Provider: "claude-code", Bare: "false"}, WithRegistryFixerExecCommand(seam))
	if err != nil {
		t.Fatalf("NewClaudeCodeRegistryFixer: %v", err)
	}
	req := sampleRegistryFixRequest(t)
	req.Config.FallbackURL = "https://media.inkscape.org/dl/resources/file/"
	req.FetchError = "no match for pattern; page redirected to https://attacker.example/steal"
	if _, err := f.FixRegistry(context.Background(), req); err != nil {
		t.Fatalf("FixRegistry: %v", err)
	}
	args := spy.args
	assertExactTools(t, "registry fixer", args, "Read", "Edit", "Write", "WebFetch")
	want := append([]string{dirRule("Read", req.ConfigDir), dirRule("Edit", req.ConfigDir)},
		webFetchRules("inkscape.org", "media.inkscape.org", "github.com", "codeload.github.com", "objects.githubusercontent.com")...)
	assertExactRuleSet(t, "registry fixer", ruleList(t, "registry fixer", args, "allow"), want)
	assertPinnedPermissions(t, "registry fixer", args, true)

	nilCfg := sampleRegistryFixRequest(t)
	nilCfg.Config = nil
	seam2, spy2 := agentSeam(printEnvelopeScript(okEnvelope))
	f2, _ := NewClaudeCodeRegistryFixer(llm.LLMConfig{Provider: "claude-code", Bare: "false"}, WithRegistryFixerExecCommand(seam2))
	if _, err := f2.FixRegistry(context.Background(), nilCfg); err != nil {
		t.Fatalf("FixRegistry (nil Config): %v", err)
	}
	want2 := append([]string{dirRule("Read", nilCfg.ConfigDir), dirRule("Edit", nilCfg.ConfigDir)},
		webFetchRules("github.com", "codeload.github.com", "objects.githubusercontent.com")...)
	assertExactRuleSet(t, "registry fixer (nil Config)", ruleList(t, "registry fixer", spy2.args, "allow"), want2)
}

// TestRegistryFix_GuidanceNamesNoShellCommand is R3.8 for the registry fixer's
// system guidance and instruction.
func TestRegistryFix_GuidanceNamesNoShellCommand(t *testing.T) {
	if m := shellWord.FindString(registryFixGuidance); m != "" {
		t.Errorf("registryFixGuidance mentions %q as a usable command (R3.8)", m)
	}
	if m := shellWord.FindString(buildRegistryFixInstruction(sampleRegistryFixRequest(t))); m != "" {
		t.Errorf("buildRegistryFixInstruction mentions %q as a usable command (R3.8)", m)
	}
	// The fetch tool the agent DOES hold is still named, so the guidance did
	// not pass by saying nothing about how to inspect upstream.
	if !strings.Contains(registryFixGuidance+buildRegistryFixInstruction(sampleRegistryFixRequest(t)), "WebFetch") {
		t.Error("the registry guidance no longer names WebFetch, the one fetch tool the agent holds")
	}
}

// ---------------------------------------------------------------------------
// 3.4 — the build fixer and the bump reviewer
// ---------------------------------------------------------------------------

// TestBuildFix_ArgvScopesToStagedDir is R2.2, R2.3 and R3.7 for the build
// fixer. Hostile half: the scope is the staged PACKAGE directory, never the
// staged repository root above it.
func TestBuildFix_ArgvScopesToStagedDir(t *testing.T) {
	isolateSecretsPaths(t)
	stubLookPathFound(t)
	seam, spy := agentSeam(printEnvelopeScript(okEnvelope))
	f, err := NewClaudeCodeBuildFixer(llm.LLMConfig{Provider: "claude-code", Bare: "false"}, WithBuildFixerExecCommand(seam))
	if err != nil {
		t.Fatalf("NewClaudeCodeBuildFixer: %v", err)
	}
	req := sampleBuildFixRequest(t)
	if _, err := f.FixBuild(context.Background(), req); err != nil {
		t.Fatalf("FixBuild: %v", err)
	}
	pkgDir := filepath.Dir(req.EbuildPath)
	args := spy.args
	assertExactTools(t, "build fixer", args, "Read", "Edit")
	assertExactRuleSet(t, "build fixer", ruleList(t, "build fixer", args, "allow"),
		[]string{dirRule("Read", pkgDir), dirRule("Edit", pkgDir)})
	if containsRule(ruleList(t, "build fixer", args, "allow"), dirRule("Read", req.StagedDir)) {
		t.Errorf("the build fixer is scoped to the staged ROOT %s, not its package directory (R2.3)", req.StagedDir)
	}
	if strings.Contains(strings.Join(args, " "), "WebFetch") {
		t.Errorf("the build fixer's argv names WebFetch (R3.7)")
	}
	assertPinnedPermissions(t, "build fixer", args, true)
}

// TestBumpReviewer_RunsInAPrivateDirectoryItRemoves is R2.2, R2.3, R2.4 and
// R3.7 for the reviewer: a fresh 0700 cwd removed after the child exits, Read
// scoped to it and nothing else, and no --add-dir.
func TestBumpReviewer_RunsInAPrivateDirectoryItRemoves(t *testing.T) {
	isolateSecretsPaths(t)
	stubLookPathFound(t)
	capture := filepath.Join(t.TempDir(), "child.txt")
	seam, spy := agentSeam(privateDirScript(capture, reviewEnvelope))
	r, err := NewClaudeCodeBumpReviewer(llm.LLMConfig{Provider: "claude-code", Bare: "false"}, WithBumpReviewerExecCommand(seam))
	if err != nil {
		t.Fatalf("NewClaudeCodeBumpReviewer: %v", err)
	}
	report, err := r.ReviewBump(context.Background(), sampleBumpReviewRequest())
	if err != nil {
		t.Fatalf("ReviewBump: %v", err)
	}
	if report.Skipped {
		t.Fatalf("the review was skipped: %s", report.SkipReason)
	}
	dir := spy.last().Dir
	assertPrivateDirRun(t, "bump reviewer", capture, dir)

	args := spy.args
	assertExactTools(t, "bump reviewer", args, "Read")
	assertExactRuleSet(t, "bump reviewer", ruleList(t, "bump reviewer", args, "allow"), []string{dirRule("Read", dir)})
	joined := strings.Join(args, " ")
	for _, word := range []string{"--add-dir", "Edit", "Write", "Bash", "WebFetch"} {
		if strings.Contains(joined, word) {
			t.Errorf("the reviewer's argv contains %q (R2.4, R3.7)", word)
		}
	}
	assertPinnedPermissions(t, "bump reviewer", args, false)
}
