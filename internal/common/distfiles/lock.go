package distfiles

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// ErrDistfileLocked reports that another writer holds a distfile this run needs
// and did not let go within the bounded wait. Callers test it with errors.Is;
// which distfile, which directory and which process holds it travel in the
// wrapped message, because those are what an operator acts on.
//
// It is a distinct sentinel from ErrDistdirNotWritable on purpose. "Somebody
// else is downloading this right now" is a transient state that resolves by
// itself and whose right answer is to fail this package and let the next run
// have it; "this directory cannot be written to" is an environment fault that
// will still be there next time. The failure classifier must be able to tell
// them apart without reading either message.
var ErrDistfileLocked = errors.New("distfile is locked by another writer")

// The lock file sits IN the distdir, next to the distfile it guards, and is
// named "." + <distfile> + ".bentoo_lockfile".
//
// The distdir is the only directory every writer is guaranteed to share: two
// runs may differ in TMPDIR, home, mount namespace and user and still share one
// DISTDIR (the `portageq distdir` default), so a lock kept anywhere else would
// let exactly those runs miss each other. It is also the directory Probe has
// already proved writable. Creating a file there changes no mode and no owner;
// the cost is a name in a directory shared with the host package manager, so
// the name is a dotfile, says whose it is, and is removed on release.
//
// The shape mirrors portage's "." + <distfile> + ".portage_lockfile"
// (portage/locks.py:251), so an operator recognises it and the one differing
// word names the tool to blame. It cannot collide with a real distfile (never a
// leading dot), with Probe's .bentoo-distdir-probe-* or with Quarantine's
// <name>._bentoo_quarantine_.*.
const (
	lockFilePrefix = "."
	lockFileSuffix = ".bentoo_lockfile"
)

// lockWait bounds how long acquiring the locks for ONE package may take, from
// the first attempt to the last. It is a whole-call budget rather than a
// per-distfile one, so a package expecting eight distfiles cannot wait eight
// times as long as a package expecting one.
//
// Two minutes is chosen against what the lock is held for: the entire pkgdev
// invocation, download included. A waiter is therefore usually waiting on a real
// download, and a bound short enough to be useful has to be willing to give up
// on a slow one. Giving up is the specified outcome and the cheap one — the
// package is reported as failed and the next run finds the distfile already
// fetched and verified, and reuses it. Waiting is the expensive one: the
// sweep runs under a semaphore, so a waiter is holding a worker slot the whole
// time it waits (autoupdate/sweep.go, `func ExecuteOverlaySweep`).
//
// Tests shorten it, the way they shorten portageqTimeout, so the timeout branch
// can be exercised without a slow test.
var lockWait = 2 * time.Minute

// lockPoll is how long a contended attempt waits before trying again. There is
// nothing to wake a waiter up — the holder is a different process, and the
// release it is waiting for is an unlink — so polling is the mechanism, and the
// interval trades responsiveness against how often a whole sweep's worth of
// waiters touch the same directory. The one exception is the caller's context:
// a cancel ends the wait at once, not at the next poll (see acquireLock).
var lockPoll = 25 * time.Millisecond

// FetchLock is the exclusive claim one manifest step holds over the distfile
// names it is about to fetch, for the whole of its pkgdev invocation, so that
// concurrent writers never fetch onto the same path.
//
// It also underpins FetchScope's ownership rule ("absent before the fetch,
// therefore ours now"), which only holds if nothing else writes those names
// during the download between RecordFetchScope's Lstat and CleanupFailedFetch's
// Remove; without the lock our failure branch could delete another worker's
// download. Quarantine has the same Lstat-to-Rename gap. Hence the order:
//
//	resolve -> LOCK -> Quarantine -> prepopulate -> record -> pkgdev -> on failure: cleanup -> RELEASE
//
// Release is safe to defer right after a successful LockFetch and safe to call
// more than once.
//
// It does NOT cover the host's package manager: portage serializes fetches
// under FEATURES=distlocks, but pkgdev does not take part, so a sweep and an
// emerge wanting the same distfile are not serialized by anything. That is a
// documented limitation; a private handshake with portage would be worse.
type FetchLock struct {
	// distdir is kept with the locks so a diagnostic can name the directory
	// the contention is in, and so the two halves cannot be given different
	// directories — the same reason FetchScope keeps it.
	distdir string
	// held are the per-distfile locks this value owns, in acquisition order.
	// Unexported: nothing outside this file may add to it or release one of
	// them individually.
	held []*heldLock
}

