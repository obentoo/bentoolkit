package snapshot

import (
	"errors"
	"fmt"
	"os/exec"
)

// ErrDriverUnavailable is returned when an active driver's binary is absent from
// PATH. The message names the Portage package to install (R6.1).
var ErrDriverUnavailable = errors.New("snapshot driver dependency missing")

// lookPath is the injectable binary-resolution seam (defaults to exec.LookPath),
// overridable in tests so detection is deterministic regardless of host PATH
// (R6.2), mirroring internal/autoupdate's lookPath seam.
var lookPath = exec.LookPath

// driverDep is the binary a driver needs and the Portage package that provides it.
type driverDep struct {
	binary string
	pkg    string
}

// driverBinary maps a (kind, name) driver to its runtime dependency. The second
// result is false for an unknown driver — enum validation in Config.Validate
// reports those, so detection treats them as "nothing to check".
func driverBinary(kind, name string) (driverDep, bool) {
	switch kind {
	case "engine":
		if name == "btrbk" {
			return driverDep{"btrbk", "app-backup/btrbk"}, true
		}
	case "ship":
		if name == "ssh" {
			return driverDep{"ssh", "net-misc/openssh"}, true
		}
	case "schedule":
		if name == "systemd" {
			return driverDep{"systemctl", "sys-apps/systemd"}, true
		}
	}
	return driverDep{}, false
}

// detectDriver verifies the binary backing the (kind, name) driver is on PATH.
// A missing binary yields an actionable ErrDriverUnavailable naming the Portage
// package; an unknown driver is a no-op here (R6.1).
func detectDriver(kind, name string) error {
	dep, ok := driverBinary(kind, name)
	if !ok {
		return nil
	}
	if _, err := lookPath(dep.binary); err != nil {
		return fmt.Errorf("%w: %s driver %q requires %s on PATH", ErrDriverUnavailable, kind, name, dep.pkg)
	}
	return nil
}
