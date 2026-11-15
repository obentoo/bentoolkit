package main

// Authored for bentoo story 003 (autoupdate cooldown), sub-task 3.3 (CLI
// half) — R3.1, R3.2, R8.1, R8.2.
//
// Integration through `bentoo overlay autoupdate --apply all` on the testCLI
// overlay. The Applier is the production one, built from the production
// options; the newApplier seam only APPENDS the hermetic ones (every child
// process is `true`, isolation reported verified, the registry pin a no-op).
//
// Contract: design.md "Applier integration" — "Apply entry (runApplyAll and
// the single-package path): call Sweep with time.Now() and pkgdb BEFORE
// checking candidates, then save, then continue. Lifts are reported in the
// run summary." R3.1: the sweep runs when an apply run STARTS.
//
// The hostile half of R3.1 is ORDER. Plant an entry for dev-test/cda-foo-1.0.0
// whose clock ran out years ago and a pending bump of the same package to
// 1.0.1. Sweep first: 1.0.0 is lifted, then 1.0.1 is added with since = now
// and stays masked. Add first: 1.0.1 joins a queue anchored years ago (D2)
// and is lifted in the same run — the bump reaches users unmasked.
//
// A second hostile half: with NOTHING pending, the run still starts, so it
// still sweeps; a quiet day must not freeze every mask.
//
// The lift log (R8.1) is read from the invocation's log file
// ($XDG_STATE_HOME/bentoo/logs/bentoo.log) because the terminal shows WARN and
// above by default. NOTE for Q13: this test lives in cmd/bentoo, which Q13's
// command (./internal/autoupdate/...) does not run; Q12 does.
//
// This file also holds the cooldown fixture helpers the 4.1/4.2 CLI tests
// (overlay_cooldown_test.go) reuse, because 3.3 is materialized first.

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/obentoo/bentoolkit/internal/autoupdate"
	"github.com/obentoo/bentoolkit/internal/autoupdate/validate"
)

// cooldownCmdEntry is one planted entry.
type cooldownCmdEntry struct {
	pkg, version string
	since        time.Time
	origin       string
	group        string
}

const cooldownCmdHandMasks = "# hand mask above the block\n=media-libs/sdl3-3.4.20\n\n"

func cooldownCmdBlock(entries ...cooldownCmdEntry) string {
	var b strings.Builder
	b.WriteString("# BEGIN bentoo-cooldown format=1 days=7\n")
	b.WriteString("# Managed by `bentoo overlay autoupdate`. Edit with `bentoo overlay cooldown`, never by hand.\n")
	for _, e := range entries {
		line := "# cooldown since=" + e.since.UTC().Format(time.RFC3339) + " origin=" + e.origin
		if e.group != "" {
			line += " group=" + e.group
		}
		b.WriteString(line + "\n=" + e.pkg + "-" + e.version + "\n")
	}
	b.WriteString("# END bentoo-cooldown\n")
	return b.String()
}

