package main

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// Sub-task 15.2 — S046-R3.6, S046-R3.7: one refused ambient mode produces one
// sentence.
//
// Two call sites state it on a REAL `overlay manifest` run: reportModeOrPlain,
// which every report producer goes through since 12.1, and manifestUsesTUI's
// own logger.Warn. A --dry-run returns tui.Noop() before the second gate is
// reached, which is why five audits measured this as single-voiced — the one
// flag used to check is the one flag that hides the second voice. Hence the
// Red case here is the run WITHOUT --dry-run.
//
// The run is executed through the built binary (S046-R8.4): the doubling is on
// stderr, and only a real entry point shows what an operator reads.
//
// Every name carries the TestManifestSingleVoice prefix so one -run pattern
// selects the file whole.

const manifestRefusal = "is not a UI mode"

// manifestSingleVoiceOverlay builds a minimal valid overlay with one package
// and a config pointing at it, and returns the environment a run gets. PATH is
// an empty directory on purpose: pkgdev is never found, so the run fails fast
// and identically everywhere, and chooseManifestReporter — the gate under test
// — is reached before any of that matters.
func manifestSingleVoiceEnv(t *testing.T) []string {
	t.Helper()

	root := t.TempDir()
	overlay := filepath.Join(root, "overlay")
	pkg := filepath.Join(overlay, "app-misc", "foo")
	for _, dir := range []string{filepath.Join(overlay, "profiles"), filepath.Join(overlay, "metadata"), pkg,
		filepath.Join(root, "home", ".config", "bentoo"), filepath.Join(root, "emptypath")} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("preparing %s: %v", dir, err)
		}
	}
	write := func(path, content string) {
		t.Helper()
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatalf("writing %s: %v", path, err)
		}
	}
	write(filepath.Join(overlay, "profiles", "repo_name"), "testoverlay\n")
	write(filepath.Join(overlay, "metadata", "layout.conf"), "masters = gentoo\n")
	write(filepath.Join(pkg, "foo-1.0.ebuild"), "EAPI=8\n")
	write(filepath.Join(root, "home", ".config", "bentoo", "config.yaml"), "overlay:\n  path: "+overlay+"\n")

	return []string{
		"PATH=" + filepath.Join(root, "emptypath"),
		"HOME=" + filepath.Join(root, "home"),
		"XDG_CONFIG_HOME=" + filepath.Join(root, "home", ".config"),
	}
}

// manifestSingleVoiceRun runs `overlay manifest` with the given extra env and
// arguments, returning combined output and exit status.
func manifestSingleVoiceRun(t *testing.T, bin string, env []string, extraEnv string, args ...string) (string, int) {
	t.Helper()

	cmd := exec.Command(bin, append([]string{"overlay", "manifest"}, args...)...)
	cmd.Env = env
	if extraEnv != "" {
		cmd.Env = append(cmd.Env, extraEnv)
	}
	out, err := cmd.CombinedOutput()

	status := 0
	if err != nil {
		var exit *exec.ExitError
		if !errors.As(err, &exit) {
			t.Fatalf("running %v: %v\n%s", args, err, out)
		}
		status = exit.ExitCode()
	}
	return string(out), status
}

// TestManifestSingleVoiceRealRunStatesTheRefusalOnce is the defect: a real run
// answers one typo in two wordings. The control run pins the other half — the
// exit status must not change, because R3.6 is about how many times the reason
// is stated and nothing else.
func TestManifestSingleVoiceRealRunStatesTheRefusalOnce(t *testing.T) {
	bin := buildBentoo(t)
	env := manifestSingleVoiceEnv(t)

	control, controlStatus := manifestSingleVoiceRun(t, bin, env, "")
	if got := strings.Count(control, manifestRefusal); got != 0 {
		t.Fatalf("the control run stated a refusal %d times with no BENTOO_UI set; the premise is wrong:\n%s", got, control)
	}

	out, status := manifestSingleVoiceRun(t, bin, env, "BENTOO_UI=bogus")
	if got := strings.Count(out, manifestRefusal); got != 1 {
		t.Errorf("a real `overlay manifest` states the refusal %d times; S046-R3.6 requires exactly once.\n"+
			"    reportModeOrPlain states it for every report producer; manifestUsesTUI states it again in a\n"+
			"    second wording on the same run. Observed:\n%s", got, out)
	}
	if status != controlStatus {
		t.Errorf("exit status is %d with an unusable BENTOO_UI and %d without it; refusing a display key "+
			"in words must not change what the run returns (S046-R3.7)", status, controlStatus)
	}
}

// TestManifestSingleVoiceDryRunStillStatesIt is the converse: a fix that
// silenced the second voice by silencing the refusal would satisfy the count
// above and leave the operator with a typo and no sentence. A dry run reaches
// only reportModeOrPlain, so it is where "at least once" is measurable alone.
func TestManifestSingleVoiceDryRunStillStatesIt(t *testing.T) {
	bin := buildBentoo(t)
	env := manifestSingleVoiceEnv(t)

	out, status := manifestSingleVoiceRun(t, bin, env, "BENTOO_UI=bogus", "--dry-run")
	if got := strings.Count(out, manifestRefusal); got != 1 {
		t.Errorf("a dry `overlay manifest` states the refusal %d times, want exactly once.\n"+
			"    Observed:\n%s", got, out)
	}
	if status != 0 {
		t.Errorf("the dry run exited %d, want 0: an unusable display key is refused in words, not in effect", status)
	}
	if !strings.Contains(out, "plain") {
		t.Errorf("the refusal does not name the mode used instead; S046-R3.7 requires the source, the value "+
			"and the mode. Observed:\n%s", out)
	}
}
