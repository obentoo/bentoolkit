package autoupdate

// Authored for story 040, sub-task 2.2 — R2, R2.1, R2.3, R2.4.
//
// THE PRIVILEGED COMPILE READ WHATEVER DISTDIR THE HOST HAPPENED TO NAME.
// `compileOnce` spawns `<privTool> ebuild <path> clean compile` and sets no
// environment at all, so the run this process resolved with --distdir reached
// the child not at all — while the unprivileged ladder has exported a computed
// DISTDIR since story 039 (validate/build.go, R3.1). One word, "PASS", covered
// two different amounts of evidence.
//
// SETTING cmd.Env WOULD NOT HAVE FIXED IT, WHICH IS WHY THIS NEEDED A
// MEASUREMENT. Measured on this host as the real operator, 2026-08-20
// (.epic/…/.draft/d2-privilege-env-evidence.md): under sudo-rs 0.2.14 with
// env_reset in force, an exported DISTDIR does NOT survive into what the child
// sees — nothing Portage-related does. What DOES survive is sudo's own
// argument form, `sudo DISTDIR=<dir> <cmd>`, which was granted here and is the
// mechanism these tests pin. It is chosen over --preserve-env because the value
// is a LITERAL ARGUMENT this run computed: nothing crosses from the operator's
// shell, which is R2.4 in its strongest reading.
//
// `doas` HAS NO SUCH FORM, and that is a correctness constraint rather than a
// gap in the measurement: doas would try to execute a program literally named
// "DISTDIR=…". The applier PREFERS doas when it is installed, so a mechanism
// applied blindly would break the compile gate outright on those hosts. R2.3 is
// the answer there — the gate says the distdir could not be enforced — and the
// third test below is what keeps a future edit from "unifying" the two tools.
//
// The tests drive compileOnce DIRECTLY, with the privilege tool as its
// parameter, because detectPrivilegeTool asks the real PATH: a test that let it
// choose would assert one thing on a host with doas and another without.

