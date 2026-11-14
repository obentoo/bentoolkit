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
