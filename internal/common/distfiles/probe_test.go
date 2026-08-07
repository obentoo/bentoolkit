package distfiles

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"syscall"
	"testing"
)

// assertNamesTheDirectory checks that err names dir IN ITS OWN RIGHT, and not
// merely as the prefix of the probe file's path.
//
// That distinction is R1.4's whole point. "distdir is not writable:
// /var/cache/distfiles: open …/.bentoo-distdir-probe-1234-1: permission denied"
// tells an operator which directory to fix; a bare "open
// /var/cache/distfiles/.bentoo-distdir-probe-1234-1: permission denied" makes
// them work it out from a filename they have never seen. A plain
// strings.Contains cannot tell the two apart, because the probe path begins
// with the directory — so every path UNDER dir is blanked out first, and dir
// must still be named in what is left.
func assertNamesTheDirectory(t *testing.T, err error, dir string) {
	t.Helper()
	if err == nil {
		t.Fatalf("no error to inspect; expected a failure naming %q", dir)
	}
	under := regexp.MustCompile(regexp.QuoteMeta(dir) + `/\S+`)
	stripped := under.ReplaceAllString(err.Error(), "<a file under it>")
	if !strings.Contains(stripped, dir) {
		t.Errorf("the failure must name the directory itself (R1.4), not only the probe file inside it; err = %v", err)
	}
}

// denyFileWrites lowers this process's file-size limit to zero, so creating a
// file still succeeds while writing any byte into it fails. It returns a
// restore function that is safe to call more than once, and also registers it
// as cleanup so a failed assertion cannot leave the limit in place.
//
// This reproduces, deterministically and without root, the shape of the failure
// this whole story is about: on the full tmpfs the sweep was downloading into,
// the file appeared and the bytes did not. Nothing is asserted while the limit
// is down — the calls are made, the limit is restored, and only then does the
// test report.
func denyFileWrites(t *testing.T) func() {
	t.Helper()
	var orig syscall.Rlimit
	if err := syscall.Getrlimit(syscall.RLIMIT_FSIZE, &orig); err != nil {
		t.Skipf("skipping: cannot read RLIMIT_FSIZE on this system (%v), so a write that fails "+
			"after a successful create cannot be staged here", err)
	}
	if err := syscall.Setrlimit(syscall.RLIMIT_FSIZE, &syscall.Rlimit{Cur: 0, Max: orig.Max}); err != nil {
		t.Skipf("skipping: cannot lower RLIMIT_FSIZE on this system (%v)", err)
	}
	var once sync.Once
	restore := func() {
		once.Do(func() { _ = syscall.Setrlimit(syscall.RLIMIT_FSIZE, &orig) })
	}
	t.Cleanup(restore)
	return restore
}

// makeUnwritableDir returns a directory the invoking user cannot write into,
// and restores its mode afterwards so t.TempDir's own cleanup can still remove
// it. Mode 0500 keeps the directory readable and searchable — the assertions
// below stat its contents — while denying the create.
//
// It SKIPS when the tests run as root, and says so loudly. Mode bits do not
// apply to uid 0: the write would succeed, the assertion would pass for a
// reason that has nothing to do with the code, and a green would then mean
// nothing. A skip that hides an assertion is the same shape of defect this
// story is about — a failure reported as something it is not — so it is stated
// rather than silent.
func makeUnwritableDir(t *testing.T) string {
	t.Helper()
	if os.Geteuid() == 0 {
		t.Skip("skipping: running as root (euid 0), where mode 0500 does not deny a write. " +
			"This test can only assert what it claims as an unprivileged user; passing it " +
			"here would be vacuous, so it is skipped instead of faked. CI runs unprivileged.")
	}

	dir := filepath.Join(t.TempDir(), "distfiles")
	if err := os.Mkdir(dir, 0o750); err != nil {
		t.Fatalf("create directory to lock: %v", err)
	}
	if err := os.Chmod(dir, 0o500); err != nil {
		t.Fatalf("chmod %q to 0500: %v", dir, err)
	}
	// Restore before t.TempDir's cleanup runs, or the temp tree cannot be
	// removed and the failure surfaces as an unrelated cleanup error.
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })

	// Prove the lock actually took: on some filesystems (and under some
	// capabilities) 0500 is not enforced, and every assertion below would then
	// be measuring nothing.
	canary := filepath.Join(dir, "canary")
	if err := os.WriteFile(canary, []byte("x"), 0o600); err == nil {
		_ = os.Remove(canary)
		t.Skipf("skipping: %q is still writable at mode 0500, so this filesystem does not "+
			"enforce the permission this test depends on", dir)
	}
	return dir
}

