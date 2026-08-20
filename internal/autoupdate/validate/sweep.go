package validate

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

// Story 039, task 6 — R6, R6.1, R6.2, R6.3, R6.4, R6.5.
//
// Stage removes and recreates the tree OF THE PACKAGE it is staging ("R3.7:
// replace, never accumulate"). That is a replacement of ONE tree. Nothing
// removed the trees of packages that have LEFT SCOPE, so a --depth run over the
// whole overlay left one tree per package under StagingRoot, permanently. This
// file is the sweep that closes that, and it is deliberately a capability with
// no caller yet: a sweeper that runs before anyone has read its report is not
// something to ship blind.

// StagedCandidate names one package/version the current run still needs.
type StagedCandidate struct {
	Key     string // registry key: category/package, possibly :slot or @label
	Version string
}

// SweepRequest is one sweep over one staging root.
//
// InScope is what the CURRENT run still needs. A tree named there is never
// removed, whatever its record says, because a sweeper that eats the run that
// called it is worse than one that never runs.
type SweepRequest struct {
	Overlay     string
	StagingRoot string
	InScope     []StagedCandidate
}

// SweptEntry is one thing the sweep kept, and why.
//
// The reason is not decoration (R6.4). An operator looking at a staging root
// that did not shrink has to be able to read why it did not, and a sweeper that
// silently keeps things reads as a sweeper that swept.
type SweptEntry struct {
	Path   string
	Reason string
}

// SweepReport is everything one sweep did: the trees it removed, and every
// entry it left behind with the reason it left it.
//
// Both lists are sorted, so two sweeps over one staging root produce a report an
// operator can diff.
type SweepReport struct {
	Removed []string
	Kept    []SweptEntry
}

// SweepStagedTrees removes the staged trees the current run no longer needs, and
// reports everything it kept with a reason each (R6, R6.1, R6.4).
//
// # The retention policy, and why it is not "keep the last N"
//
// A tree is removed when all three hold: this package RECOGNISES it as one of
// its own, it is not in req.InScope, and its record shows no deciding gate
// FAILED. Everything else is kept and reported.
//
// The reason to keep a staged tree at all is to look at it after something went
// wrong. A tree whose gates PASSED has served its purpose the moment the verdict
// was recorded; a tree whose gate FAILED is the artifact an operator still
// needs, next to the log LogDir retained. "Keep the last N" is worse on both
// counts: it keeps passes, and it can still discard the failure somebody is
// mid-investigation on.
//
// A tree with no readable record is a tree whose outcome is UNKNOWN. It is kept,
// and the report says so. Fail-closed is right here: the cost of a wrong keep is
// disk, the cost of a wrong removal is the artifact an operator was about to
// open.
//
// # Safety (R6.2)
//
// The overlay check is ensureOutsideOverlay — the SAME one Stage uses, not a
// second one — and it runs before any entry is touched. A deletion routine with
// its own idea of what is inside the overlay is how a sweeper eventually eats a
// published package, and a sweeper that deletes half a tree and then discovers
// where it is has already done the damage.
func SweepStagedTrees(req SweepRequest) (SweepReport, error) {
	// First, and before anything is read, listed or removed.
	if err := ensureOutsideOverlay(req.Overlay, req.StagingRoot); err != nil {
		return SweepReport{}, err
	}

	inScope, err := inScopeTreePaths(req)
	if err != nil {
		return SweepReport{}, err
	}

	sweep := sweeper{root: req.StagingRoot, inScope: inScope}
	if err := sweep.walkStagingRoot(); err != nil {
		return SweepReport{}, err
	}
	return sweep.report(), nil
}

// inScopeTreePaths is the set of trees the current run still needs, keyed by the
// path StagedTreePath gives them.
//
// Keying by PATH rather than by atom and version keeps the layout rule spelled
// ONCE — the same reason StagedTreePath is exported and shared with Stage. A
// candidate this cannot name is a refusal and not a skip: a scope list the sweep
// cannot read might be naming the very tree it is about to remove, so the
// question is settled before anything is touched.
func inScopeTreePaths(req SweepRequest) (map[string]struct{}, error) {
	paths := make(map[string]struct{}, len(req.InScope))
	for _, candidate := range req.InScope {
		path, err := StagedTreePath(req.StagingRoot, candidate.Key, candidate.Version)
		if err != nil {
			return nil, fmt.Errorf("sweeping %s: naming the staged tree of %s-%s: %w",
				req.StagingRoot, candidate.Key, candidate.Version, err)
		}
		paths[path] = struct{}{}
	}
	return paths, nil
}

