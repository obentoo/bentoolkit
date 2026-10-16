package sysmon_test

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"github.com/obentoo/bentoolkit/internal/sysmon"
)

// fixtureRoot returns the absolute path of a testdata root. Every reader test
// runs under one of these or a t.TempDir(): none reads the host /proc (Q16).
func fixtureRoot(t *testing.T, name string) string {
	t.Helper()
	abs, err := filepath.Abs(filepath.Join("testdata", name))
	if err != nil {
		t.Fatalf("abs testdata/%s: %v", name, err)
	}
	return abs
}

// TestReaderMemory_ReadsUnderRoot: the figures are the fixture's, which no
// real host has, so a reader that ignored Root and read the host fails here.
func TestReaderMemory_ReadsUnderRoot(t *testing.T) {
	r := sysmon.Reader{Root: fixtureRoot(t, "root")}
	m, err := r.Memory()
	if err != nil {
		t.Fatalf("Memory() error = %v", err)
	}
	want := sysmon.Memory{Total: 4194304 * kib, Available: 314159 * kib, SwapTotal: 1048576 * kib, SwapFree: 65536 * kib}
	if m != want {
		t.Errorf("Memory() = %+v, want %+v", m, want)
	}
}

// TestReaderMemory_MissingFileErrorNamesPath is R1.3/Q4: an unreadable
// meminfo is an error that keeps its cause and names the path.
func TestReaderMemory_MissingFileErrorNamesPath(t *testing.T) {
	r := sysmon.Reader{Root: t.TempDir()}
	_, err := r.Memory()
	if err == nil {
		t.Fatal("Memory() under a root without proc/meminfo returned nil error")
	}
	if !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("Memory() error %v does not wrap fs.ErrNotExist", err)
	}
	if !strings.Contains(err.Error(), filepath.Join("proc", "meminfo")) {
		t.Errorf("Memory() error %q does not name the path read", err)
	}
}

// TestReaderMemory_MalformedFileIsError: a meminfo without MemAvailable is an
// error from the reader too, not a zero reading.
func TestReaderMemory_MalformedFileIsError(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "proc", "meminfo"), "MemTotal: 1000 kB\nMemFree: 10 kB\n")
	if m, err := (sysmon.Reader{Root: root}).Memory(); err == nil {
		t.Errorf("Memory() = %+v, nil; want an error for a meminfo without MemAvailable", m)
	}
}

// TestReaderPressure_ResourcesNeverCross: cpu and memory read their own file;
// the fixture gives each distinct figures.
func TestReaderPressure_ResourcesNeverCross(t *testing.T) {
	r := sysmon.Reader{Root: fixtureRoot(t, "root")}
	cases := []struct {
		resource string
		want     sysmon.Pressure
	}{
		{"cpu", sysmon.Pressure{SomeAvg60: 81.50, FullAvg60: 0, HasFull: true}},
		{"memory", sysmon.Pressure{SomeAvg60: 12.25, FullAvg60: 10.75, HasFull: true}},
	}
	for _, tc := range cases {
		t.Run(tc.resource, func(t *testing.T) {
			p, err := r.Pressure(tc.resource)
			if err != nil {
				t.Fatalf("Pressure(%q) error = %v", tc.resource, err)
			}
			if p != tc.want {
				t.Errorf("Pressure(%q) = %+v, want %+v", tc.resource, p, tc.want)
			}
		})
	}
}

// TestReaderPressure_UnknownResourceRefused: only cpu and memory are
// readable. The fixture root carries an io file, so a reader that accepted any
// name would succeed here, and a path-shaped name must not escape the
// pressure directory.
func TestReaderPressure_UnknownResourceRefused(t *testing.T) {
	r := sysmon.Reader{Root: fixtureRoot(t, "root")}
	for _, res := range []string{"io", "", "CPU", "../meminfo", "cpu/../memory"} {
		t.Run(res, func(t *testing.T) {
			if p, err := r.Pressure(res); err == nil {
				t.Errorf("Pressure(%q) = %+v, nil; want an error for an unsupported resource", res, p)
			}
		})
	}
}

// TestReaderPressure_AbsentIsErrNoPressure is R1.5/R3.4: a kernel without PSI
// yields ErrNoPressure, still carrying fs.ErrNotExist.
func TestReaderPressure_AbsentIsErrNoPressure(t *testing.T) {
	r := sysmon.Reader{Root: fixtureRoot(t, "nopsi")}
	for _, res := range []string{"cpu", "memory"} {
		t.Run(res, func(t *testing.T) {
			_, err := r.Pressure(res)
			if err == nil {
				t.Fatalf("Pressure(%q) without /proc/pressure returned nil error", res)
			}
			if !errors.Is(err, sysmon.ErrNoPressure) {
				t.Errorf("Pressure(%q) error %v is not ErrNoPressure", res, err)
			}
			if !errors.Is(err, fs.ErrNotExist) {
				t.Errorf("Pressure(%q) error %v does not wrap fs.ErrNotExist", res, err)
			}
		})
	}
}

