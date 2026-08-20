// NEW FILE — materialize as cmd/bentoo/overlay_staged_clean_test.go.
//
// This is the one authored file that is not an append: no existing test file
// covers `overlay staged clean`, and the house layout is one _test.go beside the
// command's own file (overlay_prune_test.go, overlay_autoupdate_sweep_test.go).
// Materialize it whole for task 4.1 and leave the later tasks' sections in
// place, or split it by the section markers — the sections are independent.
//
// # Two names this file pins, and how to change them
//
// Everything else is driven through things that already exist. Only two
// identifiers are the implementation's to provide, and both follow the house
// pattern the design names (D5, "Test seams"):
//
//   - runStagedClean(ctx, overlayPath, stagingRoot) — the testable half, split
//     from the cobra half exactly as runPrune is split from runPruneCmd and
//     runSweep from its caller: "everything that can be decided without config
//     and the overlay path lives in runPrune, which takes both as parameters so
//     the whole flow is drivable from a test".
//   - confirmStagedCleanFn — the confirmation seam, defaulting to confirmAction.
//     It cannot be confirmSweepFn: that name is taken (constraint in story.md).
//   - stagedCleanYes — this subcommand's OWN --yes flag variable. Not
//     autoupdateYes: that one is `overlay autoupdate --yes`, and driving it here
//     would set a flag the command under test never reads, so the R2.3 test would
//     take the non-interactive refusal path and pass while asserting the opposite
//     of what it claims.
//
// If the implementation spells them differently, rename them HERE and nowhere
// else. The assertions are the contract; the identifiers are not.
//
// # What is deliberately NOT stubbed
//
// The planner and the executor are the real ones, over a real staging root laid
// out with validate.Stage. A CLI test that stubs both is a test of its own
// stubs: the fidelity that matters here is that the command's idea of "a tree
// that may be removed" is the validate package's idea of it, and an inline fake
// plan would assert the opposite of that.

package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/obentoo/bentoolkit/internal/autoupdate/validate"
)

// stagedCleanTree lays out one staged tree under stagingRoot the way every
// producer does — through validate.Stage itself, so the recognition marker is
// written by the code that owns its spelling — and records the given gate
// outcome beside it.
//
// outcome "" leaves the tree without a record, which is the third case the
// retention policy distinguishes: outcome unknown, therefore kept.
func stagedCleanTree(t *testing.T, overlay, stagingRoot, atom, version string, outcome validate.Outcome) string {
	t.Helper()
	staged, err := validate.Stage(validate.StageRequest{
		Overlay:     overlay,
		StagingRoot: stagingRoot,
		Key:         atom,
		Version:     version,
		EbuildBytes: []byte("EAPI=8\nDESCRIPTION=\"t\"\nSLOT=\"0\"\nKEYWORDS=\"~amd64\"\n"),
	})
	if err != nil {
		t.Fatalf("staging %s-%s: %v", atom, version, err)
	}
	if outcome != "" {
		if err := validate.WriteStageRecord(staged, validate.StageRecord{
			Package: atom, Version: version, Depth: validate.DepthConfigure,
			Gates: []validate.GateResult{{
				Gate: validate.GateConfigure, Outcome: outcome, Reason: "recorded by the fixture",
			}},
		}); err != nil {
			t.Fatalf("recording %s-%s: %v", atom, version, err)
		}
	}
	return staged
}

// stagedCleanFixture is a staging root holding both kinds of tree: two whose
// gates passed (removable) and one whose gate FAILED (the artifact an operator
// still needs, kept). The overlay is a separate directory, because a staging
// root inside the overlay is refused before anything is read.
func stagedCleanFixture(t *testing.T) (overlay, stagingRoot string, removable, protected []string) {
	t.Helper()
	overlay = filepath.Join(t.TempDir(), "overlay")
	if err := os.MkdirAll(overlay, 0o750); err != nil {
		t.Fatalf("laying out the overlay: %v", err)
	}
	stagingRoot = filepath.Join(t.TempDir(), "staging")

	removable = []string{
		stagedCleanTree(t, overlay, stagingRoot, "media-libs/gst-plugins-base", "1.28.6", validate.OutcomePass),
		stagedCleanTree(t, overlay, stagingRoot, "media-plugins/gst-plugins-qt6", "1.29.2", validate.OutcomePass),
	}
	protected = []string{
		stagedCleanTree(t, overlay, stagingRoot, "dev-qt/qtbase", "6.9.1", validate.OutcomeFailed),
	}
	return overlay, stagingRoot, removable, protected
}