// heldLock is one acquired lock file. The open file is kept for the lifetime of
// the claim and never handed out: closing it is what tells the kernel this
// holder is gone, so an *os.File that escaped could end a claim that is still
// being relied on.
type heldLock struct {
	name string
	path string
	file *os.File
}

// LockFetch takes an exclusive lock on every distfile name in names and returns
// them as one claim, or an error having released whatever it took: holding
// three of four locks leaves the fourth unprotected.
//
// Each name is reduced with distfileName and dropped if it is not a filename,
// which keeps the lock file inside the shared distdir despite untrusted input.
// Duplicates collapse (locking a name twice would self-deadlock), and names are
// locked in sorted order so two workers sharing distfiles cannot wait on each
// other in a cycle. An empty distdir is refused, as in Probe: filepath.Join("",
// name) would lock in the working directory and separate nobody.
//
// ctx ends the wait as lockWait does, whichever comes first, because a waiter
// holds a sweep worker slot. A ctx already done is refused before any lock
// file is created; one that ends during contention ends the wait at once and
// releases the locks taken so far. Both errors wrap ctx.Err(), not
// ErrDistfileLocked: being cancelled is not contention.
//
// A caller that gets an error MUST NOT go on to fetch; it reports the package
// as failed.
func LockFetch(ctx context.Context, distdir string, names []string) (*FetchLock, error) {
	if distdir == "" {
		return nil, errors.New("cannot lock distfiles: no distdir was resolved")
	}
	// Checked before any lock file exists, so a run cancelled before its
	// manifest step began leaves nothing behind in a directory it shares with
	// the host's package manager.
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("claiming distfiles in %s: %w", distdir, err)
	}

	wanted := make([]string, 0, len(names))
	seen := make(map[string]struct{}, len(names))
	for _, raw := range names {
		name, ok := distfileName(raw)
		if !ok {
			continue
		}
		if _, dup := seen[name]; dup {
			continue
		}
		seen[name] = struct{}{}
		wanted = append(wanted, name)
	}
	slices.Sort(wanted)

	lock := &FetchLock{distdir: distdir}
	deadline := time.Now().Add(lockWait)
	for _, name := range wanted {
		held, err := acquireLock(ctx, distdir, name, deadline)
		if err != nil {
			lock.Release()
			return nil, err
		}
		lock.held = append(lock.held, held)
	}
	return lock, nil
}

// Release gives up every lock in the claim. It is safe on nil, safe on a claim
// that holds nothing, and safe to call twice, so `defer lock.Release()` can be
// written straight after LockFetch returns.
//
// Each lock file is unlinked while its flock is still held and only after the
// file at the path has been confirmed to be the one this claim created, so a
// release can never take away a lock that has since become somebody else's.
//
// Nothing is reported. A failed unlink is not something the caller can act on,
// and it is not something anyone has to act on either: the file left behind
// belongs to a process that is about to stop existing, and the kernel drops its
// flock when it does, which is exactly the state the next acquirer reaps. The
// mechanism that survives a SIGKILL survives a failed unlink for free.
func (l *FetchLock) Release() {
	if l == nil {
		return
	}
	for _, held := range l.held {
		held.release()
	}
	l.held = nil
}

