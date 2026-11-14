// Package buildenv runs the build gates in a disposable Gentoo environment —
// a container today, a virtual machine later — instead of on this host.
//
// The environment is reached through a Transport, which only knows how to
// start one, stop it, and build the command that runs an argv inside it. A
// Session does the rest, the same way for every transport: it ships the staged
// tree and the distfiles in by tar, at the same absolute paths they have here,
// so no argument needs translating; it registers the staged repository; it
// installs the build dependencies; and it starts afresh before the pass whose
// verdict is recorded.
package buildenv

import (
	"context"
	"errors"
	"os/exec"
)

// Transport reaches one disposable environment.
type Transport interface {
	// Name is the backend's name: "container", "vm".
	Name() string
	// Start brings up a fresh environment. A second Start without a Stop is an
	// error of the caller.
	Start(ctx context.Context) error
	// Command returns the command that runs argv inside the running
	// environment, as root, in dir, with env as its whole environment. Its
	// stdin, stdout and stderr are the caller's to set.
	Command(ctx context.Context, argv, env []string, dir string) *exec.Cmd
	// Stop tears the environment down. Stopping one that is not running is not
	// an error.
	Stop(ctx context.Context) error
}

// ErrEnvironment marks a failure of the environment rather than of the bump:
// it could not be started, reached, or prepared. A build that never ran says
// nothing about the ebuild, so the caller reports the bump as waiting.
var ErrEnvironment = errors.New("the build environment failed")
