package distfiles

// Authored for story 056, sub-task 6.1 (S056-R7.1). The probe name is
// .bentoo-distdir-probe-<pid>-<seq>, with seq the package's counter; the test
// plants a symlink at the NEXT name so Probe meets it on its own open.

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

func nextProbePath(dir string) string {
	return filepath.Join(dir, fmt.Sprintf(".bentoo-distdir-probe-%d-%d", os.Getpid(), probeSeq.Load()+1))
}

func TestProbeRefusesSymlinkAtProbeName(t *testing.T) {
	dir := t.TempDir()
	victim := filepath.Join(t.TempDir(), "victim")
	if err := os.WriteFile(victim, []byte("do not truncate me"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(victim, nextProbePath(dir)); err != nil {
		t.Fatal(err)
	}

	err := Probe(dir)
	if !errors.Is(err, ErrDistdirNotWritable) {
		t.Errorf("Probe with a symlink at its probe name returned %v, want ErrDistdirNotWritable", err)
	}
	if got, _ := os.ReadFile(victim); string(got) != "do not truncate me" {
		t.Errorf("Probe followed the symlink: its target now holds %q", got)
	}
}