import (
	"context"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// privilegedCompileSpy records the command compileOnce spawned: the tool it
// named, the arguments it passed, and the *exec.Cmd itself, so the child's
// ENVIRONMENT is inspectable after the call (R2.4 is a statement about what the
// child inherits, not only about the argv).
type privilegedCompileSpy struct {
	spawns int
	name   string
	args   []string
	cmd    *exec.Cmd
}

// privilegedCompileApplier builds an applier whose exec and runner seams are
// both stubbed — nothing is spawned, nothing is run, and no privilege is ever
// asked for — with distdir as the directory this run resolved.
func privilegedCompileApplier(t *testing.T, spy *privilegedCompileSpy, distdir string) (*Applier, candidatePaths) {
	t.Helper()
	tmp := t.TempDir()
	overlayDir := filepath.Join(tmp, "overlay")
	configDir := filepath.Join(tmp, "config")
	const pkg = "media-plugins/gst-plugins-qt6"

	createTestEbuildFile(t, overlayDir, pkg, "1.28.6")

	applier, err := NewApplier(overlayDir, configDir,
		WithExecCommand(func(ctx context.Context, name string, arg ...string) *exec.Cmd {
			spy.spawns++
			spy.name = name
			spy.args = append([]string(nil), arg...)
			cmd := exec.CommandContext(ctx, "true")
			spy.cmd = cmd
			return cmd
		}),
		WithApplierRunAttached(func(cmd *exec.Cmd) ([]byte, error) {
			spy.cmd = cmd
			return []byte(">>> Source compiled.\n"), nil
		}),
		WithApplierDistdir(distdir, ""),
		WithConfirmFunc(func(string) bool { return true }),
	)
	if err != nil {
		t.Fatalf("creating applier: %v", err)
	}

	stagedRoot := filepath.Join(tmp, "staging", "media-plugins", "gst-plugins-qt6", "1.29.2")
	cand := candidatePaths{
		repoRoot:   stagedRoot,
		pkgDir:     filepath.Join(stagedRoot, "media-plugins", "gst-plugins-qt6"),
		ebuildPath: filepath.Join(stagedRoot, "media-plugins", "gst-plugins-qt6", "gst-plugins-qt6-1.29.2.ebuild"),
		staged:     true,
	}
	return applier, cand
}

// assignmentIn returns the DISTDIR assignment among args, or "" when none is
// there. It looks for the ARGUMENT form specifically, which is the mechanism
// the measurement chose.
func assignmentIn(args []string) string {
	for _, arg := range args {
		if strings.HasPrefix(arg, "DISTDIR=") {
			return arg
		}
	}
	return ""
}

// TestCompileOnce_SudoCarriesTheResolvedDistdirAsAnAssignment is R2.1: the
// distdir this run resolved is the one the ebuild child reads, carried by the
// mechanism the privilege tool was MEASURED to honour.
func TestCompileOnce_SudoCarriesTheResolvedDistdirAsAnAssignment(t *testing.T) {
	const distdir = "/var/cache/distfiles-run"
	spy := &privilegedCompileSpy{}
	applier, cand := privilegedCompileApplier(t, spy, distdir)

	attempt := applier.compileOnce(cand, "media-plugins/gst-plugins-qt6", "1.29.2", "sudo")
	if attempt.err != nil {
		t.Fatalf("compileOnce reported a failure the stubbed runner never produced: %v", attempt.err)
	}
	if spy.spawns != 1 {
		t.Fatalf("compileOnce spawned %d children, want exactly one", spy.spawns)
	}
	if spy.name != "sudo" {
		t.Errorf("the privilege tool spawned was %q, want the one compileOnce was handed", spy.name)
	}

	if got := assignmentIn(spy.args); got != "DISTDIR="+distdir {
		t.Errorf("the privileged child was spawned as %q %v — the resolved distdir %q is not carried.\n"+
			"Measured on this host: sudo's env_reset discards an exported DISTDIR, so cmd.Env cannot deliver it "+
			"and the argument form is what does; without it a --compile PASS claims a hermeticity the run never had",
			spy.name, spy.args, distdir)
	}

	// The assignment PRECEDES the command, or sudo reads it as the program to
	// run rather than as an environment assignment.
	want := []string{"DISTDIR=" + distdir, "ebuild", cand.ebuildPath, "clean", "compile"}
	if len(spy.args) != len(want) {
		t.Fatalf("argv is %v, want %v", spy.args, want)
	}
	for i := range want {
		if spy.args[i] != want[i] {
			t.Errorf("argv[%d] is %q, want %q (full argv %v)", i, spy.args[i], want[i], spy.args)
		}
	}
}

// TestCompileOnce_NoResolvedDistdirAddsNothing is R3.2's rule, applied to this
// path: a run that resolved no directory sets nothing and invents nothing, so
// Portage answers from its own configuration — the honest answer, and today's
// bytes exactly.
func TestCompileOnce_NoResolvedDistdirAddsNothing(t *testing.T) {
	spy := &privilegedCompileSpy{}
	applier, cand := privilegedCompileApplier(t, spy, "")

	applier.compileOnce(cand, "media-plugins/gst-plugins-qt6", "1.29.2", "sudo")

	if got := assignmentIn(spy.args); got != "" {
		t.Errorf("the child was handed %q although this run resolved no distdir; an invented default would "+
			"point the fetch at a directory this package made up", got)
	}
	want := []string{"ebuild", cand.ebuildPath, "clean", "compile"}
	if strings.Join(spy.args, " ") != strings.Join(want, " ") {
		t.Errorf("argv is %v, want today's exact argv %v", spy.args, want)
	}
}

// TestCompileOnce_DoasIsNeverHandedAnAssignment is the constraint that makes
// the tool a BRANCH rather than a detail. doas takes no VAR=value argument: it
// would try to execute a program named "DISTDIR=…" and the compile gate would
// fail on every host that has doas installed — which the applier PREFERS.
// R2.3 covers those hosts instead, in the gate's own reason.
func TestCompileOnce_DoasIsNeverHandedAnAssignment(t *testing.T) {
	spy := &privilegedCompileSpy{}
	applier, cand := privilegedCompileApplier(t, spy, "/var/cache/distfiles-run")

	applier.compileOnce(cand, "media-plugins/gst-plugins-qt6", "1.29.2", "doas")

	if got := assignmentIn(spy.args); got != "" {
		t.Fatalf("doas was handed %q; it has no argument form for an environment assignment and would try to "+
			"execute a program by that name, so this breaks the compile gate outright on every doas host", got)
	}
	if len(spy.args) == 0 || spy.args[0] != "ebuild" {
		t.Errorf("argv is %v, want the command itself first under doas", spy.args)
	}
}

// TestCompileOnce_ThePrivilegeSurfaceIsNoWiderThanToday is R2.4. The value
// travels as an ARGUMENT, so nothing new crosses from the operator's shell —
// and in particular this path must not start assembling an environment for the
// privileged child, which is a different decision with a different risk (the
// password prompt reads TERM and, on some hosts, DISPLAY/XAUTHORITY).
func TestCompileOnce_ThePrivilegeSurfaceIsNoWiderThanToday(t *testing.T) {
	spy := &privilegedCompileSpy{}
	applier, cand := privilegedCompileApplier(t, spy, "/var/cache/distfiles-run")

	applier.compileOnce(cand, "media-plugins/gst-plugins-qt6", "1.29.2", "sudo")

	if spy.cmd == nil {
		t.Fatal("no command was captured")
	}
	if spy.cmd.Env != nil {
		t.Errorf("compileOnce set cmd.Env to %v; today it sets none, and the measurement showed sudo's "+
			"env_reset discards the inherited environment anyway — so an allow-list here would filter what "+
			"the privilege tool throws away a moment later, while risking the prompt", spy.cmd.Env)
	}
}
