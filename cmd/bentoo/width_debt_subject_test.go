package main

// Authored for story 046, sub-task 11.3 — R6.2.
//
// R6.2's subject is "a file this story MODIFIES". design.md's D9 says the same
// thing in its own words: "the trigger is now a file this story provably
// edits". filesThisStoryTouches says something narrower — "every file named in
// the task list's Context blocks" — and the two drifted apart exactly where a
// drift is invisible: commit 55e6d06 edited cmd/bentoo/overlay_prune.go for the
// constructor extraction, nobody added it to the list, and its three typed
// widths went on passing a guard whose subject no longer contained them.
//
// # Why the list is not replaced by the diff
//
// filesThisStoryTouches gives a real reason for being written out: "the rule
// has to be checkable in a working tree with no branch point to diff against".
// That is true and worth keeping — a fresh clone at a tag has no base to diff.
// Deriving the subject from git would trade a property the guard has for one it
// does not need.
//
// So the list stays and gains a checker. Where a base commit IS resolvable, the
// list must account for every diff-touched file carrying a typed width; where
// it is not, the cross-check skips and SAYS SO. A list that states its own
// scope is fine. A list nobody can check against the thing it claims to mirror
// is how this gap happened.
//
// # RED ON ARRIVAL — two ways
//
// overlay_prune.go is absent from filesThisStoryTouches and carries three
// typed widths at :659, :666 and :1002. widthDebtSubjectGaps does not exist.

import (
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// storyBaseCommit is the commit this story branched from. The cross-check below
// resolves it and skips when it cannot — a test that failed on a shallow clone
// would be reporting the clone, not the code.
const storyBaseCommit = "c8e347e"

// TestOverlayPruneIsInTheSubject is the concrete half: the file the drift hid.
//
// It is named separately from the general cross-check because this one file is
// what the audit found, and a failure here should say so rather than arrive as
// one line of a list.
func TestOverlayPruneIsInTheSubject(t *testing.T) {
	const rel = "cmd/bentoo/overlay_prune.go"

	inSubject := false
	for _, f := range filesThisStoryTouches {
		if f == rel {
			inSubject = true
			break
		}
	}
	if !inSubject {
		t.Errorf("%s is edited by this story (commit 55e6d06) and is not in filesThisStoryTouches.\n"+
			"    R6.2's subject is a file this story MODIFIES, and D9 says \"provably edits\".\n"+
			"    Remedy: add it to the list, and remove the typed widths it carries.", rel)
	}

	if hits := typedWidthsIn(t, filepath.Join(repoRoot, rel)); len(hits) > 0 {
		t.Errorf("%s still declares %d fixed column width(s): %s.\n"+
			"    Remedy: measure the widest atom each loop prints — render.ColumnWidth over the\n"+
			"    values, then pad to it — the way the other files this story touched were changed.",
			rel, len(hits), strings.Join(hits, ", "))
	}
}

// TestSubjectListAccountsForTheDiff is the general half: the list is checked
// against what the story provably edited, so the next drift is caught by the
// guard rather than by an audit two stories later.
//
// It skips rather than fails when git cannot answer, and names the reason. A
// silent skip would be indistinguishable from a pass, which is the failure mode
// this whole sub-task is about.
func TestSubjectListAccountsForTheDiff(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("cross-check skipped: git is not on PATH, so the diff cannot be read")
	}

	verify := exec.Command("git", "-C", repoRoot, "cat-file", "-e", storyBaseCommit+"^{commit}")
	if err := verify.Run(); err != nil {
		t.Skipf("cross-check skipped: base commit %s is not resolvable in this clone "+
			"(shallow checkout or a tag export), so there is no diff to read against", storyBaseCommit)
	}

	for _, rel := range widthDebtSubjectGaps(t) {
		t.Errorf("%s is in the diff since %s and carries a typed width, but is not in "+
			"filesThisStoryTouches.\n"+
			"    R6.2 applies to it. Remedy: add it to the list and measure the width, or — if the\n"+
			"    edit was mechanical and the width is genuinely story 047's — record the exemption\n"+
			"    in .draft/deviations.yaml so it is a decision rather than an omission.", rel, storyBaseCommit)
	}
}

