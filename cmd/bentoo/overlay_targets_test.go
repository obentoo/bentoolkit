package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/obentoo/bentoolkit/internal/autoupdate/targets"
)

func writeTargetsFixture(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

// seedTargets reproduces the vim case: ::gentoo overtook the overlay's mirror
// with a version that drops python3_15.
func seedTargets(t *testing.T, c *testCLI, policy string) {
	t.Helper()
	gentoo := filepath.Join(c.Home(), "gentoo")
	t.Setenv("BENTOO_GENTOO_REPO", gentoo)
	writeTargetsFixture(t, filepath.Join(c.Overlay(), "profiles", "repo_name"), "bentoo\n")
	if policy != "" {
		writeTargetsFixture(t, filepath.Join(c.Overlay(), targets.PolicyFile), policy)
	}
	writeTargetsFixture(t, filepath.Join(c.Overlay(), "app-editors/vim/vim-9.2.1275.ebuild"), "")
	writeTargetsFixture(t, filepath.Join(c.Overlay(), "metadata/md5-cache/app-editors/vim-9.2.1275"),
		"IUSE=python_single_target_python3_15\nKEYWORDS=~amd64\n")
	writeTargetsFixture(t, filepath.Join(gentoo, "metadata/md5-cache/app-editors/vim-9.2.1302"),
		"IUSE=python_single_target_python3_14\nKEYWORDS=~amd64\n")
}

const targetsPolicy = "[required]\npython_single_target = [\"python3_15\"]\n"

func TestOverlayTargetsReportsOvertakenMirror(t *testing.T) {
	c := newTestCLI(t)
	seedTargets(t, c, targetsPolicy)
	stdout, _, code := c.Run("overlay", "targets")
	if code != 1 {
		t.Fatalf("exit = %d, want 1 for a gap", code)
	}
	for _, want := range []string{"app-editors/vim-9.2.1302::gentoo", "missing python3_15", "overtaken: the overlay holds 9.2.1275"} {
		if !strings.Contains(stdout, want) {
			t.Errorf("stdout lacks %q:\n%s", want, stdout)
		}
	}
}

func TestOverlayTargetsJSON(t *testing.T) {
	c := newTestCLI(t)
	seedTargets(t, c, targetsPolicy)
	stdout, _, code := c.Run("overlay", "targets", "--json")
	if code != 1 {
		t.Fatalf("exit = %d, want 1", code)
	}
	var findings []targets.Finding
	if err := json.Unmarshal([]byte(stdout), &findings); err != nil {
		t.Fatalf("stdout is not one JSON array: %v\n%s", err, stdout)
	}
	if len(findings) != 1 || findings[0].OverlayVersion != "9.2.1275" {
		t.Fatalf("findings = %+v", findings)
	}
}

func TestOverlayTargetsClean(t *testing.T) {
	c := newTestCLI(t)
	seedTargets(t, c, "[required]\nlua_targets = [\"lua5-4\"]\n")
	stdout, _, code := c.Run("overlay", "targets", "--json")
	if code != 0 || strings.TrimSpace(stdout) != "[]" {
		t.Fatalf("exit = %d, stdout = %q, want 0 and []", code, stdout)
	}
	stdout, _, code = c.Run("overlay", "targets")
	if code != 0 || !strings.Contains(stdout, "Every package offers") {
		t.Fatalf("exit = %d, stdout = %q", code, stdout)
	}
}

func TestOverlayTargetsWithoutPolicy(t *testing.T) {
	c := newTestCLI(t)
	seedTargets(t, c, "")
	if _, _, code := c.Run("overlay", "targets"); code != 0 {
		t.Fatalf("exit = %d, want 0 for an overlay that declares nothing", code)
	}
}

func TestOverlayTargetsBrokenPolicy(t *testing.T) {
	c := newTestCLI(t)
	seedTargets(t, c, "[required\n")
	if _, _, code := c.Run("overlay", "targets"); code != 1 {
		t.Fatalf("exit = %d, want 1 for a policy that does not parse", code)
	}
}
