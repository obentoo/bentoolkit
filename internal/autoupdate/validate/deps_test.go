package validate

// Authored for story 033, sub-task 6.1 — R5, R5.3, R6, R6.2.
//
// `countEntries` comes from archive_test.go and is reused, never reimplemented.
//
// THE FALSE-POSITIVE CLASS THE SOURCE PROTOCOL MISSES. `ebuild … configure`
// never installs dependencies. So a package whose build dependencies are simply
// absent from this host fails configure for a reason that has nothing to do with
// the bump — and without this pre-check the gate reports a confident FAILED
// about an ebuild that is fine. Over a whole-registry sweep that is not one
// wrong answer, it is a report nobody trusts.
//
// The answer is Portage's own, read-only and deterministic, rather than a guess
// parsed out of a build log. Measured (design M-D):
//
//	$ PORTAGE_REPOSITORIES="$(portageq repos_config /)
//	  [bentoo-staging]
//	  location = <staged>
//	  masters = gentoo bentoo" \
//	  emerge -p --quiet "=media-plugins/gst-plugins-qt6-1.29.2::bentoo-staging"
//	[ebuild  N    ] media-plugins/gst-plugins-qt6-1.29.2
//	EXIT=0
//
// Anything the pretend-run lists BEYOND the package under test is an unsatisfied
// dependency.
//
// WHY "NO FILE IS CREATED" IS ASSERTED. `emerge` does not inherit `ebuild`'s
// auto-append of PORTDIR_OVERLAY, so the staged tree has to be made visible
// somehow — and the obvious wrong implementation is to write a temporary
// repos.conf fragment into /etc/portage/repos.conf. That would pass any check
// that only looked at the ANSWER: it needs root on most hosts, it races every
// other bentoo process, and it leaves a file behind when the run is killed.
// PORTAGE_REPOSITORIES supplies the whole repository configuration as an
// environment VALUE, so nothing is written anywhere — and that is asserted, not
// assumed.
//
// This file pins `DependenciesSatisfied(ctx, stagedRoot, atom, version string,
// deps BuildDeps) (bool, []string, error)`, the signature design.md fixes.
//
// ORDERING NOTE FOR RUN MODE: design.md places `BuildDeps` in build.go
// (sub-task 7.1), but 6.1 is its FIRST consumer and group 6 precedes group 7.
// The type therefore lands with this sub-task; 7.1 then uses it rather than
// defining it. Only the seam fields this file reads are needed here:
// ExecCommand and LookPath.

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// The two pretend-run outputs this sub-task classifies, in `emerge -p --quiet`'s
// own shape.
const (
	emergeOnlyTheBump = "[ebuild  N    ] media-plugins/gst-plugins-qt6-1.29.2\n"

	emergeWithMissingDeps = "[ebuild  N    ] media-libs/gst-plugins-base-1.29.2\n" +
		"[ebuild  N    ] dev-qt/qtbase-6.9.1\n" +
		"[ebuild  N    ] media-plugins/gst-plugins-qt6-1.29.2\n"
)

// depsSeam captures the pretend-run invocation and answers with a fixed output.
// The *exec.Cmd pointer is kept so the child ENVIRONMENT can be inspected after
// the call returns — the production code sets cmd.Env after this factory hands
// the command back, and the field is still populated once Run has finished.
type depsSeam struct {
	spawns int
	names  []string
	args   [][]string
	cmd    *exec.Cmd
}

func newDepsSeam(spy *depsSeam, output string, fail bool) BuildDeps {
	return BuildDeps{
		ExecCommand: func(ctx context.Context, name string, arg ...string) *exec.Cmd {
			spy.spawns++
			spy.names = append(spy.names, name)
			spy.args = append(spy.args, arg)
			var cmd *exec.Cmd
			if fail {
				cmd = exec.CommandContext(ctx, "false")
			} else {
				// printf writes the fixture to whatever stdout the production
				// code attached, exactly as emerge would.
				cmd = exec.CommandContext(ctx, "printf", "%s", output)
			}
			spy.cmd = cmd
			return cmd
		},
		LookPath: func(name string) (string, error) { return "/usr/bin/" + name, nil },
	}
}