// TestReaderPressure_MalformedIsNotErrNoPressure is the hostile half of
// ErrNoPressure: a pressure file that exists but does not parse is a reading
// failure, not "this kernel has no PSI" (which would switch the CPU monitor
// off for the whole run).
func TestReaderPressure_MalformedIsNotErrNoPressure(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "proc", "pressure", "cpu"), "garbage\n")
	_, err := (sysmon.Reader{Root: root}).Pressure("cpu")
	if err == nil {
		t.Fatal("Pressure(cpu) on a malformed file returned nil error")
	}
	if errors.Is(err, sysmon.ErrNoPressure) {
		t.Errorf("Pressure(cpu) on a present but malformed file = %v; it collapsed into ErrNoPressure", err)
	}
	if !strings.Contains(err.Error(), filepath.Join("pressure", "cpu")) {
		t.Errorf("Pressure(cpu) error %q does not name the path read", err)
	}
}

// statfsFake answers statfs for the paths in vals and fails for any other.
func statfsFake(vals map[string]syscall.Statfs_t) func(string, *syscall.Statfs_t) error {
	return func(path string, st *syscall.Statfs_t) error {
		v, ok := vals[filepath.Clean(path)]
		if !ok {
			return syscall.ENOENT
		}
		*st = v
		return nil
	}
}

func statDev(t *testing.T, path string) uint64 {
	t.Helper()
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat %s: %v", path, err)
	}
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		t.Fatalf("stat %s: no syscall.Stat_t", path)
	}
	return st.Dev
}

// TestReaderDisk_FreeIsBavailNotBfree is R2.6/R2.7: free space is what an
// unprivileged user can write (Bavail), never Bfree, which counts the blocks
// reserved for root. Size is Blocks * Bsize.
func TestReaderDisk_FreeIsBavailNotBfree(t *testing.T) {
	dir := t.TempDir()
	r := sysmon.Reader{Root: "/", Statfs: statfsFake(map[string]syscall.Statfs_t{
		dir: {Bsize: 4096, Blocks: 1000, Bfree: 500, Bavail: 300},
	})}
	d, err := r.Disk(dir)
	if err != nil {
		t.Fatalf("Disk(%s) error = %v", dir, err)
	}
	if d.Free != 300*4096 {
		t.Errorf("Disk.Free = %d, want %d (Bavail*Bsize); %d would be Bfree", d.Free, 300*4096, 500*4096)
	}
	if d.Size != 1000*4096 {
		t.Errorf("Disk.Size = %d, want %d (Blocks*Bsize)", d.Size, 1000*4096)
	}
	if d.Path != dir {
		t.Errorf("Disk.Path = %q, want the watched path %q", d.Path, dir)
	}
}

