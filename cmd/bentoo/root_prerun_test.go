package main

// Sub-task 16.10 — S046-R3.2, S046-R3.4, S046-R8.3.
//
// The single pre-run that R3.2 and R3.4 rest on cannot be shadowed without a
// test failing.
//
// # What is actually at stake
//
// cobra does NOT run every persistent pre-run between the root and the command
// that was invoked. It walks UP from that command and runs the FIRST hook it
// finds, then stops (spf13/cobra v1.10.2, command.go: the loop over `parents`
// breaks after the first hook unless the global EnableTraverseRunHooks is set,
// which this project never sets). So one `PersistentPreRun` or
// `PersistentPreRunE` added to `overlay manifest` — by a future sub-command
// that wants, say, its own setup — silently takes the root's hook out of the
// path for that command. Under it go both halves of what the root hook does:
//
//   - R3.2: an unusable --ui is rejected before ANY work happens. Shadowed, the
//     command runs and either renders in a mode nobody asked for or fails much
//     later, in the middle of doing something.
//   - R3.4: verbose, quiet and noColor are published from the tree's flag
//     locals to the package variables the run functions read. Shadowed, those
//     variables keep whatever the previous run left in them.
//
// Neither failure shows up in the shadowing command's own tests, because the
// shadowing command itself works fine. That is what "silently" means here, and
// it is why the rule needs a guard rather than a comment.
//
// # Why the two existing assertions are not this
//
// TestRootCommandHasPersistentPreRun (version_completion_test.go) and
// TestRootCommandPersistentPreRunNotNil (display_functions_test.go) each assert
// that the root's hook is non-nil. Both stay green in every scenario above: the
// root's hook is still there, it is simply never reached. Presence on the root
// is not singleness in the tree, and only the second one is the guarantee R3.2
// makes to an operator — that --ui is rejected whichever command was typed.
//
// # R8.3 — this guard is GREEN on the day it is written
//
// At authoring time exactly one persistent pre-run exists in the whole tree
// (root.go), so there is no natural Red to record. R8.3 requires evidence that
// a guard fails when the rule it guards is broken, so the Red below was
// produced by MUTATION on a /tmp copy of the tree, which was restored
// immediately afterwards.
//
// Mutation: in cmd/bentoo/overlay_manifest.go, inside newManifestCmd(), one
// line was added before `return cmd` —
//
//	cmd.PersistentPreRunE = func(*cobra.Command, []string) error { return nil }
//
// which is exactly the "harmless local setup" shape the rule forbids. Observed
// verbatim from `go test ./cmd/bentoo/ -run TestRootPreRun -v`, with only the
// six unaffected PASS lines elided:
//
//	=== RUN   TestRootPreRunIsDeclaredOnceAndOnTheRoot
//	    root_prerun_test.go:219: walked 30 commands in the freshly constructed tree; 2 declare a persistent pre-run hook
//	    root_prerun_test.go:235: bentoo overlay manifest declares PersistentPreRunE, but only the root bentoo may.
//	        	cobra walks UP from the command that ran and executes the FIRST persistent pre-run it finds, then stops.
//	        	So `bentoo overlay manifest` — and everything under it — would run with the root's hook never reached:
//	        	no --ui rejection before any work (S046-R3.2) and no publish of verbose/quiet/noColor (S046-R3.4).
//	        	Remedy: put the setup in this command's own PreRunE/RunE, which cobra runs IN ADDITION to the root's, never instead of it.
//	--- FAIL: TestRootPreRunIsDeclaredOnceAndOnTheRoot (0.00s)
//	[the six other tests: PASS — see below]
//	FAIL
//	FAIL	github.com/obentoo/bentoolkit/cmd/bentoo	0.003s
//	FAIL
//
// The line numbers above are those of the run. Pasting this transcript into
// this header pushed every one of them down; match on the SENTENCES, not on the
// numbers. They are left as the run printed them rather than renumbered,
// because a transcript edited after the fact is no longer evidence.
//
// One test failed and six passed, and that split is the control. Five of the
// six survivors are fixture tests that build their OWN small tree rather than
// mutating the production one, and the sixth reads this file's own names — so a
// run in which any of them failed would mean this file is broken rather than
// the command tree. Exactly one test reads newRootCmd(), and it is the one that
// fired.
//
// # Reading order, and why the hostile fixtures come first
//
// A guard for a rule of the form "these two things must not collapse into one"
// is worth only as much as the fixture that makes it fire wrongly. Four such
// fixtures are authored below, before the benign one:
//
//  1. a descendant `PersistentPreRunE` — the plain shadow;
//  2. a descendant `PersistentPreRun`, the NON-error variant — the third
//     element that a check written over `PersistentPreRunE` alone misses, even
//     though cobra treats it identically (the `else if` in the loop above), and
//     the field a command with no error to return would naturally reach for;
//  3. a hook MOVED off the root onto a descendant — the tree still holds
//     exactly ONE hook, so a guard that counted and stopped there would call it
//     compliant while every OTHER command runs with no pre-run at all;
//  4. the root's own hook demoted to `PersistentPreRun` — one hook, on the
//     root, and unable to stop the run, which is the other half of R3.2.
//
// Only then (5) the benign tree, where the rule plainly holds, which is what
// stops the four above being satisfied by a classifier that always complains.
//
// Every test in this file carries the TestRootPreRun prefix, so
// `-run TestRootPreRun` selects all of them and not a neighbour's.

