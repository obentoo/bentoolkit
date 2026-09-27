// Package procgroup stops a child process — and, on Unix, every process that
// child started — when the context it was started under is done (story 054).
//
// # The defect
//
// exec.CommandContext stops only the DIRECT child. A child that forks helpers
// (pkgdev's fetchers, make's compilers, a git transport) hands each of them the
// pipe that cmd.Stdout is copied from, and Wait does not return while any
// descriptor for that pipe is open. A cancelled run therefore lasted as long as
// its longest-lived descendant: a download, a compile, or forever for a stalled
// push. Story 046 measured it on overlay manifest: 30 s to come back from a
// cancel delivered at 300 ms, against a grandchild that slept 30 s.
//
// # The modes
//
// Configure an unstarted command created by exec.CommandContext with exactly
// one of them, then start it:
//
//   - Group: the child leads a new process group. On cancel, SIGTERM goes to
//     the whole group; whatever is still running GracePeriod later gets
//     SIGKILL. For children that never read the terminal.
//   - Foreground: the child stays in the caller's process group, which is the
//     terminal's foreground group, so a password or passphrase prompt keeps
//     working. On cancel, SIGTERM goes to the direct child only; if it is still
//     running GracePeriod later, os/exec SIGKILLs it.
//   - KillGroupNow: overlay manifest's original behaviour — its own group,
//     SIGKILL to the whole group at once, no grace.
//
// Each mode replaces cmd.Cancel. Group and Foreground also set cmd.WaitDelay
// to GracePeriod, so Wait returns within GracePeriod of the cancel even while a
// process that escaped the signals (one that called setsid) still holds the
// output pipe: os/exec then stops waiting for the pipe. Pass Wait's (or Run's)
// error through Result.
//
// # What group mode costs
//
// A child in its own group no longer receives the Ctrl+C typed at the terminal.
// It is stopped only through the caller's context, so every command that starts
// a group-mode child must cancel that context on SIGINT and SIGTERM. A child
// that reads the terminal from a background group is stopped by SIGTTIN, so a
// group-mode child needs a standard input that is not the terminal.
//
// # Outside Unix
//
// There is no process group to signal. Every mode only sets cmd.WaitDelay,
// which stops the WAIT for a descendant but leaves the descendant running.
package procgroup

import (
	"errors"
	"os/exec"
	"time"
)

// GracePeriod is how long a cancelled child and its descendants get between
// SIGTERM and SIGKILL, and how long Wait keeps draining their output after the
// cancel (R1.2, R1.3, R1.4). A caller that bounds a child by a deadline
// therefore returns within that deadline plus GracePeriod.
const GracePeriod = 5 * time.Second

// grace is the period Group and Foreground apply. It is GracePeriod except
// inside this package's own tests, which may shorten it; nothing outside the
// package can.
var grace = GracePeriod

// Result returns the error a caller should act on after Wait (or Run) of a
// command configured by this package: err itself, except that
// exec.ErrWaitDelay after a successful exit is success (R1.5).
//
// WaitDelay's timer also starts when Wait sees the child exit NORMALLY. A child
// that exits 0 while a helper it left behind still holds the output pipe makes
// Wait return exec.ErrWaitDelay once the delay expires: an error for a command
// whose work finished. os/exec returns ErrWaitDelay only after a successful
// exit status, and Result checks that status as well, so a non-zero exit, a
// signal or a cancelled context always stays a failure.
func Result(cmd *exec.Cmd, err error) error {
	if errors.Is(err, exec.ErrWaitDelay) && exitedSuccessfully(cmd) {
		return nil
	}
	return err
}

// exitedSuccessfully reports whether cmd has been waited for and its exit
// status was a success.
func exitedSuccessfully(cmd *exec.Cmd) bool {
	return cmd != nil && cmd.ProcessState != nil && cmd.ProcessState.Success()
}
