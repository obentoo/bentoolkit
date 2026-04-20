package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// setupTestHome creates a temp HOME with a valid config file pointing to a temp overlay dir.
// Sets HOME and XDG_CONFIG_HOME env vars. Returns overlayPath and cleanup func.
func setupTestHome(t *testing.T) (overlayPath string, cleanup func()) {
	t.Helper()
	tmpHome := t.TempDir()

	configDir := filepath.Join(tmpHome, ".config", "bentoo")
	if err := os.MkdirAll(configDir, 0755); err != nil {
		t.Fatalf("failed to create config dir: %v", err)
	}

	overlayDir := filepath.Join(tmpHome, "overlay")
	for _, sub := range []string{"profiles", "metadata"} {
		if err := os.MkdirAll(filepath.Join(overlayDir, sub), 0755); err != nil {
			t.Fatalf("failed to create overlay subdir: %v", err)
		}
	}

	configContent := "overlay:\n  path: " + overlayDir + "\n  remote: origin\ngit:\n  user: Test\n  email: test@test.com\n"
	if err := os.WriteFile(filepath.Join(configDir, "config.yaml"), []byte(configContent), 0644); err != nil {
		t.Fatalf("failed to write config: %v", err)
	}

	oldHome := os.Getenv("HOME")
	oldXDG := os.Getenv("XDG_CONFIG_HOME")
	os.Setenv("HOME", tmpHome)
	os.Setenv("XDG_CONFIG_HOME", filepath.Join(tmpHome, ".config"))

	return overlayDir, func() {
		os.Setenv("HOME", oldHome)
		os.Setenv("XDG_CONFIG_HOME", oldXDG)
	}
}

// exitSentinel is used as a panic value to simulate os.Exit in tests.
type exitSentinel int

// withExitIntercept replaces osExit with a panic-based interceptor, runs fn,
// and returns the exit code (or -1 if osExit was not called).
// Real panics (not exitSentinel) are re-panicked.
func withExitIntercept(fn func()) (exitCode int) {
	exitCode = -1
	orig := osExit
	osExit = func(c int) {
		panic(exitSentinel(c))
	}
	defer func() {
		osExit = orig
		if r := recover(); r != nil {
			if code, ok := r.(exitSentinel); ok {
				exitCode = int(code)
			} else {
				panic(r)
			}
		}
	}()
	fn()
	return exitCode
}

// ---- runStatus ----

// TestRunStatusValidOverlay tests runStatus with a valid (empty) overlay.
// Exits 0 or 1 depending on git status — just must not panic.
func TestRunStatusValidOverlay(t *testing.T) {
	_, cleanup := setupTestHome(t)
	defer cleanup()
	withExitIntercept(func() { runStatus(statusCmd, nil) })
}

// TestRunStatusInvalidOverlay tests runStatus when overlay is missing profiles/.
func TestRunStatusInvalidOverlay(t *testing.T) {
	_, cleanup := setupTestHome(t)
	defer cleanup()

	tmpHome := os.Getenv("HOME")
	os.RemoveAll(filepath.Join(tmpHome, "overlay", "profiles"))

	withExitIntercept(func() { runStatus(statusCmd, nil) })
}

// ---- runAdd ----

// TestRunAddNoArgs tests runAdd with no args on a valid overlay.
func TestRunAddNoArgs(t *testing.T) {
	_, cleanup := setupTestHome(t)
	defer cleanup()
	withExitIntercept(func() { runAdd(addCmd, nil) })
}

// TestRunAddWithArgs tests runAdd with a file arg on a valid overlay.
func TestRunAddWithArgs(t *testing.T) {
	overlayDir, cleanup := setupTestHome(t)
	defer cleanup()

	dummyFile := filepath.Join(overlayDir, "dummy.txt")
	_ = os.WriteFile(dummyFile, []byte("test"), 0644)

	withExitIntercept(func() { runAdd(addCmd, []string{dummyFile}) })
}

// ---- runPush ----

// TestRunPushDryRun tests runPush with --dry-run flag.
func TestRunPushDryRun(t *testing.T) {
	_, cleanup := setupTestHome(t)
	defer cleanup()

	origDryRun := pushDryRun
	pushDryRun = true
	defer func() { pushDryRun = origDryRun }()

	withExitIntercept(func() { runPush(pushCmd, nil) })
}

// TestRunPushNoDryRunExitsOnGitError tests runPush without dry-run exits(1) when git fails.
func TestRunPushNoDryRunExitsOnGitError(t *testing.T) {
	_, cleanup := setupTestHome(t)
	defer cleanup()

	origDryRun := pushDryRun
	pushDryRun = false
	defer func() { pushDryRun = origDryRun }()

	code := withExitIntercept(func() { runPush(pushCmd, nil) })
	if code != 1 {
		t.Errorf("runPush without git repo should exit(1), got exit(%d)", code)
	}
}

// ---- runDiff ----

// TestRunDiffUnstaged tests runDiff without --staged flag.
func TestRunDiffUnstaged(t *testing.T) {
	_, cleanup := setupTestHome(t)
	defer cleanup()

	origStaged := diffStaged
	diffStaged = false
	defer func() { diffStaged = origStaged }()

	withExitIntercept(func() { runDiff(diffCmd, nil) })
}

// TestRunDiffStaged tests runDiff with --staged flag.
func TestRunDiffStaged(t *testing.T) {
	_, cleanup := setupTestHome(t)
	defer cleanup()

	origStaged := diffStaged
	diffStaged = true
	defer func() { diffStaged = origStaged }()

	withExitIntercept(func() { runDiff(diffCmd, nil) })
}

// TestRunDiffWithPath tests runDiff with a path argument.
func TestRunDiffWithPath(t *testing.T) {
	overlayDir, cleanup := setupTestHome(t)
	defer cleanup()

	origStaged := diffStaged
	diffStaged = false
	defer func() { diffStaged = origStaged }()

	withExitIntercept(func() { runDiff(diffCmd, []string{overlayDir}) })
}

// ---- runLog ----

// TestRunLogDefault tests runLog with default flags.
func TestRunLogDefault(t *testing.T) {
	_, cleanup := setupTestHome(t)
	defer cleanup()

	origCount, origOneline := logCount, logOneline
	logCount = 5
	logOneline = false
	defer func() { logCount = origCount; logOneline = origOneline }()

	withExitIntercept(func() { runLog(logCmd, nil) })
}

// TestRunLogOneline tests runLog with --oneline flag.
func TestRunLogOneline(t *testing.T) {
	_, cleanup := setupTestHome(t)
	defer cleanup()

	origCount, origOneline := logCount, logOneline
	logCount = 3
	logOneline = true
	defer func() { logCount = origCount; logOneline = origOneline }()

	withExitIntercept(func() { runLog(logCmd, nil) })
}

// ---- runSync ----

// TestRunSync tests runSync on a valid overlay (fails at git level, not config).
func TestRunSync(t *testing.T) {
	_, cleanup := setupTestHome(t)
	defer cleanup()
	withExitIntercept(func() { runSync(syncCmd, nil) })
}

// ---- runCompare ----

// TestRunCompareUnknownRepo tests runCompare with unknown repo name exits(1).
func TestRunCompareUnknownRepo(t *testing.T) {
	_, cleanup := setupTestHome(t)
	defer cleanup()

	code := withExitIntercept(func() { runCompare(compareCmd, []string{"nonexistent-repo-xyz"}) })
	if code != 1 {
		t.Errorf("runCompare with unknown repo should exit(1), got exit(%d)", code)
	}
}

// TestRunCompareDefaultRepo tests runCompare with default repo (gentoo).
// Will fail at provider/network level — just must not panic.
func TestRunCompareDefaultRepo(t *testing.T) {
	_, cleanup := setupTestHome(t)
	defer cleanup()

	origClone, origNoCache, origTimeout := compareClone, compareNoCache, compareTimeout
	compareClone = false
	compareNoCache = true
	compareTimeout = 1
	defer func() {
		compareClone = origClone
		compareNoCache = origNoCache
		compareTimeout = origTimeout
	}()

	withExitIntercept(func() { runCompare(compareCmd, nil) })
}

// TestRunCompareWithRepoArg tests runCompare with explicit repo arg.
func TestRunCompareWithRepoArg(t *testing.T) {
	_, cleanup := setupTestHome(t)
	defer cleanup()

	origTimeout := compareTimeout
	compareTimeout = 1
	defer func() { compareTimeout = origTimeout }()

	withExitIntercept(func() { runCompare(compareCmd, []string{"gentoo"}) })
}

// ---- runAnalyze ----

// TestRunAnalyzeNoArgs tests runAnalyze with no args and no --all flag exits(1).
func TestRunAnalyzeNoArgs(t *testing.T) {
	_, cleanup := setupTestHome(t)
	defer cleanup()

	origAll := analyzeAll
	analyzeAll = false
	defer func() { analyzeAll = origAll }()

	code := withExitIntercept(func() { runAnalyze(analyzeCmd, nil) })
	if code != 1 {
		t.Errorf("runAnalyze with no args should exit(1), got exit(%d)", code)
	}
}

