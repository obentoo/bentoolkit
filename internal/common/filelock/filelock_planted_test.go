package filelock

// Authored for story 056, sub-task 8.3 (S056-R4.8, S056-R4.9, S056-R8.12).
// Something planted at a lock path — a symlink, a FIFO, a directory, or a lock
// file whose pid= line sits past the read bound — must never hang Acquire,
// redirect it onto another file, or make it read without bound.
//
// shortBounds comes from filelock_test.go. The tests that can block on a FIFO
// do NOT call it: an Acquire stuck in open(2) has read Wait with no
// happens-before edge to a later write of Wait, and the race detector would
// report that write in whichever test ran next.

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
	"testing"
	"time"
)

// readBound is the byte limit R4.9 puts on reading a holder's PID.
const readBound = 4096

// plantedPID is written into planted files. It must never reach an error
// message unless the lock file itself, within the read bound, carries it.
const plantedPID = 31337

// plantedDeadline bounds how long a test lets Acquire run before it counts as
// blocked. The fixed Acquire refuses a planted entry at once.
const plantedDeadline = 3 * time.Second

type acquireResult struct {
	lock *Lock
	err  error
}

func acquireAsync(path, purpose string) <-chan acquireResult {
	done := make(chan acquireResult, 1)
	go func() {
		lock, err := Acquire(path, purpose)
		done <- acquireResult{lock: lock, err: err}
	}()
	return done
}

// wakeFIFOReader frees an Acquire stuck in open(2) on fifo, so it does not
// outlive its test. Opening a FIFO O_RDWR never blocks on Linux and counts as a
// writer, which completes the stuck read-only open; removing entry then lets
// the woken Acquire create its own lock and return. If it still does not return,
// the goroutine is leaked — acceptable in a test binary, and logged.
func wakeFIFOReader(t *testing.T, fifo, entry string, done <-chan acquireResult) {
	t.Helper()
	writer, err := os.OpenFile(fifo, os.O_RDWR, 0)
	if err != nil {
		t.Logf("could not open %s to wake the blocked Acquire (goroutine leaked): %v", fifo, err)
		return
	}
	defer func() { _ = writer.Close() }()
	_ = os.Remove(entry)
	select {
	case r := <-done:
		r.lock.Release()
	case <-time.After(10 * time.Second):
		t.Logf("Acquire on %s still blocked after the wake-up; goroutine leaked", entry)
	}
}

// paddingLines returns exactly n bytes of comment lines, each ending in '\n',
// so a bounded read of them never cuts a line in two.
func paddingLines(n int) string {
	var b strings.Builder
	for n > 0 {
		width := min(n, 64)
		b.WriteString(strings.Repeat("#", width-1))
		b.WriteByte('\n')
		n -= width
	}
	return b.String()
}

// holdFlock takes the flock on path through a descriptor of the test's own, as
// a live holder in another process would. Acquire opens its own descriptor, and
// an flock belongs to the open file description, so the two contend.
func holdFlock(t *testing.T, path string) {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = f.Close() })
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		t.Fatalf("holding the flock on %s: %v", path, err)
	}
}

func assertRefusal(t *testing.T, path string, err error) {
	t.Helper()
	if err == nil {
		t.Errorf("Acquire over a planted entry at %s returned no error", path)
		return
	}
	if errors.Is(err, ErrLocked) {
		t.Errorf("a planted entry was reported as contention (wraps ErrLocked): %v", err)
	}
	if !strings.Contains(err.Error(), path) {
		t.Errorf("error %q does not name %s", err, path)
	}
	if strings.Contains(err.Error(), fmt.Sprintf("pid %d", plantedPID)) {
		t.Errorf("error %q names the PID written in the planted target: the target was read", err)
	}
}

