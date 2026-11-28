package validate

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeStagedEbuild writes body as the request's staged candidate ebuild.
func writeStagedEbuild(t *testing.T, req BuildRequest, body string) {
	t.Helper()
	category, pkg, err := splitContentAtom(req.Key)
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(req.StagedRoot, category, pkg)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, pkg+"-"+req.Version+".ebuild"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// llamaSwapRestrict is the shape of sci-ml/llama-swap-275's restriction: the
// Go module cache is filled in src_unpack, so the build needs the network.
const llamaSwapRestrict = "EAPI=8\ninherit go-module\n\n# go mod download runs in src_unpack.\nRESTRICT=\"network-sandbox\"\n"

// TestRunBuildGates_HostSandboxDeclinesANetworkSandboxRestrict: on the host
// backend the sandbox runs offline by policy, so an ebuild that needs the
// network is declined over the candidate, naming the restriction, and nothing
// is spawned. Built anyway, sci-ml/llama-swap died in unpack and was read as a
// fault of the environment (2026-11-28).
func TestRunBuildGates_HostSandboxDeclinesANetworkSandboxRestrict(t *testing.T) {
	spy := &buildSpy{}
	deps := buildSeam(spy, configureOKLog, nil)
	deps.SandboxProbe = func() (bool, string) { return true, "" }
	req := buildRequestFor(t, DepthConfigure)
	req.HostSandbox, req.SandboxTmpRoot = true, t.TempDir()
	writeStagedEbuild(t, req, llamaSwapRestrict)

	gates, err := RunBuildGates(context.Background(), req, deps)
	if err != nil {
		t.Fatalf("RunBuildGates: %v", err)
	}
	if spy.spawns != 0 {
		t.Fatalf("%d command(s) spawned; an offline build of a network-restricted ebuild proves nothing", spy.spawns)
	}
	for _, g := range gates {
		if g.Outcome != OutcomeSkipped || g.Declined != DeclineCandidate {
			t.Errorf("gate %s = %+v, want SKIPPED declined by the candidate", g.Gate, g)
		}
		if !strings.Contains(g.Reason, `RESTRICT="network-sandbox"`) || !strings.Contains(g.Reason, "offline") {
			t.Errorf("gate %s reason %q does not name the restriction and the offline policy", g.Gate, g.Reason)
		}
	}
}

// TestRunBuildGates_OffTheHostSandboxARestrictStillBuilds: a container or VM
// build runs Portage's own sandbox, which honours the restriction itself.
func TestRunBuildGates_OffTheHostSandboxARestrictStillBuilds(t *testing.T) {
	spy := &buildSpy{}
	deps := buildSeam(spy, configureOKLog, nil)
	deps.IsolationProbe = func() (bool, string) { return true, "" }
	req := buildRequestFor(t, DepthConfigure)
	writeStagedEbuild(t, req, llamaSwapRestrict)

	if _, err := RunBuildGates(context.Background(), req, deps); err != nil {
		t.Fatalf("RunBuildGates: %v", err)
	}
	if spy.spawns != 1 {
		t.Errorf("%d spawn(s); without the host sandbox the build must run", spy.spawns)
	}
}

func TestDeclaresNetworkSandboxRestrict(t *testing.T) {
	cases := map[string]bool{
		`RESTRICT="network-sandbox"`:                   true,
		"RESTRICT=\"mirror\nnetwork-sandbox\ttest\"":   true,
		`RESTRICT+=" network-sandbox"`:                 true,
		`  RESTRICT='strip network-sandbox'`:           true,
		`RESTRICT="!test? ( network-sandbox )"`:        true,
		`RESTRICT=network-sandbox`:                     true,
		`RESTRICT="mirror test"`:                       false,
		`# RESTRICT="network-sandbox" is not needed`:   false,
		`MY_RESTRICT="network-sandbox"`:                false,
		`RESTRICT="network-sandbox-extra"`:             false,
		"src_unpack() {\n\tgo-module_live_vendor\n}\n": false,
	}
	for body, want := range cases {
		if got := declaresNetworkSandboxRestrict(body); got != want {
			t.Errorf("declaresNetworkSandboxRestrict(%q) = %v, want %v", body, got, want)
		}
	}
}
