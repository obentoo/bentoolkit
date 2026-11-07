package targets

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/obentoo/bentoolkit/internal/common/ebuild"
)

// maxCacheLine bounds one md5-cache line: a go-module ebuild that still lists
// EGO_SUM carries thousands of SRC_URI tokens on one line.
const maxCacheLine = 16 << 20

// Arch is the keyword a version must carry, stable or testing, to count as
// visible. bentoo keywords amd64 only.
const Arch = "amd64"

// ReadCacheEntry returns the IUSE and KEYWORDS of one md5-cache entry.
func ReadCacheEntry(r io.Reader) (iuse, keywords string, err error) {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64<<10), maxCacheLine)
	for sc.Scan() {
		line := sc.Text()
		if v, ok := strings.CutPrefix(line, "IUSE="); ok {
			iuse = v
		} else if v, ok := strings.CutPrefix(line, "KEYWORDS="); ok {
			keywords = v
		}
	}
	if err := sc.Err(); err != nil {
		return "", "", fmt.Errorf("read md5-cache entry: %w", err)
	}
	return iuse, keywords, nil
}

// visible reports whether KEYWORDS carries Arch, stable or testing. A live
// ebuild has no keywords and is never what Portage picks by default.
func visible(keywords string) bool {
	for _, k := range strings.Fields(keywords) {
		if k == Arch || k == "~"+Arch {
			return true
		}
	}
	return false
}

// SplitPF splits a package-version name ("vim-9.2.1302-r1") at the first dash
// whose remainder is a valid version, which is how PMS separates PN from PVR:
// a PN may itself hold dashes followed by digits only when the rest is not a
// version ("font-adobe-100dpi-1.0.4").
func SplitPF(pf string) (pn, pvr string, ok bool) {
	for i := 0; i < len(pf); i++ {
		if pf[i] != '-' || i+1 >= len(pf) || pf[i+1] < '0' || pf[i+1] > '9' {
			continue
		}
		if ebuild.IsValidVersion(pf[i+1:]) {
			return pf[:i], pf[i+1:], true
		}
	}
	return "", "", false
}

// Candidate is one version a repository offers for a package.
type Candidate struct {
	Version string
	Repo    string
	Offered Offered
	// Err is set when the offer could not be read; such a candidate is reported,
	// never silently treated as offering nothing.
	Err error
}

// Finding is a package whose best visible version does not offer a required
// implementation.
type Finding struct {
	Atom    string `json:"atom"`
	Version string `json:"version"`
	Repo    string `json:"repo"`
	Gaps    []Gap  `json:"gaps,omitempty"`
	// Unreadable carries why the offer of the best version could not be read.
	Unreadable string `json:"unreadable,omitempty"`
	// OverlayVersion is the overlay's best visible version when another
	// repository's is the one Portage would pick: the overlay holds a version
	// that was overtaken, and the fix is to bump it.
	OverlayVersion string `json:"overlay_version,omitempty"`
}

// SweepOptions selects what Sweep reads.
type SweepOptions struct {
	Overlay     string
	OverlayRepo string // the overlay's repo_name, used in reports
	Gentoo      string // "" skips the master
	// All sweeps every package of the master, not only those the overlay
	// carries.
	All bool
}

// Sweep returns, for each package, the gaps of its best visible version across
// the overlay and the master. Masks are not read: a masked version that sorts
// highest is reported as if it were the one picked.
func Sweep(ctx context.Context, p *Policy, opts SweepOptions) ([]Finding, error) {
	index := map[string][]Candidate{}
	if err := indexOverlay(ctx, opts.Overlay, opts.OverlayRepo, index); err != nil {
		return nil, err
	}
	if opts.Gentoo != "" {
		if err := indexMaster(ctx, opts.Gentoo, opts.All, index); err != nil {
			return nil, err
		}
	}

	atoms := make([]string, 0, len(index))
	for atom := range index {
		atoms = append(atoms, atom)
	}
	sort.Strings(atoms)

	var findings []Finding
	for _, atom := range atoms {
		best, overlayBest := pickBest(index[atom], opts.OverlayRepo)
		if best == nil {
			continue
		}
		f := Finding{Atom: atom, Version: best.Version, Repo: best.Repo}
		if best.Err != nil {
			f.Unreadable = best.Err.Error()
		} else if f.Gaps = p.Check(atom, best.Offered); len(f.Gaps) == 0 {
			continue
		}
		if best.Repo != opts.OverlayRepo && overlayBest != nil {
			f.OverlayVersion = overlayBest.Version
		}
		findings = append(findings, f)
	}
	return findings, nil
}

