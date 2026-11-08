package main

// Authored for bentoo story 003 (autoupdate cooldown), sub-tasks 4.1 and 4.2
// — R3.5, R3.6, R4.2, R6.1, R6.2, R6.3, R6.4, R6.5, R6.6, R7.1.
//
// Integration through the real CLI (testCLI: a temp HOME, a temp overlay, a
// fresh command tree per Run, the exit status returned): the block is planted
// in <overlay>/profiles/package.mask and read back from disk after each run.
//
// Contract: design.md "CLI" —
//
//	bentoo overlay cooldown status [--json]   (keys package, version, since,
//	                                           eligible, group, origin, installed)
//	bentoo overlay cooldown confirm <=atom>... (exit 1 when not in the block)
//	bentoo overlay cooldown add <=atom>        (exit 1 when the ebuild is absent)
//	bentoo overlay cooldown sweep [--dry-run]
//
// and "every command that writes takes the same lock as apply"
// (<overlay>/.autoupdate.bentoo-lock).
//
// `installed` reads the host's /var/db/pkg, which a test does not control: the
// packages here are named so no host has them, and the assertion is on the
// value's TYPE (bool, or null when the database is unreadable) only.
//
// Hostile halves: `dev-test/foo` vs `dev-test/foo-bin` at the same version, a
// confirm of a version OLDER than the queue (not in the block), a group of
// three, and — for the lock — a reading subcommand that must NOT wait on it.
//
// s056StartOverlayHolder comes from overlay_autoupdate_lock_test.go; the
// cooldown fixture helpers (plantCooldown, expectAtoms, ...) from
// overlay_autoupdate_cooldown_test.go (3.3), materialized before this file.

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/obentoo/bentoolkit/internal/autoupdate/cooldown"
	"github.com/obentoo/bentoolkit/internal/common/filelock"
)

