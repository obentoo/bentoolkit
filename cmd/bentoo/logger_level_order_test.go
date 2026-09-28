package main

import (
	"path/filepath"
	"testing"

	"github.com/spf13/cobra"

	"github.com/obentoo/bentoolkit/internal/common/logger"
)

// loggerLevelFlagTests run the shared root command with a global logging flag.
// The flag's whole effect is a change to the process-wide logger level.
var loggerLevelFlagTests = []struct {
	name string
	run  func(*testing.T)
}{
	{"TestQuietFlagConfiguresLogger", TestQuietFlagConfiguresLogger},
	{"TestQuietFlagBehavior", TestQuietFlagBehavior},
	{"TestVerboseFlagConfiguresLogger", TestVerboseFlagConfiguresLogger},
	{"TestVerboseFlagBehavior", TestVerboseFlagBehavior},
}

// loggerOutputTests assert on a line the logger writes to stderr. Each passes
// when it is the first test in the binary to run, and each fails when a quiet
// run of the root command has run before it (measured 2026-09-28 by starting
// the package with the logger already quiet).
var loggerOutputTests = []struct {
	name string
	run  func(*testing.T)
}{
	{"TestAutoupdateAmbientModeFromTheEnvironmentKeepsTheRunAndStatesItself", TestAutoupdateAmbientModeFromTheEnvironmentKeepsTheRunAndStatesItself},
	{"TestAutoupdateAmbientModeFromTheConfigurationKeepsTheRunAndStatesItself", TestAutoupdateAmbientModeFromTheConfigurationKeepsTheRunAndStatesItself},
	{"TestAutoupdateAmbientModeStatesTheRefusalExactlyOnce", TestAutoupdateAmbientModeStatesTheRefusalExactlyOnce},
	{"TestCompareAmbientModeFromTheEnvironmentKeepsTheRunAndStatesItself", TestCompareAmbientModeFromTheEnvironmentKeepsTheRunAndStatesItself},
	{"TestCompareAmbientModeFromTheConfigurationKeepsTheRunAndStatesItself", TestCompareAmbientModeFromTheConfigurationKeepsTheRunAndStatesItself},
	{"TestCompareAmbientModeStatesTheRefusalExactlyOnce", TestCompareAmbientModeStatesTheRefusalExactlyOnce},
	{"TestAutoupdateOverlayLock_SweepsDeadTemps", TestAutoupdateOverlayLock_SweepsDeadTemps},
	{"TestValidateExportWriteFailurePreservesTheExitStatus", TestValidateExportWriteFailurePreservesTheExitStatus},
	{"TestAmbientModeRefusalKeepsTheRunAndStatesItself", TestAmbientModeRefusalKeepsTheRunAndStatesItself},
	{"TestAmbientModeRefusalReachesTheSnapshotProducerToo", TestAmbientModeRefusalReachesTheSnapshotProducerToo},
	{"TestAmbientModeRefusalCoversTheConfiguredSourceToo", TestAmbientModeRefusalCoversTheConfiguredSourceToo},
	{"TestAmbientModeRefusalStaysApartFromTheDowngradeSentence", TestAmbientModeRefusalStaysApartFromTheDowngradeSentence},
	{"TestValidateAmbientModeFromTheEnvironmentKeepsTheRunAndStatesItself", TestValidateAmbientModeFromTheEnvironmentKeepsTheRunAndStatesItself},
	{"TestValidateAmbientModeFromTheConfigurationKeepsTheRunAndStatesItself", TestValidateAmbientModeFromTheConfigurationKeepsTheRunAndStatesItself},
	{"TestValidateAmbientModeUnderJSONStatesItWithoutBreakingTheDocument", TestValidateAmbientModeUnderJSONStatesItWithoutBreakingTheDocument},
}

// TestLoggerLevelFlagsDoNotSilenceALaterTest pins the orders a shuffled run
// reaches only by chance: every logging-flag test, followed by every test that
// reads the logger's output.
//
// A shuffle seed exposes whichever of those tests happen to land after a flag
// test, and which ones that is moves whenever a test is added to or removed
// from the package. This order does not move.
func TestLoggerLevelFlagsDoNotSilenceALaterTest(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_STATE_HOME", filepath.Join(home, ".local", "state"))
	t.Cleanup(func() { resetLoggerLevelFlagState(t) })

	for _, flag := range loggerLevelFlagTests {
		t.Run("after_"+flag.name, func(t *testing.T) {
			// Each flag is measured alone, from the level the binary starts at.
			resetLoggerLevelFlagState(t)
			t.Run(flag.name, flag.run)
			for _, reader := range loggerOutputTests {
				t.Run(reader.name, reader.run)
			}
			if t.Failed() {
				t.Errorf("a test that reads the logger's output failed after %s ran: the level that flag set outlived the test that set it", flag.name)
			}
		})
	}
}

// resetLoggerLevelFlagState puts the shared root command's flags and the
// process-wide logger back to the state the test binary starts in.
//
// The help flags are part of it: a test that ran `version --help` on the shared
// tree leaves --help set on that subcommand, and every later run of `version`
// then prints help and skips the pre-run, so a flag test that runs after it
// changes nothing and the order this test pins is never exercised.
func resetLoggerLevelFlagState(t *testing.T) {
	t.Helper()
	for _, name := range []string{"quiet", "verbose"} {
		if err := rootCmd.PersistentFlags().Set(name, "false"); err != nil {
			t.Fatalf("resetting the root command's --%s flag: %v", name, err)
		}
	}
	resetHelpFlags(t, rootCmd)
	logger.Default().SetLevel(logger.LevelInfo)
}

// resetHelpFlags clears --help on cmd and every command below it.
func resetHelpFlags(t *testing.T, cmd *cobra.Command) {
	t.Helper()
	if cmd.Flags().Lookup("help") != nil {
		if err := cmd.Flags().Set("help", "false"); err != nil {
			t.Fatalf("resetting --help on %q: %v", cmd.CommandPath(), err)
		}
	}
	for _, sub := range cmd.Commands() {
		resetHelpFlags(t, sub)
	}
}
