package main

import (
	"os"
	"path/filepath"
	"testing"
)

// TestUINotesWriteBareLinesUnlessQuiet pins the output boundary of ui_notes.go:
// the line reaches the stderr of the moment as bare text, and --quiet (the
// package variable the root publishes) silences it.
func TestUINotesWriteBareLinesUnlessQuiet(t *testing.T) {
	origQuiet, origStderr := quiet, os.Stderr
	t.Cleanup(func() { quiet, os.Stderr = origQuiet, origStderr })

	capture := func(q bool) string {
		t.Helper()
		f, err := os.Create(filepath.Join(t.TempDir(), "stderr"))
		if err != nil {
			t.Fatalf("creating the capture file: %v", err)
		}
		defer func() { _ = f.Close() }()
		quiet, os.Stderr = q, f
		uiInfo("Aborted.")
		uiWarn("Path does not exist: /x")
		os.Stderr = origStderr
		b, err := os.ReadFile(f.Name())
		if err != nil {
			t.Fatalf("reading the capture file: %v", err)
		}
		return string(b)
	}

	if got, want := capture(false), "Aborted.\nPath does not exist: /x\n"; got != want {
		t.Errorf("stderr = %q, want %q (bare lines, no level or msg= prefix)", got, want)
	}
	if got := capture(true); got != "" {
		t.Errorf("stderr under quiet = %q, want nothing", got)
	}
}