// TestAcquire_PlantedSymlinkToFIFOIsRefusedWithoutBlocking: R4.8 — a symlink
// to a FIFO must not be followed, so no open(2) waits for a writer.
func TestAcquire_PlantedSymlinkToFIFOIsRefusedWithoutBlocking(t *testing.T) {
	dir := t.TempDir()
	fifo := filepath.Join(dir, "planted.fifo")
	if err := syscall.Mkfifo(fifo, 0o644); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, ".state.bentoo-lock")
	if err := os.Symlink(fifo, path); err != nil {
		t.Fatal(err)
	}

	done := acquireAsync(path, "planted")
	select {
	case r := <-done:
		if r.err == nil {
			r.lock.Release()
		}
		assertRefusal(t, path, r.err)
		if target, err := os.Readlink(path); err != nil || target != fifo {
			t.Errorf("the planted symlink was not left in place: readlink = %q, %v", target, err)
		}
		if info, err := os.Lstat(fifo); err != nil || info.Mode().Type() != os.ModeNamedPipe {
			t.Errorf("the symlink's FIFO target was modified: %v, %v", info, err)
		}
	case <-time.After(plantedDeadline):
		t.Errorf("Acquire did not return within %s: it followed the planted symlink %s and blocked opening the FIFO %s",
			plantedDeadline, path, fifo)
		wakeFIFOReader(t, fifo, path, done)
	}
}

// TestAcquire_PlantedFIFOIsRefusedWithoutBlocking: R4.8 — a FIFO at the lock
// path itself (no symlink) is not a regular file and must not block either.
func TestAcquire_PlantedFIFOIsRefusedWithoutBlocking(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".state.bentoo-lock")
	if err := syscall.Mkfifo(path, 0o644); err != nil {
		t.Fatal(err)
	}

	done := acquireAsync(path, "planted")
	select {
	case r := <-done:
		if r.err == nil {
			r.lock.Release()
		}
		assertRefusal(t, path, r.err)
		if info, err := os.Lstat(path); err != nil || info.Mode().Type() != os.ModeNamedPipe {
			t.Errorf("the planted FIFO at %s was not kept: %v, %v", path, info, err)
		}
	case <-time.After(plantedDeadline):
		t.Errorf("Acquire did not return within %s: it blocked opening the FIFO planted at %s",
			plantedDeadline, path)
		wakeFIFOReader(t, path, path, done)
	}
}

// TestAcquire_PlantedSymlinkToVictimLeavesVictimAndLinkIntact: R4.8 — the
// hostile collapse: a symlink to an ordinary file must not be treated as the
// lock. The victim carries a pid= line so that reading it shows in the error.
func TestAcquire_PlantedSymlinkToVictimLeavesVictimAndLinkIntact(t *testing.T) {
	shortBounds(t, 300*time.Millisecond, 10*time.Millisecond)
	dir := t.TempDir()
	victim := filepath.Join(dir, "victim.conf")
	body := []byte(fmt.Sprintf("pid=%d\nkeep=these bytes\n", plantedPID))
	if err := os.WriteFile(victim, body, 0o600); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, ".state.bentoo-lock")
	if err := os.Symlink(victim, path); err != nil {
		t.Fatal(err)
	}

	lock, err := Acquire(path, "planted")
	if err == nil {
		lock.Release()
	}
	assertRefusal(t, path, err)

	got, readErr := os.ReadFile(victim)
	if readErr != nil {
		t.Fatalf("the victim %s no longer exists: %v", victim, readErr)
	}
	if string(got) != string(body) {
		t.Errorf("victim bytes changed: got %q, want %q", got, body)
	}
	if info, statErr := os.Stat(victim); statErr != nil || info.Mode().Perm() != 0o600 {
		t.Errorf("victim mode changed: %v, %v", info, statErr)
	}
	if target, linkErr := os.Readlink(path); linkErr != nil || target != victim {
		t.Errorf("the planted symlink was not left in place: readlink = %q, %v", target, linkErr)
	}
}

