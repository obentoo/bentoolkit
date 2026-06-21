package main

import "testing"

// R2.1/R2.2 (T4.2): the --no-tui flag is registered on the autoupdate command.
func TestNoTUIFlagRegistered(t *testing.T) {
	if autoupdateCmd.Flags().Lookup("no-tui") == nil {
		t.Fatal("--no-tui flag should be registered on the autoupdate command")
	}
}

// R2.2 (T4.1/T4.2): the apply gate honors --no-tui — it forces plain mode
// regardless of the terminal.
func TestApplyGateRespectsNoTUIFlag(t *testing.T) {
	old := autoupdateNoTUI
	t.Cleanup(func() { autoupdateNoTUI = old })

	autoupdateNoTUI = true
	if tuiEnabledForApply() {
		t.Error("--no-tui must force plain mode (apply gate should be false)")
	}
}

// R2.2 (T4.1): the go test harness is not a TTY, so the gate selects plain mode
// (no Bubble Tea program, hence no ANSI control sequences).
func TestApplyGateNonTTYIsPlain(t *testing.T) {
	old := autoupdateNoTUI
	t.Cleanup(func() { autoupdateNoTUI = old })

	autoupdateNoTUI = false
	if tuiEnabledForApply() {
		t.Error("a non-TTY environment must select plain mode")
	}
}
