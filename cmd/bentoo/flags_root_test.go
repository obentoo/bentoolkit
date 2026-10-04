package main

// Authored for story 046, sub-task 4.3 — R3.1, R3.3.
//
// Written from the contract: R3.1 — "WHEN --ui, --export or --all is passed to
// a command that produces a report, THE SYSTEM SHALL honour it WITHOUT THAT
// COMMAND DECLARING THE FLAG ITSELF" — and design.md D4, which puts the three
// beside --verbose, --quiet and --no-color as persistent flags on the root.
//
// Unchanged Behavior 2 and story 044's R3.5 fix the other half: --no-tui is
// deprecated in its help text ONLY, and it stays where it is. It is
// autoupdate's flag, it still outranks --ui, and moving it to the root would be
// a behaviour change nobody asked for.
//
// It runs the commands rather than asserting the wiring that would reach them:
// a flag registered on the root and never consulted passes every structural
// check and does nothing at all.
//
// Red on arrival: the three flags are declared on `overlay autoupdate` alone,
// so `overlay manifest --ui=plain` is rejected as an unknown flag.

import (
	"os"
	"strings"
	"testing"

	"github.com/obentoo/bentoolkit/internal/snapshot"
)

// TestRootFlagsAppearOnACommandThatDeclaresNone is the help-text half of R3.1,
// and it is what an operator actually meets: a flag that works but is not
// listed is a flag nobody finds.
func TestRootFlagsAppearOnACommandThatDeclaresNone(t *testing.T) {
	c := newTestCLI(t)

	stdout, stderr, code := c.Run("overlay", "manifest", "--help")
	if code != 0 {
		t.Fatalf("`overlay manifest --help` exited %d (stderr: %s)", code, stderr)
	}

	for _, flag := range []string{"--ui", "--export", "--all"} {
		if !strings.Contains(stdout, flag) {
			t.Errorf("`overlay manifest --help` does not list %s — the flag is declared on autoupdate alone, so every other report-producing command is a command the operator has to guess about (R3.1)\n%s", flag, stdout)
		}
	}
}

// TestRootFlagsAreHonouredWithoutDeclaration is the behaviour half. A dry run
// is used because what is under test is whether the flags REACH the command,
// not what the command does with the packages it finds.
func TestRootFlagsAreHonouredWithoutDeclaration(t *testing.T) {
	c := newTestCLI(t)
	writeExitTestEbuild(t, c.Overlay(), "app-misc/jq", "1.7.1")
	writeExitTestEbuild(t, c.Overlay(), "dev-lang/go", "1.26.0")

	stdout, stderr, code := c.Run("overlay", "manifest", "--dry-run", "--ui=plain", "--all")

	if strings.Contains(stderr, "unknown flag") {
		t.Fatalf("a root flag was rejected by a command that does not declare it (R3.1): %s", stderr)
	}
	if code != 0 {
		t.Errorf("`overlay manifest --dry-run --ui=plain --all` exited %d\nstdout:\n%s\nstderr:\n%s", code, stdout, stderr)
	}
	if strings.ContainsRune(stdout, 0x1b) {
		t.Errorf("--ui=plain produced escape sequences — the flag reached the command and was not honoured (R2.2, R3.1):\n%q", stdout)
	}
}

