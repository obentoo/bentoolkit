package fileutil

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"syscall"
)

// staleTempName matches the temporary names WriteFileAtomic and PublishNewFile
// create — "." + base + ".bentoo-<pid>-<random>" — and captures the PID of the
// writer. Names without a numeric PID group right after ".bentoo-" (the lock
// files, writeThenRename's and realign's temporaries) never match.
var staleTempName = regexp.MustCompile(`^\..+\.bentoo-([0-9]+)-[^/]+$`)

// RemoveStaleTemps removes the temporary files that writers killed mid-write
// left under root, and returns the paths it removed. A file is removed only
// when it is a regular file (a symlink is never followed or removed), its name
// matches the temporary-name pattern, and the PID in its name is no longer a
// live process. A temporary whose writer is alive — or whose PID answers EPERM,
// meaning it belongs to another user — is kept, so a concurrent writer is never
// robbed of its file.
//
// With recursive false only root's own entries are examined; with recursive
// true the whole tree is, except any directory named .git. A failure to walk
// is wrapped naming root; a failure to remove one file is joined to the result
// naming that file, and the sweep goes on.
func RemoveStaleTemps(root string, recursive bool) ([]string, error) {
	var removed []string
	var errs error

	consider := func(path string, d fs.DirEntry) {
		if !d.Type().IsRegular() {
			return
		}
		m := staleTempName.FindStringSubmatch(d.Name())
		if m == nil {
			return
		}
		pid, err := strconv.Atoi(m[1])
		if err != nil || pid <= 0 || processAlive(pid) {
			return
		}
		if err := os.Remove(path); err != nil {
			if !errors.Is(err, fs.ErrNotExist) {
				errs = errors.Join(errs, fmt.Errorf("removing the stale temporary file %s: %w", path, err))
			}
			return
		}
		removed = append(removed, path)
	}

	if !recursive {
		entries, err := os.ReadDir(root)
		if err != nil {
			return nil, fmt.Errorf("listing %s for stale temporary files: %w", root, err)
		}
		for _, e := range entries {
			consider(filepath.Join(root, e.Name()), e)
		}
		return removed, errs
	}

	walkErr := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if d.Name() == ".git" && path != root {
				return filepath.SkipDir
			}
			return nil
		}
		consider(path, d)
		return nil
	})
	if walkErr != nil {
		return removed, errors.Join(errs, fmt.Errorf("walking %s for stale temporary files: %w", root, walkErr))
	}
	return removed, errs
}

// processAlive reports whether pid names a live process. Signal 0 checks
// without delivering anything: ESRCH means no such process; nil or EPERM (a
// process of another user) means it exists.
func processAlive(pid int) bool {
	err := syscall.Kill(pid, 0)
	return !errors.Is(err, syscall.ESRCH)
}
