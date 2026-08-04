package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/obentoo/bentoolkit/internal/autoupdate"
)

// writeLintRegistry creates an overlay whose .autoupdate/packages.toml holds
// content and returns the overlay path.
func writeLintRegistry(t *testing.T, content string) string {
	t.Helper()
	dir := t.TempDir()
	autoDir := filepath.Join(dir, ".autoupdate")
	if err := os.MkdirAll(autoDir, 0o750); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(autoDir, "packages.toml"), []byte(content), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	return dir
}

// TestRunLintClean pins that a registry obeying the record model exits without
// calling osExit at all.
func TestRunLintClean(t *testing.T) {
	dir := writeLintRegistry(t, `["dev-util/claude-code"]
url = "https://registry.npmjs.org/@anthropic-ai/claude-code"
parser = "json"
path = "dist-tags.latest"
comments = """
claude-code — npm dist-tags.latest is the stable channel.
"""
# END
`)

	if code := withExitIntercept(func() { runLint(dir) }); code != -1 {
		t.Fatalf("clean registry exited with %d, want no exit", code)
	}
}

// TestRunLintReportsAndExits pins the gate behavior: a violation exits non-zero
// so the command can be wired into a pre-commit hook. The violation is a banner
// stranded BETWEEN two records — the same block at the top of the file would be
// the file header, which the model allows.
func TestRunLintReportsAndExits(t *testing.T) {
	dir := writeLintRegistry(t, `["dev-util/claude-code"]
url = "https://registry.npmjs.org/@anthropic-ai/claude-code"
parser = "json"
path = "dist-tags.latest"
comments = """
claude-code — npm dist-tags.latest is the stable channel.
"""
# END

# ============ npm registry ============

["sys-apps/pnpm"]
url = "https://registry.npmjs.org/pnpm"
parser = "json"
path = "dist-tags.latest"
comments = """
pnpm — npm package, stable channel.
"""
# END
`)

	if code := withExitIntercept(func() { runLint(dir) }); code != 1 {
		t.Fatalf("got exit %d, want 1", code)
	}
}

// TestRunLintFileHeaderIsClean pins the exemption at the command level: a
// registry whose only comments outside a record are the file header passes the
// gate. Without this the restored ~112-line header of the real overlay would
// fail a pre-commit hook.
func TestRunLintFileHeaderIsClean(t *testing.T) {
	dir := writeLintRegistry(t, `# Bentoo Autoupdate Package Configuration
# Every record obeys the field order documented here.

["dev-util/claude-code"]
url = "https://registry.npmjs.org/@anthropic-ai/claude-code"
parser = "json"
path = "dist-tags.latest"
comments = """
claude-code — npm dist-tags.latest is the stable channel.
"""
# END
`)

	if code := withExitIntercept(func() { runLint(dir) }); code != -1 {
		t.Fatalf("file header exited with %d, want no exit", code)
	}
}

// TestRunLintMissingRegistry pins that a missing packages.toml is an error, not
// a silent pass — a lint that quietly succeeds on nothing is worse than none.
func TestRunLintMissingRegistry(t *testing.T) {
	if code := withExitIntercept(func() { runLint(t.TempDir()) }); code != 1 {
		t.Fatalf("got exit %d, want 1", code)
	}
}

// ---------------------------------------------------------------------------
// --lint --fix: the diff, and the three gates in front of the write (R7.3)
// ---------------------------------------------------------------------------