// TestSubjectCrossCheckCanFail proves the cross-check is capable of reporting
// something, on a tree where it currently reports nothing.
//
// S046-R8.3: a guard that passes on the day it is written has proved nothing
// about what it would catch. Here the evidence is structural rather than a
// recorded mutation — the same sweep is run against a subject list with one
// entry removed, and it must name the file that removal orphaned.
func TestSubjectCrossCheckCanFail(t *testing.T) {
	const probe = "cmd/bentoo/overlay_autoupdate.go"

	if _, err := os.Stat(filepath.Join(repoRoot, probe)); err != nil {
		t.Skipf("cross-check probe skipped: %s is not present (%v)", probe, err)
	}
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("cross-check probe skipped: git is not on PATH")
	}

	gaps := widthDebtSubjectGapsAgainst(t, []string{})
	if len(gaps) == 0 {
		t.Error("the cross-check found no gap against an EMPTY subject list.\n" +
			"    Every diff-touched file with a typed width should be reported when the list\n" +
			"    contains nothing. A sweep that reports nothing here reports nothing ever.")
	}
}

// ============================================================================
// Authored for story 046, sub-task 13.4 — R6.2, R8.3.
//
// Two guards above this line are half-guards, and this section writes the other
// half of each. Both are RED ON ARRIVAL, on the committed tree, with nothing
// mutated and nothing removed.
//
// # Half one: a ceiling ten above the thing it is a ceiling ON
//
// design.md's Testing Strategy says the width-debt guard "publishes the
// remaining count and fails when it rises, rather than passing silently". Only
// the first clause is true today. widthDebtBaseline is 18 — story.md's count at
// the branch point, of which eight widths were then inside the files this story
// touches — and TestWidthDebtRemainderIsCounted measures the remainder OUTSIDE
// those files, which is 8. It then compares one to the other:
//
//	if remaining > widthDebtBaseline    // 8 > 18
//
// The two numbers do not count the same set, and the gap between them is live
// headroom: TEN further typed widths can land in untouched files and this suite
// stays green while printing "8" as the published debt. A number that can move
// ten steps in the wrong direction without the guard noticing is a description
// of the debt, not a check on it — the exact thing story 044 was left holding
// when it recorded three widths as debt and nobody looked again until there
// were 18.
//
// The sibling ceiling in internal/common/report/citation_test.go is the shape
// to copy: unattributedCitationDebt is 356 because the sweep printed 356, and
// that file says so in as many words — "a MEASUREMENT rather than a target".
// The remedy here is the same one: lower widthDebtBaseline to what this sweep
// measures. That is not "editing the figure to match what was found", which
// width_debt_test.go:117 rightly forbids — that forbids RAISING the pin to
// swallow a violation, and TestWidthDebtBaselineNeverRises below turns the
// prohibition into a check instead of leaving it a comment.
//
// # Half two: a list that may name a file the diff does not contain
//
// TestSubjectListAccountsForTheDiff checks one direction — every diff-touched
// file with a typed width is on the list. Nothing checks the converse, and the
// converse is not decoration: filesThisStoryTouches is subtracted from the
// counted remainder, so a name added to it removes that file's typed widths
// from the published debt. An entry the diff does not support is therefore an
// unaudited discount on the number story 047 is handed.
//
// internal/common/report/render/style.go is such an entry today. It has been on
// the list since the list existed; `git diff --stat c8e347e HEAD -- <it>` is
// empty, so it is byte-identical to the branch point and this story provably
// did not modify it. It carries no typed width, so the discount is currently
// zero — which is precisely why this has to be caught by a rule rather than by
// the number looking wrong. The next such entry need not be harmless.
//
// # Why both halves, and in this order
//
// A list and a diff are two declarations of one set, and R6.2's subject is the
// set. A cross-check that only runs list ⊇ diff is satisfied by a list that
// names the whole repository; one that only runs list ⊆ diff is satisfied by an
// empty list. Each direction alone can be met by breaking the other, so both
// are written, and the hostile fixture — style.go, the entry that is in the
// list and not in the diff — is asserted before the benign structural probe
// that follows it.
// ============================================================================