// TestProbeSucceedsOnWritableDir is the positive half of R3.1: on a directory
// the user can write to, the pre-flight passes and says nothing.
func TestProbeSucceedsOnWritableDir(t *testing.T) {
	dir := t.TempDir()

	if err := Probe(dir); err != nil {
		t.Fatalf("Probe(%q) error = %v, want nil for a writable directory", dir, err)
	}

	// Repeatable: the classifier added later probes the same directory again
	// after a failed run, so a single-use probe would be a bug.
	for i := 0; i < 3; i++ {
		if err := Probe(dir); err != nil {
			t.Fatalf("Probe(%q) call %d error = %v; Probe must be repeatable", dir, i+2, err)
		}
	}
}

// TestProbeFailsWithErrDistdirNotWritableOnReadOnlyDir is R1.4: a directory the
// invoking user cannot write to is a hard failure that names the path, carries
// the operating system's reason, and is testable with errors.Is.
//
// This is the case that matters on a host where the invoking user is NOT in
// group portage: /var/cache/distfiles is portage:portage 0775 there too, and
// the correct answer is this error rather than a quieter directory (M2).
func TestProbeFailsWithErrDistdirNotWritableOnReadOnlyDir(t *testing.T) {
	t.Run("a directory the user has no write permission on", func(t *testing.T) {
		dir := makeUnwritableDir(t)

		err := Probe(dir)
		if err == nil {
			t.Fatalf("Probe(%q) error = nil; an unwritable distdir must fail loudly, never silently", dir)
		}
		if !errors.Is(err, ErrDistdirNotWritable) {
			t.Errorf("errors.Is(err, ErrDistdirNotWritable) = false for %v; callers gate on this sentinel", err)
		}
		assertNamesTheDirectory(t, err, dir)
		// The reason has to survive the wrapping too: "not writable" without
		// the underlying errno is not a diagnosis an operator can act on.
		if !errors.Is(err, fs.ErrPermission) {
			t.Errorf("the underlying os error must survive the wrap; errors.Is(err, fs.ErrPermission) = false for %v", err)
		}

		// A failed probe must not leave its own file behind — least of all in a
		// directory it has just declared unusable.
		entries, readErr := os.ReadDir(dir)
		if readErr != nil {
			t.Fatalf("read %q: %v", dir, readErr)
		}
		if len(entries) != 0 {
			t.Errorf("a failed Probe left %d entry/entries in %q: %v", len(entries), dir, entries)
		}
	})

	t.Run("a directory that accepts the file but not the bytes", func(t *testing.T) {
		// The failure this story was written about wore this exact disguise:
		// on a full tmpfs the file is created and the write dies with ENOSPC,
		// which wget reported as "Cannot write to '…' (Success)". A probe that
		// only created an empty file would call that directory usable and hand
		// the download straight back to the tmpfs.
		dir := t.TempDir()

		restore := denyFileWrites(t)
		err := Probe(dir)
		entries, readErr := os.ReadDir(dir)
		restore()

		if err == nil {
			t.Fatal("Probe() error = nil while no byte can be written; creating an inode is not evidence that the directory is usable")
		}
		if !errors.Is(err, ErrDistdirNotWritable) {
			t.Errorf("errors.Is(err, ErrDistdirNotWritable) = false for %v", err)
		}
		assertNamesTheDirectory(t, err, dir)
		if !errors.Is(err, syscall.EFBIG) {
			t.Errorf("the underlying write error must survive the wrap; errors.Is(err, EFBIG) = false for %v", err)
		}
		// The file it managed to create must still be taken away: the failure
		// path runs in the host's shared DISTDIR too.
		if readErr != nil {
			t.Fatalf("read %q: %v", dir, readErr)
		}
		if len(entries) != 0 {
			t.Errorf("a probe whose write failed left %d entry/entries behind: %v", len(entries), entries)
		}
	})

	t.Run("an empty path is refused instead of probing the working directory", func(t *testing.T) {
		sandbox := t.TempDir()
		t.Chdir(sandbox)

		err := Probe("")
		if err == nil {
			t.Fatal("Probe(\"\") error = nil; an empty distdir is not a directory to probe")
		}
		if !errors.Is(err, ErrDistdirNotWritable) {
			t.Errorf("errors.Is(err, ErrDistdirNotWritable) = false for %v", err)
		}
		entries, readErr := os.ReadDir(sandbox)
		if readErr != nil {
			t.Fatalf("read sandbox: %v", readErr)
		}
		if len(entries) != 0 {
			t.Errorf("Probe(\"\") wrote %d entry/entries into the working directory: %v", len(entries), entries)
		}
	})
}