// lintFixMessyRegistry carries one instance of every violation --fix repairs,
// plus one it deliberately does not:
//
//   - net-misc/foo declares the retired `binary = true` with no `type` (migrated
//     to `type = "bin"`, R1.2) and a redundant `enabled = true` (deleted, R2.2);
//   - www-client/bar assigns `parser` before `url` (reordered, R3.2);
//   - dev-vcs/tracked tracks commits with no base source — reported by the
//     linter and never repaired, because which base source is right depends on
//     where upstream versions itself (R6.1).
//
// That last record is what lets these tests tell "repaired" apart from "still
// needs a human", which is the difference a run must not blur: the operator who
// reads "repaired!" and commits would be publishing a registry the next --lint
// still fails.
const lintFixMessyRegistry = `["net-misc/foo"]
url = "https://example.com/foo"
parser = "json"
path = "version"
binary = true
enabled = true
comments = """
foo — still declares the retired binary key.
"""
# END

["www-client/bar"]
parser = "json"
url = "https://example.com/bar"
path = "version"
comments = """
bar — fields out of canonical order.
"""
# END

["dev-vcs/tracked"]
track = "commit"
url = "https://api.example.com/commits"
parser = "json"
path = "commit.committer.date"
commit_sha_path = "sha"
comments = """
tracked — commit-tracked with no base source: no repair can guess one.
"""
# END
`

// setLintFix pins --fix for one test and restores it afterwards. Like --yes it
// is a process global whose default is a publish-safety property, so it is never
// left mutated for the next test.
func setLintFix(t *testing.T, v bool) {
	t.Helper()
	orig := autoupdateFix
	t.Cleanup(func() { autoupdateFix = orig })
	autoupdateFix = v
}

// readLintRegistry returns the fixture's packages.toml bytes, so a test can
// compare the file against itself before and after a run.
func readLintRegistry(t *testing.T, overlayDir string) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(overlayDir, ".autoupdate", "packages.toml"))
	if err != nil {
		t.Fatalf("read packages.toml: %v", err)
	}
	return data
}

// runLintCapturing runs runLint over overlayDir and returns its exit code
// (-1 when it did not exit) together with everything it printed.
//
// captureStdout wraps withExitIntercept, never the other way round: runLint ends
// by calling osExit, which the intercept turns into a panic, and capturing on
// the inside would let that panic unwind past the pipe read and yield empty
// output.
func runLintCapturing(t *testing.T, overlayDir string) (int, string) {
	t.Helper()
	code := -1
	out := captureStdout(t, func() {
		code = withExitIntercept(func() { runLint(overlayDir) })
	})
	return code, out
}

// TestAutoupdateLintFixWithoutLintIsRejected pins the flag contract: --fix
// repairs what --lint reports, so on its own it has nothing to repair and is
// refused before any config, file or network work happens.
//
// The assertion is discriminating rather than incidental. HOME holds a valid
// config pointing at an empty temp overlay, so without the guard the run would
// reach the mode switch, fall through to its default branch, print the help and
// NOT exit at all. Exit 1 can therefore only come from the guard.
func TestAutoupdateLintFixWithoutLintIsRejected(t *testing.T) {
	tmpHome := t.TempDir()
	cfgDir := filepath.Join(tmpHome, ".config", "bentoo")
	if err := os.MkdirAll(cfgDir, 0o750); err != nil {
		t.Fatalf("mkdir bentoo config dir: %v", err)
	}
	overlayDir := filepath.Join(tmpHome, "overlay")
	for _, sub := range []string{"profiles", "metadata"} {
		if err := os.MkdirAll(filepath.Join(overlayDir, sub), 0o750); err != nil {
			t.Fatalf("mkdir overlay subdir: %v", err)
		}
	}
	configYAML := "overlay:\n  path: " + overlayDir + "\n  remote: origin\n" +
		"git:\n  user: Test\n  email: test@test.com\n"
	if err := os.WriteFile(filepath.Join(cfgDir, "config.yaml"), []byte(configYAML), 0o600); err != nil {
		t.Fatalf("write config.yaml: %v", err)
	}
	t.Setenv("HOME", tmpHome)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(tmpHome, ".config"))

	// Every flag runAutoupdate validates before the mode switch, pinned so this
	// test cannot fail (or pass) on another test's leftovers.
	origLint, origFix, origConc, origTimeout, origOnly, origApply, origCheck :=
		autoupdateLint, autoupdateFix, autoupdateConcurrency, autoupdateTimeout,
		autoupdateOnly, autoupdateApply, autoupdateCheck
	t.Cleanup(func() {
		autoupdateLint, autoupdateFix, autoupdateConcurrency, autoupdateTimeout,
			autoupdateOnly, autoupdateApply, autoupdateCheck =
			origLint, origFix, origConc, origTimeout, origOnly, origApply, origCheck
	})
	autoupdateLint = false // the whole point: --fix on its own
	autoupdateFix = true
	autoupdateConcurrency = autoupdate.DefaultConcurrency
	autoupdateTimeout = 0
	autoupdateOnly = ""
	autoupdateApply = ""
	autoupdateCheck = false

	var code int
	captureStdout(t, func() {
		code = withExitIntercept(func() { runAutoupdate(autoupdateCmd, nil) })
	})

	if code != 1 {
		t.Fatalf("--fix without --lint exited %d, want 1", code)
	}
}