// widthDebtHighWaterMark is the highest value widthDebtBaseline may hold.
//
// It exists so that "the pin may only fall" stops being a sentence in a comment
// and becomes something a run can refuse. Equality with the measurement (below)
// removes the headroom but does not on its own stop a future growth from being
// absorbed by re-pinning upward; this constant does. Repaying debt lowers
// widthDebtBaseline and may lower this one with it. Neither may go up.
//
// LOWERED 18 -> 8, sub-task 13.4, 2026-08-30, under the clause above. 18 was
// story.md's count at c8e347e, the figure this story was handed; 13.4 repaid the
// difference and pinned the baseline on its measurement of 8. Leaving this at 18
// would have left exactly the hole the constant exists to close: a growth to any
// value up to 18 could still be absorbed by re-pinning BOTH numbers, which keeps
// them equal and so satisfies the measurement check too. The ratchet is only a
// ratchet where it sits on the measurement.
// LOWERED 8 -> 7, sub-task 14.2, 2026-09-01, under the same clause. The pin
// fell to 7 when the eighth unit turned out to be a stale sentence rather than
// a live typed width (width_debt_test.go's ledger records which). Leaving this
// at 8 would leave one step of exactly the headroom 13.4 closed at eighteen: a
// growth back to 8 could still be absorbed by re-pinning BOTH numbers, keeping
// them equal and so satisfying the measurement check too.
const widthDebtHighWaterMark = 7

// measuredWidthDebtRemainder counts the typed widths in files this story does
// not touch, and returns the per-file breakdown with it.
//
// The walk is deliberately identical to the one inside
// TestWidthDebtRemainderIsCounted — same skipped directories, same exclusion of
// _test.go, same typedWidthsIn detector — because the assertion below is that
// the PUBLISHED number equals the MEASURED one, and a second measurement taken
// differently would compare two things that were never the same. Whoever fixes
// this is welcome to delete the duplication by having that test call this
// function; what must not happen is the two drifting, because then the pin
// would be exact against a count nobody publishes.
func measuredWidthDebtRemainder(t *testing.T) (int, map[string]int) {
	t.Helper()

	touched := make(map[string]bool, len(filesThisStoryTouches))
	for _, rel := range filesThisStoryTouches {
		touched[rel] = true
	}

	remaining := 0
	perFile := map[string]int{}

	err := filepath.WalkDir(repoRoot, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			// .claude is on this list for the reason .git and .epic are on it:
			// it is not project source. It holds the agent worktrees this
			// project's own tooling creates, and a worktree is a FULL SECOND
			// COPY of the repository — including, in a worktree left at
			// c8e347e, every width this story measured away. Walking into two
			// of them counted 26 more and put the debt at 33 against a ceiling
			// of 7. The guard then failed on any machine that had run the
			// tooling and passed on CI, which checks out clean: a guard that
			// fires on the developer rather than on the change teaches people
			// to ignore it.
			switch entry.Name() {
			case ".git", ".epic", ".claude", "testdata", "vendor", "node_modules":
				return fs.SkipDir
			}
			return nil
		}

		name := entry.Name()
		if !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			return nil
		}

		rel, relErr := filepath.Rel(repoRoot, path)
		if relErr != nil {
			return relErr
		}
		if touched[filepath.ToSlash(rel)] {
			return nil
		}

		if hits := typedWidthsIn(t, path); len(hits) > 0 {
			perFile[filepath.ToSlash(rel)] = len(hits)
			remaining += len(hits)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walking the repository: %v", err)
	}

	return remaining, perFile
}

