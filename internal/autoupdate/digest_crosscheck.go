package autoupdate

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/obentoo/bentoolkit/internal/autoupdate/ebuilds"
)

// ErrDistfileDigestMismatch marks a bump whose freshly digested distfile has
// a different size or hash than ::gentoo's Manifest records for the same
// file name. The Manifest pkgdev writes describes whatever the upstream URL
// served at that moment; a compromised release or download host would get
// its archive digested and published. ::gentoo fetched the same file name
// independently, so a disagreement means one of the two downloads is not the
// release, and the bump stops for a human instead of trusting either.
var ErrDistfileDigestMismatch = errors.New("a distfile's digest differs from ::gentoo's Manifest")

// manifestDist is one DIST line: the file size and its digests by type.
type manifestDist struct {
	size   string
	hashes map[string]string
}

// parseManifestDists reads the DIST lines of a Manifest, keyed by file name.
// Lines of other types, and malformed ones, are skipped: this is a
// comparison, and Portage itself refuses a Manifest it cannot parse.
func parseManifestDists(data []byte) map[string]manifestDist {
	dists := map[string]manifestDist{}
	sc := bufio.NewScanner(bytes.NewReader(data))
	for sc.Scan() {
		fields := strings.Fields(sc.Text())
		if len(fields) < 3 || fields[0] != "DIST" || len(fields)%2 != 1 {
			continue
		}
		d := manifestDist{size: fields[2], hashes: map[string]string{}}
		for i := 3; i+1 < len(fields); i += 2 {
			d.hashes[fields[i]] = strings.ToLower(fields[i+1])
		}
		dists[fields[1]] = d
	}
	return dists
}

// compareDists returns one description per file that both Manifests list
// with a different size or a different digest of a type both carry, sorted
// by file name, and how many shared files agreed.
func compareDists(ours, theirs map[string]manifestDist) (mismatches []string, agreed int) {
	for name, o := range ours {
		t, ok := theirs[name]
		if !ok {
			continue
		}
		if o.size != t.size {
			mismatches = append(mismatches, fmt.Sprintf("%s: size %s here, %s in ::gentoo", name, o.size, t.size))
			continue
		}
		shared, differs := 0, ""
		types := make([]string, 0, len(o.hashes))
		for typ := range o.hashes {
			types = append(types, typ)
		}
		sort.Strings(types)
		for _, typ := range types {
			theirHash, ok := t.hashes[typ]
			if !ok {
				continue
			}
			shared++
			if o.hashes[typ] != theirHash && differs == "" {
				differs = fmt.Sprintf("%s: %s %s here, %s in ::gentoo", name, typ, o.hashes[typ], theirHash)
			}
		}
		switch {
		case differs != "":
			mismatches = append(mismatches, differs)
		case shared > 0:
			agreed++
		}
	}
	sort.Strings(mismatches)
	return mismatches, agreed
}

// crossCheckGentooDigests compares the DIST entries of the Manifest just
// written in pkgDir with ::gentoo's Manifest for the same package, and fails
// on any file both list with a different size or digest. Files ::gentoo does
// not list (most of the overlay is ahead of it) are not a finding, and
// without a ::gentoo tree or either Manifest there is nothing to compare.
func (a *Applier) crossCheckGentooDigests(pkgDir, pkg string) error {
	if a.gentooPath == "" || pkgDir == "" {
		return nil
	}
	category, name, ok := ebuilds.SplitPkgAtom(pkg)
	if !ok {
		return nil
	}
	ourBytes, err := os.ReadFile(filepath.Join(pkgDir, "Manifest")) //nolint:gosec // G304: the candidate's own package directory
	if err != nil {
		return nil //nolint:nilerr // no Manifest is the coverage check's finding, not this one's
	}
	theirPath := filepath.Join(a.gentooPath, category, name, "Manifest")
	theirBytes, err := os.ReadFile(theirPath) //nolint:gosec // G304: a repo-relative package directory of ::gentoo
	if err != nil {
		return nil //nolint:nilerr // ::gentoo does not carry this package
	}

	mismatches, agreed := compareDists(parseManifestDists(ourBytes), parseManifestDists(theirBytes))
	if len(mismatches) > 0 {
		a.logger().Error("distfile digest differs from ::gentoo; bump refused",
			"package", pkg, "gentoo_manifest", theirPath, "files", len(mismatches))
		return fmt.Errorf("%w (%s): %s; one of the two downloads is not the upstream release, "+
			"so verify the archive by hand before trusting either", ErrDistfileDigestMismatch, theirPath, strings.Join(mismatches, "; "))
	}
	if agreed > 0 {
		a.logger().Info("distfile digests match ::gentoo", "package", pkg, "files", agreed)
	}
	return nil
}