// plantCooldown writes package.mask (hand masks + block), the unmask list
// those entries imply, and an ebuild for every entry.
func plantCooldown(t *testing.T, c *testCLI, entries ...cooldownCmdEntry) {
	t.Helper()
	ov := c.Overlay()
	for _, d := range []string{"profiles", ".autoupdate"} {
		if err := os.MkdirAll(filepath.Join(ov, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(ov, "profiles", "package.mask"), []byte(cooldownCmdHandMasks+cooldownCmdBlock(entries...)), 0o644); err != nil {
		t.Fatal(err)
	}
	var unmask strings.Builder
	unmask.WriteString("# Managed by bentoo overlay autoupdate\n")
	for _, e := range entries {
		unmask.WriteString("=" + e.pkg + "-" + e.version + "\n")
		cooldownCmdEbuild(t, c, e.pkg, e.version)
	}
	if err := os.WriteFile(filepath.Join(ov, ".autoupdate", "cooldown.unmask"), []byte(unmask.String()), 0o644); err != nil {
		t.Fatal(err)
	}
}

func cooldownCmdEbuild(t *testing.T, c *testCLI, pkg, version string) {
	t.Helper()
	dir := filepath.Join(c.Overlay(), filepath.FromSlash(pkg))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	body := "EAPI=8\nDESCRIPTION=\"cooldown test\"\nSLOT=\"0\"\nKEYWORDS=\"~amd64\"\n"
	if err := os.WriteFile(filepath.Join(dir, filepath.Base(pkg)+"-"+version+".ebuild"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func cooldownCmdFile(t *testing.T, c *testCLI, rel string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(c.Overlay(), rel))
	if os.IsNotExist(err) {
		return ""
	}
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// cooldownCmdAtoms returns the atom lines of the block in package.mask and of
// the unmask list, each sorted.
func cooldownCmdAtoms(t *testing.T, c *testCLI) (block, unmask []string) {
	t.Helper()
	in := false
	for _, line := range strings.Split(cooldownCmdFile(t, c, "profiles/package.mask"), "\n") {
		switch {
		case strings.HasPrefix(line, "# BEGIN bentoo-cooldown"):
			in = true
		case strings.HasPrefix(line, "# END bentoo-cooldown"):
			in = false
		case in && strings.HasPrefix(line, "="):
			block = append(block, line)
		}
	}
	for _, line := range strings.Split(cooldownCmdFile(t, c, ".autoupdate/cooldown.unmask"), "\n") {
		if line = strings.TrimSpace(line); line != "" && !strings.HasPrefix(line, "#") {
			unmask = append(unmask, line)
		}
	}
	sort.Strings(block)
	sort.Strings(unmask)
	return block, unmask
}

func expectAtoms(t *testing.T, c *testCLI, want ...string) {
	t.Helper()
	sort.Strings(want)
	block, unmask := cooldownCmdAtoms(t, c)
	if strings.Join(block, " ") != strings.Join(want, " ") {
		t.Errorf("block atoms = %v, want %v", block, want)
	}
	if strings.Join(unmask, " ") != strings.Join(want, " ") {
		t.Errorf("cooldown.unmask atoms = %v, want %v (R3.9: both files)", unmask, want)
	}
}

func snapshotCooldownFiles(t *testing.T, c *testCLI) [2]string {
	t.Helper()
	return [2]string{cooldownCmdFile(t, c, "profiles/package.mask"), cooldownCmdFile(t, c, ".autoupdate/cooldown.unmask")}
}

func expectUnchanged(t *testing.T, c *testCLI, before [2]string, why string) {
	t.Helper()
	if after := snapshotCooldownFiles(t, c); after != before {
		t.Errorf("%s, yet the cooldown files changed:\npackage.mask:\n%s\ncooldown.unmask:\n%s", why, after[0], after[1])
	}
}

var cooldownCmdRecent = time.Now().UTC().Add(-time.Hour).Truncate(time.Second)

var cooldownLongAgo = time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)

// cooldownApplyCLI is a testCLI whose Applier runs no real child process.
func cooldownApplyCLI(t *testing.T) *testCLI {
	t.Helper()
	return newTestCLI(t, withDeps(func(d *deps) {
		d.newApplier = func(overlayPath, configDir string, opts ...autoupdate.ApplierOption) (*autoupdate.Applier, error) {
			opts = append(opts,
				autoupdate.WithExecCommand(func(ctx context.Context, _ string, _ ...string) *exec.Cmd {
					return exec.CommandContext(ctx, "true")
				}),
				autoupdate.WithApplierIsolationProbe(func() (bool, string) { return true, "" }),
				autoupdate.WithApplierSetVersionsFunc(func(string, map[string]string) error { return nil }),
			)
			opts = append(opts, cleanBuildOptions()...)
			a, err := autoupdate.NewApplier(overlayPath, configDir, opts...)
			if err != nil {
				return nil, err
			}
			// The check that proves each pending bump, so the apply has a
			// proof to publish.
			for _, u := range a.Pending().List() {
				a.Validate(context.Background(), u.Package, validate.DepthNone)
			}
			return a, nil
		}
	}))
}

// seedCooldownPending creates the pending list, holding the given bumps.
func seedCooldownPending(t *testing.T, c *testCLI, bumps ...autoupdate.PendingUpdate) {
	t.Helper()
	dir := filepath.Join(c.Home(), ".config", "bentoo", "autoupdate")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	pending, err := autoupdate.NewPendingList(dir)
	if err != nil {
		t.Fatalf("NewPendingList(%s): %v", dir, err)
	}
	for _, b := range bumps {
		if err := pending.Add(b); err != nil {
			t.Fatalf("pending.Add(%s): %v", b.Package, err)
		}
	}
}

// plantDueEntryAndBump is the ORDER fixture: dev-test/cda-foo-1.0.0 masked
// since 2020 (due), its ebuild present, and a pending bump to 1.0.1.
func plantDueEntryAndBump(t *testing.T, c *testCLI) {
	t.Helper()
	plantCooldown(t, c, cooldownCmdEntry{"dev-test/cda-foo", "1.0.0", cooldownLongAgo, "autoupdate", ""})
	seedCooldownPending(t, c, autoupdate.PendingUpdate{
		Package: "dev-test/cda-foo", CurrentVersion: "1.0.0", NewVersion: "1.0.1", Status: autoupdate.StatusPending,
	})
}

// TestCooldownApplyStart_SweepRunsBeforeTheBumpIsMasked: R3.1 (order) +
// R3.2 at the apply entry point.
func TestCooldownApplyStart_SweepRunsBeforeTheBumpIsMasked(t *testing.T) {
	c := cooldownApplyCLI(t)
	plantDueEntryAndBump(t, c)

	stdout, stderr, code := c.Run("overlay", "autoupdate", "--apply", "all")
	if code != 0 {
		t.Fatalf("--apply all exited %d\nstdout:\n%s\nstderr:\n%s", code, stdout, stderr)
	}
	if _, err := os.Stat(filepath.Join(c.Overlay(), "dev-test", "cda-foo", "cda-foo-1.0.1.ebuild")); err != nil {
		t.Fatalf("precondition: the bump to 1.0.1 was not promoted: %v\nstdout:\n%s\nstderr:\n%s", err, stdout, stderr)
	}
	// 1.0.0 lifted at the start; 1.0.1 added after it, with a fresh clock.
	expectAtoms(t, c, "=dev-test/cda-foo-1.0.1")
}

// TestCooldownApplyStart_LiftsWhatIsDueEvenWithNothingPending: R3.1 — the
// run starting is the trigger, not the presence of a candidate.
func TestCooldownApplyStart_LiftsWhatIsDueEvenWithNothingPending(t *testing.T) {
	c := cooldownApplyCLI(t)
	plantCooldown(t, c,
		cooldownCmdEntry{"dev-test/cdq-old", "1.0", cooldownLongAgo, "autoupdate", ""},
		cooldownCmdEntry{"dev-test/cdq-recent", "1.0", time.Now().UTC().Add(-time.Hour).Truncate(time.Second), "autoupdate", ""},
	)
	seedCooldownPending(t, c)

	if stdout, stderr, code := c.Run("overlay", "autoupdate", "--apply", "all"); code != 0 {
		t.Fatalf("--apply all exited %d\nstdout:\n%s\nstderr:\n%s", code, stdout, stderr)
	}
	expectAtoms(t, c, "=dev-test/cdq-recent-1.0")
}

// TestCooldownSummary_ListsTheRunsAdditionsAndLifts: R8.2. After the
// "Cooldown" heading of the run summary, the added atom and the lifted atom
// are both named.
func TestCooldownSummary_ListsTheRunsAdditionsAndLifts(t *testing.T) {
	c := cooldownApplyCLI(t)
	plantDueEntryAndBump(t, c)

	stdout, stderr, code := c.Run("overlay", "autoupdate", "--apply", "all")
	if code != 0 {
		t.Fatalf("--apply all exited %d\nstderr:\n%s", code, stderr)
	}
	loc := regexp.MustCompile(`(?im)^\W*cooldown\b`).FindStringIndex(stdout)
	if loc == nil {
		t.Fatalf("the run summary has no Cooldown section:\n%s", stdout)
	}
	section := stdout[loc[0]:]
	for _, atom := range []string{"dev-test/cda-foo-1.0.1", "dev-test/cda-foo-1.0.0"} {
		if !strings.Contains(section, atom) {
			t.Errorf("the Cooldown section does not name %s:\n%s", atom, section)
		}
	}
}

// TestCooldownLog_OnePerApplyStartLift: R8.1 / Q13 for lifts.
// Exactly one log line about the lifted atom carries its package, version
// and reason `elapsed`.
func TestCooldownLog_OnePerApplyStartLift(t *testing.T) {
	c := cooldownApplyCLI(t)
	plantCooldown(t, c, cooldownCmdEntry{"dev-test/cdlog-old", "1.0", cooldownLongAgo, "autoupdate", ""})
	seedCooldownPending(t, c)

	if _, stderr, code := c.Run("overlay", "autoupdate", "--apply", "all"); code != 0 {
		t.Fatalf("--apply all exited %d\nstderr:\n%s", code, stderr)
	}
	logFile := filepath.Join(c.Home(), ".local", "state", "bentoo", "logs", "bentoo.log")
	data, err := os.ReadFile(logFile)
	if err != nil {
		t.Fatalf("reading the invocation log %s: %v", logFile, err)
	}
	pkg := regexp.MustCompile(`package["=:\s]+"?dev-test/cdlog-old"?`)
	ver := regexp.MustCompile(`version["=:\s]+"?1\.0"?\b`)
	reason := regexp.MustCompile(`reason["=:\s]+"?elapsed\b`)
	n := 0
	for _, line := range strings.Split(string(data), "\n") {
		if pkg.MatchString(line) && ver.MatchString(line) && reason.MatchString(line) {
			n++
		}
	}
	if n != 1 {
		t.Errorf("%d log records with package=dev-test/cdlog-old version=1.0 reason=elapsed, want exactly 1:\n%s", n, data)
	}
}

// --urgent names promotions that skip the cooldown; outside --apply and
// --revive it would silently mean nothing, so it is refused.
func TestCooldownUrgent_RefusedOutsideApplyAndRevive(t *testing.T) {
	c := newTestCLI(t)
	_, stderr, code := c.Run("overlay", "autoupdate", "--check", "--urgent", "dev-libs/foo")
	if code == 0 || !strings.Contains(stderr, "--urgent only applies to --apply or --revive") {
		t.Errorf("--check --urgent exited %d, stderr %q; want a refusal naming --apply and --revive", code, stderr)
	}
}