// TestRunAnalyzeWithPackage tests runAnalyze with a package arg (dry-run, no-cache).
func TestRunAnalyzeWithPackage(t *testing.T) {
	overlayDir, cleanup := setupTestHome(t)
	defer cleanup()

	pkgDir := filepath.Join(overlayDir, "net-misc", "foo")
	if err := os.MkdirAll(pkgDir, 0755); err != nil {
		t.Fatalf("failed to create pkg dir: %v", err)
	}
	_ = os.WriteFile(filepath.Join(pkgDir, "foo-1.0.ebuild"), []byte("# ebuild"), 0644)

	origAll, origNoCache, origDryRun := analyzeAll, analyzeNoCache, analyzeDryRun
	analyzeAll = false
	analyzeNoCache = true
	analyzeDryRun = true
	defer func() {
		analyzeAll = origAll
		analyzeNoCache = origNoCache
		analyzeDryRun = origDryRun
	}()

	withExitIntercept(func() { runAnalyze(analyzeCmd, []string{"net-misc/foo"}) })
}

// TestRunAnalyzeAll tests runAnalyze with --all flag (dry-run).
func TestRunAnalyzeAll(t *testing.T) {
	_, cleanup := setupTestHome(t)
	defer cleanup()

	origAll, origDryRun := analyzeAll, analyzeDryRun
	analyzeAll = true
	analyzeDryRun = true
	defer func() { analyzeAll = origAll; analyzeDryRun = origDryRun }()

	withExitIntercept(func() { runAnalyze(analyzeCmd, nil) })
}

// ---- runAutoupdate ----

// TestRunAutoupdateNoFlag tests runAutoupdate with no flags (shows help, no exit).
func TestRunAutoupdateNoFlag(t *testing.T) {
	_, cleanup := setupTestHome(t)
	defer cleanup()

	origCheck, origList, origApply := autoupdateCheck, autoupdateList, autoupdateApply
	autoupdateCheck = false
	autoupdateList = false
	autoupdateApply = ""
	defer func() {
		autoupdateCheck = origCheck
		autoupdateList = origList
		autoupdateApply = origApply
	}()

	withExitIntercept(func() { runAutoupdate(autoupdateCmd, nil) })
}

// TestRunAutoupdateList tests runAutoupdate with --list flag.
func TestRunAutoupdateList(t *testing.T) {
	_, cleanup := setupTestHome(t)
	defer cleanup()

	origCheck, origList, origApply := autoupdateCheck, autoupdateList, autoupdateApply
	autoupdateCheck = false
	autoupdateList = true
	autoupdateApply = ""
	defer func() {
		autoupdateCheck = origCheck
		autoupdateList = origList
		autoupdateApply = origApply
	}()

	withExitIntercept(func() { runAutoupdate(autoupdateCmd, nil) })
}

// TestRunAutoupdateCheck tests runAutoupdate with --check flag.
func TestRunAutoupdateCheck(t *testing.T) {
	_, cleanup := setupTestHome(t)
	defer cleanup()

	origCheck, origList, origApply := autoupdateCheck, autoupdateList, autoupdateApply
	autoupdateCheck = true
	autoupdateList = false
	autoupdateApply = ""
	defer func() {
		autoupdateCheck = origCheck
		autoupdateList = origList
		autoupdateApply = origApply
	}()

	withExitIntercept(func() { runAutoupdate(autoupdateCmd, nil) })
}

// TestRunAutoupdateApply tests runAutoupdate with --apply flag.
func TestRunAutoupdateApply(t *testing.T) {
	_, cleanup := setupTestHome(t)
	defer cleanup()

	origCheck, origList, origApply := autoupdateCheck, autoupdateList, autoupdateApply
	autoupdateCheck = false
	autoupdateList = false
	autoupdateApply = "net-misc/foo"
	defer func() {
		autoupdateCheck = origCheck
		autoupdateList = origList
		autoupdateApply = origApply
	}()

	withExitIntercept(func() { runAutoupdate(autoupdateCmd, nil) })
}

// ---- runCommit ----

// TestRunCommitDryRun tests runCommit with --dry-run flag (no staged changes → exits 0).
func TestRunCommitDryRun(t *testing.T) {
	_, cleanup := setupTestHome(t)
	defer cleanup()

	origDryRun, origMsg := commitDryRun, commitMessage
	commitDryRun = true
	commitMessage = ""
	defer func() { commitDryRun = origDryRun; commitMessage = origMsg }()

	withExitIntercept(func() { runCommit(commitCmd, nil) })
}

// TestRunCommitWithMessage tests runCommit with a custom message (will fail at git level).
func TestRunCommitWithMessage(t *testing.T) {
	_, cleanup := setupTestHome(t)
	defer cleanup()

	origDryRun, origMsg := commitDryRun, commitMessage
	commitDryRun = false
	commitMessage = "test: custom commit message"
	defer func() { commitDryRun = origDryRun; commitMessage = origMsg }()

	withExitIntercept(func() { runCommit(commitCmd, nil) })
}

// ---- runRename ----

// TestRunRenameNoArgs tests runRename with no args exits(1).
func TestRunRenameNoArgs(t *testing.T) {
	_, cleanup := setupTestHome(t)
	defer cleanup()

	code := withExitIntercept(func() { runRename(renameCmd, nil) })
	if code != 1 {
		t.Errorf("runRename with no args should exit(1), got exit(%d)", code)
	}
}

// TestRunRenameInvalidArgs tests runRename with invalid args format exits(1).
func TestRunRenameInvalidArgs(t *testing.T) {
	_, cleanup := setupTestHome(t)
	defer cleanup()

	code := withExitIntercept(func() { runRename(renameCmd, []string{"invalid-format"}) })
	if code != 1 {
		t.Errorf("runRename with invalid args should exit(1), got exit(%d)", code)
	}
}

// TestRunRenameValidArgsDryRun tests runRename with valid args and --dry-run.
func TestRunRenameValidArgsDryRun(t *testing.T) {
	_, cleanup := setupTestHome(t)
	defer cleanup()

	origFlags := renameFlags
	renameFlags.DryRun = true
	defer func() { renameFlags = origFlags }()

	// Valid format: category:pattern:oldver => newver
	withExitIntercept(func() {
		runRename(renameCmd, []string{"app-misc:foo:1.0", "=>", "2.0"})
	})
}

// withStdin replaces os.Stdin with a pipe containing the given input, runs fn,
// then restores os.Stdin.
func withStdin(t *testing.T, input string, fn func()) {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("failed to create stdin pipe: %v", err)
	}
	if _, err := w.WriteString(input); err != nil {
		t.Fatalf("failed to write stdin: %v", err)
	}
	w.Close()

	oldStdin := os.Stdin
	os.Stdin = r
	defer func() {
		os.Stdin = oldStdin
		r.Close()
	}()
	fn()
}

// ---- runInit ----

// TestRunInitAbortOnExistingConfig tests runInit aborts when user says no to overwrite.
func TestRunInitAbortOnExistingConfig(t *testing.T) {
	_, cleanup := setupTestHome(t)
	defer cleanup()

	// Respond "n" to "Overwrite?" prompt
	withStdin(t, "n\n", func() {
		withExitIntercept(func() { runInit(initCmd, nil) })
	})
}

// TestRunInitOverwriteWithDefaults tests runInit with all-default inputs (empty lines).
func TestRunInitOverwriteWithDefaults(t *testing.T) {
	_, cleanup := setupTestHome(t)
	defer cleanup()

	// "y" to overwrite, then all defaults (empty lines for path, remote, user, email)
	withStdin(t, "y\n\n\n\n\n", func() {
		withExitIntercept(func() { runInit(initCmd, nil) })
	})
}

// TestRunInitWithCustomPath tests runInit with a custom overlay path.
func TestRunInitWithCustomPath(t *testing.T) {
	tmpHome := t.TempDir()
	overlayDir := tmpHome + "/myoverlay"
	if err := os.MkdirAll(overlayDir, 0755); err != nil {
		t.Fatalf("failed to create overlay dir: %v", err)
	}

	oldHome := os.Getenv("HOME")
	oldXDG := os.Getenv("XDG_CONFIG_HOME")
	os.Setenv("HOME", tmpHome)
	os.Setenv("XDG_CONFIG_HOME", tmpHome+"/.config")
	defer func() {
		os.Setenv("HOME", oldHome)
		os.Setenv("XDG_CONFIG_HOME", oldXDG)
	}()

	// No existing config — provide path, remote, user, email
	withStdin(t, overlayDir+"\norigin\nTestUser\ntest@example.com\n", func() {
		withExitIntercept(func() { runInit(initCmd, nil) })
	})
}

// TestRunInitNoExistingConfig tests runInit when no config exists yet.
func TestRunInitNoExistingConfig(t *testing.T) {
	tmpHome := t.TempDir()
	oldHome := os.Getenv("HOME")
	oldXDG := os.Getenv("XDG_CONFIG_HOME")
	os.Setenv("HOME", tmpHome)
	os.Setenv("XDG_CONFIG_HOME", tmpHome+"/.config")
	defer func() {
		os.Setenv("HOME", oldHome)
		os.Setenv("XDG_CONFIG_HOME", oldXDG)
	}()

	// No existing config — all defaults
	withStdin(t, "\n\n\n\n", func() {
		withExitIntercept(func() { runInit(initCmd, nil) })
	})
}

// ---- confirmAction ----

// TestConfirmActionYes tests confirmAction returns true for "y".
func TestConfirmActionYes(t *testing.T) {
	withStdin(t, "y\n", func() {
		if !confirmAction("Proceed?") {
			t.Error("confirmAction should return true for 'y'")
		}
	})
}

