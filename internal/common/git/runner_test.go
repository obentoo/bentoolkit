package git

import (
	"os"
	"path/filepath"
	"testing"
)

func TestParseStatusOutput(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected []StatusEntry
	}{
		{
			name:     "empty output",
			input:    "",
			expected: nil,
		},
		{
			name:  "single added file",
			input: "A  app-misc/hello/hello-1.0.ebuild\n",
			expected: []StatusEntry{
				{Status: "A", FilePath: "app-misc/hello/hello-1.0.ebuild"},
			},
		},
		{
			name:  "single modified file",
			input: "M  app-misc/hello/hello-1.0.ebuild\n",
			expected: []StatusEntry{
				{Status: "M", FilePath: "app-misc/hello/hello-1.0.ebuild"},
			},
		},
		{
			name:  "single deleted file",
			input: "D  app-misc/hello/hello-1.0.ebuild\n",
			expected: []StatusEntry{
				{Status: "D", FilePath: "app-misc/hello/hello-1.0.ebuild"},
			},
		},
		{
			name:  "untracked file",
			input: "?? app-misc/new/new-1.0.ebuild\n",
			expected: []StatusEntry{
				{Status: "??", FilePath: "app-misc/new/new-1.0.ebuild"},
			},
		},
		{
			name:  "renamed file",
			input: "R  old-name.txt -> new-name.txt\n",
			expected: []StatusEntry{
				{Status: "R", FilePath: "new-name.txt"},
			},
		},
		{
			name: "multiple files",
			input: `A  app-misc/hello/hello-1.0.ebuild
M  sys-apps/world/world-2.0.ebuild
D  old-pkg/old-1.0.ebuild
?? new-file.txt
`,
			expected: []StatusEntry{
				{Status: "A", FilePath: "app-misc/hello/hello-1.0.ebuild"},
				{Status: "M", FilePath: "sys-apps/world/world-2.0.ebuild"},
				{Status: "D", FilePath: "old-pkg/old-1.0.ebuild"},
				{Status: "??", FilePath: "new-file.txt"},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := ParseStatusOutput(tt.input)

			if len(result) != len(tt.expected) {
				t.Errorf("expected %d entries, got %d", len(tt.expected), len(result))
				return
			}

			for i, entry := range result {
				if entry.Status != tt.expected[i].Status {
					t.Errorf("entry %d: expected status %q, got %q", i, tt.expected[i].Status, entry.Status)
				}
				if entry.FilePath != tt.expected[i].FilePath {
					t.Errorf("entry %d: expected path %q, got %q", i, tt.expected[i].FilePath, entry.FilePath)
				}
			}
		})
	}
}

func TestNewGitRunner(t *testing.T) {
	workDir := "/tmp/test-repo"
	runner := NewGitRunner(workDir)

	if runner.WorkDir() != workDir {
		t.Errorf("expected workDir %q, got %q", workDir, runner.WorkDir())
	}
}

