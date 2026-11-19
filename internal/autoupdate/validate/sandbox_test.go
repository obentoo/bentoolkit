package validate

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// indexOf returns the position of the first occurrence of seq in args, or -1.
func indexOf(args []string, seq ...string) int {
	for i := 0; i+len(seq) <= len(args); i++ {
		if slices.Equal(args[i:i+len(seq)], seq) {
			return i
		}
	}
	return -1
}

func TestSandboxArgs_HidesSecretsAndExposesOnlyTheBuildInputs(t *testing.T) {
	args := sandboxArgs([]string{"/stage", "/dist"}, "/var/tmp/bentoo-sandbox-x", "/stage",
		"ebuild", []string{"/stage/a/b/b-1.ebuild", "clean", "configure"})

	for _, want := range [][]string{
		{"--unshare-all"},
		{"--die-with-parent"},
		{"--new-session"},
		{"--ro-bind", "/", "/"},
		{"--ro-bind-try", "/stage", "/stage"},
		{"--ro-bind-try", "/dist", "/dist"},
		{"--bind", "/var/tmp/bentoo-sandbox-x", "/var/tmp/bentoo-sandbox-x"},
		{"--chdir", "/stage"},
		{"--", "ebuild", "/stage/a/b/b-1.ebuild", "clean", "configure"},
	} {
		if indexOf(args, want...) < 0 {
			t.Errorf("sandbox argv %q lacks %q", args, want)
		}
	}
	// /tmp exists on every host the suite runs on: it must be hidden, and the
	// hiding must come before the binds, or a bind under it would be covered.
	tmpfs := indexOf(args, "--tmpfs", "/tmp")
	if tmpfs < 0 {
		t.Fatalf("sandbox argv %q does not hide /tmp", args)
	}
	if bind := indexOf(args, "--bind", "/var/tmp/bentoo-sandbox-x", "/var/tmp/bentoo-sandbox-x"); bind < tmpfs {
		t.Errorf("the writable bind (at %d) precedes the tmpfs over /tmp (at %d); it would be hidden", bind, tmpfs)
	}
	if indexOf(args, "--share-net") >= 0 || indexOf(args, "--bind", "/", "/") >= 0 {
		t.Errorf("sandbox argv %q shares the network or binds / writable", args)
	}
	if got := args[len(args)-4:]; !slices.Equal(got, []string{"ebuild", "/stage/a/b/b-1.ebuild", "clean", "configure"}) {
		t.Errorf("the wrapped command must end the argv unchanged, got %q", got)
	}
}

func TestSandboxEnv(t *testing.T) {
	env := sandboxEnv([]string{
		"PATH=/usr/bin", "FEATURES=sandbox ccache", "PORTAGE_TMPDIR=/elsewhere", "PORTAGE_GRPNAME=portage",
	}, "/var/tmp/sb", "alice", "users")

	want := []string{"PATH=/usr/bin", "FEATURES=sandbox ccache -ccache",
		"PORTAGE_TMPDIR=/var/tmp/sb", "PORTAGE_USERNAME=alice", "PORTAGE_GRPNAME=users"}
	slices.Sort(env)
	slices.Sort(want)
	if !slices.Equal(env, want) {
		t.Errorf("sandboxEnv = %q, want %q", env, want)
	}
}

func TestRunBuildGates_RefusesWithoutTheSandbox(t *testing.T) {
	spy := &buildSpy{}
	deps := buildSeam(spy, configureOKLog, nil)
	deps.SandboxProbe = func() (bool, string) { return false, "bubblewrap (bwrap) is not installed" }
	req := buildRequestFor(t, DepthConfigure)
	req.HostSandbox = true

	gates, err := RunBuildGates(context.Background(), req, deps)
	if err != nil {
		t.Fatalf("RunBuildGates: %v", err)
	}
	if spy.spawns != 0 {
		t.Fatalf("%d command(s) spawned; a build without its sandbox must not run at all", spy.spawns)
	}
	g := gateNamed(t, gates, GateConfigure)
	if g.Outcome != OutcomeSkipped || g.Declined != DeclineHost {
		t.Errorf("configure gate = %+v, want SKIPPED declined by the host", g)
	}
	if !strings.Contains(g.Reason, "bubblewrap") || !strings.Contains(g.Reason, "not installed") {
		t.Errorf("reason %q does not say the sandbox is missing and why", g.Reason)
	}
}