// TestReaderDisk_DevIsStDevNotFsid is R2.3's dedup key, in both converse
// directions. The fake statfs reports the SAME Fsid (zero, as several
// filesystems do) for two paths that may live on different devices, and
// DIFFERENT Fsids for two paths in one directory. Dev must follow st_dev in
// both cases: equal for the same filesystem, the path's own st_dev otherwise.
func TestReaderDisk_DevIsStDevNotFsid(t *testing.T) {
	tmp := t.TempDir()
	a := filepath.Join(tmp, "a")
	b := filepath.Join(tmp, "b")
	for _, p := range []string{a, b} {
		if err := os.Mkdir(p, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	pkg, err := filepath.Abs(".")
	if err != nil {
		t.Fatal(err)
	}
	same := syscall.Statfs_t{Bsize: 4096, Blocks: 10, Bavail: 5}
	split := func(x int32) syscall.Statfs_t {
		s := same
		s.Fsid.X__val = [2]int32{x, x}
		return s
	}
	r := sysmon.Reader{Root: "/", Statfs: statfsFake(map[string]syscall.Statfs_t{
		a:   split(1),
		b:   split(2),
		tmp: same,
		pkg: same,
	})}

	disk := func(p string) sysmon.Disk {
		t.Helper()
		d, err := r.Disk(p)
		if err != nil {
			t.Fatalf("Disk(%s) error = %v", p, err)
		}
		return d
	}

	// Wrongly split: one filesystem, different Fsids.
	da, db := disk(a), disk(b)
	if da.Dev != db.Dev {
		t.Errorf("Disk(a).Dev = %d, Disk(b).Dev = %d; two directories of one filesystem split apart", da.Dev, db.Dev)
	}
	// Wrongly collapsed: equal Fsids, each path's own st_dev.
	for _, p := range []string{a, tmp, pkg} {
		if got, want := disk(p).Dev, statDev(t, p); got != want {
			t.Errorf("Disk(%s).Dev = %d, want st_dev %d", p, got, want)
		}
	}
	if statDev(t, tmp) != statDev(t, pkg) && disk(tmp).Dev == disk(pkg).Dev {
		t.Errorf("Disk.Dev collapsed two filesystems (%s, %s) that share an Fsid", tmp, pkg)
	}
}

// TestReaderDisk_Errors is R1.3/R2.5/Q4: a missing path, a statfs failure and
// a non-positive block size are errors naming the path; a statfs failure keeps
// its cause.
func TestReaderDisk_Errors(t *testing.T) {
	dir := t.TempDir()
	missing := filepath.Join(dir, "does-not-exist")

	t.Run("missing path", func(t *testing.T) {
		r := sysmon.Reader{Root: "/", Statfs: statfsFake(map[string]syscall.Statfs_t{
			missing: {Bsize: 4096, Blocks: 10, Bavail: 5},
		})}
		_, err := r.Disk(missing)
		if err == nil {
			t.Fatal("Disk(missing path) returned nil error")
		}
		if !strings.Contains(err.Error(), missing) {
			t.Errorf("error %q does not name the path", err)
		}
	})
	t.Run("statfs fails", func(t *testing.T) {
		r := sysmon.Reader{Root: "/", Statfs: func(string, *syscall.Statfs_t) error { return syscall.EIO }}
		_, err := r.Disk(dir)
		if !errors.Is(err, syscall.EIO) {
			t.Fatalf("Disk() error = %v, want one wrapping EIO", err)
		}
		if !strings.Contains(err.Error(), dir) {
			t.Errorf("error %q does not name the path", err)
		}
	})
	for _, bsize := range []int64{0, -4096} {
		t.Run("bsize", func(t *testing.T) {
			r := sysmon.Reader{Root: "/", Statfs: statfsFake(map[string]syscall.Statfs_t{
				dir: {Bsize: bsize, Blocks: 10, Bavail: 5},
			})}
			if d, err := r.Disk(dir); err == nil {
				t.Errorf("Disk() with Bsize %d = %+v, nil; want an error", bsize, d)
			}
		})
	}
}

// TestReaderDisk_NilStatfsUsesTheRealOne: a Reader without an injected Statfs
// queries the real filesystem, rather than reporting nothing. Only a temp
// directory is touched.
func TestReaderDisk_NilStatfsUsesTheRealOne(t *testing.T) {
	dir := t.TempDir()
	d, err := (sysmon.Reader{Root: "/"}).Disk(dir)
	if err != nil {
		t.Fatalf("Disk(%s) with the default statfs error = %v", dir, err)
	}
	if d.Size == 0 || d.Free > d.Size {
		t.Errorf("Disk(%s) = %+v; want a non-zero size and free <= size", dir, d)
	}
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

// proc is one fake /proc/<name> entry.
type proc struct {
	name    string // pid directory name
	cmdline string // NUL-separated argv; "" writes an empty file
	comm    string // "" writes no comm file
	noCmd   bool   // no cmdline file at all (the process exited mid-scan)
	cmdDir  bool   // cmdline is a directory, so reading it fails
}

func procRoot(t *testing.T, procs ...proc) string {
	t.Helper()
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "proc"), 0o700); err != nil {
		t.Fatal(err)
	}
	for _, p := range procs {
		dir := filepath.Join(root, "proc", p.name)
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		switch {
		case p.cmdDir:
			if err := os.Mkdir(filepath.Join(dir, "cmdline"), 0o700); err != nil {
				t.Fatal(err)
			}
		case !p.noCmd:
			writeFile(t, filepath.Join(dir, "cmdline"), p.cmdline)
		}
		if p.comm != "" {
			writeFile(t, filepath.Join(dir, "comm"), p.comm+"\n")
		}
	}
	writeFile(t, filepath.Join(root, "proc", "meminfo"), "MemTotal: 1 kB\nMemAvailable: 1 kB\n")
	return root
}

// TestReaderEmergeRunning is R4.3. On Gentoo emerge is a python-exec script,
// so comm reads python3 and the cmdline is what tells. Matches: argv[0] base
// emerge, or a python interpreter given an emerge script. Every non-match is
// a near miss that a looser rule would wrongly collapse into "emerge".
func TestReaderEmergeRunning(t *testing.T) {
	bg := proc{name: "1", cmdline: "/sbin/init\x00", comm: "init"}
	cases := []struct {
		name string
		p    proc
		want bool
	}{
		// Hostile: must NOT match.
		{"emerge-webrsync", proc{name: "200", cmdline: "/usr/bin/emerge-webrsync\x00", comm: "emerge-webrsync"}, false},
		{"python emerge-webrsync", proc{name: "201", cmdline: "/usr/bin/python3.14\x00/usr/bin/emerge-webrsync\x00", comm: "python3.14"}, false},
		{"emergency prefix", proc{name: "202", cmdline: "/usr/bin/emergency\x00", comm: "emergency"}, false},
		{"grep for emerge", proc{name: "203", cmdline: "grep\x00emerge\x00", comm: "grep"}, false},
		{"pager on the script", proc{name: "204", cmdline: "less\x00/usr/bin/emerge\x00", comm: "less"}, false},
		{"python -c mentioning emerge", proc{name: "205", cmdline: "python3\x00-c\x00import emerge\x00", comm: "python3"}, false},
		{"comm says emerge, cmdline does not", proc{name: "206", cmdline: "/usr/bin/sleep\x0060\x00", comm: "emerge"}, false},
		{"kernel thread, empty cmdline", proc{name: "2", cmdline: "", comm: "kthreadd"}, false},
		{"emerge as a later argument", proc{name: "207", cmdline: "/usr/bin/python3\x00/usr/bin/eix\x00emerge\x00", comm: "python3"}, false},
		// Hostile: must match although nothing says "emerge" in comm.
		{"python-exec wrapper", proc{name: "300", cmdline: "/usr/bin/python3.14\x00/usr/lib/python-exec/python3.14/emerge\x00--ask\x00@world\x00", comm: "python3.14"}, true},
		{"bare python3 any script dir", proc{name: "301", cmdline: "python3\x00/tmp/x/emerge\x00", comm: "python3"}, true},
		{"python without version", proc{name: "302", cmdline: "/usr/bin/python\x00/usr/bin/emerge\x00-uDN\x00", comm: "python"}, true},
		{"argv0 is the script path", proc{name: "303", cmdline: "/usr/lib/python-exec/python3.14/emerge\x00-1\x00foo\x00", comm: "python3.14"}, true},
		// Benign.
		{"plain emerge", proc{name: "400", cmdline: "/usr/bin/emerge\x00-uDN\x00@world\x00", comm: "emerge"}, true},
		{"bare emerge", proc{name: "401", cmdline: "emerge\x00--sync\x00", comm: "emerge"}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := sysmon.Reader{Root: procRoot(t, bg, tc.p)}
			got, err := r.EmergeRunning()
			if err != nil {
				t.Fatalf("EmergeRunning() error = %v", err)
			}
			if got != tc.want {
				t.Errorf("EmergeRunning() with cmdline %q = %v, want %v", tc.p.cmdline, got, tc.want)
			}
		})
	}
}