// TestRootFlagsReachEveryReportProducer is the assertion the two above cannot
// make between them. One command honouring the flags is exactly what the story
// starts from; what R3.1 asks is that a command NOT REMEMBERED still honours
// them, which is only observable across more than one.
// It IS every report producer now, and it took three stories to become one.
// This repository has five: `overlay manifest`, `overlay autoupdate --check`,
// `overlay validate`, `snapshot run` and `overlay compare`, each a
// `func present*Report` calling `func exportReport` in report_export.go. Two
// were listed when 046 wrote this, which is how `overlay compare` could join
// the envelope in 047 without a single assertion noticing; 047 added the third
// and named the remaining two as a measured gap rather than closing them,
// because their seeds were outside its scope. They are closed here: `overlay
// validate` through stubValidateRunner, and `snapshot run` through the same
// mocked pipeline snapshot_report_test.go drives.
//
// A row whose seed needs a path returns it, because `snapshot run` reaches its
// runner only through a config file written into a temp dir and no table above
// a t.Run can spell that.
//
// The seed is per row from 5.4 onward. `func seedCheckOverlay` gives the first
// two rows the packages and the upstream stub they need; `overlay compare`
// additionally needs a repository to compare AGAINST, and
// `func seedCompareFixture` in compare_ambient_mode_test.go supplies a local
// ::gentoo tree so the row reaches the report without the network and without a
// real tree on the machine running the suite.
func TestRootFlagsReachEveryReportProducer(t *testing.T) {
	for _, tc := range []struct {
		command []string
		// seed prepares the row and returns any FURTHER arguments it needs.
		// `snapshot run` reaches its runner only through a config file written
		// into a temp dir, so its path cannot be spelled in the table above it.
		seed func(t *testing.T, c *testCLI) []string
	}{
		{command: []string{"overlay", "manifest", "--dry-run"}},
		{command: []string{"overlay", "autoupdate", "--check", "--force"}},
		{
			command: []string{"overlay", "compare", "--no-review"},
			seed: func(t *testing.T, c *testCLI) []string {
				seedCompareFixture(t, c, true)
				return nil
			},
		},
		{
			command: []string{"overlay", "validate"},
			seed: func(_ *testing.T, c *testCLI) []string {
				stubValidateRunner(c.deps, mixedReport())
				return nil
			},
		},
		{
			command: []string{"snapshot", "run"},
			seed: func(t *testing.T, c *testCLI) []string {
				stubBinariesOnPath(t, "btrbk", "ssh")
				_, configPath := writeSnapshotConfig(t, validSnapshotTOML)
				redirectStateDir(t)
				c.deps.snapshotRunner = &snapshot.MockRunner{}
				return []string{"--config=" + configPath}
			},
		},
	} {
		t.Run(strings.Join(tc.command, " "), func(t *testing.T) {
			c := newTestCLI(t)
			seedCheckOverlay(t, c, "1.7.1", "1.8.0", "app-misc/jq")
			var extra []string
			if tc.seed != nil {
				extra = tc.seed(t, c)
			}

			export := t.TempDir() + "/report.json"
			args := append(append([]string{}, tc.command...), extra...)
			args = append(args, "--ui=plain", "--export="+export)

			_, stderr, _ := c.Run(args...)

			if strings.Contains(stderr, "unknown flag") {
				t.Errorf("%v rejected a root flag (R3.1): %s", tc.command, stderr)
			}
			// The assertion the one above cannot make. A flag has not been
			// REACHED merely because cobra declined to call it unknown: a local
			// flag shadowing a root one is refused as an "invalid argument"
			// instead, and every row stays green while the run dies. Measured,
			// on a local --ui bool added to `overlay compare` — the realistic
			// mistake of somebody adding a sixth producer — the string check
			// above says nothing and this one fails.
			//
			// The export file is the flag having ARRIVED and done its work,
			// which is the whole of what this guard's name claims. All five
			// rows already satisfied it before it was written, so nothing was
			// widened to fit.
			if _, err := os.Stat(export); err != nil {
				t.Errorf("%v honoured no --export, so a root flag did not reach it (S046-R3.1): %v\nstderr:\n%s",
					tc.command, err, stderr)
			}
		})
	}
}

// TestRootFlagsLeaveNoTUIWhereItIs pins Unchanged Behavior 2. --no-tui is
// autoupdate's, deprecated in its help text and nowhere else; promoting it
// would give every command a second opt-out competing with --ui, which is the
// drift story 045 was written to remove.
func TestRootFlagsLeaveNoTUIWhereItIs(t *testing.T) {
	c := newTestCLI(t)

	autoupdateHelp, _, _ := c.Run("overlay", "autoupdate", "--help")
	if !strings.Contains(autoupdateHelp, "--no-tui") {
		t.Errorf("`overlay autoupdate --help` no longer lists --no-tui — the flag is deprecated in its wording only (Unchanged Behavior 2)\n%s", autoupdateHelp)
	}

	manifestHelp, _, _ := c.Run("overlay", "manifest", "--help")
	if strings.Contains(manifestHelp, "--no-tui") {
		t.Errorf("--no-tui reached `overlay manifest` — it was moved to the root, giving every command a second opt-out beside --ui\n%s", manifestHelp)
	}

	_, stderr, code := c.Run("overlay", "manifest", "--dry-run", "--no-tui")
	if code == 0 || !strings.Contains(stderr, "no-tui") {
		t.Errorf("`overlay manifest --no-tui` exited %d with stderr %q — a flag that is not declared there must be rejected there", code, stderr)
	}
}