// acquireLock takes one distfile's lock, waiting until deadline or until ctx is
// done, whichever comes first. The pause between attempts selects on
// ctx.Done(), so a cancel ends the wait at once with an error that wraps
// ctx.Err() and names the distfile.
//
// O_CREATE|O_EXCL alone decides acquisition: whoever creates the file holds
// the lock. The flock on that file is a heartbeat, not a second arbiter: the
// kernel drops it when the holder dies, even by SIGKILL, so a lock whose flock
// can be taken has no live holder and is reaped, and one whose flock cannot be
// taken is waited for. The PID in the file only feeds the timeout message, so
// PID reuse can mislead the message but never the lock.
//
// Reaping (unlink-then-recreate) cannot steal a live lock: the unlink happens
// while holding the dead file's flock AND after confirming the path still
// names that inode, so of two waiters flock admits one and the other's check
// fails. A creator, briefly reapable between its O_EXCL and its flock,
// re-confirms the path names its own file before declaring itself the holder,
// and starts over if not.
func acquireLock(ctx context.Context, distdir, name string, deadline time.Time) (*heldLock, error) {
	path := filepath.Join(distdir, lockFilePrefix+name+lockFileSuffix)

	for {
		// 0644, not 0600, and the difference is load-bearing rather than lax.
		// The distdir is the host's own DISTDIR (portage:portage 0775), so the
		// writers this lock separates are frequently DIFFERENT USERS of group
		// portage. A contender detects and reaps a lock by opening it read-only
		// (see waitForLock's os.Open) and taking flock on that descriptor; at
		// 0600 every user but the holder gets EACCES there, cannot tell "held"
		// from "broken", and cannot reap a lock a dead run left behind — which
		// is the exact mutual exclusion this lock exists for, lost. Nothing secret is
		// in the file: it carries the holder's PID so a stale lock is
		// diagnosable, and no user but the owner may write it.
		file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644) //nolint:gosec // G302: 0o644 is readable by design, see above; G304: path is the host distdir joined with a lock name built from distfileName, which refuses anything but one bare file name
		switch {
		case err == nil:
			held, err := claimLock(file, path, name)
			if err != nil {
				return nil, fmt.Errorf("failed to take the lock on distfile %q in %s: %w", name, distdir, err)
			}
			if held != nil {
				return held, nil
			}
			// The file we had just created was reaped from under us. Whoever
			// did it is now racing us for the same name; go round again.
		case errors.Is(err, fs.ErrExist):
			if err := reapIfAbandoned(path); err != nil {
				return nil, fmt.Errorf("failed to inspect the lock on distfile %q in %s: %w", name, distdir, err)
			}
		default:
			return nil, fmt.Errorf("failed to create the lock file for distfile %q in %s: %w", name, distdir, err)
		}

		remaining := time.Until(deadline)
		if remaining <= 0 {
			return nil, fmt.Errorf("%w: %q in %s is held by %s, and it did not become free within %s",
				ErrDistfileLocked, name, distdir, describeHolder(path), lockWait)
		}
		select {
		case <-ctx.Done():
			return nil, fmt.Errorf("waiting for the lock on distfile %q in %s: %w", name, distdir, ctx.Err())
		case <-time.After(min(remaining, lockPoll)):
		}
	}
}

// claimLock finishes an acquisition on a lock file this call has just created,
// and reports the claim, or nil when the file stopped being ours before the
// claim was complete.
//
// The order of the three steps is the contract. The flock comes first, so the
// window in which the new file looks abandoned is as short as it can be made.
// The PID is written second, so it is there before anyone is told to wait for
// us. The identity check comes last, so it covers both of the steps before it:
// if the file was reaped at any point since it was created, this is where that
// is found out, and the caller starts again rather than fetching alongside
// whoever reaped it.
//
// A failure after the file exists closes it and leaves it. That is deliberate:
// an unflocked lock file is precisely what the reaper is for, so the next
// acquirer clears it on its first attempt. Unlinking it here would need the same
// identity check for no benefit.
func claimLock(file *os.File, path, name string) (*heldLock, error) {
	if err := flockExclusiveNonBlocking(file); err != nil {
		_ = file.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) {
			// Somebody reaped our new file and flocked it in the moment
			// before we could. Not an error; a lost race.
			return nil, nil
		}
		return nil, err
	}

	if _, err := file.WriteString(lockPayload(name)); err != nil {
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
	return &heldLock{name: name, path: path, file: file}, nil
}

// reapIfAbandoned removes the lock file at path when its holder is gone, and
// leaves it alone when the holder is alive. It reports only errors: whether
// anything was reaped is not something the caller needs, because either way the
// answer is to try the exclusive create again.
//
// "Gone" is decided by the kernel and not by us — see acquireLock. The identity
// check between taking the flock and unlinking is what makes the removal safe;
// without it this function is the bug it exists to avoid.
func reapIfAbandoned(path string) error {
	// Opened read-only: flock(2) needs no write access, and the lock file may
	// belong to another user of the shared distdir. Unlinking it needs
	// permission on the DIRECTORY, which is the same permission that let us
	// try to create the lock in the first place.
	file, err := os.Open(path) //nolint:gosec // G304: path is the lock path acquireLock built: the host distdir plus a distfileName-confined bare name
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			// Released between our create and this open. Nothing to reap.
			return nil
		}
		return err
	}
	defer func() { _ = file.Close() }()

	if err := flockExclusiveNonBlocking(file); err != nil {
		if errors.Is(err, syscall.EWOULDBLOCK) {
			// A live holder. This is the ordinary contended case.
			return nil
		}
		return err
	}

	same, err := pathNamesFile(path, file)
	if err != nil {
		return err
	}
	if !same {
		// The path names something else now — the lock was released and
		// retaken, or something was planted over it. Either way it is not
		// ours to remove.
		return nil
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) { //nolint:gosec // G703: path is the lock path acquireLock built: the host distdir plus a distfileName-confined bare name
		return err
	}
	return nil
}