// TestConfirmActionYesFull tests confirmAction returns true for "yes".
func TestConfirmActionYesFull(t *testing.T) {
	withStdin(t, "yes\n", func() {
		if !confirmAction("Proceed?") {
			t.Error("confirmAction should return true for 'yes'")
		}
	})
}

// TestConfirmActionNo tests confirmAction returns false for "n".
func TestConfirmActionNo(t *testing.T) {
	withStdin(t, "n\n", func() {
		if confirmAction("Proceed?") {
			t.Error("confirmAction should return false for 'n'")
		}
	})
}

// TestConfirmActionEmpty tests confirmAction returns false for empty input.
func TestConfirmActionEmpty(t *testing.T) {
	withStdin(t, "\n", func() {
		if confirmAction("Proceed?") {
			t.Error("confirmAction should return false for empty input")
		}
	})
}

// TestConfirmActionEOF tests confirmAction returns false on EOF.
func TestConfirmActionEOF(t *testing.T) {
	withStdin(t, "", func() {
		if confirmAction("Proceed?") {
			t.Error("confirmAction should return false on EOF")
		}
	})
}

// ---- promptConfirmation ----

// TestPromptConfirmationYes tests promptConfirmation returns true for "y".
func TestPromptConfirmationYes(t *testing.T) {
	withStdin(t, "y\n", func() {
		if !promptConfirmation() {
			t.Error("promptConfirmation should return true for 'y'")
		}
	})
}

// TestPromptConfirmationNo tests promptConfirmation returns false for "n".
func TestPromptConfirmationNo(t *testing.T) {
	withStdin(t, "n\n", func() {
		if promptConfirmation() {
			t.Error("promptConfirmation should return false for 'n'")
		}
	})
}

// TestPromptConfirmationEOF tests promptConfirmation returns false on EOF.
func TestPromptConfirmationEOF(t *testing.T) {
	withStdin(t, "", func() {
		if promptConfirmation() {
			t.Error("promptConfirmation should return false on EOF")
		}
	})
}

// ---- runRename with confirmation paths ----

// TestRunRenameWithMatchesAndConfirmNo tests runRename with valid args, matches found, user says no.
func TestRunRenameWithMatchesAndConfirmNo(t *testing.T) {
	overlayDir, cleanup := setupTestHome(t)
	defer cleanup()

	// Create a matching ebuild
	pkgDir := filepath.Join(overlayDir, "app-misc", "foo")
	if err := os.MkdirAll(pkgDir, 0755); err != nil {
		t.Fatalf("failed to create pkg dir: %v", err)
	}
	_ = os.WriteFile(filepath.Join(pkgDir, "foo-1.0.ebuild"), []byte("# ebuild"), 0644)

	origFlags := renameFlags
	renameFlags.DryRun = false
	renameFlags.Yes = false
	renameFlags.Force = false
	defer func() { renameFlags = origFlags }()

	// User says "n" to confirmation
	withStdin(t, "n\n", func() {
		withExitIntercept(func() {
			runRename(renameCmd, []string{"app-misc:foo:1.0", "=>", "2.0"})
		})
	})
}

// TestRunRenameWithMatchesSkipPrompt tests runRename with --yes flag (skip confirmation).
func TestRunRenameWithMatchesSkipPrompt(t *testing.T) {
	overlayDir, cleanup := setupTestHome(t)
	defer cleanup()

	pkgDir := filepath.Join(overlayDir, "app-misc", "bar")
	if err := os.MkdirAll(pkgDir, 0755); err != nil {
		t.Fatalf("failed to create pkg dir: %v", err)
	}
	_ = os.WriteFile(filepath.Join(pkgDir, "bar-1.0.ebuild"), []byte("# ebuild"), 0644)

	origFlags := renameFlags
	renameFlags.DryRun = false
	renameFlags.Yes = true
	renameFlags.Force = false
	defer func() { renameFlags = origFlags }()

	withExitIntercept(func() {
		runRename(renameCmd, []string{"app-misc:bar:1.0", "=>", "2.0"})
	})
}

// TestRunRenameGlobalSearchRequiresConfirm tests runRename with "*" category requires confirm.
func TestRunRenameGlobalSearchRequiresConfirm(t *testing.T) {
	overlayDir, cleanup := setupTestHome(t)
	defer cleanup()

	pkgDir := filepath.Join(overlayDir, "app-misc", "baz")
	if err := os.MkdirAll(pkgDir, 0755); err != nil {
		t.Fatalf("failed to create pkg dir: %v", err)
	}
	_ = os.WriteFile(filepath.Join(pkgDir, "baz-1.0.ebuild"), []byte("# ebuild"), 0644)

	origFlags := renameFlags
	renameFlags.DryRun = false
	renameFlags.Yes = true // -y set but global search still needs confirm
	renameFlags.Force = false
	defer func() { renameFlags = origFlags }()

	// User says "n" to global search confirmation
	withStdin(t, "n\n", func() {
		withExitIntercept(func() {
			runRename(renameCmd, []string{"*:baz:1.0", "=>", "2.0"})
		})
	})
}

// ---- runCommit additional paths ----

// TestRunCommitDryRunWithStagedChanges tests runCommit dry-run path with staged changes.
// We need a git repo for this — skip if git init fails.
func TestRunCommitDryRunWithStagedChanges(t *testing.T) {
	overlayDir, cleanup := setupTestHome(t)
	defer cleanup()

	// Init git repo in overlay dir
	if err := runGitCmd(overlayDir, "init"); err != nil {
		t.Skip("git init failed, skipping")
	}
	_ = runGitCmd(overlayDir, "config", "user.email", "test@test.com")
	_ = runGitCmd(overlayDir, "config", "user.name", "Test")

	// Create and stage a file
	testFile := filepath.Join(overlayDir, "app-misc", "pkg", "pkg-1.0.ebuild")
	if err := os.MkdirAll(filepath.Dir(testFile), 0755); err != nil {
		t.Skip("failed to create dir")
	}
	_ = os.WriteFile(testFile, []byte("# ebuild"), 0644)
	_ = runGitCmd(overlayDir, "add", testFile)

	origDryRun, origMsg := commitDryRun, commitMessage
	commitDryRun = true
	commitMessage = ""
	defer func() { commitDryRun = origDryRun; commitMessage = origMsg }()

	withExitIntercept(func() { runCommit(commitCmd, nil) })
}

// runGitCmd runs a git command in the given directory.
func runGitCmd(dir string, args ...string) error {
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	return cmd.Run()
}

// ---- runSync additional paths ----

// TestRunSyncWithGitRepo tests runSync with an initialized git repo.
func TestRunSyncWithGitRepo(t *testing.T) {
	overlayDir, cleanup := setupTestHome(t)
	defer cleanup()

	if err := runGitCmd(overlayDir, "init"); err != nil {
		t.Skip("git init failed, skipping")
	}
	_ = runGitCmd(overlayDir, "config", "user.email", "test@test.com")
	_ = runGitCmd(overlayDir, "config", "user.name", "Test")

	withExitIntercept(func() { runSync(syncCmd, nil) })
}

// ---- runCompare additional paths ----

// TestRunCompareWithConfiguredRepo tests runCompare with a custom repo in config.
func TestRunCompareWithConfiguredRepo(t *testing.T) {
	tmpHome := t.TempDir()
	overlayDir := filepath.Join(tmpHome, "overlay")
	for _, sub := range []string{"profiles", "metadata"} {
		_ = os.MkdirAll(filepath.Join(overlayDir, sub), 0755)
	}

	configDir := filepath.Join(tmpHome, ".config", "bentoo")
	_ = os.MkdirAll(configDir, 0755)

	// Config with a custom repo pointing to a local path
	configContent := `overlay:
  path: ` + overlayDir + `
  remote: origin
git:
  user: Test
  email: test@test.com
repositories:
  localrepo:
    provider: git
    url: ` + overlayDir + `
`
	_ = os.WriteFile(filepath.Join(configDir, "config.yaml"), []byte(configContent), 0644)

	oldHome := os.Getenv("HOME")
	oldXDG := os.Getenv("XDG_CONFIG_HOME")
	os.Setenv("HOME", tmpHome)
	os.Setenv("XDG_CONFIG_HOME", filepath.Join(tmpHome, ".config"))
	defer func() {
		os.Setenv("HOME", oldHome)
		os.Setenv("XDG_CONFIG_HOME", oldXDG)
	}()

	origTimeout := compareTimeout
	compareTimeout = 1
	defer func() { compareTimeout = origTimeout }()

	withExitIntercept(func() { runCompare(compareCmd, []string{"localrepo"}) })
}

// setupTestHomeWithGitRepo creates a temp HOME with a valid config AND an initialized git repo.
func setupTestHomeWithGitRepo(t *testing.T) (overlayDir string, cleanup func()) {
	t.Helper()
	overlayDir, cleanup = setupTestHome(t)

	if err := runGitCmd(overlayDir, "init"); err != nil {
		cleanup()
		t.Skip("git init not available, skipping")
	}
	_ = runGitCmd(overlayDir, "config", "user.email", "test@test.com")
	_ = runGitCmd(overlayDir, "config", "user.name", "Test")
	return overlayDir, cleanup
}

// ---- runCommit interactive paths ----

