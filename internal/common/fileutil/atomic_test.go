package fileutil

// Authored for story 056, sub-task 1.1 (S056-R1.1 to S056-R1.7, S056-Q8).
//
// These tests pin the CONTRACT of WriteFileAtomic and PublishNewFile from the
// outside: what is on disk afterwards, under which mode, and what is NOT on
// disk. Nothing here asks the implementation where it put its temporary file;
// the only place a temporary name is observed is the leftover of a writer that
// was SIGKILLed mid-write, which is exactly the case the naming rule exists for.

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

const (
	// atomicChildTargetEnv switches the test binary into the writer-child role.
	atomicChildTargetEnv = "BENTOO_TEST_ATOMIC_CHILD_TARGET"
	// atomicChildSizeEnv is the payload size the child writes, in bytes.
	atomicChildSizeEnv = "BENTOO_TEST_ATOMIC_CHILD_SIZE"
)

// TestHelperAtomicWriteChild is not a test. The parent re-executes the test
// binary with -test.run selecting only this function and the env var set; the
// child then calls WriteFileAtomic ONCE with a large payload and blocks on its
// stdin, so a parent that kills it late sees a completed write rather than a
// second one.
func TestHelperAtomicWriteChild(t *testing.T) {
	target := os.Getenv(atomicChildTargetEnv)
	if target == "" {
		t.Skip("child role only; selected by the parent through " + atomicChildTargetEnv)
	}
	size, err := strconv.Atoi(os.Getenv(atomicChildSizeEnv))
	if err != nil || size <= 0 {
		fmt.Fprintf(os.Stderr, "atomic child: bad size %q: %v\n", os.Getenv(atomicChildSizeEnv), err)
		os.Exit(3)
	}
	if err := WriteFileAtomic(target, bytes.Repeat([]byte("N"), size), 0o644); err != nil {
		fmt.Fprintf(os.Stderr, "atomic child: WriteFileAtomic(%q): %v\n", target, err)
		os.Exit(4)
	}
	_, _ = io.Copy(io.Discard, os.Stdin)
	os.Exit(0)
}

// withTestUmask sets the process umask for one test and restores it. Tests in
// this package never run in parallel, which is what makes that safe.
func withTestUmask(t *testing.T, mask int) {
	t.Helper()
	old := syscall.Umask(mask)
	t.Cleanup(func() { syscall.Umask(old) })
}

// entryNames lists a directory's entries, sorted.
func entryNames(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("reading %s: %v", dir, err)
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		names = append(names, e.Name())
	}
	sort.Strings(names)
	return names
}

func assertEntries(t *testing.T, dir string, want ...string) {
	t.Helper()
	sort.Strings(want)
	if got := entryNames(t, dir); strings.Join(got, "\x00") != strings.Join(want, "\x00") {
		t.Errorf("%s holds %q, want exactly %q", dir, got, want)
	}
}

func skipIfRoot(t *testing.T) {
	t.Helper()
	if os.Geteuid() == 0 {
		t.Skip("root ignores directory permissions, so a read-only directory cannot make a write fail")
	}
}

// TestWriteFileAtomic_WritesExactBytesAndMode: R1.1. The mode is the one asked
// for, under a umask that would narrow it AND under one that would not narrow a
// wide create mode — both halves, because an implementation that relies on the
// create mode is caught by one and an implementation that forgets to chmod a
// replaced file by the other.
func TestWriteFileAtomic_WritesExactBytesAndMode(t *testing.T) {
	cases := []struct {
		name     string
		umask    int
		existing *os.FileMode // nil: target does not exist yet
		mode     os.FileMode
	}{
		{name: "new file 0644 under umask 077", umask: 0o077, mode: 0o644},
		{name: "new file 0600 under umask 000", umask: 0o000, mode: 0o600},
		{name: "replace 0600 file with 0644 under umask 077", umask: 0o077, existing: modePtr(0o600), mode: 0o644},
		{name: "replace 0644 file with 0640 under umask 000", umask: 0o000, existing: modePtr(0o644), mode: 0o640},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			withTestUmask(t, tc.umask)
			dir := t.TempDir()
			path := filepath.Join(dir, "state.json")
			if err := os.WriteFile(filepath.Join(dir, "neighbour"), []byte("n"), 0o600); err != nil {
				t.Fatal(err)
			}
			if tc.existing != nil {
				if err := os.WriteFile(path, []byte("old contents that are longer than the new ones"), 0o600); err != nil {
					t.Fatal(err)
				}
				if err := os.Chmod(path, *tc.existing); err != nil {
					t.Fatal(err)
				}
			}
			data := []byte("new\x00bytes\n")

			if err := WriteFileAtomic(path, data, tc.mode); err != nil {
				t.Fatalf("WriteFileAtomic: %v", err)
			}

			got, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("reading back: %v", err)
			}
			if !bytes.Equal(got, data) {
				t.Errorf("file holds %q, want exactly %q", got, data)
			}
			info, err := os.Lstat(path)
			if err != nil {
				t.Fatal(err)
			}
			if !info.Mode().IsRegular() {
				t.Errorf("%s is %v, want a regular file", path, info.Mode())
			}
			if info.Mode().Perm() != tc.mode {
				t.Errorf("mode is %#o, want %#o", info.Mode().Perm(), tc.mode)
			}
			assertEntries(t, dir, "neighbour", "state.json")
		})
	}
}

