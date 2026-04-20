package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// setupTestOverlay creates a tmpDir with a minimal overlay structure suitable
// for tests that need a filesystem-level overlay without a real git remote.
// The returned path is automatically cleaned up when the test finishes.
func setupTestOverlay(t *testing.T) string {
	t.Helper()

	tmpDir := t.TempDir()

	// Create profiles directory with repo_name file
	profilesDir := filepath.Join(tmpDir, "profiles")
	if err := os.MkdirAll(profilesDir, 0755); err != nil {
		t.Fatalf("setupTestOverlay: failed to create profiles dir: %v", err)
	}

	repoNameFile := filepath.Join(profilesDir, "repo_name")
	if err := os.WriteFile(repoNameFile, []byte("test-overlay\n"), 0644); err != nil {
		t.Fatalf("setupTestOverlay: failed to write repo_name: %v", err)
	}

	// Create metadata/layout.conf
	metadataDir := filepath.Join(tmpDir, "metadata")
	if err := os.MkdirAll(metadataDir, 0755); err != nil {
		t.Fatalf("setupTestOverlay: failed to create metadata dir: %v", err)
	}

	layoutConf := filepath.Join(metadataDir, "layout.conf")
	layoutContent := "masters = gentoo\nrepo-name = test-overlay\n"
	if err := os.WriteFile(layoutConf, []byte(layoutContent), 0644); err != nil {
		t.Fatalf("setupTestOverlay: failed to write layout.conf: %v", err)
	}

	// Initialize a bare git repo so git commands succeed
	cmd := exec.Command("git", "init", tmpDir)
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=Test",
		"GIT_AUTHOR_EMAIL=test@example.com",
		"GIT_COMMITTER_NAME=Test",
		"GIT_COMMITTER_EMAIL=test@example.com",
	)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("setupTestOverlay: git init failed: %v\n%s", err, out)
	}

	// Configure local git identity so commits work
	for _, args := range [][]string{
		{"git", "-C", tmpDir, "config", "user.email", "test@example.com"},
		{"git", "-C", tmpDir, "config", "user.name", "Test User"},
	} {
		c := exec.Command(args[0], args[1:]...)
		if out, err := c.CombinedOutput(); err != nil {
			t.Fatalf("setupTestOverlay: %v failed: %v\n%s", args, err, out)
		}
	}

	return tmpDir
}