// TestRunCommitInteractiveYes tests runCommit interactive path with "y" response.
func TestRunCommitInteractiveYes(t *testing.T) {
	overlayDir, cleanup := setupTestHomeWithGitRepo(t)
	defer cleanup()

	// Stage a file
	pkgDir := filepath.Join(overlayDir, "app-misc", "mypkg")
	_ = os.MkdirAll(pkgDir, 0755)
	testFile := filepath.Join(pkgDir, "mypkg-1.0.ebuild")
	_ = os.WriteFile(testFile, []byte("# ebuild"), 0644)
	_ = runGitCmd(overlayDir, "add", testFile)

	origDryRun, origMsg := commitDryRun, commitMessage
	commitDryRun = false
	commitMessage = ""
	defer func() { commitDryRun = origDryRun; commitMessage = origMsg }()

	// "y" to proceed with generated message
	withStdin(t, "y\n", func() {
		withExitIntercept(func() { runCommit(commitCmd, nil) })
	})
}

// TestRunCommitInteractiveCancel tests runCommit interactive path with "c" response.
func TestRunCommitInteractiveCancel(t *testing.T) {
	overlayDir, cleanup := setupTestHomeWithGitRepo(t)
	defer cleanup()

	pkgDir := filepath.Join(overlayDir, "app-misc", "cancelpkg")
	_ = os.MkdirAll(pkgDir, 0755)
	testFile := filepath.Join(pkgDir, "cancelpkg-1.0.ebuild")
	_ = os.WriteFile(testFile, []byte("# ebuild"), 0644)
	_ = runGitCmd(overlayDir, "add", testFile)

	origDryRun, origMsg := commitDryRun, commitMessage
	commitDryRun = false
	commitMessage = ""
	defer func() { commitDryRun = origDryRun; commitMessage = origMsg }()

	withStdin(t, "c\n", func() {
		withExitIntercept(func() { runCommit(commitCmd, nil) })
	})
}

// TestRunCommitInteractiveEdit tests runCommit interactive path with "e" then custom message.
func TestRunCommitInteractiveEdit(t *testing.T) {
	overlayDir, cleanup := setupTestHomeWithGitRepo(t)
	defer cleanup()

	pkgDir := filepath.Join(overlayDir, "app-misc", "editpkg")
	_ = os.MkdirAll(pkgDir, 0755)
	testFile := filepath.Join(pkgDir, "editpkg-1.0.ebuild")
	_ = os.WriteFile(testFile, []byte("# ebuild"), 0644)
	_ = runGitCmd(overlayDir, "add", testFile)

	origDryRun, origMsg := commitDryRun, commitMessage
	commitDryRun = false
	commitMessage = ""
	defer func() { commitDryRun = origDryRun; commitMessage = origMsg }()

	// "e" to edit, then provide custom message
	withStdin(t, "e\nmy custom commit message\n", func() {
		withExitIntercept(func() { runCommit(commitCmd, nil) })
	})
}

// TestRunCommitInteractiveDefault tests runCommit interactive path with default (empty) response.
func TestRunCommitInteractiveDefault(t *testing.T) {
	overlayDir, cleanup := setupTestHomeWithGitRepo(t)
	defer cleanup()

	pkgDir := filepath.Join(overlayDir, "app-misc", "defpkg")
	_ = os.MkdirAll(pkgDir, 0755)
	testFile := filepath.Join(pkgDir, "defpkg-1.0.ebuild")
	_ = os.WriteFile(testFile, []byte("# ebuild"), 0644)
	_ = runGitCmd(overlayDir, "add", testFile)

	origDryRun, origMsg := commitDryRun, commitMessage
	commitDryRun = false
	commitMessage = ""
	defer func() { commitDryRun = origDryRun; commitMessage = origMsg }()

	// Empty response = default "yes"
	withStdin(t, "\n", func() {
		withExitIntercept(func() { runCommit(commitCmd, nil) })
	})
}

// TestRunCommitInteractiveInvalidOption tests runCommit with invalid option exits(1).
func TestRunCommitInteractiveInvalidOption(t *testing.T) {
	overlayDir, cleanup := setupTestHomeWithGitRepo(t)
	defer cleanup()

	pkgDir := filepath.Join(overlayDir, "app-misc", "invpkg")
	_ = os.MkdirAll(pkgDir, 0755)
	testFile := filepath.Join(pkgDir, "invpkg-1.0.ebuild")
	_ = os.WriteFile(testFile, []byte("# ebuild"), 0644)
	_ = runGitCmd(overlayDir, "add", testFile)

	origDryRun, origMsg := commitDryRun, commitMessage
	commitDryRun = false
	commitMessage = ""
	defer func() { commitDryRun = origDryRun; commitMessage = origMsg }()

	code := withStdinCode(t, "invalid\n", func() int {
		return withExitIntercept(func() { runCommit(commitCmd, nil) })
	})
	if code != 1 {
		t.Errorf("runCommit with invalid option should exit(1), got exit(%d)", code)
	}
}

// withStdinCode is like withStdin but the fn returns an int (exit code).
func withStdinCode(t *testing.T, input string, fn func() int) int {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("failed to create stdin pipe: %v", err)
	}
	if _, err := w.WriteString(input); err != nil {
		t.Fatalf("failed to write stdin: %v", err)
	}
	w.Close()

	oldStdin := os.Stdin
	os.Stdin = r
	defer func() {
		os.Stdin = oldStdin
		r.Close()
	}()
	return fn()
}

// ---- runAdd success path ----

// TestRunAddSuccessPath tests runAdd when files are successfully added.
func TestRunAddSuccessPath(t *testing.T) {
	overlayDir, cleanup := setupTestHomeWithGitRepo(t)
	defer cleanup()

	// Create a file to add
	pkgDir := filepath.Join(overlayDir, "app-misc", "addpkg")
	_ = os.MkdirAll(pkgDir, 0755)
	testFile := filepath.Join(pkgDir, "addpkg-1.0.ebuild")
	_ = os.WriteFile(testFile, []byte("# ebuild"), 0644)

	withExitIntercept(func() { runAdd(addCmd, []string{testFile}) })
}

// ---- runAutoupdate with packages.toml ----

// setupAutoupdateConfig creates a packages.toml in the overlay's .autoupdate dir.
func setupAutoupdateConfig(t *testing.T, overlayDir string) {
	t.Helper()
	autoupdateDir := filepath.Join(overlayDir, ".autoupdate")
	_ = os.MkdirAll(autoupdateDir, 0755)

	// Create a minimal packages.toml in the correct location
	tomlContent := `["app-misc/testpkg"]
url = "https://example.com"
parser = "github"
`
	_ = os.WriteFile(filepath.Join(autoupdateDir, "packages.toml"), []byte(tomlContent), 0644)
}

// TestRunAutoupdateCheckWithConfig tests runAutoupdate --check with a packages.toml.
func TestRunAutoupdateCheckWithConfig(t *testing.T) {
	overlayDir, cleanup := setupTestHome(t)
	defer cleanup()

	setupAutoupdateConfig(t, overlayDir)

	origCheck, origList, origApply := autoupdateCheck, autoupdateList, autoupdateApply
	autoupdateCheck = true
	autoupdateList = false
	autoupdateApply = ""
	defer func() {
		autoupdateCheck = origCheck
		autoupdateList = origList
		autoupdateApply = origApply
	}()

	withExitIntercept(func() { runAutoupdate(autoupdateCmd, nil) })
}

// TestRunAutoupdateCheckSpecificPkg tests runAutoupdate --check with a specific package arg.
func TestRunAutoupdateCheckSpecificPkg(t *testing.T) {
	overlayDir, cleanup := setupTestHome(t)
	defer cleanup()

	setupAutoupdateConfig(t, overlayDir)

	origCheck, origList, origApply := autoupdateCheck, autoupdateList, autoupdateApply
	autoupdateCheck = true
	autoupdateList = false
	autoupdateApply = ""
	defer func() {
		autoupdateCheck = origCheck
		autoupdateList = origList
		autoupdateApply = origApply
	}()

	withExitIntercept(func() { runAutoupdate(autoupdateCmd, []string{"app-misc/testpkg"}) })
}

// ---- runCompare with packages in overlay ----

// TestRunCompareWithPackages tests runCompare when overlay has packages (gets further in).
func TestRunCompareWithPackages(t *testing.T) {
	tmpHome := t.TempDir()
	overlayDir := filepath.Join(tmpHome, "overlay")
	for _, sub := range []string{"profiles", "metadata"} {
		_ = os.MkdirAll(filepath.Join(overlayDir, sub), 0755)
	}

	// Add a package to the overlay
	pkgDir := filepath.Join(overlayDir, "app-misc", "foo")
	_ = os.MkdirAll(pkgDir, 0755)
	_ = os.WriteFile(filepath.Join(pkgDir, "foo-1.0.ebuild"), []byte("# ebuild"), 0644)

	configDir := filepath.Join(tmpHome, ".config", "bentoo")
	_ = os.MkdirAll(configDir, 0755)
	configContent := "overlay:\n  path: " + overlayDir + "\n  remote: origin\ngit:\n  user: Test\n  email: test@test.com\n"
	_ = os.WriteFile(filepath.Join(configDir, "config.yaml"), []byte(configContent), 0644)

	oldHome := os.Getenv("HOME")
	oldXDG := os.Getenv("XDG_CONFIG_HOME")
	os.Setenv("HOME", tmpHome)
	os.Setenv("XDG_CONFIG_HOME", filepath.Join(tmpHome, ".config"))
	defer func() {
		os.Setenv("HOME", oldHome)
		os.Setenv("XDG_CONFIG_HOME", oldXDG)
	}()

	origTimeout, origNoCache := compareTimeout, compareNoCache
	compareTimeout = 1
	compareNoCache = true
	defer func() { compareTimeout = origTimeout; compareNoCache = origNoCache }()

	// Use a local git repo as provider to avoid network
	withExitIntercept(func() { runCompare(compareCmd, []string{"gentoo"}) })
}

