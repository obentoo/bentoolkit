package main

// Integration through the real CLI for `overlay cooldown set` and `unset`:
// the registry is planted in <overlay>/.autoupdate/packages.toml and read back
// after each run. The record-level rules (placement, groups, key resolution,
// rollback) are pinned in internal/autoupdate/registry/cooldown_write_test.go;
// these pin the command: flags, exit codes, output, the lock, --dry-run and
// --sweep.

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/obentoo/bentoolkit/internal/common/filelock"
)

const cooldownSetRegistry = `["dev-test/cds-foo"]
url = "https://example.invalid/foo.json"
parser = "json"
path = "version"
comments = """
foo
"""
# END

["dev-test/cds-line@stable"]
url = "https://example.invalid/line.json"
parser = "json"
path = "stable"
series = "^1\\."
comments = """
stable
"""
# END

["dev-test/cds-line@preview"]
url = "https://example.invalid/line.json"
parser = "json"
path = "preview"
series = "^2\\."
comments = """
preview
"""
# END

["dev-test/cds-grp-a"]
url = "https://example.invalid/a.json"
parser = "json"
path = "version"
cooldown_with = ["dev-test/cds-grp-b"]
cooldown_days = 2
cooldown_reason = "released together"
comments = """
a
"""
# END

["dev-test/cds-grp-b"]
url = "https://example.invalid/b.json"
parser = "json"
path = "version"
cooldown_with = ["dev-test/cds-grp-a"]
cooldown_days = 2
cooldown_reason = "released together"
comments = """
b
"""
# END
`

// cooldownSetCLI is a testCLI whose overlay carries cooldownSetRegistry and a
// cooldown block holding the entries given.
func cooldownSetCLI(t *testing.T, entries ...cooldownCmdEntry) *testCLI {
	t.Helper()
	c := newTestCLI(t)
	plantCooldown(t, c, entries...)
	if err := os.WriteFile(cooldownSetPath(c), []byte(cooldownSetRegistry), 0o644); err != nil {
		t.Fatal(err)
	}
	return c
}

func cooldownSetPath(c *testCLI) string {
	return filepath.Join(c.Overlay(), ".autoupdate", "packages.toml")
}