// TestAutoupdateLintFixDeclineLeavesRegistryByteIdentical pins R7.3's decline:
// a "no" answer leaves packages.toml exactly as it was — proven on the BYTES,
// with the real RepairResult.Write still wired and the real confirmAction
// reading a fake stdin. A mock write seam would pass here even if the gate were
// deleted.
func TestAutoupdateLintFixDeclineLeavesRegistryByteIdentical(t *testing.T) {
	dir := writeLintRegistry(t, lintFixMessyRegistry)
	before := readLintRegistry(t, dir)

	setLintFix(t, true)
	setReconcileYes(t, false)
	setReconcileInteractive(t, func() bool { return true }) // pretend a terminal
	feedStdin(t, "n\n")

	code, out := runLintCapturing(t, dir)

	after := readLintRegistry(t, dir)
	if !bytes.Equal(before, after) {
		t.Errorf("a declined repair wrote to packages.toml (R7.3)\nbefore:\n%s\nafter:\n%s", before, after)
	}
	// The diff must have been shown BEFORE the question: approving a change
	// nobody has seen is the failure this gate exists to prevent.
	for _, want := range []string{"@@", "-binary = true", "+type = \"bin\"", "-enabled = true"} {
		if !strings.Contains(out, want) {
			t.Errorf("the repair was not shown as a diff: missing %q in:\n%s", want, out)
		}
	}
	if i, j := strings.Index(out, "@@"), strings.Index(out, "packages.toml?"); i < 0 || j < 0 || i > j {
		t.Errorf("the prompt came before the diff (diff at %d, prompt at %d); got:\n%s", i, j, out)
	}
	if strings.Contains(out, "Repaired packages.toml") {
		t.Errorf("a declined run claimed to have repaired; got:\n%s", out)
	}
	if code != 1 {
		t.Errorf("a declined repair exited %d, want 1: every finding is still there", code)
	}
}