// TestProbeRemovesItsOwnFile pins the second half of the probe's contract: it
// writes into a directory that belongs to the host package manager, so what it
// creates it also takes away. A probe file left behind in the shared DISTDIR is
// litter every emerge on the machine would then see.
func TestProbeRemovesItsOwnFile(t *testing.T) {
	t.Run("a successful probe leaves the directory as it found it", func(t *testing.T) {
		dir := t.TempDir()
		// Seed an unrelated file, so the assertion distinguishes "the probe
		// cleaned up after itself" from "the probe removed everything".
		payload := filepath.Join(dir, "hello-1.0.0.tar.gz")
		if err := os.WriteFile(payload, []byte("cached"), 0o600); err != nil {
			t.Fatalf("seed distfile: %v", err)
		}

		if err := Probe(dir); err != nil {
			t.Fatalf("Probe(%q) error = %v", dir, err)
		}

		entries, err := os.ReadDir(dir)
		if err != nil {
			t.Fatalf("read %q: %v", dir, err)
		}
		if len(entries) != 1 || entries[0].Name() != "hello-1.0.0.tar.gz" {
			names := make([]string, 0, len(entries))
			for _, e := range entries {
				names = append(names, e.Name())
			}
			t.Errorf("directory contents after Probe = %v, want only the pre-existing distfile", names)
		}
		if data, err := os.ReadFile(payload); err != nil || string(data) != "cached" {
			t.Errorf("Probe disturbed a file it did not create; read = %q, err = %v", data, err)
		}
	})

	t.Run("concurrent probes of one directory neither collide nor litter", func(t *testing.T) {
		// The classifier added by a later sub-task re-probes the same distdir
		// once per failed package, and a sweep runs those packages in parallel.
		// A probe name unique only per PROCESS would have two goroutines share
		// a filename: one would remove the other's file mid-probe.
		dir := t.TempDir()

		const goroutines = 32
		errs := make([]error, goroutines)
		var wg sync.WaitGroup
		start := make(chan struct{})
		for i := 0; i < goroutines; i++ {
			wg.Add(1)
			go func(i int) {
				defer wg.Done()
				<-start // widen the window: everyone probes at once
				errs[i] = Probe(dir)
			}(i)
		}
		close(start)
		wg.Wait()

		for i, err := range errs {
			if err != nil {
				t.Errorf("concurrent Probe %d error = %v; the directory is writable and Probe must be concurrency-safe", i, err)
			}
		}
		entries, err := os.ReadDir(dir)
		if err != nil {
			t.Fatalf("read %q: %v", dir, err)
		}
		if len(entries) != 0 {
			names := make([]string, 0, len(entries))
			for _, e := range entries {
				names = append(names, e.Name())
			}
			t.Errorf("%d probe file(s) survived %d concurrent probes: %v", len(entries), goroutines, names)
		}
	})
}

