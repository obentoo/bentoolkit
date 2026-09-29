package fileutil

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"syscall"
)

// WriteFileAtomic replaces the file at path with data, under mode, so that a
// reader — or a crash — sees either the previous file or the new one and never
// a mixture.
//
// The bytes go to a temporary file in path's own directory (so the rename
// never crosses a filesystem), named "." + base + ".bentoo-<pid>-<random>" so
// a leftover of a killed writer names the process that left it and is swept by
// RemoveStaleTemps. The temporary file is synced, closed with its error
// checked, and given mode before it is renamed over path; the parent directory
// is then synced so the rename itself survives a power loss.
//
// Any failure before the rename removes the temporary file and leaves path
// byte-identical; the returned error wraps the cause and names path. A removal
// that itself fails is joined to the error, naming the file left behind — this
// package logs nothing.
func WriteFileAtomic(path string, data []byte, mode os.FileMode) (err error) {
	tmpName, err := writeTemp(path, data, mode)
	if err != nil {
		return err
	}
	committed := false
	defer func() {
		if !committed {
			err = errors.Join(err, removeTemp(tmpName, path))
		}
	}()

	if err := os.Rename(tmpName, path); err != nil { //nolint:gosec // G703: path is a file the caller owns (a state, cache or registry file in the config dir or overlay, or an ebuild already in a splitPkgAtom-confined package directory); tmpName is os.CreateTemp's name beside it
		return fmt.Errorf("renaming %s into place as %s: %w", tmpName, path, err)
	}
	committed = true
	return syncDir(filepath.Dir(path))
}

// PublishNewFile creates path holding data under mode, and refuses to replace
// anything already there: the file is written to a temporary name exactly as
// WriteFileAtomic writes it, then hard-linked to path, which fails when any
// entry — a file, a directory, even a dangling symlink — already sits at path.
// That failure satisfies errors.Is(err, fs.ErrExist), the entry is left
// untouched, and no temporary file remains.
//
// The link, unlike a check followed by a create, is one step: an entry created
// between a caller's existence check and the publish is never overwritten.
//
// Any other error means nothing was published: when a step after the link
// fails — removing the temporary name or syncing the directory — the entry at
// path is removed again, but only while path still names the file this call
// linked, so an entry someone else put there in the meantime is left alone.
func PublishNewFile(path string, data []byte, mode os.FileMode) error {
	tmpName, err := writeTemp(path, data, mode)
	if err != nil {
		return err
	}
	linked, err := os.Lstat(tmpName)
	if err != nil {
		return errors.Join(
			fmt.Errorf("inspecting %s before publishing it as %s: %w", tmpName, path, err),
			removeTemp(tmpName, path))
	}

	if err := os.Link(tmpName, path); err != nil {
		return errors.Join(
			fmt.Errorf("publishing %s without overwriting: %w", path, err),
			removeTemp(tmpName, path))
	}

	// From here the file is published; a failure must take it back.
	postErr := removeTemp(tmpName, path)
	if postErr == nil {
		postErr = syncDirFunc(filepath.Dir(path))
	}
	if postErr == nil {
		return nil
	}
	return errors.Join(fmt.Errorf("publishing %s: %w", path, postErr), unpublish(path, linked))
}

// syncDirFunc is the directory sync PublishNewFile runs after the link. It is
// a package variable so tests can make that last step fail.
var syncDirFunc = syncDir

// unpublish removes path when it still names the file described by linked.
// Identity is device and inode, read without following a symlink.
func unpublish(path string, linked os.FileInfo) error {
	onDisk, err := os.Lstat(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		return fmt.Errorf("inspecting %s to take back a failed publish: %w", path, err)
	}
	if !os.SameFile(onDisk, linked) {
		return nil
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("removing %s after a failed publish: %w", path, err)
	}
	return nil
}

// writeTemp writes data to a new temporary file beside path and returns its
// name. The file is synced, closed and set to mode; on any failure it is
// removed and the error names path.
func writeTemp(path string, data []byte, mode os.FileMode) (tmpName string, err error) {
	dir, base := filepath.Dir(path), filepath.Base(path)
	tmp, err := os.CreateTemp(dir, "."+base+".bentoo-"+strconv.Itoa(os.Getpid())+"-*")
	if err != nil {
		return "", fmt.Errorf("creating a temporary file beside %s: %w", path, err)
	}
	tmpName = tmp.Name()

	closed := false
	defer func() {
		if err == nil {
			return
		}
		if !closed {
			_ = tmp.Close() // the write already failed; that error is the one reported
		}
		err = errors.Join(err, removeTemp(tmpName, path))
	}()

	if _, err := tmp.Write(data); err != nil {
		return "", fmt.Errorf("writing %s for %s: %w", tmpName, path, err)
	}
	// Synced before it can be published: a rename of a name whose contents were
	// still only in the page cache could survive a crash as an empty file.
	if err := tmp.Sync(); err != nil {
		return "", fmt.Errorf("syncing %s before publishing it as %s: %w", tmpName, path, err)
	}
	closed = true
	if err := tmp.Close(); err != nil {
		return "", fmt.Errorf("closing %s before publishing it as %s: %w", tmpName, path, err)
	}
	// Always set explicitly: os.CreateTemp asks for 0600 but the umask can
	// narrow even that, and the published file must carry exactly mode.
	//nolint:gosec // G302: mode is the caller's choice for the file it owns (0600 for state, the ebuild's or registry's own mode otherwise).
	if err := os.Chmod(tmpName, mode); err != nil {
		return "", fmt.Errorf("setting mode %04o on %s before publishing it as %s: %w", mode.Perm(), tmpName, path, err)
	}
	return tmpName, nil
}

// removeTemp removes a temporary file written for path. A file already gone is
// not an error; any other failure is returned naming the file left behind.
func removeTemp(tmpName, path string) error {
	if err := os.Remove(tmpName); err != nil && !errors.Is(err, fs.ErrNotExist) { //nolint:gosec // G703: tmpName is the name os.CreateTemp generated beside the caller's own file, never an input
		return fmt.Errorf("removing the temporary file %s left beside %s: %w", tmpName, path, err)
	}
	return nil
}

// syncDir fsyncs a directory so a rename or link inside it survives a power
// loss. A filesystem that cannot sync a directory reports EINVAL, which is
// ignored: there is nothing more durable to ask of it.
func syncDir(dir string) error {
	d, err := os.Open(dir) //nolint:gosec // G304: dir is filepath.Dir of the file the caller is writing; it is opened read-only to fsync, nothing is read from it
	if err != nil {
		return fmt.Errorf("opening directory %s to sync it: %w", dir, err)
	}
	syncErr := d.Sync()
	closeErr := d.Close()
	if syncErr != nil && !errors.Is(syncErr, syscall.EINVAL) {
		return fmt.Errorf("syncing directory %s: %w", dir, syncErr)
	}
	if closeErr != nil {
		return fmt.Errorf("closing directory %s after syncing it: %w", dir, closeErr)
	}
	return nil
}
