package sysmon_test

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"testing"

	"github.com/obentoo/bentoolkit/internal/sysmon"
)

// Story 098, sub-task 2.1: ParseEmergeLine recognises the four emerge.log
// lines a failed-build check needs, and EmergeFollower reads only what is
// appended after Start (R1.1), survives log rotation by truncation or by
// replacement (R1.10), and bounds each read to MaxRead bytes and each line to
// MaxLine bytes, discarding the rest of an over-long line (R1.12). Every
// fixture is a t.TempDir() file: no test reads the host /var/log (Q18).

// s098AtomRE is the atom shape an event may carry (design, Security).
var s098AtomRE = regexp.MustCompile(`^[A-Za-z0-9+_.-]+/[A-Za-z0-9+_.-]+$`)

func s098Started(atom string) sysmon.EmergeEvent {
	return sysmon.EmergeEvent{Kind: sysmon.EmergeStarted, Atom: atom}
}

func s098Completed(atom string) sysmon.EmergeEvent {
	return sysmon.EmergeEvent{Kind: sysmon.EmergeCompleted, Atom: atom}
}

func s098ExitOK() sysmon.EmergeEvent { return sysmon.EmergeEvent{Kind: sysmon.EmergeExitOK} }

func s098ExitFailed(status int) sysmon.EmergeEvent {
	return sysmon.EmergeEvent{Kind: sysmon.EmergeExitFailed, Status: status}
}

// Lines as portage writes them: "<epoch>:  <message>".
const (
	s098LineStartA   = "1779857047:  >>> emerge (1 of 2) dev-libs/blake3-1.8.5 to /\n"
	s098LineDoneA    = "1779857052:  ::: completed emerge (1 of 2) dev-libs/blake3-1.8.5 to /\n"
	s098LineStartB   = "1779857052:  >>> emerge (2 of 2) dev-libs/libsigc++-2.12.1-r1 to /\n"
	s098LineFailed1  = "1779857388:  *** exiting unsuccessfully with status '1'.\n"
	s098LineOK       = "1779856548:  *** exiting successfully.\n"
	s098LineNoise    = "1779857053:  === (2 of 2) Compiling/Merging (dev-libs/libsigc++-2.12.1-r1::/var/db/repos/gentoo/dev-libs/libsigc++/libsigc++-2.12.1-r1.ebuild)\n"
	s098LineSessionS = "1779857040: Started emerge on: Oct 16, 2026 10:00:00\n"
)

// TestS098EmergeLogParse_RecognisedLines: the four event lines parse to their
// kind, atom and status. Differently written lines that mean the same event
// (one space or a tab after the timestamp, another target root) must parse to
// the same event, and the Started and Completed atoms of one package must be
// the same string so they can be paired.
func TestS098EmergeLogParse_RecognisedLines(t *testing.T) {
	cases := []struct {
		line string
		want sysmon.EmergeEvent
	}{
		{"1779857047:  >>> emerge (1 of 4) dev-libs/blake3-1.8.5 to /", s098Started("dev-libs/blake3-1.8.5")},
		{"1779857047: >>> emerge (1 of 4) dev-libs/blake3-1.8.5 to /", s098Started("dev-libs/blake3-1.8.5")},
		{"1779857047:\t>>> emerge (1 of 4) dev-libs/blake3-1.8.5 to /", s098Started("dev-libs/blake3-1.8.5")},
		{"1779857047:  >>> emerge (12 of 340) dev-libs/blake3-1.8.5 to /mnt/gentoo/", s098Started("dev-libs/blake3-1.8.5")},
		{"1779857052:  ::: completed emerge (1 of 4) dev-libs/blake3-1.8.5 to /", s098Completed("dev-libs/blake3-1.8.5")},
		{"1779857052:  >>> emerge (2 of 4) dev-libs/libsigc++-2.12.1-r1 to /", s098Started("dev-libs/libsigc++-2.12.1-r1")},
		{"1779857052:  ::: completed emerge (2 of 4) dev-libs/libsigc++-2.12.1-r1 to /", s098Completed("dev-libs/libsigc++-2.12.1-r1")},
		{"1779857052:  >>> emerge (1 of 1) dev-python/typing_extensions-4.15.0 to /", s098Started("dev-python/typing_extensions-4.15.0")},
		{"1779856548:  *** exiting successfully.", s098ExitOK()},
		{"1779857388:  *** exiting unsuccessfully with status '1'.", s098ExitFailed(1)},
		{"1779857388:  *** exiting unsuccessfully with status '130'.", s098ExitFailed(130)},
	}
	for _, tc := range cases {
		t.Run(tc.line, func(t *testing.T) {
			got, ok := sysmon.ParseEmergeLine([]byte(tc.line))
			if !ok {
				t.Fatalf("ParseEmergeLine(%q) = not an event, want %+v", tc.line, tc.want)
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("ParseEmergeLine(%q) = %+v, want %+v", tc.line, got, tc.want)
			}
		})
	}
}