// TestReaderEmergeRunning_SkipsPerPidFailures: a process that exits mid-scan
// (no cmdline), an unreadable cmdline and a non-pid entry are skipped; the
// emerge process after them is still found, with no error.
func TestReaderEmergeRunning_SkipsPerPidFailures(t *testing.T) {
	root := procRoot(t,
		proc{name: "10", noCmd: true},
		proc{name: "11", cmdDir: true},
		proc{name: "12", cmdline: "/bin/sh\x00", comm: "sh"},
		proc{name: "99", cmdline: "/usr/bin/python3.14\x00/usr/lib/python-exec/python3.14/emerge\x00", comm: "python3.14"},
	)
	got, err := (sysmon.Reader{Root: root}).EmergeRunning()
	if err != nil {
		t.Fatalf("EmergeRunning() error = %v; per-pid failures must be skipped", err)
	}
	if !got {
		t.Error("EmergeRunning() = false; the emerge process after the vanished ones was missed")
	}
}

// TestReaderEmergeRunning_IgnoresNonPidEntries: only numeric entries are
// processes. A non-numeric directory carrying an emerge cmdline is not one.
func TestReaderEmergeRunning_IgnoresNonPidEntries(t *testing.T) {
	root := procRoot(t,
		proc{name: "net", cmdline: "/usr/bin/emerge\x00", comm: "emerge"},
		proc{name: "12abc", cmdline: "/usr/bin/emerge\x00", comm: "emerge"},
		proc{name: "12", cmdline: "/bin/sh\x00", comm: "sh"},
	)
	got, err := (sysmon.Reader{Root: root}).EmergeRunning()
	if err != nil {
		t.Fatalf("EmergeRunning() error = %v", err)
	}
	if got {
		t.Error("EmergeRunning() = true from a non-pid /proc entry")
	}
}

// TestReaderEmergeRunning_NoProcDirIsError: failing to list proc itself is an
// error naming the path, not "no emerge running".
func TestReaderEmergeRunning_NoProcDirIsError(t *testing.T) {
	root := t.TempDir()
	got, err := (sysmon.Reader{Root: root}).EmergeRunning()
	if err == nil {
		t.Fatalf("EmergeRunning() without a proc dir = %v, nil; want an error", got)
	}
	if !strings.Contains(err.Error(), "proc") {
		t.Errorf("error %q does not name the path listed", err)
	}
}