// TestWidthDebtBaselineEqualsTheMeasurement is the ceiling with its headroom
// taken out: the published debt figure must BE the measurement, not sit above
// it.
//
// Equality rather than an upper bound, and the message says which direction was
// broken, because the two directions have opposite remedies:
//
//   - measured ABOVE the pin — the debt grew. Constraint 8. The remedy is the
//     width, never the number.
//   - measured BELOW the pin — the pin is stale. Someone repaid debt, or the
//     pin never matched in the first place, and every step of the difference is
//     a typed width that can land in an untouched file with the suite green.
//
// RED ON ARRIVAL: measured 8, pinned 18. Not a mutation and not a missing
// symbol — both numbers were in the committed tree, and the second one is
// printed by TestWidthDebtRemainderIsCounted's own log line on every run.
//
// CORRECTED, sub-task 13.4, 2026-08-30: the sentence above is true of the day
// this file was written and stopped being checkable the moment 13.4 lowered
// widthDebtBaseline onto the measurement. It is kept rather than deleted because
// it is this guard's R8.3 evidence — the failure it produced is quoted verbatim
// in .draft/red-evidence.yaml under task 13.4. The same convention, and the same
// reason for it, is used by width_debt_test.go's own header.
func TestWidthDebtBaselineEqualsTheMeasurement(t *testing.T) {
	remaining, perFile := measuredWidthDebtRemainder(t)

	files := make([]string, 0, len(perFile))
	for file := range perFile {
		files = append(files, file)
	}
	sort.Strings(files)

	t.Logf("typed column widths remaining in files this story does not touch: %d", remaining)
	for _, file := range files {
		t.Logf("    %2d  %s", perFile[file], file)
	}

	switch {
	case remaining > widthDebtBaseline:
		t.Errorf("the typed-width debt is %d, ABOVE the published ceiling of %d — it grew (Constraint 8).\n"+
			"    Remedy: measure the widths that pushed it over, listed above. The ceiling may not be\n"+
			"    raised to meet them; that is what story 044 did with three widths and what left 18.",
			remaining, widthDebtBaseline)
	case remaining < widthDebtBaseline:
		t.Errorf("the typed-width debt measures %d and widthDebtBaseline publishes %d, leaving %d step(s) of headroom.\n"+
			"    A ceiling above its own measurement is not a ceiling: %d further typed widths can land in\n"+
			"    files this story does not touch and this suite stays green while still printing %d as the\n"+
			"    debt. design.md's Testing Strategy asks this guard to \"fail when it rises\", and today it\n"+
			"    fails only when it rises past a number nothing measures.\n"+
			"    Remedy: lower widthDebtBaseline to %d, the figure this sweep prints, and say in its comment\n"+
			"    that it is a measurement — the form internal/common/report/citation_test.go pins\n"+
			"    unattributedCitationDebt in. Do not raise anything, and do not widen this assertion.",
			remaining, widthDebtBaseline, widthDebtBaseline-remaining,
			widthDebtBaseline-remaining, remaining, remaining)
	}
}

// TestWidthDebtBaselineNeverRises is the ratchet the equality above cannot
// supply on its own.
//
// Equality pins the number to the measurement; it does not stop a later growth
// from being absorbed by moving BOTH — grow the debt to nine, re-pin to nine,
// and an equality-only suite is green. What forbids that is that the pin may
// only fall, which until now lived in a comment. Here it is a check: the pin
// may never exceed the highest value it has ever held.
//
// GREEN ON ARRIVAL, and deliberately so — this one is a ratchet, and a ratchet
// that fails on the day it is installed is installed backwards. Its Red is
// reachable and cheap to see: raise widthDebtBaseline by one and it reports.
func TestWidthDebtBaselineNeverRises(t *testing.T) {
	if widthDebtBaseline > widthDebtHighWaterMark {
		t.Errorf("widthDebtBaseline is %d, above the %d this pin has previously held.\n"+
			"    The debt figure is a hand-off story 047 checks, and it may only fall — a number edited\n"+
			"    upward to accommodate what was found records nothing.\n"+
			"    Remedy: measure the widths instead. If they are genuinely story 047's, record the\n"+
			"    exemption in .draft/deviations.yaml rather than in this constant.",
			widthDebtBaseline, widthDebtHighWaterMark)
	}
}