// TestCooldownStatus_JSONIsTheTrayContract: R6.2. A JSON array, one object
// per entry, with exactly the seven keys; since/eligible are RFC 3339; a
// grouped entry's eligible time is anchored at its group's NEWEST member: every
// version keeps its own clock and a group lifts whole, so it waits for the
// last member to age.
func TestCooldownStatus_JSONIsTheTrayContract(t *testing.T) {
	c := newTestCLI(t)
	t0 := time.Date(2026, 11, 1, 6, 0, 0, 0, time.UTC)
	t1 := t0.Add(48 * time.Hour)
	t2 := t0.Add(72 * time.Hour)
	const g = "dev-test/cdj-ai-jail,dev-test/cdj-ai-jail-bin"
	plantCooldown(t, c,
		cooldownCmdEntry{"dev-test/cdj-ai-jail", "3.2.0", t1, "autoupdate", g},
		cooldownCmdEntry{"dev-test/cdj-ai-jail-bin", "3.2.0", t0, "autoupdate", g},
		cooldownCmdEntry{"dev-test/cdj-freerdp", "3.33.0-r1", t2, "manual", ""},
	)

	stdout, stderr, code := c.Run("overlay", "cooldown", "status", "--json")
	if code != 0 {
		t.Fatalf("status --json exited %d\nstderr: %s", code, stderr)
	}
	var rows []map[string]any
	if err := json.Unmarshal([]byte(stdout), &rows); err != nil {
		t.Fatalf("stdout is not a JSON array: %v\n%s", err, stdout)
	}
	if len(rows) != 3 {
		t.Fatalf("%d objects, want 3:\n%s", len(rows), stdout)
	}

	wantKeys := []string{"eligible", "group", "installed", "origin", "package", "since", "version"}
	byAtom := map[string]map[string]any{}
	for _, r := range rows {
		var keys []string
		for k := range r {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		if strings.Join(keys, ",") != strings.Join(wantKeys, ",") {
			t.Errorf("object keys = %v, want exactly %v", keys, wantKeys)
		}
		switch r["installed"].(type) {
		case bool, nil:
		default:
			t.Errorf("installed = %#v, want a bool (or null when /var/db/pkg is unreadable)", r["installed"])
		}
		byAtom[fmt.Sprint(r["package"])+"-"+fmt.Sprint(r["version"])] = r
	}

	check := func(atom string, since, eligible time.Time, origin string, members ...string) {
		t.Helper()
		r, ok := byAtom[atom]
		if !ok {
			t.Errorf("no object for %s (prefix-named packages must stay apart): %v", atom, byAtom)
			return
		}
		for key, want := range map[string]time.Time{"since": since, "eligible": eligible} {
			got, err := time.Parse(time.RFC3339, fmt.Sprint(r[key]))
			if err != nil || !got.Equal(want) {
				t.Errorf("%s %s = %v, want %s (RFC 3339)", atom, key, r[key], want.Format(time.RFC3339))
			}
		}
		if r["origin"] != origin {
			t.Errorf("%s origin = %v, want %s", atom, r["origin"], origin)
		}
		group, _ := json.Marshal(r["group"])
		for _, m := range members {
			if !strings.Contains(string(group), `"`+m+`"`) && !strings.Contains(string(group), m+",") && !strings.Contains(string(group), ","+m) {
				t.Errorf("%s group = %s, want it to name %s", atom, group, m)
			}
		}
		if len(members) == 0 && strings.Contains(string(group), "dev-test/") {
			t.Errorf("%s group = %s, want none", atom, group)
		}
	}
	week := 7 * 24 * time.Hour
	check("dev-test/cdj-ai-jail-3.2.0", t1, t1.Add(week), "autoupdate", "dev-test/cdj-ai-jail", "dev-test/cdj-ai-jail-bin")
	check("dev-test/cdj-ai-jail-bin-3.2.0", t0, t1.Add(week), "autoupdate", "dev-test/cdj-ai-jail", "dev-test/cdj-ai-jail-bin")
	check("dev-test/cdj-freerdp-3.33.0-r1", t2, t2.Add(week), "manual")
}

// TestCooldownStatus_TheTableListsEveryEntry: R6.1. Each entry's package
// and version share a line with its origin; the prefix-named pair is two rows.
func TestCooldownStatus_TheTableListsEveryEntry(t *testing.T) {
	c := newTestCLI(t)
	plantCooldown(t, c,
		cooldownCmdEntry{"dev-test/cdt-foo", "1.0", cooldownCmdRecent, "autoupdate", ""},
		cooldownCmdEntry{"dev-test/cdt-foo-bin", "1.0", cooldownCmdRecent, "manual", ""},
	)
	before := snapshotCooldownFiles(t, c)

	stdout, stderr, code := c.Run("overlay", "cooldown", "status")
	if code != 0 {
		t.Fatalf("status exited %d\nstderr: %s", code, stderr)
	}
	for _, row := range []struct{ pkg, origin string }{{"dev-test/cdt-foo", "autoupdate"}, {"dev-test/cdt-foo-bin", "manual"}} {
		re := regexp.MustCompile(regexp.QuoteMeta(row.pkg) + `[\s-]+1\.0\b.*\b` + row.origin + `\b`)
		found := false
		for _, line := range strings.Split(stdout, "\n") {
			if re.MatchString(line) && (row.pkg != "dev-test/cdt-foo" || !strings.Contains(line, "cdt-foo-bin")) {
				found = true
			}
		}
		if !found {
			t.Errorf("no table row for %s 1.0 with origin %s:\n%s", row.pkg, row.origin, stdout)
		}
	}
	expectUnchanged(t, c, before, "status is read-only")
}

// TestCooldownStatus_AMalformedBlockExitsOneAndADisabledOverlayExitsZero:
// R7.1 for the CLI — exit 1 naming package.mask and its FILE line, nothing
// rewritten — and the converse: an overlay with no block is cooldown-off,
// exit 0, nothing written.
func TestCooldownStatus_AMalformedBlockExitsOneAndADisabledOverlayExitsZero(t *testing.T) {
	c := newTestCLI(t)
	plantCooldown(t, c)
	mask := cooldownCmdHandMasks + // 3 lines
		"# BEGIN bentoo-cooldown format=1 days=7\n" + // line 4
		"not an atom\n" + // line 5
		"# END bentoo-cooldown\n"
	if err := os.WriteFile(filepath.Join(c.Overlay(), "profiles", "package.mask"), []byte(mask), 0o644); err != nil {
		t.Fatal(err)
	}
	before := snapshotCooldownFiles(t, c)

	stdout, stderr, code := c.Run("overlay", "cooldown", "status")
	if code != 1 {
		t.Errorf("status over a malformed block exited %d, want 1", code)
	}
	if !regexp.MustCompile(`package\.mask:5\b`).MatchString(stdout + stderr) {
		t.Errorf("the failure does not name package.mask:5:\nstdout: %s\nstderr: %s", stdout, stderr)
	}
	expectUnchanged(t, c, before, "the block is malformed")

	t.Run("no block", func(t *testing.T) {
		c := newTestCLI(t)
		if err := os.WriteFile(filepath.Join(c.Overlay(), "profiles", "package.mask"), []byte(cooldownCmdHandMasks), 0o644); err != nil {
			t.Fatal(err)
		}
		if _, stderr, code := c.Run("overlay", "cooldown", "status"); code != 0 {
			t.Errorf("status on an overlay without a block exited %d, want 0\nstderr: %s", code, stderr)
		}
		if got := cooldownCmdFile(t, c, "profiles/package.mask"); got != cooldownCmdHandMasks {
			t.Errorf("package.mask changed:\n%s", got)
		}
		if got := cooldownCmdFile(t, c, ".autoupdate/cooldown.unmask"); got != "" {
			t.Errorf("cooldown.unmask was written: %q", got)
		}
	})
}

// TestCooldownConfirm_LiftsThatVersionAndEveryOlderOne: R3.5 / D3 / R3.9.
// The prefix-named package at the same version and the newer queued version
// stay, in both files.
func TestCooldownConfirm_LiftsThatVersionAndEveryOlderOne(t *testing.T) {
	c := newTestCLI(t)
	plantCooldown(t, c,
		cooldownCmdEntry{"dev-test/cdc-foo", "1.0", cooldownCmdRecent, "autoupdate", ""},
		cooldownCmdEntry{"dev-test/cdc-foo", "1.1", cooldownCmdRecent, "autoupdate", ""},
		cooldownCmdEntry{"dev-test/cdc-foo", "1.2", cooldownCmdRecent, "autoupdate", ""},
		cooldownCmdEntry{"dev-test/cdc-foo-bin", "1.1", cooldownCmdRecent, "autoupdate", ""},
	)

	stdout, stderr, code := c.Run("overlay", "cooldown", "confirm", "=dev-test/cdc-foo-1.1")
	if code != 0 {
		t.Fatalf("confirm exited %d\nstderr: %s", code, stderr)
	}
	expectAtoms(t, c, "=dev-test/cdc-foo-1.2", "=dev-test/cdc-foo-bin-1.1")
	for _, lifted := range []string{"dev-test/cdc-foo-1.0", "dev-test/cdc-foo-1.1"} {
		if !strings.Contains(stdout, lifted) {
			t.Errorf("the printed lifts do not name %s:\n%s", lifted, stdout)
		}
	}
}

// TestCooldownConfirm_AGroupMemberLiftsTheWholeGroup: R4.2 through the CLI,
// with three members and a prefix-named non-member that stays.
func TestCooldownConfirm_AGroupMemberLiftsTheWholeGroup(t *testing.T) {
	c := newTestCLI(t)
	const g = "dev-test/cdg-a,dev-test/cdg-b,dev-test/cdg-c"
	plantCooldown(t, c,
		cooldownCmdEntry{"dev-test/cdg-a", "1.0", cooldownCmdRecent, "autoupdate", g},
		cooldownCmdEntry{"dev-test/cdg-a-bin", "1.0", cooldownCmdRecent, "autoupdate", ""},
		cooldownCmdEntry{"dev-test/cdg-b", "2.0", cooldownCmdRecent, "autoupdate", g},
		cooldownCmdEntry{"dev-test/cdg-c", "3.0", cooldownCmdRecent, "autoupdate", g},
	)

	if _, stderr, code := c.Run("overlay", "cooldown", "confirm", "=dev-test/cdg-b-2.0"); code != 0 {
		t.Fatalf("confirm exited %d\nstderr: %s", code, stderr)
	}
	expectAtoms(t, c, "=dev-test/cdg-a-bin-1.0")
}

// TestCooldownConfirm_AnAtomNotInTheBlockExitsOneNamingIt: R3.6. Hostile:
// the block holds the prefix-named package at that version and a NEWER
// version of the package itself — neither makes the atom "in the block".
func TestCooldownConfirm_AnAtomNotInTheBlockExitsOneNamingIt(t *testing.T) {
	c := newTestCLI(t)
	plantCooldown(t, c,
		cooldownCmdEntry{"dev-test/cdn-foo", "1.2", cooldownCmdRecent, "autoupdate", ""},
		cooldownCmdEntry{"dev-test/cdn-foo-bin", "1.1", cooldownCmdRecent, "autoupdate", ""},
	)
	before := snapshotCooldownFiles(t, c)

	stdout, stderr, code := c.Run("overlay", "cooldown", "confirm", "=dev-test/cdn-foo-1.1")
	if code != 1 {
		t.Errorf("confirm of an atom not in the block exited %d, want 1", code)
	}
	if !strings.Contains(stdout+stderr, "dev-test/cdn-foo-1.1") {
		t.Errorf("the failure does not name the atom:\nstdout: %s\nstderr: %s", stdout, stderr)
	}
	expectUnchanged(t, c, before, "the atom is not in the block")
}

// TestCooldownAdd_MasksAnEbuildPresentInTheOverlay: R6.3. The atom of a
// prefix-named package parses into its own Package; the entry carries origin
// manual and lands in both files.
func TestCooldownAdd_MasksAnEbuildPresentInTheOverlay(t *testing.T) {
	c := newTestCLI(t)
	plantCooldown(t, c)
	cooldownCmdEbuild(t, c, "dev-test/cda-foo-bin", "1.0")

	if _, stderr, code := c.Run("overlay", "cooldown", "add", "=dev-test/cda-foo-bin-1.0"); code != 0 {
		t.Fatalf("add exited %d\nstderr: %s", code, stderr)
	}
	b, err := cooldown.Load(c.Overlay())
	if err != nil || b == nil {
		t.Fatalf("cooldown.Load after add = (%v, %v)", b, err)
	}
	var found bool
	for _, e := range b.Entries {
		if e.Package == "dev-test/cda-foo-bin" && e.Version == "1.0" {
			found = true
			if e.Origin != "manual" {
				t.Errorf("Origin = %q, want manual", e.Origin)
			}
			if time.Since(e.Since) > time.Hour || time.Until(e.Since) > time.Minute {
				t.Errorf("Since = %s, want about now", e.Since)
			}
		}
	}
	if !found {
		t.Fatalf("no entry Package=dev-test/cda-foo-bin Version=1.0: %+v", b.Entries)
	}
	expectAtoms(t, c, "=dev-test/cda-foo-bin-1.0")
}

// TestCooldownAdd_AMissingEbuildOrABadAtomExitsOneAndWritesNothing: R6.4 and
// R7.4. Hostile: the prefix-named package's ebuild exists at that version,
// which does not make the asked-for one exist.
func TestCooldownAdd_AMissingEbuildOrABadAtomExitsOneAndWritesNothing(t *testing.T) {
	for _, atom := range []string{"=dev-test/cdm-foo-1.0", "=dev-test/cdm-foo-1.0;rm"} {
		t.Run(atom, func(t *testing.T) {
			c := newTestCLI(t)
			plantCooldown(t, c)
			cooldownCmdEbuild(t, c, "dev-test/cdm-foo", "0.9")
			cooldownCmdEbuild(t, c, "dev-test/cdm-foo-bin", "1.0")
			before := snapshotCooldownFiles(t, c)

			_, _, code := c.Run("overlay", "cooldown", "add", atom)
			if code != 1 {
				t.Errorf("add %s exited %d, want 1", atom, code)
			}
			expectUnchanged(t, c, before, "add was refused")
		})
	}
}

// TestCooldownSweepCmd_DryRunPrintsTheLiftsAndChangesNothing: R6.5, then the
// converse — the same sweep without --dry-run performs exactly those lifts.
func TestCooldownSweepCmd_DryRunPrintsTheLiftsAndChangesNothing(t *testing.T) {
	c := newTestCLI(t)
	old := time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)
	plantCooldown(t, c,
		cooldownCmdEntry{"dev-test/cds-old", "1.0", old, "autoupdate", ""},
		cooldownCmdEntry{"dev-test/cds-gone", "1.0", cooldownCmdRecent, "autoupdate", ""},
		cooldownCmdEntry{"dev-test/cds-recent", "1.0", cooldownCmdRecent, "autoupdate", ""},
	)
	if err := os.Remove(filepath.Join(c.Overlay(), "dev-test", "cds-gone", "cds-gone-1.0.ebuild")); err != nil {
		t.Fatal(err)
	}
	before := snapshotCooldownFiles(t, c)

	stdout, stderr, code := c.Run("overlay", "cooldown", "sweep", "--dry-run")
	if code != 0 {
		t.Fatalf("sweep --dry-run exited %d\nstderr: %s", code, stderr)
	}
	for _, want := range []struct{ atom, reason string }{{"dev-test/cds-old-1.0", "elapsed"}, {"dev-test/cds-gone-1.0", "stale"}} {
		ok := false
		for _, line := range strings.Split(stdout, "\n") {
			if strings.Contains(line, want.atom) && strings.Contains(line, want.reason) {
				ok = true
			}
		}
		if !ok {
			t.Errorf("--dry-run does not print %s with reason %s:\n%s", want.atom, want.reason, stdout)
		}
	}
	if strings.Contains(stdout, "cds-recent") {
		t.Errorf("--dry-run would lift the recent entry:\n%s", stdout)
	}
	expectUnchanged(t, c, before, "--dry-run")

	if _, stderr, code := c.Run("overlay", "cooldown", "sweep"); code != 0 {
		t.Fatalf("sweep exited %d\nstderr: %s", code, stderr)
	}
	expectAtoms(t, c, "=dev-test/cds-recent-1.0")
}