// sweeper carries one sweep's state across the three levels of the staging
// layout, <category>/<package>/<version>.
type sweeper struct {
	root    string
	inScope map[string]struct{}

	removed []string
	kept    []SweptEntry
}

// walkStagingRoot reads the staging root itself, the first of the three levels.
func (s *sweeper) walkStagingRoot() error {
	entries, err := os.ReadDir(s.root)
	if errors.Is(err, fs.ErrNotExist) {
		// A staging root nothing has staged into yet is not a failure. It is the
		// answer "there is nothing to sweep".
		return nil
	}
	if err != nil {
		return fmt.Errorf("reading the staging root %s: %w", s.root, err)
	}
	for _, entry := range entries {
		path := filepath.Join(s.root, entry.Name())
		if !entry.IsDir() {
			s.keep(path, notADirectoryReason)
			continue
		}
		s.walkCategory(path)
	}
	return nil
}

// walkCategory reads one <category> directory.
func (s *sweeper) walkCategory(categoryDir string) {
	for _, entry := range s.children(categoryDir) {
		path := filepath.Join(categoryDir, entry.Name())
		if !entry.IsDir() {
			s.keep(path, notADirectoryReason)
			continue
		}
		s.walkPackage(path, entry.Name())
	}
}

// walkPackage reads one <category>/<package> directory. Its children sit where a
// staged tree sits, so each one is a decision.
func (s *sweeper) walkPackage(packageDir, pkg string) {
	for _, entry := range s.children(packageDir) {
		path := filepath.Join(packageDir, entry.Name())
		if !entry.IsDir() {
			s.keep(path, notADirectoryReason)
			continue
		}
		s.decide(path, pkg, entry.Name())
	}
}

// decide applies the retention policy to one directory sitting where a staged
// tree sits. It is the only place in this file that removes anything.
func (s *sweeper) decide(dir, pkg, version string) {
	if recognised, why := recognisedStagedTree(dir, pkg, version); !recognised {
		s.keep(dir, why)
		return
	}
	if _, needed := s.inScope[dir]; needed {
		s.keep(dir, "the current run still needs this tree")
		return
	}
	if why, keep := recordKeepsIt(dir); keep {
		s.keep(dir, why)
		return
	}
	if err := removeStagedTree(dir); err != nil {
		// It is still there, so it is a keep — reported as one rather than
		// swallowed, because a removal that failed is exactly the thing an
		// operator staring at an unchanged staging root needs to see.
		s.keep(dir, fmt.Sprintf("it could not be removed (%v)", err))
		return
	}
	s.removed = append(s.removed, dir)
}

// removeStagedTree takes one recognised tree out as a UNIT (R6.5).
//
// The one thing this must not do is remove the marker first and the rest after.
// profiles/repo_name is the ONLY thing that identifies a tree as this package's
// (see recognisedStagedTree), so a removal interrupted after the marker was
// deliberately taken out on its own would leave a tree nothing recognises — and
// R6.3 keeps what it does not recognise, so it would sit under the staging root
// forever, which is the very defect this file exists to close.
//
// One os.RemoveAll keeps every state an interruption can leave on the safe side
// of that. The entry ends up gone, or still recognised — in which case the next
// sweep reaches the same verdict and removes it — or truncated and no longer
// recognised, in which case the next sweep keeps it AND names it in the report.
// There is no state in which a later run treats a half-removed entry as a tree
// it may reuse without saying so.
func removeStagedTree(dir string) error {
	if err := os.RemoveAll(dir); err != nil {
		return fmt.Errorf("removing the staged tree %s: %w", dir, err)
	}
	return nil
}