// subjectEntriesOutsideTheDiff returns every entry of the given subject list
// that this story cannot be shown to have modified: absent from the diff since
// storyBaseCommit AND with no uncommitted change on disk.
//
// It is the converse of widthDebtSubjectGapsAgainst, and it skips on exactly
// the same conditions and says the same two reasons why. A guard that fails in
// a shallow clone is reporting the clone; a guard that skips without naming
// what it skipped over is indistinguishable from a pass, which is the failure
// mode the whole cross-check exists to close.
//
// # Why `git status` is consulted as well as the diff
//
// base..HEAD sees only what is COMMITTED. During the story's own execution a
// file can be legitimately on the list and legitimately edited in the working
// tree with the commit not yet made, and reporting that would be a guard people
// learn to ignore for the one minute of the cycle when it is wrong. An
// uncommitted modification is proof of a touch just as a commit is, so it
// counts. This widens what is accepted; it never narrows it, so it cannot mask
// an entry nothing supports — style.go is clean in the index, clean in the
// working tree and absent from the diff, and it is reported either way.
//
// If `git status` cannot be read the diff still decides, and the reason is
// logged rather than swallowed: the run is then slightly stricter than intended
// and the log says why a mid-flight file might be named.
func subjectEntriesOutsideTheDiff(t *testing.T, subject []string) []string {
	t.Helper()

	if _, err := exec.LookPath("git"); err != nil {
		t.Skipf("converse cross-check skipped: git is not on PATH (%v), so the diff cannot be read", err)
	}
	if err := exec.Command("git", "-C", repoRoot, "cat-file", "-e", storyBaseCommit+"^{commit}").Run(); err != nil {
		t.Skipf("converse cross-check skipped: base commit %s is not resolvable in this clone (%v), "+
			"so there is no diff to check the subject list against", storyBaseCommit, err)
	}

	out, err := exec.Command("git", "-C", repoRoot, "diff", "--name-only", storyBaseCommit+"..HEAD").Output()
	if err != nil {
		t.Skipf("converse cross-check skipped: `git diff --name-only %s..HEAD` could not be read (%v), "+
			"so there is no diff to check the subject list against", storyBaseCommit, err)
	}

	touched := map[string]bool{}
	for _, rel := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if rel != "" {
			touched[rel] = true
		}
	}

	// Uncommitted work counts as a touch. Porcelain v1 lines are two status
	// characters, a space, then the path; a rename carries "old -> new" and
	// both sides are marked, since either spelling may be the one on the list.
	if status, statusErr := exec.Command("git", "-C", repoRoot, "status", "--porcelain").Output(); statusErr == nil {
		for _, line := range strings.Split(strings.TrimSpace(string(status)), "\n") {
			if len(line) < 4 {
				continue
			}
			for _, path := range strings.Split(strings.TrimSpace(line[3:]), " -> ") {
				if path = strings.Trim(strings.TrimSpace(path), `"`); path != "" {
					touched[path] = true
				}
			}
		}
	} else {
		t.Logf("`git status --porcelain` could not be read (%v), so only committed work counts as a "+
			"touch here; a file edited but not yet committed may be reported below", statusErr)
	}

	var unsupported []string
	for _, rel := range subject {
		if !touched[rel] {
			unsupported = append(unsupported, rel)
		}
	}
	sort.Strings(unsupported)
	return unsupported
}