// stagedRootFingerprint is every byte under root as one string: every path, and
// the digest of every file's content.
//
// R2.1 is "leave the staging root BYTE-IDENTICAL", and the success metric says
// it is measured by comparing a listing before and after. A listing of the top
// level is not enough — a run that emptied a tree and left its directory would
// pass one — so this walks all of it.
func stagedRootFingerprint(t *testing.T, root string) string {
	t.Helper()
	var lines []string
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, relErr := filepath.Rel(root, path)
		if relErr != nil {
			return relErr
		}
		if info.IsDir() {
			lines = append(lines, "d "+rel)
			return nil
		}
		body, readErr := os.ReadFile(path) //nolint:gosec // path comes from walking a t.TempDir()
		if readErr != nil {
			return readErr
		}
		sum := sha256.Sum256(body)
		lines = append(lines, "f "+rel+" "+hex.EncodeToString(sum[:]))
		return nil
	})
	if err != nil {
		t.Fatalf("fingerprinting %s: %v", root, err)
	}
	sort.Strings(lines)
	sum := sha256.Sum256([]byte(strings.Join(lines, "\n")))
	return hex.EncodeToString(sum[:])
}

// setStagedCleanConfirm pins the confirmation seam for one test and restores it.
// countingConfirm also reports whether the prompt was reached at all, which is
// what R1.3 and R2.2 are about: not "the answer was no" but "the question was
// never asked".
func setStagedCleanConfirm(t *testing.T, answer bool, calls *int) {
	t.Helper()
	orig := confirmStagedCleanFn
	t.Cleanup(func() { confirmStagedCleanFn = orig })
	confirmStagedCleanFn = func(string) bool {
		*calls++
		return answer
	}
}

// setStagedCleanYes pins this subcommand's --yes for one test and restores it
// after. The flag is a process global and its default is a safety property — an
// unattended run that removes directories — so it is never left mutated.
//
// It is a helper of its own rather than the reconcile one, which mutates
// autoupdateYes: that is a DIFFERENT command's flag, and a test driving it would
// be asserting against a variable the code under test does not read.
func setStagedCleanYes(t *testing.T, v bool) {
	t.Helper()
	orig := stagedCleanYes
	t.Cleanup(func() { stagedCleanYes = orig })
	stagedCleanYes = v
}

// stagedCleanCmd finds the `clean` subcommand of `overlay staged`, or fails.
func stagedCleanCmd(t *testing.T) *cobraCommandUnderTest {
	t.Helper()
	for _, cmd := range overlayCmd.Commands() {
		if cmd.Use != "staged" && !strings.HasPrefix(cmd.Use, "staged ") {
			continue
		}
		for _, sub := range cmd.Commands() {
			if sub.Use == "clean" || strings.HasPrefix(sub.Use, "clean ") {
				return &cobraCommandUnderTest{Use: sub.Use, Short: sub.Short, Long: sub.Long}
			}
		}
		t.Fatalf("`overlay staged` is registered but has no `clean` subcommand; it has %d subcommand(s)",
			len(cmd.Commands()))
	}
	t.Fatal("no `staged` subcommand is registered under `overlay`; the sweeper story 039 shipped without a " +
		"caller still has none, and every assertion below is about a door that does not exist")
	return nil
}

// cobraCommandUnderTest carries the three fields these tests read, so the
// helper above can fail from one place instead of returning a nil *cobra.Command
// every caller has to check.
type cobraCommandUnderTest struct{ Use, Short, Long string }

// =============================================================================
// Task 4.1 — R6.1, R6.2: a new door, resolved where the producers write
// =============================================================================

