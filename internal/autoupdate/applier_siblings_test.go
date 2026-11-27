package autoupdate

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/obentoo/bentoolkit/internal/autoupdate/cooldown"
	"github.com/obentoo/bentoolkit/internal/autoupdate/validate"
)

// siblingsApplier is an applier over an overlay holding mesa and mesa_clc at
// their current versions, with a staging root, as a run's applier is.
func siblingsApplier(t *testing.T) *Applier {
	t.Helper()
	overlay := t.TempDir()
	for path, body := range map[string]string{
		"profiles/repo_name": "bentoo\n",
		"eclass/x.eclass":    "# eclass\n",
		"media-libs/mesa/mesa-27.0.0_pre20261125.ebuild":       "EAPI=8\nRDEPEND=\"~dev-util/mesa_clc-${PV}\"\n",
		"dev-util/mesa_clc/mesa_clc-27.0.0_pre20261125.ebuild": "EAPI=8\n",
	} {
		full := filepath.Join(overlay, path)
		if err := os.MkdirAll(filepath.Dir(full), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return &Applier{overlayPath: overlay, stagingRoot: t.TempDir(), siblings: &siblingsState{}}
}

var mesaRun = []PendingUpdate{
	{Package: "media-libs/mesa", NewVersion: "27.0.0_pre20261126"},
	{Package: "dev-util/mesa_clc", NewVersion: "27.0.0_pre20261126"},
}

// TestStageRunSiblings_EveryCandidateIsVisibleUntilReleased: the run's
// candidates are staged once, each at its candidate version, and the release
// removes the repository and takes it out of every later resolve.
func TestStageRunSiblings_EveryCandidateIsVisibleUntilReleased(t *testing.T) {
	a := siblingsApplier(t)

	release := a.StageRunSiblings(mesaRun)
	repo := a.resolveScope().Siblings
	if repo.Root == "" {
		t.Fatal("no siblings repository was staged for a two-candidate run")
	}
	for _, cpv := range []string{"media-libs/mesa-27.0.0_pre20261126", "dev-util/mesa_clc-27.0.0_pre20261126"} {
		if !repo.Holds(cpv) {
			t.Errorf("the siblings repository does not hold %s: %v", cpv, repo.CPVs)
		}
	}
	body, err := os.ReadFile(filepath.Join(repo.Root, "media-libs/mesa/mesa-27.0.0_pre20261126.ebuild"))
	if err != nil || !strings.Contains(string(body), "mesa_clc") {
		t.Errorf("the sibling ebuild is not the current ebuild copied to the candidate version: %q, %v", body, err)
	}

	release()
	if _, err := os.Stat(repo.Root); !os.IsNotExist(err) {
		t.Errorf("release left %s behind: %v", repo.Root, err)
	}
	if got := a.resolveScope().Siblings; got.Root != "" {
		t.Errorf("a resolve after release still sees %s", got.Root)
	}
}

// TestStageRunSiblings_ASingleCandidateHasNoSiblings: nothing is staged when
// there is nobody to resolve against.
func TestStageRunSiblings_ASingleCandidateHasNoSiblings(t *testing.T) {
	a := siblingsApplier(t)
	release := a.StageRunSiblings(mesaRun[:1])
	defer release()
	if got := a.resolveScope().Siblings; got.Root != "" {
		t.Errorf("a one-candidate run staged siblings at %s", got.Root)
	}
}

// TestStageRunSiblings_ConcurrentResolvesReadOneRepository: --concurrency
// workers read the scope while the run releases it; the race detector is the
// assertion.
func TestStageRunSiblings_ConcurrentResolvesReadOneRepository(t *testing.T) {
	a := siblingsApplier(t)
	release := a.StageRunSiblings(mesaRun)
	view := a.withoutAgents()

	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range 50 {
				_ = view.resolveScope()
			}
		}()
	}
	release()
	wg.Wait()
}

// fakeEmerge answers every pretend resolve with out.
func fakeEmerge(out string) validate.BuildDeps {
	return validate.BuildDeps{
		ExecCommand: func(ctx context.Context, _ string, _ ...string) *exec.Cmd {
			return exec.CommandContext(ctx, "printf", "%s", out)
		},
		LookPath: func(name string) (string, error) { return "/usr/bin/" + name, nil },
	}
}

// stagedCandidateTree is a candidate's own staged tree, as Stage leaves it.
func stagedCandidateTree(t *testing.T, name string) candidatePaths {
	t.Helper()
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "profiles"), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "profiles", "repo_name"), []byte(name+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return candidatePaths{staged: true, repoRoot: root}
}

// TestHostDependencyGates_NamesASameRunSibling is the mesa case end to end:
// with the run's siblings registered, the resolve lists mesa_clc from them, and
// the declined gate says it is a candidate of this run rather than "install".
func TestHostDependencyGates_NamesASameRunSibling(t *testing.T) {
	t.Setenv("PORTAGE_REPOSITORIES", "[gentoo]\nlocation = /var/db/repos/gentoo\n")
	a := siblingsApplier(t)
	release := a.StageRunSiblings(mesaRun)
	defer release()
	repo := a.resolveScope().Siblings

	out := "[ebuild  N    ] dev-util/mesa_clc-27.0.0_pre20261126::" + repo.Name() + "\n" +
		"[ebuild  N    ] media-libs/mesa-27.0.0_pre20261126\n"
	gates, stop := a.hostDependencyGates(context.Background(), stagedCandidateTree(t, "bentoolkit-staging-mesa-27_0_0_pre20261126"),
		"media-libs/mesa", "27.0.0_pre20261126", validate.DepthCompile, fakeEmerge(out))
	if !stop || len(gates) == 0 {
		t.Fatalf("stop=%v gates=%v, want the build gates declined", stop, gates)
	}
	if reason := gates[0].Reason; !strings.Contains(reason, "publish and install dev-util/mesa_clc-27.0.0_pre20261126 (a candidate of this same run") {
		t.Errorf("reason = %q, want the sibling named as a candidate of this run", reason)
	}
}

// TestHostDependencyGates_NamesACooldownHeldDependency is the vulkan-layers
// case: the dependency is an overlay version the cooldown masks.
func TestHostDependencyGates_NamesACooldownHeldDependency(t *testing.T) {
	t.Setenv("PORTAGE_REPOSITORIES", "[gentoo]\nlocation = /var/db/repos/gentoo\n")
	a := siblingsApplier(t)
	a.cooldown = &cooldownState{loaded: true, block: &cooldown.Block{Days: 7, Entries: []cooldown.Entry{
		{Package: "dev-util/vulkan-utility-libraries", Version: "1.4.369_p20261123"},
	}}}

	out := "[ebuild  N    ] dev-util/vulkan-utility-libraries-1.4.369_p20261123::bentoo\n" +
		"[ebuild  N    ] media-libs/vulkan-layers-1.4.369_p20261126\n"
	gates, stop := a.hostDependencyGates(context.Background(), stagedCandidateTree(t, "bentoolkit-staging-vulkan-layers-1_4_369_p20261126"),
		"media-libs/vulkan-layers", "1.4.369_p20261126", validate.DepthCompile, fakeEmerge(out))
	if !stop || len(gates) == 0 {
		t.Fatalf("stop=%v gates=%v, want the build gates declined", stop, gates)
	}
	if reason := gates[0].Reason; !strings.Contains(reason, "`bentoo overlay cooldown confirm =dev-util/vulkan-utility-libraries-1.4.369_p20261123`") {
		t.Errorf("reason = %q, want the cooldown named", reason)
	}
}
