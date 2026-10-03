package autoupdate

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// filesdirVersionedRef matches a ${FILESDIR} path built from a version
// variable: "${FILESDIR}/${P}-fix.patch", "$FILESDIR/${PN}-${PV}.conf".
var filesdirVersionedRef = regexp.MustCompile(`\$\{?FILESDIR\}?/[^"'\s]*\$\{?(P|PV|PF|MY_P|MY_PV)\}?`)

// warnIfFilesNameOldVersion reports files under files/ that carry the version
// being left behind in their name, when the ebuild builds its ${FILESDIR}
// paths from a version variable. A bump renames the ebuild and nothing else,
// so "${FILESDIR}/${P}-gcc15.patch" then names a file that does not exist and
// the new ebuild dies in src_prepare.
//
// It warns rather than renames. The previous ebuild stays in the tree unless
// --clean removes it, and it still reads the old name; and a file copied to a
// name the new ebuild never asks for is litter the overlay's parity sweep
// counts. Which of the two applies is a human's call. Like the ::gentoo
// advisory it never fails the bump.
func (a *Applier) warnIfFilesNameOldVersion(pkg, oldVersion, newVersion string) {
	category, pkgName, ok := splitPkgAtom(pkg)
	if !ok {
		return
	}
	pkgDir := filepath.Join(a.overlayPath, category, pkgName)
	oldPV := revisionSuffixRegex.ReplaceAllString(oldVersion, "")
	newPV := revisionSuffixRegex.ReplaceAllString(newVersion, "")
	if oldPV == newPV {
		return
	}

	src, err := os.ReadFile(filepath.Join(pkgDir, pkgName+"-"+oldVersion+".ebuild")) //nolint:gosec // repo-relative package dir
	if err != nil || !filesdirVersionedRef.Match(src) {
		return
	}

	oldP := pkgName + "-" + oldPV
	filesDir := filepath.Join(pkgDir, "files")
	var stale []string
	_ = filepath.WalkDir(filesDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.Contains(d.Name(), oldP) {
			return nil //nolint:nilerr // an unreadable entry is not this advisory's business
		}
		renamed := filepath.Join(filepath.Dir(path), strings.ReplaceAll(d.Name(), oldP, pkgName+"-"+newPV))
		if _, err := os.Stat(renamed); err == nil {
			return nil // the new version's file is already there
		}
		rel, _ := filepath.Rel(pkgDir, path)
		stale = append(stale, rel)
		return nil
	})
	if len(stale) == 0 {
		return
	}
	a.reporter.TaskStage(pkg, fmt.Sprintf(
		"%s named for %s, and the ebuild builds ${FILESDIR} paths from a version variable — the %s ebuild will look for %s; copy or rename before src_prepare",
		strings.Join(stale, ", "), oldPV, newPV, strings.ReplaceAll(strings.Join(stale, ", "), oldP, pkgName+"-"+newPV)))
}
