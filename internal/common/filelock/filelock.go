// Package filelock is an inter-process exclusive lock on one path.
//
// It is the mechanism internal/common/distfiles uses for its per-distfile
// locks, generalised to any path: the lock file is created with O_EXCL, held
// with flock(2) LOCK_EX for as long as the holder lives, and carries the
// holder's PID for the human who finds it. Whether a lock is live is decided by
// the kernel, never by the PID: a lock file whose flock can be taken belongs to
// a process that has died, and is reaped — but only after confirming the path
// still names the same inode, so a reap can never remove a live lock that
// replaced the dead one.
package filelock

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// ErrLocked is returned by Acquire when another live process kept the lock for
// longer than Wait.
var ErrLocked = errors.New("lock is held by another process")

// Wait bounds how long Acquire waits for a live holder, and Poll is the
// interval between attempts. They are variables so tests can shorten them.
var (
	Wait = 2 * time.Minute
	Poll = 25 * time.Millisecond
)

// Lock is a held lock. Release gives it up.
type Lock struct {
	path string
	file *os.File
}

// Acquire takes the exclusive lock at path, waiting up to Wait for a live
// holder and reaping the lock file of a holder that has died. purpose is
// written into the lock file so a person who finds it knows what holds it.
//
// On timeout the error wraps ErrLocked and names path and the holder's PID.
// Any other failure is wrapped naming path and does not wrap ErrLocked.
func Acquire(path, purpose string) (*Lock, error) {
	deadline := time.Now().Add(Wait)
	for {
		// 0644, not 0600: another user sharing the directory must be able to
		// open the lock read-only to take its flock and reap it after a crash.
		// Nothing secret is in it — only the holder's PID and purpose.
		// #nosec G302 -- readable by design; see above
		file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
		switch {
		case err == nil:
			held, err := claim(file, path, purpose)
			if err != nil {
				return nil, fmt.Errorf("taking the lock %s: %w", path, err)
			}
			if held != nil {
				return held, nil
			}
			// Our new file was reaped from under us; race again.
		case errors.Is(err, fs.ErrExist):
			if err := reapIfAbandoned(path); err != nil {
				return nil, fmt.Errorf("inspecting the lock %s: %w", path, err)
			}
			// A reaped lock is retried at once, without waiting a poll.
			if _, statErr := os.Lstat(path); errors.Is(statErr, fs.ErrNotExist) {
				continue
			}
		default:
			return nil, fmt.Errorf("creating the lock file %s: %w", path, err)
		}

		remaining := time.Until(deadline)
		if remaining <= 0 {
			return nil, fmt.Errorf("%w: %s is held by %s and did not become free within %s",
				ErrLocked, path, describeHolder(path), Wait)
		}
		time.Sleep(min(remaining, Poll))
	}
}

// Release gives up the lock: the file is unlinked while its flock is still
// held, and only if the path still names the file this Lock holds, then it is
// closed. It is safe on a nil Lock and safe to call twice.
func (l *Lock) Release() {
	if l == nil || l.file == nil {
		return
	}
	if same, err := pathNamesFile(l.path, l.file); err == nil && same {
		_ = os.Remove(l.path) // best effort: a leftover without a flock is reaped by the next Acquire
	}
	_ = l.file.Close()
	l.file = nil
}

// Path is the lock file's path.
func (l *Lock) Path() string { return l.path }

// claim finishes an acquisition of a file this call just created: flock first,
// payload second, identity check last, so a reap at any point since the create
// is noticed. It returns nil (and no error) when the file stopped being ours.
func claim(file *os.File, path, purpose string) (*Lock, error) {
	if err := flockExclusiveNonBlocking(file); err != nil {
		_ = file.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return nil, nil
		}
		return nil, err
	}
	// The create mode was narrowed by the umask; the lock must stay readable
	// by other users for the reap to work.
	//nolint:gosec // G302: readable by design, see Acquire.
	if err := file.Chmod(0o644); err != nil {
		_ = file.Close()
		return nil, err
	}
	if _, err := file.WriteString(payload(purpose)); err != nil {
		_ = file.Close()
		return nil, err
	}
	same, err := pathNamesFile(path, file)
	if err != nil {
		_ = file.Close()
		return nil, err
	}
	if !same {
		_ = file.Close()
		return nil, nil
	}
	return &Lock{path: path, file: file}, nil
}

