//go:build !unix

package procgroup

import (
	"os/exec"
	"time"
)

// Group is the fallback for platforms without process groups. There is
// no group to signal and no portable way to reach a grandchild, so it only sets
// cmd.WaitDelay to GracePeriod: once the context is done, os/exec stops the
// direct child through cmd.Cancel (exec.CommandContext's Process.Kill) and,
// GracePeriod later, stops waiting for any descendant still holding the output
// pipe. That descendant keeps running.
func Group(cmd *exec.Cmd) {
	cmd.WaitDelay = grace
}

// Foreground is the fallback for platforms without process groups: as
// Group does here, it only sets cmd.WaitDelay to GracePeriod.
func Foreground(cmd *exec.Cmd) {
	cmd.WaitDelay = grace
}

// manifestWaitDelay bounds how long cmd.Wait may block draining I/O after a
// cancelled run's child has been killed, on the platforms that cannot kill a
// process group.
//
// It is generous on purpose. WaitDelay's timer also starts when the child exits
// NORMALLY, so a short delay would turn a successful pkgdev whose helper lingers
// a moment into a target reported as failed — a failure invented by the
// reporting path, exactly the kind of false report this package prevents. Ten
// seconds is long enough that only a genuinely stuck descendant reaches it, and
// short enough that an interrupt still returns while the operator is watching.
const manifestWaitDelay = 10 * time.Second

// KillGroupNow is the non-Unix substitute for overlay manifest's group kill,
// and it is deliberately weaker than the Unix one — see the Unix
// KillGroupNow for what is being worked around and why the group kill is the
// answer there.
//
// syscall.SysProcAttr has no Setpgid field outside Unix, so there is no group to
// signal and no portable way to reach a grandchild at all. WaitDelay is what
// remains: it does not kill the descendant, it stops WAITING for it, closing the
// pipes so Wait returns. The orphan keeps running.
//
// That trade is accepted here rather than hidden, for one reason: pkgdev is a
// Gentoo tool (dev-util/pkgdev) and overlay manifest's own discovery step
// refuses the run without it, so this fallback is compiled for completeness
// rather than for a host that regenerates Manifests. A prompt, honest report
// with a leaked helper beats a run that cannot return at all.
func KillGroupNow(cmd *exec.Cmd) {
	cmd.WaitDelay = manifestWaitDelay
}
