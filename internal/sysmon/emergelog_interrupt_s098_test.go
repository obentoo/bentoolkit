package sysmon_test

import (
	"testing"

	"github.com/obentoo/bentoolkit/internal/sysmon"
)

// A run stopped by a signal skips its "*** exiting ..." line and only writes
// "*** terminating.": the follower reports it as an interrupted exit (status
// 130), so the packages it left started are never blamed on a later failure.
// A run that wrote its exit line first is not reported twice.

const s098LineTerminating = "1779857400:  *** terminating.\n"

func TestS098EmergeLogFollow_TerminatingWithoutExitIsInterrupted(t *testing.T) {
	path, f := s098Follow(t, "", 0, 0)
	s098Append(t, path, s098LineStartA+s098LineTerminating)
	s098Same(t, "terminating with no exit line", s098Read(t, f),
		[]sysmon.EmergeEvent{s098Started("dev-libs/blake3-1.8.5"), s098ExitFailed(130)})
}

func TestS098EmergeLogFollow_TerminatingAfterExitAddsNothing(t *testing.T) {
	path, f := s098Follow(t, "", 0, 0)
	s098Append(t, path, s098LineStartB+s098LineFailed1+s098LineTerminating)
	s098Same(t, "terminating after a failed exit", s098Read(t, f),
		[]sysmon.EmergeEvent{s098Started("dev-libs/libsigc++-2.12.1-r1"), s098ExitFailed(1)})
	s098Append(t, path, s098LineOK+s098LineTerminating)
	s098Same(t, "terminating after a successful exit", s098Read(t, f), []sysmon.EmergeEvent{s098ExitOK()})
	// The exit seen flag is per run: the next run's bare terminating counts.
	s098Append(t, path, s098LineStartA+s098LineTerminating)
	s098Same(t, "the next run interrupted", s098Read(t, f),
		[]sysmon.EmergeEvent{s098Started("dev-libs/blake3-1.8.5"), s098ExitFailed(130)})
}

func TestS098EmergeLogParse_TerminatingIsNotAnEvent(t *testing.T) {
	if ev, ok := sysmon.ParseEmergeLine([]byte("1779857400:  *** terminating.")); ok {
		t.Errorf("ParseEmergeLine(terminating) = %+v, want not an event: only the follower knows whether an exit line preceded it", ev)
	}
}

// A --sync (or --config, --deselect, --depclean) run started beside a build
// ends with a bare terminating too, but it starts no package: the build's
// later failure must still be reported.
func TestS098EmergeLogFollow_SyncBesideABuildForgetsNothing(t *testing.T) {
	path, f := s098Follow(t, "", 0, 0)
	s098Append(t, path, s098LineSessionS+
		"1779857041:  >>> emerge (1 of 1) llvm-core/clang-22.1.6 to /\n"+
		"1779857100: Started emerge on: Oct 16, 2026 10:01:40\n"+
		"1779857100:  *** emerge --sync\n"+
		"1779857101:  === Sync completed for gentoo\n"+
		s098LineTerminating+
		s098LineFailed1+s098LineTerminating)
	s098Same(t, "a sync terminating beside a build", s098Read(t, f),
		[]sysmon.EmergeEvent{s098Started("llvm-core/clang-22.1.6"), s098ExitFailed(1)})
}

// A run that writes "Started emerge on:" and then a bare terminating without
// starting any package is not an interrupted build either.
func TestS098EmergeLogFollow_BareTerminatingWithNothingStartedIsSilent(t *testing.T) {
	path, f := s098Follow(t, "", 0, 0)
	s098Append(t, path, s098LineSessionS+"1779857040:  *** emerge --deselect dev-libs/foo\n"+s098LineTerminating)
	s098Same(t, "a deselect run", s098Read(t, f), nil)
}
