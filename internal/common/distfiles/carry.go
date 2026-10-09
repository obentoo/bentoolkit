package distfiles

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// DownloadSuffix is the name portage gives a download still in progress: it
// fetches into "<name>.__download__" and renames to "<name>" only once the file
// is complete (_download_suffix in portage's package/ebuild/fetch.py). A file
// carrying it is a partial by construction, whatever its size.
const DownloadSuffix = ".__download__"

// FileError is one file a distdir operation could not handle. The operation
// goes on with the next file, so a caller receives every failure at once and
// decides how to report it; this package does not log.
type FileError struct {
	Name   string // the distfile name; empty when the directory itself failed
	Source string // the directory the file was read from
	Err    error
}

func (e FileError) Error() string {
	if e.Name == "" {
		return fmt.Sprintf("distdir %s: %v", e.Source, e.Err)
	}
	return fmt.Sprintf("distfile %s in %s: %v", e.Name, e.Source, e.Err)
}

func (e FileError) Unwrap() error { return e.Err }

// CarryOver moves the completed downloads of one private distdir into another
// and returns how many moved, plus one FileError per file that could not be.
//
// Only top-level regular files move. A symlink stays behind because the usual
// one here points into the distfiles cache, and the directory it would land in
// can belong to an agent holding a Write tool: a link there would let that
// agent overwrite the cache entry. A directory, fifo or device is never opened
// (opening a fifo blocks), and a name ending in DownloadSuffix is a partial.
//
// A name already present in to is left alone, whatever it is: nothing is
// replaced, so an entry planted there is never written through. A move that
// fails leaves the file where it was. from and to are expected to share a
// filesystem; across filesystems every rename fails and is reported.
func CarryOver(from, to string) (int, []FileError) {
	if from == "" || to == "" || from == to {
		return 0, nil
	}
	entries, err := os.ReadDir(from)
	if err != nil {
		return 0, []FileError{{Source: from, Err: fmt.Errorf("reading distdir %s: %w", from, err)}}
	}

	moved := 0
	var failures []FileError
	for _, entry := range entries {
		name := entry.Name()
		// ReadDir reports the entry's own type, never its target's.
		if !entry.Type().IsRegular() || strings.HasSuffix(name, DownloadSuffix) {
			continue
		}
		dst := filepath.Join(to, name)
		if _, err := os.Lstat(dst); err == nil || !errors.Is(err, fs.ErrNotExist) {
			// Present, or not provably absent: either way, not ours to replace.
			continue
		}
		if err := os.Rename(filepath.Join(from, name), dst); err != nil {
			failures = append(failures, FileError{
				Name:   name,
				Source: from,
				Err:    fmt.Errorf("moving %s from %s to %s: %w", name, from, to, err),
			})
			continue
		}
		moved++
	}
	return moved, failures
}