// TestSubjectListNamesNothingOutsideTheDiff is the converse half of
// TestSubjectListAccountsForTheDiff: the list may not claim a file the story
// did not touch.
//
// The direction matters numerically, which is why this is an error rather than
// a note. TestWidthDebtRemainderIsCounted subtracts every listed file from the
// counted remainder, so an unsupported entry silently discounts that file's
// typed widths from the published debt — a name on a list is enough to make
// widths disappear from the number story 047 is handed.
//
// RED ON ARRIVAL: internal/common/report/render/style.go is on the list and is
// byte-identical to c8e347e. Its discount happens to be zero widths today, and
// a rule is what catches the entry whose discount is not.
//
// CORRECTED, sub-task 13.4, 2026-08-30: that entry was removed from
// filesThisStoryTouches, so the sentence above describes a tree state that no
// longer exists. Kept for the same reason as the note in
// TestWidthDebtBaselineEqualsTheMeasurement above — it is the R8.3 evidence, and
// the failure it produced is quoted in .draft/red-evidence.yaml under task 13.4.
func TestSubjectListNamesNothingOutsideTheDiff(t *testing.T) {
	for _, rel := range subjectEntriesOutsideTheDiff(t, filesThisStoryTouches) {
		state := "on disk and unmodified since then"
		discount := 0
		if _, err := os.Stat(filepath.Join(repoRoot, rel)); err != nil {
			state = "not present on disk at all"
		} else {
			discount = len(typedWidthsIn(t, filepath.Join(repoRoot, rel)))
		}

		t.Errorf("%s is in filesThisStoryTouches and is NOT in the diff since %s — %s.\n"+
			"    R6.2's subject is a file this story MODIFIES, so the list is a claim about the diff, and\n"+
			"    this entry is a claim the diff does not support. It is not cosmetic: the remainder count\n"+
			"    subtracts every listed file, so this entry discounts %d typed width(s) from the published\n"+
			"    debt without anything having been repaid.\n"+
			"    Remedy: remove it from the list. If the story is about to touch it, touch it first — an\n"+
			"    uncommitted edit already counts here.", rel, storyBaseCommit, state, discount)
	}
}

// TestSubjectListConverseCanFail proves the converse sweep is capable of
// reporting something, in the shape TestSubjectCrossCheckCanFail uses for the
// forward one (S046-R8.3).
//
// The probe is a real production file that this story genuinely did not touch,
// fed to the sweep as if someone had listed it. A sweep that cannot report THAT
// reports nothing ever — and the two failure modes it guards against are both
// live: a `touched` set that silently swallowed everything (a `git status`
// parse that matched every path) and an accounting loop that never fires.
//
// GREEN ON ARRIVAL, unlike the two above: it is structural evidence, not a
// finding. It skips, naming the reason, if the probe file stops existing or
// ever enters the diff — at which point it would be testing nothing and should
// be re-pointed rather than deleted.
func TestSubjectListConverseCanFail(t *testing.T) {
	// RE-POINTED, sub-task 14.2: the probe was
	// misc/design/design-system/component/catalogue.go until that sub-task
	// edited it, which put it in the diff and left this check asserting
	// nothing. The failure it raised is quoted in .draft/red-evidence.yaml —
	// the probe announcing its own fixture had gone benign is the capability
	// this test exists to have. internal/autoupdate/ is outside every part of
	// this story's subject: its internal/ work is common/report, overlay and
	// snapshot.
	//
	// RE-POINTED AGAIN, 2026-09-15: checker.go itself entered the diff when the
	// revive scan's filtered-entry fix landed, and the probe announced its own
	// fixture had gone benign — the capability above, working. doc.go replaces
	// it: a package doc with no behaviour to change, so nothing routes an edit
	// through it, and it is as far outside this story's subject as checker.go
	// was.
	const probe = "internal/autoupdate/doc.go"

	if _, err := os.Stat(filepath.Join(repoRoot, probe)); err != nil {
		t.Skipf("converse probe skipped: %s is not present (%v)", probe, err)
	}

	unsupported := subjectEntriesOutsideTheDiff(t, []string{probe})
	if len(unsupported) == 0 {
		t.Errorf("the converse cross-check found nothing against a subject list whose only entry is %s,\n"+
			"    a file this story does not touch. Either the sweep counts every path as touched, or its\n"+
			"    accounting loop never reports at all.\n"+
			"    If that file has since entered the diff, re-point this probe at one still outside it —\n"+
			"    the check is what stops the converse sweep going quiet.", probe)
	}
}
