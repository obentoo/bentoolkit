package sysmon

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"regexp"
	"strconv"
	"syscall"
)

// EmergeEventKind is what one emerge.log line reports.
type EmergeEventKind int

// The emerge.log events a failed-build check needs.
const (
	EmergeStarted    EmergeEventKind = iota + 1 // ">>> emerge (i of n) <atom> to <root>"
	EmergeCompleted                             // "::: completed emerge (i of n) <atom> to <root>"
	EmergeExitOK                                // "*** exiting successfully."
	EmergeExitFailed                            // "*** exiting unsuccessfully with status '<n>'."
)

// EmergeEvent is one recognised emerge.log line. Atom is set for Started and
// Completed ("category/package-version"), Status for ExitFailed.
type EmergeEvent struct {
	Kind   EmergeEventKind
	Atom   string
	Status int
}

// Default bounds of an EmergeFollower read.
const (
	DefaultEmergeMaxRead = 4 << 20
	DefaultEmergeMaxLine = 64 << 10
)

var (
	emergePrefixRE    = regexp.MustCompile(`^[0-9]+:[ \t]+`)
	emergeStartedRE   = regexp.MustCompile(`^>>> emerge \([0-9]+ of [0-9]+\) (\S+) to \S`)
	emergeCompletedRE = regexp.MustCompile(`^::: completed emerge \([0-9]+ of [0-9]+\) (\S+) to \S`)
	emergeExitFailRE  = regexp.MustCompile(`^\*\*\* exiting unsuccessfully with status '([0-9]+)'\.$`)
	// An atom is exactly category/package-version; no component may start
	// with a dot, so a path such as ../.. is never accepted.
	emergeAtomRE = regexp.MustCompile(`^[A-Za-z0-9+_][A-Za-z0-9+_.-]*/[A-Za-z0-9+_][A-Za-z0-9+_.-]*$`)
)

const (
	emergeExitOKText       = "*** exiting successfully."
	emergeTerminatingText  = "*** terminating."
	emergeRunStartText     = "Started emerge on:"
	emergeInterruptedState = 130
)

// ParseEmergeLine reads one emerge.log line, without its newline. ok is false
// for every line that is not one of the four events, including an event line
// whose atom is not a plain category/package-version.
func ParseEmergeLine(line []byte) (EmergeEvent, bool) {
	loc := emergePrefixRE.FindIndex(line)
	if loc == nil {
		return EmergeEvent{}, false
	}
	msg := bytes.TrimRight(line[loc[1]:], " \t\r")
	if string(msg) == emergeExitOKText {
		return EmergeEvent{Kind: EmergeExitOK}, true
	}
	if m := emergeExitFailRE.FindSubmatch(msg); m != nil {
		status, err := strconv.Atoi(string(m[1]))
		if err != nil {
			return EmergeEvent{}, false
		}
		return EmergeEvent{Kind: EmergeExitFailed, Status: status}, true
	}
	for _, p := range []struct {
		re   *regexp.Regexp
		kind EmergeEventKind
	}{{emergeStartedRE, EmergeStarted}, {emergeCompletedRE, EmergeCompleted}} {
		if m := p.re.FindSubmatch(msg); m != nil {
			if !emergeAtomRE.Match(m[1]) {
				return EmergeEvent{}, false
			}
			return EmergeEvent{Kind: p.kind, Atom: string(m[1])}, true
		}
	}
	return EmergeEvent{}, false
}

// EmergeFollower reads the lines appended to an emerge log since Start. It
// survives rotation by truncation (the size drops below the offset) and by
// replacement (the inode changes): either restarts at the new beginning.
// MaxRead bounds the bytes one Read consumes and MaxLine the length of a line;
// a longer line is discarded whole. Zero bounds take the defaults. It returns
// errors and never logs; it is not safe for concurrent use.
type EmergeFollower struct {
	Path    string
	MaxRead int
	MaxLine int

	started  bool
	inode    uint64
	offset   int64
	carry    []byte // the bytes after the last newline read
	skipping bool   // inside an over-long line, discarding up to its newline
	exited   bool   // an exit line was read since the last "terminating"
	building bool   // a package started since the last "Started emerge on:"
}

// Start positions the follower at the current end of the log, so only lines
// appended afterwards are read.
func (f *EmergeFollower) Start() error {
	fh, err := os.Open(f.Path)
	if err != nil {
		return fmt.Errorf("opening emerge log %s: %w", f.Path, err)
	}
	defer fh.Close() //nolint:errcheck // read-only file
	fi, err := fh.Stat()
	if err != nil {
		return fmt.Errorf("reading emerge log %s: %w", f.Path, err)
	}
	f.inode = inodeOf(fi)
	f.offset = fi.Size()
	f.carry, f.skipping = nil, false
	f.exited, f.building = false, false
	f.started = true
	return nil
}