// TestAutoupdateLintFixYesWritesWithoutReadingStdin pins R7.3's first clause:
// --yes repairs unattended. Stdin is a real (non-terminal) file holding "n", so
// a run that consulted it at all would decline and write nothing; the TTY probe
// and the confirm seam are tripwires, so a run that even asked whether it may
// prompt fails.
//
// It also pins the honesty of the summary: the one finding no repair can guess
// survives, is named, and keeps the exit code non-zero.
func TestAutoupdateLintFixYesWritesWithoutReadingStdin(t *testing.T) {
	dir := writeLintRegistry(t, lintFixMessyRegistry)

	setLintFix(t, true)
	setReconcileYes(t, true)
	setReconcileInteractive(t, func() bool {
		t.Error("--yes must not consult the TTY probe: it is an explicit approval")
		return false
	})
	setReconcileConfirm(t, func(string) bool {
		t.Error("--yes must not prompt: stdin was read")
		return false
	})
	feedStdin(t, "n\n") // the trap: reading this would decline

	code, out := runLintCapturing(t, dir)
	got := string(readLintRegistry(t, dir))

	if !strings.Contains(out, "--yes given") {
		t.Errorf("the unattended write did not announce itself; got:\n%s", out)
	}
	// R1.2/R1.3/R2.2: the retired key is migrated, the redundant one deleted.
	for _, gone := range []string{"binary = true", "enabled = true"} {
		if strings.Contains(got, gone) {
			t.Errorf("--yes did not repair %q; registry:\n%s", gone, got)
		}
	}
	if !strings.Contains(got, `type = "bin"`) {
		t.Errorf("--yes did not migrate binary to type; registry:\n%s", got)
	}
	// R3.2: the reordered record, checked as the exact block it must now be.
	wantBlock := "[\"www-client/bar\"]\nurl = \"https://example.com/bar\"\nparser = \"json\"\npath = \"version\"\n"
	if !strings.Contains(got, wantBlock) {
		t.Errorf("--yes did not reorder www-client/bar into canonical order; registry:\n%s", got)
	}
	// R3.3: the only documentation 411 records have must survive the rewrite.
	if !strings.Contains(got, "foo — still declares the retired binary key.") {
		t.Errorf("the repair lost a comments block; registry:\n%s", got)
	}
	// The finding --fix declines to guess at is still there, said out loud — and
	// it is the ONLY one left, which is also the idempotence claim: a repaired
	// registry re-lints silent except for what no repair offers.
	if !strings.Contains(out, "1 finding(s) still need a human") {
		t.Errorf("the run implied a clean registry while a finding remains; got:\n%s", out)
	}
	if !strings.Contains(out, autoupdate.LintLegacyBase) {
		t.Errorf("the remaining finding is not named; got:\n%s", out)
	}
	if at := strings.Index(out, "still need a human"); at >= 0 {
		tail := out[at:]
		for _, repaired := range []string{autoupdate.LintLegacyBinary, autoupdate.LintRedundantEnabled, autoupdate.LintFieldOrder} {
			if strings.Contains(tail, repaired) {
				t.Errorf("%s is listed as still needing a human, but --fix repairs it; got:\n%s", repaired, out)
			}
		}
	}
	if code != 1 {
		t.Errorf("exit code = %d, want 1: a finding the repair does not touch is still a finding", code)
	}
}

// TestAutoupdateLintFixNonTTYWithoutYesWritesNothing pins R7.3's second clause:
// a piped or scripted run prints the diff, writes nothing, and says how to write
// it — the confirm seam is a tripwire, because prompting a pipe is how
// `yes | bentoo …` publishes a rewrite nobody read.
func TestAutoupdateLintFixNonTTYWithoutYesWritesNothing(t *testing.T) {
	dir := writeLintRegistry(t, lintFixMessyRegistry)
	before := readLintRegistry(t, dir)

	setLintFix(t, true)
	setReconcileYes(t, false)
	setReconcileInteractive(t, func() bool { return false }) // piped / CI
	setReconcileConfirm(t, func(string) bool {
		t.Error("a non-interactive run must not prompt")
		return true
	})

	code, out := runLintCapturing(t, dir)

	if after := readLintRegistry(t, dir); !bytes.Equal(before, after) {
		t.Errorf("a non-interactive run wrote to packages.toml (R7.3)\nbefore:\n%s\nafter:\n%s", before, after)
	}
	if !strings.Contains(out, "@@") || !strings.Contains(out, "+type = \"bin\"") {
		t.Errorf("the repair was not printed as a diff; got:\n%s", out)
	}
	if !strings.Contains(out, "--yes") {
		t.Errorf("the refusal does not say how to write it unattended; got:\n%s", out)
	}
	if code != 1 {
		t.Errorf("exit code = %d, want 1: nothing was repaired", code)
	}
}

