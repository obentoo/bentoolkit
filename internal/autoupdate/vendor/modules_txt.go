package vendor

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
)

// modulesTxt is where `go mod vendor` records the modules it vendored,
// relative to the module's top directory.
const modulesTxt = "vendor/modules.txt"

// pinnedSubdir is the work dir entry the fetched tarball's modules.txt is
// extracted into.
const pinnedSubdir = "pinned"

// ModulesTxtDiffers extracts vendor/modules.txt from the fetched vendor
// tarball, regenerates the vendor tree from req.SourcePath as Generate does,
// without packing it, and reports whether the two files differ byte for byte.
//
// The fetched tarball's top directory does not have to match the source's:
// a tarball built for an older version compares by its modules.txt alone. A
// tarball holding no vendor/modules.txt, or more than one, is an error, never
// a match. Only req.SourcePath, req.WorkRoot and req.Timeout are used. The
// work dir is removed on every path.
func ModulesTxtDiffers(ctx context.Context, r Runner, fetched string, req GenerateRequest) (differs bool, err error) {
	if req.Timeout <= 0 {
		return false, fmt.Errorf("vendor %s: timeout %s is not positive", filepath.Base(req.SourcePath), req.Timeout)
	}
	err = withWorkDir(req.WorkRoot, func(work string) error {
		pinned, err := extractModulesTxt(ctx, r, work, fetched)
		if err != nil {
			return err
		}
		top, err := vendorTree(ctx, r, work, req)
		if err != nil {
			return err
		}
		fresh, err := readInRoot(filepath.Join(work, srcSubdir), path.Join(top, modulesTxt))
		if err != nil {
			return fmt.Errorf("read regenerated %s for %s: %w", modulesTxt, filepath.Base(req.SourcePath), err)
		}
		differs = !bytes.Equal(pinned, fresh)
		return nil
	})
	if err != nil {
		return false, err
	}
	return differs, nil
}

// extractModulesTxt extracts every member of fetched ending in
// vendor/modules.txt into <work>/pinned and returns the content of the single
// one that sits at vendor/modules.txt or <top>/vendor/modules.txt.
func extractModulesTxt(ctx context.Context, r Runner, work, fetched string) ([]byte, error) {
	name := filepath.Base(fetched)
	dir := filepath.Join(work, pinnedSubdir)
	if err := os.Mkdir(dir, 0o700); err != nil {
		return nil, fmt.Errorf("extract %s from %s: %w", modulesTxt, name, err)
	}
	out, err := r.Run(ctx, work, childEnv("LC_ALL=C"), "tar", "-xJf", fetched, "-C", dir, "--wildcards", "*"+modulesTxt)
	if err != nil {
		if bytes.Contains(out, []byte("Not found in archive")) {
			return nil, fmt.Errorf("%s holds no %s: %w%s", name, modulesTxt, err, outputTail(out))
		}
		return nil, fmt.Errorf("extract %s from %s: %w%s", modulesTxt, name, err, outputTail(out))
	}
	found, err := modulesTxtCandidates(dir)
	if err != nil {
		return nil, fmt.Errorf("extract %s from %s: %w", modulesTxt, name, err)
	}
	switch len(found) {
	case 0:
		return nil, fmt.Errorf("%s holds no %s", name, modulesTxt)
	case 1:
	default:
		return nil, fmt.Errorf("%s holds %d %s files, expected one: %q", name, len(found), modulesTxt, found)
	}
	body, err := readInRoot(dir, found[0])
	if err != nil {
		return nil, fmt.Errorf("read %s from %s: %w", found[0], name, err)
	}
	return body, nil
}

// modulesTxtCandidates lists, relative to dir, the regular files at
// vendor/modules.txt and at <top>/vendor/modules.txt for every top directory.
// Deeper matches of the extraction pattern, such as a vendored module's own
// files or a look-alike "foo-vendor/modules.txt", are not candidates.
func modulesTxtCandidates(dir string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	paths := []string{modulesTxt}
	for _, e := range entries {
		if e.IsDir() {
			paths = append(paths, path.Join(e.Name(), modulesTxt))
		}
	}
	var found []string
	for _, p := range paths {
		info, err := os.Lstat(filepath.Join(dir, filepath.FromSlash(p)))
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, err
		}
		if info.Mode().IsRegular() {
			found = append(found, p)
		}
	}
	return found, nil
}

// readInRoot reads the regular file rel (slash-separated) under dir through
// os.Root, so neither a symlinked file nor a symlinked parent directory can
// make it read outside dir.
func readInRoot(dir, rel string) (body []byte, err error) {
	root, err := os.OpenRoot(dir)
	if err != nil {
		return nil, err
	}
	defer func() {
		if cErr := root.Close(); cErr != nil && err == nil {
			body, err = nil, cErr
		}
	}()
	info, err := root.Lstat(rel)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("%s is not a regular file", rel)
	}
	return root.ReadFile(rel)
}