func TestAddPathValidation(t *testing.T) {
	// Create a temporary directory to act as our overlay
	tmpDir, err := os.MkdirTemp("", "git-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	// Initialize a git repo in the temp dir
	runner := NewGitRunner(tmpDir)
	_, _, err = runner.runCommand("init")
	if err != nil {
		t.Fatalf("failed to init git repo: %v", err)
	}

	// Create a test file inside the overlay
	testFile := filepath.Join(tmpDir, "test.txt")
	if err := os.WriteFile(testFile, []byte("test content"), 0644); err != nil {
		t.Fatalf("failed to create test file: %v", err)
	}

	t.Run("add existing file succeeds", func(t *testing.T) {
		err := runner.Add("test.txt")
		if err != nil {
			t.Errorf("expected no error, got %v", err)
		}
	})

	t.Run("add non-existent file returns file not found error", func(t *testing.T) {
		err := runner.Add("nonexistent.txt")
		if err != ErrFileNotFound {
			t.Errorf("expected ErrFileNotFound, got %v", err)
		}
	})

	t.Run("add path outside overlay returns error", func(t *testing.T) {
		err := runner.Add("../outside.txt")
		if err != ErrPathOutsideOverlay {
			t.Errorf("expected ErrPathOutsideOverlay, got %v", err)
		}
	})

	t.Run("add with absolute path outside overlay returns error", func(t *testing.T) {
		err := runner.Add("/etc/passwd")
		if err != ErrPathOutsideOverlay {
			t.Errorf("expected ErrPathOutsideOverlay, got %v", err)
		}
	})

	t.Run("add with no paths adds all", func(t *testing.T) {
		// Create another file
		anotherFile := filepath.Join(tmpDir, "another.txt")
		if err := os.WriteFile(anotherFile, []byte("another"), 0644); err != nil {
			t.Fatalf("failed to create another file: %v", err)
		}

		err := runner.Add()
		if err != nil {
			t.Errorf("expected no error for Add(), got %v", err)
		}
	})
}

func TestGitRunnerCommit(t *testing.T) {
	// Create a temporary directory
	tmpDir, err := os.MkdirTemp("", "git-commit-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	runner := NewGitRunner(tmpDir)

	// Initialize git repo
	_, _, err = runner.runCommand("init")
	if err != nil {
		t.Fatalf("failed to init git repo: %v", err)
	}

	// Configure git user for the test repo
	_, _, _ = runner.runCommand("config", "user.email", "test@example.com")
	_, _, _ = runner.runCommand("config", "user.name", "Test User")

	// Create and stage a file
	testFile := filepath.Join(tmpDir, "test.txt")
	if err := os.WriteFile(testFile, []byte("test"), 0644); err != nil {
		t.Fatalf("failed to create test file: %v", err)
	}
	if err := runner.Add("test.txt"); err != nil {
		t.Fatalf("failed to add file: %v", err)
	}

	t.Run("commit with message succeeds", func(t *testing.T) {
		err := runner.Commit("test commit", "", "")
		if err != nil {
			t.Errorf("expected no error, got %v", err)
		}
	})

	t.Run("commit with author", func(t *testing.T) {
		// Create another file for a new commit
		file2 := filepath.Join(tmpDir, "test2.txt")
		if err := os.WriteFile(file2, []byte("test2"), 0644); err != nil {
			t.Fatalf("failed to create test file: %v", err)
		}
		if err := runner.Add("test2.txt"); err != nil {
			t.Fatalf("failed to add file: %v", err)
		}

		err := runner.Commit("commit with author", "Custom User", "custom@example.com")
		if err != nil {
			t.Errorf("expected no error, got %v", err)
		}
	})
}

func TestGitRunnerStatus(t *testing.T) {
	// Create a temporary directory
	tmpDir, err := os.MkdirTemp("", "git-status-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	runner := NewGitRunner(tmpDir)

	// Initialize git repo
	_, _, err = runner.runCommand("init")
	if err != nil {
		t.Fatalf("failed to init git repo: %v", err)
	}

	t.Run("empty repo has no status entries", func(t *testing.T) {
		entries, err := runner.Status()
		if err != nil {
			t.Errorf("expected no error, got %v", err)
		}
		if len(entries) != 0 {
			t.Errorf("expected 0 entries, got %d", len(entries))
		}
	})

	t.Run("untracked file shows in status", func(t *testing.T) {
		testFile := filepath.Join(tmpDir, "untracked.txt")
		if err := os.WriteFile(testFile, []byte("untracked"), 0644); err != nil {
			t.Fatalf("failed to create test file: %v", err)
		}

		entries, err := runner.Status()
		if err != nil {
			t.Errorf("expected no error, got %v", err)
		}
		if len(entries) != 1 {
			t.Errorf("expected 1 entry, got %d", len(entries))
			return
		}
		if entries[0].Status != "??" {
			t.Errorf("expected status '??', got %q", entries[0].Status)
		}
	})
}