// TestAcquire_PlantedDanglingSymlinkIsRefusedWithoutCreatingTarget: R4.8 — a
// symlink whose target does not exist is still a symlink: refused as such, not
// waited on as a lock, and its target never created.
func TestAcquire_PlantedDanglingSymlinkIsRefusedWithoutCreatingTarget(t *testing.T) {
	shortBounds(t, 300*time.Millisecond, 10*time.Millisecond)
	dir := t.TempDir()
	target := filepath.Join(dir, "never-created")
	path := filepath.Join(dir, ".state.bentoo-lock")
	if err := os.Symlink(target, path); err != nil {
		t.Fatal(err)
	}

	lock, err := Acquire(path, "planted")
	if err == nil {
		lock.Release()
	}
	assertRefusal(t, path, err)
	if _, statErr := os.Lstat(target); !errors.Is(statErr, os.ErrNotExist) {
		t.Errorf("the dangling symlink's target %s was created: %v", target, statErr)
	}
	if got, linkErr := os.Readlink(path); linkErr != nil || got != target {
		t.Errorf("the planted symlink was not left in place: readlink = %q, %v", got, linkErr)
	}
}

// TestAcquire_PlantedEmptyDirectoryIsRefusedAndKept: R4.8 — the hostile case
// for a directory: an EMPTY one can be removed like a stale lock file, and must
// not be.
func TestAcquire_PlantedEmptyDirectoryIsRefusedAndKept(t *testing.T) {
	shortBounds(t, 300*time.Millisecond, 10*time.Millisecond)
	path := filepath.Join(t.TempDir(), ".state.bentoo-lock")
	if err := os.Mkdir(path, 0o755); err != nil {
		t.Fatal(err)
	}

	lock, err := Acquire(path, "planted")
	if err == nil {
		lock.Release()
	}
	assertRefusal(t, path, err)
	if info, statErr := os.Lstat(path); statErr != nil || !info.IsDir() {
		t.Errorf("the directory planted at %s was not kept: %v, %v", path, info, statErr)
	}
}

