package autoupdate

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/obentoo/bentoolkit/internal/autoupdate/buildenv"
	"github.com/obentoo/bentoolkit/internal/autoupdate/validate"
)

// envTransport is a build environment that is not there: the preparation
// steps succeed (or emerge fails, with failDeps), and the build children it
// would run are named "inenv" so a test can tell they were moved in.
type envTransport struct {
	failDeps bool
	// isolated is whether a namespace can be created inside the environment.
	isolated bool
	// dead makes the environment stop answering.
	dead   bool
	mu     sync.Mutex
	starts int
	argv   [][]string
}

func (e *envTransport) Name() string     { return "fakeenv" }
func (e *envTransport) Identity() string { return "fakeenv-image (sha256:fake)" }
func (e *envTransport) Start(context.Context) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.starts++
	return nil
}
func (e *envTransport) Stop(context.Context) error { return nil }
func (e *envTransport) Command(ctx context.Context, argv, _ []string, _ string) *exec.Cmd {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.argv = append(e.argv, argv)
	if e.dead {
		return exec.CommandContext(ctx, "sh", "-c", "echo 'Failed to connect to vsock:42:22' >&2; exit 255")
	}
	switch argv[0] {
	case "emerge":
		if e.failDeps {
			return exec.CommandContext(ctx, "sh", "-c", "echo 'emerge: there are no ebuilds to satisfy dev-libs/missing'; exit 1")
		}
		return exec.CommandContext(ctx, "true")
	case "unshare":
		if e.isolated {
			return exec.CommandContext(ctx, "true")
		}
		return exec.CommandContext(ctx, "sh", "-c", "echo 'unshare: unshare failed: Operation not permitted'; exit 1")
	case "tar", "sh":
		return exec.CommandContext(ctx, "sh", "-c", "cat >/dev/null")
	}
	return exec.CommandContext(ctx, "inenv", argv...)
}

func (e *envTransport) ran(argv0 string) bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	return slices.ContainsFunc(e.argv, func(a []string) bool { return a[0] == argv0 })
}

// TestBuildEnvironment_TheCheckBuildsInTheEnvironment: the check readies the
// environment (tree shipped, dependencies installed), runs the ebuild there,
// and records a fresh pass by the backend that ran it.
func TestBuildEnvironment_TheCheckBuildsInTheEnvironment(t *testing.T) {
	builds := 0
	a, pkg := scriptedBuildApplier(t, buildRun{output: cleanCompile}, &builds)
	env := &envTransport{}
	var ranIn []string
	a.runCaptured = func(cmd *exec.Cmd) error {
		ranIn = append(ranIn, filepath.Base(cmd.Path))
		_, err := cmd.Stdout.Write([]byte(cleanCompile))
		return err
	}
	WithApplierBuildBackend(buildenv.NewSession(env))(a)

	res := a.Validate(t.Context(), pkg, validate.DepthCompile)

	if env.starts == 0 || !env.ran("tar") || !env.ran("emerge") {
		t.Fatalf("the environment was not readied: starts=%d commands=%v", env.starts, env.argv)
	}
	if !slices.Contains(ranIn, "inenv") {
		t.Errorf("the build child did not run in the environment: ran %v", ranIn)
	}
	if ok, why := validate.PromotionDecision(res.Gates, nil); !ok {
		t.Fatalf("the gates did not pass in the environment: %s", why)
	}
	records, _ := filepath.Glob(filepath.Join(a.StagingRoot(), "*", "*", "*", ".bentoo-stage-record.json"))
	if len(records) != 1 {
		t.Fatalf("want one stage record, found %v", records)
	}
	rec, err := validate.ReadStageRecord(filepath.Dir(records[0]))
	if err != nil {
		t.Fatal(err)
	}
	if rec.Backend != "fakeenv" || !rec.FreshEnvironment {
		t.Errorf("record backend=%q fresh=%v, want fakeenv and a fresh pass", rec.Backend, rec.FreshEnvironment)
	}
	if want := "fakeenv-image (sha256:fake)"; rec.Environment != want {
		t.Errorf("record environment = %q, want the environment's own %q, not this host's tree", rec.Environment, want)
	}
}

// TestBuildEnvironment_ADependencyFailureWaits: dependencies the environment
// cannot install are the environment's failure; every build gate declines over
// the host, so the bump waits instead of reaching the fixer.
func TestBuildEnvironment_ADependencyFailureWaits(t *testing.T) {
	builds := 0
	a, pkg := scriptedBuildApplier(t, buildRun{output: cleanCompile}, &builds)
	WithApplierBuildBackend(buildenv.NewSession(&envTransport{failDeps: true}))(a)

	res := a.Validate(t.Context(), pkg, validate.DepthCompile)

	sawBuildGate := false
	for _, g := range res.Gates {
		if g.Gate == validate.GateOptions || g.Gate == validate.GateQA {
			continue
		}
		sawBuildGate = true
		if g.Outcome != validate.OutcomeSkipped || g.Declined != validate.DeclineHost || !strings.Contains(g.Reason, "build environment") {
			t.Errorf("gate %s = %s/%s %q; want SKIPPED over the host, naming the build environment", g.Gate, g.Outcome, g.Declined, g.Reason)
		}
	}
	if !sawBuildGate {
		t.Fatalf("no build gate was reported: %+v", res.Gates)
	}
	if builds != 0 {
		t.Errorf("%d build(s) ran although the environment could not be readied", builds)
	}
}