// TestS098EmergeLogParse_LookAlikesAreNotEvents is the hostile half: lines
// that resemble an event but are not one must never parse as one. "exiting
// unsuccessfully" contains "successfully" and must never read as success; an
// atom that is not exactly category/package of the allowed characters is
// dropped, so it can never reach a path or a notification.
func TestS098EmergeLogParse_LookAlikesAreNotEvents(t *testing.T) {
	lines := []string{
		"",
		"1779857040: Started emerge on: Oct 16, 2026 10:00:00",
		"1779857040:  *** emerge --ask --verbose @world",
		"1779857053:  === (2 of 2) Compiling/Merging (dev-libs/libsigc++-2.12.1-r1::/var/db/repos/gentoo/dev-libs/libsigc++/libsigc++-2.12.1-r1.ebuild)",
		"1779857060:  >>> AUTOCLEAN: dev-libs/blake3:0",
		"1779857061:  >>> unmerge success: dev-libs/blake3-1.8.4",
		"1779857062:  === Unmerging... (dev-libs/blake3-1.8.4)",
		"1779857063:  *** terminating.",
		"1779857064:  *** exiting",
		"1779857065:  >>> emerge (x of 2) dev-libs/blake3-1.8.5 to /",
		"1779857066:  >>> emerge (1 of 2) blake3-1.8.5 to /",
		"1779857067:  >>> emerge (1 of 2) ../../etc/passwd to /",
		"1779857068:  >>> emerge (1 of 2) dev-libs/blake3-1.8.5/../../x to /",
		"1779857069:  >>> emerge (1 of 2) dev-libs/blake3<b>x</b>-1.8.5 to /",
		"1779857070:  >>> emerge (1 of 2) dev-libs/blake3 bogus to /",
		"1779857071:  >>> emerge (1 of 2) /blake3-1.8.5 to /",
		"1779857072:  ::: completed emerge (1 of 2) dev-libs/ to /",
		"1779857073:  *** exiting unsuccessfully with status 'abc'.",
	}
	for _, line := range lines {
		if ev, ok := sysmon.ParseEmergeLine([]byte(line)); ok {
			t.Errorf("ParseEmergeLine(%q) = %+v, want not an event", line, ev)
		}
	}
	ev, ok := sysmon.ParseEmergeLine([]byte("1779857388:  *** exiting unsuccessfully with status '1'."))
	if ok && ev.Kind == sysmon.EmergeExitOK {
		t.Error("an `exiting unsuccessfully` line parsed as a successful exit")
	}
}

// s098Follow writes initial to a new log file, starts a follower on it with
// the given bounds and returns both. Zero bounds mean the defaults.
func s098Follow(t *testing.T, initial string, maxRead, maxLine int) (string, *sysmon.EmergeFollower) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "emerge.log")
	if err := os.WriteFile(path, []byte(initial), 0o600); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
	f := &sysmon.EmergeFollower{Path: path, MaxRead: maxRead, MaxLine: maxLine}
	if err := f.Start(); err != nil {
		t.Fatalf("Start on %s: %v", path, err)
	}
	return path, f
}

