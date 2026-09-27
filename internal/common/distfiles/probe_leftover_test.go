package distfiles

// Authored for story 056, sub-task 6.1 (S056-R7.2) — a GUARD, green before and
// after the fix: the refusal of a symlink must not become a refusal of the
// regular leftover a dead run with a recycled PID can leave, which is the
// documented reason the open has no O_EXCL.

import (
	"os"
	"testing"
)

func TestProbeOverwritesLeftoverRegularFile(t *testing.T) {
	dir := t.TempDir()
	path := nextProbePath(dir)
	if err := os.WriteFile(path, []byte("left by a dead run"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := Probe(dir); err != nil {
		t.Fatalf("Probe over a regular leftover: %v", err)
	}
	if _, err := os.Lstat(path); err == nil {
		t.Errorf("Probe left %s behind", path)
	}
}