// release gives up one lock. Unlink first, close second: the flock is what keeps
// any other process out, so dropping it before the name is gone would let a
// waiter reap and recreate a file we are still about to unlink.
func (h *heldLock) release() {
	if h == nil || h.file == nil {
		return
	}
	// Confirm before removing, for the reason CleanupFailedFetch re-checks its
	// names: this is an unlink in a directory the tool does not own. Nothing
	// can have replaced the file while we hold its flock, which is exactly why
	// the check is cheap enough to keep.
	if same, err := pathNamesFile(h.path, h.file); err == nil && same {
		_ = os.Remove(h.path) //nolint:gosec // G703: h.path is the lock path acquireLock built: the host distdir plus a distfileName-confined bare name
	}
	_ = h.file.Close()
	h.file = nil
}

// pathNamesFile reports whether path still refers to the very file that is
// open, comparing device and inode rather than names.
//
// The lookup is os.Lstat, so a symlink planted at the path is compared as
// itself and never followed. It cannot then be the file we hold, so every caller
// treats that as "not ours" and backs off — which is the safe direction for all
// three of them, since each is deciding whether it may unlink.
func pathNamesFile(path string, file *os.File) (bool, error) {
	onDisk, err := os.Lstat(path) //nolint:gosec // G703: path is the lock path acquireLock built: the host distdir plus a distfileName-confined bare name; Lstat never follows a planted symlink
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

// flockExclusiveNonBlocking takes flock(2) LOCK_EX on the open file, or returns
// syscall.EWOULDBLOCK if another open file description already holds it.
//
// flock(2) and not fcntl(2): an flock belongs to the OPEN FILE DESCRIPTION, so
// two goroutines in one process that opened the file separately contend exactly
// as two processes do. POSIX record locks belong to the process, and would hand
// the second goroutine of a sweep a lock the first one already holds, missing
// the most common concurrency of all: the workers of one sweep
// (autoupdate/sweep.go, `func ExecuteOverlaySweep`).
//
// The descriptor is reached through SyscallConn rather than Fd so the file
// cannot be closed or garbage-collected underneath the call.
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

// lockPayload is what a holder writes into its lock file. It is for a person:
// whoever finds one of these in /var/cache/distfiles reads what it is, who has
// it and since when, without a log to correlate against and without this tool
// installed. One field per line and "pid=" first among them, so it is as easy to
// grep as to read.
func lockPayload(name string) string {
	return fmt.Sprintf("bentoo distfile lock\npid=%d\ndistfile=%s\ntaken=%s\n",
		os.Getpid(), name, time.Now().UTC().Format(time.RFC3339))
}

// describeHolder turns a lock file into the phrase the timeout error uses. It
// never fails: a lock we could not read is still a lock we could not take, and
// the caller is already reporting the more important half of that.
func describeHolder(path string) string {
	if pid, ok := holderPID(path); ok {
		return fmt.Sprintf("pid %d", pid)
	}
	return fmt.Sprintf("a process that did not identify itself in %s", filepath.Base(path))
}

// holderPID reads the PID a holder recorded, and reports whether there was one
// to read. Everything about an unreadable, empty, truncated or unparseable lock
// file is the same answer — nobody named — because this value is only ever put
// into a message.
func holderPID(path string) (int, bool) {
	data, err := os.ReadFile(path) //nolint:gosec // G304: path is the lock path acquireLock built: the host distdir plus a distfileName-confined bare name
	if err != nil {
		return 0, false
	}
	for line := range strings.SplitSeq(string(data), "\n") {
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
