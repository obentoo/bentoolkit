package overlay

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/obentoo/bentoolkit/internal/common/provider"
	"github.com/obentoo/bentoolkit/internal/gentoo/keywords"
	"github.com/obentoo/bentoolkit/internal/gentoo/repo"
)

// mirrorHeaderMarker is the header a ::gentoo mirror carries in this overlay.
const mirrorHeaderMarker = "Mirrored from ::gentoo"

// mirrorRevisionRe is a -rN revision suffix: versions are compared without
// it, so our 1.0-r1 is ::gentoo's 1.0 and ::gentoo's 1.1-r1 carries our 1.1.
var mirrorRevisionRe = regexp.MustCompile(`-r[0-9]+$`)

// MirrorAhead is an overlay version ::gentoo does not carry, of a package
// ::gentoo does carry, that is still shaped as a ::gentoo mirror: a bump past
// ::gentoo that kept ::gentoo's keywords or its mirror header.
type MirrorAhead struct {
	Version string
	// Keywords is true when KEYWORDS holds anything other than exactly
	// ~amd64, the overlay baseline; a multi-line value counts.
	Keywords bool
	// Header is true when the ebuild text carries the mirror header.
	Header bool
}

// mirrorAheadVersions lists the versions of pkg that are ahead of ::gentoo
// but still shaped as mirrors. It needs ::gentoo's package directory on disk,
// so an API-only provider yields nothing; so does a package ::gentoo does not
// carry, which is overlay-only and was never a mirror.
func mirrorAheadVersions(pkg repo.PackageInfo, prov provider.Provider, overlayPath string) []MirrorAhead {
	dirProv, ok := prov.(provider.PackageDirProvider)
	if overlayPath == "" || !ok {
		return nil
	}
	upstreamDir, err := dirProv.LocalPackagePath(pkg.Category, pkg.Package)
	if err != nil {
		return nil
	}
	carried := carriedBaseVersions(upstreamDir, pkg.Package)
	if len(carried) == 0 {
		return nil
	}
	var out []MirrorAhead
	for _, v := range pkg.Versions {
		base := mirrorRevisionRe.ReplaceAllString(v, "")
		if carried[base] || strings.HasSuffix(base, "9999") {
			continue
		}
		body, err := os.ReadFile(filepath.Join(overlayPath, pkg.Category, pkg.Package, pkg.Package+"-"+v+".ebuild")) //nolint:gosec // G304: an ebuild the overlay scan itself listed under overlayPath
		if err != nil {
			continue
		}
		value, singleLine, present := keywords.Read(body)
		m := MirrorAhead{
			Version:  v,
			Keywords: present && (!singleLine || value != "~amd64"),
			Header:   bytes.Contains(body, []byte(mirrorHeaderMarker)),
		}
		if m.Keywords || m.Header {
			out = append(out, m)
		}
	}
	return out
}

// carriedBaseVersions reads the versions, without -rN, of the ebuilds in a
// ::gentoo package directory. A directory that cannot be read carries none.
func carriedBaseVersions(dir, pn string) map[string]bool {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	carried := make(map[string]bool, len(entries))
	for _, e := range entries {
		v, ok := strings.CutPrefix(e.Name(), pn+"-")
		if !ok || e.IsDir() {
			continue
		}
		v, ok = strings.CutSuffix(v, ".ebuild")
		if !ok || v == "" || v[0] < '0' || v[0] > '9' {
			continue
		}
		carried[mirrorRevisionRe.ReplaceAllString(v, "")] = true
	}
	return carried
}

// mirrorAheadFindings turns every result's MirrorAhead entries into findings.
// They are findings only: nothing that decides a verdict reads them.
func mirrorAheadFindings(results []CompareResult) []Finding {
	var out []Finding
	for _, r := range results {
		for _, m := range r.MirrorAhead {
			var conds []string
			if m.Keywords {
				conds = append(conds, "its KEYWORDS is not the ~amd64 baseline")
			}
			if m.Header {
				conds = append(conds, "it still carries the \""+mirrorHeaderMarker+"\" header")
			}
			out = append(out, Finding{
				Kind:    FindingMirrorAhead,
				Atom:    r.Category + "/" + r.Package,
				Version: m.Version,
				Detail: fmt.Sprintf("version %s is ahead of ::gentoo but still shaped as a mirror: %s",
					m.Version, strings.Join(conds, ", and ")),
			})
		}
	}
	return out
}