// recognisedStagedTree answers whether dir is a tree THIS package produced, and
// says why not when it is not (R6.3).
//
// The check is self-verifying on purpose. Stage writes profiles/repo_name
// holding stagedRepoName(pkg, version), so a directory is one of ours when that
// file's content matches the package and version ITS OWN PATH implies. Two
// properties follow and both are the point: a directory an operator parked under
// the staging root cannot accidentally satisfy it, and a tree that was MOVED
// stops satisfying it — which is the safe direction, because everything
// unrecognised is kept.
//
// The expected name comes from stagedRepoName itself and never from a second
// spelling of the rule. A sweep whose idea of the name drifted from Stage's
// would either stop recognising its own trees and quietly keep everything, or
// start recognising something else.
func recognisedStagedTree(dir, pkg, version string) (bool, string) {
	marker := filepath.Join(dir, "profiles", "repo_name")

	body, err := os.ReadFile(marker)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return false, "it carries no profiles/repo_name, so it is not a tree this package produced"
	case err != nil:
		return false, fmt.Sprintf("its profiles/repo_name could not be read (%v), so it cannot be identified as a tree this package produced", err)
	}

	want := stagedRepoName(pkg, version)
	if got := strings.TrimSpace(string(body)); got != want {
		return false, fmt.Sprintf("its profiles/repo_name names %q and a tree at this path would name %q, so it is not a tree this package produced here", got, want)
	}
	return true, ""
}

// recordKeepsIt answers whether a recognised, out-of-scope tree must be kept
// anyway because of what its record says, and why.
//
// A record that cannot be read at all is an UNKNOWN outcome, not a passing one.
// Both spellings of unknown — no record, and a record that would not parse —
// keep the tree, and they are reported apart because they are different facts
// about the operator's machine.
func recordKeepsIt(dir string) (string, bool) {
	record, err := ReadStageRecord(dir)
	switch {
	case errors.Is(err, ErrNoStageRecord):
		return "it carries no validation record, so its outcome is unknown", true
	case err != nil:
		return fmt.Sprintf("its validation record could not be read (%v), so its outcome is unknown", err), true
	}

	for _, gate := range record.Gates {
		// GateQA decides nothing — the same D8 exclusion Report.ExitCode,
		// EbuildResult.WorstOutcome and StageRecord.Proves already make. A
		// metadata.xml finding is not the failure an operator keeps a whole tree
		// around to look at.
		if gate.Gate == GateQA {
			continue
		}
		if gate.Outcome == OutcomeFailed {
			return fmt.Sprintf("the %s gate FAILED, and this tree is the artifact an operator still needs", gate.Gate), true
		}
	}
	return "", false
}

// notADirectoryReason covers everything under the staging root that the walk
// will not descend into or remove: a plain file, and a symlink too. DirEntry
// reports the type recorded in the directory entry itself, so a symlink answers
// false to IsDir even when it points at a directory — which is the direction a
// routine that removes things wants, because whatever it points at lives
// somewhere this sweep was never asked about.
const notADirectoryReason = "it is not a directory, so it is neither a tree this package produced nor a level of the staging layout"

// children lists one level of the walk.
//
// A directory that vanished between its parent's listing and this call has
// nothing left to keep or remove, and nothing to report. A directory that cannot
// be LISTED is one whose contents cannot be recognised, and R6.3 keeps what it
// does not recognise — so it is kept, named, and not descended into.
func (s *sweeper) children(dir string) []os.DirEntry {
	entries, err := os.ReadDir(dir)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return nil
	case err != nil:
		s.keep(dir, fmt.Sprintf("it could not be read (%v), so nothing inside it could be recognised", err))
		return nil
	}
	return entries
}

// keep records one entry the sweep left behind, with the reason (R6.4).
func (s *sweeper) keep(path, reason string) {
	s.kept = append(s.kept, SweptEntry{Path: path, Reason: reason})
}

// report sorts both lists so that two sweeps over one staging root produce a
// report an operator can diff. os.ReadDir already returns entries in order, but
// the guarantee belongs to the report rather than to the traversal that happens
// to provide it.
func (s *sweeper) report() SweepReport {
	slices.Sort(s.removed)
	slices.SortFunc(s.kept, func(a, b SweptEntry) int { return strings.Compare(a.Path, b.Path) })
	return SweepReport{Removed: s.removed, Kept: s.kept}
}
