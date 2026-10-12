package repair

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

// ErrNoBundle reports that no repair bundle exists for the requested package.
var ErrNoBundle = errors.New("no repair bundle")

// ErrStagedTreeGone reports a bundle whose staged tree no longer exists.
var ErrStagedTreeGone = errors.New("staged tree no longer exists")

// FindBundle returns the newest bundle, by Created, among the staged trees of
// pkg ("category/name") under <stagingRoot>/<category>/<name>/<version>/, and
// the path it was read from. With no bundle the error is ErrNoBundle and names
// the packages that do have one; with only unreadable bundles it is their read
// errors, each naming its path. A bundle whose staged root is gone yields
// ErrStagedTreeGone naming that root.
func FindBundle(stagingRoot, pkg string) (Bundle, string, error) {
	cat, name, err := splitPackage(pkg)
	if err != nil {
		return Bundle{}, "", err
	}
	pkgDir := filepath.Join(stagingRoot, cat, name)
	paths, err := bundlesIn(pkgDir)
	if err != nil {
		return Bundle{}, "", fmt.Errorf("find repair bundle for %s: %w", pkg, err)
	}

	var best Bundle
	var bestPath string
	var unreadable []error
	for _, path := range paths {
		b, err := ReadBundle(path)
		if err != nil {
			unreadable = append(unreadable, err)
			continue
		}
		if bestPath == "" || b.Created.After(best.Created) {
			best, bestPath = b, path
		}
	}
	switch {
	case bestPath == "" && len(unreadable) > 0:
		return Bundle{}, "", fmt.Errorf("find repair bundle for %s: %w", pkg, errors.Join(unreadable...))
	case bestPath == "":
		return Bundle{}, "", noBundleError(stagingRoot, pkg)
	}

	if err := checkStagedRoot(best.StagedRoot); err != nil {
		return Bundle{}, "", fmt.Errorf("repair bundle %s for %s: %w", bestPath, pkg, err)
	}
	return best, bestPath, nil
}

// splitPackage splits "category/name" and refuses anything that could leave
// the staging root.
func splitPackage(pkg string) (cat, name string, err error) {
	cat, name, ok := strings.Cut(pkg, "/")
	if !ok || !isPathComponent(cat) || !isPathComponent(name) {
		return "", "", fmt.Errorf("find repair bundle: invalid package %q, want category/name", pkg)
	}
	return cat, name, nil
}

func isPathComponent(s string) bool {
	return s != "" && s != "." && s != ".." && !strings.ContainsAny(s, `/\`)
}

// noBundleError wraps ErrNoBundle with the packages that do have a bundle.
func noBundleError(stagingRoot, pkg string) error {
	have, err := ListBundles(stagingRoot)
	switch {
	case err != nil:
		return fmt.Errorf("%w for %s (listing the other bundles failed: %w)", ErrNoBundle, pkg, err)
	case len(have) == 0:
		return fmt.Errorf("%w for %s; no package has one", ErrNoBundle, pkg)
	default:
		return fmt.Errorf("%w for %s; packages with a bundle: %s", ErrNoBundle, pkg, strings.Join(have, ", "))
	}
}

// checkStagedRoot returns ErrStagedTreeGone, naming root, unless root is an
// existing directory.
func checkStagedRoot(root string) error {
	if root == "" {
		return fmt.Errorf("%w: the bundle names no staged root", ErrStagedTreeGone)
	}
	st, err := os.Stat(root)
	switch {
	case errors.Is(err, os.ErrNotExist):
		return fmt.Errorf("%w: %s", ErrStagedTreeGone, root)
	case err != nil:
		return fmt.Errorf("check staged tree %s: %w", root, err)
	case !st.IsDir():
		return fmt.Errorf("%w: %s is not a directory", ErrStagedTreeGone, root)
	}
	return nil
}

// ListBundles returns each package ("category/name") that has at least one
// bundle under stagingRoot, once and sorted. A missing staging root has none.
func ListBundles(stagingRoot string) ([]string, error) {
	cats, err := readDirs(stagingRoot)
	if err != nil {
		return nil, fmt.Errorf("list repair bundles: %w", err)
	}
	var out []string
	for _, cat := range cats {
		names, err := readDirs(filepath.Join(stagingRoot, cat))
		if err != nil {
			return nil, fmt.Errorf("list repair bundles: %w", err)
		}
		for _, name := range names {
			paths, err := bundlesIn(filepath.Join(stagingRoot, cat, name))
			if err != nil {
				return nil, fmt.Errorf("list repair bundles: %w", err)
			}
			if len(paths) > 0 {
				out = append(out, cat+"/"+name)
			}
		}
	}
	slices.Sort(out)
	return out, nil
}

// bundlesIn returns the path of every <pkgDir>/<version>/BundleFile that
// exists, in directory-name order. A missing pkgDir has none.
func bundlesIn(pkgDir string) ([]string, error) {
	versions, err := readDirs(pkgDir)
	if err != nil {
		return nil, err
	}
	var paths []string
	for _, v := range versions {
		path := filepath.Join(pkgDir, v, BundleFile)
		_, err := os.Lstat(path)
		switch {
		case err == nil:
			paths = append(paths, path)
		case !errors.Is(err, os.ErrNotExist):
			return nil, fmt.Errorf("check repair bundle %s: %w", path, err)
		}
	}
	return paths, nil
}

// readDirs returns the names of the subdirectories of dir, sorted. A missing
// dir has none.
func readDirs(dir string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", dir, err)
	}
	var names []string
	for _, e := range entries {
		if e.IsDir() {
			names = append(names, e.Name())
		}
	}
	return names, nil
}

// RemoveBundle removes <stagedRoot>/BundleFile and nothing else. A bundle that
// is already absent is not an error.
func RemoveBundle(stagedRoot string) error {
	if stagedRoot == "" {
		return errors.New("remove repair bundle: empty staged root")
	}
	path := filepath.Join(stagedRoot, BundleFile)
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("remove repair bundle %s: %w", path, err)
	}
	return nil
}