func modePtr(m os.FileMode) *os.FileMode { return &m }

// TestWriteFileAtomic_ReplacesTheInodeRatherThanTruncatingIt: R1.1/R1.3. A hard
// link to the old file, kept outside the target's directory, still holds the OLD
// bytes afterwards. An in-place truncate-and-write changes the shared inode and
// the probe with it; only a new file renamed over the name leaves it alone.
func TestWriteFileAtomic_ReplacesTheInodeRatherThanTruncatingIt(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "target")
	probeDir := filepath.Join(root, "probe")
	for _, d := range []string{dir, probeDir} {
		if err := os.Mkdir(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	path := filepath.Join(dir, "x.ebuild")
	old := []byte("EAPI=8\nOLD=1\n")
	if err := os.WriteFile(path, old, 0o644); err != nil {
		t.Fatal(err)
	}
	probe := filepath.Join(probeDir, "old-inode")
	if err := os.Link(path, probe); err != nil {
		t.Skipf("hard links unsupported here: %v", err)
	}

	if err := WriteFileAtomic(path, []byte("EAPI=8\nNEW=1\n"), 0o644); err != nil {
		t.Fatalf("WriteFileAtomic: %v", err)
	}

	if got, _ := os.ReadFile(probe); !bytes.Equal(got, old) {
		t.Errorf("a hard link to the previous file now reads %q: the target was rewritten in place, not replaced", got)
	}
	if got, _ := os.ReadFile(path); string(got) != "EAPI=8\nNEW=1\n" {
		t.Errorf("target reads %q after the write", got)
	}
	assertEntries(t, dir, "x.ebuild")
}

// TestWriteFileAtomic_FailureBeforeRenameLeavesTargetAndNoTemp: R1.4.
func TestWriteFileAtomic_FailureBeforeRenameLeavesTargetAndNoTemp(t *testing.T) {
	t.Run("directory not writable", func(t *testing.T) {
		skipIfRoot(t)
		dir := t.TempDir()
		path := filepath.Join(dir, "cache.json")
		old := []byte(`{"entries":{"x/a":1}}`)
		if err := os.WriteFile(path, old, 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(dir, 0o555); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.Chmod(dir, 0o755) })

		err := WriteFileAtomic(path, []byte("replacement"), 0o600)
		if err == nil {
			t.Fatal("WriteFileAtomic succeeded in a read-only directory")
		}
		if !errors.Is(err, fs.ErrPermission) {
			t.Errorf("error %q does not wrap the permission failure that caused it", err)
		}
		if !strings.Contains(err.Error(), path) {
			t.Errorf("error %q does not name %s", err, path)
		}
		if got, _ := os.ReadFile(path); !bytes.Equal(got, old) {
			t.Errorf("target changed to %q after a failed write", got)
		}
		assertEntries(t, dir, "cache.json")
	})

	t.Run("parent directory missing", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "gone", "cache.json")
		err := WriteFileAtomic(path, []byte("x"), 0o600)
		if err == nil {
			t.Fatal("WriteFileAtomic succeeded into a directory that does not exist")
		}
		if !errors.Is(err, fs.ErrNotExist) {
			t.Errorf("error %q does not wrap the missing-directory cause", err)
		}
		if !strings.Contains(err.Error(), path) {
			t.Errorf("error %q does not name %s", err, path)
		}
	})
}

