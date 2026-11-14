package main

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/obentoo/bentoolkit/internal/common/filelock"
)

// TestBuildBackend_AnUnavailableOrUnknownBackendStopsBeforeAnyWork: vm is
// named but not built yet, and a typo is not a backend; neither may fall back
// to the host in silence.
func TestBuildBackend_AnUnavailableOrUnknownBackendStopsBeforeAnyWork(t *testing.T) {
	for _, tc := range []struct{ name, want string }{
		{"vm", "not available yet"},
		{"kvm", "unknown build backend"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := newTestCLI(t)
			_, stderr, code := c.Run("overlay", "autoupdate", "--check", "--build-backend", tc.name)
			if code == 0 || !strings.Contains(stderr, tc.want) || !strings.Contains(stderr, "--build-backend") {
				t.Errorf("--build-backend %s exited %d; stderr does not say %q from --build-backend:\n%s", tc.name, code, tc.want, stderr)
			}
		})
	}
}

// TestBuildBackend_TheConfigKeyIsReadAndTheFlagWins: autoupdate.build.backend
// selects the backend, and --build-backend overrides it for one run.
func TestBuildBackend_TheConfigKeyIsReadAndTheFlagWins(t *testing.T) {
	c := newTestCLI(t)
	path := filepath.Join(c.Home(), ".config", "bentoo", "config.yaml")
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString("autoupdate:\n  build:\n    backend: vm\n"); err != nil {
		t.Fatal(err)
	}
	_ = f.Close()

	if _, stderr, code := c.Run("overlay", "autoupdate", "--check"); code == 0 || !strings.Contains(stderr, "autoupdate.build.backend") {
		t.Errorf("the config key was not read: exit %d\n%s", code, stderr)
	}
	if _, stderr, _ := c.Run("overlay", "autoupdate", "--check", "--build-backend", "host"); strings.Contains(stderr, "build backend") {
		t.Errorf("--build-backend host did not override the config key:\n%s", stderr)
	}
}

// TestBuildSession_ASecondSessionIsRefusedAtOnce: while a session holds the
// build environment, a run that may build exits non-zero at once and names
// the holder's PID and start time.
func TestBuildSession_ASecondSessionIsRefusedAtOnce(t *testing.T) {
	c := newTestCLI(t)
	dir := filepath.Join(c.Home(), ".config", "bentoo", "autoupdate")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	held, err := filelock.TryAcquire(filepath.Join(dir, buildSessionLockName), "test session")
	if err != nil {
		t.Fatal(err)
	}
	defer held.Release()

	_, stderr, code := c.Run("overlay", "autoupdate", "--check", "--llm", "--yes")

	if code == 0 {
		t.Fatalf("a second build session exited 0:\n%s", stderr)
	}
	for _, want := range []string{"build environment", "pid " + strconv.Itoa(os.Getpid()), "running since"} {
		if !strings.Contains(stderr, want) {
			t.Errorf("the refusal does not name %q:\n%s", want, stderr)
		}
	}
}

// TestBuildSession_AFreshInstallHasNoStateDirectoryYet: the first run that
// may build creates the state directory the lock lives in.
func TestBuildSession_AFreshInstallHasNoStateDirectoryYet(t *testing.T) {
	configDir := filepath.Join(t.TempDir(), "never-created", "autoupdate")
	ar := testAutoupdateRun(testAutoupdateOptions())
	ar.opts.revive = "all"

	release, err := ar.takeBuildSession(configDir)
	if err != nil {
		t.Fatalf("takeBuildSession on a missing state directory: %v", err)
	}
	release()
}

// TestBuildBackend_TheContainerNeedsAnImageOnlyWhenItBuilds: a mode that
// builds stops before any work when no image is configured; one that does not
// build does not need one.
func TestBuildBackend_TheContainerNeedsAnImageOnlyWhenItBuilds(t *testing.T) {
	c := newTestCLI(t)
	_, stderr, code := c.Run("overlay", "autoupdate", "--check", "--llm", "--yes", "--build-backend", "container")
	if code == 0 || !strings.Contains(stderr, "no build image is configured") {
		t.Errorf("a building run with no image exited %d:\n%s", code, stderr)
	}
	if _, stderr, _ := c.Run("overlay", "autoupdate", "--check", "--build-backend", "container"); strings.Contains(stderr, "build image") {
		t.Errorf("a check that builds nothing asked for an image:\n%s", stderr)
	}
}