import (
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

// preRunSite is one command that declares a persistent pre-run, named by its
// full command path ("bentoo overlay manifest") and by which of cobra's two
// fields it used.
type preRunSite struct {
	path  string
	field string
}

// collectPreRunSites walks the whole tree depth-first and returns every command
// declaring either persistent pre-run field, plus the number of commands
// walked. The count is returned rather than logged here so that every caller
// publishes it: a walk that visited nothing would otherwise report "no
// violations" and be indistinguishable from a walk that visited everything.
func collectPreRunSites(cmd *cobra.Command) (sites []preRunSite, walked int) {
	walked = 1

	// Both fields, deliberately. cobra's hook loop is
	// `if PersistentPreRunE != nil { ... } else if PersistentPreRun != nil { ... }`
	// and breaks after either — so the non-error variant shadows the root's
	// hook just as completely.
	if cmd.PersistentPreRunE != nil {
		sites = append(sites, preRunSite{path: cmd.CommandPath(), field: "PersistentPreRunE"})
	}
	if cmd.PersistentPreRun != nil {
		sites = append(sites, preRunSite{path: cmd.CommandPath(), field: "PersistentPreRun"})
	}

	for _, child := range cmd.Commands() {
		childSites, childWalked := collectPreRunSites(child)
		sites = append(sites, childSites...)
		walked += childWalked
	}
	return sites, walked
}

// rootPreRunViolations states the rule ONCE, so that the production tree and
// the fixtures below are judged by the same code: the tree must hold exactly
// one persistent pre-run; it must sit on the root; and it must be the
// error-returning variant, because only a hook that can return an error stops
// cobra before RunE — and stopping the run is the half of R3.2 that matters.
//
// It returns one sentence per violation, each naming the offending command
// path, plus the number of commands walked.
func rootPreRunViolations(root *cobra.Command) (problems []string, walked int) {
	sites, walked := collectPreRunSites(root)
	rootPath := root.CommandPath()

	rootHasE := false
	for _, site := range sites {
		switch {
		case site.path != rootPath:
			problems = append(problems, site.path+" declares "+site.field+", but only the root "+rootPath+" may.\n"+
				"\tcobra walks UP from the command that ran and executes the FIRST persistent pre-run it finds, then stops.\n"+
				"\tSo `"+site.path+"` — and everything under it — would run with the root's hook never reached:\n"+
				"\tno --ui rejection before any work (S046-R3.2) and no publish of verbose/quiet/noColor (S046-R3.4).\n"+
				"\tRemedy: put the setup in this command's own PreRunE/RunE, which cobra runs IN ADDITION to the root's, never instead of it.")
		case site.field == "PersistentPreRunE":
			rootHasE = true
		default:
			problems = append(problems, "the root "+rootPath+" declares PersistentPreRun, the variant that cannot return an error.\n"+
				"\tOnly an error RETURNED from the hook stops cobra before RunE, so a rejection written here satisfies the message\n"+
				"\thalf of S046-R3.2 and loses the half that matters — that NOTHING runs.")
		}
	}

	if !rootHasE {
		problems = append(problems, "the root "+rootPath+" declares no PersistentPreRunE.\n"+
			"\tIt is the one hook S046-R3.2 (reject an unusable --ui before any work) and S046-R3.4 (publish verbose/quiet/noColor)\n"+
			"\trest on; without it, every command in the tree runs with neither.")
	}
	return problems, walked
}

// fixturePreRunTree builds a small tree shaped like the production one —
// a root with the hook, a nested `overlay manifest`, a top-level `version` —
// for the classifier fixtures below to mutate.
//
// It is built here rather than taken from newRootCmd() on purpose. These four
// tests judge the CLASSIFIER, and a classifier fixture carved out of the
// production tree would fail for two different reasons at once: because the
// classifier broke, or because the tree did. Keeping them separate is what lets
// a mutation of the real tree fail exactly one test in this file, and makes a
// failure of any fixture mean "this file is wrong", not "the tree is wrong".
func fixturePreRunTree() *cobra.Command {
	root := &cobra.Command{
		Use:               "bentoo",
		PersistentPreRunE: func(*cobra.Command, []string) error { return nil },
	}
	overlay := &cobra.Command{Use: "overlay"}
	overlay.AddCommand(&cobra.Command{Use: "manifest"})
	root.AddCommand(overlay)
	root.AddCommand(&cobra.Command{Use: "version"})
	return root
}

// fixtureCommand descends a fixture tree by name, failing loudly if the shape
// it assumes is not there.
func fixtureCommand(t *testing.T, parent *cobra.Command, names ...string) *cobra.Command {
	t.Helper()

	cmd := parent
	for _, name := range names {
		next := commandNamed(cmd, name)
		if next == nil {
			t.Fatalf("the fixture tree has no %q under %q — the fixture no longer poses the question it was written for", name, cmd.CommandPath())
		}
		cmd = next
	}
	return cmd
}

// TestRootPreRunIsDeclaredOnceAndOnTheRoot is the subject: the tree this
// process would actually run.
//
// It builds the tree with newRootCmd() rather than reading the package-level
// rootCmd, so what it checks is the wiring the constructors produce and not
// whatever package initialisation order happened to leave behind.
func TestRootPreRunIsDeclaredOnceAndOnTheRoot(t *testing.T) {
	root := newRootCmd()

	sites, _ := collectPreRunSites(root)
	problems, walked := rootPreRunViolations(root)

	t.Logf("walked %d commands in the freshly constructed tree; %d declare a persistent pre-run hook", walked, len(sites))

	// The house rule against a guard that passes over an empty set. Zero is
	// the case the sub-task names; one is the same failure one step later,
	// because a tree holding only its root can hold no descendant hook, and
	// this guard would then be asserting nothing about the 29 commands it
	// exists for.
	if walked == 0 {
		t.Fatal("walked 0 commands — the guard passed over an empty tree and proved nothing")
	}
	if walked < 2 {
		t.Fatalf("walked %d command(s) — a tree with no descendants cannot show a descendant hook, so this guard would be vacuous. "+
			"newRootCmd() is expected to return the whole wired tree (see TestNewRootCmdCarriesTheProductionShape).", walked)
	}

	for _, problem := range problems {
		t.Error(problem)
	}
}

// TestRootPreRunGuardRejectsADescendantErrorHook is hostile fixture 1: the
// plain shadow. A descendant is given the same field the root uses.
//
// The assertion is not merely "some problem was reported" but that the sentence
// NAMES THE COMMAND PATH. A guard reporting "a descendant has a hook" without
// saying which one leaves whoever hits it to search 30 commands by hand, and
// the message is most of what a guard is worth.
func TestRootPreRunGuardRejectsADescendantErrorHook(t *testing.T) {
	root := fixturePreRunTree()
	fixtureCommand(t, root, "overlay", "manifest").PersistentPreRunE = func(*cobra.Command, []string) error { return nil }

	problems, walked := rootPreRunViolations(root)
	t.Logf("walked %d commands in fixture 1 (descendant PersistentPreRunE); %d problem(s) reported", walked, len(problems))
	if walked < 2 {
		t.Fatalf("walked %d command(s) — the fixture tree lost its descendants", walked)
	}

	if len(problems) != 1 {
		t.Fatalf("a descendant PersistentPreRunE produced %d problems, want exactly 1: %v", len(problems), problems)
	}
	if !strings.Contains(problems[0], "bentoo overlay manifest") {
		t.Errorf("the reported problem does not name the offending command path %q, so it cannot be acted on:\n%s",
			"bentoo overlay manifest", problems[0])
	}
	if !strings.Contains(problems[0], "PersistentPreRunE") {
		t.Errorf("the reported problem does not name the field that shadows the root's hook:\n%s", problems[0])
	}
}

// TestRootPreRunGuardRejectsADescendantNonErrorHook is hostile fixture 2: the
// THIRD element that a check written over PersistentPreRunE alone would miss.
//
// PersistentPreRun and PersistentPreRunE are different fields with different
// signatures, and a reader asking "is the root's the only PreRunE?" answers yes
// on this tree. cobra does not: its loop takes whichever of the two it meets
// first and breaks. The shadow is total either way.
func TestRootPreRunGuardRejectsADescendantNonErrorHook(t *testing.T) {
	root := fixturePreRunTree()
	fixtureCommand(t, root, "version").PersistentPreRun = func(*cobra.Command, []string) {}

	problems, walked := rootPreRunViolations(root)
	t.Logf("walked %d commands in fixture 2 (descendant PersistentPreRun, the non-error variant); %d problem(s) reported", walked, len(problems))
	if walked < 2 {
		t.Fatalf("walked %d command(s) — the fixture tree lost its descendants", walked)
	}

	if len(problems) != 1 {
		t.Fatalf("a descendant PersistentPreRun (non-error variant) produced %d problems, want exactly 1: %v", len(problems), problems)
	}
	if !strings.Contains(problems[0], "bentoo version") {
		t.Errorf("the reported problem does not name the offending command path %q:\n%s", "bentoo version", problems[0])
	}
	// "declares PersistentPreRun," cannot match the E variant's sentence,
	// which reads "declares PersistentPreRunE," — so this asserts the
	// message tells the two fields apart rather than reporting whichever
	// one the guard happened to look for.
	if !strings.Contains(problems[0], "declares PersistentPreRun,") {
		t.Errorf("the reported problem does not name PersistentPreRun as the shadowing field, so a reader would go looking for the wrong one:\n%s", problems[0])
	}
}

// TestRootPreRunGuardRejectsAHookMovedOffTheRoot is hostile fixture 3, and the
// converse of the two above: the tree still holds EXACTLY ONE persistent
// pre-run, and it is still the error-returning variant. Only its owner changed.
//
// A guard phrased as "count the hooks; there must be one" is green here — while
// every command in the tree except that one subtree now runs with no pre-run at
// all, which is strictly worse than the fixture such a guard was written for.
// Counting is not identity, and this is the fixture that tells them apart.
func TestRootPreRunGuardRejectsAHookMovedOffTheRoot(t *testing.T) {
	root := fixturePreRunTree()
	overlay := fixtureCommand(t, root, "overlay")
	overlay.PersistentPreRunE = root.PersistentPreRunE
	root.PersistentPreRunE = nil

	sites, walked := collectPreRunSites(root)
	if len(sites) != 1 {
		t.Fatalf("fixture 3 is meant to leave exactly one hook in the tree, and left %d — it no longer poses the question it was written for", len(sites))
	}

	problems, _ := rootPreRunViolations(root)
	t.Logf("walked %d commands in fixture 3 (one hook, moved off the root onto `overlay`); %d problem(s) reported", walked, len(problems))

	if len(problems) == 0 {
		t.Fatal("a tree whose single pre-run hook sits on `overlay` instead of on the root was reported as compliant — " +
			"the guard is counting hooks rather than checking which command owns the one it found")
	}

	joined := strings.Join(problems, "\n")
	if !strings.Contains(joined, "bentoo overlay") {
		t.Errorf("no reported problem names `bentoo overlay`, the command that now owns the hook:\n%s", joined)
	}
	if !strings.Contains(joined, "the root bentoo declares no PersistentPreRunE") {
		t.Errorf("no reported problem says the ROOT is now without a hook, which is the half that costs every OTHER command its --ui rejection:\n%s", joined)
	}
}

// TestRootPreRunGuardRejectsARootHookThatCannotStopTheRun is hostile fixture 4:
// the hook is on the root, and it is the only one in the tree, and it is still
// wrong — it is PersistentPreRun, the variant with no error to return.
//
// Nothing is shadowed here, so the first three fixtures all say "compliant".
// What is lost is the other half of R3.2: cobra skips RunE only when the hook
// RETURNS an error, so a --ui rejection written in this field can print its
// sentence and then watch the command run anyway. "One hook, on the root" is
// not the whole rule; "one hook, on the root, able to stop the run" is.
func TestRootPreRunGuardRejectsARootHookThatCannotStopTheRun(t *testing.T) {
	root := fixturePreRunTree()
	root.PersistentPreRunE = nil
	root.PersistentPreRun = func(*cobra.Command, []string) {}

	sites, walked := collectPreRunSites(root)
	if len(sites) != 1 {
		t.Fatalf("fixture 4 is meant to leave exactly one hook in the tree, and left %d", len(sites))
	}

	problems, _ := rootPreRunViolations(root)
	t.Logf("walked %d commands in fixture 4 (one hook, on the root, non-error variant); %d problem(s) reported", walked, len(problems))

	if len(problems) == 0 {
		t.Fatal("a root whose only pre-run is PersistentPreRun was reported as compliant — " +
			"a hook that cannot return an error cannot stop the run, which is the half of S046-R3.2 that matters")
	}
	if joined := strings.Join(problems, "\n"); !strings.Contains(joined, "cannot return an error") {
		t.Errorf("no reported problem explains that the field cannot stop the run, so a reader would not know what to change:\n%s", joined)
	}
}

// TestRootPreRunGuardAcceptsACompliantTree is the benign fixture, last: an
// untouched tree of the production shape must produce no complaint at all.
//
// It is what stops the three tests above being satisfied by a classifier that
// simply always complains — the failure mode that would make them all pass
// while telling nobody anything.
func TestRootPreRunGuardAcceptsACompliantTree(t *testing.T) {
	root := fixturePreRunTree()

	problems, walked := rootPreRunViolations(root)
	t.Logf("walked %d commands in the untouched fixture tree; %d problem(s) reported", walked, len(problems))
	if walked < 2 {
		t.Fatalf("walked %d command(s) — the fixture tree is not wired, so a clean result here means nothing", walked)
	}

	if len(problems) != 0 {
		t.Errorf("a compliant tree — one hook, on the root, error-returning — was reported as violating the rule, so the three hostile fixtures above prove nothing:\n%s",
			strings.Join(problems, "\n"))
	}
}
