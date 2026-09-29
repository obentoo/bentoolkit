package main

// Authored for story 046, sub-task 11.1 — R3.6.
//
// The rule: a run refused before it does any work states its reason ONCE.
// R3.2 already requires the refusal to name the accepted set; it says nothing
// about how many times, and 4.4 made that gap reachable. Moving --ui
// validation out of `overlay autoupdate` and into the root's PersistentPreRunE
// gave the returned error two printers: cobra's own, and main.go's
// `fmt.Fprintln(os.Stderr, err)`. Both run. Every one of the 30 commands now
// prints the same sentence twice.
//
// # Why this test builds the binary, and 4.4's three tests could not catch it
//
// testCLI.Run calls newRootCmd() and cmd.Execute() IN PROCESS. main() is never
// entered, so main.go's printer never runs inside the harness, and the harness
// reads one copy of a sentence the operator reads two of. That is not a flaw in
// the harness — it is doing what it was built for — but it does mean the
// doubling lives in exactly the seam it cannot observe.
//
// S046-R8.4 already answers what to do about that: "WHEN a command's
// end-to-end behaviour is asserted, THE SYSTEM SHALL execute that command
// through its real entry point, rather than asserting the wiring that would
// reach it." Asserting `rootCmd.SilenceErrors == true` would be asserting the
// wiring. So this test compiles the program and runs it.
//
// `version` is the probe deliberately: it touches no config, no overlay and no
// network, so a failure here is the rejection path and never the command.
//
// # RED ON ARRIVAL
//
// The sentence is printed twice today, once by cobra and once by main.go.

import (
	"errors"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// buildBentoo compiles the program under test and returns the binary's path.
//
// The binary is what R8.4 asks for: the real entry point, main() included.
// Compiling it per test package costs one build and buys the only view of
// stderr that matches what an operator sees.
func buildBentoo(t *testing.T) string {
	t.Helper()

	bin := filepath.Join(t.TempDir(), "bentoo")
	build := exec.Command("go", "build", "-o", bin, ".")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("building the program under test failed: %v\n%s", err, out)
	}
	return bin
}

// TestRejectionIsStatedOnce pins R3.6 on the path that made it reachable.
//
// The assertion counts occurrences rather than comparing the whole stream: the
// requirement is about how many times the reason is stated, and a test that
// pinned the exact bytes of stderr would fail for every unrelated wording
// change while saying nothing about the count.
func TestRejectionIsStatedOnce(t *testing.T) {
	bentoo := buildBentoo(t)

	out, err := exec.Command(bentoo, "version", "--ui=bogus").CombinedOutput()
	if err == nil {
		t.Fatal("`version --ui=bogus` succeeded; R3.2 requires the run to be refused")
	}

	const reason = "is not a UI mode"
	if got := strings.Count(string(out), reason); got != 1 {
		t.Errorf("the refusal is stated %d times; R3.6 requires exactly once.\n"+
			"    Remedy: the error returned by Execute() reaches two printers — cobra's and\n"+
			"    main.go's. Silence one. root.go already sets SilenceUsage for the same reason.\n"+
			"    Observed stderr:\n%s", got, out)
	}
}

// TestRejectionKeepsItsExitStatusAndItsSentence guards the other half: R3.6 is
// about the count and nothing else. A fix that silenced a printer and took the
// exit status or the accepted-set naming with it would satisfy the count and
// break R3.2, which is the requirement this one sits beside.
func TestRejectionKeepsItsExitStatusAndItsSentence(t *testing.T) {
	bentoo := buildBentoo(t)

	out, err := exec.Command(bentoo, "version", "--ui=bogus").CombinedOutput()

	var exit *exec.ExitError
	if !errors.As(err, &exit) {
		t.Fatalf("expected a non-zero exit, got err=%v", err)
	}
	if exit.ExitCode() != 1 {
		t.Errorf("exit status is %d, want 1 — R3.2's refusal status is unchanged by R3.6", exit.ExitCode())
	}

	for _, mode := range []string{"auto", "plain", "inline", "fullscreen"} {
		if !strings.Contains(string(out), mode) {
			t.Errorf("the refusal no longer names %q; R3.2 requires the accepted set.\n    Observed: %s", mode, out)
		}
	}
}
