package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	yaml "go.yaml.in/yaml/v3"
)

// TestAuditWiringMakeAuditRunsAuditComments reads make's own rule database
// (make -q -p prints it without running any recipe), so the check sees the
// audit target's real prerequisites after variable expansion and line
// continuations -- a mention of audit-comments in a comment, in .PHONY or in
// help text does not count.
func TestAuditWiringMakeAuditRunsAuditComments(t *testing.T) {
	if _, err := exec.LookPath("make"); err != nil {
		t.Skip("make is not on PATH")
	}
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatalf("resolving repository root: %v", err)
	}
	cmd := exec.Command("make", "-s", "--no-print-directory", "-q", "-p", "-C", root, "audit")
	env := []string{"LC_ALL=C"}
	for _, kv := range os.Environ() {
		name, _, _ := strings.Cut(kv, "=")
		switch name {
		case "MAKEFLAGS", "MFLAGS", "MAKELEVEL", "MAKEOVERRIDES", "GNUMAKEFLAGS", "LC_ALL", "LANG", "LANGUAGE":
			continue
		}
		env = append(env, kv)
	}
	cmd.Env = env
	// -q exits 1 when the goal is out of date; only the printed database matters.
	out, _ := cmd.CombinedOutput()

	var prereqs []string
	found := false
	for _, line := range strings.Split(string(out), "\n") {
		if rest, ok := strings.CutPrefix(line, "audit:"); ok && !strings.HasPrefix(rest, "=") {
			found = true
			prereqs = append(prereqs, strings.Fields(rest)...)
		}
	}
	if !found {
		t.Fatalf("make's database has no rule for the audit target\n%s", auditWiringTail(string(out), 20))
	}
	for _, want := range []string{"audit-ctx", "audit-comments"} {
		has := false
		for _, p := range prereqs {
			if p == want {
				has = true
			}
		}
		if !has {
			t.Errorf("make audit does not run %s: prerequisites are %q", want, prereqs)
		}
	}
}

// TestAuditWiringCILintJobRunsAuditCommentsAsOwnStep parses the CI workflow:
// the lint job must carry one step whose whole command is `make
// audit-comments`, beside the step whose whole command is `make audit-ctx`.
// A combined command line in a single step does not satisfy either.
func TestAuditWiringCILintJobRunsAuditCommentsAsOwnStep(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "..", ".github", "workflows", "ci.yml"))
	if err != nil {
		t.Fatalf("reading ci.yml: %v", err)
	}
	var wf struct {
		Jobs map[string]struct {
			Steps []struct {
				Name string `yaml:"name"`
				Uses string `yaml:"uses"`
				Run  string `yaml:"run"`
			} `yaml:"steps"`
		} `yaml:"jobs"`
	}
	if err := yaml.Unmarshal(data, &wf); err != nil {
		t.Fatalf("parsing ci.yml: %v", err)
	}
	lint, ok := wf.Jobs["lint"]
	if !ok {
		t.Fatal("ci.yml has no lint job")
	}
	count := map[string]int{}
	for _, s := range lint.Steps {
		count[strings.TrimSpace(s.Run)]++
	}
	for _, want := range []string{"make audit-ctx", "make audit-comments"} {
		if count[want] != 1 {
			t.Errorf("lint job: want exactly one step whose command is %q, found %d", want, count[want])
		}
	}
}

// auditWiringTail returns the last n lines of s, for short failure messages.
func auditWiringTail(s string, n int) string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n")
}