// TestStagedClean_IsRegisteredAsItsOwnSubcommandAndMovesNothing is R6.2's
// structural half, and the naming argument from D1 turned into an assertion.
//
// `sweep` and `--clean` already mean, to this operator, a DELETION FROM A
// PUBLISHED OVERLAY that auto-commits and pushes within minutes. A
// scratch-directory cleanup has the inverse risk profile, and a flag on
// `overlay autoupdate` would sit one keystroke from the one that publishes
// deletions. So: a subcommand of its own, and the autoupdate command gains
// nothing.
func TestStagedClean_IsRegisteredAsItsOwnSubcommandAndMovesNothing(t *testing.T) {
	cmd := stagedCleanCmd(t)

	if cmd.Short == "" || cmd.Long == "" {
		t.Error("`overlay staged clean` has no description; every other overlay subcommand carries both, and " +
			"this one removes directories")
	}

	// The mirror of S027-G6: the new command must not arrive as a modifier on an
	// invocation that already does something else, or an existing `--apply … `
	// run silently becomes a sweep of the shared staging root.
	for _, name := range []string{"clean-staging", "staged-clean", "clean-staged"} {
		if autoupdateCmd.Flags().Lookup(name) != nil {
			t.Errorf("`overlay autoupdate` grew a --%s flag; the staged-tree cleanup is a standalone subcommand "+
				"precisely so that no existing invocation can be converted into a whole-root sweep (D1, R6.2)", name)
		}
	}
}

// TestStagedClean_ResolvesTheStagingRootWithTheProducersOwnResolver is R6.1, and
// it is asserted over the SOURCE for a reason.
//
// The requirement is not "the command finds some staging root" — a test can
// always hand it one. It is that the command cannot look somewhere the producers
// do not write, and that is a property of there being ONE spelling of the path:
// autoupdateStagingRoot(). A second spelling would keep passing every behavioural
// test in this file, because those supply the root themselves, and would then
// sweep an empty directory forever on the operator's machine — reporting
// "nothing to sweep" over a staging root that is full.
//
// The precedent for asserting a source-level fact is in this package already:
// TestSweepRoutingKeepsApplyClean pins the ordering of a switch the same way.
func TestStagedClean_ResolvesTheStagingRootWithTheProducersOwnResolver(t *testing.T) {
	fset := token.NewFileSet()
	pkgs, err := parser.ParseDir(fset, ".", func(fi os.FileInfo) bool {
		return !strings.HasSuffix(fi.Name(), "_test.go")
	}, 0)
	if err != nil {
		t.Fatalf("parsing package main: %v", err)
	}

	callers := 0
	for _, pkg := range pkgs {
		for name, file := range pkg.Files {
			if !fileMentions(file, "PlanStagedSweep") {
				continue
			}
			callers++
			if !fileMentions(file, "autoupdateStagingRoot") {
				t.Errorf("%s plans a staged sweep but never calls autoupdateStagingRoot(); the sweep has to "+
					"resolve the root with the same resolver the producers use, or it cannot look where they "+
					"write (R6.1)", name)
			}
			// The two ways a second spelling gets written. Neither belongs in a
			// file that already has the answer one call away.
			for _, forbidden := range []string{"UserHomeDir", "stagingDirName"} {
				if fileMentions(file, forbidden) {
					t.Errorf("%s derives a staging path through %s of its own; there is one resolver and this "+
						"is a second spelling of it, which goes stale silently (R6.1)", name, forbidden)
				}
			}
		}
	}
	if callers == 0 {
		t.Fatal("no file in package main references PlanStagedSweep, so the command does not plan a staged " +
			"sweep at all and this test asserted nothing")
	}
}

// fileMentions answers whether the file names ident anywhere in its CODE.
// Comments are not part of the AST, which is the point: a prose sentence quoting
// autoupdateStagingRoot() must not satisfy the assertion above.
func fileMentions(file *ast.File, ident string) bool {
	found := false
	ast.Inspect(file, func(n ast.Node) bool {
		switch node := n.(type) {
		case *ast.Ident:
			if node.Name == ident {
				found = true
			}
		case *ast.SelectorExpr:
			if node.Sel != nil && node.Sel.Name == ident {
				found = true
			}
		}
		return !found
	})
	return found
}

// =============================================================================
// Task 4.2 — R1.2, R1.3: the whole plan is printed, and an empty one does not
// ask a question
// =============================================================================

