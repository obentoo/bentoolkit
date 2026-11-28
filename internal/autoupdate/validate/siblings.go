package validate

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// siblingsRepoPattern is the os.MkdirTemp pattern of a run's siblings
// repository. The directory's base name IS the repository name, so the random
// part sits in the middle: Portage refuses a repository name that ends in a
// hyphen followed by something version-shaped, and a bare random suffix is all
// digits.
const siblingsRepoPattern = stagedRepoPrefix + "-siblings-*-run"

// siblingsPriority is the repos.conf priority of the siblings repository: below
// every overlay that declares none (Portage reads an absent priority as 0, the
// candidate's own staged tree included), above gentoo's default of -1000.
//
// Below the candidate's own tree because the siblings repository holds the
// candidate too, at the same version: on a tie the candidate's own tree — the
// one its manifest and fixes were written into — must win.
const siblingsPriority = "-1"

// SiblingCandidate is one candidate of a run, as the siblings repository
// carries it: the registry key, the candidate version and the ebuild body.
type SiblingCandidate struct {
	Key, Version string
	EbuildBytes  []byte
}

// SiblingsRequest is everything StageSiblings needs. Overlay is the published
// overlay eclass/ and profiles/ are copied from; Parent is the directory the
// repository is created in ("" = os.TempDir()).
type SiblingsRequest struct {
	Overlay, Parent string
	Candidates      []SiblingCandidate
}

// Siblings is a staged repository holding every candidate of one run, so that a
// candidate's pretend resolve can see an exact-version dependency on another
// candidate of the same run (media-libs/mesa on ~dev-util/mesa_clc-${PV}).
//
// It is built ONCE, before any candidate is validated, and never written again:
// the resolves of up to --concurrency candidates read it at the same time, and
// a tree nobody writes needs no lock. Each candidate's own staged tree cannot
// serve instead — Stage replaces a tree in place, so a resolve reading a
// sibling's tree could meet it half-built, and a sibling not yet staged would
// be missing for a reason that depends on worker scheduling.
type Siblings struct {
	// Root is the repository's location; its base name is the repository name.
	Root string
	// CPVs are the category/package-version strings the repository holds, sorted.
	CPVs []string
}

// Name is the repository name Portage knows Root by.
func (s Siblings) Name() string { return filepath.Base(s.Root) }

// Holds reports whether cpv (category/package-version, no "::repo") is a
// candidate in the repository.
func (s Siblings) Holds(cpv string) bool {
	i := sort.SearchStrings(s.CPVs, cpv)
	return i < len(s.CPVs) && s.CPVs[i] == cpv
}

// Remove deletes the repository. A zero Siblings removes nothing.
func (s Siblings) Remove() error {
	if s.Root == "" {
		return nil
	}
	if err := os.RemoveAll(s.Root); err != nil {
		return fmt.Errorf("removing the siblings repository %s: %w", s.Root, err)
	}
	return nil
}

// StageSiblings builds the siblings repository of one run: eclass/, profiles/
// and metadata/layout.conf as Stage builds them, its own profiles/repo_name,
// and every candidate's ebuild. A candidate whose key cannot name a package
// directory fails the whole call, because the caller built the list from keys
// it already validated.
//
// Only the ebuilds travel — no Manifest, no files/. A pretend resolve reads
// dependency metadata and nothing else; the candidate under test still
// resolves from its own, complete tree.
func StageSiblings(req SiblingsRequest) (Siblings, error) {
	overlay := strings.TrimSpace(req.Overlay)
	if overlay == "" {
		return Siblings{}, fmt.Errorf("staging the run's siblings: no overlay given to copy eclasses and profiles from")
	}
	if info, err := os.Stat(overlay); err != nil {
		return Siblings{}, fmt.Errorf("staging the run's siblings: reading the published overlay %s: %w", overlay, err)
	} else if !info.IsDir() {
		return Siblings{}, fmt.Errorf("staging the run's siblings: the published overlay %s is not a directory", overlay)
	}

	root, err := os.MkdirTemp(req.Parent, siblingsRepoPattern)
	if err != nil {
		return Siblings{}, fmt.Errorf("staging the run's siblings: %w", err)
	}
	built := Siblings{Root: root}
	fail := func(err error) (Siblings, error) {
		_ = built.Remove() // the build error is the one the caller acts on
		return Siblings{}, fmt.Errorf("staging the run's siblings in %s: %w", root, err)
	}
	if err := ensureOutsideOverlay(overlay, root); err != nil {
		return fail(err)
	}
	if err := os.Chmod(root, stagedDirMode); err != nil {
		return fail(err)
	}
	for _, dir := range carriedRepoDirs {
		if err := carryRepoDir(filepath.Join(overlay, dir), filepath.Join(root, dir)); err != nil {
			return fail(err)
		}
	}
	if err := writeStagedLayout(overlay, root); err != nil {
		return fail(err)
	}
	if err := writeStagedFile(filepath.Join(root, "profiles"), "repo_name", []byte(built.Name()+"\n")); err != nil {
		return fail(err)
	}

	seen := map[string]bool{}
	for _, cand := range req.Candidates {
		category, pkg, err := splitContentAtom(cand.Key)
		if err != nil {
			return fail(err)
		}
		version := strings.TrimSpace(cand.Version)
		if err := usableAsPathElement("version", version); err != nil {
			return fail(fmt.Errorf("%s: %w", cand.Key, err))
		}
		if len(cand.EbuildBytes) == 0 {
			return fail(fmt.Errorf("%s-%s: no ebuild body given", cand.Key, version))
		}
		cpv := category + "/" + pkg + "-" + version
		if seen[cpv] {
			// Two release lines of one package at one version: one ebuild.
			continue
		}
		seen[cpv] = true
		if err := writeCandidate(root, category, pkg, version, cand.EbuildBytes); err != nil {
			return fail(err)
		}
		built.CPVs = append(built.CPVs, cpv)
	}
	sort.Strings(built.CPVs)
	return built, nil
}