func cooldownSetRead(t *testing.T, c *testCLI) string {
	t.Helper()
	data, err := os.ReadFile(cooldownSetPath(c))
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func TestCooldownSet_WritesTheRecordAndSaysWhatChanged(t *testing.T) {
	c := cooldownSetCLI(t)
	stdout, stderr, code := c.Run("overlay", "cooldown", "set", "dev-test/cds-foo", "--days", "3", "--reason", "upstream hotfixes")
	if code != 0 {
		t.Fatalf("set exited %d\nstdout: %s\nstderr: %s", code, stdout, stderr)
	}
	got := cooldownSetRead(t, c)
	want := strings.Replace(cooldownSetRegistry, "path = \"version\"\ncomments = \"\"\"\nfoo",
		"path = \"version\"\ncooldown_days = 3\ncooldown_reason = \"upstream hotfixes\"\ncomments = \"\"\"\nfoo", 1)
	if got != want {
		t.Errorf("registry after set:\n%s\nwant:\n%s", got, want)
	}
	for _, s := range []string{"dev-test/cds-foo: cooldown_days (block's days) -> 3", "bentoo overlay diff", "bentoo overlay cooldown sweep"} {
		if !strings.Contains(stdout, s) {
			t.Errorf("stdout lacks %q:\n%s", s, stdout)
		}
	}

	// Updating it rewrites in place.
	stdout, stderr, code = c.Run("overlay", "cooldown", "set", "dev-test/cds-foo", "--days", "0", "--reason", "first-party")
	if code != 0 {
		t.Fatalf("second set exited %d\nstderr: %s", code, stderr)
	}
	if !strings.Contains(stdout, "cooldown_days 3 -> 0") {
		t.Errorf("stdout lacks the old -> new days:\n%s", stdout)
	}
	if got := cooldownSetRead(t, c); !strings.Contains(got, "cooldown_days = 0\ncooldown_reason = \"first-party\"\ncomments") || strings.Count(got, "cooldown_days") != 3 {
		t.Errorf("registry after the update:\n%s", got)
	}
}

func TestCooldownUnset_DropsBothKeysOfTheWholeGroup(t *testing.T) {
	c := cooldownSetCLI(t)
	stdout, stderr, code := c.Run("overlay", "cooldown", "unset", "dev-test/cds-grp-a")
	if code != 0 {
		t.Fatalf("unset exited %d\nstderr: %s", code, stderr)
	}
	got := cooldownSetRead(t, c)
	if strings.Contains(got, "cooldown_days") || strings.Contains(got, "cooldown_reason") {
		t.Errorf("an override survived unset:\n%s", got)
	}
	if !strings.Contains(stdout, "dev-test/cds-grp-b") || !strings.Contains(stdout, "-> (block's days)") {
		t.Errorf("stdout does not name the group member or the new days:\n%s", stdout)
	}
}

func TestCooldownSet_RefusalsExitOneAndWriteNothing(t *testing.T) {
	for name, args := range map[string][]string{
		"ambiguous key":  {"set", "dev-test/cds-line", "--days", "3", "--reason", "r"},
		"unknown key":    {"set", "dev-test/cds-nope", "--days", "3", "--reason", "r"},
		"missing reason": {"set", "dev-test/cds-foo", "--days", "3"},
		"empty reason":   {"set", "dev-test/cds-foo", "--days", "3", "--reason", " "},
		"missing days":   {"set", "dev-test/cds-foo", "--reason", "r"},
		"invalid days":   {"set", "dev-test/cds-foo", "--days", "3651", "--reason", "r"},
		"negative days":  {"set", "dev-test/cds-foo", "--days=-1", "--reason", "r"},
		"zero on group":  {"set", "dev-test/cds-grp-b", "--days", "0", "--reason", "r"},
		"sweep dry-run":  {"set", "dev-test/cds-foo", "--days", "3", "--reason", "r", "--dry-run", "--sweep"},
		"unset unknown":  {"unset", "dev-test/cds-nope"},
	} {
		t.Run(name, func(t *testing.T) {
			c := cooldownSetCLI(t)
			stdout, stderr, code := c.Run(append([]string{"overlay", "cooldown"}, args...)...)
			if code != 1 {
				t.Errorf("exited %d, want 1\nstdout: %s\nstderr: %s", code, stdout, stderr)
			}
			if got := cooldownSetRead(t, c); got != cooldownSetRegistry {
				t.Errorf("a refused edit changed the registry:\n%s", got)
			}
			if name == "ambiguous key" && (!strings.Contains(stderr, "dev-test/cds-line@preview") || !strings.Contains(stderr, "--all-lines")) {
				t.Errorf("the ambiguity error does not list the records and the flag:\n%s", stderr)
			}
		})
	}
}

func TestCooldownSet_AllLinesSetsEveryReleaseLine(t *testing.T) {
	c := cooldownSetCLI(t)
	if _, stderr, code := c.Run("overlay", "cooldown", "set", "dev-test/cds-line", "--all-lines", "--days", "5", "--reason", "r"); code != 0 {
		t.Fatalf("set --all-lines exited %d\nstderr: %s", code, stderr)
	}
	if got := cooldownSetRead(t, c); strings.Count(got, "cooldown_days = 5") != 2 {
		t.Errorf("both release lines should carry cooldown_days = 5:\n%s", got)
	}
}

func TestCooldownSet_DryRunPrintsTheDiffAndWritesNothing(t *testing.T) {
	c := cooldownSetCLI(t)
	stdout, stderr, code := c.Run("overlay", "cooldown", "set", "dev-test/cds-grp-a", "--days", "4", "--reason", "slower", "--dry-run")
	if code != 0 {
		t.Fatalf("dry run exited %d\nstderr: %s", code, stderr)
	}
	for _, s := range []string{"+++ b/packages.toml", "-cooldown_days = 2", "+cooldown_days = 4", "dev-test/cds-grp-b"} {
		if !strings.Contains(stdout, s) {
			t.Errorf("dry-run output lacks %q:\n%s", s, stdout)
		}
	}
	if got := cooldownSetRead(t, c); got != cooldownSetRegistry {
		t.Errorf("--dry-run wrote:\n%s", got)
	}
}

func TestCooldownSet_SweepRunsRightAfterTheWrite(t *testing.T) {
	c := cooldownSetCLI(t, cooldownCmdEntry{"dev-test/cds-old", "1.0", cooldownLongAgo, "manual", ""})
	stdout, stderr, code := c.Run("overlay", "cooldown", "set", "dev-test/cds-foo", "--days", "3", "--reason", "r", "--sweep")
	if code != 0 {
		t.Fatalf("set --sweep exited %d\nstderr: %s", code, stderr)
	}
	if !strings.Contains(stdout, "lifted  =dev-test/cds-old-1.0") {
		t.Errorf("the sweep did not lift the elapsed entry:\n%s", stdout)
	}
	if strings.Contains(cooldownCmdFile(t, c, "profiles/package.mask"), "=dev-test/cds-old-1.0") {
		t.Error("the swept entry is still in the block")
	}
}

func TestCooldownSet_RefusesWhileAnApplyHoldsTheLock(t *testing.T) {
	oldWait, oldPoll := filelock.Wait, filelock.Poll
	filelock.Wait, filelock.Poll = 300*time.Millisecond, 10*time.Millisecond
	t.Cleanup(func() { filelock.Wait, filelock.Poll = oldWait, oldPoll })

	c := cooldownSetCLI(t)
	lockPath := filepath.Join(c.Overlay(), ".autoupdate.bentoo-lock")
	pid := s056StartOverlayHolder(t, lockPath)
	for _, args := range [][]string{
		{"set", "dev-test/cds-foo", "--days", "3", "--reason", "r"},
		{"unset", "dev-test/cds-grp-a"},
	} {
		stdout, stderr, code := c.Run(append([]string{"overlay", "cooldown"}, args...)...)
		if code != 1 {
			t.Errorf("cooldown %s exited %d while pid %d holds the lock, want 1", args[0], code, pid)
		}
		if !strings.Contains(stdout+stderr, lockPath) && !strings.Contains(stdout+stderr, fmt.Sprint(pid)) {
			t.Errorf("cooldown %s does not name the lock or its holder:\n%s%s", args[0], stdout, stderr)
		}
	}
	if got := cooldownSetRead(t, c); got != cooldownSetRegistry {
		t.Errorf("the registry changed while the lock was held:\n%s", got)
	}
}