// s098Append appends text to the file at path.
func s098Append(t *testing.T, path, text string) {
	t.Helper()
	fh, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatalf("open %s for append: %v", path, err)
	}
	if _, err := fh.WriteString(text); err != nil {
		t.Fatalf("append to %s: %v", path, err)
	}
	if err := fh.Close(); err != nil {
		t.Fatalf("close %s: %v", path, err)
	}
}

// s098Read calls Read and fails the test on an error.
func s098Read(t *testing.T, f *sysmon.EmergeFollower) []sysmon.EmergeEvent {
	t.Helper()
	ev, err := f.Read()
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	return ev
}

// s098Same compares two event lists, treating nil and empty alike.
func s098Same(t *testing.T, what string, got, want []sysmon.EmergeEvent) {
	t.Helper()
	if len(got) == 0 && len(want) == 0 {
		return
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("%s: events = %+v, want %+v", what, got, want)
	}
}

// TestS098EmergeLogFollow_StartsAtEndOfFile is R1.1: a failed run already in
// the log when the follower starts is never reported; only lines appended
// after Start come back, in order, each exactly once.
func TestS098EmergeLogFollow_StartsAtEndOfFile(t *testing.T) {
	path, f := s098Follow(t, s098LineSessionS+s098LineStartB+s098LineFailed1, 0, 0)
	s098Same(t, "first Read after Start", s098Read(t, f), nil)

	s098Append(t, path, s098LineStartA+s098LineNoise+s098LineDoneA+s098LineOK)
	s098Same(t, "Read after an append", s098Read(t, f),
		[]sysmon.EmergeEvent{s098Started("dev-libs/blake3-1.8.5"), s098Completed("dev-libs/blake3-1.8.5"), s098ExitOK()})
	s098Same(t, "Read with nothing new", s098Read(t, f), nil)

	s098Append(t, path, s098LineFailed1)
	s098Same(t, "Read after a second append", s098Read(t, f), []sysmon.EmergeEvent{s098ExitFailed(1)})
}

// TestS098EmergeLogFollow_PartialLineWaitsForItsNewline: a line emerge has
// half written is parsed once, whole, when its newline arrives — never as a
// truncated atom and never twice.
func TestS098EmergeLogFollow_PartialLineWaitsForItsNewline(t *testing.T) {
	path, f := s098Follow(t, "", 0, 0)
	s098Append(t, path, "1779857047:  >>> emerge (1 of 2) dev-libs/bla")
	s098Same(t, "Read of a half-written line", s098Read(t, f), nil)
	s098Append(t, path, "ke3-1.8.5 to /")
	s098Same(t, "Read of a line still missing its newline", s098Read(t, f), nil)
	s098Append(t, path, "\n"+s098LineFailed1[:20])
	s098Same(t, "Read once the newline arrived", s098Read(t, f),
		[]sysmon.EmergeEvent{s098Started("dev-libs/blake3-1.8.5")})
	s098Append(t, path, s098LineFailed1[20:])
	s098Same(t, "Read of the next completed line", s098Read(t, f), []sysmon.EmergeEvent{s098ExitFailed(1)})
	s098Same(t, "Read with nothing new", s098Read(t, f), nil)
}