func TestRunBuildGates_RunsEbuildInsideTheSandbox(t *testing.T) {
	spy := &buildSpy{}
	deps := buildSeam(spy, configureOKLog, nil)
	deps.SandboxProbe = func() (bool, string) { return true, "" }
	deps.IsolationProbe = func() (bool, string) { return false, "no network namespace for this user" }
	req := buildRequestFor(t, DepthConfigure)
	req.HostSandbox = true
	req.Distdir = "/dist"
	req.SandboxTmpRoot = t.TempDir()

	gates, err := RunBuildGates(context.Background(), req, deps)
	if err != nil {
		t.Fatalf("RunBuildGates: %v", err)
	}
	if spy.spawns != 1 || spy.names[0] != sandboxBinary {
		t.Fatalf("spawned %q, want one %s", spy.names, sandboxBinary)
	}
	argv := spy.argv[0]
	sep := indexOf(argv, "--", "ebuild")
	if sep < 0 || argv[sep+3] != "clean" || argv[sep+4] != "configure" {
		t.Errorf("bwrap argv %q does not end with the ebuild invocation", argv)
	}
	if indexOf(argv, "--ro-bind-try", req.StagedRoot, req.StagedRoot) < 0 || indexOf(argv, "--ro-bind-try", "/dist", "/dist") < 0 {
		t.Errorf("bwrap argv %q does not expose the staged tree and the distdir read-only", argv)
	}

	var tmpdir string
	for _, kv := range spy.envAtSpawn {
		if v, ok := strings.CutPrefix(kv, "PORTAGE_TMPDIR="); ok {
			tmpdir = v
		}
	}
	if filepath.Dir(tmpdir) != req.SandboxTmpRoot {
		t.Errorf("PORTAGE_TMPDIR = %q, want a private directory under %q", tmpdir, req.SandboxTmpRoot)
	}
	if indexOf(argv, "--bind", tmpdir, tmpdir) < 0 {
		t.Errorf("the private PORTAGE_TMPDIR %q is not the writable bind in %q", tmpdir, argv)
	}
	if _, err := os.Stat(tmpdir); !os.IsNotExist(err) {
		t.Errorf("the private PORTAGE_TMPDIR %q survived the build (stat err = %v)", tmpdir, err)
	}

	g := gateNamed(t, gates, GateConfigure)
	if g.Outcome != OutcomePass {
		t.Fatalf("configure gate = %+v, want PASS", g)
	}
	if strings.Contains(g.Reason, unverifiedIsolationLabel) {
		t.Errorf("a sandboxed pass is network-isolated, yet reads %q", g.Reason)
	}
}

// TestRunBuildGates_SandboxSatisfiesRequireIsolation: an operator who
// requires isolation gets the build on a host whose Portage cannot create a
// network namespace, because the sandbox can.
func TestRunBuildGates_SandboxSatisfiesRequireIsolation(t *testing.T) {
	spy := &buildSpy{}
	deps := buildSeam(spy, configureOKLog, nil)
	deps.SandboxProbe = func() (bool, string) { return true, "" }
	deps.IsolationProbe = func() (bool, string) { return false, "denied" }
	req := buildRequestFor(t, DepthConfigure)
	req.HostSandbox, req.RequireIsolation, req.SandboxTmpRoot = true, true, t.TempDir()

	gates, err := RunBuildGates(context.Background(), req, deps)
	if err != nil {
		t.Fatalf("RunBuildGates: %v", err)
	}
	if spy.spawns != 1 {
		t.Errorf("%d spawn(s); the sandboxed build should have run", spy.spawns)
	}
	if g := gateNamed(t, gates, GateConfigure); g.Outcome != OutcomePass {
		t.Errorf("configure gate = %+v, want PASS", g)
	}
}

// TestSandbox_RealBubblewrapHidesFilesAndNetwork runs the real bwrap with
// the argv a build gets, proving what the unit tests only assert on paper: a
// file in a hidden directory is unreadable, the exposed paths are readable,
// only the private directory is writable, and loopback is the only network
// interface. It is skipped where bwrap cannot create its namespaces.
func TestSandbox_RealBubblewrapHidesFilesAndNetwork(t *testing.T) {
	if ok, why := probeHostSandbox(); !ok {
		t.Skipf("bubblewrap sandbox unavailable here: %s", why)
	}
	base := t.TempDir() // under /tmp, which the sandbox hides
	secret := filepath.Join(base, "secret")
	exposed := filepath.Join(base, "exposed")
	private := filepath.Join(base, "private")
	for _, dir := range []string{exposed, private} {
		if err := os.Mkdir(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(secret, []byte("TOPSECRET"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(exposed, "input"), []byte("ok"), 0o600); err != nil {
		t.Fatal(err)
	}

	script := `cat "$1" 2>/dev/null && echo SECRET-READ
cat "$2/input" || echo EXPOSED-UNREADABLE
(echo x > "$2/written") 2>/dev/null && echo EXPOSED-WRITABLE
echo x > "$3/written" || echo PRIVATE-UNWRITABLE
echo IFACES; tail -n +3 /proc/net/dev | cut -d: -f1 | tr -d ' '`
	args := sandboxArgs([]string{exposed}, private, private, "sh", []string{"-c", script, "sh", secret, exposed, private})
	out, err := exec.CommandContext(t.Context(), sandboxBinary, args...).CombinedOutput()
	if err != nil {
		t.Fatalf("bwrap: %v\n%s", err, out)
	}
	got := string(out)
	for _, bad := range []string{"SECRET-READ", "EXPOSED-UNREADABLE", "EXPOSED-WRITABLE", "PRIVATE-UNWRITABLE"} {
		if strings.Contains(got, bad) {
			t.Errorf("sandbox output reports %s:\n%s", bad, got)
		}
	}
	// /proc/net/dev lists the reader's own network namespace (/sys would show
	// the host's, since it is the host's sysfs mounted read-only).
	_, ifaces, _ := strings.Cut(got, "IFACES\n")
	if strings.TrimSpace(ifaces) != "lo" {
		t.Errorf("network interfaces inside the sandbox: %q, want only lo", ifaces)
	}
}
