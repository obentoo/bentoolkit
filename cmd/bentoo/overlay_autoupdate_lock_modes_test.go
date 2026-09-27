package main

// Written at run time for story 056, sub-task 5.1 (S056-R4.1), Red first. It
// reuses s056AutoupdateEnv and s056RunCapturingFDs from the pre-authored
// overlay_autoupdate_lock_test.go.

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/obentoo/bentoolkit/internal/common/filelock"
)

// TestAutoupdateOverlayLock_ReadOnlyModesTakeNoLock: R4.1's exemption. While
// this test holds the overlay lock, --list and --lint (without --fix) run to
// their end and never mention the lock, because they only read. The hostile
// half is the same --lint WITH --fix, which rewrites the registry and must be
// refused naming the lock — without it, a build that takes no lock at all
// would pass the read-only half vacuously.
func TestAutoupdateOverlayLock_ReadOnlyModesTakeNoLock(t *testing.T) {
	overlay, _ := s056AutoupdateEnv(t)
	oldWait, oldPoll := filelock.Wait, filelock.Poll
	filelock.Wait, filelock.Poll = 200*time.Millisecond, 10*time.Millisecond
	t.Cleanup(func() { filelock.Wait, filelock.Poll = oldWait, oldPoll })

	lockPath := filepath.Join(overlay, ".autoupdate.bentoo-lock")
	held, err := filelock.Acquire(lockPath, "test holds the overlay")
	if err != nil {
		t.Fatalf("taking the overlay lock for the test: %v", err)
	}
	t.Cleanup(held.Release)

	modes := []struct {
		name      string
		set       func()
		wantsLock bool
	}{
		{"--list", func() { autoupdateCheck, autoupdateList = false, true }, false},
		{"--lint", func() { autoupdateCheck, autoupdateLint, autoupdateFix = false, true, false }, false},
		{"--lint --fix", func() { autoupdateCheck, autoupdateLint, autoupdateFix = false, true, true }, true},
	}
	for _, m := range modes {
		t.Run(m.name, func(t *testing.T) {
			autoupdateCheck, autoupdateList, autoupdateLint, autoupdateFix = true, false, false, false
			m.set()

			code, out := s056RunCapturingFDs(t)
			mentionsLock := strings.Contains(out, lockPath)
			if m.wantsLock {
				if code != 1 || !mentionsLock {
					t.Errorf("%s ran while the overlay lock was held (exit %d); want exit 1 naming %s:\n%s", m.name, code, lockPath, out)
				}
				return
			}
			if code == 1 || mentionsLock {
				t.Errorf("%s is read-only and must take no overlay lock, but it exited %d and/or named %s:\n%s", m.name, code, lockPath, out)
			}
		})
	}
}
