package main

import (
	"os/exec"

	"github.com/obentoo/bentoolkit/internal/autoupdate"
)

// cleanBuildLog is a build child that got through compile: the phase markers
// Portage prints, which is what the build gates read as a pass.
const cleanBuildLog = ">>> Source unpacked in /var/tmp/portage/work\n" +
	">>> Preparing source in /var/tmp/portage/work/src ...\n" +
	">>> Source prepared.\n" +
	">>> Configuring source in /var/tmp/portage/work/src ...\n" +
	">>> Source configured.\n" +
	">>> Compiling source in /var/tmp/portage/work/src ...\n" +
	">>> Source compiled.\n"

// cleanBuildOptions make every build gate a fixture runs pass, so a check
// proves the bump and an apply can publish it.
func cleanBuildOptions() []autoupdate.ApplierOption {
	return []autoupdate.ApplierOption{
		autoupdate.WithApplierRunCaptured(func(cmd *exec.Cmd) error {
			_, err := cmd.Stdout.Write([]byte(cleanBuildLog))
			return err
		}),
		autoupdate.WithApplierIsolationProbe(func() (bool, string) { return true, "" }),
	}
}
