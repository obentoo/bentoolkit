package validate

import (
	"context"
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

// StagedSweepPlan is what one sweep WOULD do. Nothing in producing it removes
// anything.
//
// The two lists partition everything the walk saw: a directory is a planned
// removal or a keep with a reason, never both and never neither. That is what
// makes a printed plan auditable — an entry in neither list is a tree the plan
// says nothing about, and an operator reads silence as "it will not be touched".
//
// StagingRoot rides along because the executor acts on the root the PLAN names.
// A plan that did not carry it could only be executed against whatever root the
// caller happened to be holding, which is the one mistake this shape exists to
// make impossible.
//
// Both lists are sorted, so two plans over one staging root diff cleanly.
type StagedSweepPlan struct {
	StagingRoot string
	Remove      []string     // trees this sweep would remove, sorted
	Kept        []SweptEntry // everything else, with the reason it stays, sorted
}

// SweepStagedTrees removes the staged trees the current run no longer needs, and
// reports everything it kept with a reason each (R6, R6.1, R6.4).
//
// It is the plan followed by its effect and nothing else: PlanStagedSweep
// reaches every verdict — including the refusal of a staging root inside the
// overlay, which still happens before a single entry is read — and
// ExecuteStagedSweep is the only thing in this file that acts on them. The
// retention policy is documented on PlanStagedSweep, where it is applied.
//
// # Why it is RETAINED
//
// This is the surface story 039's R6 tests assert, and those tests are the only
// thing guarding the behaviour the split refactors: the same verdicts, in the
// same order, with a failed removal still reported as a keep carrying the error.
// Keeping the function is what lets them stay the proof they were written to be.
// The alternative — deleting it and rewriting its four tests against the new
// pair — would have replaced the evidence with assertions written by the same
// change they are supposed to check, which proves nothing about what was
// preserved (S041-R7.2).
//
// # It has NO production caller
//
// Nothing outside tests calls it. The subcommand this story adds holds
// PlanStagedSweep and ExecuteStagedSweep directly, because it prints the plan
// and asks before executing it, and there is no point in this shape at which it
// could interject. That is stated here rather than left to be found, because
// THIS story exists precisely because this capability shipped in 0.25.0 with no
// caller — this file's own header says so, at the top. A second uncalled
// function in the same file at least declares itself.
//
// # Why context.Background() is not a shortcut here
//
// The signature takes no context and could only gain one by changing, which
// R7.2 forbids. An entry point that cannot be cancelled is acceptable because
// the caller that needs cancellation is the subcommand, which passes its own
// context to ExecuteStagedSweep so a SIGINT lands between trees. What is
// uncancellable is this test surface, not the path an operator interrupts.
func SweepStagedTrees(req SweepRequest) (SweepReport, error) {
	plan, err := PlanStagedSweep(req)
	if err != nil {
		// Propagated unchanged: the refusal an operator sees through this door
		// has to be the one PlanStagedSweep reached, not a second wording of it.
		return SweepReport{}, err
	}
	return ExecuteStagedSweep(context.Background(), plan), nil // SAFE: non-cancellable retained surface; the subcommand that needs cancellation passes its own ctx to ExecuteStagedSweep (S041-R7.2)
}

// PlanStagedSweep walks one staging root, classifies every entry it finds, and
// leaves the filesystem exactly as it found it (S041-R1.1).
//
// # Why the decision is separate from the effect
//
// The sweeper used to judge and os.RemoveAll in the same traversal, so there was
// no point at which a complete report existed and nothing had yet been removed:
// nothing to show an operator before the deletions, and nothing a caller could
// decline. The house convention is the opposite in four places already —
// PlanOverlaySweep/ExecuteOverlaySweep, PlanPrune/ExecutePrune, PlanApply/Apply
// — and this is that same split. Producing a plan is a read-only act; that is
// the whole point of it, not an incidental property of the current code.
//
// # The retention policy, and why it is not "keep the last N"
//
// A tree is planned for removal when all three hold: this package RECOGNISES it
// as one of its own, it is not in req.InScope, and its record shows no deciding
// gate FAILED. Everything else is kept and reported.
//
// The reason to keep a staged tree at all is to look at it after something went
// wrong. A tree whose gates PASSED has served its purpose the moment the verdict
// was recorded; a tree whose gate FAILED is the artifact an operator still
// needs, next to the log LogDir retained. "Keep the last N" is worse on both
// counts: it keeps passes, and it can still discard the failure somebody is
// mid-investigation on.
//
// A tree with no readable record is a tree whose outcome is UNKNOWN. It is kept,
// and the plan says so. Fail-closed is right here: the cost of a wrong keep is
// disk, the cost of a wrong removal is the artifact an operator was about to
// open.
//
// # Safety (R6.2), and why the check is the FIRST statement
//
// The overlay check is ensureOutsideOverlay — the SAME one Stage uses, not a
// second one. A deletion routine with its own idea of what is inside the overlay
// is how a sweeper eventually eats a published package. It came first when one
// function both judged and deleted, and it comes first here for a second reason:
// this is now the thing that READS the staging root, and a reader that walks
// first and refuses afterwards has already listed the published overlay by the
// time it says no.
//
// inScopeTreePaths comes next and propagates its error unchanged, for the reason
// stated there: a request whose scope cannot be named is refused, never silently
// swept. Both refusals return the zero plan — a refusal carrying a populated
// plan is an invitation to execute it.
func PlanStagedSweep(req SweepRequest) (StagedSweepPlan, error) {
	// First, and before anything is read or listed.
	if err := ensureOutsideOverlay(req.Overlay, req.StagingRoot); err != nil {
		return StagedSweepPlan{}, err
	}

	inScope, err := inScopeTreePaths(req)
	if err != nil {
		return StagedSweepPlan{}, err
	}

	sweep := sweeper{root: req.StagingRoot, inScope: inScope}
	if err := sweep.walkStagingRoot(); err != nil {
		return StagedSweepPlan{}, err
	}
	return sweep.plan(), nil
}

// ExecuteStagedSweep removes exactly the trees plan.Remove names, and reports
// what it did and what it left (S041-R1.4, R3.1, R3.2).
//
// # Why it acts on the plan and never decides again
//
// The plan is what an operator was shown, and at a confirmation prompt it is
// what they said yes to. An executor that walked the staging root a second time
// would reach its own verdicts on a root that has moved since: a tree staged
// between the printing and the answer is removable by every rule in the policy
// and appears nowhere in the list that was approved. So the walk happens once,
// in PlanStagedSweep, and this reads plan.Remove and nothing else — every path
// it touches was named by the plan, under the root the plan walked.
//
// # Why it returns no error
//
// It matches ExecuteOverlaySweep. A tree that could not be removed is reported
// as a keep carrying the error as its reason, which is the fact an operator
// staring at a staging root that did not shrink actually needs. A batch-level
// error on top of that would either repeat it or, worse, be read as "the sweep
// failed" and discard the removals that did succeed.
//
// # Cancellation (R3.1), and where R3.2 actually lives
//
// ctx is consulted BEFORE each removal rather than after, so a SIGINT lands
// between trees while the next one is still whole. Every tree the sweep did not
// reach is then reported as a keep naming the interruption: "you stopped me" is
// a reason, and a report that simply stopped listing would be indistinguishable
// from a completed sweep over a shorter plan.
//
// That check decides WHICH trees are touched. What makes R3.2's "wholly present
// or wholly absent" true of a tree that IS touched is removeStagedTree being one
// os.RemoveAll that is never split — the reasoning is on that function, and it
// is the half of the invariant no amount of checking here could provide.
func ExecuteStagedSweep(ctx context.Context, plan StagedSweepPlan) SweepReport {
	// Cloned rather than appended to in place. The plan is the record of what
	// was decided, and possibly what an operator was shown; a sweep that grew
	// the caller's copy of the keeps would leave it holding something other than
	// what it agreed to.
	report := SweepReport{Kept: slices.Clone(plan.Kept)}

	for i, dir := range plan.Remove {
		if err := ctx.Err(); err != nil {
			// From i, not from i+1: the tree at i is one of the ones not reached.
			report.Kept = append(report.Kept, unreachedKeeps(plan.Remove[i:], err)...)
			break
		}
		if err := removeStagedTree(dir); err != nil {
			// It is still there, so it is a keep — reported as one rather than
			// swallowed, because a removal that failed is exactly the thing an
			// operator staring at an unchanged staging root needs to see.
			report.Kept = append(report.Kept, SweptEntry{
				Path:   dir,
				Reason: fmt.Sprintf("it could not be removed (%v)", err),
			})
			continue
		}
		report.Removed = append(report.Removed, dir)
	}

	// Removed is a subsequence of the already sorted plan.Remove, so only the
	// keeps can have left their order: a failed removal and an unreached tree
	// both join them after the plan was sorted.
	sortKept(report.Kept)
	return report
}

// unreachedKeeps names every tree a stopped sweep did not get to (R3.1).
//
// It exists as its own function because "kept" is doing two different jobs in
// this file, and only one of them is a decision: PlanStagedSweep's keeps are
// verdicts about the tree itself, these are verdicts about the sweep. Both have
// to appear in the same list — the report covers the whole plan or it trails
// off, and a report that trails off reads as a shorter plan that completed —
// but the reason has to say which kind it is, which is why it names the
// interruption instead of describing the tree.
func unreachedKeeps(unreached []string, err error) []SweptEntry {
	keeps := make([]SweptEntry, 0, len(unreached))
	for _, dir := range unreached {
		keeps = append(keeps, SweptEntry{
			Path:   dir,
			Reason: fmt.Sprintf("the sweep was interrupted before it reached this tree (%v)", err),
		})
	}
	return keeps
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

	remove []string
	kept   []SweptEntry
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
// tree sits, and RECORDS the verdict instead of acting on it.
//
// It used to os.RemoveAll right here, in the same pass that judged. Appending
// the path instead is the whole of S041-R1.1 at the level where it is decided:
// every branch below either keeps or plans, none of them touches the disk, and
// the two lists it feeds partition everything the walk saw.
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
	s.remove = append(s.remove, dir)
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

// plan sorts both lists so that two plans over one staging root are something an
// operator can diff. os.ReadDir already returns entries in order, but the
// guarantee belongs to the plan rather than to the traversal that happens to
// provide it.
func (s *sweeper) plan() StagedSweepPlan {
	slices.Sort(s.remove)
	sortKept(s.kept)
	return StagedSweepPlan{StagingRoot: s.root, Remove: s.remove, Kept: s.kept}
}

// sortKept is the one spelling of the keeps' order, shared by the plan and by
// anything that adds a keep after the plan was made — a failed removal being the
// case that exists today. Two orders would be two diffs of the same sweep.
func sortKept(kept []SweptEntry) {
	slices.SortFunc(kept, func(a, b SweptEntry) int { return strings.Compare(a.Path, b.Path) })
}