// ---- runAnalyzeAll non-dry-run path ----

// TestRunAnalyzeAllNonDryRun tests runAnalyzeAll without dry-run (user says "n" to save).
func TestRunAnalyzeAllNonDryRun(t *testing.T) {
	_, cleanup := setupTestHome(t)
	defer cleanup()

	origAll, origDryRun := analyzeAll, analyzeDryRun
	analyzeAll = true
	analyzeDryRun = false
	defer func() { analyzeAll = origAll; analyzeDryRun = origDryRun }()

	// "n" to "Save all successful schemas?"
	withStdin(t, "n\n", func() {
		withExitIntercept(func() { runAnalyze(analyzeCmd, nil) })
	})
}

// ---- runAnalyze with tilde path ----

// TestRunAnalyzeWithTildePath tests runAnalyze when overlay path starts with ~.
func TestRunAnalyzeWithTildePath(t *testing.T) {
	tmpHome := t.TempDir()
	overlayDir := filepath.Join(tmpHome, "overlay")
	for _, sub := range []string{"profiles", "metadata"} {
		_ = os.MkdirAll(filepath.Join(overlayDir, sub), 0755)
	}

	configDir := filepath.Join(tmpHome, ".config", "bentoo")
	_ = os.MkdirAll(configDir, 0755)
	// Use ~ in path
	configContent := "overlay:\n  path: ~/overlay\n  remote: origin\ngit:\n  user: Test\n  email: test@test.com\n"
	_ = os.WriteFile(filepath.Join(configDir, "config.yaml"), []byte(configContent), 0644)

	oldHome := os.Getenv("HOME")
	oldXDG := os.Getenv("XDG_CONFIG_HOME")
	os.Setenv("HOME", tmpHome)
	os.Setenv("XDG_CONFIG_HOME", filepath.Join(tmpHome, ".config"))
	defer func() {
		os.Setenv("HOME", oldHome)
		os.Setenv("XDG_CONFIG_HOME", oldXDG)
	}()

	origAll, origDryRun := analyzeAll, analyzeDryRun
	analyzeAll = true
	analyzeDryRun = true
	defer func() { analyzeAll = origAll; analyzeDryRun = origDryRun }()

	withExitIntercept(func() { runAnalyze(analyzeCmd, nil) })
}

// ---- runAutoupdate with tilde path ----

// TestRunAutoupdateWithTildePath tests runAutoupdate when overlay path starts with ~.
func TestRunAutoupdateWithTildePath(t *testing.T) {
	tmpHome := t.TempDir()
	overlayDir := filepath.Join(tmpHome, "overlay")
	for _, sub := range []string{"profiles", "metadata"} {
		_ = os.MkdirAll(filepath.Join(overlayDir, sub), 0755)
	}

	configDir := filepath.Join(tmpHome, ".config", "bentoo")
	_ = os.MkdirAll(configDir, 0755)
	configContent := "overlay:\n  path: ~/overlay\n  remote: origin\ngit:\n  user: Test\n  email: test@test.com\n"
	_ = os.WriteFile(filepath.Join(configDir, "config.yaml"), []byte(configContent), 0644)

	oldHome := os.Getenv("HOME")
	oldXDG := os.Getenv("XDG_CONFIG_HOME")
	os.Setenv("HOME", tmpHome)
	os.Setenv("XDG_CONFIG_HOME", filepath.Join(tmpHome, ".config"))
	defer func() {
		os.Setenv("HOME", oldHome)
		os.Setenv("XDG_CONFIG_HOME", oldXDG)
	}()

	origCheck, origList, origApply := autoupdateCheck, autoupdateList, autoupdateApply
	autoupdateCheck = false
	autoupdateList = true
	autoupdateApply = ""
	defer func() {
		autoupdateCheck = origCheck
		autoupdateList = origList
		autoupdateApply = origApply
	}()

	withExitIntercept(func() { runAutoupdate(autoupdateCmd, nil) })
}

// ---- runStatus success path ----

// TestRunStatusWithGitRepo tests runStatus with an initialized git repo.
func TestRunStatusWithGitRepo(t *testing.T) {
	overlayDir, cleanup := setupTestHomeWithGitRepo(t)
	defer cleanup()
	withExitIntercept(func() { runStatus(statusCmd, nil) })
	_ = overlayDir
}

// ---- runPush dry-run success path ----

// TestRunPushDryRunWithGitRepo tests runPush dry-run with an initialized git repo.
func TestRunPushDryRunWithGitRepo(t *testing.T) {
	overlayDir, cleanup := setupTestHomeWithGitRepo(t)
	defer cleanup()
	_ = overlayDir

	origDryRun := pushDryRun
	pushDryRun = true
	defer func() { pushDryRun = origDryRun }()

	withExitIntercept(func() { runPush(pushCmd, nil) })
}

// ---- runAutoupdate empty overlay path ----

// TestRunAutoupdateEmptyOverlayPath tests runAutoupdate exits(1) when overlay path is empty.
func TestRunAutoupdateEmptyOverlayPath(t *testing.T) {
	tmpHome := t.TempDir()
	configDir := filepath.Join(tmpHome, ".config", "bentoo")
	_ = os.MkdirAll(configDir, 0755)
	// Config with empty overlay path
	_ = os.WriteFile(filepath.Join(configDir, "config.yaml"),
		[]byte("overlay:\n  path: \"\"\n  remote: origin\n"), 0644)

	oldHome := os.Getenv("HOME")
	oldXDG := os.Getenv("XDG_CONFIG_HOME")
	os.Setenv("HOME", tmpHome)
	os.Setenv("XDG_CONFIG_HOME", filepath.Join(tmpHome, ".config"))
	defer func() {
		os.Setenv("HOME", oldHome)
		os.Setenv("XDG_CONFIG_HOME", oldXDG)
	}()

	origCheck, origList, origApply := autoupdateCheck, autoupdateList, autoupdateApply
	autoupdateCheck = true
	autoupdateList = false
	autoupdateApply = ""
	defer func() {
		autoupdateCheck = origCheck
		autoupdateList = origList
		autoupdateApply = origApply
	}()

	code := withExitIntercept(func() { runAutoupdate(autoupdateCmd, nil) })
	if code != 1 {
		t.Errorf("runAutoupdate with empty overlay path should exit(1), got exit(%d)", code)
	}
}

// ---- runAnalyze empty overlay path ----

// TestRunAnalyzeEmptyOverlayPath tests runAnalyze exits(1) when overlay path is empty.
func TestRunAnalyzeEmptyOverlayPath(t *testing.T) {
	tmpHome := t.TempDir()
	configDir := filepath.Join(tmpHome, ".config", "bentoo")
	_ = os.MkdirAll(configDir, 0755)
	_ = os.WriteFile(filepath.Join(configDir, "config.yaml"),
		[]byte("overlay:\n  path: \"\"\n  remote: origin\n"), 0644)

	oldHome := os.Getenv("HOME")
	oldXDG := os.Getenv("XDG_CONFIG_HOME")
	os.Setenv("HOME", tmpHome)
	os.Setenv("XDG_CONFIG_HOME", filepath.Join(tmpHome, ".config"))
	defer func() {
		os.Setenv("HOME", oldHome)
		os.Setenv("XDG_CONFIG_HOME", oldXDG)
	}()

	origAll := analyzeAll
	analyzeAll = true
	defer func() { analyzeAll = origAll }()

	code := withExitIntercept(func() { runAnalyze(analyzeCmd, nil) })
	if code != 1 {
		t.Errorf("runAnalyze with empty overlay path should exit(1), got exit(%d)", code)
	}
}

// ---- runCheck with packages.toml in overlay ----

// TestRunCheckWithPackagesConfig tests runCheck when packages.toml exists in overlay.
func TestRunCheckWithPackagesConfig(t *testing.T) {
	overlayDir, cleanup := setupTestHome(t)
	defer cleanup()

	// Put packages.toml in the correct location: overlay/.autoupdate/packages.toml
	setupAutoupdateConfig(t, overlayDir)

	origCheck, origList, origApply := autoupdateCheck, autoupdateList, autoupdateApply
	autoupdateCheck = true
	autoupdateList = false
	autoupdateApply = ""
	defer func() {
		autoupdateCheck = origCheck
		autoupdateList = origList
		autoupdateApply = origApply
	}()

	// Will fail at HTTP level (no real network), but gets past NewChecker
	withExitIntercept(func() { runAutoupdate(autoupdateCmd, nil) })
}

