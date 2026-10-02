// Package pkgdb reads Portage's installed-package database (/var/db/pkg):
// one directory per installed package at <root>/<CATEGORY>/<PF>/, holding a
// SLOT file and, for packages installed from a named repository, a
// repository file.
package pkgdb

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/obentoo/bentoolkit/internal/common/ebuild"
)

// DefaultRoot is where Portage keeps the installed-package database.
const DefaultRoot = "/var/db/pkg"

// maxRecordBytes bounds how much of a SLOT or repository file is read. Both
// hold a single short line; anything beyond the bound is not part of it.
const maxRecordBytes = 4096

// Package is one installed package.
type Package struct {
	Category string
	Name     string
	// Version keeps the revision (e.g. "1.0-r3") so ebuild.CompareVersions
	// can order it.
	Version string
	// Slot is the part of the SLOT record before "/" (the subslot is dropped).
	Slot string
	// Repo is the repository record, or empty when the package has none
	// (R5.6: such a package is not installed from any named repository).
	Repo string
}

// Reader is the InstalledReader adapter: Installed reads the database at Root.
type Reader struct {
	Root string
}

// Installed returns what Read returns for r.Root.
func (r Reader) Installed(ctx context.Context) (pkgs []Package, skipped int, err error) {
	return Read(ctx, r.Root)
}

// Read returns every installed package under root, in directory order
// (category, then PF, both sorted by name).
//
// An entry that cannot be read — an unreadable category directory, or a
// package directory whose SLOT or repository record cannot be read — is
// skipped and counted in skipped, never fatal, so one bad entry does not hide
// the rest. Entries that are not packages (stray files, directory names
// without a version part) are ignored without being counted. A missing
// repository record yields an empty Repo (R5.6).
//
// err is non-nil only when root itself cannot be listed or ctx is done; it
// wraps the cause with %w and names root.
func Read(ctx context.Context, root string) (pkgs []Package, skipped int, err error) {
	if err := ctx.Err(); err != nil {
		return nil, 0, rootError(root, err)
	}
	categories, err := os.ReadDir(root)
	if err != nil {
		return nil, 0, rootError(root, err)
	}

	for _, cat := range categories {
		if !cat.IsDir() || !safeEntryName(cat.Name()) {
			continue
		}
		catDir := filepath.Join(root, cat.Name())
		entries, err := os.ReadDir(catDir)
		if err != nil {
			skipped++
			continue
		}
		for _, entry := range entries {
			if err := ctx.Err(); err != nil {
				return nil, 0, rootError(root, err)
			}
			if !entry.IsDir() || !safeEntryName(entry.Name()) {
				continue
			}
			name, version, ok := splitPF(entry.Name())
			if !ok {
				continue
			}
			pkg, err := readPackage(filepath.Join(catDir, entry.Name()))
			if err != nil {
				skipped++
				continue
			}
			pkg.Category, pkg.Name, pkg.Version = cat.Name(), name, version
			pkgs = append(pkgs, pkg)
		}
	}
	return pkgs, skipped, nil
}

// safeEntryName rejects a directory entry name that could leave the database
// root once joined to a path. os.ReadDir never returns such a name; the check
// is what makes the joined paths below safe to open.
func safeEntryName(name string) bool {
	return name != "" && name != "." && !strings.Contains(name, "/") && !strings.Contains(name, "..")
}

// splitPF splits a PF ("name-version[-rN]") into name and version. It scans
// the "-" positions from the right and takes the first whose remainder is a
// valid version, so names that contain "-<digit>" segments ("font-arial-1",
// "foo-2d") keep them. A name must be non-empty and must not begin with "-"
// (PMS forbids it; Portage's in-progress "-MERGING-<PF>" directories do).
func splitPF(pf string) (name, version string, ok bool) {
	for i := strings.LastIndexByte(pf, '-'); i > 0; i = strings.LastIndexByte(pf[:i], '-') {
		v := pf[i+1:]
		// IsValidVersion trims surrounding whitespace; require the bare form
		// so the name and version always reassemble to pf.
		if v != strings.TrimSpace(v) || !ebuild.IsValidVersion(v) {
			continue
		}
		name = pf[:i]
		if strings.HasPrefix(name, "-") {
			return "", "", false
		}
		return name, v, true
	}
	return "", "", false
}

// readPackage reads the SLOT and repository records of the package directory
// dir. A missing repository record is an empty Repo; any other failure is an
// error.
func readPackage(dir string) (Package, error) {
	slot, err := readFirstLine(filepath.Join(dir, "SLOT"))
	if err != nil {
		return Package{}, err
	}
	slot, _, _ = strings.Cut(slot, "/")

	repo, err := readFirstLine(filepath.Join(dir, "repository"))
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return Package{}, err
	}
	return Package{Slot: strings.TrimSpace(slot), Repo: repo}, nil
}

// readFirstLine returns the first line of the file at path, trimmed of
// surrounding whitespace, reading at most maxRecordBytes.
func readFirstLine(path string) (string, error) {
	// The entry names in path passed safeEntryName.
	f, err := os.Open(path) //nolint:gosec // G304: path under the constant /var/db/pkg root, from ReadDir entries
	if err != nil {
		return "", fmt.Errorf("open %s: %w", path, err)
	}
	defer f.Close() //nolint:errcheck // read-only handle: a failed close cannot lose data

	data, err := io.ReadAll(io.LimitReader(f, maxRecordBytes))
	if err != nil {
		return "", fmt.Errorf("read %s: %w", path, err)
	}
	line, _, _ := strings.Cut(string(data), "\n")
	return strings.TrimSpace(line), nil
}

// rootError wraps an error that stops the whole read, naming root.
func rootError(root string, err error) error {
	return fmt.Errorf("read installed packages under %s: %w", root, err)
}