// killAtomicWriterMidWrite starts a child that writes a payload of size bytes
// to target through WriteFileAtomic, waits until the child's temporary file
// appears (a file named .<base>.bentoo-<childpid>-* in target's directory), and
// SIGKILLs it. It returns the child's PID and the leftover's name, or
// conclusive=false when the child had already finished its rename by the time
// the kill landed (target then holds the complete new payload).
func killAtomicWriterMidWrite(t *testing.T, target string, size int) (pid int, leftover string, conclusive bool) {
	t.Helper()
	dir, base := filepath.Dir(target), filepath.Base(target)

	cmd := exec.Command(os.Args[0], "-test.run=^TestHelperAtomicWriteChild$")
	cmd.Env = append(os.Environ(),
		atomicChildTargetEnv+"="+target,
		atomicChildSizeEnv+"="+strconv.Itoa(size),
	)
	cmd.Stderr = os.Stderr
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatalf("stdin pipe: %v", err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatalf("starting the writer child: %v", err)
	}
	pid = cmd.Process.Pid
	reaped := false
	t.Cleanup(func() {
		if !reaped {
			_ = stdin.Close()
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
		}
	})

	tempName := regexp.MustCompile(`^` + regexp.QuoteMeta("."+base+".bentoo-"+strconv.Itoa(pid)+"-") + `[^/]+$`)
	deadline := time.Now().Add(30 * time.Second)
	for {
		for _, name := range entryNames(t, dir) {
			if tempName.MatchString(name) {
				leftover = name
			}
		}
		if leftover != "" {
			break
		}
		if cmd.ProcessState != nil || time.Now().After(deadline) {
			t.Fatalf("the writer child (pid %d) never showed a temporary file named .%s.bentoo-%d-* in %s; the directory holds %q",
				pid, base, pid, dir, entryNames(t, dir))
		}
		// The kill is sent only once the temporary file is seen.
		time.Sleep(200 * time.Microsecond) // polling: the writer child's temporary file has appeared
	}

	if err := cmd.Process.Signal(syscall.SIGKILL); err != nil {
		t.Fatalf("killing the writer child: %v", err)
	}
	_ = cmd.Wait()
	reaped = true

	if _, err := os.Lstat(filepath.Join(dir, leftover)); errors.Is(err, fs.ErrNotExist) {
		return pid, "", false
	}
	return pid, leftover, true
}

// TestWriteFileAtomic_KilledWriterLeavesPreviousBytes: R1.5 and the first half
// of Q8, plus R1.2's "a leftover names the process that wrote it". A kill that
// lands after the rename is inconclusive rather than a failure, and the payload
// grows until a kill lands inside the write; a target holding anything other
// than the old bytes or the whole new payload is a failure at once.
func TestWriteFileAtomic_KilledWriterLeavesPreviousBytes(t *testing.T) {
	size := 64 << 20
	for attempt := 1; attempt <= 4; attempt, size = attempt+1, size*2 {
		dir := t.TempDir()
		target := filepath.Join(dir, "packages.toml")
		old := []byte("[\"x/a\"]\nurl = \"https://example.invalid\"\n")
		if err := os.WriteFile(target, old, 0o644); err != nil {
			t.Fatal(err)
		}

		pid, leftover, conclusive := killAtomicWriterMidWrite(t, target, size)

		got, err := os.ReadFile(target)
		if err != nil {
			t.Fatalf("attempt %d: the target is gone after the writer (pid %d) was killed: %v", attempt, pid, err)
		}
		if !conclusive {
			if len(got) != size || !bytes.Equal(got, bytes.Repeat([]byte("N"), size)) {
				t.Fatalf("attempt %d: the kill landed after the temp vanished, yet the target holds %d bytes that are neither the old file nor the complete new one", attempt, len(got))
			}
			t.Logf("attempt %d: kill landed after the rename; retrying with a larger payload", attempt)
			continue
		}
		if !bytes.Equal(got, old) {
			t.Fatalf("attempt %d: writer pid %d was SIGKILLed mid-write and the target now holds %d bytes, not its previous %d",
				attempt, pid, len(got), len(old))
		}
		info, err := os.Lstat(filepath.Join(dir, leftover))
		if err != nil || !info.Mode().IsRegular() {
			t.Fatalf("attempt %d: leftover %s is not a regular file in the target's directory (%v)", attempt, leftover, err)
		}
		return
	}
	t.Fatal("no kill landed inside the write in 4 attempts; the payload is too small for this machine")
}