// TestStagedClean_PrintsEveryEntryOfThePlan is R1.2.
//
// The fixture is deliberately larger than a page of the eye: a truncation ("… and
// 3 more") hides exactly the line that is wrong, and the operator approves the
// whole batch on one keystroke. Every removal AND every keep is looked for,
// because a plan that lists only what it will delete gives no way to tell "this
// tree is protected" from "the sweep never saw it".
func TestStagedClean_PrintsEveryEntryOfThePlan(t *testing.T) {
	overlay := filepath.Join(t.TempDir(), "overlay")
	if err := os.MkdirAll(overlay, 0o750); err != nil {
		t.Fatalf("laying out the overlay: %v", err)
	}
	stagingRoot := filepath.Join(t.TempDir(), "staging")

	var expected []string
	for _, spec := range []struct {
		atom, version string
		outcome       validate.Outcome
	}{
		{"media-libs/gst-plugins-base", "1.28.6", validate.OutcomePass},
		{"media-plugins/gst-plugins-qt6", "1.29.2", validate.OutcomePass},
		{"dev-libs/glib", "2.84.0", validate.OutcomePass},
		{"app-editors/zed", "0.199.0", validate.OutcomePass},
		{"dev-qt/qtbase", "6.9.1", validate.OutcomeFailed},
		{"dev-qt/qtdeclarative", "6.9.1", validate.OutcomeFailed},
		{"net-libs/nghttp2", "1.66.0", ""},
	} {
		expected = append(expected,
			stagedCleanTree(t, overlay, stagingRoot, spec.atom, spec.version, spec.outcome))
	}

	calls := 0
	setStagedCleanConfirm(t, false, &calls)

	out := captureStdout(t, func() {
		runStagedClean(context.Background(), overlay, stagingRoot)
	})

	for _, path := range expected {
		if !strings.Contains(out, path) {
			t.Errorf("the printed plan does not name %s. Every entry, never a sample or a truncation: the "+
				"operator approves one batch, and the line that is wrong is the one a truncation drops (R1.2)",
				path)
		}
	}
}

// TestStagedClean_AnEmptyPlanSaysWhichEmptinessItIsAndDoesNotPrompt is R1.3, and
// the two cases below are the whole requirement — the fact alone is not enough.
//
// "Nothing to sweep" after finding nothing, and "nothing to sweep" after finding
// three protected trees, are different facts about the operator's machine. The
// second sentence is the one that stops an operator from concluding the staging
// root is empty when it is full of failures they still need. S027-R7.1 made
// exactly this correction for the overlay sweep; this is the same rule at a new
// command.
//
// The prompt count is asserted in both: asking "remove 0 trees?" trains the
// operator to say yes without reading.
func TestStagedClean_AnEmptyPlanSaysWhichEmptinessItIsAndDoesNotPrompt(t *testing.T) {
	t.Run("nothing was found", func(t *testing.T) {
		overlay := filepath.Join(t.TempDir(), "overlay")
		if err := os.MkdirAll(overlay, 0o750); err != nil {
			t.Fatalf("laying out the overlay: %v", err)
		}
		empty := filepath.Join(t.TempDir(), "staging")

		calls := 0
		setStagedCleanConfirm(t, true, &calls)

		out := strings.ToLower(captureStdout(t, func() {
			runStagedClean(context.Background(), overlay, empty)
		}))

		if calls != 0 {
			t.Errorf("the command asked for a confirmation %d time(s) over a plan that removes nothing (R1.3)", calls)
		}
		if !strings.Contains(out, "nothing") && !strings.Contains(out, "no staged tree") {
			t.Errorf("the output over an empty staging root does not say that nothing was found: %q", out)
		}
		if strings.Contains(out, "protect") {
			t.Errorf("the output says something was protected although the staging root is empty; the two "+
				"emptinesses are different facts and reporting the wrong one is the S027-R7.1 defect: %q", out)
		}
	})

	t.Run("everything present was protected", func(t *testing.T) {
		overlay := filepath.Join(t.TempDir(), "overlay")
		if err := os.MkdirAll(overlay, 0o750); err != nil {
			t.Fatalf("laying out the overlay: %v", err)
		}
		stagingRoot := filepath.Join(t.TempDir(), "staging")
		failed := stagedCleanTree(t, overlay, stagingRoot, "dev-qt/qtbase", "6.9.1", validate.OutcomeFailed)
		unknown := stagedCleanTree(t, overlay, stagingRoot, "net-libs/nghttp2", "1.66.0", "")

		calls := 0
		setStagedCleanConfirm(t, true, &calls)

		out := captureStdout(t, func() {
			runStagedClean(context.Background(), overlay, stagingRoot)
		})
		low := strings.ToLower(out)

		if calls != 0 {
			t.Errorf("the command asked for a confirmation %d time(s) although its plan removes nothing (R1.3)", calls)
		}
		if !strings.Contains(low, "protect") {
			t.Errorf("the output does not say that everything found was protected: %q. Saying \"nothing was "+
				"found\" here contradicts the very list printed above it", out)
		}
		for _, path := range []string{failed, unknown} {
			if !strings.Contains(out, path) {
				t.Errorf("the protected tree %s is not named in the output; the reason a sweep removed nothing "+
					"has to be readable per tree (R1.2, R1.3)", path)
			}
		}
	})
}