// TestDependenciesSatisfied_OnlyTheBumpItselfIsSatisfied is the measured case:
// on the maintainer's host the gst bump listed only itself, and that is what
// "this host can answer the question" looks like.
func TestDependenciesSatisfied_OnlyTheBumpItselfIsSatisfied(t *testing.T) {
	spy := &depsSeam{}

	ok, missing, err := DependenciesSatisfied(context.Background(),
		t.TempDir(), "media-plugins/gst-plugins-qt6", "1.29.2", newDepsSeam(spy, emergeOnlyTheBump, false))

	if err != nil {
		t.Fatalf("DependenciesSatisfied: %v", err)
	}
	if !ok {
		t.Errorf("reported unsatisfied although the pretend run listed only the package under test; missing=%v", missing)
	}
	if len(missing) != 0 {
		t.Errorf("reported %v as missing; the package under test is not its own unsatisfied dependency", missing)
	}
	if spy.spawns != 1 {
		t.Errorf("spawned %d processes, want exactly 1 pretend run", spy.spawns)
	}
}

// TestDependenciesSatisfied_ExtraAtomsAreReportedByName is what makes the skip
// actionable. "Dependencies are missing" sends the operator to work out which;
// naming them turns the skip into an instruction.
func TestDependenciesSatisfied_ExtraAtomsAreReportedByName(t *testing.T) {
	spy := &depsSeam{}

	ok, missing, err := DependenciesSatisfied(context.Background(),
		t.TempDir(), "media-plugins/gst-plugins-qt6", "1.29.2", newDepsSeam(spy, emergeWithMissingDeps, false))

	if err != nil {
		t.Fatalf("DependenciesSatisfied: %v", err)
	}
	if ok {
		t.Fatal("reported satisfied although the pretend run listed two packages beyond the one under test")
	}

	joined := strings.Join(missing, " ")
	for _, want := range []string{"media-libs/gst-plugins-base", "dev-qt/qtbase"} {
		if !strings.Contains(joined, want) {
			t.Errorf("the unsatisfied list %v does not name %q", missing, want)
		}
	}
	if strings.Contains(joined, "media-plugins/gst-plugins-qt6") {
		t.Errorf("the unsatisfied list %v includes the package under test; it is the subject of the question, not an answer to it", missing)
	}
}

// TestDependenciesSatisfied_NonZeroExitIsUndeterminedNotUnsatisfied is R5.3 and
// R6.2 together, and the distinction is the whole story in miniature: "Portage
// says a dependency is missing" and "Portage could not be asked" are different
// answers. Both make the caller SKIP, and only one of them is about the bump.
//
// The error is returned so the caller can name the right reason; ok is false so
// no build starts on an unanswered question.
func TestDependenciesSatisfied_NonZeroExitIsUndeterminedNotUnsatisfied(t *testing.T) {
	spy := &depsSeam{}

	ok, missing, err := DependenciesSatisfied(context.Background(),
		t.TempDir(), "media-plugins/gst-plugins-qt6", "1.29.2", newDepsSeam(spy, "", true))

	if err == nil {
		t.Fatal("a failed pretend run returned no error; the caller cannot then tell 'a dependency is missing' from 'I could not ask'")
	}
	if ok {
		t.Error("reported satisfied from a pretend run that failed; a build must not start on an unanswered question")
	}
	if len(missing) != 0 {
		t.Errorf("returned %v as unsatisfied atoms from a run that produced no usable output; that is a claim, not a measurement", missing)
	}
}

