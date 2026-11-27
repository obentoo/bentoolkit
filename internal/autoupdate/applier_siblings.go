package autoupdate

import (
	"os"
	"strings"
	"sync"

	"github.com/obentoo/bentoolkit/internal/autoupdate/ebuilds"
	"github.com/obentoo/bentoolkit/internal/autoupdate/parse"
	"github.com/obentoo/bentoolkit/internal/autoupdate/validate"
	"github.com/obentoo/bentoolkit/internal/common/ebuild"
)

// siblingsState holds the run's siblings repository. It is written by
// StageRunSiblings and its release, before and after the worker pool, and read
// by every candidate's resolve; the lock makes that contract checkable by the
// race detector rather than a matter of call order.
type siblingsState struct {
	mu   sync.RWMutex
	repo validate.Siblings
}

// StageRunSiblings stages every candidate of updates into one siblings
// repository (validate.StageSiblings) that each candidate's pretend resolve then
// sees at a priority below its own staged tree. That is what lets
// media-libs/mesa, which pins ~dev-util/mesa_clc-${PV}, resolve while
// dev-util/mesa_clc at the same version is a candidate of the same run: each
// candidate is staged in its own throwaway repository, so without this the
// resolve reports "there are no ebuilds to satisfy" for a version the run holds.
//
// It is built ONCE, before any candidate runs, from each package's current
// ebuild copied to the candidate version — the same bytes Stage starts from.
// That makes the result independent of worker scheduling (a sibling staged
// later than its consumer's dependency gate is still visible) and leaves the
// repository read-only while up to --concurrency resolves read it.
//
// Best effort: a failure to build it is logged and the run continues with each
// candidate resolving alone, as before. The returned release removes the
// repository; it is never nil.
func (a *Applier) StageRunSiblings(updates []PendingUpdate) (release func()) {
	noop := func() {}
	if len(updates) < 2 || a.stagingRoot == "" {
		return noop
	}
	candidates := make([]validate.SiblingCandidate, 0, len(updates))
	for _, update := range updates {
		if cand, ok := a.siblingCandidate(update); ok {
			candidates = append(candidates, cand)
		}
	}
	if len(candidates) < 2 {
		return noop
	}
	repo, err := validate.StageSiblings(validate.SiblingsRequest{Overlay: a.overlayPath, Candidates: candidates})
	if err != nil {
		a.logger().Warn("the run's candidates were not staged as siblings; a dependency on another candidate of this run will not resolve",
			"candidates", len(candidates), "err", err)
		return noop
	}
	a.logger().Debug("staged the run's candidates as siblings for the dependency resolve",
		"repository", repo.Name(), "path", repo.Root, "candidates", len(repo.CPVs))

	if a.siblings == nil {
		a.siblings = &siblingsState{}
	}
	state := a.siblings
	state.mu.Lock()
	state.repo = repo
	state.mu.Unlock()
	return func() {
		state.mu.Lock()
		state.repo = validate.Siblings{}
		state.mu.Unlock()
		if err := repo.Remove(); err != nil {
			a.logger().Warn("the run's siblings repository could not be removed", "path", repo.Root, "err", err)
		}
	}
}

// siblingCandidate is update as the siblings repository carries it, derived the
// way Apply and Validate derive the candidate: the version normalised and
// revised, the body copied from the current ebuild. An update none of them
// could stage is left out.
func (a *Applier) siblingCandidate(update PendingUpdate) (validate.SiblingCandidate, bool) {
	pkg := update.Package
	version := parse.StripVersionPrefix(strings.TrimSpace(update.NewVersion))
	if !ebuild.IsValidVersion(version) {
		return validate.SiblingCandidate{}, false
	}
	version = ebuilds.ApplyRevision(version, a.configs[pkg].Revision)
	current, err := a.resolveCurrentVersion(pkg)
	if err != nil {
		a.logger().Debug("not staged as a sibling: no current ebuild", "package", pkg, "err", err)
		return validate.SiblingCandidate{}, false
	}
	path := a.EbuildPath(pkg, current)
	if path == "" {
		return validate.SiblingCandidate{}, false
	}
	body, err := os.ReadFile(path) //nolint:gosec // G304: the current ebuild of a registry key, the path Apply reads its source from
	if err != nil {
		a.logger().Debug("not staged as a sibling: the current ebuild could not be read", "package", pkg, "path", path, "err", err)
		return validate.SiblingCandidate{}, false
	}
	return validate.SiblingCandidate{Key: pkg, Version: version, EbuildBytes: body}, true
}

// resolveScope is what a candidate's pretend resolve sees beyond this host's
// repositories and its own staged tree: the run's siblings, and the versions
// the overlay's cooldown holds.
func (a *Applier) resolveScope() validate.ResolveScope {
	var scope validate.ResolveScope
	if a.siblings != nil {
		a.siblings.mu.RLock()
		scope.Siblings = a.siblings.repo
		a.siblings.mu.RUnlock()
	}
	scope.CooldownHeld = a.cooldownHeld()
	return scope
}

// cooldownHeld is the set of category/package-version strings the overlay's
// cooldown block masks, or nil when the overlay has no block (or it could not
// be read: then nothing is named as held, which only costs wording).
func (a *Applier) cooldownHeld() map[string]bool {
	if a.cooldown == nil {
		return nil
	}
	a.cooldown.mu.Lock()
	defer a.cooldown.mu.Unlock()
	block, err := a.cooldownLocked()
	if err != nil || block == nil || len(block.Entries) == 0 {
		return nil
	}
	held := make(map[string]bool, len(block.Entries))
	for _, entry := range block.Entries {
		held[entry.Package+"-"+entry.Version] = true
	}
	return held
}