// =============================================================================
// Task 4.3 — R2.1, R2.2, R2.3: nothing is removed without consent
// =============================================================================

// TestStagedClean_ADeclinedConfirmationLeavesTheStagingRootByteIdentical is
// R2.1, and it is the story's own success metric: "measured by comparing a
// listing before and after".
//
// The assertion is the fingerprint, not the survival of one tree. A test that
// checked only that the removable trees still exist would pass against a run
// that entered the removal path and failed halfway through one of them — which
// is the state R2.1 exists to make unreachable, since a declined sweep must not
// enter the removal path at all.
func TestStagedClean_ADeclinedConfirmationLeavesTheStagingRootByteIdentical(t *testing.T) {
	overlay, stagingRoot, removable, _ := stagedCleanFixture(t)
	before := stagedRootFingerprint(t, stagingRoot)

	calls := 0
	setStagedCleanConfirm(t, false, &calls)
	setReconcileInteractive(t, func() bool { return true })
	setStagedCleanYes(t, false)

	out := captureStdout(t, func() {
		runStagedClean(context.Background(), overlay, stagingRoot)
	})

	if calls != 1 {
		t.Fatalf("the confirmation was asked %d time(s), want exactly 1; with no question asked this test says "+
			"nothing about what a NO does, and with several the operator is worn down into a yes", calls)
	}
	if after := stagedRootFingerprint(t, stagingRoot); after != before {
		t.Errorf("a declined sweep changed the staging root: %s -> %s (R2.1)", before, after)
	}
	// Named separately so a failure says which tree went, and to keep the
	// fingerprint from being the only reader of the fixture.
	for _, path := range removable {
		if _, err := os.Stat(path); err != nil {
			t.Errorf("the tree %s the operator declined to remove is gone (%v)", path, err)
		}
		if !strings.Contains(out, path) {
			t.Errorf("%s was planned for removal and never printed, so the operator declined a list that did "+
				"not name it (R1.2)", path)
		}
	}
}

// TestStagedClean_ANonInteractiveSessionRefusesAndStillPrints is R2.2, and the
// "still prints" half is the requirement.
//
// A silent refusal reads as "nothing to sweep" — the operator learns the wrong
// fact about their machine and the command looks broken rather than careful. So
// the plan is printed, nothing is removed, and the flag that would authorize it
// is named in the output rather than left to the help text.
//
// registryPromptIsInteractive requires BOTH stdin and stdout to be TTYs, so
// `yes | bentoo …` cannot answer for a human; it is pinned false here because a
// test binary's streams are not terminals anyway and an accidental change of
// that condition would silently switch this test to the interactive path.
func TestStagedClean_ANonInteractiveSessionRefusesAndStillPrints(t *testing.T) {
	overlay, stagingRoot, removable, _ := stagedCleanFixture(t)
	before := stagedRootFingerprint(t, stagingRoot)

	calls := 0
	setStagedCleanConfirm(t, true, &calls)
	setReconcileInteractive(t, func() bool { return false })
	setStagedCleanYes(t, false)

	out := captureStdout(t, func() {
		runStagedClean(context.Background(), overlay, stagingRoot)
	})

	if calls != 0 {
		t.Errorf("the confirmation seam was consulted %d time(s) in a non-interactive session; a prompt nobody "+
			"can answer is either a hang or an assumed yes (R2.2)", calls)
	}
	if after := stagedRootFingerprint(t, stagingRoot); after != before {
		t.Errorf("a non-interactive run changed the staging root: %s -> %s (R2.2)", before, after)
	}
	for _, path := range removable {
		if !strings.Contains(out, path) {
			t.Errorf("the refusal did not print %s; a refusal that prints nothing is indistinguishable from a "+
				"staging root with nothing in it (R2.2)", path)
		}
	}
	if !strings.Contains(out, "--yes") {
		t.Errorf("the refusal does not name --yes: %q. The operator is left knowing only that nothing happened "+
			"(R2.2)", out)
	}
}