// TestS098EmergeLogFollow_CopyTruncateRestartsAtZero is R1.10 for logrotate's
// copytruncate: the same file shrinks below the offset read so far, and the
// follower continues from its new beginning.
func TestS098EmergeLogFollow_CopyTruncateRestartsAtZero(t *testing.T) {
	path, f := s098Follow(t, strings.Repeat(s098LineNoise, 20), 0, 0)
	s098Append(t, path, s098LineStartA+s098LineDoneA)
	s098Same(t, "Read before rotation", s098Read(t, f),
		[]sysmon.EmergeEvent{s098Started("dev-libs/blake3-1.8.5"), s098Completed("dev-libs/blake3-1.8.5")})

	if err := os.Truncate(path, 0); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	s098Append(t, path, s098LineStartB+s098LineFailed1)
	s098Same(t, "Read after copytruncate", s098Read(t, f),
		[]sysmon.EmergeEvent{s098Started("dev-libs/libsigc++-2.12.1-r1"), s098ExitFailed(1)})
}

// TestS098EmergeLogFollow_ReplacedFileRestartsAtZero is R1.10 for a rename
// and recreate. The new file is made larger than the old offset, so a
// follower that checked the size alone would skip the new file's first lines:
// only the changed inode tells. Lines added to the renamed old file are not
// the followed path's any more.
func TestS098EmergeLogFollow_ReplacedFileRestartsAtZero(t *testing.T) {
	path, f := s098Follow(t, s098LineSessionS, 0, 0)
	s098Append(t, path, s098LineStartA)
	s098Same(t, "Read before rotation", s098Read(t, f), []sysmon.EmergeEvent{s098Started("dev-libs/blake3-1.8.5")})

	// The old file stays alive under its new name, so the new one cannot
	// reuse its inode.
	if err := os.Rename(path, path+".1"); err != nil {
		t.Fatalf("rename: %v", err)
	}
	fresh := s098LineStartB + strings.Repeat(s098LineNoise, 10) + s098LineFailed1
	if err := os.WriteFile(path, []byte(fresh), 0o600); err != nil {
		t.Fatalf("recreate: %v", err)
	}
	s098Append(t, path+".1", s098LineDoneA)
	s098Same(t, "Read after rename and recreate", s098Read(t, f),
		[]sysmon.EmergeEvent{s098Started("dev-libs/libsigc++-2.12.1-r1"), s098ExitFailed(1)})
	s098Same(t, "Read with nothing new", s098Read(t, f), nil)
}

// TestS098EmergeLogFollow_ReadIsBoundedByMaxRead is R1.12 for the read bound:
// one Read never consumes more than MaxRead bytes, and the rest arrives on
// later Reads, every event exactly once and in order.
func TestS098EmergeLogFollow_ReadIsBoundedByMaxRead(t *testing.T) {
	// Every line is exactly 64 bytes, so 256 bytes hold at most 4 whole lines.
	var lines []string
	var want []sysmon.EmergeEvent
	for i := range 10 {
		atom := "app-misc/pkg" + string(rune('a'+i)) + "-1.0"
		line := "1779857047:  >>> emerge (1 of 1) " + atom + " to /"
		line += strings.Repeat("x", 63-len(line)) + "\n"
		if len(line) != 64 {
			t.Fatalf("fixture line is %d bytes, want 64", len(line))
		}
		lines = append(lines, line)
		want = append(want, s098Started(atom))
	}
	path, f := s098Follow(t, "", 256, 0)
	s098Append(t, path, strings.Join(lines, ""))

	first := s098Read(t, f)
	if len(first) == 0 || len(first) > 4 {
		t.Fatalf("first Read with MaxRead 256 returned %d events, want 1 to 4 (at most 256 bytes of 64-byte lines)", len(first))
	}
	got := append([]sysmon.EmergeEvent{}, first...)
	for range 10 {
		got = append(got, s098Read(t, f)...)
	}
	s098Same(t, "every event across bounded Reads", got, want)
}