// TestAcquire_PlantedNonEmptyDirectoryIsRefusedAndKept: R4.8 — a directory
// with content is refused and nothing in it is touched.
func TestAcquire_PlantedNonEmptyDirectoryIsRefusedAndKept(t *testing.T) {
	shortBounds(t, 300*time.Millisecond, 10*time.Millisecond)
	path := filepath.Join(t.TempDir(), ".state.bentoo-lock")
	if err := os.Mkdir(path, 0o755); err != nil {
		t.Fatal(err)
	}
	inner := filepath.Join(path, "keep.txt")
	if err := os.WriteFile(inner, []byte("keep\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	lock, err := Acquire(path, "planted")
	if err == nil {
		lock.Release()
	}
	assertRefusal(t, path, err)
	if got, readErr := os.ReadFile(inner); readErr != nil || string(got) != "keep\n" {
		t.Errorf("the planted directory's content changed: %q, %v", got, readErr)
	}
}

// TestAcquire_UnflockedLockWithPIDPastReadBoundIsReaped: R4.9 with R8.12 — the
// bound limits what is READ, never whether an abandoned regular lock is
// reaped: that decision is the flock's, not the PID's.
func TestAcquire_UnflockedLockWithPIDPastReadBoundIsReaped(t *testing.T) {
	shortBounds(t, time.Minute, 10*time.Millisecond)
	path := filepath.Join(t.TempDir(), ".state.bentoo-lock")
	body := paddingLines(readBound) + fmt.Sprintf("pid=%d\n", plantedPID)
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}

	lock, err := Acquire(path, "planted")
	if err != nil {
		t.Fatalf("Acquire over an unflocked lock with its pid= line past byte %d: %v", readBound, err)
	}
	lock.Release()
}

// TestAcquire_LiveHolderWithPIDPastReadBoundDidNotIdentifyItself: R4.9 — a
// pid= line that starts at byte offset 4096 lies outside the bounded read, so
// the holder is reported as one that did not identify itself.
func TestAcquire_LiveHolderWithPIDPastReadBoundDidNotIdentifyItself(t *testing.T) {
	shortBounds(t, 300*time.Millisecond, 10*time.Millisecond)
	path := filepath.Join(t.TempDir(), ".state.bentoo-lock")
	body := paddingLines(readBound) + fmt.Sprintf("pid=%d\n", plantedPID)
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	holdFlock(t, path)

	lock, err := Acquire(path, "waiter")
	if err == nil {
		lock.Release()
		t.Fatal("Acquire got a lock whose flock is held")
	}
	if !errors.Is(err, ErrLocked) {
		t.Errorf("a live holder's timeout does not wrap ErrLocked: %v", err)
	}
	if !strings.Contains(err.Error(), path) {
		t.Errorf("error %q does not name %s", err, path)
	}
	if !strings.Contains(err.Error(), "did not identify itself") {
		t.Errorf("error %q does not say the holder did not identify itself: the read went past byte %d", err, readBound)
	}
	if strings.Contains(err.Error(), fmt.Sprintf("pid %d", plantedPID)) {
		t.Errorf("error %q names a PID written past byte %d", err, readBound)
	}
}

// TestAcquire_LiveHolderWithPIDEndingAtReadBoundIsNamed: R4.9 — the converse
// of the bound: a pid= line whose last byte is byte 4096 is inside it, and the
// holder is still named by its PID.
func TestAcquire_LiveHolderWithPIDEndingAtReadBoundIsNamed(t *testing.T) {
	shortBounds(t, 300*time.Millisecond, 10*time.Millisecond)
	path := filepath.Join(t.TempDir(), ".state.bentoo-lock")
	pidLine := fmt.Sprintf("pid=%d\n", plantedPID)
	body := paddingLines(readBound-len(pidLine)) + pidLine + "purpose=past the bound\n"
	if len(body) < readBound || body[:readBound][readBound-len(pidLine):] != pidLine {
		t.Fatalf("fixture: the pid= line does not end at byte %d", readBound)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	holdFlock(t, path)

	lock, err := Acquire(path, "waiter")
	if err == nil {
		lock.Release()
		t.Fatal("Acquire got a lock whose flock is held")
	}
	if !errors.Is(err, ErrLocked) {
		t.Errorf("a live holder's timeout does not wrap ErrLocked: %v", err)
	}
	if want := fmt.Sprintf("pid %d", plantedPID); !strings.Contains(err.Error(), want) {
		t.Errorf("error %q does not name %s: the read stopped short of byte %d", err, want, readBound)
	}
}

// anyNamedPID matches an error that names a holder by PID.
var anyNamedPID = regexp.MustCompile(`\bpid [0-9]+`)

// TestAcquire_LiveHolderWithPIDLineCutByReadBoundDidNotIdentifyItself: R4.9 —
// the hostile half of the bound: a pid= line that straddles byte 4096 is cut by
// the bounded read to a shorter, valid-looking PID ("pid=31"). A line not ended
// by a newline within the bound is ignored, so the holder did not identify
// itself — neither the full PID nor the truncated one may be named.
func TestAcquire_LiveHolderWithPIDLineCutByReadBoundDidNotIdentifyItself(t *testing.T) {
	shortBounds(t, 300*time.Millisecond, 10*time.Millisecond)
	path := filepath.Join(t.TempDir(), ".state.bentoo-lock")
	cut := "pid=31" // what a 4096-byte read keeps of the line below
	body := paddingLines(readBound-len(cut)) + fmt.Sprintf("pid=%d\n", plantedPID)
	if body[:readBound][readBound-len(cut):] != cut {
		t.Fatalf("fixture: byte %d does not cut the pid= line to %q", readBound, cut)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	holdFlock(t, path)

	lock, err := Acquire(path, "waiter")
	if err == nil {
		lock.Release()
		t.Fatal("Acquire got a lock whose flock is held")
	}
	if !errors.Is(err, ErrLocked) {
		t.Errorf("a live holder's timeout does not wrap ErrLocked: %v", err)
	}
	if !strings.Contains(err.Error(), "did not identify itself") {
		t.Errorf("error %q does not say the holder did not identify itself: a pid= line cut at byte %d was used", err, readBound)
	}
	if named := anyNamedPID.FindString(err.Error()); named != "" {
		t.Errorf("error %q names %q from a pid= line not ended within byte %d", err, named, readBound)
	}
}
