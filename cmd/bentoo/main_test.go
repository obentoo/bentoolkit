package main

import (
	"fmt"
	"os"
	"testing"
)

// TestMain keeps the whole package's runs out of the developer's real state
// directory (story 062). Every tree whose command runs opens
// $XDG_STATE_HOME/bentoo/logs/bentoo.log, and many tests here build the root
// outside newTestCLI; without this, each of them would append to
// ~/.local/state/bentoo/logs/bentoo.log. BENTOO_LOG_LEVEL is cleared for the
// same reason: a value in the developer's shell would change what a test's
// stderr carries.
//
// A re-exec'd child runs TestMain too, and inherits the parent's environment.
// The parent marks the environment it isolated with testStateIsolatedEnv; a
// child that finds the mark keeps the XDG_STATE_HOME and BENTOO_LOG_LEVEL its
// parent gave it — either the parent's temporary directory or one the test
// chose on purpose — rather than overwriting a choice the test made.
func TestMain(m *testing.M) {
	os.Exit(runTests(m))
}

// testStateIsolatedEnv marks a process environment TestMain already isolated.
const testStateIsolatedEnv = "BENTOO_CMD_TEST_STATE_ISOLATED"

// runTests is TestMain's body, split out so the temporary directory is removed
// by a deferred call before os.Exit, which would skip it.
func runTests(m *testing.M) int {
	if os.Getenv(testStateIsolatedEnv) != "" {
		return m.Run()
	}

	state, err := os.MkdirTemp("", "bentoo-cmd-test-state-")
	if err != nil {
		fmt.Fprintf(os.Stderr, "TestMain: creating a temporary XDG_STATE_HOME: %v\n", err)
		return 1
	}
	defer func() { _ = os.RemoveAll(state) }()

	if err := os.Setenv("XDG_STATE_HOME", state); err != nil {
		fmt.Fprintf(os.Stderr, "TestMain: setting XDG_STATE_HOME: %v\n", err)
		return 1
	}
	if err := os.Unsetenv(logLevelEnv); err != nil {
		fmt.Fprintf(os.Stderr, "TestMain: unsetting %s: %v\n", logLevelEnv, err)
		return 1
	}
	if err := os.Setenv(testStateIsolatedEnv, "1"); err != nil {
		fmt.Fprintf(os.Stderr, "TestMain: setting %s: %v\n", testStateIsolatedEnv, err)
		return 1
	}
	return m.Run()
}