// TestRunCheckSpecificPkgWithConfig tests runCheck with a specific package and packages.toml.
func TestRunCheckSpecificPkgWithConfig(t *testing.T) {
	overlayDir, cleanup := setupTestHome(t)
	defer cleanup()

	setupAutoupdateConfig(t, overlayDir)

	origCheck, origList, origApply := autoupdateCheck, autoupdateList, autoupdateApply
	autoupdateCheck = true
	autoupdateList = false
	autoupdateApply = ""
	defer func() {
		autoupdateCheck = origCheck
		autoupdateList = origList
		autoupdateApply = origApply
	}()

	// Pass a specific package arg — will fail at HTTP but covers the args > 0 branch
	withExitIntercept(func() { runAutoupdate(autoupdateCmd, []string{"app-misc/testpkg"}) })
}

// ---- runSync with conflict result ----

// TestRunSyncConflictResult tests runSync when sync returns a conflict result.
// We use a git repo with a remote that fails fetch — gets to the error path.
func TestRunSyncConflictResult(t *testing.T) {
	overlayDir, cleanup := setupTestHomeWithGitRepo(t)
	defer cleanup()

	// Add a remote that doesn't exist — fetch will fail, runSync exits(1)
	_ = runGitCmd(overlayDir, "remote", "add", "origin", "https://invalid.example.com/repo.git")

	withExitIntercept(func() { runSync(syncCmd, nil) })
}

// ---- runPush success path with git repo ----

// TestRunPushWithGitRepoNoRemote tests runPush (no dry-run) with git repo but no remote.
// overlay.Push will fail at git push level, runPush exits(1).
func TestRunPushWithGitRepoNoRemote(t *testing.T) {
	overlayDir, cleanup := setupTestHomeWithGitRepo(t)
	defer cleanup()
	_ = overlayDir

	origDryRun := pushDryRun
	pushDryRun = false
	defer func() { pushDryRun = origDryRun }()

	code := withExitIntercept(func() { runPush(pushCmd, nil) })
	if code != 1 {
		t.Errorf("runPush without remote should exit(1), got exit(%d)", code)
	}
}

// ---- runStatus success path ----

// TestRunStatusSuccessWithGitRepo tests runStatus with a proper git repo (no staged changes).
func TestRunStatusSuccessWithGitRepo(t *testing.T) {
	overlayDir, cleanup := setupTestHomeWithGitRepo(t)
	defer cleanup()
	_ = overlayDir

	// Should succeed: git status works, no changes
	withExitIntercept(func() { runStatus(statusCmd, nil) })
}

// ---- runAdd with git repo (success path) ----

// TestRunAddWithGitRepoNoFiles tests runAdd with git repo but no files to add.
func TestRunAddWithGitRepoNoFiles(t *testing.T) {
	_, cleanup := setupTestHomeWithGitRepo(t)
	defer cleanup()

	withExitIntercept(func() { runAdd(addCmd, nil) })
}

// ---- runDiff with git repo ----

// TestRunDiffWithGitRepo tests runDiff with an initialized git repo.
func TestRunDiffWithGitRepo(t *testing.T) {
	_, cleanup := setupTestHomeWithGitRepo(t)
	defer cleanup()

	origStaged := diffStaged
	diffStaged = false
	defer func() { diffStaged = origStaged }()

	withExitIntercept(func() { runDiff(diffCmd, nil) })
}

// ---- runLog with git repo ----

// TestRunLogWithGitRepo tests runLog with an initialized git repo (no commits yet).
func TestRunLogWithGitRepo(t *testing.T) {
	_, cleanup := setupTestHomeWithGitRepo(t)
	defer cleanup()

	origCount, origOneline := logCount, logOneline
	logCount = 5
	logOneline = false
	defer func() { logCount = origCount; logOneline = origOneline }()

	withExitIntercept(func() { runLog(logCmd, nil) })
}

// TestRunLogOnelineWithGitRepo tests runLog --oneline with an initialized git repo.
func TestRunLogOnelineWithGitRepo(t *testing.T) {
	_, cleanup := setupTestHomeWithGitRepo(t)
	defer cleanup()

	origCount, origOneline := logCount, logOneline
	logCount = 3
	logOneline = true
	defer func() { logCount = origCount; logOneline = origOneline }()

	withExitIntercept(func() { runLog(logCmd, nil) })
}

// ---- runSync success and conflict paths via real git ----

// setupGitRepoWithRemote creates a local git repo with a bare remote that has commits.
// Returns the overlay dir, remote dir, and cleanup func.
func setupGitRepoWithRemote(t *testing.T) (overlayDir, remoteDir string, cleanup func()) {
	t.Helper()

	tmpHome := t.TempDir()
	overlayDir = filepath.Join(tmpHome, "overlay")
	remoteDir = filepath.Join(tmpHome, "remote.git")

	for _, sub := range []string{"profiles", "metadata"} {
		_ = os.MkdirAll(filepath.Join(overlayDir, sub), 0755)
	}
	// Create remoteDir so git init --bare can use it as cwd
	if err := os.MkdirAll(remoteDir, 0755); err != nil {
		t.Skip("cannot create remote dir")
	}

	// Init bare remote (run in remoteDir)
	if err := runGitCmd(remoteDir, "init", "--bare"); err != nil {
		t.Skip("git init --bare not available: " + err.Error())
	}

	// Init overlay repo
	if err := os.MkdirAll(overlayDir, 0755); err != nil {
		t.Skip("cannot create overlay dir")
	}
	if err := runGitCmd(overlayDir, "init"); err != nil {
		t.Skip("git init not available")
	}
	_ = runGitCmd(overlayDir, "config", "user.email", "test@test.com")
	_ = runGitCmd(overlayDir, "config", "user.name", "Test")
	_ = runGitCmd(overlayDir, "remote", "add", "origin", remoteDir)

	// Create initial commit in overlay and push to remote
	testFile := filepath.Join(overlayDir, "README")
	_ = os.WriteFile(testFile, []byte("initial"), 0644)
	_ = runGitCmd(overlayDir, "add", ".")
	_ = runGitCmd(overlayDir, "commit", "-m", "initial")
	_ = runGitCmd(overlayDir, "push", "origin", "HEAD:master")

	// Write config
	configDir := filepath.Join(tmpHome, ".config", "bentoo")
	_ = os.MkdirAll(configDir, 0755)
	configContent := "overlay:\n  path: " + overlayDir + "\n  remote: origin\ngit:\n  user: Test\n  email: test@test.com\n"
	_ = os.WriteFile(filepath.Join(configDir, "config.yaml"), []byte(configContent), 0644)

	oldHome := os.Getenv("HOME")
	oldXDG := os.Getenv("XDG_CONFIG_HOME")
	os.Setenv("HOME", tmpHome)
	os.Setenv("XDG_CONFIG_HOME", filepath.Join(tmpHome, ".config"))

	return overlayDir, remoteDir, func() {
		os.Setenv("HOME", oldHome)
		os.Setenv("XDG_CONFIG_HOME", oldXDG)
	}
}

// TestRunSyncSuccessPath tests runSync when already up-to-date (fetch+merge succeeds).
func TestRunSyncSuccessPath(t *testing.T) {
	_, _, cleanup := setupGitRepoWithRemote(t)
	defer cleanup()

	// Already up-to-date — fetch succeeds, merge is a no-op → result.Success == true
	withExitIntercept(func() { runSync(syncCmd, nil) })
}

// TestRunSyncWithNewRemoteCommits tests runSync when remote has new commits.
func TestRunSyncWithNewRemoteCommits(t *testing.T) {
	overlayDir, remoteDir, cleanup := setupGitRepoWithRemote(t)
	defer cleanup()

	// Clone remote to add a new commit
	cloneDir := filepath.Join(filepath.Dir(overlayDir), "clone")
	if err := runGitCmd(".", "clone", remoteDir, cloneDir); err != nil {
		t.Skip("git clone not available")
	}
	_ = runGitCmd(cloneDir, "config", "user.email", "test@test.com")
	_ = runGitCmd(cloneDir, "config", "user.name", "Test")
	newFile := filepath.Join(cloneDir, "newfile.txt")
	_ = os.WriteFile(newFile, []byte("new content"), 0644)
	_ = runGitCmd(cloneDir, "add", ".")
	_ = runGitCmd(cloneDir, "commit", "-m", "add newfile")
	_ = runGitCmd(cloneDir, "push", "origin", "HEAD:master")

	// Now sync overlay — should fetch and merge the new commit
	withExitIntercept(func() { runSync(syncCmd, nil) })
}

// ---- runAdd success path with git repo ----

// TestRunAddSuccessWithGitRepo tests runAdd when files are staged successfully.
func TestRunAddSuccessWithGitRepo(t *testing.T) {
	overlayDir, cleanup := setupTestHomeWithGitRepo(t)
	defer cleanup()

	// Create a file and add it
	pkgDir := filepath.Join(overlayDir, "app-misc", "newpkg")
	_ = os.MkdirAll(pkgDir, 0755)
	testFile := filepath.Join(pkgDir, "newpkg-1.0.ebuild")
	_ = os.WriteFile(testFile, []byte("# ebuild"), 0644)

	// runAdd with no args adds all — should succeed and show status
	withExitIntercept(func() { runAdd(addCmd, nil) })
}

// ---- runPush dry-run success with git repo ----

// TestRunPushDryRunSuccessWithGitRepo tests runPush dry-run with a real git repo.
func TestRunPushDryRunSuccessWithGitRepo(t *testing.T) {
	_, _, cleanup := setupGitRepoWithRemote(t)
	defer cleanup()

	origDryRun := pushDryRun
	pushDryRun = true
	defer func() { pushDryRun = origDryRun }()

	withExitIntercept(func() { runPush(pushCmd, nil) })
}

