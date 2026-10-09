package distfiles

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// copyTempPattern names a copy in progress. The leading dot keeps it from ever
// equalling a distfile name, so a half-written file is never mistaken for one.
const copyTempPattern = ".bentoo-copy-*"

// CopyFromSources copies each named distfile that distdir does not yet hold
// from the first source directory holding it as a regular file, and returns
// how many were copied plus one FileError per copy that failed.
//
// It copies, never links: the directory it fills can belong to an agent holding
// a Write tool, and a link there would let that agent overwrite the source. On
// Linux io.Copy between two files uses copy_file_range, which reflinks on
// filesystems that support it, so a copy costs no space there.
//
// A source entry is stat'ed before it is opened, and anything that is not a
// regular file (or a symlink to one) is passed over unopened: opening a fifo
// blocks. A name that is not a single path element is skipped without a read
// or a write, since names come from upstream-influenced Manifests. A name
// already present in distdir is never replaced. Each copy is written under a
// temporary name and renamed into place only once complete; a failed copy
// leaves neither the temporary file nor the final name, and the name is not
// retried from a later source.
func CopyFromSources(distdir string, sources, names []string) (int, []FileError) {
	copied := 0
	var failures []FileError
	for _, name := range names {
		if !isSinglePathElement(name) {
			continue
		}
		final := filepath.Join(distdir, name)
		if _, err := os.Lstat(final); err == nil || !errors.Is(err, fs.ErrNotExist) {
			continue
		}
		for _, source := range sources {
			done, err := copyOne(filepath.Join(source, name), distdir, final)
			if err != nil {
				failures = append(failures, FileError{
					Name:   name,
					Source: source,
					Err:    fmt.Errorf("copying %s from %s into %s: %w", name, source, distdir, err),
				})
				break
			}
			if done {
				copied++
				break
			}
		}
	}
	return copied, failures
}

// isSinglePathElement reports whether name can only ever resolve to an entry
// directly inside the directory it is joined to.
func isSinglePathElement(name string) bool {
	return name != "" && name != "." && name != ".." &&
		filepath.Base(name) == name && !strings.ContainsRune(name, filepath.Separator)
}

// copyOne copies src to final through a temporary file in distdir. It returns
// (false, nil) when src is absent or not a regular file, so the caller tries
// the next source, and an error only for a copy that was attempted and failed.
func copyOne(src, distdir, final string) (bool, error) {
	info, err := os.Stat(src)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return false, nil
		}
		return false, err
	}
	if !info.Mode().IsRegular() {
		return false, nil
	}
	in, err := os.Open(src) //nolint:gosec // G304: src is a distfile name, checked to be one path element, under a source directory the caller resolved
	if err != nil {
		return false, err
	}
	defer in.Close() //nolint:errcheck // read-only; a close error cannot affect the copy
	if opened, err := in.Stat(); err != nil || !opened.Mode().IsRegular() {
		// Swapped for something else between the two stats: not ours to read.
		return false, err
	}

	out, err := os.CreateTemp(distdir, copyTempPattern)
	if err != nil {
		return false, err
	}
	tmp := out.Name()
	if _, err := io.Copy(out, in); err != nil {
		return false, discardTemp(out, tmp, err)
	}
	if err := out.Close(); err != nil {
		return false, discardTemp(nil, tmp, err)
	}
	if err := os.Rename(tmp, final); err != nil {
		return false, discardTemp(nil, tmp, err)
	}
	return true, nil
}

// discardTemp closes (when still open) and removes a temporary copy after a
// failure, and returns the failure together with any cleanup error.
func discardTemp(out *os.File, tmp string, cause error) error {
	var closeErr error
	if out != nil {
		closeErr = out.Close()
	}
	removeErr := os.Remove(tmp)
	if errors.Is(removeErr, fs.ErrNotExist) {
		removeErr = nil
	}
	return errors.Join(cause, closeErr, removeErr)
}
