package main

// Authored for story 058, sub-task 2.1 — R1.1, R1.3, R2.5.
//
// The twelve git-workflow and small commands are RunE commands, and each row
// of the design.md exit-code table that can be driven without a network is
// driven end to end through execute, with no exit intercept: the code must
// come back as a returned error. The rows were measured at 6be73ec.
//
// Red on arrival: every one of these commands is still a Run command.

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

var s058GitCommands = []string{
	"overlay add", "overlay commit", "overlay diff", "overlay init", "overlay log",
	"overlay pull", "overlay push", "overlay rename", "overlay status",
	"distfile fetch", "version", "completion",
}

func TestS058GitCommandsReturnThroughRunE(t *testing.T) {
	if bad := s058NotOnRunE(t, s058GitCommands...); len(bad) > 0 {
		t.Errorf("these commands still end the process themselves instead of returning their outcome (R1.1):\n  %s", strings.Join(bad, "\n  "))
	}
}

// s058Git runs git in dir with a fixed identity and no system or global config.
func s058Git(t *testing.T, dir string, args ...string) {
	t.Helper()
	full := append([]string{"-c", "user.name=S058", "-c", "user.email=s058@example.invalid", "-c", "commit.gpgsign=false"}, args...)
	cmd := exec.Command("git", full...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
}

// s058GitRepo turns the overlay into a git repository with one commit.
func s058GitRepo(t *testing.T, c *testCLI) []string {
	t.Helper()
	dir := c.Overlay()
	s058Git(t, dir, "init", "-q")
	if err := os.WriteFile(filepath.Join(dir, "README"), []byte("s058\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	s058Git(t, dir, "add", "README")
	s058Git(t, dir, "commit", "-q", "-m", "init")
	return nil
}

func s058GitRows() []s058Row {
	return []s058Row{
		// Would wrongly become 1 once a path returns an error value: these
		// paths succeed, including the explicit "nothing to do" exit 0.
		{name: "commit with nothing staged", args: []string{"overlay", "commit"}, setup: s058GitRepo, want: 0},
		{name: "diff with an unstaged change", args: []string{"overlay", "diff"}, setup: func(t *testing.T, c *testCLI) []string {
			s058GitRepo(t, c)
			if err := os.WriteFile(filepath.Join(c.Overlay(), "README"), []byte("changed\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			return nil
		}, want: 0},
		{name: "status on a clean repository", args: []string{"overlay", "status"}, setup: s058GitRepo, want: 0},
		{name: "log on a repository with one commit", args: []string{"overlay", "log"}, setup: s058GitRepo, want: 0},
		{name: "version", args: []string{"version"}, want: 0},
		{name: "completion bash", args: []string{"completion", "bash"}, want: 0},
		// Failures stay 1, each diagnostic printed once.
		{name: "status outside a git repository", args: []string{"overlay", "status"}, want: 1, once: "git command failed"},
		{name: "commit outside a git repository", args: []string{"overlay", "commit"}, want: 1, once: "getting status: git command failed"},
		{name: "log outside a git repository", args: []string{"overlay", "log"}, want: 1, once: "running git log: exit status 128"},
		{name: "diff outside a git repository", args: []string{"overlay", "diff"}, want: 1, once: "running git diff:"},
		{name: "add outside a git repository", args: []string{"overlay", "add"}, want: 1},
		{name: "push outside a git repository", args: []string{"overlay", "push"}, want: 1},
		{name: "pull outside a git repository", args: []string{"overlay", "pull"}, want: 1},
		{name: "distfile fetch of a package with no record", args: []string{"distfile", "fetch", "app-misc/nosuch"}, setup: func(t *testing.T, _ *testCLI) []string {
			return []string{"--distdir", t.TempDir()}
		}, want: 1},
		// Usage rejected by cobra stays 1.
		{name: "completion for an unknown shell", args: []string{"completion", "bogus"}, want: 1, usage: true},
		{name: "rename with two arguments", args: []string{"overlay", "rename", "a", "b"}, want: 1, usage: true},
		{name: "distfile fetch with no package", args: []string{"distfile", "fetch"}, want: 1, usage: true},
	}
}

func TestS058GitCommandsKeepTheirExitCodes(t *testing.T) {
	s058RunRows(t, s058GitRows())
}
