package repair

import (
	"slices"
	"strings"
	"testing"
)

func sessionSpec() SessionSpec {
	b := sampleBundle("/var/tmp/bentoo-staging/dev-go/govulncheck/1.9.0")
	b.Failure = "manifest command failed: UNIQUE-FAILURE-TEXT-42 404 Not Found"
	return SessionSpec{
		Bundle:      b,
		PkgDir:      "/var/tmp/bentoo-staging/dev-go/govulncheck/1.9.0/dev-go/govulncheck",
		OverlayPath: "/var/db/repos/bentoo",
		Secrets:     []string{"/home/u/.config/bentoo/secrets", "/etc/bentoo/secrets"},
		Guidance:    "GUIDANCE-TEXT honour the 10 gotchas",
	}
}

// flagValue returns the element after the first occurrence of flag.
func flagValue(t *testing.T, args []string, flag string) string {
	t.Helper()
	i := slices.Index(args, flag)
	if i < 0 || i+1 >= len(args) {
		t.Fatalf("argv lacks %s <value>: %q", flag, args)
	}
	return args[i+1]
}

// TestSessionArgsPromptComesFirstAndNothingFollowsTheDenyRules: the deny-rule
// flag is variadic, so the prompt must be the first element and every element
// after --disallowedTools must be a rule.
func TestSessionArgsPromptComesFirstAndNothingFollowsTheDenyRules(t *testing.T) {
	args := SessionArgs(sessionSpec())
	if len(args) == 0 {
		t.Fatalf("SessionArgs returned an empty argv")
	}
	if strings.HasPrefix(args[0], "-") {
		t.Fatalf("argv[0] = %q, want the prompt as the first positional argument", args[0])
	}
	if !strings.Contains(args[0], "UNIQUE-FAILURE-TEXT-42") {
		t.Fatalf("argv[0] is not the prompt carrying the bundle: %q", args[0])
	}
	for i, a := range args[1:] {
		if strings.Contains(a, "UNIQUE-FAILURE-TEXT-42") {
			t.Fatalf("the prompt appears again at argv[%d]: a variadic flag would swallow it", i+1)
		}
	}
	d := slices.Index(args, "--disallowedTools")
	if d < 0 {
		t.Fatalf("argv lacks --disallowedTools: %q", args)
	}
	for _, a := range args[d+1:] {
		if !strings.HasPrefix(a, "Read(") && !strings.HasPrefix(a, "Edit(") {
			t.Fatalf("%q follows --disallowedTools and would be swallowed as a deny rule: %q", a, args[d:])
		}
	}
}

// TestSessionArgsDeniesOverlayEditsAndSecretAccess covers R4.4.
func TestSessionArgsDeniesOverlayEditsAndSecretAccess(t *testing.T) {
	spec := sessionSpec()
	args := SessionArgs(spec)
	d := slices.Index(args, "--disallowedTools")
	if d < 0 {
		t.Fatalf("argv lacks --disallowedTools: %q", args)
	}
	deny := args[d+1:]
	want := []string{"Edit(//var/db/repos/bentoo/**)"}
	for _, p := range spec.Secrets {
		want = append(want, "Read(/"+p+")", "Edit(/"+p+")")
	}
	for _, w := range want {
		if !slices.Contains(deny, w) {
			t.Errorf("deny rules lack %q: %q", w, deny)
		}
	}
	for _, r := range deny {
		if strings.Contains(r, spec.Bundle.StagedRoot) {
			t.Errorf("deny rule %q covers the staged tree the session must repair", r)
		}
	}
}

// TestSessionArgsInteractiveDefaultPermissionModeOnly covers R4.2, R4.3 and
// the constraints: no allow rules, no non-interactive mode, the operator's own
// configuration is kept.
func TestSessionArgsInteractiveDefaultPermissionModeOnly(t *testing.T) {
	args := SessionArgs(sessionSpec())
	if got := flagValue(t, args, "--permission-mode"); got != "default" {
		t.Fatalf("--permission-mode %q, want default", got)
	}
	n := 0
	for _, a := range args {
		if a == "--permission-mode" || strings.HasPrefix(a, "--permission-mode=") {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("--permission-mode appears %d times, want exactly once: %q", n, args)
	}
	for _, bad := range []string{
		"--dangerously-skip-permissions", "--allow-dangerously-skip-permissions",
		"bypassPermissions", "acceptEdits", "dontAsk", "plan",
		"--allowedTools", "--allowed-tools", "-p", "--print",
		"--setting-sources", "--settings", "--strict-mcp-config",
	} {
		for _, a := range args[1:] {
			if a == bad || strings.HasPrefix(a, bad+"=") {
				t.Errorf("argv carries forbidden %q: %q", bad, args)
			}
		}
	}
}

// TestSessionArgsPromptCarriesBundleAndInstruction covers R4.5 plus the
// staged-tree scope and the shared ebuild guidance.
func TestSessionArgsPromptCarriesBundleAndInstruction(t *testing.T) {
	spec := sessionSpec()
	args := SessionArgs(spec)
	if len(args) == 0 {
		t.Fatalf("SessionArgs returned an empty argv")
	}
	prompt := args[0]
	for _, want := range []string{
		spec.Bundle.Package, spec.Bundle.CurrentVersion, spec.Bundle.Candidate, spec.Bundle.Stage,
		"UNIQUE-FAILURE-TEXT-42", spec.Bundle.StagedRoot, spec.Bundle.EbuildPath,
		"Bash", "WebFetch(raw.githubusercontent.com)", spec.Bundle.UpstreamURLs[0],
		"pkgdev manifest",
	} {
		if !strings.Contains(prompt, want) {
			t.Errorf("prompt lacks %q", want)
		}
	}
	if got := flagValue(t, args, "--append-system-prompt"); got != spec.Guidance {
		t.Errorf("--append-system-prompt = %q, want the guidance verbatim", got)
	}
	if got := flagValue(t, args, "--add-dir"); got != spec.Bundle.StagedRoot {
		t.Errorf("--add-dir = %q, want the staged root %q", got, spec.Bundle.StagedRoot)
	}
	for i, a := range args {
		if a == "--add-dir" && i+1 < len(args) && strings.HasPrefix(args[i+1], spec.OverlayPath) {
			t.Errorf("--add-dir grants the published overlay %q", args[i+1])
		}
	}
}
