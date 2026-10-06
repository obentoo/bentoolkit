package main

// Story 062, sub-task 4.3: func execute logs a failWith cause on the
// invocation's logger, so the line reaches stderr AND the log file (story 060,
// R7.1, R7.2), and then closes the log file itself, because cobra skips the
// root's PersistentPostRunE on an error return. Run in-process: execute never
// ends the process, so no re-exec child is needed.

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

const s062FailWithProbe = "s062 failwith probe cause"

// s062ExitCleanupCount is the number of cleanups exitProcess would run now.
func s062ExitCleanupCount() int {
	exitCleanupsMu.Lock()
	defer exitCleanupsMu.Unlock()
	return len(exitCleanups)
}

func TestS062FailWithCauseReachesTheLogFileAndExecuteClosesIt(t *testing.T) {
	home := t.TempDir()
	state := filepath.Join(home, ".local", "state")
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_STATE_HOME", state)
	t.Setenv(logLevelEnv, "")

	extra := &cobra.Command{
		Use: "s062failwith",
		RunE: func(*cobra.Command, []string) error {
			return failWith(1, errors.New(s062FailWithProbe))
		},
	}

	before := s062ExitCleanupCount()
	_, stderr, code := s062Execute(t, extra, "s062failwith")

	if code != 1 {
		t.Errorf("exit code = %d, want 1", code)
	}
	if lines := s062LinesWith(stderr, s062FailWithProbe); len(lines) != 1 ||
		!strings.Contains(lines[0], `level=ERROR msg="command failed" err="`+s062FailWithProbe+`"`) {
		t.Errorf("stderr lines carrying the cause = %q, want exactly one ERROR record\nstderr:\n%s", lines, stderr)
	}

	data, err := os.ReadFile(filepath.Join(state, "bentoo", "logs", "bentoo.log"))
	if err != nil {
		t.Fatalf("read log file: %v", err)
	}
	n := 0
	for _, l := range strings.Split(string(data), "\n") {
		var rec struct{ Level, Msg, Err string }
		if json.Unmarshal([]byte(l), &rec) == nil && rec.Level == "ERROR" && rec.Msg == "command failed" && rec.Err == s062FailWithProbe {
			n++
		}
	}
	if n != 1 {
		t.Errorf("the log file holds %d record(s) of the cause, want exactly 1\nlog:\n%s", n, data)
	}

	// Closing the file through its close func also cancels its exit cleanup,
	// so a failed in-process run leaves the registry as it found it.
	if after := s062ExitCleanupCount(); after != before {
		t.Errorf("exit cleanups = %d after the failed run, want %d: execute left the log file open", after, before)
	}
}
