package snapshot

import (
	"errors"
	"os/exec"
	"strings"
	"testing"
)

// stubLookPath replaces the lookPath seam for the test's duration. present lists
// the binaries that resolve; everything else returns exec.ErrNotFound.
func stubLookPath(t *testing.T, present ...string) {
	t.Helper()
	orig := lookPath
	t.Cleanup(func() { lookPath = orig })
	set := make(map[string]bool, len(present))
	for _, p := range present {
		set[p] = true
	}
	lookPath = func(name string) (string, error) {
		if set[name] {
			return "/usr/bin/" + name, nil
		}
		return "", exec.ErrNotFound
	}
}

func TestDetectDriver_MissingBinaryNamesPackage(t *testing.T) {
	stubLookPath(t) // nothing present

	err := detectDriver("engine", "btrbk")
	if !errors.Is(err, ErrDriverUnavailable) {
		t.Fatalf("err = %v, want ErrDriverUnavailable", err)
	}
	if !strings.Contains(err.Error(), "app-backup/btrbk") {
		t.Errorf("error %q does not name the Portage package", err)
	}
}

func TestDetectDriver_PresentBinary(t *testing.T) {
	stubLookPath(t, "btrbk", "ssh", "systemctl", "restic", "rclone")

	for _, c := range []struct{ kind, name string }{
		{"engine", "btrbk"},
		{"ship", "ssh"},
		{"ship", "restic"},
		{"ship", "archive"},
		{"schedule", "systemd"},
	} {
		if err := detectDriver(c.kind, c.name); err != nil {
			t.Errorf("detectDriver(%s,%s) = %v, want nil", c.kind, c.name, err)
		}
	}
}

func TestDetectDriver_CloudShippersNamePackage(t *testing.T) {
	// The ship driver is keyed on ship.Type: restic needs the restic binary,
	// archive needs rclone. Each missing binary names its Portage package (R7.1).
	cases := []struct {
		name, shipType, pkg string
	}{
		{"restic absent", "restic", "app-backup/restic"},
		{"rclone absent", "archive", "net-misc/rclone"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			stubLookPath(t) // nothing present
			err := detectDriver("ship", tc.shipType)
			if !errors.Is(err, ErrDriverUnavailable) {
				t.Fatalf("err = %v, want ErrDriverUnavailable", err)
			}
			if !strings.Contains(err.Error(), tc.pkg) {
				t.Errorf("error %q does not name the Portage package %q", err, tc.pkg)
			}
		})
	}
}

func TestDetectDriver_SnapperNamesPackage(t *testing.T) {
	// Snapper absent from PATH: the validate-time error is actionable and names
	// the real Gentoo package, app-backup/snapper (R5.1, via the lookPath seam
	// R5.2).
	stubLookPath(t) // nothing present
	err := detectDriver("engine", "snapper")
	if !errors.Is(err, ErrDriverUnavailable) {
		t.Fatalf("err = %v, want ErrDriverUnavailable", err)
	}
	if !strings.Contains(err.Error(), "app-backup/snapper") {
		t.Errorf("error %q does not name the Portage package", err)
	}

	stubLookPath(t, "snapper")
	if err := detectDriver("engine", "snapper"); err != nil {
		t.Errorf("detectDriver(engine, snapper) with snapper on PATH = %v, want nil", err)
	}
}

func TestDetectDriver_UnknownIsNoop(t *testing.T) {
	stubLookPath(t) // nothing present
	if err := detectDriver("engine", "zfs"); err != nil {
		t.Errorf("unknown driver should be a detection no-op, got %v", err)
	}
}