// Read returns the events of the complete lines appended since the last
// Read, in order. A follower whose Start failed starts now, at the end of the
// log, and returns no event, so lines written before the log became readable
// are never reported. A log that cannot be read is an error naming the path;
// when it comes back as a new file it is read from its beginning.
func (f *EmergeFollower) Read() ([]EmergeEvent, error) {
	if !f.started {
		return nil, f.Start()
	}
	fh, err := os.Open(f.Path)
	if err != nil {
		f.inode = 0 // a log that comes back is a new file
		return nil, fmt.Errorf("opening emerge log %s: %w", f.Path, err)
	}
	defer fh.Close() //nolint:errcheck // read-only file
	fi, err := fh.Stat()
	if err != nil {
		return nil, fmt.Errorf("reading emerge log %s: %w", f.Path, err)
	}
	if ino := inodeOf(fi); ino != f.inode || fi.Size() < f.offset {
		f.inode, f.offset = ino, 0
		f.carry, f.skipping = nil, false
		f.exited, f.building = false, false
	}
	n := min(fi.Size()-f.offset, int64(f.maxRead()))
	if n <= 0 {
		return nil, nil
	}
	chunk := make([]byte, n)
	got, err := fh.ReadAt(chunk, f.offset)
	if err != nil && !errors.Is(err, io.EOF) {
		return nil, fmt.Errorf("reading emerge log %s: %w", f.Path, err)
	}
	f.offset += int64(got)
	return f.consume(chunk[:got]), nil
}

// consume splits chunk into lines, carrying an unfinished one to the next
// Read and discarding every line longer than MaxLine.
func (f *EmergeFollower) consume(chunk []byte) []EmergeEvent {
	var events []EmergeEvent
	for len(chunk) > 0 {
		i := bytes.IndexByte(chunk, '\n')
		if i < 0 {
			f.hold(chunk)
			break
		}
		line := chunk[:i]
		chunk = chunk[i+1:]
		if f.skipping {
			f.skipping = false
			continue
		}
		if len(f.carry)+len(line) > f.maxLine() {
			f.carry = nil
			continue
		}
		if len(f.carry) > 0 {
			line = append(f.carry, line...)
			f.carry = nil
		}
		events = f.track(line, events)
	}
	return events
}

// hold keeps an unfinished line for the next Read, or starts discarding it
// once it is longer than MaxLine.
func (f *EmergeFollower) hold(part []byte) {
	if f.skipping {
		return
	}
	if len(f.carry)+len(part) > f.maxLine() {
		f.carry, f.skipping = nil, true
		return
	}
	f.carry = append(f.carry, part...)
}

// track appends the event of one complete line to events. A run stopped by a
// signal skips its exit line and only writes "terminating"; when it had
// started a package, that terminating is reported as an interrupted exit, so
// the packages it left started are forgotten instead of blamed on a later
// failure. --sync, --config, --deselect and --depclean runs also end with a
// bare terminating and start no package; emerge.log carries no process id, so
// when such a run's terminating lands after another run started a package,
// that build is forgotten too and a later failure of it raises nothing.
func (f *EmergeFollower) track(line []byte, events []EmergeEvent) []EmergeEvent {
	if ev, ok := ParseEmergeLine(line); ok {
		switch ev.Kind {
		case EmergeStarted:
			f.building = true
		case EmergeExitOK, EmergeExitFailed:
			f.exited = true
		case EmergeCompleted:
		}
		return append(events, ev)
	}
	switch emergeMessage(line) {
	case emergeRunStartText:
		f.building = false
	case emergeTerminatingText:
		if !f.exited && f.building {
			events = append(events, EmergeEvent{Kind: EmergeExitFailed, Status: emergeInterruptedState})
		}
		f.exited, f.building = false, false
	}
	return events
}

// emergeMessage is the message of an emerge.log line without its timestamp,
// cut to the run-start prefix when it is a "Started emerge on:" line; "" for a
// line with no timestamp.
func emergeMessage(line []byte) string {
	loc := emergePrefixRE.FindIndex(line)
	if loc == nil {
		return ""
	}
	msg := string(bytes.TrimRight(line[loc[1]:], " \t\r"))
	if len(msg) >= len(emergeRunStartText) && msg[:len(emergeRunStartText)] == emergeRunStartText {
		return emergeRunStartText
	}
	return msg
}

func (f *EmergeFollower) maxRead() int {
	if f.MaxRead > 0 {
		return f.MaxRead
	}
	return DefaultEmergeMaxRead
}

func (f *EmergeFollower) maxLine() int {
	if f.MaxLine > 0 {
		return f.MaxLine
	}
	return DefaultEmergeMaxLine
}

// inodeOf is the inode of fi, or 0 when the platform does not report one.
func inodeOf(fi os.FileInfo) uint64 {
	if st, ok := fi.Sys().(*syscall.Stat_t); ok {
		return st.Ino
	}
	return 0
}