// pickBest returns the highest version and the overlay's highest. On equal
// versions the overlay wins, as its repository priority makes it win in
// Portage.
func pickBest(cands []Candidate, overlayRepo string) (best, overlayBest *Candidate) {
	better := func(c, than *Candidate) bool {
		if than == nil {
			return true
		}
		cmp := ebuild.CompareVersions(c.Version, than.Version)
		return cmp > 0 || (cmp == 0 && c.Repo == overlayRepo)
	}
	for i := range cands {
		c := &cands[i]
		if better(c, best) {
			best = c
		}
		if c.Repo == overlayRepo && better(c, overlayBest) {
			overlayBest = c
		}
	}
	return best, overlayBest
}

// indexOverlay reads every ebuild of the overlay, from its md5-cache entry when
// one exists and from the ebuild text otherwise, so a stale cache cannot hide a
// candidate.
func indexOverlay(ctx context.Context, overlay, repo string, index map[string][]Candidate) error {
	ebuildPaths, err := filepath.Glob(filepath.Join(overlay, "*", "*", "*.ebuild"))
	if err != nil {
		return fmt.Errorf("list the ebuilds of %s: %w", overlay, err)
	}
	for _, path := range ebuildPaths {
		if err := ctx.Err(); err != nil {
			return fmt.Errorf("sweep interrupted: %w", err)
		}
		pkgDir := filepath.Dir(path)
		cat, pn := filepath.Base(filepath.Dir(pkgDir)), filepath.Base(pkgDir)
		if strings.HasPrefix(cat, ".") {
			continue
		}
		pf := strings.TrimSuffix(filepath.Base(path), ".ebuild")
		name, version, ok := SplitPF(pf)
		if !ok || name != pn {
			continue
		}
		cand := Candidate{Version: version, Repo: repo}
		cachePath := filepath.Join(overlay, "metadata", "md5-cache", cat, pf)
		iuse, keywords, err := readCacheFile(cachePath)
		switch {
		case err == nil:
			if !visible(keywords) {
				continue
			}
			cand.Offered = FromIUSE(iuse)
		case errors.Is(err, os.ErrNotExist):
			content, readErr := os.ReadFile(path) //nolint:gosec // G304: an ebuild found by globbing the overlay being swept
			if readErr != nil {
				return fmt.Errorf("read %s: %w", path, readErr)
			}
			if strings.Contains(version, "9999") {
				continue
			}
			cand.Offered, cand.Err = FromEbuild(content)
		default:
			return err
		}
		index[cat+"/"+pn] = append(index[cat+"/"+pn], cand)
	}
	return nil
}

// indexMaster reads the master's md5-cache: only the packages the overlay
// carries, unless all is set.
func indexMaster(ctx context.Context, gentoo string, all bool, index map[string][]Candidate) error {
	cacheRoot := filepath.Join(gentoo, "metadata", "md5-cache")
	cats, err := os.ReadDir(cacheRoot)
	if err != nil {
		return fmt.Errorf("read the md5-cache of %s: %w", gentoo, err)
	}
	carried := make(map[string]bool, len(index))
	for atom := range index {
		carried[atom] = true
	}
	for _, cat := range cats {
		if !cat.IsDir() {
			continue
		}
		if err := ctx.Err(); err != nil {
			return fmt.Errorf("sweep interrupted: %w", err)
		}
		entries, err := os.ReadDir(filepath.Join(cacheRoot, cat.Name()))
		if err != nil {
			return fmt.Errorf("read the md5-cache of %s: %w", gentoo, err)
		}
		for _, e := range entries {
			pn, version, ok := SplitPF(e.Name())
			atom := cat.Name() + "/" + pn
			if !ok || (!all && !carried[atom]) {
				continue
			}
			iuse, keywords, err := readCacheFile(filepath.Join(cacheRoot, cat.Name(), e.Name()))
			if err != nil {
				return err
			}
			if !visible(keywords) {
				continue
			}
			index[atom] = append(index[atom], Candidate{Version: version, Repo: "gentoo", Offered: FromIUSE(iuse)})
		}
	}
	return nil
}

func readCacheFile(path string) (iuse, keywords string, err error) {
	f, err := os.Open(path) //nolint:gosec // G304: a metadata-cache entry under a repository being swept
	if err != nil {
		return "", "", fmt.Errorf("open %s: %w", path, err)
	}
	defer func() { _ = f.Close() }()
	iuse, keywords, err = ReadCacheEntry(f)
	if err != nil {
		return "", "", fmt.Errorf("%s: %w", path, err)
	}
	return iuse, keywords, nil
}

// RepoName reads <overlay>/profiles/repo_name.
func RepoName(overlay string) (string, error) {
	data, err := os.ReadFile(filepath.Join(overlay, "profiles", "repo_name")) //nolint:gosec // G304: a fixed name under the configured overlay
	if err != nil {
		return "", fmt.Errorf("read profiles/repo_name: %w", err)
	}
	name := strings.TrimSpace(string(data))
	if name == "" || strings.ContainsAny(name, " \t/") {
		return "", fmt.Errorf("profiles/repo_name %q is not a repository name", name)
	}
	return name, nil
}
