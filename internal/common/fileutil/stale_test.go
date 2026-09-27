package fileutil

// Authored for story 056, sub-task 1.3 (S056-R6.1, S056-R6.2, second half of
// S056-Q8). Shares killAtomicWriterMidWrite, entryNames and assertEntries with
// atomic_test.go (sub-task 1.1), so this file is materialised with or after it.

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// deadPID returns the PID of a child that has already exited and been reaped.
// The kernel answers ESRCH for it; PID reuse inside one test is not a concern.
func deadPID(t *testing.T) int {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run=^$")
	if err := cmd.Run(); err != nil {
		t.Fatalf("running a short-lived child: %v", err)
	}
	return cmd.ProcessState.Pid()
}

func plantFile(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
}

// TestRemoveStaleTemps_RemovesOnlyDeadWritersTemps: R6.1 and its hostile half
// R6.2. Every KEEP row is a name that some rule could wrongly sweep: a live PID,
// a PID that answers EPERM, a name missing the leading dot or the base, the
// overlay lock itself, writeThenRename's and realign's temps (not migrated, so
// not swept), a directory and a symlink carrying a dead PID, and anything under
// .git.
func TestRemoveStaleTemps_RemovesOnlyDeadWritersTemps(t *testing.T) {
	dead := strconv.Itoa(deadPID(t))
	self := strconv.Itoa(os.Getpid())

	for _, recursive := range []bool{true, false} {
		t.Run("recursive="+strconv.FormatBool(recursive), func(t *testing.T) {
			root := t.TempDir()
			remove := []string{
				".hello-1.0.ebuild.bentoo-" + dead + "-123456",
				".cache.json.bentoo-" + dead + "-abc",
			}
			nested := filepath.Join("app-misc", "hello", ".hello-1.1.ebuild.bentoo-"+dead+"-987")
			keep := []string{
				".hello-1.0.ebuild.bentoo-" + self + "-live",
				".pending.json.bentoo-1-eperm",
				"hello-1.0.ebuild.bentoo-" + dead + "-nodot",
				".bentoo-" + dead + "-nobase",
				".autoupdate.bentoo-lock",
				".state.bentoo-lock",
				".hello-1.0.ebuild.bentoo-2984756123",
				".hello-1.0.ebuild.bentoo-realign-" + dead + "77",
				filepath.Join(".git", ".index.bentoo-"+dead+"-x"),
				"hello-1.0.ebuild",
			}
			for _, name := range append(append([]string{}, remove...), keep...) {
				plantFile(t, filepath.Join(root, name))
			}
			plantFile(t, filepath.Join(root, nested))
			if err := os.Mkdir(filepath.Join(root, ".dir.bentoo-"+dead+"-d"), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(filepath.Join(root, "hello-1.0.ebuild"), filepath.Join(root, ".link.bentoo-"+dead+"-s")); err != nil {
				t.Fatal(err)
			}

			removed, err := RemoveStaleTemps(root, recursive)
			if err != nil {
				t.Fatalf("RemoveStaleTemps: %v", err)
			}

			want := []string{}
			for _, name := range remove {
				want = append(want, filepath.Join(root, name))
			}
			if recursive {
				want = append(want, filepath.Join(root, nested))
			} else {
				keep = append(keep, nested)
			}
			sort.Strings(want)
			sort.Strings(removed)
			if strings.Join(removed, "\n") != strings.Join(want, "\n") {
				t.Errorf("RemoveStaleTemps reported\n  %q\nwant\n  %q", removed, want)
			}
			for _, p := range want {
				if _, err := os.Lstat(p); err == nil {
					t.Errorf("%s (dead writer %s) is still on disk", p, dead)
				}
			}
			keep = append(keep, ".dir.bentoo-"+dead+"-d", ".link.bentoo-"+dead+"-s")
			for _, name := range keep {
				if _, err := os.Lstat(filepath.Join(root, name)); err != nil {
					t.Errorf("%s must be left in place, but it is gone: %v", name, err)
				}
			}
		})
	}
}

// TestRemoveStaleTemps_RemovesKilledWritersLeftover: Q8's second half. The
// leftover of a WriteFileAtomic child that was SIGKILLed mid-write is swept by
// the next run, and the target keeps its previous bytes throughout.
func TestRemoveStaleTemps_RemovesKilledWritersLeftover(t *testing.T) {
	size := 64 << 20
	for attempt := 1; attempt <= 4; attempt, size = attempt+1, size*2 {
		dir := t.TempDir()
		target := filepath.Join(dir, "cache.json")
		old := []byte(`{"entries":{}}`)
		if err := os.WriteFile(target, old, 0o600); err != nil {
			t.Fatal(err)
		}
		pid, leftover, conclusive := killAtomicWriterMidWrite(t, target, size)
		if !conclusive {
			continue
		}

		removed, err := RemoveStaleTemps(dir, false)
		if err != nil {
			t.Fatalf("RemoveStaleTemps: %v", err)
		}
		if len(removed) != 1 || removed[0] != filepath.Join(dir, leftover) {
			t.Errorf("RemoveStaleTemps removed %q, want exactly the leftover of killed pid %d (%s)", removed, pid, leftover)
		}
		assertEntries(t, dir, "cache.json")
		if got, _ := os.ReadFile(target); !bytes.Equal(got, old) {
			t.Errorf("target holds %d bytes after the sweep, want its previous %d", len(got), len(old))
		}
		return
	}
	t.Fatal("no kill landed inside the write in 4 attempts")
}