// TestResolveReturnsErrorRatherThanFallingBackWhenUnwritable is the statement
// this sub-task exists to make: Resolve does not have a fallback branch.
//
// The defect being replaced downloaded into a temporary directory and, when the
// chosen one did not work, quietly used another. That is how distfiles ended up
// in a 31 GB tmpfs on a host already deep into swap. Here the unwritable
// directory is the one the host itself named, and the only acceptable outcome
// is an error naming it — not DefaultCache, not os.TempDir(), not anywhere.
func TestResolveReturnsErrorRatherThanFallingBackWhenUnwritable(t *testing.T) {
	t.Run("the host's own DISTDIR is unwritable", func(t *testing.T) {
		locked := makeUnwritableDir(t)
		stubPortageqOutput(t, locked+"\n")

		dir, err := Resolve("", "")
		t.Cleanup(dir.Cleanup)

		if err == nil {
			t.Fatalf("Resolve(\"\", \"\") error = nil for the unwritable %q; the failure this story removes was exactly a silent retreat", locked)
		}
		if !errors.Is(err, ErrDistdirNotWritable) {
			t.Errorf("errors.Is(err, ErrDistdirNotWritable) = false for %v; the pre-flight verdict must be testable by the caller", err)
		}
		assertNamesTheDirectory(t, err, locked)

		// No fallback: the reported path is the directory that failed, and
		// nothing else was chosen behind the caller's back.
		if dir.Path != locked {
			t.Errorf("Resolve Path = %q, want the failing %q — a different path here means it fell back", dir.Path, locked)
		}
		if dir.Path == DefaultCache {
			t.Error("Resolve fell back to DefaultCache after the probe failed")
		}
		tmp := os.TempDir()
		sep := string(os.PathSeparator)
		if dir.Path == tmp || strings.HasPrefix(dir.Path, strings.TrimRight(tmp, sep)+sep) {
			t.Errorf("Resolve fell back into os.TempDir() (%q): %q — that is the tmpfs this story removes", tmp, dir.Path)
		}

		// Created must stay false on the error path, so the deferred Cleanup a
		// caller writes right after Resolve cannot delete a directory this run
		// did not make.
		if dir.Created {
			t.Error("Created = true alongside an error; Cleanup would then remove a directory Resolve only failed to prepare")
		}
		dir.Cleanup()
		if _, statErr := os.Stat(locked); statErr != nil {
			t.Errorf("the unwritable directory must survive Cleanup, stat err = %v", statErr)
		}
	})

	t.Run("an explicit --distdir that is unwritable is not swapped for another", func(t *testing.T) {
		locked := makeUnwritableDir(t)
		// Stubbed with a perfectly good answer: if a fallback existed, this is
		// the path it would reach for, and the assertion would catch it.
		fallback := filepath.Join(t.TempDir(), "host-distdir")
		call := stubPortageqOutput(t, fallback+"\n")

		dir, err := Resolve(locked, "")

		if err == nil {
			t.Fatalf("Resolve(%q, \"\") error = nil for an unwritable explicit distdir", locked)
		}
		if !errors.Is(err, ErrDistdirNotWritable) {
			t.Errorf("errors.Is(err, ErrDistdirNotWritable) = false for %v", err)
		}
		if dir.Path != locked {
			t.Errorf("Resolve Path = %q, want the explicit %q", dir.Path, locked)
		}
		if call.calls != 0 {
			t.Errorf("portageq was queried %d time(s) after the explicit path failed; a failed rung must not reopen the precedence", call.calls)
		}
		if _, statErr := os.Stat(fallback); !os.IsNotExist(statErr) {
			t.Errorf("a fallback directory %q was created, stat err = %v", fallback, statErr)
		}
	})

	t.Run("a writable distdir still resolves normally", func(t *testing.T) {
		// The guard against a probe that fails everything: if the pre-flight
		// were wrong in the other direction, the sub-tests above would still
		// pass while every real run failed.
		writable := t.TempDir()
		stubPortageqOutput(t, writable+"\n")

		dir, err := Resolve("", "")
		t.Cleanup(dir.Cleanup)

		if err != nil {
			t.Fatalf("Resolve(\"\", \"\") error = %v for the writable %q", err, writable)
		}
		if dir.Path != writable {
			t.Errorf("Resolve Path = %q, want %q", dir.Path, writable)
		}
		if entries, readErr := os.ReadDir(dir.Path); readErr != nil || len(entries) != 0 {
			t.Errorf("the pre-flight left %v behind in the resolved distdir (err = %v)", entries, readErr)
		}
	})
}
