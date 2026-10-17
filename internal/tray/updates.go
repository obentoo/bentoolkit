package tray

import "time"

// checkScheduler decides when an update check runs. Triggers (a repository
// sync, a successful emerge, the start-up delay) make one check pending; the
// App starts it at a sample where no emerge runs and no other check is in
// flight. Triggers that arrive while a check runs collapse into exactly one
// more check after it. It only decides: the App runs the check, calling
// started when it begins and finished when its result arrives.
type checkScheduler struct {
	pending  bool      // a trigger is waiting for a check to start
	running  bool      // a check is in flight
	again    bool      // a trigger arrived while the running check ran
	startAt  time.Time // when the start-up check becomes due; zero once fired or when none is wanted
	revSeen  bool      // revisions has sampled once, whether repo_revisions existed or not
	revStamp time.Time // the last repo_revisions mtime seen; zero while the file was never present
}

// trigger asks for one check: pending when idle, or one more after the check
// that is running now.
func (s *checkScheduler) trigger() {
	if s.running {
		s.again = true
		return
	}
	s.pending = true
}

// due reports whether a check should start at now. The start-up delay fires
// once, as a trigger, the first time now reaches startAt. A running emerge
// holds a pending check until a sample sees none.
func (s *checkScheduler) due(now time.Time, emerge bool) bool {
	if !s.startAt.IsZero() && !now.Before(s.startAt) {
		s.startAt = time.Time{}
		s.trigger()
	}
	return s.pending && !s.running && !emerge
}

// waiting reports, without changing anything, whether a check would start at
// now if no emerge ran: one is pending or the start-up delay has elapsed, and
// none is running. The App scans for a running emerge only then.
func (s *checkScheduler) waiting(now time.Time) bool {
	if s.running {
		return false
	}
	return s.pending || (!s.startAt.IsZero() && !now.Before(s.startAt))
}

// started records that the pending check began.
func (s *checkScheduler) started() {
	s.pending = false
	s.running = true
}

// finished records that the running check ended; a trigger that arrived
// during it makes one more check pending.
func (s *checkScheduler) finished() {
	s.running = false
	if s.again {
		s.again = false
		s.pending = true
	}
}

// revisions reports whether repo_revisions changed since the last sample:
// true for any mtime different from the one last seen, including a file
// created after the first sample found it absent (the first sync ever). The
// first sample is recorded silently, present or absent. A file that goes
// absent never reports and keeps the mtime last seen, so it reports again
// only when it reappears with a different one.
func (s *checkScheduler) revisions(mtime time.Time, ok bool) bool {
	if !s.revSeen {
		s.revSeen = true
		if ok {
			s.revStamp = mtime
		}
		return false
	}
	if !ok || mtime.Equal(s.revStamp) {
		return false
	}
	s.revStamp = mtime
	return true
}