// TestCooldownLock_WritingSubcommandsRefuseWhileAnApplyHoldsTheLock: R6.6.
// While another process holds the overlay lock, confirm, add and sweep exit
// non-zero (after the shortened wait) and write nothing. The converse:
// status, which writes nothing, answers at once — a tray polling it must not
// stall behind an hour-long apply.
func TestCooldownLock_WritingSubcommandsRefuseWhileAnApplyHoldsTheLock(t *testing.T) {
	oldWait, oldPoll := filelock.Wait, filelock.Poll
	filelock.Wait, filelock.Poll = 300*time.Millisecond, 10*time.Millisecond
	t.Cleanup(func() { filelock.Wait, filelock.Poll = oldWait, oldPoll })

	c := newTestCLI(t)
	plantCooldown(t, c,
		cooldownCmdEntry{"dev-test/cdl-old", "1.0", time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC), "autoupdate", ""},
		cooldownCmdEntry{"dev-test/cdl-foo", "1.0", cooldownCmdRecent, "autoupdate", ""},
	)
	cooldownCmdEbuild(t, c, "dev-test/cdl-new", "1.0")
	lockPath := filepath.Join(c.Overlay(), ".autoupdate.bentoo-lock")
	pid := s056StartOverlayHolder(t, lockPath)
	before := snapshotCooldownFiles(t, c)

	for _, args := range [][]string{
		{"confirm", "=dev-test/cdl-foo-1.0"},
		{"add", "=dev-test/cdl-new-1.0"},
		{"sweep"},
	} {
		stdout, stderr, code := c.Run(append([]string{"overlay", "cooldown"}, args...)...)
		if code == 0 {
			t.Errorf("cooldown %s exited 0 while pid %d holds %s", args[0], pid, lockPath)
		}
		if !strings.Contains(stdout+stderr, lockPath) && !strings.Contains(stdout+stderr, fmt.Sprint(pid)) {
			t.Errorf("cooldown %s does not name the lock or its holder:\n%s%s", args[0], stdout, stderr)
		}
	}
	expectUnchanged(t, c, before, "the overlay lock was held")

	start := time.Now()
	if _, stderr, code := c.Run("overlay", "cooldown", "status"); code != 0 {
		t.Errorf("status exited %d while the lock was held; it writes nothing and must not wait on it\nstderr: %s", code, stderr)
	}
	if d := time.Since(start); d >= filelock.Wait {
		t.Errorf("status took %s, at least the lock wait: it waited on the apply lock", d)
	}
}
