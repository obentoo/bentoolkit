package buildenv

import (
	"archive/tar"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// writeTar writes every path in paths to w as one tar archive, each entry named
// by its absolute path without the leading slash, so `tar -C / -xf -` inside
// the environment recreates it where it is here. A directory is written with
// everything under it; symlinks are written as links, never followed.
func writeTar(w io.Writer, paths []string) error {
	tw := tar.NewWriter(w)
	for _, root := range paths {
		abs, err := filepath.Abs(root)
		if err != nil {
			return fmt.Errorf("resolving %s: %w", root, err)
		}
		walkErr := filepath.WalkDir(abs, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			return addEntry(tw, path, d)
		})
		if walkErr != nil {
			return fmt.Errorf("archiving %s: %w", abs, walkErr)
		}
	}
	return tw.Close()
}

// addEntry writes one file system entry to tw.
func addEntry(tw *tar.Writer, path string, d fs.DirEntry) error {
	info, err := d.Info()
	if err != nil {
		return err
	}
	link := ""
	if info.Mode()&fs.ModeSymlink != 0 {
		if link, err = os.Readlink(path); err != nil {
			return err
		}
	}
	hdr, err := tar.FileInfoHeader(info, link)
	if err != nil {
		return err
	}
	hdr.Name = strings.TrimPrefix(path, "/")
	if info.IsDir() {
		hdr.Name += "/"
	}
	inEnvironment(hdr, info.Mode())
	if err := tw.WriteHeader(hdr); err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return nil
	}
	f, err := os.Open(path) //nolint:gosec // G304: a path under a tree this run staged or a distfile its Manifest names
	if err != nil {
		return err
	}
	defer f.Close() //nolint:errcheck // read-only
	_, err = io.Copy(tw, f)
	return err
}

// inEnvironment makes hdr what the entry must be inside the environment: owned
// by root and readable by everyone, executable where it was executable here.
//
// A staged tree is private on this host -- 0750 directories and 0600 files
// (validate.stagedDirMode, validate.stagedFileMode), because it holds a
// candidate nobody has reviewed and is no business of the host's other users.
// Copied as it is, it reached the environment owned by the uid that staged it
// and readable by that uid alone, while Portage reads an ebuild as the portage
// user: every build died sourcing it ("act-0.2.98.ebuild: Permission denied",
// before src_prepare), and the gate reported the environment as the failure
// (2026-11-16, on a Shidashi build VM). The environment is disposable and holds
// nobody else, so the host's reason for the private modes does not travel with
// the tree. Symlinks keep their own mode, which no one reads.
func inEnvironment(hdr *tar.Header, mode fs.FileMode) {
	hdr.Uid, hdr.Gid = 0, 0
	hdr.Uname, hdr.Gname = "root", "root"
	switch {
	case mode&fs.ModeSymlink != 0:
	case mode.IsDir(), mode&0o111 != 0:
		hdr.Mode = 0o755
	default:
		hdr.Mode = 0o644
	}
}
