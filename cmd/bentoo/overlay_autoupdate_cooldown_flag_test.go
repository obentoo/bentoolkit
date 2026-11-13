package main

// --cooldown switches the overlay's cooldown on from the apply itself. The
// block it writes lives in the overlay's own profiles/package.mask, so the
// mask reaches every host that syncs the overlay, and later runs keep masking
// without the flag.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/obentoo/bentoolkit/internal/autoupdate"
)

// plantUncooledBump is an overlay with hand masks and NO cooldown block,
// dev-test/cdf-foo-1.0.0 in the tree and a pending bump to 1.0.1.
func plantUncooledBump(t *testing.T, c *testCLI) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(c.Overlay(), "profiles"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(c.Overlay(), "profiles", "package.mask"), []byte(cooldownCmdHandMasks), 0o644); err != nil {
		t.Fatal(err)
	}
	cooldownCmdEbuild(t, c, "dev-test/cdf-foo", "1.0.0")
	seedCooldownPending(t, c, autoupdate.PendingUpdate{
		Package: "dev-test/cdf-foo", CurrentVersion: "1.0.0", NewVersion: "1.0.1", Status: autoupdate.StatusPending,
	})
}

func cdfEbuildExists(c *testCLI, version string) bool {
	_, err := os.Stat(filepath.Join(c.Overlay(), "dev-test", "cdf-foo", "cdf-foo-"+version+".ebuild"))
	return err == nil
}

func TestCooldownFlag_EnablesTheBlockAndMasksThisRunsBump(t *testing.T) {
	c := cooldownApplyCLI(t)
	plantUncooledBump(t, c)

	stdout, stderr, code := c.Run("overlay", "autoupdate", "--apply", "all", "--cooldown", "--clean")
	if code != 0 {
		t.Fatalf("--apply all --cooldown exited %d\nstdout:\n%s\nstderr:\n%s", code, stdout, stderr)
	}
	if !cdfEbuildExists(c, "1.0.1") {
		t.Fatalf("precondition: the bump to 1.0.1 was not promoted\nstdout:\n%s\nstderr:\n%s", stdout, stderr)
	}
	mask := cooldownCmdFile(t, c, "profiles/package.mask")
	if !strings.HasPrefix(mask, cooldownCmdHandMasks) {
		t.Errorf("the hand masks above the block were not kept byte for byte:\n%s", mask)
	}
	if !strings.Contains(mask, "# BEGIN bentoo-cooldown format=1 days=7\n") {
		t.Errorf("bare --cooldown did not write a days=7 block:\n%s", mask)
	}
	expectAtoms(t, c, "=dev-test/cdf-foo-1.0.1")
	if !cdfEbuildExists(c, "1.0.0") {
		t.Errorf("1.0.0 was removed while 1.0.1 is masked: nothing installable is left")
	}
	if !strings.Contains(stdout, "Cooldown enabled") {
		t.Errorf("stdout does not announce the cooldown was enabled:\n%s", stdout)
	}
}

// Without the flag an overlay with no block behaves as before: the bump is
// promoted unmasked and package.mask is not touched.
func TestCooldownFlag_AbsentLeavesAnUncooledOverlayAlone(t *testing.T) {
	c := cooldownApplyCLI(t)
	plantUncooledBump(t, c)

	if stdout, stderr, code := c.Run("overlay", "autoupdate", "--apply", "all"); code != 0 {
		t.Fatalf("--apply all exited %d\nstdout:\n%s\nstderr:\n%s", code, stdout, stderr)
	}
	if got := cooldownCmdFile(t, c, "profiles/package.mask"); got != cooldownCmdHandMasks {
		t.Errorf("package.mask changed without --cooldown:\n%s", got)
	}
}

// On an overlay already under cooldown, --cooldown=N rewrites days= and
// keeps every entry.
func TestCooldownFlag_RetunesAnExistingBlock(t *testing.T) {
	c := cooldownApplyCLI(t)
	plantCooldown(t, c, cooldownCmdEntry{"dev-test/cdr-keep", "1.0", cooldownCmdRecent, "autoupdate", ""})
	seedCooldownPending(t, c)

	stdout, stderr, code := c.Run("overlay", "autoupdate", "--apply", "all", "--cooldown=14")
	if code != 0 {
		t.Fatalf("--apply all --cooldown=14 exited %d\nstdout:\n%s\nstderr:\n%s", code, stdout, stderr)
	}
	if mask := cooldownCmdFile(t, c, "profiles/package.mask"); !strings.Contains(mask, "format=1 days=14\n") {
		t.Errorf("days= was not rewritten to 14:\n%s", mask)
	}
	expectAtoms(t, c, "=dev-test/cdr-keep-1.0")
}

func TestCooldownFlag_Refusals(t *testing.T) {
	for _, tc := range []struct {
		name string
		args []string
		want string
	}{
		{"outside apply and revive", []string{"--check", "--cooldown"}, "--cooldown only applies to --apply or --revive"},
		{"zero days", []string{"--apply", "all", "--cooldown=0"}, "the delay must be 1 to 3650 days"},
		{"too many days", []string{"--apply", "all", "--cooldown=3651"}, "the delay must be 1 to 3650 days"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := newTestCLI(t)
			_, stderr, code := c.Run(append([]string{"overlay", "autoupdate"}, tc.args...)...)
			if code == 0 || !strings.Contains(stderr, tc.want) {
				t.Errorf("%v exited %d, stderr %q; want a refusal containing %q", tc.args, code, stderr, tc.want)
			}
			if got := cooldownCmdFile(t, c, "profiles/package.mask"); strings.Contains(got, "bentoo-cooldown") {
				t.Errorf("a refused run wrote the cooldown block:\n%s", got)
			}
		})
	}
}