// TestAutoupdateLintFixCleanRegistryDoesNotPrompt pins the no-op case: a repair
// with nothing to write must not ask. "Confirm writing 0 changes?" is the
// question that teaches an operator to answer yes without reading, and the one
// prompt here that has to survive that habit is the one that publishes.
func TestAutoupdateLintFixCleanRegistryDoesNotPrompt(t *testing.T) {
	dir := writeLintRegistry(t, `["dev-util/claude-code"]
url = "https://registry.npmjs.org/@anthropic-ai/claude-code"
parser = "json"
path = "dist-tags.latest"
comments = """
claude-code — npm dist-tags.latest is the stable channel.
"""
# END
`)
	before := readLintRegistry(t, dir)

	setLintFix(t, true)
	setReconcileYes(t, false)
	setReconcileInteractive(t, func() bool {
		t.Error("nothing changes, so nothing may be confirmed")
		return false
	})
	setReconcileConfirm(t, func(string) bool {
		t.Error("a repair that changes nothing must not prompt")
		return true
	})

	code, out := runLintCapturing(t, dir)

	if after := readLintRegistry(t, dir); !bytes.Equal(before, after) {
		t.Errorf("a no-op repair rewrote packages.toml\nbefore:\n%s\nafter:\n%s", before, after)
	}
	if strings.Contains(out, "@@") {
		t.Errorf("a no-op repair printed a diff; got:\n%s", out)
	}
	if code != -1 {
		t.Errorf("exit code = %d, want no exit: the registry is clean", code)
	}
}

// TestAutoupdateLintFixNothingRepairableSaysItOnce covers the shape a maintainer
// actually hits once the registry has been repaired: every mechanical finding is
// gone, and what is left is the kind --fix declines to guess at.
//
// The run must not print those findings twice. runLint lists them, then --fix
// finds nothing to write — reprinting them under "still need a human" would put
// the same lines on screen twice in one command, and the two sentences read as a
// contradiction besides ("nothing to repair" / "2 issues remain"). One line ties
// them together instead.
func TestAutoupdateLintFixNothingRepairableSaysItOnce(t *testing.T) {
	// track = "commit" with no base_from: reported by legacy-base, which carries
	// no repair on purpose (R6.1).
	dir := writeLintRegistry(t, `["sci-ml/ik_llama-cpp"]
track = "commit"
url = "https://api.github.com/repos/x/y/commits"
parser = "json"
path = "0.sha"
commit_sha_path = "0.sha"
comments = """
ik_llama-cpp — commit-tracked, base version left to the ebuild.
"""
# END
`)
	before := readLintRegistry(t, dir)

	setLintFix(t, true)
	setReconcileYes(t, false)
	setReconcileConfirm(t, func(string) bool {
		t.Error("a repair that changes nothing must not prompt")
		return true
	})

	code, out := runLintCapturing(t, dir)

	if after := readLintRegistry(t, dir); !bytes.Equal(before, after) {
		t.Errorf("a no-op repair rewrote packages.toml\nbefore:\n%s\nafter:\n%s", before, after)
	}

	// The finding LINE appears exactly once: runLint's listing. The old code
	// added a second copy under the "still need a human" heading.
	//
	// Counted on "[pkg] rule:" rather than on the rule name alone, because the
	// tally block prints the rule name too ("legacy-base  1") — that is a
	// different statement, not a repetition, and must not make this fail.
	finding := "[sci-ml/ik_llama-cpp] " + autoupdate.LintLegacyBase + ":"
	if n := strings.Count(out, finding); n != 1 {
		t.Errorf("the finding line is printed %d time(s), want exactly 1; got:\n%s", n, out)
	}
	if strings.Contains(out, "still need a human:") {
		t.Errorf("the findings were listed a second time; got:\n%s", out)
	}
	// …and the verdict still says both things: nothing was repairable, and that
	// is because what remains has no mechanical fix.
	if !strings.Contains(out, "Nothing to repair") {
		t.Errorf("the run did not say the repair found nothing; got:\n%s", out)
	}
	if !strings.Contains(out, "no mechanical fix") {
		t.Errorf("the run did not explain why nothing was repaired; got:\n%s", out)
	}
	if code != 1 {
		t.Errorf("exit code = %d, want 1: a finding no repair touches is still a finding", code)
	}
}