// TestBuildEnvironment_IsolationIsMeasuredInTheEnvironment: this host can
// create a namespace, but a pass only counts as isolated when the environment
// the build ran in can.
func TestBuildEnvironment_IsolationIsMeasuredInTheEnvironment(t *testing.T) {
	for _, isolated := range []bool{false, true} {
		t.Run(map[bool]string{false: "not isolated", true: "isolated"}[isolated], func(t *testing.T) {
			builds := 0
			a, pkg := scriptedBuildApplier(t, buildRun{output: cleanCompile}, &builds)
			a.runCaptured = func(cmd *exec.Cmd) error {
				_, err := cmd.Stdout.Write([]byte(cleanCompile))
				return err
			}
			env := &envTransport{isolated: isolated}
			WithApplierBuildBackend(buildenv.NewSession(env))(a)

			res := a.Validate(t.Context(), pkg, validate.DepthCompile)

			if !env.ran("unshare") {
				t.Fatalf("isolation was not measured in the environment: %v", env.argv)
			}
			for _, g := range res.Gates {
				if g.Gate != validate.GatePatches {
					continue
				}
				if g.Outcome != validate.OutcomePass {
					t.Fatalf("patches = %s: %s", g.Outcome, g.Reason)
				}
				if labelled := strings.Contains(g.Reason, "unverified isolation"); labelled == isolated {
					t.Errorf("patches reason %q: labelled unverified = %v with the environment isolated = %v", g.Reason, labelled, isolated)
				}
				return
			}
			t.Fatalf("no patches gate was reported: %+v", res.Gates)
		})
	}
}

// TestBuildEnvironment_ADistNameWithAPathShipsNothing: the staged Manifest is
// editable by the fixer, so a DIST name that climbs out of the distdir must not
// send a file from elsewhere on this host into the environment.
func TestBuildEnvironment_ADistNameWithAPathShipsNothing(t *testing.T) {
	root := t.TempDir()
	distdir := filepath.Join(root, "distfiles")
	pkgDir := filepath.Join(root, "staged", "app-misc", "probe")
	for _, d := range []string{distdir, pkgDir} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	for _, f := range []string{filepath.Join(root, "secret"), filepath.Join(distdir, "probe-1.tar.gz")} {
		if err := os.WriteFile(f, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	manifest := "DIST ../secret 1 SHA512 00\nDIST probe-1.tar.gz 1 SHA512 00\nDIST .. 1 SHA512 00\n"
	if err := os.WriteFile(filepath.Join(pkgDir, "Manifest"), []byte(manifest), 0o644); err != nil {
		t.Fatal(err)
	}
	a := &Applier{distdir: distdir}

	got, err := a.environmentDistfiles(candidatePaths{pkgDir: pkgDir})
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{filepath.Join(distdir, "probe-1.tar.gz")}; !slices.Equal(got, want) {
		t.Errorf("shipped %v, want only %v", got, want)
	}
}

// TestBuildEnvironment_ALostEnvironmentIsTheMachines: a build that failed in an
// environment which no longer answers failed on the machine, whatever its
// transcript says, so the fixer never sees it.
func TestBuildEnvironment_ALostEnvironmentIsTheMachines(t *testing.T) {
	transcript := ">>> Preparing source in /var/tmp/portage/x/work ...\n>>> Source prepared.\nmake: *** [all] Error 2\n"
	env := &envTransport{}
	session := buildenv.NewSession(env)
	a := &Applier{buildBackend: session}
	if err := session.Prepare(t.Context(), buildenv.PrepareRequest{StagedRoot: t.TempDir(), Atom: "=app-misc/x-1::s"}); err != nil {
		t.Fatal(err)
	}

	if ev := a.buildFaultEvidenceFor(t.Context(), buildAttempt{transcript: transcript}); ev.environmentLost || buildFaultVerdict(ev) != nil {
		t.Fatalf("a live environment's failed build was blamed on the machine: %+v", ev)
	}
	env.dead = true
	ev := a.buildFaultEvidenceFor(t.Context(), buildAttempt{transcript: transcript})
	verdict := buildFaultVerdict(ev)
	if !errors.Is(verdict, ErrBuildEnvironment) || !errors.Is(verdict, errBuildEnvironmentLost) {
		t.Errorf("a lost environment: verdict = %v, want ErrBuildEnvironment naming the lost environment", verdict)
	}
	if host := (&Applier{buildBackend: validate.HostBackend{}}).buildFaultEvidenceFor(t.Context(), buildAttempt{transcript: transcript}); host.environmentLost {
		t.Error("the host backend reported a lost environment")
	}
}
