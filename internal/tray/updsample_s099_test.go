package tray

// Story 099, sub-task 5.3, Executor-authored: start-up scheduling and the
// helpers of the update check that no App-level test can see.

import (
	"errors"
	"fmt"
	"io/fs"
	"os/exec"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/obentoo/bentoolkit/internal/common/config"
	"github.com/obentoo/bentoolkit/internal/gentoo/pending"
)

type s099FixedClock struct{ SystemClock }

func (s099FixedClock) Now() time.Time { return time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC) }

func s099Mon(updates, glsa bool) *config.Monitors {
	m, _ := config.TrayConfig{}.GetMonitors()
	m.Updates.Enabled, m.GLSA.Enabled = updates, glsa
	return &m
}

func TestS099World_X_StartDelayOnlyWhenACheckCanRun(t *testing.T) {
	cmd := &pending.Command{Path: "/s099-no-such-root/emerge"}
	start := s099FixedClock{}.Now()
	for _, tc := range []struct {
		name          string
		updates, glsa bool
		world, adv    *pending.Command
		want          time.Time
	}{
		{"both disabled", false, false, cmd, cmd, time.Time{}},
		{"both enabled, no command found", true, true, nil, nil, time.Time{}},
		{"updates only", true, false, cmd, nil, start.Add(5 * time.Minute)},
		{"glsa only", false, true, nil, cmd, start.Add(5 * time.Minute)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a := New(Deps{Clock: s099FixedClock{}, Monitors: s099Mon(tc.updates, tc.glsa), Updates: tc.world, GLSA: tc.adv})
			if !a.sched.startAt.Equal(tc.want) {
				t.Errorf("startAt = %v, want %v", a.sched.startAt, tc.want)
			}
			if a.d.Run == nil || a.d.SyncTime == nil {
				t.Error("New left the Run or SyncTime seam nil")
			}
		})
	}
}

func TestS099World_X_StderrTailIsTheLastBytesAsValidUTF8(t *testing.T) {
	long := strings.Repeat("a", 300) + "é" + strings.Repeat("z", 254) + "\n"
	got := stderrTail([]byte(long))
	if len(got) > stderrTailBytes || !utf8.ValidString(got) {
		t.Errorf("tail of %d bytes valid=%v, want at most %d valid bytes", len(got), utf8.ValidString(got), stderrTailBytes)
	}
	if !strings.HasSuffix(got, "zzz") {
		t.Errorf("tail %q does not end with the last bytes", got)
	}
	if stderrTail(nil) != "" {
		t.Error("empty stderr gave a tail")
	}
}

func TestS099World_X_CannotExecuteCauses(t *testing.T) {
	for _, tc := range []struct {
		err  error
		want bool
	}{
		{fmt.Errorf("starting emerge: %w", exec.ErrNotFound), true},
		{fmt.Errorf("starting /usr/bin/emerge: %w", fs.ErrNotExist), true},
		{fmt.Errorf("starting /usr/bin/emerge: %w", fs.ErrPermission), true},
		{fmt.Errorf("emerge: %w", pending.ErrTimeout), false},
		{errors.New("emerge: broken pipe"), false},
	} {
		if got := cannotExecute(tc.err); got != tc.want {
			t.Errorf("cannotExecute(%v) = %v, want %v", tc.err, got, tc.want)
		}
	}
}