// reapIfAbandoned removes the lock file at path when no live process holds its
// flock and the path still names the file that was checked.
func reapIfAbandoned(path string) error {
	file, err := openLockReadOnly(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		return err
	}
	defer func() { _ = file.Close() }()

	if err := flockExclusiveNonBlocking(file); err != nil {
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return nil // a live holder
		}
		return err
	}
	same, err := pathNamesFile(path, file)
	if err != nil || !same {
		return err
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	return nil
}

// openLockReadOnly opens an existing lock file for reading without following a
// symlink and without blocking, and refuses anything that is not a regular
// file. Another user sharing the directory can plant an entry at the lock name:
// a symlink must not redirect the open to its target, and a FIFO or directory
// must neither hang the open nor be reaped as if it were a stale lock.
func openLockReadOnly(path string) (*os.File, error) {
	file, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		if errors.Is(err, syscall.ELOOP) {
			return nil, fmt.Errorf("%s is a symlink; refusing to treat it as a lock: %w", path, err)
		}
		return nil, err
	}
	info, err := file.Stat()
	if err != nil {
		_ = file.Close() // the Stat error is the one reported
		return nil, err
	}
	if !info.Mode().IsRegular() {
		_ = file.Close() // nothing was read; the refusal is the one reported
		return nil, fmt.Errorf("%s is not a regular file (%s); refusing to treat it as a lock", path, info.Mode().Type())
	}
	return file, nil
}

// pathNamesFile reports whether path (not followed if it is a symlink) is the
// open file, by device and inode.
func pathNamesFile(path string, file *os.File) (bool, error) {
	onDisk, err := os.Lstat(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return false, nil
		}
		return false, err
	}
	held, err := file.Stat()
	if err != nil {
		return false, err
	}
	return os.SameFile(onDisk, held), nil
}

// flockExclusiveNonBlocking takes flock(2) LOCK_EX|LOCK_NB. An flock belongs to
// the open file description, so two goroutines that opened the file separately
// contend exactly as two processes do.
func flockExclusiveNonBlocking(file *os.File) error {
	conn, err := file.SyscallConn()
	if err != nil {
		return err
	}
	var flockErr error
	if err := conn.Control(func(fd uintptr) {
		flockErr = syscall.Flock(int(fd), syscall.LOCK_EX|syscall.LOCK_NB)
	}); err != nil {
		return err
	}
	return flockErr
}

// payload is what a holder writes into its lock file, one field per line with
// pid= first so it is easy to grep.
func payload(purpose string) string {
	return fmt.Sprintf("bentoo lock\npid=%d\npurpose=%s\ntaken=%s\n",
		os.Getpid(), purpose, time.Now().UTC().Format(time.RFC3339))
}

// describeHolder names the holder recorded in the lock file for an error
// message; it never fails.
func describeHolder(path string) string {
	if pid, ok := holderPID(path); ok {
		return "pid " + strconv.Itoa(pid)
	}
	return "a process that did not identify itself in " + filepath.Base(path)
}

// holderReadBound caps how much of a lock file holderPID reads: the payload is
// a few short lines, and a planted file must not make the read unbounded.
const holderReadBound = 4096

// holderPID reads the pid= line of a lock file, looking only at the first
// holderReadBound bytes. A line the bound cuts off, with no newline inside
// them, is ignored, so a truncated pid= line never names the wrong process.
func holderPID(path string) (int, bool) {
	file, err := openLockReadOnly(path)
	if err != nil {
		return 0, false
	}
	defer func() { _ = file.Close() }() // read-only; nothing to lose on close
	data, err := io.ReadAll(io.LimitReader(file, holderReadBound))
	if err != nil {
		return 0, false
	}
	for line := range strings.SplitSeq(completeLines(string(data)), "\n") {
		raw, found := strings.CutPrefix(strings.TrimSpace(line), "pid=")
		if !found {
			continue
		}
		pid, err := strconv.Atoi(raw)
		if err != nil || pid <= 0 {
			return 0, false
		}
		return pid, true
	}
	return 0, false
}

// completeLines returns s up to and including its last newline: every line
// that ended inside s, without the unterminated tail.
func completeLines(s string) string {
	return s[:strings.LastIndexByte(s, '\n')+1]
}