// TestPublishNewFile_FreshDestination: R1.6 — a destination that does not
// exist is published with the exact bytes and mode, and the temporary file is
// unlinked (the published file has a single link, and no other entry appears).
func TestPublishNewFile_FreshDestination(t *testing.T) {
	for _, tc := range []struct {
		umask int
		mode  os.FileMode
	}{{0o077, 0o644}, {0o000, 0o600}} {
		t.Run(fmt.Sprintf("mode %#o under umask %#o", tc.mode, tc.umask), func(t *testing.T) {
			withTestUmask(t, tc.umask)
			dir := t.TempDir()
			path := filepath.Join(dir, "hello-1.1.ebuild")
			data := []byte("EAPI=8\n")

			if err := PublishNewFile(path, data, tc.mode); err != nil {
				t.Fatalf("PublishNewFile onto a fresh destination: %v", err)
			}
			got, err := os.ReadFile(path)
			if err != nil || !bytes.Equal(got, data) {
				t.Fatalf("destination holds %q (%v), want %q", got, err, data)
			}
			info, err := os.Lstat(path)
			if err != nil {
				t.Fatal(err)
			}
			if info.Mode().Perm() != tc.mode || !info.Mode().IsRegular() {
				t.Errorf("destination is %v, want a regular file at %#o", info.Mode(), tc.mode)
			}
			if st, ok := info.Sys().(*syscall.Stat_t); ok && st.Nlink != 1 {
				t.Errorf("destination has %d links: the temporary name was not unlinked", st.Nlink)
			}
			assertEntries(t, dir, "hello-1.1.ebuild")
		})
	}
}

// TestPublishNewFile_RefusesAnyExistingEntry: R1.7 — the hostile half of R1.6.
// A dangling symlink is the case a Stat-based existence check cannot see (Stat
// follows it and reports ENOENT); only a link-based publish refuses it.
func TestPublishNewFile_RefusesAnyExistingEntry(t *testing.T) {
	cases := []struct {
		name  string
		plant func(t *testing.T, path, outside string)
		check func(t *testing.T, path, outside string)
	}{
		{
			name: "regular file",
			plant: func(t *testing.T, path, _ string) {
				if err := os.WriteFile(path, []byte("SLOT=6\n"), 0o640); err != nil {
					t.Fatal(err)
				}
			},
			check: func(t *testing.T, path, _ string) {
				got, _ := os.ReadFile(path)
				info, _ := os.Lstat(path)
				if string(got) != "SLOT=6\n" || info.Mode().Perm() != 0o640 {
					t.Errorf("the existing file became %q at %v", got, info.Mode())
				}
			},
		},
		{
			name: "dangling symlink",
			plant: func(t *testing.T, path, outside string) {
				if err := os.Symlink(outside, path); err != nil {
					t.Fatal(err)
				}
			},
			check: func(t *testing.T, path, outside string) {
				if target, err := os.Readlink(path); err != nil || target != outside {
					t.Errorf("the symlink became %q (%v), want it still pointing at %s", target, err, outside)
				}
				if _, err := os.Lstat(outside); !errors.Is(err, fs.ErrNotExist) {
					t.Errorf("the symlink's target %s was created: the publish followed the link", outside)
				}
			},
		},
		{
			name: "directory",
			plant: func(t *testing.T, path, _ string) {
				if err := os.Mkdir(path, 0o755); err != nil {
					t.Fatal(err)
				}
			},
			check: func(t *testing.T, path, _ string) {
				if info, err := os.Lstat(path); err != nil || !info.IsDir() {
					t.Errorf("the directory at %s is gone (%v)", path, err)
				}
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			outside := filepath.Join(t.TempDir(), "victim")
			path := filepath.Join(dir, "hello-1.1.ebuild")
			tc.plant(t, path, outside)

			err := PublishNewFile(path, []byte("EAPI=8\n"), 0o644)
			if !errors.Is(err, fs.ErrExist) {
				t.Fatalf("PublishNewFile over an existing %s returned %v, want an error wrapping fs.ErrExist", tc.name, err)
			}
			if !strings.Contains(err.Error(), path) {
				t.Errorf("error %q does not name %s", err, path)
			}
			tc.check(t, path, outside)
			assertEntries(t, dir, "hello-1.1.ebuild")
		})
	}
}