// TestS098EmergeLogFollow_DefaultMaxReadIs4MiB: with MaxRead unset the bound
// is 4 MiB. An event placed after 5 MiB of other lines is not returned by the
// first Read, and is returned, once, by a later one.
func TestS098EmergeLogFollow_DefaultMaxReadIs4MiB(t *testing.T) {
	path, f := s098Follow(t, "", 0, 0)
	filler := strings.Repeat(strings.Repeat("y", 1023)+"\n", 5<<10) // 5 MiB of 1 KiB lines
	s098Append(t, path, s098LineStartA+filler+s098LineStartB)

	first := s098Read(t, f)
	s098Same(t, "first Read (at most 4 MiB)", first, []sysmon.EmergeEvent{s098Started("dev-libs/blake3-1.8.5")})
	var rest []sysmon.EmergeEvent
	for range 4 {
		rest = append(rest, s098Read(t, f)...)
	}
	s098Same(t, "later Reads", rest, []sysmon.EmergeEvent{s098Started("dev-libs/libsigc++-2.12.1-r1")})
}

// TestS098EmergeLogFollow_OverLongLineIsDiscardedWhole is R1.12 for the line
// bound and its hostile half: the tail of an over-long line is shaped like an
// event, and a follower that only cut the first MaxLine bytes would report
// it. The whole line must go, whether it arrives in one Read or over several;
// the converse, a line under the bound that is padded long, still parses.
func TestS098EmergeLogFollow_OverLongLineIsDiscardedWhole(t *testing.T) {
	const maxLine = 128
	evilTail := "1779857047:  >>> emerge (1 of 1) evil/tail-1 to /"
	good := "1779857048:  >>> emerge (1 of 1) good/pkg-1 to /"
	padded := good + strings.Repeat("z", 100-len(good)) // under the bound

	t.Run("one read", func(t *testing.T) {
		path, f := s098Follow(t, "", 0, maxLine)
		s098Append(t, path, strings.Repeat("Q", 300)+evilTail+"\n"+good+"\n"+padded+"\n")
		s098Same(t, "Read past an over-long line", s098Read(t, f),
			[]sysmon.EmergeEvent{s098Started("good/pkg-1"), s098Started("good/pkg-1")})
	})
	t.Run("several reads", func(t *testing.T) {
		path, f := s098Follow(t, "", 0, maxLine)
		s098Append(t, path, strings.Repeat("Q", 100))
		s098Same(t, "Read 1", s098Read(t, f), nil)
		s098Append(t, path, strings.Repeat("Q", 100))
		s098Same(t, "Read 2", s098Read(t, f), nil)
		s098Append(t, path, strings.Repeat("Q", 100))
		s098Same(t, "Read 3", s098Read(t, f), nil)
		s098Append(t, path, evilTail+"\n"+good+"\n")
		s098Same(t, "Read once the over-long line ended", s098Read(t, f), []sysmon.EmergeEvent{s098Started("good/pkg-1")})
	})
	t.Run("bound crossed in the previous read", func(t *testing.T) {
		// The carry passes MaxLine on one Read; the event-shaped tail is all
		// the next Read sees before the newline, and it is still the same line.
		path, f := s098Follow(t, "", 0, maxLine)
		s098Append(t, path, strings.Repeat("Q", 150))
		s098Same(t, "Read 1", s098Read(t, f), nil)
		s098Append(t, path, evilTail+"\n"+good+"\n")
		s098Same(t, "Read of the tail", s098Read(t, f), []sysmon.EmergeEvent{s098Started("good/pkg-1")})
	})
	t.Run("default 64 KiB", func(t *testing.T) {
		path, f := s098Follow(t, "", 0, 0)
		s098Append(t, path, strings.Repeat("Q", 65<<10)+evilTail+"\n"+good+"\n")
		s098Same(t, "Read past a line over 64 KiB", s098Read(t, f), []sysmon.EmergeEvent{s098Started("good/pkg-1")})
	})
}