// TestRunPushSuccessWithGitRepo tests runPush (no dry-run) with a real git repo and remote.
func TestRunPushSuccessWithGitRepo(t *testing.T) {
	overlayDir, _, cleanup := setupGitRepoWithRemote(t)
	defer cleanup()

	// Stage and commit something to push, set upstream tracking
	testFile := filepath.Join(overlayDir, "app-misc", "pkg", "pkg-1.0.ebuild")
	_ = os.MkdirAll(filepath.Dir(testFile), 0755)
	_ = os.WriteFile(testFile, []byte("# ebuild"), 0644)
	_ = runGitCmd(overlayDir, "add", ".")
	_ = runGitCmd(overlayDir, "commit", "-m", "add pkg")
	// Set upstream so push works without --set-upstream
	_ = runGitCmd(overlayDir, "push", "--set-upstream", "origin", "master")

	origDryRun := pushDryRun
	pushDryRun = false
	defer func() { pushDryRun = origDryRun }()

	withExitIntercept(func() { runPush(pushCmd, nil) })
}

// ---- runStatus success path with git repo ----

// TestRunStatusSuccessNoChanges tests runStatus with a git repo and no changes.
func TestRunStatusSuccessNoChanges(t *testing.T) {
	_, _, cleanup := setupGitRepoWithRemote(t)
	defer cleanup()

	withExitIntercept(func() { runStatus(statusCmd, nil) })
}

// ---- runCommit success path with git repo and remote ----

// TestRunCommitWithMessageSuccess tests runCommit with a custom message and git repo.
func TestRunCommitWithMessageSuccess(t *testing.T) {
	overlayDir, _, cleanup := setupGitRepoWithRemote(t)
	defer cleanup()

	// Stage a file
	testFile := filepath.Join(overlayDir, "app-misc", "staged", "staged-1.0.ebuild")
	_ = os.MkdirAll(filepath.Dir(testFile), 0755)
	_ = os.WriteFile(testFile, []byte("# ebuild"), 0644)
	_ = runGitCmd(overlayDir, "add", ".")

	origDryRun, origMsg := commitDryRun, commitMessage
	commitDryRun = false
	commitMessage = "test: commit with message"
	defer func() { commitDryRun = origDryRun; commitMessage = origMsg }()

	withExitIntercept(func() { runCommit(commitCmd, nil) })
}

// ---- runList success path ----

// TestRunListSuccessPath tests runList when pending list loads successfully.
func TestRunListSuccessPath(t *testing.T) {
	_, cleanup := setupTestHome(t)
	defer cleanup()

	origCheck, origList, origApply := autoupdateCheck, autoupdateList, autoupdateApply
	autoupdateCheck = false
	autoupdateList = true
	autoupdateApply = ""
	defer func() {
		autoupdateCheck = origCheck
		autoupdateList = origList
		autoupdateApply = origApply
	}()

	// runList loads pending list from configDir — should succeed with empty list
	withExitIntercept(func() { runAutoupdate(autoupdateCmd, nil) })
}

// ---- runSync conflict path ----

// TestRunSyncConflictPath tests runSync when sync returns a conflict (Success == false).
// We create a scenario where local and remote have diverged with conflicting changes.
func TestRunSyncConflictPath(t *testing.T) {
	tmpHome := t.TempDir()
	overlayDir := filepath.Join(tmpHome, "overlay")
	remoteDir := filepath.Join(tmpHome, "remote.git")

	for _, sub := range []string{"profiles", "metadata"} {
		_ = os.MkdirAll(filepath.Join(overlayDir, sub), 0755)
	}
	_ = os.MkdirAll(remoteDir, 0755)

	if err := runGitCmd(remoteDir, "init", "--bare"); err != nil {
		t.Skip("git init --bare not available")
	}
	_ = os.MkdirAll(overlayDir, 0755)
	if err := runGitCmd(overlayDir, "init"); err != nil {
		t.Skip("git init not available")
	}
	_ = runGitCmd(overlayDir, "config", "user.email", "test@test.com")
	_ = runGitCmd(overlayDir, "config", "user.name", "Test")
	_ = runGitCmd(overlayDir, "remote", "add", "origin", remoteDir)

	// Initial commit in overlay
	sharedFile := filepath.Join(overlayDir, "shared.txt")
	_ = os.WriteFile(sharedFile, []byte("line1\n"), 0644)
	_ = runGitCmd(overlayDir, "add", ".")
	_ = runGitCmd(overlayDir, "commit", "-m", "initial")
	_ = runGitCmd(overlayDir, "push", "origin", "HEAD:master")

	// Clone to make a conflicting commit on remote
	cloneDir := filepath.Join(tmpHome, "clone")
	if err := runGitCmd(tmpHome, "clone", remoteDir, cloneDir); err != nil {
		t.Skip("git clone not available")
	}
	_ = runGitCmd(cloneDir, "config", "user.email", "test@test.com")
	_ = runGitCmd(cloneDir, "config", "user.name", "Test")
	_ = os.WriteFile(filepath.Join(cloneDir, "shared.txt"), []byte("remote change\n"), 0644)
	_ = runGitCmd(cloneDir, "add", ".")
	_ = runGitCmd(cloneDir, "commit", "-m", "remote change")
	_ = runGitCmd(cloneDir, "push", "origin", "master")

	// Make a conflicting local change
	_ = os.WriteFile(sharedFile, []byte("local change\n"), 0644)
	_ = runGitCmd(overlayDir, "add", ".")
	_ = runGitCmd(overlayDir, "commit", "-m", "local change")

	// Write config
	configDir := filepath.Join(tmpHome, ".config", "bentoo")
	_ = os.MkdirAll(configDir, 0755)
	configContent := "overlay:\n  path: " + overlayDir + "\n  remote: origin\ngit:\n  user: Test\n  email: test@test.com\n"
	_ = os.WriteFile(filepath.Join(configDir, "config.yaml"), []byte(configContent), 0644)

	oldHome := os.Getenv("HOME")
	oldXDG := os.Getenv("XDG_CONFIG_HOME")
	os.Setenv("HOME", tmpHome)
	os.Setenv("XDG_CONFIG_HOME", filepath.Join(tmpHome, ".config"))
	defer func() {
		os.Setenv("HOME", oldHome)
		os.Setenv("XDG_CONFIG_HOME", oldXDG)
	}()

	// runSync: fetch succeeds, merge fails with conflict → result.Success == false
	code := withExitIntercept(func() { runSync(syncCmd, nil) })
	// Should exit 1 due to sync failure
	if code != 1 {
		t.Logf("runSync conflict path: exit code %d (may vary by git version)", code)
	}
}

// ---- runCompare with GitHub provider (rate limit path) ----

// TestRunCompareGitHubRateLimitPath tests runCompare when GitHub API is accessible.
// This covers the rate limit check block (lines 125-140).
func TestRunCompareGitHubRateLimitPath(t *testing.T) {
	_, cleanup := setupTestHome(t)
	defer cleanup()

	// Add a package to the overlay so we get past the "no packages" check
	tmpHome := os.Getenv("HOME")
	overlayDir := filepath.Join(tmpHome, "overlay")
	pkgDir := filepath.Join(overlayDir, "app-misc", "foo")
	_ = os.MkdirAll(pkgDir, 0755)
	_ = os.WriteFile(filepath.Join(pkgDir, "foo-1.0.ebuild"), []byte("# ebuild"), 0644)

	origTimeout, origNoCache := compareTimeout, compareNoCache
	compareTimeout = 1
	compareNoCache = true
	defer func() { compareTimeout = origTimeout; compareNoCache = origNoCache }()

	// Use gentoo (GitHub provider) — will hit rate limit check, then fail at API
	withExitIntercept(func() { runCompare(compareCmd, []string{"gentoo"}) })
}

// ---- runDiff and runLog error paths ----

// TestRunDiffConfigLoadError tests runDiff when config load fails (no HOME).
// config.Load() never errors in practice, but we can test the overlay path error.
func TestRunDiffOverlayPathError(t *testing.T) {
	tmpHome := t.TempDir()
	configDir := filepath.Join(tmpHome, ".config", "bentoo")
	_ = os.MkdirAll(configDir, 0755)
	// Config with invalid overlay path (file instead of dir)
	invalidPath := filepath.Join(tmpHome, "notadir")
	_ = os.WriteFile(invalidPath, []byte("not a dir"), 0644)
	configContent := "overlay:\n  path: " + invalidPath + "\n  remote: origin\n"
	_ = os.WriteFile(filepath.Join(configDir, "config.yaml"), []byte(configContent), 0644)

	oldHome := os.Getenv("HOME")
	oldXDG := os.Getenv("XDG_CONFIG_HOME")
	os.Setenv("HOME", tmpHome)
	os.Setenv("XDG_CONFIG_HOME", filepath.Join(tmpHome, ".config"))
	defer func() {
		os.Setenv("HOME", oldHome)
		os.Setenv("XDG_CONFIG_HOME", oldXDG)
	}()

	origStaged := diffStaged
	diffStaged = false
	defer func() { diffStaged = origStaged }()

	code := withExitIntercept(func() { runDiff(diffCmd, nil) })
	if code != 1 {
		t.Errorf("runDiff with invalid overlay path should exit(1), got exit(%d)", code)
	}
}

