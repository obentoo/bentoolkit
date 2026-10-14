package sysmon

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"syscall"
)

// ErrNoPressure reports that the kernel exposes no pressure stall information
// file for a resource. The error returned with it also wraps the underlying
// fs.ErrNotExist; a pressure file that exists but fails to read or parse is a
// different error.
var ErrNoPressure = errors.New("pressure stall information unavailable")

// Reader reads host figures under Root ("/" in production, a testdata tree in
// tests). Statfs defaults to syscall.Statfs when nil; tests inject a fake.
type Reader struct {
	Root   string
	Statfs func(path string, st *syscall.Statfs_t) error
}

// Disk is the space of the filesystem holding a watched path.
type Disk struct {
	Path       string // the watched path as configured
	Dev        uint64 // st_dev of Path, the key that tells filesystems apart
	Size, Free uint64 // bytes; Free = Bavail * Bsize
}

// Memory reads and parses Root/proc/meminfo.
func (r Reader) Memory() (Memory, error) {
	path := filepath.Join(r.Root, "proc", "meminfo")
	b, err := os.ReadFile(path) //nolint:gosec // G304: a fixed name under the reader's root
	if err != nil {
		return Memory{}, fmt.Errorf("reading %s: %w", path, err)
	}
	m, err := ParseMeminfo(b)
	if err != nil {
		return Memory{}, fmt.Errorf("reading %s: %w", path, err)
	}
	return m, nil
}

// Pressure reads and parses Root/proc/pressure/<resource>. Only "cpu" and
// "memory" are accepted, so no other name can reach the filesystem. An absent
// file yields an error wrapping both ErrNoPressure and its cause.
func (r Reader) Pressure(resource string) (Pressure, error) {
	if resource != "cpu" && resource != "memory" {
		return Pressure{}, fmt.Errorf("pressure resource %q is not cpu or memory", resource)
	}
	path := filepath.Join(r.Root, "proc", "pressure", resource)
	b, err := os.ReadFile(path) //nolint:gosec // G304: resource is cpu or memory, refused above otherwise
	if errors.Is(err, fs.ErrNotExist) {
		return Pressure{}, fmt.Errorf("%w: reading %s: %w", ErrNoPressure, path, err)
	}
	if err != nil {
		return Pressure{}, fmt.Errorf("reading %s: %w", path, err)
	}
	p, err := ParsePressure(b)
	if err != nil {
		return Pressure{}, fmt.Errorf("reading %s: %w", path, err)
	}
	return p, nil
}

// Disk reports the size and the unprivileged free space of the filesystem
// holding path. Free counts Bavail, not Bfree: the reserved blocks Bfree
// includes belong to root. Dev is the path's st_dev rather than the statfs
// Fsid, which several filesystems report as zero. A block size that is not
// positive is an error rather than a nonsense figure.
func (r Reader) Disk(path string) (Disk, error) {
	fi, err := os.Stat(path)
	if err != nil {
		return Disk{}, fmt.Errorf("checking disk %s: %w", path, err)
	}
	stat, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		return Disk{}, fmt.Errorf("checking disk %s: no device number in the stat result", path)
	}
	statfs := r.Statfs
	if statfs == nil {
		statfs = syscall.Statfs
	}
	var st syscall.Statfs_t
	if err := statfs(path, &st); err != nil {
		return Disk{}, fmt.Errorf("querying free space on %s: %w", path, err)
	}
	if st.Bsize <= 0 {
		return Disk{}, fmt.Errorf("querying free space on %s: filesystem reported block size %d", path, st.Bsize)
	}
	bsize := uint64(st.Bsize) // #nosec G115 -- Bsize validated positive above
	return Disk{
		Path: path,
		Dev:  stat.Dev,
		Size: st.Blocks * bsize,
		Free: st.Bavail * bsize,
	}, nil
}

// EmergeRunning reports whether any process under Root/proc runs emerge,
// judged from its cmdline (comm reads python3 for the python-exec wrapper).
// A process whose cmdline cannot be read, typically because it exited during
// the scan, is skipped; failing to list Root/proc itself is an error.
func (r Reader) EmergeRunning() (bool, error) {
	dir := filepath.Join(r.Root, "proc")
	entries, err := os.ReadDir(dir)
	if err != nil {
		return false, fmt.Errorf("listing %s: %w", dir, err)
	}
	for _, e := range entries {
		if !isPid(e.Name()) {
			continue
		}
		b, err := os.ReadFile(filepath.Join(dir, e.Name(), "cmdline")) //nolint:gosec // G304: e.Name() is digits only, checked by isPid
		if err != nil {
			continue
		}
		if isEmergeCmdline(b) {
			return true, nil
		}
	}
	return false, nil
}

// isPid reports whether a /proc entry name is a process id: digits only.
func isPid(name string) bool {
	if name == "" {
		return false
	}
	for _, c := range name {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}

// isEmergeCmdline matches a NUL-separated argv whose argv[0] has the base name
// emerge, or whose argv[0] is a python interpreter given a script with the
// base name emerge. emerge-webrsync and other near names never match.
func isEmergeCmdline(b []byte) bool {
	argv := strings.Split(string(bytes.TrimRight(b, "\x00")), "\x00")
	if argv[0] == "" {
		return false
	}
	prog := filepath.Base(argv[0])
	if prog == "emerge" {
		return true
	}
	return strings.HasPrefix(prog, "python") && len(argv) > 1 && filepath.Base(argv[1]) == "emerge"
}