// TestS098EmergeLogFollow_ErrorsNameThePath is Q4 and the base of R1.11: a log
// that cannot be opened at Start, or that vanishes before a Read, is an error
// naming the path and wrapping the cause; the follower does not log.
func TestS098EmergeLogFollow_ErrorsNameThePath(t *testing.T) {
	t.Run("absent at Start", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "no-such", "emerge.log")
		f := &sysmon.EmergeFollower{Path: path}
		err := f.Start()
		if err == nil {
			t.Fatal("Start on an absent log returned nil")
		}
		if !errors.Is(err, fs.ErrNotExist) || !strings.Contains(err.Error(), path) {
			t.Errorf("Start error %q: want it to wrap fs.ErrNotExist and name %s", err, path)
		}
	})
	t.Run("unreadable at Start", func(t *testing.T) {
		if os.Geteuid() == 0 {
			t.Skip("root reads a mode-000 file")
		}
		path := filepath.Join(t.TempDir(), "emerge.log")
		if err := os.WriteFile(path, []byte(s098LineOK), 0o000); err != nil {
			t.Fatalf("write: %v", err)
		}
		err := (&sysmon.EmergeFollower{Path: path}).Start()
		if err == nil {
			t.Fatal("Start on an unreadable log returned nil")
		}
		if !errors.Is(err, fs.ErrPermission) || !strings.Contains(err.Error(), path) {
			t.Errorf("Start error %q: want it to wrap fs.ErrPermission and name %s", err, path)
		}
	})
	t.Run("removed before Read", func(t *testing.T) {
		// The old content is longer than the new one, so the restart is
		// visible even if the filesystem hands the new file the old inode.
		path, f := s098Follow(t, strings.Repeat(s098LineOK, 3), 0, 0)
		if err := os.Remove(path); err != nil {
			t.Fatalf("remove: %v", err)
		}
		_, err := f.Read()
		if err == nil {
			t.Fatal("Read of a removed log returned nil")
		}
		if !errors.Is(err, fs.ErrNotExist) || !strings.Contains(err.Error(), path) {
			t.Errorf("Read error %q: want it to wrap fs.ErrNotExist and name %s", err, path)
		}
		// The log comes back as a new file: it is read from its beginning.
		if err := os.WriteFile(path, []byte(s098LineFailed1), 0o600); err != nil {
			t.Fatalf("recreate: %v", err)
		}
		s098Same(t, "Read after the log came back", s098Read(t, f), []sysmon.EmergeEvent{s098ExitFailed(1)})
	})
}

// FuzzParseEmergeLine is Q17: no input panics, and an accepted line carries an
// atom of the allowed shape only on Started and Completed events.
func FuzzParseEmergeLine(f *testing.F) {
	for _, seed := range []string{
		s098LineStartA, s098LineDoneA, s098LineStartB, s098LineFailed1, s098LineOK, s098LineNoise, s098LineSessionS,
		"1:  >>> emerge (1 of 1) a/b to /", "1:  *** exiting unsuccessfully with status '99999999999999999999'.",
		"1:  >>> emerge (1 of 1) ../.. to /", "", ":", "1:", "1:  >>> emerge (", "\x00\xff",
	} {
		f.Add([]byte(strings.TrimSuffix(seed, "\n")))
	}
	f.Fuzz(func(t *testing.T, line []byte) {
		ev, ok := sysmon.ParseEmergeLine(line)
		if !ok {
			return
		}
		switch ev.Kind {
		case sysmon.EmergeStarted, sysmon.EmergeCompleted:
			if !s098AtomRE.MatchString(ev.Atom) {
				t.Errorf("ParseEmergeLine(%q) accepted atom %q outside the allowed shape", line, ev.Atom)
			}
		case sysmon.EmergeExitOK, sysmon.EmergeExitFailed:
			if ev.Atom != "" {
				t.Errorf("ParseEmergeLine(%q) gave an exit event an atom %q", line, ev.Atom)
			}
			if ev.Status < 0 {
				t.Errorf("ParseEmergeLine(%q) gave a negative status %d", line, ev.Status)
			}
		default:
			t.Errorf("ParseEmergeLine(%q) returned unknown kind %v", line, ev.Kind)
		}
	})
}