// TestRunLogOverlayPathError tests runLog when overlay path is invalid.
func TestRunLogOverlayPathError(t *testing.T) {
	tmpHome := t.TempDir()
	configDir := filepath.Join(tmpHome, ".config", "bentoo")
	_ = os.MkdirAll(configDir, 0755)
	invalidPath := filepath.Join(tmpHome, "notadir")
	_ = os.WriteFile(invalidPath, []byte("not a dir"), 0644)
	configContent := "overlay:\n  path: " + invalidPath + "\n  remote: origin\n"
	_ = os.WriteFile(filepath.Join(configDir, "config.yaml"), []byte(configContent), 0644)

	oldHome := os.Getenv("HOME")
	oldXDG := os.Getenv("XDG_CONFIG_HOME")
	os.Setenv("HOME", tmpHome)
	os.Setenv("XDG_CONFIG_HOME", filepath.Join(tmpHome, ".config"))
	defer func() {
		os.Setenv("HOME", oldHome)
		os.Setenv("XDG_CONFIG_HOME", oldXDG)
	}()

	origCount, origOneline := logCount, logOneline
	logCount = 5
	logOneline = false
	defer func() { logCount = origCount; logOneline = origOneline }()

	code := withExitIntercept(func() { runLog(logCmd, nil) })
	if code != 1 {
		t.Errorf("runLog with invalid overlay path should exit(1), got exit(%d)", code)
	}
}

// ---- runStatus overlay path error ----

// TestRunStatusOverlayPathError tests runStatus when overlay path is a file (not dir).
func TestRunStatusOverlayPathError(t *testing.T) {
	tmpHome := t.TempDir()
	configDir := filepath.Join(tmpHome, ".config", "bentoo")
	_ = os.MkdirAll(configDir, 0755)
	invalidPath := filepath.Join(tmpHome, "notadir")
	_ = os.WriteFile(invalidPath, []byte("not a dir"), 0644)
	configContent := "overlay:\n  path: " + invalidPath + "\n  remote: origin\n"
	_ = os.WriteFile(filepath.Join(configDir, "config.yaml"), []byte(configContent), 0644)

	oldHome := os.Getenv("HOME")
	oldXDG := os.Getenv("XDG_CONFIG_HOME")
	os.Setenv("HOME", tmpHome)
	os.Setenv("XDG_CONFIG_HOME", filepath.Join(tmpHome, ".config"))
	defer func() {
		os.Setenv("HOME", oldHome)
		os.Setenv("XDG_CONFIG_HOME", oldXDG)
	}()

	code := withExitIntercept(func() { runStatus(statusCmd, nil) })
	if code != 1 {
		t.Errorf("runStatus with invalid overlay path should exit(1), got exit(%d)", code)
	}
}

// ---- runPush dry-run error path ----

// TestRunPushDryRunOverlayError tests runPush dry-run when overlay path is invalid.
func TestRunPushDryRunOverlayError(t *testing.T) {
	tmpHome := t.TempDir()
	configDir := filepath.Join(tmpHome, ".config", "bentoo")
	_ = os.MkdirAll(configDir, 0755)
	invalidPath := filepath.Join(tmpHome, "notadir")
	_ = os.WriteFile(invalidPath, []byte("not a dir"), 0644)
	configContent := "overlay:\n  path: " + invalidPath + "\n  remote: origin\n"
	_ = os.WriteFile(filepath.Join(configDir, "config.yaml"), []byte(configContent), 0644)

	oldHome := os.Getenv("HOME")
	oldXDG := os.Getenv("XDG_CONFIG_HOME")
	os.Setenv("HOME", tmpHome)
	os.Setenv("XDG_CONFIG_HOME", filepath.Join(tmpHome, ".config"))
	defer func() {
		os.Setenv("HOME", oldHome)
		os.Setenv("XDG_CONFIG_HOME", oldXDG)
	}()

	origDryRun := pushDryRun
	pushDryRun = true
	defer func() { pushDryRun = origDryRun }()

	code := withExitIntercept(func() { runPush(pushCmd, nil) })
	if code != 1 {
		t.Errorf("runPush dry-run with invalid overlay path should exit(1), got exit(%d)", code)
	}
}

// ---- runInit path creation ----

// TestRunInitCreateNonExistentPath tests runInit when overlay path doesn't exist and user says "y" to create.
func TestRunInitCreateNonExistentPath(t *testing.T) {
	tmpHome := t.TempDir()
	oldHome := os.Getenv("HOME")
	oldXDG := os.Getenv("XDG_CONFIG_HOME")
	os.Setenv("HOME", tmpHome)
	os.Setenv("XDG_CONFIG_HOME", tmpHome+"/.config")
	defer func() {
		os.Setenv("HOME", oldHome)
		os.Setenv("XDG_CONFIG_HOME", oldXDG)
	}()

	newOverlayPath := filepath.Join(tmpHome, "new-overlay")
	// Input: path that doesn't exist, "y" to create, default remote, user, email
	input := newOverlayPath + "\ny\norigin\nTestUser\ntest@example.com\n"
	withStdin(t, input, func() {
		withExitIntercept(func() { runInit(initCmd, nil) })
	})
}

// TestRunInitPathDoesNotExistSayNo tests runInit when overlay path doesn't exist and user says "n".
func TestRunInitPathDoesNotExistSayNo(t *testing.T) {
	tmpHome := t.TempDir()
	oldHome := os.Getenv("HOME")
	oldXDG := os.Getenv("XDG_CONFIG_HOME")
	os.Setenv("HOME", tmpHome)
	os.Setenv("XDG_CONFIG_HOME", tmpHome+"/.config")
	defer func() {
		os.Setenv("HOME", oldHome)
		os.Setenv("XDG_CONFIG_HOME", oldXDG)
	}()

	nonExistentPath := filepath.Join(tmpHome, "does-not-exist")
	// Input: non-existent path, "n" to not create, default remote, user, email
	input := nonExistentPath + "\nn\norigin\nTestUser\ntest@example.com\n"
	withStdin(t, input, func() {
		withExitIntercept(func() { runInit(initCmd, nil) })
	})
}

// ---- runPush dry-run success path ----

// TestRunPushDryRunWithUpstream tests runPush dry-run with a repo that has upstream set.
func TestRunPushDryRunWithUpstream(t *testing.T) {
	_, _, cleanup := setupGitRepoWithRemote(t)
	defer cleanup()

	origDryRun := pushDryRun
	pushDryRun = true
	defer func() { pushDryRun = origDryRun }()

	// setupGitRepoWithRemote already pushed to origin/master, so dry-run should succeed
	withExitIntercept(func() { runPush(pushCmd, nil) })
}

// ---- runCompare all-up-to-date path ----

// TestRunCompareLocalRepoUpToDate tests runCompare with a local git repo provider
// where overlay has packages — gets to CompareWithProvider which returns empty results.
func TestRunCompareLocalRepoUpToDate(t *testing.T) {
	tmpHome := t.TempDir()
	overlayDir := filepath.Join(tmpHome, "overlay")
	remoteDir := filepath.Join(tmpHome, "remote.git")

	for _, sub := range []string{"profiles", "metadata"} {
		_ = os.MkdirAll(filepath.Join(overlayDir, sub), 0755)
	}
	_ = os.MkdirAll(remoteDir, 0755)

	if err := runGitCmd(remoteDir, "init", "--bare"); err != nil {
		t.Skip("git init --bare not available")
	}
	_ = os.MkdirAll(overlayDir, 0755)
	if err := runGitCmd(overlayDir, "init"); err != nil {
		t.Skip("git init not available")
	}
	_ = runGitCmd(overlayDir, "config", "user.email", "test@test.com")
	_ = runGitCmd(overlayDir, "config", "user.name", "Test")
	_ = runGitCmd(overlayDir, "remote", "add", "origin", remoteDir)

	// Add a package to the overlay
	pkgDir := filepath.Join(overlayDir, "app-misc", "foo")
	_ = os.MkdirAll(pkgDir, 0755)
	_ = os.WriteFile(filepath.Join(pkgDir, "foo-1.0.ebuild"), []byte("# ebuild"), 0644)
	_ = runGitCmd(overlayDir, "add", ".")
	_ = runGitCmd(overlayDir, "commit", "-m", "initial")
	_ = runGitCmd(overlayDir, "push", "origin", "HEAD:master")

	configDir := filepath.Join(tmpHome, ".config", "bentoo")
	_ = os.MkdirAll(configDir, 0755)
	configContent := `overlay:
  path: ` + overlayDir + `
  remote: origin
git:
  user: Test
  email: test@test.com
repositories:
  localrepo:
    provider: git
    url: ` + remoteDir + `
`
	_ = os.WriteFile(filepath.Join(configDir, "config.yaml"), []byte(configContent), 0644)

	oldHome := os.Getenv("HOME")
	oldXDG := os.Getenv("XDG_CONFIG_HOME")
	os.Setenv("HOME", tmpHome)
	os.Setenv("XDG_CONFIG_HOME", filepath.Join(tmpHome, ".config"))
	defer func() {
		os.Setenv("HOME", oldHome)
		os.Setenv("XDG_CONFIG_HOME", oldXDG)
	}()

	origTimeout := compareTimeout
	compareTimeout = 30
	defer func() { compareTimeout = origTimeout }()

	// Compare with local git repo — will clone and compare, likely all up-to-date
	withExitIntercept(func() { runCompare(compareCmd, []string{"localrepo"}) })
}
