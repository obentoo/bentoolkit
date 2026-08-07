package distfiles

import (
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
// will still be there next time. D5's classifier must be able to tell them
// apart without reading either message.
var ErrDistfileLocked = errors.New("distfile is locked by another writer")

// The lock file sits IN the distdir, next to the distfile it guards, and is
// named "." + <distfile> + ".bentoo_lockfile".
//
// # Why in the distdir rather than beside it
//
// The distdir is the only directory that all the writers this lock exists to
// separate are guaranteed to share. Two `bentoo` runs may have different
// TMPDIRs, different homes, different mount namespaces and different users, and
// still be pointed at one DISTDIR — indeed that is the ordinary case, since the
// default comes from `portageq distdir` and is the same answer for everyone on
// the host. A lock kept anywhere else would let exactly those two runs miss each
// other, which is the collision R2.4 is about. It is also the only directory
// this package has already proved writable (Probe), and the one directory the
// story forbids swapping for a temporary one.
//
// Creating a file inside a directory is not changing that directory, so R2.5 is
// untouched: no mode and no owner is altered, here or anywhere else in this
// file. What a lock file does cost is a name in a directory shared with the host
// package manager, which is why the name is a dotfile, says whose it is, and is
// removed on release.
//
// # Why this shape of name
//
// Portage names the lock for a distfile "." + <distfile> + ".portage_lockfile"
// (portage/locks.py:251), and one of those appears in this very directory
// whenever an emerge fetches. Mirroring the shape means an operator who has seen
// portage's recognises ours at a glance, and the single word that differs is the
// word that says which tool to blame. The names cannot collide with each other,
// with a real distfile (which never begins with a dot), with Probe's
// .bentoo-distdir-probe-* or with Quarantine's <name>._bentoo_quarantine_.*.
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
// on a slow one. Giving up is the specified outcome (D4) and the cheap one — the
// package is reported as failed and the next run finds the distfile already
// fetched and verified, which is R2.1's reuse. Waiting is the expensive one: the
// sweep runs under a semaphore (internal/autoupdate/sweep.go:620), so a waiter
// is holding a worker slot the whole time it waits.
//
// Tests shorten it, the way they shorten portageqTimeout, so the timeout branch
// can be exercised without a slow test.
var lockWait = 2 * time.Minute

// lockPoll is how long a contended attempt sleeps before trying again. There is
// nothing to wake a waiter up — the holder is a different process, and the
// release it is waiting for is an unlink — so polling is the mechanism, and the
// interval trades responsiveness against how often a whole sweep's worth of
// waiters touch the same directory.
var lockPoll = 25 * time.Millisecond

// FetchLock is the exclusive claim one manifest step holds over the distfile
// names it is about to fetch, for the whole of its pkgdev invocation. It is D4's
// first half and the thing R2.4 asks for, and it is also the load-bearing
// assumption under D3's ownership rule.
//
// # The window it closes
//
// FetchScope decides what this run may delete by measuring which expected names
// were ABSENT before the fetch: absent then, therefore ours now. That inference
// is only true if nothing else can write those names in between, and between
// RecordFetchScope's Lstat and CleanupFailedFetch's Remove there is a long gap —
// a whole download. Without this lock a second worker can create a file under
// one of those names inside that gap, and our failure branch then deletes a
// download that is not ours: the exact half of R2.4 that says one worker's
// failure must not delete a file another worker is using.
//
// Quarantine has the same shape of gap and says so (see its Concurrency note):
// a file that appears between its Lstat and its Rename is not seen. The lock is
// what closes both, which is why it is taken before either of them runs.
//
// # Where it goes in the manifest step
//
//	resolve -> LOCK -> Quarantine -> prepopulate -> record -> pkgdev -> on failure: cleanup -> RELEASE
//
// Held across the lot. Taking it later would leave Quarantine's window open;
// releasing it earlier would leave FetchScope's open. Release is safe to defer
// immediately after a successful LockFetch, and safe to call more than once.
//
// # What it does NOT cover
//
// The host's own package manager. Portage serializes its fetches under
// FEATURES=distlocks, but pkgdev does not participate in that mechanism — see
// D4 in this story's design.md for the evidence — so a sweep running at the same
// time as an emerge that wants the same distfile is not serialized by anything.
// That is a documented limitation of the tool, not something this type can fix
// from outside, and inventing a private handshake with portage would be worse
// than saying so.
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
// them as one claim, or returns an error having released whatever it managed to
// take. There is no partial success: a step that holds three of four locks is
// unprotected on the fourth, which is the only one that matters.
//
// Each name is reduced with distfileName first, and one that does not reduce to
// a filename is dropped rather than locked. The reduction is what keeps the lock
// file inside the distdir — the name is untrusted input, and this directory is
// shared with the system package manager — and a string that is not a filename
// cannot name a file in this directory for two writers to collide on. Duplicates
// collapse: locking one name twice would deadlock against ourselves.
//
// The reduced names are locked in sorted order, and that is not cosmetic. Two
// workers whose packages share two distfiles would otherwise be able to take
// them in opposite orders and wait on each other; one global order over one
// shared directory makes a cycle impossible, so the only way this call ends is
// with the locks or with the timeout.
//
// An empty distdir is refused, as in Probe, Quarantine and RecordFetchScope:
// filepath.Join("", name) resolves against the WORKING directory, so the lock
// would be taken somewhere nobody asked about and would separate nobody from
// anybody.
//
// A caller that gets an error MUST NOT go on to fetch. That is the whole point:
// a fetch racing another fetch onto the same path is what R2.4 forbids, and
// "report the package as failed" is the specified answer (D4).
func LockFetch(distdir string, names []string) (*FetchLock, error) {
	if distdir == "" {
		return nil, errors.New("cannot lock distfiles: no distdir was resolved")
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
		held, err := acquireLock(distdir, name, deadline)
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

// acquireLock takes one distfile's lock, waiting until deadline.
//
// # The rule, and why there are two mechanisms
//
// Acquisition is decided by O_CREATE|O_EXCL and by nothing else: whoever creates
// the file holds the lock (D4). The flock taken on that same file is not a
// second arbiter — it is a heartbeat. The kernel drops an flock when the process
// holding it dies, for any reason and including SIGKILL, so "can I flock the
// existing lock file?" is an exact answer to "is its holder still alive?", with
// no timeout to tune and no PID to guess at. That is the staleness rule: a lock
// whose flock can be taken has no live holder and is reaped; a lock whose flock
// cannot be taken is live and is waited for. A killed run therefore blocks the
// next one for as long as it takes that run to notice, and not one moment more.
//
// The PID written into the file is for the human, exactly as D4 says: it is what
// the timeout error reports so a stuck lock can be traced to a process. Nothing
// in this file decides anything from it, so PID reuse cannot mislead the lock —
// only the message, and only into naming a process that has since been recycled.
//
// # Why the reap cannot steal a live lock
//
// Reaping is unlink-then-recreate, which is the classic way to destroy the lock
// you were only supposed to clean up. It is safe here because the unlink happens
// while holding the dead file's flock AND only after confirming the path still
// names that same inode. Two waiters cannot both be in that state: flock admits
// one, and the loser's confirmation then fails, because the path no longer names
// the inode it opened. The one gap left is the instant between a creator's
// O_EXCL and its flock, where its file is briefly reapable; the creator closes
// it by re-confirming the path still names its own file before it declares
// itself the holder, and starting over if it does not.
func acquireLock(distdir, name string, deadline time.Time) (*heldLock, error) {
	path := filepath.Join(distdir, lockFilePrefix+name+lockFileSuffix)

	for {
		// 0644, not 0600, and the difference is load-bearing rather than lax.
		// The distdir is the host's own DISTDIR (portage:portage 0775), so the
		// writers this lock separates are frequently DIFFERENT USERS of group
		// portage. A contender detects and reaps a lock by opening it read-only
		// (see waitForLock's os.Open) and taking flock on that descriptor; at
		// 0600 every user but the holder gets EACCES there, cannot tell "held"
		// from "broken", and cannot reap a lock a dead run left behind — which
		// is the exact mutual exclusion R2.4 asks for, lost. Nothing secret is
		// in the file: it carries the holder's PID so a stale lock is
		// diagnosable, and no user but the owner may write it.
		// #nosec G302 -- readable by design; see above
		file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
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
		time.Sleep(min(remaining, lockPoll))
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
	file, err := os.Open(path)
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
	if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
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
		_ = os.Remove(h.path)
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

// flockExclusiveNonBlocking takes flock(2) LOCK_EX on the open file, or returns
// syscall.EWOULDBLOCK if another open file description already holds it.
//
// flock(2) and not fcntl(2): an flock belongs to the OPEN FILE DESCRIPTION, so
// two goroutines in one process that opened the file separately contend exactly
// as two processes do. POSIX record locks belong to the process, and would hand
// the second goroutine of a sweep a lock the first one already holds — the
// concurrency R2.4 names first (sweep.go:620) is the one they would not see.
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
	data, err := os.ReadFile(path)
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
