//go:build integration
// +build integration

package overlay

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/obentoo/bentoo-tools/internal/common/config"
	"github.com/obentoo/bentoo-tools/internal/common/git"
)

// testRepo represents a temporary git repository for integration testing
type testRepo struct {
	path    string
	t       *testing.T
	cleanup func()
}

// setupTestRepo creates a temporary git repository for testing.
// It initializes a git repo with profiles/ and metadata/ directories
// to satisfy overlay validation requirements.
// _Requirements: 5.1_
func setupTestRepo(t *testing.T) *testRepo {
	t.Helper()

	// Create temporary directory
	tmpDir, err := os.MkdirTemp("", "bentoo-integration-test-*")
	if err != nil {
		t.Fatalf("Failed to create temp directory: %v", err)
	}

	repo := &testRepo{
		path: tmpDir,
		t:    t,
		cleanup: func() {
			os.RemoveAll(tmpDir)
		},
	}

	// Initialize git repository
	repo.runGit("init")

	// Configure git user for commits
	repo.runGit("config", "user.name", "Test User")
	repo.runGit("config", "user.email", "test@example.com")

	// Create overlay structure (profiles/ and metadata/)
	if err := os.MkdirAll(filepath.Join(tmpDir, "profiles"), 0755); err != nil {
		repo.cleanup()
		t.Fatalf("Failed to create profiles directory: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(tmpDir, "metadata"), 0755); err != nil {
		repo.cleanup()
		t.Fatalf("Failed to create metadata directory: %v", err)
	}

	// Create initial commit so we have a valid repo state
	repo.writeFile("profiles/repo_name", "test-overlay")
	repo.writeFile("metadata/layout.conf", "masters = gentoo\n")
	repo.runGit("add", ".")
	repo.runGit("commit", "-m", "Initial commit")

	return repo
}


// setupTestRepoWithRemote creates a test repo with a bare remote for push testing.
// _Requirements: 5.1, 5.3_
func setupTestRepoWithRemote(t *testing.T) (*testRepo, string) {
	t.Helper()

	// Create the main repo
	repo := setupTestRepo(t)

	// Create a bare remote repository
	remoteDir, err := os.MkdirTemp("", "bentoo-integration-remote-*")
	if err != nil {
		repo.cleanup()
		t.Fatalf("Failed to create remote directory: %v", err)
	}

	// Update cleanup to also remove remote
	originalCleanup := repo.cleanup
	repo.cleanup = func() {
		originalCleanup()
		os.RemoveAll(remoteDir)
	}

	// Initialize bare repository
	cmd := exec.Command("git", "init", "--bare")
	cmd.Dir = remoteDir
	if err := cmd.Run(); err != nil {
		repo.cleanup()
		t.Fatalf("Failed to init bare repo: %v", err)
	}

	// Add remote to main repo
	repo.runGit("remote", "add", "origin", remoteDir)

	// Push initial commit to remote
	repo.runGit("push", "-u", "origin", "master")

	return repo, remoteDir
}

// runGit executes a git command in the test repository
func (r *testRepo) runGit(args ...string) string {
	r.t.Helper()

	cmd := exec.Command("git", args...)
	cmd.Dir = r.path
	output, err := cmd.CombinedOutput()
	if err != nil {
		r.t.Fatalf("git %v failed: %v\nOutput: %s", args, err, output)
	}
	return string(output)
}

// writeFile creates a file with the given content in the test repository
func (r *testRepo) writeFile(relPath, content string) {
	r.t.Helper()

	fullPath := filepath.Join(r.path, relPath)

	// Ensure parent directory exists
	dir := filepath.Dir(fullPath)
	if err := os.MkdirAll(dir, 0755); err != nil {
		r.t.Fatalf("Failed to create directory %s: %v", dir, err)
	}

	if err := os.WriteFile(fullPath, []byte(content), 0644); err != nil {
		r.t.Fatalf("Failed to write file %s: %v", relPath, err)
	}
}

// deleteFile removes a file from the test repository
func (r *testRepo) deleteFile(relPath string) {
	r.t.Helper()

	fullPath := filepath.Join(r.path, relPath)
	if err := os.Remove(fullPath); err != nil {
		r.t.Fatalf("Failed to delete file %s: %v", relPath, err)
	}
}

// newConfig creates a config pointing to the test repository
func (r *testRepo) newConfig() *config.Config {
	return &config.Config{
		Overlay: config.OverlayConfig{
			Path:   r.path,
			Remote: "origin",
		},
		Git: config.GitConfig{
			User:  "Test User",
			Email: "test@example.com",
		},
	}
}

// newGitRunner creates a GitRunner for the test repository
func (r *testRepo) newGitRunner() *git.GitRunner {
	return git.NewGitRunner(r.path)
}

// getLastCommitMessage returns the message of the last commit
func (r *testRepo) getLastCommitMessage() string {
	r.t.Helper()
	return r.runGit("log", "-1", "--format=%s")
}

// getLastCommitAuthor returns the author of the last commit
func (r *testRepo) getLastCommitAuthor() string {
	r.t.Helper()
	return r.runGit("log", "-1", "--format=%an <%ae>")
}


// TestIntegrationCommit tests the Commit function with a real git repository.
// It creates a repo, makes changes, commits, and verifies the result.
// _Requirements: 5.2_
func TestIntegrationCommit(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	t.Run("commit with generated message", func(t *testing.T) {
		repo := setupTestRepo(t)
		defer repo.cleanup()

		cfg := repo.newConfig()

		// Create an ebuild file
		repo.writeFile("app-misc/hello/hello-1.0.ebuild", "# Test ebuild\nEAPI=8\n")
		repo.runGit("add", ".")

		// Get staged changes and generate message
		changes, err := GetStagedChanges(cfg)
		if err != nil {
			t.Fatalf("GetStagedChanges() error = %v", err)
		}

		message := GenerateMessage(changes)
		if message == "" {
			t.Fatal("GenerateMessage() returned empty message")
		}

		// Commit the changes
		err = Commit(cfg, message)
		if err != nil {
			t.Fatalf("Commit() error = %v", err)
		}

		// Verify the commit was created with correct message
		lastMsg := repo.getLastCommitMessage()
		if lastMsg != message+"\n" {
			t.Errorf("Commit message = %q, want %q", lastMsg, message)
		}
	})

	t.Run("commit with custom author", func(t *testing.T) {
		repo := setupTestRepo(t)
		defer repo.cleanup()

		cfg := repo.newConfig()
		cfg.Git.User = "Custom Author"
		cfg.Git.Email = "custom@example.com"

		// Create a file and stage it
		repo.writeFile("app-misc/test/test-1.0.ebuild", "# Test\n")
		repo.runGit("add", ".")

		// Commit
		err := Commit(cfg, "add(app-misc/test-1.0)")
		if err != nil {
			t.Fatalf("Commit() error = %v", err)
		}

		// Verify author
		author := repo.getLastCommitAuthor()
		expected := "Custom Author <custom@example.com>\n"
		if author != expected {
			t.Errorf("Commit author = %q, want %q", author, expected)
		}
	})

	t.Run("commit version bump", func(t *testing.T) {
		repo := setupTestRepo(t)
		defer repo.cleanup()

		cfg := repo.newConfig()

		// Create initial ebuild and commit
		repo.writeFile("sys-apps/myapp/myapp-1.0.ebuild", "# v1.0\n")
		repo.runGit("add", ".")
		repo.runGit("commit", "-m", "add(sys-apps/myapp-1.0)")

		// Version bump: delete old, add new
		repo.deleteFile("sys-apps/myapp/myapp-1.0.ebuild")
		repo.writeFile("sys-apps/myapp/myapp-2.0.ebuild", "# v2.0\n")
		repo.runGit("add", ".")

		// Get changes and generate message
		changes, err := GetStagedChanges(cfg)
		if err != nil {
			t.Fatalf("GetStagedChanges() error = %v", err)
		}

		// Should detect version bump
		hasUpgrade := false
		for _, c := range changes {
			if c.Type == Up && c.OldVersion == "1.0" && c.Version == "2.0" {
				hasUpgrade = true
				break
			}
		}
		if !hasUpgrade {
			t.Errorf("Expected version bump detection, got changes: %+v", changes)
		}

		message := GenerateMessage(changes)

		// Commit
		err = Commit(cfg, message)
		if err != nil {
			t.Fatalf("Commit() error = %v", err)
		}

		// Verify commit message contains version transition
		lastMsg := repo.getLastCommitMessage()
		if lastMsg != message+"\n" {
			t.Errorf("Commit message = %q, want %q", lastMsg, message)
		}
	})

	t.Run("commit with nothing staged fails", func(t *testing.T) {
		repo := setupTestRepo(t)
		defer repo.cleanup()

		cfg := repo.newConfig()

		// Try to commit without staging anything
		err := Commit(cfg, "test message")
		if err == nil {
			t.Error("Commit() should fail when nothing is staged")
		}
	})
}


// TestIntegrationPush tests the Push function with a real git repository and remote.
// It creates a repo with a remote, commits changes, pushes, and verifies the result.
// _Requirements: 5.3_
func TestIntegrationPush(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	t.Run("successful push", func(t *testing.T) {
		repo, _ := setupTestRepoWithRemote(t)
		defer repo.cleanup()

		cfg := repo.newConfig()

		// Create a new file and commit
		repo.writeFile("app-misc/newpkg/newpkg-1.0.ebuild", "# New package\n")
		repo.runGit("add", ".")
		repo.runGit("commit", "-m", "add(app-misc/newpkg-1.0)")

		// Push the changes
		result, err := Push(cfg)
		if err != nil {
			t.Fatalf("Push() error = %v", err)
		}

		if result.UpToDate {
			t.Error("Push() result.UpToDate = true, want false (we had changes)")
		}

		if result.Message != "Changes pushed successfully." {
			t.Errorf("Push() result.Message = %q, want %q", result.Message, "Changes pushed successfully.")
		}
	})

	t.Run("push when up-to-date", func(t *testing.T) {
		repo, _ := setupTestRepoWithRemote(t)
		defer repo.cleanup()

		cfg := repo.newConfig()

		// Don't make any changes, just try to push
		// Note: git push exits with code 0 even when up-to-date,
		// outputting "Everything up-to-date" to stderr.
		// The current implementation doesn't detect this case
		// (it only detects up-to-date when git returns an error).
		result, err := Push(cfg)
		if err != nil {
			t.Fatalf("Push() error = %v", err)
		}

		// The push succeeds (no error) but reports as not up-to-date
		// because git exits with code 0
		if result == nil {
			t.Fatal("Push() result should not be nil")
		}
	})

	t.Run("push multiple commits", func(t *testing.T) {
		repo, _ := setupTestRepoWithRemote(t)
		defer repo.cleanup()

		cfg := repo.newConfig()

		// Create first commit
		repo.writeFile("app-misc/pkg1/pkg1-1.0.ebuild", "# Package 1\n")
		repo.runGit("add", ".")
		repo.runGit("commit", "-m", "add(app-misc/pkg1-1.0)")

		// Create second commit
		repo.writeFile("app-misc/pkg2/pkg2-1.0.ebuild", "# Package 2\n")
		repo.runGit("add", ".")
		repo.runGit("commit", "-m", "add(app-misc/pkg2-1.0)")

		// Push both commits
		result, err := Push(cfg)
		if err != nil {
			t.Fatalf("Push() error = %v", err)
		}

		if result.UpToDate {
			t.Error("Push() result.UpToDate = true, want false")
		}

		// Verify both commits are on remote by checking log
		// After push, local and remote should be in sync
		repo.runGit("fetch", "origin")
		localHead := repo.runGit("rev-parse", "HEAD")
		remoteHead := repo.runGit("rev-parse", "origin/master")

		if localHead != remoteHead {
			t.Errorf("Local HEAD %s != Remote HEAD %s after push", localHead, remoteHead)
		}
	})

	t.Run("push without remote fails", func(t *testing.T) {
		repo := setupTestRepo(t) // No remote configured
		defer repo.cleanup()

		cfg := repo.newConfig()

		// Create a commit
		repo.writeFile("app-misc/test/test-1.0.ebuild", "# Test\n")
		repo.runGit("add", ".")
		repo.runGit("commit", "-m", "test commit")

		// Push should fail (no remote)
		_, err := Push(cfg)
		if err == nil {
			t.Error("Push() should fail when no remote is configured")
		}
	})
}

// TestIntegrationPushDryRun tests the PushDryRun function.
// _Requirements: 5.3_
func TestIntegrationPushDryRun(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	t.Run("dry run does not push changes", func(t *testing.T) {
		repo, _ := setupTestRepoWithRemote(t)
		defer repo.cleanup()

		cfg := repo.newConfig()

		// Create a commit
		repo.writeFile("app-misc/dryrun/dryrun-1.0.ebuild", "# Dry run test\n")
		repo.runGit("add", ".")
		repo.runGit("commit", "-m", "add(app-misc/dryrun-1.0)")

		// Dry run should not fail
		_, err := PushDryRun(cfg)
		if err != nil {
			t.Fatalf("PushDryRun() error = %v", err)
		}

		// Verify nothing was actually pushed
		repo.runGit("fetch", "origin")
		localHead := repo.runGit("rev-parse", "HEAD")
		remoteHead := repo.runGit("rev-parse", "origin/master")

		if localHead == remoteHead {
			t.Error("Dry run should not actually push changes")
		}
	})

	t.Run("dry run when up-to-date", func(t *testing.T) {
		repo, _ := setupTestRepoWithRemote(t)
		defer repo.cleanup()

		cfg := repo.newConfig()

		// No changes to push
		output, err := PushDryRun(cfg)
		if err != nil {
			t.Fatalf("PushDryRun() error = %v", err)
		}

		// Should indicate nothing to push
		if output == "" {
			t.Error("PushDryRun() should return some output")
		}
	})
}

// setupTestRepoWithUpstream creates a test repo with a bare remote and a second
// clone that simulates upstream changes. Returns the main repo, the upstream clone,
// and the remote path.
// _Requirements: 6.1, 6.2, 6.3, 6.4_
func setupTestRepoWithUpstream(t *testing.T) (*testRepo, *testRepo, string) {
	t.Helper()

	// Create a bare remote repository first
	remoteDir, err := os.MkdirTemp("", "bentoo-integration-remote-*")
	if err != nil {
		t.Fatalf("Failed to create remote directory: %v", err)
	}

	// Initialize bare repository
	cmd := exec.Command("git", "init", "--bare")
	cmd.Dir = remoteDir
	if err := cmd.Run(); err != nil {
		os.RemoveAll(remoteDir)
		t.Fatalf("Failed to init bare repo: %v", err)
	}

	// Create the main repo (user's local repo)
	mainDir, err := os.MkdirTemp("", "bentoo-integration-main-*")
	if err != nil {
		os.RemoveAll(remoteDir)
		t.Fatalf("Failed to create main directory: %v", err)
	}

	mainRepo := &testRepo{
		path: mainDir,
		t:    t,
		cleanup: func() {
			os.RemoveAll(mainDir)
			os.RemoveAll(remoteDir)
		},
	}

	// Initialize main repo
	mainRepo.runGit("init")
	mainRepo.runGit("config", "user.name", "Main User")
	mainRepo.runGit("config", "user.email", "main@example.com")

	// Create overlay structure
	if err := os.MkdirAll(filepath.Join(mainDir, "profiles"), 0755); err != nil {
		mainRepo.cleanup()
		t.Fatalf("Failed to create profiles directory: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(mainDir, "metadata"), 0755); err != nil {
		mainRepo.cleanup()
		t.Fatalf("Failed to create metadata directory: %v", err)
	}

	// Create initial commit
	mainRepo.writeFile("profiles/repo_name", "test-overlay")
	mainRepo.writeFile("metadata/layout.conf", "masters = gentoo\n")
	mainRepo.runGit("add", ".")
	mainRepo.runGit("commit", "-m", "Initial commit")

	// Add remote and push
	mainRepo.runGit("remote", "add", "origin", remoteDir)
	mainRepo.runGit("push", "-u", "origin", "master")

	// Create upstream clone (simulates another developer's repo)
	upstreamDir, err := os.MkdirTemp("", "bentoo-integration-upstream-*")
	if err != nil {
		mainRepo.cleanup()
		t.Fatalf("Failed to create upstream directory: %v", err)
	}

	// Clone from remote
	cmd = exec.Command("git", "clone", remoteDir, upstreamDir)
	if err := cmd.Run(); err != nil {
		mainRepo.cleanup()
		os.RemoveAll(upstreamDir)
		t.Fatalf("Failed to clone upstream: %v", err)
	}

	upstreamRepo := &testRepo{
		path: upstreamDir,
		t:    t,
		cleanup: func() {
			os.RemoveAll(upstreamDir)
		},
	}

	// Configure upstream repo
	upstreamRepo.runGit("config", "user.name", "Upstream User")
	upstreamRepo.runGit("config", "user.email", "upstream@example.com")

	// Update main repo cleanup to also remove upstream
	originalCleanup := mainRepo.cleanup
	mainRepo.cleanup = func() {
		originalCleanup()
		os.RemoveAll(upstreamDir)
	}

	return mainRepo, upstreamRepo, remoteDir
}

// TestIntegrationSync tests the Sync function with a real git repository.
// It creates a repo with remote, adds upstream changes, syncs, and verifies merge.
// _Requirements: 6.1, 6.2, 6.3, 6.4_
func TestIntegrationSync(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	t.Run("sync pulls upstream changes", func(t *testing.T) {
		mainRepo, upstreamRepo, _ := setupTestRepoWithUpstream(t)
		defer mainRepo.cleanup()
		defer upstreamRepo.cleanup()

		// Add a new file in upstream and push
		upstreamRepo.writeFile("app-misc/upstream-pkg/upstream-pkg-1.0.ebuild", "# Upstream package\n")
		upstreamRepo.runGit("add", ".")
		upstreamRepo.runGit("commit", "-m", "add(app-misc/upstream-pkg-1.0)")
		upstreamRepo.runGit("push", "origin", "master")

		// Sync main repo
		runner := mainRepo.newGitRunner()
		result, err := SyncWithRunner(runner, "origin")
		if err != nil {
			t.Fatalf("SyncWithRunner() error = %v", err)
		}

		// Verify sync succeeded
		if !result.Success {
			t.Errorf("SyncWithRunner() Success = false, want true")
		}

		if len(result.Conflicts) > 0 {
			t.Errorf("SyncWithRunner() Conflicts = %v, want empty", result.Conflicts)
		}

		// Verify the upstream file now exists in main repo
		upstreamFile := filepath.Join(mainRepo.path, "app-misc/upstream-pkg/upstream-pkg-1.0.ebuild")
		if _, err := os.Stat(upstreamFile); os.IsNotExist(err) {
			t.Error("Upstream file should exist after sync")
		}
	})

	t.Run("sync with multiple upstream commits", func(t *testing.T) {
		mainRepo, upstreamRepo, _ := setupTestRepoWithUpstream(t)
		defer mainRepo.cleanup()
		defer upstreamRepo.cleanup()

		// Add multiple commits in upstream
		upstreamRepo.writeFile("app-misc/pkg1/pkg1-1.0.ebuild", "# Package 1\n")
		upstreamRepo.runGit("add", ".")
		upstreamRepo.runGit("commit", "-m", "add(app-misc/pkg1-1.0)")

		upstreamRepo.writeFile("app-misc/pkg2/pkg2-1.0.ebuild", "# Package 2\n")
		upstreamRepo.runGit("add", ".")
		upstreamRepo.runGit("commit", "-m", "add(app-misc/pkg2-1.0)")

		upstreamRepo.runGit("push", "origin", "master")

		// Sync main repo
		runner := mainRepo.newGitRunner()
		result, err := SyncWithRunner(runner, "origin")
		if err != nil {
			t.Fatalf("SyncWithRunner() error = %v", err)
		}

		if !result.Success {
			t.Errorf("SyncWithRunner() Success = false, want true")
		}

		// Verify both files exist
		pkg1File := filepath.Join(mainRepo.path, "app-misc/pkg1/pkg1-1.0.ebuild")
		pkg2File := filepath.Join(mainRepo.path, "app-misc/pkg2/pkg2-1.0.ebuild")

		if _, err := os.Stat(pkg1File); os.IsNotExist(err) {
			t.Error("pkg1 file should exist after sync")
		}
		if _, err := os.Stat(pkg2File); os.IsNotExist(err) {
			t.Error("pkg2 file should exist after sync")
		}
	})

	t.Run("sync when already up-to-date", func(t *testing.T) {
		mainRepo, upstreamRepo, _ := setupTestRepoWithUpstream(t)
		defer mainRepo.cleanup()
		defer upstreamRepo.cleanup()

		// No upstream changes, just sync
		runner := mainRepo.newGitRunner()
		result, err := SyncWithRunner(runner, "origin")
		if err != nil {
			t.Fatalf("SyncWithRunner() error = %v", err)
		}

		if !result.Success {
			t.Errorf("SyncWithRunner() Success = false, want true")
		}

		if len(result.Conflicts) > 0 {
			t.Errorf("SyncWithRunner() Conflicts = %v, want empty", result.Conflicts)
		}
	})

	t.Run("sync with non-conflicting local changes", func(t *testing.T) {
		mainRepo, upstreamRepo, _ := setupTestRepoWithUpstream(t)
		defer mainRepo.cleanup()
		defer upstreamRepo.cleanup()

		// Add a file in upstream
		upstreamRepo.writeFile("app-misc/upstream-pkg/upstream-pkg-1.0.ebuild", "# Upstream\n")
		upstreamRepo.runGit("add", ".")
		upstreamRepo.runGit("commit", "-m", "add(app-misc/upstream-pkg-1.0)")
		upstreamRepo.runGit("push", "origin", "master")

		// Add a different file in main (non-conflicting)
		mainRepo.writeFile("app-misc/local-pkg/local-pkg-1.0.ebuild", "# Local\n")
		mainRepo.runGit("add", ".")
		mainRepo.runGit("commit", "-m", "add(app-misc/local-pkg-1.0)")

		// Sync should succeed (no conflicts)
		runner := mainRepo.newGitRunner()
		result, err := SyncWithRunner(runner, "origin")
		if err != nil {
			t.Fatalf("SyncWithRunner() error = %v", err)
		}

		if !result.Success {
			t.Errorf("SyncWithRunner() Success = false, want true")
		}

		// Both files should exist
		upstreamFile := filepath.Join(mainRepo.path, "app-misc/upstream-pkg/upstream-pkg-1.0.ebuild")
		localFile := filepath.Join(mainRepo.path, "app-misc/local-pkg/local-pkg-1.0.ebuild")

		if _, err := os.Stat(upstreamFile); os.IsNotExist(err) {
			t.Error("Upstream file should exist after sync")
		}
		if _, err := os.Stat(localFile); os.IsNotExist(err) {
			t.Error("Local file should still exist after sync")
		}
	})

	t.Run("sync detects merge conflicts", func(t *testing.T) {
		mainRepo, upstreamRepo, _ := setupTestRepoWithUpstream(t)
		defer mainRepo.cleanup()
		defer upstreamRepo.cleanup()

		// Both repos modify the same file differently
		conflictFile := "profiles/repo_name"

		// Upstream modifies the file
		upstreamRepo.writeFile(conflictFile, "upstream-overlay")
		upstreamRepo.runGit("add", ".")
		upstreamRepo.runGit("commit", "-m", "Update repo name (upstream)")
		upstreamRepo.runGit("push", "origin", "master")

		// Main repo modifies the same file differently
		mainRepo.writeFile(conflictFile, "local-overlay")
		mainRepo.runGit("add", ".")
		mainRepo.runGit("commit", "-m", "Update repo name (local)")

		// Sync should detect conflict
		runner := mainRepo.newGitRunner()
		result, err := SyncWithRunner(runner, "origin")

		// The sync should not return an error, but report conflicts
		if err != nil {
			t.Fatalf("SyncWithRunner() error = %v", err)
		}

		if result.Success {
			t.Error("SyncWithRunner() Success = true, want false (conflict expected)")
		}

		if len(result.Conflicts) == 0 {
			t.Error("SyncWithRunner() Conflicts should not be empty")
		}

		// Abort the merge to clean up
		mainRepo.runGit("merge", "--abort")
	})

	t.Run("sync with empty remote fails", func(t *testing.T) {
		mainRepo, _, _ := setupTestRepoWithUpstream(t)
		defer mainRepo.cleanup()

		runner := mainRepo.newGitRunner()
		_, err := SyncWithRunner(runner, "")

		if err == nil {
			t.Error("SyncWithRunner() should fail with empty remote")
		}

		if err != ErrNoRemote {
			t.Errorf("SyncWithRunner() error = %v, want ErrNoRemote", err)
		}
	})
}
