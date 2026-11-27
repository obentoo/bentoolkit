package repair

import (
	"slices"
	"strings"
	"testing"
)

// batchSpec is two bundles, each with its own staged tree and failure text,
// and the first with a fixer session that must not be resumed.
func batchSpec() BatchSessionSpec {
	a := sampleBundle("/var/tmp/bentoo-staging/dev-go/govulncheck/1.9.0")
	a.Failure = "FAILURE-A 404 Not Found"
	a.FixerSession = "fixer-session-a"
	b := sampleBundle("/var/tmp/bentoo-staging/app-misc/jq/1.8.0")
	b.Package, b.Candidate, b.Failure = "app-misc/jq", "1.8.0", "FAILURE-B compile error"
	return BatchSessionSpec{
		Bundles:     []Bundle{a, b},
		OverlayPath: "/var/db/repos/bentoo",
		Secrets:     []string{"/home/u/.config/bentoo/secrets"},
		Guidance:    "GUIDANCE-TEXT",
	}
}

// TestBatchSessionArgsCarriesEveryBundleInOnePrompt: the first element is the
// one prompt, and it names the count and every bundle's failure.
func TestBatchSessionArgsCarriesEveryBundleInOnePrompt(t *testing.T) {
	args := BatchSessionArgs(batchSpec())
	if len(args) == 0 || strings.HasPrefix(args[0], "-") {
		t.Fatalf("argv[0] is not the prompt: %q", args)
	}
	for _, want := range []string{"these 2 failed", "FAILURE-A", "FAILURE-B", "app-misc/jq", "dev-go/govulncheck"} {
		if !strings.Contains(args[0], want) {
			t.Errorf("the prompt lacks %q", want)
		}
	}
	for i, a := range args[1:] {
		if strings.Contains(a, "FAILURE-") {
			t.Fatalf("a failure text appears again at argv[%d]", i+1)
		}
	}
}

// TestBatchSessionArgsAddsEveryStagedTreeAndResumesNothing: one --add-dir per
// tree, default permission mode, no resume of any fixer session.
func TestBatchSessionArgsAddsEveryStagedTreeAndResumesNothing(t *testing.T) {
	s := batchSpec()
	args := BatchSessionArgs(s)
	var dirs []string
	for i, a := range args {
		if a == "--add-dir" && i+1 < len(args) {
			dirs = append(dirs, args[i+1])
		}
	}
	want := []string{s.Bundles[0].StagedRoot, s.Bundles[1].StagedRoot}
	if !slices.Equal(dirs, want) {
		t.Errorf("--add-dir values = %q, want %q", dirs, want)
	}
	if got := flagValue(t, args, "--permission-mode"); got != "default" {
		t.Errorf("--permission-mode = %q, want default", got)
	}
	if slices.Contains(args, "--resume") || slices.Contains(args, "fixer-session-a") {
		t.Errorf("the batch session resumes a fixer session: %q", args)
	}
	for _, banned := range []string{"--dangerously-skip-permissions", "bypassPermissions", "acceptEdits", "dontAsk"} {
		if slices.Contains(args, banned) {
			t.Errorf("argv carries %q", banned)
		}
	}
}

// TestBatchSessionArgsDenyRulesComeLast: only rules follow --disallowedTools,
// and they cover the overlay and every secrets file.
func TestBatchSessionArgsDenyRulesComeLast(t *testing.T) {
	args := BatchSessionArgs(batchSpec())
	d := slices.Index(args, "--disallowedTools")
	if d < 0 {
		t.Fatalf("argv lacks --disallowedTools: %q", args)
	}
	rules := args[d+1:]
	want := []string{"Edit(//var/db/repos/bentoo/**)", "Read(//home/u/.config/bentoo/secrets)", "Edit(//home/u/.config/bentoo/secrets)"}
	if !slices.Equal(rules, want) {
		t.Errorf("deny rules = %q, want %q", rules, want)
	}
}

// TestBatchPromptNamesEachPublishedPackageDirectory: the cp destination of
// every package is its directory in the configured overlay, slot suffix
// dropped, so a session cannot point at /var/db/repos (2026-11-26).
func TestBatchPromptNamesEachPublishedPackageDirectory(t *testing.T) {
	s := batchSpec()
	s.OverlayPath = "/home/u/overlay"
	s.Bundles[1].Package = "net-libs/nodejs:26"
	prompt := BatchSessionArgs(s)[0]
	for _, want := range []string{
		"- dev-go/govulncheck: /home/u/overlay/dev-go/govulncheck\n",
		"- net-libs/nodejs:26: /home/u/overlay/net-libs/nodejs\n",
		"Never copy into /var/db/repos",
	} {
		if !strings.Contains(prompt, want) {
			t.Errorf("the batch prompt lacks %q", want)
		}
	}
}
