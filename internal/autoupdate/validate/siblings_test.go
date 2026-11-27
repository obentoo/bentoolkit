package validate

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// The 2026-11-26 run: media-libs/mesa pins ~dev-util/mesa_clc-${PV}, and both
// were candidates of the same run, each in its own staged tree.
const (
	mesaKey      = "media-libs/mesa"
	mesaVersion  = "27.0.0_pre20261126"
	mesaClcKey   = "dev-util/mesa_clc"
	mesaClcCPV   = "dev-util/mesa_clc-27.0.0_pre20261126"
	vulkanLibCPV = "dev-util/vulkan-utility-libraries-1.4.369_p20261123"
)

// siblingsOverlay is a published overlay with the repository directories
// StageSiblings carries.
func siblingsOverlay(t *testing.T) string {
	t.Helper()
	overlay := t.TempDir()
	for path, body := range map[string]string{
		"eclass/bentoo.eclass":    "# eclass\n",
		"profiles/repo_name":      "bentoo\n",
		"profiles/package.mask":   "# masks\n",
		"metadata/layout.conf":    "masters = gentoo\nthin-manifests = true\n",
		"dev-util/mesa_clc/x.txt": "not carried\n",
	} {
		full := filepath.Join(overlay, path)
		if err := os.MkdirAll(filepath.Dir(full), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return overlay
}

func stageMesaSiblings(t *testing.T) Siblings {
	t.Helper()
	repo, err := StageSiblings(SiblingsRequest{
		Overlay: siblingsOverlay(t),
		Parent:  t.TempDir(),
		Candidates: []SiblingCandidate{
			{Key: mesaKey, Version: mesaVersion, EbuildBytes: []byte("EAPI=8\nRDEPEND=\"~dev-util/mesa_clc-${PV}\"\n")},
			{Key: mesaClcKey, Version: mesaVersion, EbuildBytes: []byte("EAPI=8\n")},
		},
	})
	if err != nil {
		t.Fatalf("StageSiblings: %v", err)
	}
	t.Cleanup(func() { _ = repo.Remove() })
	return repo
}

// TestStageSiblings_HoldsEveryCandidateUnderAPortageLegalName: one repository,
// every candidate's ebuild, a repo_name matching its directory, and a name
// Portage accepts (not ending in a hyphen and digits).
func TestStageSiblings_HoldsEveryCandidateUnderAPortageLegalName(t *testing.T) {
	repo := stageMesaSiblings(t)

	raw, err := os.ReadFile(filepath.Join(repo.Root, "profiles", "repo_name"))
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.TrimSpace(string(raw)); got != repo.Name() {
		t.Errorf("repo_name = %q, want the directory name %q", got, repo.Name())
	}
	if !strings.HasPrefix(repo.Name(), stagedRepoPrefix) || regexp.MustCompile(`-[0-9]+$`).MatchString(repo.Name()) {
		t.Errorf("repository name %q is not a recognisable, Portage-legal staging name", repo.Name())
	}
	for _, rel := range []string{
		"media-libs/mesa/mesa-27.0.0_pre20261126.ebuild",
		"dev-util/mesa_clc/mesa_clc-27.0.0_pre20261126.ebuild",
		"eclass/bentoo.eclass",
		"metadata/layout.conf",
	} {
		if _, err := os.Stat(filepath.Join(repo.Root, rel)); err != nil {
			t.Errorf("%s is missing from the siblings repository: %v", rel, err)
		}
	}
	if !repo.Holds(mesaClcCPV) || !repo.Holds("media-libs/mesa-"+mesaVersion) || repo.Holds("media-libs/mesa-1") {
		t.Errorf("Holds disagrees with the staged candidates %v", repo.CPVs)
	}

	if err := repo.Remove(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(repo.Root); !os.IsNotExist(err) {
		t.Errorf("Remove left %s behind: %v", repo.Root, err)
	}
}

// fixedEmerge is an emerge seam printing out and exiting with code.
func fixedEmerge(spy *depsSeam, out string, code string) BuildDeps {
	return BuildDeps{
		ExecCommand: func(ctx context.Context, name string, arg ...string) *exec.Cmd {
			spy.spawns++
			spy.args = append(spy.args, arg)
			cmd := exec.CommandContext(ctx, "sh", "-c", `printf %s "$1"; exit "$2"`, "sh", out, code)
			spy.cmd = cmd
			return cmd
		},
		LookPath: func(name string) (string, error) { return "/usr/bin/" + name, nil },
	}
}

// hermeticRepos pins the host repository configuration the resolve composes
// on, so no test reads /etc/portage.
func hermeticRepos(t *testing.T) {
	t.Setenv(portageRepositoriesVar, "[gentoo]\nlocation = /var/db/repos/gentoo\n")
}

func childRepositories(t *testing.T, cmd *exec.Cmd) string {
	t.Helper()
	// The last assignment is the one the child sees: the inherited value comes
	// first in the environment, the composed one after it.
	for i := len(cmd.Env) - 1; i >= 0; i-- {
		if value, ok := strings.CutPrefix(cmd.Env[i], portageRepositoriesVar+"="); ok {
			return value
		}
	}
	t.Fatalf("%s is absent from the child environment", portageRepositoriesVar)
	return ""
}

// TestDependenciesSatisfiedIn_ASameRunSiblingResolvesAndIsNamedAsOne: the
// siblings repository is registered below the candidate's own tree, and the
// sibling the resolve lists is named as a candidate of this run, not as
// something to install from a repository that has it.
func TestDependenciesSatisfiedIn_ASameRunSiblingResolvesAndIsNamedAsOne(t *testing.T) {
	hermeticRepos(t)
	repo := stageMesaSiblings(t)
	spy := &depsSeam{}
	out := "[ebuild  N    ] " + mesaClcCPV + "::" + repo.Name() + "\n" +
		"[ebuild  N    ] media-libs/mesa-" + mesaVersion + "\n"

	ok, missing, err := DependenciesSatisfiedIn(context.Background(), t.TempDir(), mesaKey, mesaVersion,
		ResolveScope{Siblings: repo}, fixedEmerge(spy, out, "0"))
	if err != nil {
		t.Fatalf("DependenciesSatisfiedIn: %v", err)
	}
	if ok || len(missing) != 1 || missing[0] != mesaClcCPV+siblingNote {
		t.Fatalf("missing = %v, want only %s marked as a sibling", missing, mesaClcCPV)
	}

	repos := childRepositories(t, spy.cmd)
	section := "[" + repo.Name() + "]\nlocation = " + repo.Root + "\nmasters = gentoo\npriority = " + siblingsPriority + "\n"
	if !strings.Contains(repos, section) {
		t.Errorf("the siblings repository is not registered at priority %s:\n%s", siblingsPriority, repos)
	}
	if action := DependencyAction(missing); !strings.Contains(action, "publish and install "+mesaClcCPV) ||
		strings.HasPrefix(action, "install ") {
		t.Errorf("DependencyAction = %q, want the sibling named as a candidate of this run", action)
	}
}

// TestDependenciesSatisfiedIn_ZeroScopeRegistersNoSiblings: without a run's
// siblings the composed configuration is what it always was.
func TestDependenciesSatisfiedIn_ZeroScopeRegistersNoSiblings(t *testing.T) {
	hermeticRepos(t)
	spy := &depsSeam{}
	if _, _, err := DependenciesSatisfied(context.Background(), t.TempDir(), mesaKey, mesaVersion,
		fixedEmerge(spy, "[ebuild  N    ] media-libs/mesa-"+mesaVersion+"\n", "0")); err != nil {
		t.Fatal(err)
	}
	if repos := childRepositories(t, spy.cmd); strings.Contains(repos, "siblings") || strings.Contains(repos, "priority") {
		t.Errorf("a zero scope registered more than the staged tree:\n%s", repos)
	}
}

// TestDependenciesSatisfiedIn_ACooldownHeldDependencyIsNamedAsOne is the
// vulkan-layers case of 2026-11-26: the host unmasks cooldown.unmask, so the
// resolve succeeds and lists the held version to install. The action names the
// cooldown rather than a bare "install".
func TestDependenciesSatisfiedIn_ACooldownHeldDependencyIsNamedAsOne(t *testing.T) {
	hermeticRepos(t)
	out := "[ebuild  N    ] " + vulkanLibCPV + "::bentoo\n" +
		"[ebuild  N    ] dev-libs/plain-1.0\n" +
		"[ebuild  N    ] media-libs/vulkan-layers-1.4.369_p20261126\n"

	ok, missing, err := DependenciesSatisfiedIn(context.Background(), t.TempDir(), "media-libs/vulkan-layers", "1.4.369_p20261126",
		ResolveScope{CooldownHeld: map[string]bool{vulkanLibCPV: true}}, fixedEmerge(&depsSeam{}, out, "0"))
	if err != nil || ok {
		t.Fatalf("ok=%v err=%v, want a determined unsatisfied answer", ok, err)
	}
	action := DependencyAction(missing)
	for _, want := range []string{"install dev-libs/plain-1.0", "the cooldown still masks", "bentoo overlay cooldown confirm =" + vulkanLibCPV, "lab VM"} {
		if !strings.Contains(action, want) {
			t.Errorf("DependencyAction = %q, missing %q", action, want)
		}
	}
}

// emergeMaskedByCooldown is a resolve refused on a host that does NOT unmask
// cooldown.unmask.
const emergeMaskedByCooldown = "\n!!! All ebuilds that could satisfy \"~" + vulkanLibCPV + "\" have been masked.\n" +
	"!!! One of the following masked packages is required to complete your request:\n" +
	"- " + vulkanLibCPV + "::bentoo (masked by: package.mask)\n" +
	"/var/db/repos/bentoo/profiles/package.mask:\n" +
	"# cooldown since=2026-11-23T20:22:27Z origin=autoupdate line=dev-util/vulkan-utility-libraries\n"

// TestDependenciesSatisfiedIn_AResolveRefusedOnlyByTheCooldownIsAnAnswer: the
// mask is ours and temporary, so the host's answer is "it lacks this version",
// named as held — not an undetermined failure that names nothing.
func TestDependenciesSatisfiedIn_AResolveRefusedOnlyByTheCooldownIsAnAnswer(t *testing.T) {
	hermeticRepos(t)
	held := ResolveScope{CooldownHeld: map[string]bool{vulkanLibCPV: true}}

	ok, missing, err := DependenciesSatisfiedIn(context.Background(), t.TempDir(), "media-libs/vulkan-layers", "1.4.369_p20261126",
		held, fixedEmerge(&depsSeam{}, emergeMaskedByCooldown, "1"))
	if err != nil {
		t.Fatalf("a cooldown-masked resolve was reported as undetermined: %v", err)
	}
	if ok || len(missing) != 1 || missing[0] != vulkanLibCPV+cooldownNote {
		t.Fatalf("missing = %v, want only %s marked as held", missing, vulkanLibCPV)
	}

	// A mask that is not the cooldown's stays undetermined.
	_, _, err = DependenciesSatisfiedIn(context.Background(), t.TempDir(), "media-libs/vulkan-layers", "1.4.369_p20261126",
		ResolveScope{CooldownHeld: map[string]bool{"dev-util/other-1": true}}, fixedEmerge(&depsSeam{}, emergeMaskedByCooldown, "1"))
	if err == nil {
		t.Error("a resolve refused by a mask the cooldown does not hold was read as an answer")
	}
}
