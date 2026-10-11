package fixer

import (
	"context"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/obentoo/bentoolkit/internal/autoupdate/llm"
)

// argvGoldenPath holds the three unattended agents' argv as recorded from main
// before the interactive repair session was added. Set S091_RECORD_GOLDEN=1 to
// rewrite it; a rewrite is a change to an unattended grant and needs review.
const argvGoldenPath = "testdata/argv_unchanged_s091.golden"

// normalizeArgv replaces every per-run temp path with a stable placeholder,
// longest first, so the golden compares the argv's shape and text only.
func normalizeArgv(args []string, places map[string]string) string {
	keys := make([]string, 0, len(places))
	for k := range places {
		if k != "" {
			keys = append(keys, k)
		}
	}
	sort.Slice(keys, func(i, j int) bool { return len(keys[i]) > len(keys[j]) })
	var b strings.Builder
	for _, a := range args {
		for _, k := range keys {
			a = strings.ReplaceAll(a, k, places[k])
		}
		b.WriteString(strings.ReplaceAll(a, "\n", `\n`))
		b.WriteByte('\n')
	}
	return b.String()
}

func manifestFixerArgv(t *testing.T, home string) string {
	t.Helper()
	seam, spy := agentSeam(printEnvelopeScript(okEnvelope))
	f := newTestFixer(t, llm.LLMConfig{Provider: "claude-code", Bare: "false"}, WithFixerExecCommand(seam))
	req := sampleFixRequest(t)
	if _, err := f.FixManifest(context.Background(), req); err != nil {
		t.Fatalf("FixManifest: %v", err)
	}
	return normalizeArgv(spy.args, map[string]string{req.PkgDir: "<PKG>", home: "<HOME>"})
}

func buildFixerArgv(t *testing.T, home string) string {
	t.Helper()
	seam, spy := agentSeam(printEnvelopeScript(okEnvelope))
	f, err := NewClaudeCodeBuildFixer(llm.LLMConfig{Provider: "claude-code", Bare: "false"}, WithBuildFixerExecCommand(seam))
	if err != nil {
		t.Fatalf("NewClaudeCodeBuildFixer: %v", err)
	}
	req := sampleBuildFixRequest(t)
	if _, err := f.FixBuild(context.Background(), req); err != nil {
		t.Fatalf("FixBuild: %v", err)
	}
	return normalizeArgv(spy.args, map[string]string{req.StagedDir: "<STAGED>", home: "<HOME>"})
}

func bumpReviewerArgv(t *testing.T, home string) string {
	t.Helper()
	seam, spy := agentSeam(printEnvelopeScript(reviewEnvelope))
	r, err := NewClaudeCodeBumpReviewer(llm.LLMConfig{Provider: "claude-code", Bare: "false"}, WithBumpReviewerExecCommand(seam))
	if err != nil {
		t.Fatalf("NewClaudeCodeBumpReviewer: %v", err)
	}
	if _, err := r.ReviewBump(context.Background(), sampleBumpReviewRequest()); err != nil {
		t.Fatalf("ReviewBump: %v", err)
	}
	return normalizeArgv(spy.args, map[string]string{spy.last().Dir: "<PRIVATE>", home: "<HOME>"})
}

// TestUnattendedArgvUnchanged pins the manifest fixer's, the build fixer's and
// the bump reviewer's argv byte for byte against a golden recorded from main
// before the interactive repair session existed (R6.1): that session's wider
// grant must never leak into an agent that runs with nobody watching.
func TestUnattendedArgvUnchanged(t *testing.T) {
	home := isolateSecretsPaths(t)
	stubLookPathFound(t)
	got := "== manifest fixer\n" + manifestFixerArgv(t, home) +
		"== build fixer\n" + buildFixerArgv(t, home) +
		"== bump reviewer\n" + bumpReviewerArgv(t, home)

	if os.Getenv("S091_RECORD_GOLDEN") == "1" {
		if err := os.MkdirAll(filepath.Dir(argvGoldenPath), 0o755); err != nil {
			t.Fatalf("creating %s: %v", filepath.Dir(argvGoldenPath), err)
		}
		if err := os.WriteFile(argvGoldenPath, []byte(got), 0o644); err != nil {
			t.Fatalf("recording %s: %v", argvGoldenPath, err)
		}
		return
	}
	want, err := os.ReadFile(argvGoldenPath)
	if err != nil {
		t.Fatalf("reading %s: %v", argvGoldenPath, err)
	}
	if got != string(want) {
		t.Errorf("the unattended agents' argv changed (R6.1)\n--- want (%s)\n%s\n--- got\n%s", argvGoldenPath, want, got)
	}
}
