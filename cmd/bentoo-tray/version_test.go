package main

// bentoo-tray reports its own version: --version names the tray, then the
// bentoolkit release it was built from, and the startup log line carries both
// under separate attributes. In a test binary the release is the unstamped
// "dev" and the tray's version comes from its embedded file, so the two values
// differ and a swap between them shows.

import (
	"strings"
	"testing"
	"time"

	release "github.com/obentoo/bentoolkit/internal/common/version"
	trayversion "github.com/obentoo/bentoolkit/internal/tray/version"
)

// requireDistinctVersions fails when the fixture cannot tell the tray's
// version from the release's, which would let a swap pass unseen.
func requireDistinctVersions(t *testing.T) {
	t.Helper()
	if trayversion.Version() == release.Version {
		t.Fatalf("fixture: the tray version and the bentoolkit release are both %q; a swap would pass unseen", release.Version)
	}
}

func TestRun_VersionNamesTheTrayAndTheRelease(t *testing.T) {
	requireDistinctVersions(t)
	c := newChild(t, "unix:path=/nonexistent/bentoo-tray-bus", "unix:path=/nonexistent/bentoo-tray-bus", nil, "--version")
	if code := c.run(t, 10*time.Second); code != 0 {
		t.Fatalf("--version exited %d, want 0; stderr:\n%s", code, c.stderr)
	}
	out := c.stdout.String()
	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	if len(lines) < 2 {
		t.Fatalf("--version printed %d line(s), want at least 2:\n%s", len(lines), out)
	}
	if want := "bentoo-tray version " + trayversion.Version(); lines[0] != want {
		t.Errorf("first line = %q, want %q", lines[0], want)
	}
	if want := "  bentoolkit: " + release.Version; lines[1] != want {
		t.Errorf("second line = %q, want %q", lines[1], want)
	}
	if want := trayversion.Info() + "\n"; out != want {
		t.Errorf("stdout =\n%s\nwant the tray's Info():\n%s", out, want)
	}
	if strings.Contains(out, "bentoo version") {
		t.Errorf("--version names the program `bentoo version`:\n%s", out)
	}
}

func TestRun_StartupLogCarriesTheTrayVersionAndTheRelease(t *testing.T) {
	requireDistinctVersions(t)
	code, stderr := alreadyRunning(t, nil)
	if code != 0 {
		t.Fatalf("exit code %d, want 0; stderr:\n%s", code, stderr)
	}
	var starting []map[string]string
	for _, l := range logLines(t, stderr) {
		if l["msg"] == "bentoo-tray starting" {
			starting = append(starting, l)
		}
	}
	if len(starting) != 1 {
		t.Fatalf("%d `bentoo-tray starting` lines, want 1:\n%s", len(starting), stderr)
	}
	l := starting[0]
	if got, want := l["version"], trayversion.Version(); got != want {
		t.Errorf("startup line version=%q, want the tray version %q:\n%s", got, want, stderr)
	}
	if got, ok := l["bentoolkit"]; !ok || got != release.Version {
		t.Errorf("startup line bentoolkit=%q (present: %v), want the bentoolkit release %q:\n%s", got, ok, release.Version, stderr)
	}
}