// TestStagedClean_TheAuthorizingFlagWarnsAndProceeds is R2.3.
//
// Both halves matter and each fails differently: a flag that proceeds without
// warning removes trees unannounced in a session nobody is watching, and a flag
// that warns without proceeding makes the unattended path impossible. The
// protected tree is asserted to survive in the same run, because "--yes"
// authorizes the PLAN, never a wider sweep than the one that was printed.
func TestStagedClean_TheAuthorizingFlagWarnsAndProceeds(t *testing.T) {
	overlay, stagingRoot, removable, protected := stagedCleanFixture(t)

	calls := 0
	setStagedCleanConfirm(t, false, &calls)
	setReconcileInteractive(t, func() bool { return false })
	setStagedCleanYes(t, true)

	out := captureStdout(t, func() {
		runStagedClean(context.Background(), overlay, stagingRoot)
	})

	if calls != 0 {
		t.Errorf("--yes still went through the confirmation seam %d time(s); the flag means \"do not ask\", and "+
			"a seam returning false here would make the flag do nothing at all", calls)
	}
	for _, path := range removable {
		if _, err := os.Stat(path); err == nil {
			t.Errorf("--yes was given and %s is still there; the flag's whole purpose is the unattended run (R2.3)", path)
		}
	}
	for _, path := range protected {
		if _, err := os.Stat(path); err != nil {
			t.Errorf("--yes removed %s, whose gate FAILED (%v); the flag authorizes the plan that was printed, "+
				"and the retention policy is not part of what it waives", path, err)
		}
	}
	low := strings.ToLower(out)
	if !strings.Contains(low, "--yes") && !strings.Contains(low, "without a prompt") && !strings.Contains(low, "unattended") {
		t.Errorf("the run removed trees without saying that it was proceeding unprompted: %q (R2.3)", out)
	}
}

// =============================================================================
// Task 4.4 — R6.3: the help states the concurrency the design accepts
// =============================================================================

// TestStagedClean_HelpStatesThatAConcurrentRunsTreeCanBeRemoved is R6.3, and it
// is the one place this hazard is written down for an operator.
//
// D8 refuses a lock, a pid file and an ownership marker on purpose: the
// retention rule is expressed by the path, which is why several packages can be
// staged concurrently with no coordination at all, and story 039 rejected a
// central index one story ago. The accepted consequence is that a sweep running
// beside an `--apply` can remove a tree that run is about to use. The cost is
// bounded — the apply fails with a working directory that does not exist, which
// story 040 classifies as an environment failure, so the LLM fixer is not
// invoked and nothing wrong is published — but "bounded" is not "invisible", and
// the operator learns it here rather than from a failed run at 2am.
func TestStagedClean_HelpStatesThatAConcurrentRunsTreeCanBeRemoved(t *testing.T) {
	cmd := stagedCleanCmd(t)
	help := strings.ToLower(cmd.Short + "\n" + cmd.Long)

	saysConcurrent := strings.Contains(help, "concurrent") ||
		strings.Contains(help, "at the same time") ||
		strings.Contains(help, "another run") ||
		strings.Contains(help, "running beside")
	if !saysConcurrent {
		t.Errorf("the help never mentions another run happening at the same time: %q. There is no lock on the "+
			"staging root by design, so this is the only warning an operator gets (R6.3, D8)", cmd.Long)
	}

	saysItCanBeRemoved := strings.Contains(help, "remove") || strings.Contains(help, "delete")
	if !saysItCanBeRemoved {
		t.Errorf("the help mentions a concurrent run but not that its tree can be REMOVED: %q. The hazard is "+
			"the removal, and a help text that only says \"be careful\" states nothing (R6.3)", cmd.Long)
	}
}
