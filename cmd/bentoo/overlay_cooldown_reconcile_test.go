package main

// `cooldown sweep` (and the apply-start sweep, the same function) applies the
// registry as it stands now to the entries already masked: a record moved to
// cooldown_days = 0 is lifted (reason registry), a changed cooldown_days is
// written on the entry. Before 2026-11-25 both only reached the next
// candidate. Hostile half: an unreadable registry must not stop the sweep —
// the time rules still run and nothing is reconciled.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func plantReconcileOverlay(t *testing.T, c *testCLI, registryText string) {
	t.Helper()
	plantCooldown(t, c,
		cooldownCmdEntry{"dev-test/rec-off", "1.0", cooldownCmdRecent, "autoupdate", ""},
		cooldownCmdEntry{"dev-test/rec-short", "1.0", cooldownCmdRecent, "autoupdate", ""},
	)
	// plantCooldown writes no line=; the registry owns only entries that name
	// their line, so add it the way an apply would have written it.
	mask := filepath.Join(c.Overlay(), "profiles", "package.mask")
	data, err := os.ReadFile(mask)
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	for _, pkg := range []string{"dev-test/rec-off", "dev-test/rec-short"} {
		text = strings.Replace(text, " origin=autoupdate\n="+pkg+"-", " origin=autoupdate line="+pkg+"\n="+pkg+"-", 1)
	}
	if err := os.WriteFile(mask, []byte(text), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(c.Overlay(), ".autoupdate", "packages.toml"), []byte(registryText), 0o644); err != nil {
		t.Fatal(err)
	}
}

const reconcileRegistry = `["dev-test/rec-off"]
url = "https://example.org/rec-off/releases"
parser = "regex"
pattern = 'v([0-9.]+)'
cooldown_days = 0
cooldown_reason = "our own signed release"

["dev-test/rec-short"]
url = "https://example.org/rec-short/releases"
parser = "regex"
pattern = 'v([0-9.]+)'
cooldown_days = 2
cooldown_reason = "security releases"
`

func TestCooldownSweepCmd_ReconcilesTheMaskedEntriesWithTheRegistry(t *testing.T) {
	c := newTestCLI(t)
	plantReconcileOverlay(t, c, reconcileRegistry)

	stdout, stderr, code := c.Run("overlay", "cooldown", "sweep")
	if code != 0 {
		t.Fatalf("sweep exited %d\nstderr: %s", code, stderr)
	}
	if !strings.Contains(stdout, "dev-test/rec-off-1.0") || !strings.Contains(stdout, "registry") {
		t.Errorf("sweep does not report =dev-test/rec-off-1.0 lifted by the registry:\n%s", stdout)
	}
	expectAtoms(t, c, "=dev-test/rec-short-1.0")
	data, err := os.ReadFile(filepath.Join(c.Overlay(), "profiles", "package.mask"))
	if err != nil {
		t.Fatal(err)
	}
	if !reconcileEntryHasDays2(string(data)) {
		t.Errorf("the rec-short entry does not carry days=2:\n%s", data)
	}
}

func TestCooldownSweepCmd_AnUnreadableRegistryStillSweepsByTime(t *testing.T) {
	c := newTestCLI(t)
	plantReconcileOverlay(t, c, "this is [not toml\n")
	old := time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)
	cooldownCmdEbuild(t, c, "dev-test/rec-old", "1.0")
	mask := filepath.Join(c.Overlay(), "profiles", "package.mask")
	data, err := os.ReadFile(mask)
	if err != nil {
		t.Fatal(err)
	}
	text := strings.Replace(string(data), "# END bentoo-cooldown",
		"# cooldown since="+old.Format(time.RFC3339)+" origin=autoupdate\n=dev-test/rec-old-1.0\n# END bentoo-cooldown", 1)
	if err := os.WriteFile(mask, []byte(text), 0o644); err != nil {
		t.Fatal(err)
	}

	if _, stderr, code := c.Run("overlay", "cooldown", "sweep"); code != 0 {
		t.Fatalf("sweep exited %d with an unreadable registry; the time rules must still run\nstderr: %s", code, stderr)
	}
	expectAtoms(t, c, "=dev-test/rec-off-1.0", "=dev-test/rec-short-1.0")
}

// reconcileEntryHasDays2 accepts days=2 anywhere on the rec-short comment line, whatever
// order the renderer puts its attributes in.
func reconcileEntryHasDays2(text string) bool {
	for l := range strings.SplitSeq(text, "\n") {
		if strings.Contains(l, "line=dev-test/rec-short") && strings.Contains(l, "days=2") {
			return true
		}
	}
	return false
}