// TestDependenciesSatisfied_ComposesPortageRepositoriesInTheChildEnvironment
// pins HOW the staged tree is made visible. emerge does not inherit ebuild's
// auto-append, and PORTAGE_REPOSITORIES is the way that needs no file.
func TestDependenciesSatisfied_ComposesPortageRepositoriesInTheChildEnvironment(t *testing.T) {
	spy := &depsSeam{}
	staged := t.TempDir()

	if _, _, err := DependenciesSatisfied(context.Background(),
		staged, "media-plugins/gst-plugins-qt6", "1.29.2", newDepsSeam(spy, emergeOnlyTheBump, false)); err != nil {
		t.Fatalf("DependenciesSatisfied: %v", err)
	}

	if spy.cmd == nil {
		t.Fatal("no command was captured")
	}
	if spy.cmd.Env == nil {
		t.Fatal("cmd.Env was never set, so the child inherits the operator's shell and cannot see the staged tree at all")
	}

	var repos string
	for _, kv := range spy.cmd.Env {
		if strings.HasPrefix(kv, "PORTAGE_REPOSITORIES=") {
			repos = strings.TrimPrefix(kv, "PORTAGE_REPOSITORIES=")
		}
	}
	if repos == "" {
		t.Fatalf("PORTAGE_REPOSITORIES is absent from the child environment: %v", spy.cmd.Env)
	}
	if !strings.Contains(repos, staged) {
		t.Errorf("PORTAGE_REPOSITORIES does not name the staged tree %q:\n%s", staged, repos)
	}
	if !strings.Contains(repos, "location") {
		t.Errorf("PORTAGE_REPOSITORIES carries no location line; Portage cannot resolve the staged repo:\n%s", repos)
	}

	// And the pretend run is a PRETEND run, scoped to the exact version in the
	// staged repo. `emerge` without -p installs.
	args := strings.Join(spy.args[0], " ")
	if !strings.Contains(args, "-p") && !strings.Contains(args, "--pretend") {
		t.Errorf("the emerge invocation %q is not a pretend run; this function must never install anything", args)
	}
	if !strings.Contains(args, "media-plugins/gst-plugins-qt6-1.29.2") {
		t.Errorf("the emerge invocation %q does not name the exact version under test", args)
	}
}

// TestDependenciesSatisfied_WritesNoFileAnywhere is the assertion that rules out
// the obvious wrong implementation. A temporary repos.conf fragment would
// produce the same answer, need root on most hosts, race every concurrent
// bentoo process, and survive a killed run.
func TestDependenciesSatisfied_WritesNoFileAnywhere(t *testing.T) {
	spy := &depsSeam{}
	staged := t.TempDir()

	// A watched, empty working directory: a relative path written by mistake
	// lands here.
	work := t.TempDir()
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	if err := os.Chdir(work); err != nil {
		t.Fatalf("chdir: %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(cwd) })

	// And a watched TMPDIR, so an os.MkdirTemp/CreateTemp is visible too.
	tmp := t.TempDir()
	t.Setenv("TMPDIR", tmp)

	stagedBefore := countEntries(t, staged)

	if _, _, err := DependenciesSatisfied(context.Background(),
		staged, "media-plugins/gst-plugins-qt6", "1.29.2", newDepsSeam(spy, emergeOnlyTheBump, false)); err != nil {
		t.Fatalf("DependenciesSatisfied: %v", err)
	}

	if got := countEntries(t, work); got != 0 {
		entries, _ := os.ReadDir(work)
		var names []string
		for _, e := range entries {
			names = append(names, filepath.Join(work, e.Name()))
		}
		t.Errorf("the pretend run wrote %v into the working directory; the repository configuration travels in the environment, not in a file", names)
	}
	if got := countEntries(t, tmp); got != 0 {
		t.Errorf("the pretend run left %d entries under TMPDIR; nothing is written anywhere (design M-D)", got)
	}
	if got := countEntries(t, staged); got != stagedBefore {
		t.Errorf("the pretend run modified the staged tree: %d entries before, %d after", stagedBefore, got)
	}
}

// TestDependenciesSatisfied_AsksNoQuestionWhenEmergeIsAbsent keeps a host
// without Portage from producing a confident answer. `emerge` missing is not
// "no dependencies are missing".
func TestDependenciesSatisfied_AsksNoQuestionWhenEmergeIsAbsent(t *testing.T) {
	spy := &depsSeam{}
	deps := newDepsSeam(spy, emergeOnlyTheBump, false)
	deps.LookPath = func(string) (string, error) { return "", os.ErrNotExist }

	ok, _, err := DependenciesSatisfied(context.Background(),
		t.TempDir(), "media-plugins/gst-plugins-qt6", "1.29.2", deps)

	if err == nil {
		t.Fatal("a host without emerge returned no error; the caller would read the answer as 'nothing is missing'")
	}
	if ok {
		t.Error("reported satisfied on a host where the question could not be asked")
	}
	if spy.spawns != 0 {
		t.Errorf("spawned %d processes although the binary lookup failed", spy.spawns)
	}
}
